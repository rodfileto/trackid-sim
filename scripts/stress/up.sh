#!/usr/bin/env bash
# Starts the stress stack: trackid's containers as compose project trackid-stress, migrated,
# with the demo's match thresholds, and trackid's API and worker built from ../trackid and run
# against it. The worker must come up on the GPU, or this stops.
#
#   scripts/stress/up.sh            # WORKER_CONCURRENCY=20 scripts/stress/up.sh to vary the worker
#
# Stop it with scripts/stress/down.sh, which also erases the stack's data.

set -euo pipefail
# shellcheck disable=SC1091
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
die() { echo "stress/up: $*" >&2; exit 1; }
mkdir -p "$STRESS_RUN_DIR"

wait_for() {
  local name="$1" check="$2"
  for _ in $(seq 1 60); do
    if eval "$check" >/dev/null 2>&1; then return; fi
    sleep 1
  done
  die "$name did not become ready"
}

echo "Starting containers (project trackid-stress) ..."
# Built from ../trackid each time (cached when nothing changed), so the sidecar runs the code checked out.
stress_compose up -d --build postgres redis minio sourceafis-sidecar
# Over TCP: while the image initialises the database, it listens on its socket only.
wait_for "Postgres" "stress_compose exec -T postgres pg_isready -h 127.0.0.1 -U trackid -d trackid"
wait_for "the fingerprint sidecar" "curl -sf ${FINGERPRINT_SIDECAR_URL}/healthz"
wait_for "MinIO" "curl -sf ${S3_ENDPOINT}/minio/health/live"

echo "Migrating and setting the match thresholds ..."
cd "$TRACKID_DIR"
go run ./cmd/migrate up
go run ./cmd/match-threshold -review 0.6 -confirm 0.7 -by stress -source "trackid-sim stress (demo band)" -commit >/dev/null
go run ./cmd/match-threshold -embedding-type FINGERPRINT_SOURCEAFIS -review 40 -confirm 60 -by stress \
  -source "trackid-sim stress (demo band)" -commit >/dev/null

echo "Building trackid's API and worker ..."
go build -o "$STRESS_RUN_DIR/trackid" ./cmd/trackid
go build -o "$STRESS_RUN_DIR/worker" ./cmd/worker

start() {
  local name="$1"
  local pidfile="$STRESS_RUN_DIR/$name.pid"
  if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
    die "$name is already running (PID $(cat "$pidfile")); scripts/stress/down.sh first"
  fi
  # Run from trackid's root, where the API finds frontend/dist; its .env does not override what
  # env.sh exported.
  setsid bash -c "cd '$TRACKID_DIR' && exec '$STRESS_RUN_DIR/$name'" >"$STRESS_RUN_DIR/$name.log" 2>&1 &
  echo $! >"$pidfile"
}

start trackid
wait_for "the API" "curl -sf ${STRESS_API_URL}/health"
start worker
wait_for "the worker" "grep -q 'trackid worker starting' '$STRESS_RUN_DIR/worker.log'"
grep -q "vision: CUDA execution provider enabled" "$STRESS_RUN_DIR/worker.log" \
  || { "$(dirname "${BASH_SOURCE[0]}")/down.sh" >/dev/null 2>&1 || true; die "the worker is not on the GPU (see $STRESS_RUN_DIR/worker.log); the stress test runs on GPU only"; }
grep -q "registered handlers for fingerprint" "$STRESS_RUN_DIR/worker.log" \
  || die "the worker has no fingerprint handlers (see $STRESS_RUN_DIR/worker.log)"

echo "Stress stack up: API ${STRESS_API_URL}, worker concurrency ${WORKER_CONCURRENCY}, logs in ${STRESS_RUN_DIR}"
