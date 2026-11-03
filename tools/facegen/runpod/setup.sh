#!/usr/bin/env bash
# Prepare a rented pod to run gen_variants.py. Idempotent: safe to re-run after
# a pod is destroyed and recreated against the same network volume.
#
#   ssh facegen-pod 'bash /workspace/facegen/runpod/setup.sh'
#
# Everything expensive lands on /workspace, which is the network volume and the
# only thing that survives the pod. The container filesystem does not: a pod is
# cattle, the volume is the herd.
set -euo pipefail

WORKSPACE="${WORKSPACE:-/workspace}"
PROJECT="${PROJECT:-$WORKSPACE/facegen}"

# ~28 GB of weights (12 GB GGUF transformer + the 16 GB Qwen2.5-VL encoder, VAE
# and tokenizer). On the volume it is downloaded once and every later pod finds
# it already there -- that download is otherwise the largest part of a short
# rental's bill, in both time and transfer.
export HF_HOME="${HF_HOME:-$WORKSPACE/hf-cache}"
mkdir -p "$HF_HOME"

# The venv and uv's cache go on the LOCAL container disk, not on $WORKSPACE.
# RunPod mounts a network volume there (MooseFS), which is right for the
# weights -- a few huge files, read sequentially, expensive to re-download --
# and wrong for a venv, which is ~30k small files. The rule: persist what is
# expensive or impossible to recreate (weights, outputs); keep what `uv sync`
# rebuilds from the lock in one command on fast local disk.
# Both on the same filesystem, so uv hardlinks from cache to venv rather than
# copying every wheel.
LOCAL="${LOCAL:-/root/facegen-local}"
export UV_PROJECT_ENVIRONMENT="${UV_PROJECT_ENVIRONMENT:-$LOCAL/venv}"
export UV_CACHE_DIR="${UV_CACHE_DIR:-$LOCAL/uv-cache}"
mkdir -p "$UV_PROJECT_ENVIRONMENT" "$UV_CACHE_DIR"

echo "==> workspace $WORKSPACE, project $PROJECT"
echo "==> HF_HOME $HF_HOME (volume)"
echo "==> venv $UV_PROJECT_ENVIRONMENT (local disk)"
df -h "$WORKSPACE" / 2>/dev/null | cat
nvidia-smi --query-gpu=name,memory.total,compute_cap --format=csv || true

if ! command -v uv >/dev/null 2>&1; then
  echo "==> installing uv"
  curl -LsSf https://astral.sh/uv/install.sh | sh
  export PATH="$HOME/.local/bin:$PATH"
fi

cd "$PROJECT"
echo "==> uv sync --frozen (from the committed lock, so the pod runs what local ran)"
uv sync --frozen

# Fail here rather than 26 minutes into a run: this whole exercise exists
# because the local card has no bf16, so a pod without it is worth nothing.
echo "==> hardware profile"
uv run python hw.py
uv run python - <<'PY'
import hw, os, sys
p = hw.detect()
if not p.bf16:
    sys.exit(f"REFUSING: {p.name} has no native bf16; rent an "
             "Ampere-or-newer card -- that is the entire point of renting")
if not p.gguf_fast_path and os.environ.get("FACEGEN_ALLOW_SLOW_GGUF") != "1":
    sys.exit(f"REFUSING: {p.name} (sm_{p.sm[0]}{p.sm[1]}) has no GGUF fast path. "
             "Measured on an RTX 5090: the Q4_K_M weights are dequantized on "
             "the CPU to ~40 GB instead of staying quantized on the card, "
             "which no consumer card can then hold. Rent Ada or older "
             "(RTX 4090, RTX 6000 Ada, L40S, A40). "
             "Set FACEGEN_ALLOW_SLOW_GGUF=1 to re-test this.")
if p.kind != "rented":
    print(f"note: detected as '{p.kind}', not 'rented'; "
          "set FACEGEN_PROFILE=rented if this is a pod with a display attached")
print("bf16 ok")
PY

echo "==> fetching weights into $HF_HOME (skipped if already on the volume)"
uv run python fetch_qwen.py

# One file the run commands source, so the paths are stated once and a later
# shell (or a reconnect after a dropped SSH session) cannot drift from them.
cat > "$PROJECT/env.sh" <<ENVEOF
export HF_HOME="$HF_HOME"
export UV_PROJECT_ENVIRONMENT="$UV_PROJECT_ENVIRONMENT"
export UV_CACHE_DIR="$UV_CACHE_DIR"
export PATH="\$HOME/.local/bin:\$PATH"
ENVEOF
echo "==> wrote $PROJECT/env.sh"

echo "==> ready. Run:"
echo "    cd $PROJECT && source env.sh && uv run python gen_variants.py"
