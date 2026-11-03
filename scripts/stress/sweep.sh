#!/usr/bin/env bash
# The burst sweep (docs/stress-test-plan.md in ../trackid, phase 1): for each level, a fresh
# stress stack and one burst of the demo world's mix scaled to that many times the demo's 300
# humans. It stops at the first level whose pipeline does not clear within the time cap and
# reports that level as the limit. Records go to out/stress/.
#
#   scripts/stress/sweep.sh [-levels "1 2 5 10 20"] [-senders 8] [-window 5m] [-timecap 2h] [-seed 1]
#
# WORKER_CONCURRENCY in the environment sets the worker's (default 10). Each level erases the
# stress stack (down.sh) and brings it up again (up.sh); the development stack is not touched.

set -euo pipefail
SIM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LEVELS="1 2 5 10 20"
SENDERS=8
WINDOW=5m
TIMECAP=2h
SEED=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    -levels) LEVELS="$2"; shift 2 ;;
    -senders) SENDERS="$2"; shift 2 ;;
    -window) WINDOW="$2"; shift 2 ;;
    -timecap) TIMECAP="$2"; shift 2 ;;
    -seed) SEED="$2"; shift 2 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see -h)" >&2; exit 2 ;;
  esac
done

cd "$SIM_DIR"
go build -o bin/trackid-sim ./cmd/trackid-sim
for level in $LEVELS; do
  humans=$((300 * level))
  echo "=== level ${level}x: ${humans} humans, ${SENDERS} senders, window ${WINDOW} ==="
  scripts/stress/down.sh >/dev/null 2>&1 || true
  scripts/stress/up.sh
  set +e
  (
    # shellcheck disable=SC1091
    source scripts/stress/env.sh
    bin/trackid-sim stress -seed "$SEED" -humans "$humans" -senders "$SENDERS" -window "$WINDOW" -timecap "$TIMECAP" \
      -label "x${level}-s${SENDERS}-w${WINDOW}-c${WORKER_CONCURRENCY}"
  )
  status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    echo "=== level ${level}x did not pass (timed out or failed operations): the limit; stopping the sweep ==="
    break
  fi
done
scripts/stress/down.sh >/dev/null 2>&1 || true
echo "records: $SIM_DIR/out/stress/"
