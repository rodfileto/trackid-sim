"""C0 spike, step 2: a pool of base faces, one per simulated human.

Deterministic: the attributes, the prompts and the per-face seeds all derive
from --seed and the committed IBGE shares (demographics.py, fetch_shares.py), and every image is recorded in a manifest with its prompt, seed,
model revision and sha256. That manifest is the provenance the release depends
on (ROADMAP block E), so it is tracked even though the images are not.

Resumable: an image already on disk with a matching manifest entry is skipped,
so a kill part-way through costs only the faces not yet made.

Run it bounded, one transformer per process -- see README.md:

    systemd-run --user --scope -p MemoryMax=45G \
        uv run python gen_pool.py --count 200
"""
import argparse, gc, hashlib, json, time
from pathlib import Path

import torch
from diffusers import FluxPipeline, FluxTransformer2DModel
from transformers import T5EncoderModel

import demographics
import hw

MODEL = "black-forest-labs/FLUX.1-schnell"
OUT = Path("spike_out/pool")
MANIFEST = Path("pool_manifest.json")

# Attributes and prompts live in demographics.py, which samples colour, sex and
# age from the committed IBGE Censo 2022 shares (fetch_shares.py). Nothing
# downstream may read those attributes as ground truth about the rendered
# images: they are what was *asked for*. The first pool asked for Censo shares
# too and still rendered lighter than requested -- the skew is in FLUX's
# weights, and PLAN.md scopes demographic bias out of every claim built on this
# corpus.


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--count", type=int, default=200)
    ap.add_argument("--seed", type=int, default=20260920)
    ap.add_argument("--size", type=int, default=512)
    ap.add_argument("--steps", type=int, default=4)
    ap.add_argument("--smile-share", type=float, default=demographics.SMILE_SHARE)
    args = ap.parse_args()

    # Placement (VRAM cap, streamed vs resident) comes from the device, not a
    # flag -- see hw.py. On the local card this reproduces the 0.72 budget.
    profile = hw.detect().apply()
    # FLUX.1-schnell's transformer is 12B, so ~24 GB in fp16.
    plan = profile.plan(24.0)
    print(profile.describe(), flush=True)
    OUT.mkdir(parents=True, exist_ok=True)

    shares = demographics.load_shares()
    people = [p.as_dict() for p in demographics.sample_population(
        args.count, args.seed, smile_share=args.smile_share, shares=shares)]
    for p in people:
        p["prompt"] = demographics.prompt_for(demographics.Person(
            **{k: v for k, v in p.items() if k != "prompt"}))
    asked = demographics.realized_shares(
        [demographics.Person(**{k: v for k, v in p.items() if k != "prompt"}) for p in people])
    print("asked-for shares:", json.dumps(asked["ethnicity"]), flush=True)

    done = {}
    if MANIFEST.exists():
        prev = json.loads(MANIFEST.read_text())
        by_id = {p["id"]: p for p in people}
        # Same rule as gen_variants: resume only on an entry matching the work
        # about to be done, prompt included. A prompt edit must regenerate, or
        # the manifest would describe images it did not produce.
        done = {f["id"]: f for f in prev.get("faces", [])
                if (OUT / f"{f['id']}.png").exists() and f.get("prompt_seed") == args.seed
                and f["id"] in by_id and f.get("prompt") == by_id[f["id"]]["prompt"]}
    todo = [p for p in people if p["id"] not in done]
    print(f"{len(done)} already done, {len(todo)} to generate", flush=True)
    if not todo:
        return

    # Stage 1: prompts on the CPU, so the card stays free and bitsandbytes never
    # quantizes on-device (see README).
    t0 = time.time()
    # Both of these are silent and slow -- loading T5 in fp32 is ~20 GB of RAM
    # and encoding 200 prompts on CPU is ~20 minutes -- so they say so. An
    # unannounced 20-minute stall is indistinguishable from a hang, and this
    # loop previously printed nothing until every prompt was done.
    print(f"loading T5 text encoder (fp32, ~20 GB RAM)...", flush=True)
    t5 = T5EncoderModel.from_pretrained(MODEL, subfolder="text_encoder_2", torch_dtype=torch.float32)
    enc = FluxPipeline.from_pretrained(MODEL, transformer=None, vae=None, text_encoder_2=t5,
                                       torch_dtype=torch.float32)
    print(f"encoding {len(todo)} prompts on cpu ({time.time() - t0:.0f}s to load)", flush=True)
    embeds = {}
    te = time.time()
    with torch.no_grad():
        for n, p in enumerate(todo, 1):
            pe, ppe, _ = enc.encode_prompt(p["prompt"], prompt_2=None, max_sequence_length=256,
                                           device="cpu")
            embeds[p["id"]] = (pe.to(torch.float16), ppe.to(torch.float16))
            if n == 1 or n % 10 == 0 or n == len(todo):
                eta = (time.time() - te) / n * (len(todo) - n)
                print(f"  encode [{n}/{len(todo)}] {p['id']}  eta {eta / 60:.1f}m", flush=True)
    del enc, t5
    gc.collect()
    print(f"encode: {time.time() - t0:.0f}s for {len(todo)} prompts (cpu)", flush=True)

    # Stage 2: transformer stays in RAM, streamed to the GPU per leaf module.
    t1 = time.time()
    tf = FluxTransformer2DModel.from_pretrained(MODEL, subfolder="transformer",
                                                torch_dtype=torch.float16)
    revision = getattr(tf.config, "_commit_hash", None)
    pipe = FluxPipeline.from_pretrained(MODEL, transformer=tf, text_encoder=None,
                                        text_encoder_2=None, tokenizer=None, tokenizer_2=None,
                                        torch_dtype=torch.float16)
    if plan.resident:
        pipe.to("cuda")
    else:
        pipe.transformer.enable_group_offload(onload_device=torch.device("cuda"),
                                              offload_device=torch.device("cpu"),
                                              offload_type="leaf_level", use_stream=True)
        pipe.vae.to("cuda")
    pipe.vae.to("cuda")
    if plan.usable_gb < 12:
        pipe.vae.enable_tiling()
    print(f"load transformer: {time.time() - t1:.0f}s, "
          f"{'resident' if plan.resident else 'streamed'}", flush=True)

    faces = list(done.values())
    for n, p in enumerate(todo, 1):
        pe, ppe = embeds[p["id"]]
        t = time.time()
        img = pipe(prompt_embeds=pe.to("cuda"), pooled_prompt_embeds=ppe.to("cuda"),
                   height=args.size, width=args.size, num_inference_steps=args.steps,
                   guidance_scale=0.0,
                   generator=torch.Generator("cpu").manual_seed(p["seed"])).images[0]
        dt = time.time() - t
        path = OUT / f"{p['id']}.png"
        img.save(path)
        faces.append({**p, "prompt_seed": args.seed, "file": str(path),
                      "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
                      "seconds": round(dt, 1),
                      # fp16 on Turing fails silently as a flat frame, not an
                      # exception, so keep a cheap per-image sanity check.
                      "luma_range": img.convert("L").getextrema()})
        MANIFEST.write_text(json.dumps(
            {"model": MODEL, "revision": revision, "size": args.size, "steps": args.steps,
             "prompt_seed": args.seed, "smile_share": args.smile_share,
             "profile": profile.as_dict(),
             # The demographic provenance travels with the corpus (block E),
             # alongside the shares actually asked for.
             "demographics": {**shares["provenance"], "asked_for": asked},
             "faces": sorted(faces, key=lambda f: f["id"])}, indent=1))
        print(f"[{n}/{len(todo)}] {p['id']} {p['sex']} {p['age']} {dt:.1f}s", flush=True)
    print("done", flush=True)


if __name__ == "__main__":
    main()
