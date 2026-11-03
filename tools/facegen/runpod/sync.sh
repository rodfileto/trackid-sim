#!/usr/bin/env bash
# Move work between here and the pod. Only inputs go up and only results come
# down: weights live on the pod's network volume and are never synced.
#
#   ./runpod/sync.sh push     code + base faces + pool manifest -> pod
#   ./runpod/sync.sh pull     variants + variant manifest -> here
#
# Expects a host alias in ~/.ssh/config (RunPod gives you the host and port on
# the pod's Connect tab):
#
#   Host facegen-pod
#       HostName 213.0.0.1
#       User root
#       Port 40000
#       IdentityFile ~/.ssh/id_ed25519
#
# Override with POD=other-alias, or point it straight at a host.
set -euo pipefail

# -rlptDz, not -a: -a additionally implies -o -g (preserve owner and group),
# and RunPod mounts a network volume (MooseFS) at /workspace that refuses
# chown. The files transfer either way, but rsync then exits 23 and takes the
# rest of the script with it. Permissions and times are still preserved, which
# is what actually matters here -- the executable bit on these scripts.
RS=(-rlptDz --info=stats1)

POD="${POD:-facegen-pod}"
REMOTE="${REMOTE:-/workspace/facegen}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "${1:-}" in
push)
  echo "==> $HERE -> $POD:$REMOTE"
  ssh "$POD" "mkdir -p $REMOTE/spike_out/pool"
  # The code, the lock (so the pod resolves to the same versions), and the
  # attribute/prompt manifest. .venv and weights are excluded: the pod builds
  # its own venv for its own CUDA, and 28 GB of weights would be absurd here.
  rsync "${RS[@]}" \
    --exclude '.venv/' --exclude '__pycache__/' --exclude 'spike_out/' \
    --exclude 'samples/' \
    "$HERE/" "$POD:$REMOTE/"
  # The base faces are the actual input to the edit: 30 512px PNGs, ~15 MB.
  rsync "${RS[@]}" "$HERE/spike_out/pool/" "$POD:$REMOTE/spike_out/pool/"
  ;;
pull)
  echo "==> $POD:$REMOTE -> $HERE"
  mkdir -p "$HERE/spike_out/variants"
  rsync "${RS[@]}" "$POD:$REMOTE/spike_out/variants/" "$HERE/spike_out/variants/"
  # The manifest is the provenance the release depends on (ROADMAP block E):
  # prompts, seeds, weights, sha256 and the profile that produced each image.
  rsync "${RS[@]}" "$POD:$REMOTE/variant_manifest.json" "$HERE/"
  echo "==> now run the identity check locally:"
  echo "    (cd ../facescore && go run .)"
  ;;
*)
  echo "usage: $0 {push|pull}   (POD=$POD REMOTE=$REMOTE)" >&2
  exit 2
  ;;
esac
