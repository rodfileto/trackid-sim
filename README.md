# trackid-sim

A synthetic, **ground-truthed** population simulator for
[trackid](../trackid). It generates people, messy enrollments, offending and
cases; renders their faces into enrollment photos and CCTV evidence frames;
loads them through trackid's own extension contracts (`identity.Ingest`,
`cases.Ingest`); lets trackid's own pipeline embed, match and cluster the
faces; and scores the result against the truth.

The sim writes **no decisions**. SYSTEM decisions come from trackid's matcher;
human review (VERIFICATOR, REVIEWER) is left to the people using the demo, who
can then be scored against the same ground truth.

No real data is used anywhere. Every generated identifier is visibly synthetic
(`SIM-P-…`, `SIM-RG-…`, `SIM-CPF-…`; the last can never validate as a real CPF).

```
world.Generate ──► out/world.json ──► load.Ingest ────────► Postgres + MinIO
 (names, enrol-      (inputs + hidden     media: photos, prints, │
  ments, offending,   ground truth)       CCTV frames, lift      ▼ trackid's queue
  cases, faces)                           cards + boxes     cmd/worker: embed / extract ─► match ─► SYSTEM decisions
                                                                                        └► cluster.Run
                                          eval.Evaluate ◄── clusters  (truth stays in world.json, never in the DB)
```

## Run it

Needs the trackid stack up (`../trackid/scripts/start_dev.sh`, with the
worker's vision models configured) and `go build`ing needs `../trackid`
checked out beside this repo (see `go.mod`). The face pool images live in the
gitignored `tools/facegen/spike_out/`, so `load` with images only works where
they were generated.

The whole cycle on trackid's development database -- erase it, migrate, set the match
thresholds, recreate your super admin (asked for, with its password, before anything is erased),
generate and load -- is one script, which asks you to type the database's name to confirm:

```bash
scripts/reload-world.sh            # -seed N, -humans N
```

Step by step:

```bash
go build -o bin/trackid-sim ./cmd/trackid-sim

bin/trackid-sim generate -out out/world.json                    # deterministic for a given -seed
# On a fresh database, set the demo's match thresholds first (from ../trackid): without a
# version, faces are matched at one cutoff with no review band, and their clusters chain
# different people together (pairwise precision about 0.3 instead of 0.98).
#   go run ./cmd/match-threshold -review 0.6 -confirm 0.7 -source "trackid-sim demo" -commit
#   go run ./cmd/match-threshold -embedding-type FINGERPRINT_SOURCEAFIS -review 40 -confirm 60 -source "trackid-sim demo" -commit
bin/trackid-sim load -world out/world.json -env ../trackid/.env  # waits for trackid's worker
bin/trackid-sim eval -world out/world.json -env ../trackid/.env  # re-score what is in the DB, e.g. after users review
bin/trackid-sim users -env ../trackid/.env -examiners 12        # instructor + coded examiners; key in out/class-key.json
bin/trackid-sim snapshot -env ../trackid/.env                     # clusters before the class, out/snapshot.json
bin/trackid-sim plant -world out/world.json -env ../trackid/.env  # errors on purpose; record in out/plant.json
bin/trackid-sim debrief -world out/world.json -env ../trackid/.env -before out/snapshot.json   # out/debrief.html
bin/trackid-sim frame -case SIM-2016-00003 -evidence 1           # render one evidence (CCTV frame or lift card) to out/frames/
bin/trackid-sim fingerprints -fingers 100                        # impressions for tools/fpscore's matcher check
```

Every loaded case also gets the request it answers (trackid's `requests`): the occurrence's
police station asking by a visibly synthetic ofício (`Ofício <case>`, e.g. `Ofício SIM-2015-00005`), received two days
after the occurrence was registered, due thirty days later, sending the case's evidence files,
asking the standard questions of its nature and naming the task it asks: a facial review of a
facial case, a fingerprint comparison of a fingerprint case (trackid opens nothing on its own). It
is paperwork derived at load time, not a rule of the world, and it is what trackid needs to route
the work and write its reports. No request sends a reference photo yet: references come up on the
case page through matching, and the coordinator adds a comparison's pairs from there. `eval` also scores the identifications examiners made by basis -- a lead, a laudo at or
above the identification level, or a laudo below it -- and `plant -reports` issues lead reports
through trackid's `reports`, which needs `WORDCRAFT_CLI` in `-env`.

`load` refuses to run if `person` or `biometric_cases` holds any row without the
`SIM-` prefix (`-force` overrides; never use it on real data). It is idempotent:
re-running adds no rows, and uploads are content-addressed. `-images=false` loads
`sim://` placeholders instead, and nothing gets matched.

`generate` binds humans to the fixed face pool (`tools/facegen/pool_index.json`:
200 base faces, 146 with a variant that kept its identity). The default 300
humans fit it entirely; a larger `-humans` drops the photos and facial traces
the pool cannot cover, and says how many.

Useful knobs: `generate -humans -offenders -enrolled -duplicates -doubles -seed -lexicon -pool`;
`load -images -wait`. Each facial evidence records its capture level (`good` or
`medium`, 30/70 by default) and degradation parameters in `world.json`.

### Stress test

`scripts/stress/` and `trackid-sim stress` measure a trackid stack under load, on a separate
stack (compose project `trackid-stress`, its own ports and volumes) that leaves the development
one alone. The plan, decisions and results are in trackid's `docs/stress-test-plan.md`.

```bash
scripts/stress/up.sh                                   # fresh stress stack, worker on the GPU (checked)
source scripts/stress/env.sh
bin/trackid-sim stress -humans 600 -senders 8 -window 5m   # one burst; out/stress/<time>-<label>/record.json
bin/trackid-sim stress-report out/stress/*/record.json     # one line per run
scripts/stress/down.sh                                 # stop and erase the stress stack
scripts/stress/sweep.sh -levels "1 2 5 10 20"          # burst levels, a fresh stack each, stops at the limit
scripts/stress/class.sh -vus 30 -hold 5m               # concurrent users with k6, then consistency checks
```

A burst sends the demo world's mix, scaled by `-humans`, from parallel senders within a window,
through the same contracts as `load` (`load.Burst`); every file is drawn before the clock starts,
and kept in `out/stress/cache/` by a hash of the world (`-cache`), so a burst of the same world
again reads the files instead of drawing them (28 min at 10x). After changing how `media` or
`fpgen` draws, delete that directory: the key covers the world, not the drawing code.
Above the pool's 200 identities `-face-reuse` (on by default) lets humans share faces
(`world.ReuseFaces`), so nothing is dropped: such a world measures time and resources, not accuracy.

### Real names (Censo 2022)

A ready-built lexicon for Brazil is committed in `onomastic/data/ibge2022/`:

```bash
bin/trackid-sim generate -lexicon onomastic/data/ibge2022 -out out/world.json
```

It is built by `fetch-lexicon` from the IBGE names API (the service behind
[censo2022.ibge.gov.br/nomes](https://censo2022.ibge.gov.br/nomes/rankings)). To rebuild
it, or build one for a state or municipality:

```bash
bin/trackid-sim fetch-lexicon -out out/lexicon -localidade SP   # 0 = Brazil, a state, or a 7-digit municipality code
bin/trackid-sim generate -lexicon out/lexicon -out out/world.json
```

- Names carried by at least `-min-per-million` (default 5) people per million
  inhabitants are kept; the rankings are saved as `ranking_*.csv` snapshots and reused
  on reruns for the same locality and floor (`-refresh` fetches again).
- Given names come from the female and male first-name rankings. `-min-sex-share`
  (default 1%) leaves a name off a sex's list below that share (e.g. the few women
  recorded as "Jose").
- IBGE counts every word after the first given name as a surname, so its surname
  ranking also holds middle names and suffixes. Suffixes (Junior, Filho, Neto,
  Sobrinho) are left out, and so is any word at least `-middle-name-ratio` (default 2)
  times more common as a first name (Maria, Jose; Rosa and Conceicao stay).
- Everything left out is listed in `dropped.csv`. Accents are stripped by the source.

### Geography (Rio de Janeiro)

Where occurrences happen comes from real data, committed under `world/data/rio/`
with its provenance (raw downloads and geocoder answers are cached in `out/cache/`):

```sh
bin/trackid-sim fetch-isp     # ISP-RJ counts per police-station area (CISP), last 12 months + CISP boundaries
bin/trackid-sim fetch-banks   # BCB bank branches, geocoded with Nominatim (1 request/s; -limit N for a trial)
```

Occurrence types follow ISP-RJ's classification; see [docs/OCCURRENCE.md](docs/OCCURRENCE.md)
and, for the licences, [docs/LICENSES.md](docs/LICENSES.md).

## What is modelled

| Layer | Package | Model |
|---|---|---|
| Names | `onomastic` | Compound given names; children inherit mother's then father's last surname; siblings share parents; undeclared fathers; particles (*da/dos*). Records carry the human's data exactly: no typos, abbreviations or wrong birth dates, since this world is not for testing data entry (removed 2026-11-05). |
| Population | `world` | Enrollment is more likely for offenders. Four enrollment kinds: `primary`, `renewal` (same person, new document), `duplicate` (same human under a **different** person: an RG from another state, normal) and `impostor` (*Double identities*, below). Heavy-tailed (discrete Pareto) events per offender, habitual co-offending partners, bystander traces, offenders who are never enrolled. |
| Prints | `fpgen`, `media` | Each human has one finger, a master print drawn from (seed, human id) by an SFinGe-style generator (Go port of `tools/fpgen`). Every NIST file is a new rolled impression of it; a fingerprint evidence is a lift card with one latent per trace, each boxed by its tile. SourceAFIS scores match the Python prototype's (rolled genuine median ~680, latent genuine 39.5% at threshold 40, impostors ~0%). |
| Images | `media` | Enrollment photos are the pool's base faces as they are. A facial evidence is one CCTV frame: each trace's person (their kept variant) stands behind a counter against a plain wall, scaled so the face is the capture level's size, then blurred, noised and JPEG-compressed as `tools/facegen/degrade.py` does, with a DVR timestamp inside the occurrence's time window (so a frame never postdates the request that sent it). Each trace's box is where its face was placed. |
| Scoring | `eval` | Pairwise precision/recall/F1 vs true identity, impure clusters, fragmented humans, identification outcomes for questioned traces (correct / wrong person / unresolved), and unknown subjects falsely linked to an enrolled person -- over whatever trackid holds: its matcher's decisions, and users' reviews once they make them. |

### Double identities

Decided 2026-11-05. The usual document fraud is not a second record under the same person: it is a document of its own
copying someone else's data. Human A holds a driver's licence, or an RG from another state, with human B's name, parents
and birth date exactly, and A's own face and fingerprints. Nothing in trackid merges people, so the document gets a person
of its own, and what ties it to B is the data it copies. `-doubles` sets how many (5% of humans), and exact shares vary the
rest, half each by default: `-fraud-licence` (a licence, else an RG from another state), `-fraud-fiscal` (it carries B's CPF,
else none), `-fraudster-enrolled` (A also has a record under their own name, else nowhere but here). A crime A commits
resolves, on paper, to B. Where A has their own record, their cluster holds two names; where A does not, the only trace is
one identity with two sets of biometrics. trackid's identity conflicts look for both (its docs/ask-plan.md).

## What is NOT modelled (yet)

- **One finger per human**, not a ten-print: every NIST file and every lift is
  the same finger. trackid matches templates exhaustively, so ten fingers would
  cost an hour or more of matching per load. Matching prints needs trackid's
  `sourceafis-sidecar` running and `FINGERPRINT_SIDECAR_URL` set for the worker.
- The bundled default lexicon is a **placeholder** (approximate ordering, Zipf
  weights). Pass `-lexicon onomastic/data/ibge2022` for Censo 2022 frequencies;
  census names carry no accents.
- Scores come from real matchers (AuraFace, SourceAFIS) on synthetic images, so
  their error rates say nothing about those matchers on real captures, and
  students' accuracy says nothing about trained examiners; use the simulator to
  study *system behaviour* (stability, auditability, effect of review policy),
  not biometric accuracy. Always sweep the parameters the simulator sets.
- No revocation/split scenarios yet (see `docs/PLAN.md`).

## Ground truth

`Enrollment.HumanID`, `Enrollment.Kind`, `Trace.HumanID`, `Trace.Bystander` and
`Case.EventID` are truth. They live only in `world.json`. Synthetic rows are
identifiable by their `SIM-` identifiers, and cases and occurrences by a
description starting with `SIMULATED`.

## Development

```bash
go vet ./... && go test ./...
```
Unit tests cover determinism, world invariants, name frequencies against the
lexicon, and the metrics. The end-to-end path needs a live stack.

## Licence

Apache-2.0 (see [LICENSE](LICENSE)); cite it with [CITATION.cff](CITATION.cff).
The models, name data and boundaries the simulator uses keep their own terms,
recorded in [docs/LICENSES.md](docs/LICENSES.md). The released corpus will carry
its own data licence (planned: CC-BY-4.0, once every generator's terms are
re-read; ROADMAP block E).
