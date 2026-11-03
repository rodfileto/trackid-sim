"""C0/C1: other images of the same person, with Qwen-Image-Edit.

`spike_qwen.py` proved one variant end to end and is kept as the record of that
measurement. This is the batch driver the remaining C0 item needs -- "drop
variants where the edit changed the person; report the drop rate" -- and the
generation half of C1.

What a variant is for: a degraded copy of a base face shares identity with it by
construction, so scoring that pair measures the recognizer, not the generator.
A variant is a genuinely different photograph of the person. The corpus needs
them because a real case compares an enrollment photo against a *different*
capture, never against a filtered copy of itself.

Placement (resident vs streamed, encoder on GPU vs CPU, bf16 vs fp32) is decided
by hw.py from the device, not hard-coded. On the local RTX 2080 that reproduces
the spike's configuration; on a rented Ampere-or-newer card it drops the
guardrails that card does not need. The profile is recorded in the manifest.

Deterministic and resumable, like gen_pool.py: seeds derive from --seed, an
image already on disk with a matching manifest entry is skipped, and every
image records its prompt, seed, model revision and sha256.

Local (streamed, fp32 -- slow; measured 26.6 min/image):

    systemd-run --user --scope -p MemoryMax=45G -p MemorySwapMax=0 \
        uv run python gen_variants.py

Rented pod (see runpod/README.md):

    uv run python gen_variants.py --count 30
"""
import argparse, gc, hashlib, json, random, time
from pathlib import Path

import torch
from diffusers import GGUFQuantizationConfig, QwenImageEditPipeline, QwenImageTransformer2DModel
from PIL import Image

import hw

REPO = "Qwen/Qwen-Image-Edit"
GGUF_REPO = "QuantStack/Qwen-Image-Edit-GGUF"
GGUF_FILE = "Qwen_Image_Edit-Q4_K_M.gguf"
# Q4_K_M everywhere, local and rented, even where a bigger card could hold Q8 or
# the 38 GB bf16 weights. The recorded gate result (+0.765) was measured on
# Q4_K_M; changing weights with the hardware would make pod output and local
# output different experiments. Upgrading is a deliberate, recorded choice.
WEIGHTS_GB = 12.0
ENCODER_GB = 16.0            # Qwen2.5-VL-7B in bf16
NEGATIVE = " "               # Qwen's own default negative prompt

POOL = Path("pool_manifest.json")
MANIFEST = Path("variant_manifest.json")
OUT = Path("spike_out/variants")
EMBEDS = Path("spike_out/embeds")

# One or more re-encounters per base face: full-length register photographs,
# each with its own expression and head angle.
#
# The base face is a passport-style head-and-shoulders crop, and asking an edit
# model for another head-and-shoulders portrait is nearly an identity
# transform -- so it returns one. Measured twice on the first pool: +0.937 and
# +0.932, both re-renders of the input. A full-length reframe cannot be
# satisfied that way, and 20 of 20 came back as genuine new photographs.
#
# Expression and angle are the second half of the problem. The first 20
# register shots were all frontal and neutral, which makes the matching task
# unrealistically easy: a real questioned image catches whatever face the
# person was making, from whatever angle the camera happened to be at. These
# are the conditions under which a recognizer actually has to work, so the
# corpus has to contain them -- and the identity check will say which of them
# the generator can hold identity through.
#
# Wording order is load-bearing and follows the spike: the edit instruction
# leads, the preservation clause trails.
REGISTER_SIZE = (832, 1216)   # ~2:3 portrait, ~1.0 MP: Qwen's trained range,
                              # and a standing figure needs the vertical frame.

# Phrased as what the face is *doing*, not as a label attached to it: "scowling
# angrily" moves a diffusion model where "an angry expression" does not, the
# same way a tone descriptor beats an ethnonym in the base-face prompts.
# One primary transformation per variant, because the model obeys the first
# instruction and drops the ones competing with it. Measured on 5 variants
# asking for an expression *and* an angle: the reframe always happened, the
# expression happened once clearly, and the angle never happened at all -- it
# was third in line. The original spike, whose prompt *led* with "Turn the head
# to a three-quarter view", got exactly that.
#
# So half the pool leads with an expression and half leads with an angle, each
# as an imperative at the front. That also isolates the two factors instead of
# confounding them: a drop rate for "angry" that is really "angry and turned
# 30 degrees" would support no claim about either.
#
# `kind` is the condition, so facescore reports the drop rate per condition --
# which is the question worth asking. Neutral and frontal are excluded: as a
# primary transformation they are no-ops, and they are what the other half of
# the pool already holds fixed.
REGISTER_SIZE = (832, 1216)   # ~2:3 portrait, ~1.0 MP: Qwen's trained range,
                              # and a standing figure needs the vertical frame.

# Phrased as what the face is *doing*, not as a label attached to it, the same
# way a tone descriptor beats an ethnonym in the base-face prompts.
EXPRESSIONS = [
    ("smiling",      "Make the person smile broadly, teeth showing, eyes crinkled"),
    ("angry",        "Make the person scowl angrily, brows drawn down and "
                     "together, jaw clenched"),
    ("disappointed", "Make the person frown with disappointment, mouth turned "
                     "down, eyes cast down"),
    ("surprised",    "Make the person look startled, eyes wide open, eyebrows "
                     "raised high"),
    ("tired",        "Make the person look exhausted, eyelids heavy and half "
                     "closed"),
]

ANGLES = [
    ("threeq_l",  "Turn the head well to the left so the face is seen in "
                  "three-quarter view"),
    ("threeq_r",  "Turn the head well to the right so the face is seen in "
                  "three-quarter view"),
    ("chin_down", "Tilt the head downward so the face is angled toward the "
                  "floor, eyes toward the camera"),
    ("chin_up",   "Lift the chin so the face is angled upward"),
]

# The lead instruction is substituted in; everything after it is the register
# framing, which survives because it is structural rather than competing.
REGISTER = ("{lead}, in a full-length standing photograph of the same person "
            "showing the whole body from head to feet, wearing completely "
            "different everyday clothing, plain light grey studio wall behind, "
            "arms relaxed at the sides, even lighting, "
            "keep the same person, same hair, photographic")


def embed_path(job):
    """Cache file for one job's prompt embedding.

    Keyed on the prompt text, not just the job id: the embedding *is* the
    encoded prompt, so reusing it after the prompt changed would silently
    generate the old prompt while the manifest recorded the new one. That is
    the one failure a provenance manifest must never have.
    """
    h = hashlib.sha256(job["prompt"].encode()).hexdigest()[:8]
    return EMBEDS / f"{job['id']}-{h}.pt"


def to(t, device, dtype=None):
    """Move a tensor that may be None (see the mask note in encode)."""
    if t is None:
        return None
    return t.to(device, dtype) if dtype is not None else t.to(device)


def build_jobs(faces, per_face, seed):
    """The work list. Pure function of (faces, per_face, seed).

    Faces alternate between an expression-led and an angle-led variant, so the
    two halves are exactly balanced rather than balanced in expectation.
    """
    jobs = []
    for i, f in enumerate(faces):
        rng = random.Random(f"{seed}/{f['id']}")
        # Alternate by position so any prefix of the pool is balanced too --
        # a 5-face or 20-face trial gets both kinds, not five of one.
        modes = ["expression", "angle"] if i % 2 == 0 else ["angle", "expression"]
        for mode in modes[:per_face]:
            pool = EXPRESSIONS if mode == "expression" else ANGLES
            key, lead = rng.choice(pool)
            jobs.append({"id": f"{f['id']}_{key}", "base_id": f["id"], "kind": key,
                         "mode": mode, "base_file": f["file"],
                         "prompt": REGISTER.format(lead=lead),
                         "width": REGISTER_SIZE[0], "height": REGISTER_SIZE[1],
                         "seed": rng.randrange(2**31)})
    return jobs


def encode(jobs, profile, plan, dtype):
    """Stage 1: Qwen2.5-VL reads each (prompt, image) pair.

    The encoder is a 7B vision-language model, not a small T5, so it is a second
    heavyweight and has to be gone before the transformer arrives. Embeds spill
    to disk rather than being held: 90 pairs is ~1.7 GB of RAM otherwise, and a
    kill part-way through then costs only the encodes not yet done.
    """
    EMBEDS.mkdir(parents=True, exist_ok=True)
    pending = [j for j in jobs if not embed_path(j).exists()]
    if not pending:
        print(f"embeds: all {len(jobs)} cached", flush=True)
        return
    dev = plan.encoder_device
    t0 = time.time()
    enc = QwenImageEditPipeline.from_pretrained(
        REPO, transformer=None, vae=None,
        torch_dtype=dtype if dev == "cuda" else torch.float32)
    if dev == "cuda":
        enc.text_encoder.to("cuda")
    print(f"encoding {len(pending)} prompts on {dev}", flush=True)
    for n, j in enumerate(pending, 1):
        src = Image.open(j["base_file"]).convert("RGB")
        with torch.no_grad():
            pe, pmask = enc.encode_prompt(prompt=[j["prompt"]], image=src, device=dev)
            # true_cfg_scale > 1 needs the negative encoded here too: there is
            # no text encoder left in stage 2 to do it.
            ne, nmask = enc.encode_prompt(prompt=[NEGATIVE], image=src, device=dev)
        # encode_prompt returns None for an all-ones mask -- it drops a mask
        # that masks nothing, and the negative prompt " " has no padding. The
        # pipeline accepts None and warns that every token is valid, which is
        # correct, so carry the None through rather than inventing a mask.
        torch.save({"pe": to(pe, "cpu", dtype), "pmask": to(pmask, "cpu"),
                    "ne": to(ne, "cpu", dtype), "nmask": to(nmask, "cpu")},
                   embed_path(j))
        print(f"  [{n}/{len(pending)}] {j['id']}", flush=True)
    del enc
    gc.collect()
    torch.cuda.empty_cache()
    print(f"encode: {time.time() - t0:.0f}s for {len(pending)} prompts ({dev})", flush=True)


def build_pipe(profile, plan, dtype):
    """Stage 2: the transformer, placed as the card allows."""
    t = time.time()
    tf = QwenImageTransformer2DModel.from_single_file(
        f"https://huggingface.co/{GGUF_REPO}/blob/main/{GGUF_FILE}",
        quantization_config=GGUFQuantizationConfig(compute_dtype=dtype),
        torch_dtype=dtype, config=REPO, subfolder="transformer")
    pipe = QwenImageEditPipeline.from_pretrained(
        REPO, transformer=tf, text_encoder=None, tokenizer=None, processor=None,
        torch_dtype=dtype)
    if plan.resident:
        pipe.to("cuda")
    else:
        # Streaming exists to fit, not to go fast: it overlaps the copy of the
        # next leaf module with compute on the current one, which made FLUX
        # 5.8x faster at the same VRAM. It also pins ~24 GB of host memory.
        pipe.transformer.enable_group_offload(
            onload_device=torch.device("cuda"), offload_device=torch.device("cpu"),
            offload_type="leaf_level", use_stream=True)
        pipe.vae.to("cuda")
    # In fp16 the latents stay finite through the transformer but the VAE
    # overflows, decoding to a solid black frame with no exception raised. The
    # VAE is only 0.24 GB, so it runs in fp32 whenever compute is fp16.
    vae_dtype = torch.float32 if dtype == torch.float16 else dtype
    pipe.vae.to("cuda", dtype=vae_dtype)
    if plan.usable_gb < 12:
        pipe.vae.enable_tiling()
    if vae_dtype != dtype:
        # Only the encode boundary needs help: the pipeline already casts
        # latents to vae.dtype before decode, but it feeds the source image in
        # pipeline dtype and concatenates the result with compute-dtype latents.
        inner = pipe._encode_vae_image
        pipe._encode_vae_image = (
            lambda image, generator: inner(image.to(vae_dtype), generator).to(dtype))
    print(f"load transformer: {time.time() - t:.0f}s, "
          f"{'resident' if plan.resident else 'streamed'}, "
          f"{torch.cuda.memory_allocated() / 2**30:.2f} GB on card", flush=True)
    return pipe


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--count", type=int, default=0, help="faces to cover (0 = all in the pool)")
    ap.add_argument("--per-face", type=int, default=1, choices=(1, 2),
                    help="1 = one variant per face (half expression-led, half "
                         "angle-led); 2 = one of each per face")
    ap.add_argument("--steps", type=int, default=30)
    ap.add_argument("--cfg", type=float, default=4.0)
    ap.add_argument("--seed", type=int, default=20260920)
    ap.add_argument("--encode-only", action="store_true",
                    help="stage 1 only; the transformer never loads")
    args = ap.parse_args()

    profile = hw.detect().apply()
    plan = profile.plan(WEIGHTS_GB, encoder_gb=ENCODER_GB)
    dtype = profile.dtype
    print(profile.describe(), flush=True)
    print(f"  transformer {'resident' if plan.resident else 'streamed'}, "
          f"encoder on {plan.encoder_device}, compute "
          f"{str(dtype).removeprefix('torch.')}", flush=True)
    if not profile.bf16:
        print("  !! no bf16: Qwen runs in fp32 at ~53 s/step. This is the case "
              "for renting -- see runpod/README.md", flush=True)

    pool = json.loads(POOL.read_text())
    faces = pool["faces"][:args.count] if args.count else pool["faces"]
    jobs = build_jobs(faces, args.per_face, args.seed)

    done, carried = {}, {}
    if MANIFEST.exists():
        prev = json.loads(MANIFEST.read_text())
        by_id = {j["id"]: j for j in jobs}
        # Resume only on an entry that matches the work we are about to do:
        # same seed *and* same prompt. A prompt edit must regenerate, or the
        # manifest would describe images it did not produce.
        done = {v["id"]: v for v in prev.get("variants", [])
                if Path(v["file"]).exists() and v.get("prompt_seed") == args.seed
                and v["id"] in by_id and v.get("prompt") == by_id[v["id"]]["prompt"]}
        # Everything else whose image is still on disk is *carried*, not
        # resumed: it will not be regenerated, but it keeps its manifest entry.
        # Rebuilding the manifest from the current job list alone silently
        # stripped provenance from 20 images that were still there -- and a
        # manifest that does not describe what is on disk is worse than none,
        # because the corpus ships it as the record.
        carried = {v["id"]: v for v in prev.get("variants", [])
                   if Path(v["file"]).exists() and v["id"] not in done}
        if carried:
            print(f"carrying {len(carried)} manifest entries from earlier runs "
                  f"(images on disk, not regenerated)", flush=True)
    todo = [j for j in jobs if j["id"] not in done]
    print(f"{len(done)} already done, {len(todo)} to generate", flush=True)
    if not todo:
        return

    encode(todo, profile, plan, dtype)
    if args.encode_only:
        return

    OUT.mkdir(parents=True, exist_ok=True)
    pipe = build_pipe(profile, plan, dtype)
    rows, t_all = list(done.values()) + list(carried.values()), time.time()
    for n, j in enumerate(todo, 1):
        e = torch.load(embed_path(j), weights_only=True)
        src = Image.open(j["base_file"]).convert("RGB")
        t = time.time()
        img = pipe(image=src,
                   prompt_embeds=to(e["pe"], "cuda", dtype),
                   prompt_embeds_mask=to(e["pmask"], "cuda"),
                   negative_prompt_embeds=to(e["ne"], "cuda", dtype),
                   negative_prompt_embeds_mask=to(e["nmask"], "cuda"),
                   true_cfg_scale=args.cfg, num_inference_steps=args.steps,
                   # Explicit, because the pipeline otherwise derives the output
                   # size from the square input and a standing figure does not
                   # fit a square frame.
                   width=j["width"], height=j["height"],
                   generator=torch.Generator("cpu").manual_seed(j["seed"])).images[0]
        dt = time.time() - t
        path = OUT / f"{j['id']}.png"
        img.save(path)
        rows.append({**j, "prompt_seed": args.seed, "file": str(path),
                     "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                     "seconds": round(dt, 1), "size": img.size,
                     # fp16/fp32 failures on Turing are flat frames, not
                     # exceptions, so keep a cheap per-image sanity check.
                     "luma_range": img.convert("L").getextrema()})
        MANIFEST.write_text(json.dumps(
            {"model": REPO, "weights": GGUF_FILE, "steps": args.steps,
             "true_cfg_scale": args.cfg, "prompt_seed": args.seed,
             "profile": profile.as_dict(), "resident": plan.resident,
             "variants": sorted(rows, key=lambda v: v["id"])}, indent=1))
        eta = (time.time() - t_all) / n * (len(todo) - n)
        print(f"[{n}/{len(todo)}] {j['id']} {dt:.1f}s  eta {eta / 60:.0f}m", flush=True)
    print(f"done: {len(todo)} variants in {(time.time() - t_all) / 60:.1f} min", flush=True)


if __name__ == "__main__":
    main()
