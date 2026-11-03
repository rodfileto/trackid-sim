#!/usr/bin/env bash
# The concurrent-users track (docs/stress-test-plan.md in ../trackid, phase 2): a fresh stress
# stack with a world loaded, a class's accounts, and k6 driving examiners, a coordinator,
# analysts and the image lab at once; then consistency checks on what they wrote. The summary,
# the checks and the logs go to out/stress/<time>-class-<VUS>/.
#
#   scripts/stress/class.sh [-vus 30] [-hold 5m] [-ramp 2m] [-humans 300] [-analysts 3] [-lab 2] [-keep]
#
# k6 runs from its container image (grafana/k6, pinned) on the host's network. -keep leaves
# the stress stack up afterwards, for a look in the UI at http://127.0.0.1:8182.

set -euo pipefail
SIM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
K6_IMAGE="grafana/k6:0.54.0"
VUS=30 HOLD=5m RAMP=2m HUMANS=300 ANALYSTS=3 LAB=2 KEEP=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -vus) VUS="$2"; shift 2 ;;
    -hold) HOLD="$2"; shift 2 ;;
    -ramp) RAMP="$2"; shift 2 ;;
    -humans) HUMANS="$2"; shift 2 ;;
    -analysts) ANALYSTS="$2"; shift 2 ;;
    -lab) LAB="$2"; shift 2 ;;
    -keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see -h)" >&2; exit 2 ;;
  esac
done

cd "$SIM_DIR"
RUN="out/stress/$(date +%Y%m%d-%H%M%S)-class-${VUS}"
mkdir -p "$RUN"
go build -o bin/trackid-sim ./cmd/trackid-sim

scripts/stress/down.sh >/dev/null 2>&1 || true
scripts/stress/up.sh
# shellcheck disable=SC1091
source scripts/stress/env.sh

echo "Loading a ${HUMANS}-human world ..."
bin/trackid-sim generate -lexicon onomastic/data/ibge2022 -humans "$HUMANS" -out "$RUN/world.json" >"$RUN/generate.log" 2>&1
bin/trackid-sim load -world "$RUN/world.json" -wait 30m >"$RUN/load.log" 2>&1 || { tail -5 "$RUN/load.log"; exit 1; }

# One account per virtual user that can exist at once (k6 numbers them across scenarios: the
# examiners at the spike, the coordinator, analysts and lab), so no two share one: a shared
# account is one person in two tabs, whose second tab is refused (403) once the first submits.
ACCOUNTS=$((VUS * 2 + 1 + ANALYSTS + LAB))
echo "Creating ${ACCOUNTS} examiner accounts and the instructor ..."
bin/trackid-sim users -examiners "$ACCOUNTS" -key "$RUN/class-key.json" >"$RUN/users.log"

bin/trackid-sim fingerprints -fingers 1 -out "$RUN/prints" >/dev/null 2>&1
latent="$(ls "$RUN"/prints/f*_l*.png | head -1)"
cp "$latent" "$RUN/latent.png"

echo "Running k6: ${VUS} examiners (${RAMP} ramp, ${HOLD} hold, spike to $((VUS * 2))), ${ANALYSTS} analysts, ${LAB} in the lab ..."
docker run --rm --network host -u "$(id -u):$(id -g)" \
  -v "$SIM_DIR/scripts/stress/k6:/scripts:ro" -v "$SIM_DIR/$RUN:/out" "$K6_IMAGE" run --quiet \
  -e API="$STRESS_API_URL" -e KEY=/out/class-key.json -e LATENT=/out/latent.png -e SUMMARY=/out/summary.json \
  -e VUS="$VUS" -e HOLD="$HOLD" -e RAMP="$RAMP" -e ANALYSTS="$ANALYSTS" -e LAB="$LAB" \
  /scripts/class.js | tee "$RUN/k6.txt" || k6_status=$?

echo "Consistency checks ..."
stress_compose exec -T postgres psql -U trackid -d trackid -A -F $'\t' <scripts/stress/checks.sql | tee "$RUN/checks.tsv"
cp "$STRESS_RUN_DIR"/*.log "$RUN/" 2>/dev/null || true

if [[ "$KEEP" != 1 ]]; then
  scripts/stress/down.sh >/dev/null 2>&1 || true
fi
echo "results: $SIM_DIR/$RUN"
broken="$(awk -F'\t' 'NR>1 && $2 ~ /^[0-9]+$/ && $2 > 0' "$RUN/checks.tsv" | wc -l)"
if [[ "${k6_status:-0}" -ne 0 || "$broken" -ne 0 ]]; then
  echo "class stress did not pass: k6 exit ${k6_status:-0}, ${broken} consistency checks broken" >&2
  exit 1
fi
