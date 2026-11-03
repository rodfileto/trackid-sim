"""C0 spike, step 4: can Qwen-Image-Edit make a *new* image of the same person?

This is the real C0 gate. The degradation check (degrade.py + facescore) showed
the recognizer survives quality loss, but those genuine pairs come from the same
source file, so identity is shared by construction. A variant from an edit model
is a genuinely different image, and whether identity survives it is what decides
the face layer.

Fitting 20B on an 8 GB card that also drives a display, in 60 GB of RAM:

  * the transformer is pre-quantized GGUF (Q4_K_M, 12 GB against 38 GB fp16).
    bitsandbytes is not an option -- it quantizes on the GPU and blows the cap.
  * the text encoder is Qwen2.5-VL-7B, a vision-language model that reads the
    input image, so it is a second heavyweight rather than a small T5. It runs
    on the CPU in stage 1 and is freed before the transformer loads.
  * the transformer is then streamed to the card per leaf module, which is what
    made FLUX 5.8x faster at the same VRAM.

true_cfg_scale defaults to 4.0, so the negative prompt must be encoded in
stage 1 as well -- there is no text encoder left in stage 2 to do it.

Run it bounded, as ever (see README):

    systemd-run --user --scope -p MemoryMax=45G -p MemorySwapMax=0 \
        uv run python spike_qwen.py
"""
import argparse, gc, json, time
from pathlib import Path

import torch
from diffusers import GGUFQuantizationConfig, QwenImageEditPipeline, QwenImageTransformer2DModel
from PIL import Image

REPO = "Qwen/Qwen-Image-Edit"
GGUF_REPO = "QuantStack/Qwen-Image-Edit-GGUF"
GGUF_FILE = "Qwen_Image_Edit-Q4_K_M.gguf"
NEGATIVE = " "  # Qwen's own default negative prompt

# Note on the "negative_prompt_embeds_mask is not provided" warning: harmless.
# encode_prompt returns None for an all-ones mask (it drops a mask that masks
# nothing), and " " has no padding, so the pipeline warns that it will treat
# every negative token as valid -- which is correct, they are.


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--face", default="spike_out/pool/face_000.png")
    ap.add_argument("--prompt", default=(
        "Turn the head to a three-quarter view looking slightly to her left, "
        "keep the same person, same face, same hair, same clothing, "
        "photographic, same lighting"))
    ap.add_argument("--steps", type=int, default=50)
    ap.add_argument("--cfg", type=float, default=4.0)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--budget", type=float, default=0.72)
    ap.add_argument("--compute", choices=["fp16", "fp32"], default="fp16",
                    help="GGUF dequantization dtype; fp32 is the no-bf16 fallback")
    ap.add_argument("--out", type=Path, default=Path("samples/qwen_variant.png"))
    args = ap.parse_args()

    torch.cuda.set_per_process_memory_fraction(args.budget)
    src = Image.open(args.face).convert("RGB")
    print(f"source {args.face} {src.size}", flush=True)

    # Stage 1: Qwen2.5-VL encodes (prompt, image) on the CPU.
    t0 = time.time()
    enc = QwenImageEditPipeline.from_pretrained(REPO, transformer=None, vae=None,
                                                torch_dtype=torch.float32)
    with torch.no_grad():
        pe, pmask = enc.encode_prompt(prompt=[args.prompt], image=src, device="cpu")
        ne, nmask = enc.encode_prompt(prompt=[NEGATIVE], image=src, device="cpu")
    keep = torch.float16 if args.compute == "fp16" else torch.float32
    pe, ne = pe.to(keep), ne.to(keep)
    del enc
    gc.collect()
    print(f"encode: {time.time() - t0:.0f}s (cpu), embeds {tuple(pe.shape)}", flush=True)

    # Stage 2: GGUF transformer, streamed.
    compute = torch.float16 if args.compute == "fp16" else torch.float32
    t1 = time.time()
    tf = QwenImageTransformer2DModel.from_single_file(
        f"https://huggingface.co/{GGUF_REPO}/blob/main/{GGUF_FILE}",
        quantization_config=GGUFQuantizationConfig(compute_dtype=compute),
        torch_dtype=compute, config=REPO, subfolder="transformer")
    pipe = QwenImageEditPipeline.from_pretrained(REPO, transformer=tf, text_encoder=None,
                                                 tokenizer=None, processor=None,
                                                 torch_dtype=compute)
    pipe.transformer.enable_group_offload(onload_device=torch.device("cuda"),
                                          offload_device=torch.device("cpu"),
                                          offload_type="leaf_level", use_stream=True)
    # This card has no bf16 and Qwen was trained in bf16. In fp16 the latents
    # stay finite through the transformer but the VAE overflows, decoding to a
    # solid black frame with no exception raised. The VAE is only 0.24 GB, so
    # it runs in fp32 regardless of --compute.
    pipe.vae.to("cuda", dtype=torch.float32); pipe.vae.enable_tiling()
    if pipe.vae.dtype != compute:
        # Only the encode boundary needs help: the pipeline already casts
        # latents to vae.dtype before decode, but it feeds the source image in
        # pipeline dtype and then concatenates the result with fp16 latents.
        _encode_vae_image = pipe._encode_vae_image
        pipe._encode_vae_image = (
            lambda image, generator: _encode_vae_image(
                image.to(pipe.vae.dtype), generator).to(compute))
    print(f"load transformer: {time.time() - t1:.0f}s, resident "
          f"{torch.cuda.memory_allocated() / 2**30:.2f} GB", flush=True)

    # Watch the latents so a failure names its stage: NaN here means the
    # transformer overflowed; clean latents plus a black image means the VAE.
    bad_step = []

    def watch(p, i, t, kw):
        lat = kw.get("latents")
        if lat is not None and not bad_step and not torch.isfinite(lat).all():
            bad_step.append(i)
            print(f"!! non-finite latents first at step {i}", flush=True)
        return kw

    t2 = time.time()
    res = pipe(image=src, prompt_embeds=pe, prompt_embeds_mask=pmask,
               negative_prompt_embeds=ne, negative_prompt_embeds_mask=nmask,
               true_cfg_scale=args.cfg, num_inference_steps=args.steps,
               generator=torch.Generator("cpu").manual_seed(args.seed),
               callback_on_step_end=watch).images
    out = res[0]
    dt = time.time() - t2
    if bad_step:
        print(f"FAIL: transformer produced NaN/Inf at step {bad_step[0]} "
              f"-- fp16 compute overflowed, retry with --compute fp32", flush=True)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    out.save(args.out)
    # Side by side, for the manual look that decides this gate.
    pair = Image.new("RGB", (src.width + out.width, max(src.height, out.height)), "white")
    pair.paste(src, (0, 0)); pair.paste(out, (src.width, 0))
    pair_path = args.out.with_name(args.out.stem + "_vs_base.png")
    pair.save(pair_path)

    info = {"compute": args.compute, "nan_step": bad_step[0] if bad_step else None,
            "source": args.face, "prompt": args.prompt, "steps": args.steps,
            "true_cfg_scale": args.cfg, "seed": args.seed, "size": out.size,
            "seconds": round(dt, 1), "seconds_per_step": round(dt / args.steps, 2),
            "peak_vram_gb": round(torch.cuda.max_memory_allocated() / 2**30, 2),
            "luma_range": out.convert("L").getextrema(),
            "out": str(args.out), "comparison": str(pair_path)}
    Path("spike_out/qwen_spike.json").write_text(json.dumps(info, indent=1))
    print(json.dumps(info, indent=1), flush=True)
    print("done", flush=True)


if __name__ == "__main__":
    main()
