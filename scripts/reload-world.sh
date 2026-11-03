#!/usr/bin/env bash
# Erases trackid's development database and loads a freshly generated synthetic world into it:
#
#   scripts/reload-world.sh [-seed N] [-humans N]
#
# It asks everything first -- the super admin account to recreate, its password, and a confirmation
# typed as the database's name -- and only then:
#   1. drops and recreates the database named by DATABASE_URL in ../trackid/.env, and clears the
#      worker's Redis queue (left-over jobs would point at ids the new data reuses);
#   2. migrates it and sets the demo's match thresholds (without them faces chain different people
#      into one cluster, see README);
#   3. creates the super admin (trackid's cmd/create-superuser);
#   4. generates the world (Censo 2022 names) and loads it, waiting for trackid's worker.
#
# trackid's development stack must be running (../trackid/scripts/start_dev.sh, with the fingerprint
# sidecar): the worker embeds, extracts, matches and clusters what load sends it. TRACKID_DIR
# overrides where trackid is checked out, and TRACKID_ENV the env file (default its .env).

set -euo pipefail

SIM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TRACKID_DIR="${TRACKID_DIR:-$SIM_DIR/../trackid}"
TRACKID_DIR="$(cd "$TRACKID_DIR" && pwd)"
ENV_FILE="${TRACKID_ENV:-$TRACKID_DIR/.env}"

SEED=1
HUMANS=300
while [[ $# -gt 0 ]]; do
  case "$1" in
    -seed) SEED="$2"; shift 2 ;;
    -humans) HUMANS="$2"; shift 2 ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see -h)" >&2; exit 2 ;;
  esac
done

die() { echo "reload-world: $*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || die "no $ENV_FILE"
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

# DATABASE_URL: postgres://user:password@host:port/name?params
[[ "${DATABASE_URL:-}" =~ ^postgres(ql)?://([^:@/]+)(:[^@]*)?@[^/]+/([^?]+) ]] || die "cannot read DATABASE_URL"
DB_USER="${BASH_REMATCH[2]}"
DB_NAME="${BASH_REMATCH[4]}"
REDIS_DB=0
[[ "${REDIS_URL:-}" =~ /([0-9]+)$ ]] && REDIS_DB="${BASH_REMATCH[1]}"

# docker compose exec reads standard input; give it none, so it never eats the answers below.
compose() { docker compose -f "$TRACKID_DIR/docker-compose.yml" "$@" </dev/null; }
psql_admin() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d postgres -qtA "$@"; }
psql_db() { compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" -qtA "$@"; }

# trackid's stack must be up: load waits for its worker.
worker_pid_file="$TRACKID_DIR/.run/worker.pid"
if [[ ! -f "$worker_pid_file" ]] || ! kill -0 "$(cat "$worker_pid_file")" 2>/dev/null; then
  die "trackid's worker is not running; start the stack first: $TRACKID_DIR/scripts/start_dev.sh"
fi
psql_admin -c "SELECT 1" >/dev/null || die "cannot reach Postgres (docker compose service postgres)"

# --- Everything asked up front ------------------------------------------------------------------

# Interactive only: without a terminal (e.g. Claude Code's "!" prompt) the questions cannot be
# answered. RELOAD_WORLD_STDIN=1 accepts piped answers (tests).
if [[ ! -t 0 && "${RELOAD_WORLD_STDIN:-}" != 1 ]]; then
  die "it asks questions and needs a terminal: run it in a terminal window"
fi

# Defaults from the current super admin, when the database has one.
defaults=""
if psql_admin -c "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" | grep -q 1; then
  defaults="$(psql_db -F $'\t' -c "SELECT first_name, last_name, staff_number, job_title, username,
      examines_fingerprint, examines_facial, intelligence_analyst, coordinator
      FROM users WHERE account_role = 'SUPER_ADMIN' AND is_active ORDER BY id LIMIT 1" 2>/dev/null || true)"
fi
IFS=$'\t' read -r d_first d_last d_staff d_job d_user d_fp d_facial d_intel d_coord <<<"${defaults:-}"

ask() { # ask VAR "Question" default
  local answer
  read -r -p "$2${3:+ [$3]}: " answer
  printf -v "$1" '%s' "${answer:-$3}"
}
yes_no() { # yes_no VAR "Question" default(t/f)
  local def="n" answer
  [[ "$3" == "t" ]] && def="y"
  read -r -p "$2 (y/n) [$def]: " answer
  answer="${answer:-$def}"
  [[ "$answer" =~ ^[yYsS] ]] && printf -v "$1" true || printf -v "$1" false
}

echo "The super admin to create in the new database:"
ask first_name "  First name" "$d_first"
ask last_name "  Last name" "$d_last"
ask staff_number "  Staff number (to sign in)" "$d_staff"
ask job_title "  Job title" "$d_job"
ask username "  Username (never changes)" "$d_user"
[[ -n "$staff_number" && -n "$username" ]] || die "staff number and username are required"
yes_no examines_fp "  Examines fingerprints?" "${d_fp:-f}"
yes_no examines_facial "  Examines faces?" "${d_facial:-f}"
yes_no intelligence "  Intelligence analyst?" "${d_intel:-f}"
yes_no coordinator "  Coordinator?" "${d_coord:-f}"
while true; do
  read -r -s -p "  Password (8 characters or more): " password; echo
  read -r -s -p "  Repeat it: " password2; echo
  [[ "$password" == "$password2" ]] || { echo "  They do not match."; continue; }
  [[ ${#password} -ge 8 ]] || { echo "  Too short."; continue; }
  break
done

echo
echo "This ERASES every record in the database '$DB_NAME' (and its worker queue, Redis db $REDIS_DB),"
echo "then loads a synthetic world (seed $SEED, $HUMANS humans). Files already in object storage stay."
read -r -p "Type the database name to go on: " confirm
[[ "$confirm" == "$DB_NAME" ]] || die "not confirmed; nothing was changed"

# --- The work -------------------------------------------------------------------------------------

echo "== Dropping and recreating $DB_NAME"
psql_admin -c "DROP DATABASE IF EXISTS \"$DB_NAME\" WITH (FORCE)" -c "CREATE DATABASE \"$DB_NAME\""
compose exec -T redis redis-cli -n "$REDIS_DB" flushdb >/dev/null

cd "$TRACKID_DIR"
echo "== Migrating"
go run ./cmd/migrate up
echo "== Match thresholds"
go run ./cmd/match-threshold -review 0.6 -confirm 0.7 -by "${username}" -commit \
  -source "trackid-sim demo: C0 pool, variant-matched impostor p99.9 ~0.70; pairs 0.60-0.70 go to users for review"
go run ./cmd/match-threshold -embedding-type FINGERPRINT_SOURCEAFIS -review 40 -confirm 60 -by "${username}" -commit \
  -source "trackid-sim demo: SourceAFIS 40 to review, 60 to confirm"

echo "== Super admin $username"
flags=(-first-name "$first_name" -last-name "$last_name" -staff-number "$staff_number" -job-title "$job_title" -username "$username")
$examines_fp && flags+=(-examines-fingerprint)
$examines_facial && flags+=(-examines-facial)
$intelligence && flags+=(-intelligence)
$coordinator && flags+=(-coordinator)
TRACKID_SUPERUSER_PASSWORD="$password" go run ./cmd/create-superuser "${flags[@]}" </dev/null
unset password password2

cd "$SIM_DIR"
echo "== Generating the world"
go build -o bin/trackid-sim ./cmd/trackid-sim
bin/trackid-sim generate -lexicon onomastic/data/ibge2022 -seed "$SEED" -humans "$HUMANS" -out out/world.json
echo "== Loading it (waits for trackid's worker)"
bin/trackid-sim load -world out/world.json -env "$ENV_FILE" -wait 25m

echo
echo "Done: $DB_NAME holds the world in out/world.json; sign in as $staff_number."
