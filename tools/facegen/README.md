# facegen

Image generation for the face layer ([ROADMAP.md](../../docs/ROADMAP.md) block C).
Python lives here, isolated from the Go build: `uv run python spike_base.py`.

Licences of every model used are recorded in
[docs/LICENSES.md](../../docs/LICENSES.md). Read that before adding a generator.

## Two machines, one set of scripts

These scripts run on the local RTX 2080 and on a rented pod, and those two want
opposite things: the local card needs every guardrail below, and a pod needs
none of them. `hw.py` makes that decision once, from what the device reports --
resident vs streamed weights, encoder on GPU vs CPU, bf16 vs the fp32 fallback,
whether to cap VRAM at all -- so no script carries a `--rented` flag.

    uv run python hw.py        # what this machine can do, and what follows

What it decides is *placement*. What it deliberately does not touch is what
gets generated: the same weights, seeds and prompts on both machines, so a pod
run and a local run stay comparable. See [runpod/README.md](runpod/README.md).

Everything below is the local card, and is what `hw.py` encodes.

## The hardware this has to survive

An RTX 2080: 8 GB VRAM, no bf16, **and it drives the display**. 60 GB system RAM.
Both resources have to be bounded explicitly, because exceeding either takes the
whole desktop down rather than raising an error. Both mistakes were made once
each while measuring; the guardrails below are the result.

### VRAM: cap it, or X dies

```python
torch.cuda.set_per_process_memory_fraction(0.72)   # ~5.5 of 7.6 GB
```

The desktop holds ~1 GB. Without the cap, torch expands into the memory X needs
and the machine freezes hard — no Python traceback, no OOM killer, power button.
With the cap a failure is an ordinary `torch.OutOfMemoryError`.

Never let the 12B transformer sit on the card whole (6.4 GB in nf4, 24 GB fp16).
Stream it instead; see below.

### RAM: one model per process

`use_stream=True` **pins** host memory. Pinned pages are page-locked: not
swappable, not reclaimed promptly when you drop the reference. Setting
`pipe = tf = None` between configs does *not* give it back.

Measured cost of ignoring this: two streaming configs in one process reached
23 GB anon + 35 GB shmem and the kernel OOM-killer fired.

So: **load one transformer per process and let process exit reclaim it.** To
sweep configs, re-exec; don't loop in-process. Bound it as well, so the kernel
kills the job and not your editor:

```bash
systemd-run --user --scope -p MemoryMax=40G uv run python spike_base.py
```

### bitsandbytes quantizes *on the GPU*

`from_pretrained(..., quantization_config=BitsAndBytesConfig(load_in_4bit=True))`
materialises on the card, and its allocator warmup over-reserves — it blows a
5.5 GB cap while merely *loading* T5. Two ways around it, both used here:

- Encode prompts on the **CPU**. For a handful of prompts it costs nothing:
  18 s on CPU vs 17 s on GPU. It removes a whole class of OOM.
- Keep the transformer in fp16 in RAM and stream it, rather than quantizing.

## Measured throughput (FLUX.1-schnell, 512 px, 4 steps)

| Offload config | s/image | Peak VRAM |
|---|---|---|
| `leaf_level`, no stream | 53.6 | 0.76 GB |
| **`leaf_level`, `use_stream=True`** | **9.2** | **0.76 GB** |

Same seed, same image out of both — streaming changes speed, not results. The
44 s difference is the GPU idling on weight transfer; with `use_stream=True` the
copy of the next layer overlaps compute on the current one.

Note how little VRAM either uses. The card is not the binding constraint once
weights are streamed; **host RAM and PCIe bandwidth are.**

### Qwen-Image-Edit (20B): why the variants are rented

fp16 weights would be ~40 GB, pinned, in a 60 GB machine that is also running a
desktop. That does not fit with any headroom, so the variants stage starts from
**pre-quantized GGUF** (12 GB at Q4_K_M) rather than fp16 — which also avoids
the bitsandbytes GPU-quantization trap above.

That makes it *fit*. It does not make it fast, and the reason is not VRAM:
Qwen was trained in bf16, Turing has no bf16, and fp16 decodes to a solid black
frame (the VAE overflows without raising). The only working local path is fp32
at **53.1 s/step** — 26.6 min per image, ~40 h for the 90 variants the corpus
needs. So base faces stay local and variants go to a rented Ampere-or-newer
card, where the same weights sit resident and run at 1-2 s/step.

## Layout


- `hw.py` — the device profile every pipeline asks for its placement.
- `gen_pool.py` — the base face pool (FLUX.1-schnell). Local; resumable.
- `gen_variants.py` — other images of the same person (Qwen-Image-Edit).
  Resumable, and splits into an encode stage and a generate stage so the two
  heavyweight models never share the machine. Rented; see `runpod/`.
- `degrade.py` — base faces into CCTV-like 1920x1080 captures.
- `runpod/` — `setup.sh` and `sync.sh` for the rented half, plus the pod choice
  and its cost.
- `spike_base.py`, `spike_qwen.py`, `tune_offload.py` — the C0 spikes, kept as
  the record of the measurements the roadmap cites. `gen_pool.py` and
  `gen_variants.py` superseded the first two for real runs; `tune_offload.py`
  **loops in-process, so see the RAM warning** and run one config per call.
- `samples/` — images meant to be looked at (tracked).
- `spike_out/` — raw run output, embeds, timings, logs (gitignored).

Scoring lives next door in [`../facescore`](../facescore), a separate Go module
(cgo + onnxruntime, which the simulator's build must not inherit). It reads
`variant_manifest.json` and reports the identity-check drop rate.
