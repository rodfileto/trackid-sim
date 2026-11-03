# Renting a GPU for the variants stage

The base faces stay local: FLUX.1-schnell makes one in 8.7 s on the RTX 2080,
which is cheaper than renting. The **variants** do not, and the reason is
narrow: Qwen-Image-Edit was trained in bf16, and this card has no bf16 tensor
cores. In fp16 the transformer emits NaN at step 0 and the VAE decodes to a
solid black frame; the only working local path is fp32 at **53.1 s/step**, or
26.6 minutes per image. The 90 variants the corpus needs would take ~40 hours of
a machine that is also somebody's desktop.

Any Ampere-or-newer card runs bf16 natively *and* holds the 12 GB of quantized
weights resident instead of streaming them per leaf module. That is the whole
trade: a few dollars against a day and a half.

## Before anything else: two cards that look fine and are not

Run this the moment a pod is up, before pushing anything. It costs ten seconds
and catches both failures that cost a pod each:

```sh
ssh facegen-pod 'nvidia-smi --query-gpu=name,compute_cap,driver_version --format=csv,noheader'
```

- **`compute_cap` must be below 12.** Blackwell (RTX 5090, sm_120) passes the
  bf16 check and then dequantises the GGUF weights on the CPU to ~40 GB instead
  of keeping them quantised on the card. Twenty minutes in, the GPU had still
  done nothing. Use Ada: RTX 4090, RTX 6000 Ada, L40S.
- **The driver no longer matters** — torch is pinned to cu128, which runs on
  12.8 and 13.x drivers alike. It used to: a cu130 build on a 12.8 host reports
  "no CUDA device" on a working card.

`setup.sh` refuses both automatically, but only after the push; the command
above refuses them before you spend anything.

## Which pod

Three thresholds, the first two checked by `setup.sh` before anything downloads:

- **compute capability ≥ 8.0** (Ampere or newer). This is the reason to rent.
  Turing is what we already have.
- **≥ 17 GB VRAM**, so the Q4_K_M transformer (12 GB) sits resident with the
  4 GB of headroom `hw.py` reserves for activations and the VAE.
- **≥ 21 GB VRAM**, so the Qwen2.5-VL encoder runs on the card too. This is the
  threshold that actually decides the shopping list: below it `hw.py` drops the
  encoder to the CPU, which was measured locally at 36 s per prompt pair — 50
  minutes of a rented card doing nothing but text encoding. It is why the
  20 GB RTX 4000 Ada is a false economy at $0.28/hr, and why **24 GB is the
  floor**.

Estimated cost of the 90-variant run at 30 steps (s/step scaled from published
Qwen-Image 20B Q4 timings at 1024 px — estimates, replaced by the trial run's
measurement):

| GPU | $/hr | VRAM | est. cost | est. wall-clock |
|---|---|---|---|---|
| RTX A5000 | 0.27 | 24 GB | $1.31 | 4.9 h |
| **RTX 4090** | **0.74** | **24 GB** | **$1.65** | **2.2 h** |
| RTX 5090 | 0.99 | 32 GB | $1.54 | 1.6 h |
| L40S | 1.09 | 48 GB | $2.84 | 2.6 h |
| A100 SXM | 1.59 | 80 GB | $3.20 | 2.0 h |
| H100 SXM | 3.49 | 80 GB | $4.40 | 1.3 h |

The whole spread is about three dollars, so this is not a cost decision: pick on
wall-clock and on the risk of a wasted run. **RTX 4090** is the default — it
clears both VRAM thresholds, it is the best-tested path for diffusers, and the
$0.34 it costs over an A5000 buys back 2.6 hours. The RTX 5090 is faster still
and nominally cheaper, but Blackwell (sm_120) is the newest kernel path and any
gap in the pinned torch costs more in debugging than the run saves.

Everything at 48 GB and up is wasted money here. `hw.py` will not load bigger
weights to fill a bigger card, deliberately — see "What deliberately does not
change" below.

**Disk.** Budget ~40 GB: ~28 GB of weights, ~9 GB of venv and uv cache, and
the outputs. The container-disk default is too small and the download dies
part-way through.

RunPod mounts a **network volume** at `/workspace` (MooseFS), and `setup.sh`
splits the layout across it rather than putting everything in one place:

| | where | why |
|---|---|---|
| weights (`HF_HOME`) | `/workspace` volume | 28 GB, few huge files, expensive to re-download, and they survive the pod |
| code, outputs, manifest | `/workspace` volume | a pod that dies mid-run must not take 2 h of generation with it |
| venv + uv cache | local container disk | ~30k small files, and `uv sync --frozen` rebuilds them from the lock in one command — nothing to gain from persisting, a lot to lose from a network filesystem |

Persist what is expensive or impossible to recreate; keep the reproducible part
local and fast. `setup.sh` writes `env.sh` in the project directory with the
resolved paths, and every later command sources it so a reconnect cannot drift.

**A network volume pins you to one datacenter**, which is where GPU
availability bites. That is a real cost if you have to re-rent; it is worth it
only if you expect to iterate across several days.

## The run

**Expose TCP port 22 on the pod** and use the *direct* SSH connection, not the
`ssh.runpod.io` proxy. The proxy gives an interactive shell and nothing else:
it has no exec channel, so `ssh pod 'command'` fails with "Your SSH client
doesn't support PTY", and rsync, scp and sftp cannot run over it at all. Every
command below needs one or both.

Then the alias, from the host and port on the Connect tab:

```
Host facegen-pod
    HostName 213.0.0.1
    User root
    Port 40000
    IdentityFile ~/.ssh/id_ed25519
    ServerAliveInterval 30
```

Then, from `tools/facegen/`:

```sh
./runpod/sync.sh push                              # code + the 30 base faces
ssh facegen-pod 'bash /workspace/facegen/runpod/setup.sh'
```

Then a **trial of three images before the full run**. It costs about five
minutes and settles the three things that can waste the whole rental: that
bf16 really works on this card, what a step actually costs here, and whether
the prompts preserve identity at all:

```sh
ssh facegen-pod 'cd /workspace/facegen && source env.sh && \
    uv run python gen_variants.py --count 1 --per-face 3'
./runpod/sync.sh pull
(cd ../facescore && go run .)                      # three variants, one face
```

If the drop rate on those three is sane and the per-image time matches the
estimate, run the rest. Nothing is regenerated: the manifest already records
the three, so the full run picks up at image four.

```sh
# nohup, because a dropped SSH connection would otherwise kill a 2 h run.
# (It would resume, but only after re-uploading and re-detecting the pod.)
ssh facegen-pod 'cd /workspace/facegen && source env.sh && \
    nohup uv run python gen_variants.py > variants.log 2>&1 &'
ssh facegen-pod 'tail -f /workspace/facegen/variants.log'   # watch; Ctrl-C is safe
./runpod/sync.sh pull                              # variants + manifest
(cd ../facescore && go run .)                      # identity check, drop rate
```

`gen_variants.py` prints an ETA after every image, so the log says early on
whether the estimate above held.

The generation step is resumable and writes `variant_manifest.json` after every
image, so an interrupted pod costs only the images not yet made: `sync.sh pull`,
then push to a new pod and re-run. **Destroy the pod when the run ends** — it
bills by the second while it exists, whether or not it is computing.

## What deliberately does *not* change on the pod

`hw.py` decides placement from the device, but it does not change **what is
generated**:

- **Same weights.** Q4_K_M everywhere, local and rented, even on a card that
  could hold Q8 or the 38 GB bf16 transformer. The recorded C0 gate result
  (cosine +0.765) was measured on Q4_K_M; letting the quantization follow the
  hardware would make the pod run a different experiment from the local one,
  and the drop rate would no longer be comparable to the number that opened the
  gate.
- **Same seeds and prompts.** Both derive from `--seed` in `gen_variants.py`,
  not from the machine.

Identity scoring stays local rather than moving to the pod: `tools/facescore`
needs cgo and onnxruntime against the `trackid-vision` checkout, and it runs on
CPU in seconds. Renting a GPU to do it would cost more in setup than it saves.

Results are still not bit-identical across the two machines — different CUDA
kernels give different rounding — so the manifest records the profile that made
each image. Compare runs by their scores, not their hashes.

## Cost, measured

200 variants in **350 min** on an RTX 4090 at **105 s each**, about **$4.50**.
Fixed overhead is negligible: 49 s to encode all 200 prompts on the card and
one transformer load (6-42 s warm, ~5 min with a cold page cache). The GPU sits
near 0% during that load, which looks like a stall and is not.

The pre-run estimate was $1.65 for 90 variants at an assumed 2.5 s/step; the
measured step is 3.5 s. It was an estimate, and this is why the rule is to
replace estimates with the first real run.
