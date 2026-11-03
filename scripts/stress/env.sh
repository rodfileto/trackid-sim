# Environment of the stress stack (docs/stress-test-plan.md in ../trackid). Source it:
#
#   source scripts/stress/env.sh
#
# The stress stack is trackid's own docker-compose.yml run as a second compose project,
# trackid-stress, on its own ports and volumes, so the development stack and the world loaded
# in it are never touched. It takes trackid's .env for what is not about the stack (the vision
# models, the GPU libraries, WordCraft) and overrides every address the stack has.

STRESS_SIM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TRACKID_DIR="${TRACKID_DIR:-$STRESS_SIM_DIR/../trackid}"
TRACKID_DIR="$(cd "$TRACKID_DIR" && pwd)"

if [[ -f "$TRACKID_DIR/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$TRACKID_DIR/.env"
  set +a
fi

set -a
TRACKID_STRESS=1
COMPOSE_PROJECT_NAME=trackid-stress
STRESS_RUN_DIR="$STRESS_SIM_DIR/out/stress/.run"

POSTGRES_PORT=56432
REDIS_PORT=56380
MINIO_PORT=59100
MINIO_CONSOLE_PORT=59101
SIDECAR_PORT=58190
PORT=8182

DATABASE_URL="postgres://trackid:trackid_dev@localhost:${POSTGRES_PORT}/trackid?sslmode=disable"
REDIS_URL="redis://localhost:${REDIS_PORT}/0"
S3_ENDPOINT="http://localhost:${MINIO_PORT}"
S3_ACCESS_KEY=trackid
S3_SECRET_KEY=trackid_dev_storage
S3_BUCKET=trackid-stress
FINGERPRINT_SIDECAR_URL="http://localhost:${SIDECAR_PORT}"
JWT_SECRET=stress-test-secret
STRESS_API_URL="http://127.0.0.1:${PORT}"

# GPU only (decided 2026-10-29): up.sh refuses to go on if the worker falls back to CPU.
VISION_USE_GPU=true
WORKER_CONCURRENCY="${WORKER_CONCURRENCY:-10}"
set +a

stress_compose() {
  docker compose -p trackid-stress -f "$TRACKID_DIR/docker-compose.yml" \
    -f "$STRESS_SIM_DIR/scripts/stress/compose.stress.yml" "$@"
}
