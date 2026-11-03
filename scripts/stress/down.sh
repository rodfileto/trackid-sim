#!/usr/bin/env bash
# Stops the stress stack and erases its data: trackid's API and worker started by up.sh, and the
# containers and volumes of compose project trackid-stress. Nothing of the development stack
# (compose project trackid) is touched.

set -euo pipefail
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

for name in worker trackid; do
  pidfile="$STRESS_RUN_DIR/$name.pid"
  if [[ -f "$pidfile" ]]; then
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid"
      for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$pidfile"
  fi
done
stress_compose down -v --remove-orphans
