#!/usr/bin/env bash
# Keep pulling variants off the pod until the run finishes.
#
# The pod has no persistent volume, so anything not yet pulled dies with it.
# This loop is the safety net, and it is deliberately independent of whatever
# started it: launched with nohup it survives closing the editor, the terminal
# and the session that created it. It does not survive powering the machine off.
#
#   nohup ./runpod/autopull.sh > spike_out/autopull.log 2>&1 &
#
# Stops on its own when the run prints "done:", or after MAX_HOURS.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POD="${POD:-facegen-pod}"
REMOTE="${REMOTE:-/workspace/facegen}"
LOGFILE="${LOGFILE:-full200.log}"
INTERVAL="${INTERVAL:-300}"      # 5 minutes: ~3 images, cheap either way
MAX_HOURS="${MAX_HOURS:-10}"

deadline=$(( $(date +%s) + MAX_HOURS*3600 ))
echo "[$(date +%H:%M:%S)] autopull started: $POD:$REMOTE every ${INTERVAL}s"

while :; do
  # ssh failures are expected and survivable -- a dropped connection or a
  # rebooting pod must not end the watch, only skip a cycle.
  # Count images on disk rather than grepping the log for "[n/200]": the
  # bracket needs escaping that does not survive the trip through ssh's
  # quoting, and a silently failing grep shifts every line that follows it --
  # which is what stopped the finish signal from ever firing.
  out=$(ssh -o BatchMode=yes -o ConnectTimeout=20 "$POD" \
        "ls $REMOTE/spike_out/variants/*.png 2>/dev/null | wc -l; \
         grep -c '^done:' $REMOTE/$LOGFILE 2>/dev/null || echo 0" 2>/dev/null) || out=""
  n=$(echo "$out" | sed -n 1p)
  fin=$(echo "$out" | sed -n 2p)

  "$HERE/runpod/sync.sh" pull >/dev/null 2>&1 \
    && echo "[$(date +%H:%M:%S)] pulled; pod at ${n:-?}/200" \
    || echo "[$(date +%H:%M:%S)] pull failed (pod unreachable?), will retry"

  if [ "${fin:-0}" != "0" ]; then
    echo "[$(date +%H:%M:%S)] run finished; final pull done"
    ls "$HERE/spike_out/variants"/*.png 2>/dev/null | wc -l | xargs echo "  variants local:"
    echo "  next:  cd ../facescore && go run ."
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "[$(date +%H:%M:%S)] stopping after ${MAX_HOURS}h without a finish signal"
    break
  fi
  sleep "$INTERVAL"
done
