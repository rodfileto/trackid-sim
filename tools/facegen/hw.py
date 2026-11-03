"""What this machine can do, and what that implies for the pipelines.

Every script here was first written against one card: an RTX 2080 with 8 GB, no
bf16, driving the display. The guardrails that card needs (cap the VRAM, stream
the weights, encode prompts on the CPU, fall back to fp32) are all wrong on a
rented GPU -- they cost speed to protect resources the pod does not have to
protect. Rather than a --rented flag threaded through every script, the
decisions are made once, here, from what the device reports.

    profile = detect()
    plan = profile.plan(weights_gb=12.0)      # Qwen's Q4_K_M transformer
    if plan.resident: pipe.to("cuda")
    else:             pipe.transformer.enable_group_offload(...)

The measured facts behind the rules (see README.md and ROADMAP C0):

  * Streaming is 5.8x faster than plain offload at the same VRAM, but resident
    weights are faster still: streaming exists to fit, not to go fast.
  * Qwen was trained in bf16. On a card without it, fp16 decodes to a solid
    black frame (the VAE overflows, no exception) and fp32 costs 53 s/step.
  * Pinned host memory is not reclaimed on del, so one model per process.
    That holds on the pod too -- it is a property of pinning, not of the card.

Override the detection with FACEGEN_PROFILE=local|rented|auto (default auto)
and FACEGEN_DISPLAY=0|1. A run records the profile it used in its manifest, so
a pod result and a local result stay distinguishable after the fact.
"""
import os
import subprocess
from dataclasses import dataclass, asdict

import torch

# Headroom over the weights: activations, the VAE, the allocator's own slack.
# 1024x1024 with tiled VAE decode peaked at 2 GB in the local spike; 4 gives
# room for a bigger batch without re-measuring.
HEADROOM_GB = 4.0


@dataclass(frozen=True)
class Plan:
    """How to place one model, given its size."""
    resident: bool          # weights live on the card, vs streamed per leaf module
    encoder_on_gpu: bool    # the text encoder fits too (in its own stage)
    usable_gb: float

    @property
    def encoder_device(self):
        return "cuda" if self.encoder_on_gpu else "cpu"


@dataclass(frozen=True)
class Profile:
    kind: str               # "local" (shared with a desktop) or "rented"
    name: str
    vram_gb: float
    bf16: bool
    drives_display: bool
    memory_fraction: float | None   # None = the whole card is ours
    gpus: int
    sm: tuple               # compute capability (major, minor)

    @property
    def gguf_fast_path(self):
        """Can this card hold GGUF weights quantized, or will it expand them?

        Measured, 2026-09-20: on sm_89 (RTX 4090) the Q4_K_M transformer loads
        in 67 s and sits on the card at 12.47 GB, still quantized. On sm_120
        (RTX 5090) the same code fell back to dequantizing on the CPU -- one
        core pinned, host RSS climbing past 25 GB against a 12 GB file, GPU
        idle, no end in sight after 20 minutes. Expanded to bf16 those weights
        are ~40 GB and fit no consumer card.

        The card reports bf16 support and passes every other check, so nothing
        else catches this. Blackwell is excluded until a torch/diffusers
        release is shown to fix it -- re-measure before lifting.
        """
        return self.sm[0] < 12

    @property
    def dtype(self):
        """Compute dtype. fp32 is the no-bf16 fallback, not a choice."""
        return torch.bfloat16 if self.bf16 else torch.float32

    @property
    def flux_dtype(self):
        """FLUX is fine in fp16 on Turing -- measured, 30 faces, no NaN."""
        return torch.bfloat16 if self.bf16 else torch.float16

    def plan(self, weights_gb, encoder_gb=16.0, headroom_gb=HEADROOM_GB):
        usable = self.vram_gb * (self.memory_fraction or 0.95)
        return Plan(resident=usable >= weights_gb + headroom_gb,
                    encoder_on_gpu=usable >= encoder_gb + headroom_gb,
                    usable_gb=round(usable, 1))

    def apply(self):
        """Install the guardrails this machine needs. Call before allocating."""
        if self.memory_fraction is not None:
            # Without the cap, torch expands into the memory X needs and the
            # machine freezes hard -- no traceback, no OOM killer, power button.
            torch.cuda.set_per_process_memory_fraction(self.memory_fraction)
        return self

    def describe(self):
        cap = f"capped at {self.memory_fraction:.0%}" if self.memory_fraction else "whole card"
        return (f"{self.kind}: {self.name} sm_{self.sm[0]}{self.sm[1]}, "
                f"{self.vram_gb:.0f} GB ({cap}), "
                f"bf16 {'yes' if self.bf16 else 'NO -- fp32 fallback'}"
                + ("" if self.gguf_fast_path else ", GGUF FAST PATH UNAVAILABLE")
                + (f", {self.gpus} GPUs (using 0)" if self.gpus > 1 else ""))

    def as_dict(self):
        return {**asdict(self), "dtype": str(self.dtype).removeprefix("torch.")}


def drives_display():
    """Is this card also painting a desktop? Then it needs headroom left free.

    Explicit override first; then the container marker, since a pod image has
    no desktop; then whether anything is actually drawing on the card.
    """
    env = os.environ.get("FACEGEN_DISPLAY")
    if env is not None:
        return env not in ("0", "", "false")
    if os.path.exists("/.dockerenv"):
        return False
    try:
        # Processes with an X/Wayland compositor among them mean a live desktop.
        out = subprocess.run(["nvidia-smi", "--query-compute-apps=process_name",
                              "--format=csv,noheader"], capture_output=True,
                             text=True, timeout=10).stdout
        if any(k in out for k in ("Xorg", "Xwayland", "gnome-shell", "kwin")):
            return True
    except (OSError, subprocess.SubprocessError):
        pass
    return bool(os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY"))


def detect():
    if not torch.cuda.is_available():
        # Distinguish "no GPU" from "GPU present, driver too old", which is
        # the likelier failure on a rented pod and which torch reports only as
        # a warning before claiming there is no device at all. The lockfile's
        # torch is a cu130 build; RunPod's CUDA selector picks the host driver,
        # and a 12.8 driver cannot initialise it. Selecting "CUDA 12.8" there
        # is what produced this, so name the fix rather than the symptom.
        built = torch.version.cuda
        driver = "unknown"
        try:
            driver = subprocess.run(
                ["nvidia-smi", "--query-gpu=driver_version", "--format=csv,noheader"],
                capture_output=True, text=True, timeout=10).stdout.strip().splitlines()[0]
        except (OSError, subprocess.SubprocessError, IndexError):
            pass
        raise SystemExit(
            f"no usable CUDA device. torch {torch.__version__} is built for "
            f"CUDA {built}; the host driver is {driver}. If a GPU is present, "
            f"the driver is too old for this torch -- redeploy the pod "
            f"selecting CUDA {built} or newer.")
    props = torch.cuda.get_device_properties(0)
    vram_gb = props.total_memory / 2**30
    display = drives_display()
    forced = os.environ.get("FACEGEN_PROFILE", "auto")
    kind = forced if forced in ("local", "rented") else ("local" if display else "rented")
    return Profile(
        kind=kind,
        name=props.name,
        vram_gb=vram_gb,
        # NOT torch.cuda.is_bf16_supported(): it counts emulation and returns
        # True on this Turing card, which then produced NaN at step 0 and a
        # black VAE decode. Native bf16 tensor cores start at Ampere (sm_80).
        bf16=props.major >= 8,
        drives_display=display,
        # 0.72 of 7.6 GB leaves the ~1 GB the desktop holds, plus slack. A pod
        # has nothing to leave room for.
        memory_fraction=0.72 if display else None,
        gpus=torch.cuda.device_count(),
        sm=(props.major, props.minor),
    )


if __name__ == "__main__":
    p = detect()
    print(p.describe())
    for label, gb in (("Qwen-Image-Edit Q4_K_M", 12.0), ("FLUX.1-schnell fp16", 24.0)):
        plan = p.plan(gb)
        print(f"  {label:24s} {gb:5.1f} GB -> "
              f"{'resident' if plan.resident else 'streamed'}, "
              f"encoder on {plan.encoder_device}, usable {plan.usable_gb} GB")
