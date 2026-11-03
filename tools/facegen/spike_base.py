"""C0 spike, step 1: FLUX.1-schnell base faces on an 8 GB Turing GPU.

The GPU also drives the display, so torch is capped well short of the card and
the 12B transformer is never resident whole. Two stages, neither of which lets
bitsandbytes quantize on the GPU (its allocator warmup blows the cap):

  1. T5 encodes the prompts on the CPU. A handful of prompts is cheap there and
     it leaves the whole card free for stage 2.
  2. The transformer stays in system RAM and diffusers streams it to the GPU a
     leaf module at a time. Slow (every layer crosses PCIe on every step) but it
     fits, and the throughput number is the point of the spike.
"""
import argparse, gc, json, time
import torch
from diffusers import FluxPipeline, FluxTransformer2DModel
from transformers import T5EncoderModel

MODEL = "black-forest-labs/FLUX.1-schnell"
PROMPT = ("Passport-style frontal photograph of a {age} year old {sex}, neutral expression, "
          "plain light grey background, even studio lighting, sharp focus, photorealistic")
CASES = [(28, "woman"), (54, "man"), (19, "woman"), (41, "man"), (67, "woman"), (33, "man")]

ap = argparse.ArgumentParser()
ap.add_argument("-n", type=int, default=2)
ap.add_argument("--size", type=int, default=512)
ap.add_argument("--steps", type=int, default=4)
ap.add_argument("--budget", type=float, default=0.72, help="fraction of VRAM torch may use")
args = ap.parse_args()

# Leave the rest of the card to the display server: a failure must surface as a
# Python OutOfMemoryError, not as a frozen desktop.
torch.cuda.set_per_process_memory_fraction(args.budget)
total = torch.cuda.get_device_properties(0).total_memory / 2**30
print(f"vram budget: {args.budget * total:.1f} of {total:.1f} GB", flush=True)

cases = CASES[: args.n]
prompts = [PROMPT.format(age=a, sex=s) for a, s in cases]

# Stage 1: prompt encoding, entirely on the CPU.
t0 = time.time()
t5 = T5EncoderModel.from_pretrained(MODEL, subfolder="text_encoder_2", torch_dtype=torch.float32)
enc = FluxPipeline.from_pretrained(MODEL, transformer=None, vae=None, text_encoder_2=t5,
                                   torch_dtype=torch.float32)
embeds = []
with torch.no_grad():
    for p in prompts:
        pe, ppe, _ = enc.encode_prompt(p, prompt_2=None, max_sequence_length=256, device="cpu")
        embeds.append((pe.to(torch.float16), ppe.to(torch.float16)))
del enc, t5
gc.collect()
print(f"encode: {time.time() - t0:.0f}s for {len(prompts)} prompts (cpu)", flush=True)

# Stage 2: denoising. Transformer lives in RAM, streamed to the GPU per leaf module.
t1 = time.time()
tf = FluxTransformer2DModel.from_pretrained(MODEL, subfolder="transformer", torch_dtype=torch.float16)
pipe = FluxPipeline.from_pretrained(MODEL, transformer=tf, text_encoder=None, text_encoder_2=None,
                                    tokenizer=None, tokenizer_2=None, torch_dtype=torch.float16)
# use_stream overlaps the copy of the next leaf module with compute on the
# current one: 9.2 s/image against 53.6 s without it, same VRAM, same output.
# It pins host memory, so keep to one transformer per process (see README).
pipe.transformer.enable_group_offload(
    onload_device=torch.device("cuda"), offload_device=torch.device("cpu"),
    offload_type="leaf_level", use_stream=True)
pipe.vae.to("cuda"); pipe.vae.enable_tiling()
print(f"load transformer: {time.time() - t1:.0f}s, resident "
      f"{torch.cuda.memory_allocated() / 2**30:.2f} GB", flush=True)

log = []
for i, ((age, sex), (pe, ppe)) in enumerate(zip(cases, embeds)):
    t = time.time()
    g = torch.Generator("cpu").manual_seed(1000 + i)
    img = pipe(prompt_embeds=pe.to("cuda"), pooled_prompt_embeds=ppe.to("cuda"),
               height=args.size, width=args.size, num_inference_steps=args.steps,
               guidance_scale=0.0, generator=g).images[0]
    dt = time.time() - t
    img.save(f"spike_out/base_{i:02d}.png")
    # fp16 has no bf16 fallback on Turing: an overflow shows up as a flat image,
    # not an exception, so record the luma range as a cheap sanity check.
    log.append({"i": i, "age": age, "sex": sex, "seconds": round(dt, 1),
                "peak_vram_gb": round(torch.cuda.max_memory_allocated() / 2**30, 2),
                "luma_range": img.convert("L").getextrema()})
    print(log[-1], flush=True)
    json.dump({"size": args.size, "steps": args.steps, "images": log},
              open("spike_out/base_timing.json", "w"), indent=1)
print("done", flush=True)
