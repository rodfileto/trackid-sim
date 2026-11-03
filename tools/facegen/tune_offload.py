"""C0 spike, step 1b: how fast can weights be streamed to an 8 GB card?

base_00/01 cost ~54 s at 0.76 GB peak against a 5.5 GB budget, i.e. leaf-level
offload left most of the card idle. Time one image per offload config to find
the real ceiling. The answer also decides whether Qwen-Image-Edit (20B, ~1.7x
these weights) is viable locally at all.
"""
import gc, json, time
import torch
from diffusers import FluxPipeline, FluxTransformer2DModel
from transformers import T5EncoderModel

MODEL = "black-forest-labs/FLUX.1-schnell"
PROMPT = ("Passport-style frontal photograph of a 28 year old woman, neutral expression, "
          "plain light grey background, even studio lighting, sharp focus, photorealistic")
CONFIGS = [
    {"name": "leaf_level",           "kw": dict(offload_type="leaf_level", use_stream=False)},
    {"name": "leaf_level+stream",    "kw": dict(offload_type="leaf_level", use_stream=True)},
    {"name": "block2+stream",        "kw": dict(offload_type="block_level", num_blocks_per_group=2, use_stream=True)},
    {"name": "block4+stream",        "kw": dict(offload_type="block_level", num_blocks_per_group=4, use_stream=True)},
]

torch.cuda.set_per_process_memory_fraction(0.72)

# Encode once on the CPU; reused by every config.
t5 = T5EncoderModel.from_pretrained(MODEL, subfolder="text_encoder_2", torch_dtype=torch.float32)
enc = FluxPipeline.from_pretrained(MODEL, transformer=None, vae=None, text_encoder_2=t5,
                                   torch_dtype=torch.float32)
with torch.no_grad():
    pe, ppe, _ = enc.encode_prompt(PROMPT, prompt_2=None, max_sequence_length=256, device="cpu")
pe, ppe = pe.to(torch.float16), ppe.to(torch.float16)
del enc, t5
gc.collect()
print("encoded", flush=True)

results = []
for cfg in CONFIGS:
    try:
        tf = FluxTransformer2DModel.from_pretrained(MODEL, subfolder="transformer",
                                                    torch_dtype=torch.float16)
        pipe = FluxPipeline.from_pretrained(MODEL, transformer=tf, text_encoder=None,
                                            text_encoder_2=None, tokenizer=None, tokenizer_2=None,
                                            torch_dtype=torch.float16)
        pipe.transformer.enable_group_offload(onload_device=torch.device("cuda"),
                                              offload_device=torch.device("cpu"), **cfg["kw"])
        pipe.vae.to("cuda"); pipe.vae.enable_tiling()
        torch.cuda.reset_peak_memory_stats()
        t = time.time()
        img = pipe(prompt_embeds=pe.to("cuda"), pooled_prompt_embeds=ppe.to("cuda"),
                   height=512, width=512, num_inference_steps=4, guidance_scale=0.0,
                   generator=torch.Generator("cpu").manual_seed(1000)).images[0]
        row = {"config": cfg["name"], "seconds": round(time.time() - t, 1),
               "peak_vram_gb": round(torch.cuda.max_memory_allocated() / 2**30, 2),
               "luma_range": img.convert("L").getextrema()}
        img.save(f"spike_out/tune_{cfg['name'].replace('+', '_')}.png")
    except torch.OutOfMemoryError:
        row = {"config": cfg["name"], "seconds": None, "peak_vram_gb": None, "error": "OOM"}
    except Exception as e:  # keep the sweep going; a bad config is a result too
        row = {"config": cfg["name"], "seconds": None, "peak_vram_gb": None,
               "error": f"{type(e).__name__}: {e}"[:200]}
    results.append(row)
    print(row, flush=True)
    pipe = tf = None
    gc.collect(); torch.cuda.empty_cache()
    json.dump(results, open("spike_out/tune_offload.json", "w"), indent=1)
print("done", flush=True)
