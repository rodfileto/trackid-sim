# Implementation roadmap

Concrete tasks behind the phases in [PLAN.md](PLAN.md). Work top to bottom (the repo stays in development until the last block); each block
lists what to build and how to know it is done.

## A. Fixes in trackid core (tracked here because the simulator depends on them)

One migration (then `024_biometric_decisions_byte_order.sql`; since the squash, part of `005_create_biometric_decisions.sql`) + code change in `../trackid`:

- [x] Index `biometric_decisions (feature_b_id, feature_a_id)`; lookups from the b side currently scan the whole index.
- [x] Make the order check byte-wise: `feature_a_id COLLATE "C" < feature_b_id COLLATE "C"`, and re-order any existing rows where en_US and byte order disagree.
- [x] A view (`biometric_decision_sides`) returning each decision in both directions (`feature_id`, `counterpart_id`, role, decision, ...), and use it in the readers.
- [x] `cluster.Identify` should return distinct edges written, not input rows. (Moot since 2026-10-27: `cluster.Identify` was removed with Neo4j, below.)
- [x] Once the check is byte-wise, replace `LEAST/GREATEST` in `load/load.go` with a plain Go `<`.

**Done when:** a regression test inserts `TRACE:1#feature` / `TRACE:10#feature` through `biometricmatch` without error.

Found 2026-10-11 while running the demo with fingerprints (open, in `../trackid`):

- [x] `scripts/start_dev.sh` / `stop_dev.sh` do not start or stop the `sourceafis-sidecar` container, so the worker has no fingerprint matcher unless it is started by hand (`docker compose up -d sourceafis-sidecar`).
- [x] The API on :8082 serves the prebuilt `frontend/dist`, which nothing rebuilds. A build from 2026-09-18 (before the `biometric_cases` rename) still read the modality from `caseType`, so every case showed as "Impressão digital" and facial cases lost their face workspace -- the data and the API were right (`modality: "FACIAL"`). Rebuilt locally (`npm run build`); either rebuild it in `start_dev.sh` or stop serving it in development, where Vite on :5173 is the current frontend. Note for SSH sessions: :8082 listens on all interfaces and gets auto-forwarded, :5173 only on localhost and may need forwarding by hand. **Fixed 2026-10-19:** `start_dev.sh` starts the sidecar when `FINGERPRINT_SIDECAR_URL` is set and waits on its `/healthz` before starting the worker (which checks it only at startup), rebuilds `frontend/dist` when any frontend source is newer than its `index.html`, and `stop_dev.sh` stops the sidecar. Checked with a full start/stop: the worker registered the fingerprint handlers, and :8082 served a fresh bundle.
- [x] A review band for fingerprints, like the face one (2026-10-12, branch `fingerprint-review-band`, not merged): migration 010 relaxes `match_thresholds`' check to `0 < review <= confirm` (SourceAFIS scores are unbounded; the `<= 1` rule moves to `Band.ValidateSimilarity` for embeddings), `biometricmatch.RunTemplatesBand` classifies each pair POSITIVE at or above confirm and INCONCLUSIVE from review up, citing the version, `fingerprint.RunSync` reads the current version on every run (`DefaultBand` = single cutoff 40 when none is set), and `cmd/match-threshold -embedding-type FINGERPRINT_SOURCEAFIS` sets it. Tested with a fake matcher (`TestRunTemplatesBand`). **Version #2: review 40, confirm 60.** Measured on a clean reload of the seed-1 world (below).

Changed in `../trackid` on 2026-10-27 (`55f83ec`):

- [x] **Neo4j removed.** The graph sync, the Cypher migrations, `cluster.Identify` and the `identify`/`sync-graph`/`sync-identity` commands are gone; the clusters in Postgres already give the identification, and `cluster.Run` no longer persists to a graph. The sim followed (`d3289c1`): `load` no longer runs identify, `connect` no longer opens Neo4j, and `.env` needs no `NEO4J_*` settings.
- [x] **Facial reports.** Requests (`requests`: who asks, by which document, the questions, the files sent), questioned vs reference faces, suitability, a 7-level conclusion scale on FACIAL_COMPARISON, lead reports and laudos written in WordCraft and issued as PDF (`reports`), video as material (`video`). The sim followed (`d3289c1`): `load` registers one request per case, `eval` scores links by basis (lead vs laudo), and `plant -reports` issues real lead reports (needs `WORDCRAFT_CLI`).

## B. Phase 1: realistic names

- [x] `onomastic/ibge.go` + a `fetch-lexicon` subcommand that writes `given_female.txt`, `given_male.txt`, `surnames.txt` with `name,weight` lines. Source: the IBGE Censo 2022 names API (first-name rankings per sex and the surname ranking), for Brazil, a state or a municipality. The 2010 tables were dropped: they had no sex and no surnames.
- [x] Surnames: IBGE counts every word after the first given name, so suffixes (Junior, Filho, Neto, Sobrinho) and words far more common as a first name (middle names like Maria) are left out and listed in `dropped.csv`.
- [x] ~~Condition given names on birth decade~~ Decided against: one 2022 snapshot.
- [x] ~~Calibrate `Registry` / `Police` corruption rates~~ Decided against: corruption rates are the user's parameters, not calibrated by the simulator.
- [x] Report the full-name collision rate in the `generate` summary (humans sharing their true full name with another human).

**Done when:** a test compares sampled name frequencies to the source table within tolerance. Done in `onomastic/frequency_test.go`: drawn names match the committed lexicon within 5 s.e., and the lexicon is exactly what the defaults make of the committed ranking snapshots.

## C. Phases 3-4: images and real scores

Faces are generated by us: **FLUX.1-schnell** makes one base face per human and **Qwen-Image-Edit** makes other images of that same face (both Apache-2.0 per their Hugging Face model cards; re-read before release). Scores come from trackid-vision's **YuNet** detector and **AuraFace** recognizer, selected explicitly: up to trackid-vision v0.3.0 `vision.Config.Detector` defaulted to `DetectorSCRFD`, whose InsightFace weights are non-commercial and fail the app track (YuNet is the default since 2026-10-20; see LICENSES.md). Every licence is recorded in [LICENSES.md](LICENSES.md). 

Constraints found while planning:

- ~~`cases.Ingest` cannot attach media~~ Fixed in C2: `EvidenceInput.File` and `TraceInput.Box`/`Crop` (`cases.FileInput`, `cases.Box`). The schema already carried everything needed (`case_files`, `case_evidences.case_file_id`, `case_traces.box_x1..y2`/`detection_score`/`case_file_id`, `GetCodificationSource`'s codification-image/face-crop/evidence-image+box priority) -- only `Ingest` itself was not wiring it through. `cases.CreateTraces` is still not a substitute for re-loading (it appends new traces on every call and makes its own `case_evidences` row, which breaks the `(case, evidence, trace)` lookup in `load.Features`).
- The enrollment side is ready: `identity.FileInput` takes a `StorageRef` and `ComputeForIdentityFeature` embeds the whole photo.
- ~~`biometricmatch.Run` writes its own SYSTEM POSITIVE rows, so `decide` must be split into a machine stage and a human-review stage.~~ Superseded 2026-09-22: `decide` is gone. SYSTEM decisions come from trackid's own pipeline and human review is done live by users (see C4).
- Local hardware is an RTX 2080 (8 GB, no bf16), 60 GB RAM, and the card also drives the display. Measured in C0, not assumed: the GPU is **not** the binding constraint. With the fp16 transformer held in RAM and streamed to the card per leaf module (`enable_group_offload(..., use_stream=True)`), a 512 px base face costs 8.7 s at 0.76 GB of VRAM; the same config without `use_stream` costs 53.6 s, so the whole difference is transfer hiding behind compute. What binds is **host RAM and PCIe**: streaming pins ~24 GB of non-swappable host memory per model. See [tools/facegen/README.md](../tools/facegen/README.md) for the guardrails both limits need.

**C0. Spike and licence gate (do first)**

- [x] Isolated Python environment outside the Go build (`uv`, Python 3.12, torch, diffusers) under `tools/facegen/`.
- [x] ~~About 30 humans: base face from FLUX.1-schnell (quantized, 512 px)~~ **200 base faces** at **8.6 s each**, 28.6 min, no failures (fp16 streamed, not quantized; quantizing on an 8 GB card is what fails). `gen_pool.py` + `demographics.py` + `pool_manifest.json`. The first 30-face pool was deleted and regenerated at 200 once the prompts were driven by IBGE shares.
- [x] ~~2-3 variants each from Qwen-Image-Edit~~ **200 variants, one per face**, on a rented RTX 4090: pre-quantized GGUF Q4_K_M (12 GB against 38 GB fp16), resident on the card, the Qwen2.5-VL encoder on the GPU in its own stage. Proven locally first (`spike_qwen.py`, 20B streamed in 0.47 GB resident) but at 26.6 min/image in fp32, since the 2080 has no bf16. One variant per face rather than 2-3 was a choice for the demo, not a limit: `--per-face 2` gives one expression-led and one angle-led variant per identity.
- [x] Embed with trackid-vision and plot genuine vs impostor cosine scores (`tools/facescore`, a separate Go module so the sim build stays free of cgo/onnxruntime). Impostor pairs are matched per quality level — a clean/clean baseline against clean/degraded genuine pairs measures domain, not identity.
- [x] Degrade into CCTV-like captures (`degrade.py`, three levels, 1920x1080). YuNet detected the face in **100%** of 600 captures, down to a 48 px face. The frame is the portrait pasted on flat grey with a hard seam, which is fine for scoring and will not do as a displayable evidence image in C3.
- [x] Drop variants where the edit changed the person; report the drop rate. **27.0% (54/200)**: 47 identity lost, 7 never reframed, 0 undetected. 146 usable variants, which is the corpus.
  - The variant is a **full-length register photograph in different clothing**, not another head-and-shoulders portrait. Asking an edit model for the same framing it was given is close to an identity transform and it returns one: two attempts scored +0.937 and +0.932. A full-length reframe cannot be satisfied by re-rendering, and 20 of 20 came back as genuine new photographs.
  - **One primary transformation per variant**, half expression-led and half angle-led, each as an imperative at the front of the prompt. The model obeys the first instruction and drops what competes with it: a prompt asking for an expression *and* an angle produced the reframe every time, the expression once, and the angle never. Splitting them also stops the two factors being confounded.
  - The check has **three** parts, each of which had to be corrected after a measured failure:
    - *Identity lost*: score at or under the **variant-matched** impostor p99.9. Scoring a full-length variant against the clean/clean baseline measures the domain gap, not identity, and rejected a variant that was plainly the same man (+0.645 against a +0.702 floor built from the wrong pairs).
    - *Never reframed*: structural, not cosine. A face embedding ignores framing, clothing and background by design, so a genuine new photograph scores ~0.95 exactly as a re-render would; an embedding ceiling called 12 of 20 real full-length shots near-duplicates. The test compares the detected face's share of the frame against its base face's, and caught 7 in the full run — two at +0.929 and +0.934, which no cosine threshold would have flagged.
    - *Thresholds are quantiles, not extremes*. The maximum of ~20k impostor pairs is one pair and climbs with pool size: going from 30 to 200 faces moved it from +0.659 to +0.763 with no change to the generator. p99.9 is stable as the corpus grows.
  - Drop rate per condition, from 17-28 variants each:

| condition | kept | drop | mean cosine | drop rate |
|---|---|---|---|---|
| angry | 14 | 9 | +0.690 | 39% |
| smiling | 12 | 7 | +0.729 | 37% |
| chin_up | 16 | 8 | +0.762 | 33% |
| threeq_r | 19 | 9 | +0.752 | 32% |
| chin_down | 19 | 7 | +0.791 | 27% |
| surprised | 16 | 5 | +0.794 | 24% |
| disappointed | 14 | 3 | +0.776 | 18% |
| tired | 17 | 3 | +0.788 | 15% |
| threeq_l | 19 | 3 | +0.800 | 14% |

  - **The per-condition ordering is suggestive, not established.** At this sample size the headline gaps do not reach significance: angry 39% vs tired 15% is Fisher p = 0.091, and threeq_l 14% vs threeq_r 32% -- a 2.3x gap between mirror-image transformations -- is p = 0.186. The overall 27% is well estimated at n=200; separating conditions would need roughly 80 each. Report the total, not the ranking, until then.
  - Consequence for C3: a full-length frame puts the face at ~80-120 px natively, already about the `medium` capture level, so the degradation ladder cannot simply be stacked on top of these.
  - Cost: 200 variants in 350 min on a rented RTX 4090 at 105 s each, about $4.50.
- [x] [LICENSES.md](LICENSES.md): each model's terms and where read, the datasets and models rejected and why, and the wording used about "synthetic". Qwen-Image-Edit's own text is still to be read in place.
- [x] Decide local vs rented GPU from the measured throughput: **local** for base faces (200 in 28.6 min), **rented** for the variants (Qwen measured at 26.6 min/image locally; see below). Placement is chosen by `tools/facegen/hw.py` from the device rather than by a flag, so one set of scripts runs on both; the rented half is [tools/facegen/runpod/](../tools/facegen/runpod/README.md).

**Go/no-go, recorded 2026-09-20: GO.** Identity survives an edit, which was the
gate the whole face layer rested on. A three-quarter-view variant of `face_000`
scored **cosine +0.765** against its base face — above every one of the 435
impostor pairs then available (worst case +0.659), detector score 0.91.

That decision stands, but the pool it was measured on was replaced the same day
(see "Second pool" below) and its images are no longer on disk; the numbers in
this paragraph are a dated record of how the gate was passed, not a description
of the current corpus. The +0.765 is also worth reading against the thresholds
the larger pool produced: it would clear the new floor by 0.002.

**Second pool, 2026-09-20: 200 identities.** The 30-face spike pool was deleted
and regenerated at 200, with attributes sampled from IBGE Censo 2022 rather than
from hardcoded shares (`tools/facegen/fetch_shares.py` →
`censo2022_shares.json`, `demographics.py`). Colour, sex and single-year age are
sourced; skin tone, hair, facial hair, build and expression are ours, since IBGE
does not publish them. 200 faces in 28.6 min at 8.6 s each, no failures.

| | asked for | IBGE Censo 2022 |
|---|---|---|
| parda | 45.5% | 45.34% |
| branca | 43.5% | 43.46% |
| preta | 10.0% | 10.17% |
| indígena / amarela | 0.5% / 0.5% | 0.60% / 0.42% |
| female | 52.0% | 51.87% |

Measured on 200 faces, cosine similarity from AuraFace, impostor baseline
matched to each level. Detection was **100% at every level, including 48 px**:

| Capture | Face size | genuine | impostor | d' | |
|---|---|---|---|---|---|
| good | 200 px | +0.950 | +0.246 | 8.37 | clean gap 0.119 |
| medium | 100 px | +0.803 | +0.211 | 6.97 | overlap |
| poor | 48 px | +0.256 | +0.097 | 1.86 | overlap |

d' rose at every level against the 30-face pool while the clean gaps shrank, and
`medium` lost its gap entirely. Both are the same effect: 39,800 impostor pairs
per level instead of 870 reach much further into the tail, so the impostor
maximum climbs (+0.649 → +0.769 at `good`) with no change to the generator. The
earlier clean gap at `medium` was an artefact of small n. **A threshold read off
a maximum is therefore not a threshold** — it moves with pool size — so the
variant rules now use quantiles: floor at the impostor p99.9, ceiling at the
same-file p0.1, both reported with the extreme alongside.

| | 30 faces | 200 faces |
|---|---|---|
| floor (identity lost) | max +0.659 | p99.9 **+0.702** (max +0.763) |
| ceiling (near-duplicate) | min +0.885 | p0.1 **+0.888** (min +0.888) |

**Prompt-level tone control, and what it did not fix.** The first pool rendered
lighter than its prompts asked for. The second leads each prompt with a physical
tone descriptor ("deep brown skin") ahead of the ethnonym, which measurably
separates the groups — ITA (Individual Typology Angle, `tools/facegen/ita.py`)
over all 200 faces:

| asked | n | median ITA | band of the median |
|---|---|---|---|
| branca | 87 | +33.4 | intermediate |
| parda | 91 | +4.1 | brown |
| preta | 20 | −34.2 | dark |

Monotonic and well separated, so the attribute is doing work the first pool's
phenotype word did not. Two things it does not establish. The bands overlap at
the edges (5 of 87 `branca` measure `brown`, 3 of 91 `parda` measure
`intermediate`), so the recorded attribute is not a reliable description of the
rendered image and must never be read as one. And ITA is illumination-sensitive,
while these are uniformly studio-lit synthetic images, so the absolute bands are
not comparable to measurements on real photographs — only the separation between
groups is. PLAN.md's exclusion of demographic claims is unchanged by any of this.

Also observed and unfixed: FLUX renders young faces as conventionally
attractive, symmetrical and flawlessly lit, far more uniform than real
enrollment photographs. The effect is much weaker on older faces. It is a
limitation to state, not a defect to prompt around.

**Local vs rented GPU, decided by measurement.** Base faces stay local: 8.7 s
each is cheaper than renting. Variants do not. This card has no bf16 and Qwen
was trained in bf16; in fp16 the transformer emits NaN at step 0 and the VAE
decodes to solid black, so compute is forced to fp32 at **53.1 s/step — 26.6 min
per image**, i.e. ~40 h for 90 variants. Any Ampere-or-newer card runs bf16
natively *and* holds the 12 GB of quantized weights resident instead of
streaming them, which is ~1-3 s/step: the same 90 variants in about 2 h for a
few dollars. Batching several faces per forward pass is the local fallback,
since it amortizes the weight transfer that dominates here (untested).

Implemented as a rented RTX 4090, driven from
[tools/facegen/runpod/](../tools/facegen/runpod/README.md): push code and base
faces, `setup.sh`, `gen_variants.py`, pull the variants, score locally. The pod
runs the **same weights, seeds and prompts** as a local run — only placement
changes. **Measured: 200 variants in 350 min at 105 s each, about $4.50**, with
the transformer resident at 12.47 GB and the Qwen2.5-VL encoder on the card.
A detached `autopull.sh` rsyncs results home every 5 minutes, because a pod
without a persistent volume takes everything unpulled with it.

Getting there cost four pods, and each failure is now a guard in the code:

- **Blackwell (sm_120) has no GGUF fast path.** On an RTX 5090 the identical
  code dequantised the Q4_K_M weights on the CPU instead of keeping them
  quantised on the card — one core pinned, host RSS past 25 GB against a 12 GB
  file, GPU idle after 20 minutes. Expanded to bf16 those weights are ~40 GB
  and fit no consumer card. The card reports bf16 and passes every other check,
  so `hw.py` now refuses sm_120 by compute capability (`gguf_fast_path`).
- **The host driver is a lottery.** RunPod's CUDA selector and the image's CUDA
  do not determine the driver the pod lands on, and the PyPI torch 2.14 was a
  cu130 build needing a ≥580 driver: a perfectly good RTX 4090 on a 12.8 driver
  reported "no CUDA device". torch is now pinned to the **cu128** index, which
  runs on 12.8 *and* 13.x drivers, so the driver stops being a variable. That
  meant torch 2.14 → 2.11, since the cu128 index carries nothing newer;
  verified on both the local 2080 and the pod, GGUF path unchanged (12.47 GB
  resident either way). `hw.py` now names the driver mismatch instead of
  claiming there is no GPU.
- **The `ssh.runpod.io` proxy has no exec channel**, so rsync and non-interactive
  commands cannot run over it. Expose TCP 22 and set `PUBLIC_KEY` at creation.
- **A network volume pins the pod to one datacenter**, which is where GPU
  availability bites; the first restart failed for exactly that reason. The
  28 GB re-download costs about $0.13, so a single run does not need one.

**What the variant run settled.** The spike's genuine pairs were a base face
against a degraded copy of *the same file*, so they shared identity by
construction and demonstrated recognizer robustness, not that the generator can
produce a *new* image of the same person. The variant run answers that directly:
146 of 200 full-length register photographs — in different clothing, with an
altered expression or head angle — are recognised as their base face above
every impostor bar the top 0.1% of their own domain. The fallback this
paragraph used to hold open (a faceless corpus, with P5 moved to an external
benchmark) is not needed.

Also observed: the pool renders **lighter-skinned than its prompts asked for**,
which were shaped to Censo 2022 shares. Direct evidence for the PLAN principle
that demographic bias is out of scope for any claim built on this corpus.

**C1. Face pool bound to the world**

**Decided 2026-09-22: the corpus is fixed** at C0's 200 base faces (enrollment photos) and 146 kept variants (camera captures); no more generation on a rented GPU. The world is fitted to the pool instead of the pool being generated from the world. Generating per human would have needed ~2540 faces and ~74 h of rented GPU for the old 5000-human default.

- [x] Bind pool faces to world humans. `tools/facegen/pool_index.py` joins `pool_manifest.json` with the identity-check verdicts (from the gitignored `tools/facescore/scores.json`) into the committed `pool_index.json`; `world.AssignFaces` (run by `generate -pool`, on by default) gives each human who needs one a face of their sex, ages matched by minimum total gap, and records the pool's sha256 in `world.json`. A human captured on camera needs a face with a kept variant. When the pool is short, double-identity pairs are served first, then captured humans, then enrolled-only; photo files and facial traces of humans left without a face are removed (and cases left empty), and `generate` says how many.
- [x] Default world down to **300 humans**, sized to the pool: at seed 1 with the IBGE lexicon, 152/152 humans needing a face get one (44 captured on camera, 15 double-identity pairs), mean age gap 1.8 years. At 1000 humans the pool covers 200 of 544 and drops 379 photos.
- [x] Adults only, with birth years from the Censo 2022 adult table (the same one the pool was prompted from, embedded in `world/data/` with a test that it has not drifted). Ages are taken on 1 January of `YearFrom` (a 2022 table applied to 2015, an approximation). One sibling per family is drawn from the table and the others are spaced 1-3 years around them; each 10-year band stays within 0.8 points of Censo (`TestAgesAreAdultAndFollowCenso`).
- [x] **Double identities** (`-doubles`, default 5% of `Humans`): two enrolled humans A and B, each with their own record, and A *also* holds a record under B's person_id with B's name, parents and birth date but A's face and fingerprints (enrollment kind `impostor`, `Human.Impersonates` on A). That gives two checks something to find: **A's biometric cluster carries two names** (A's own and B's), and **B's person carries biometrics of two humans**, so a crime A commits resolves, on paper, to B. A is drawn from enrolled offenders first (at seed 1 all 15 are offenders and 12 leave traces); B is an enrolled human of A's sex, closest in age; nobody is in two pairs. All three records carry a photo and a ten-print, because the point is that biometrics contradict the record. *Superseded 2026-11-05: the document now has a person of its own, is a licence or an RG from another state, and A need not have another record; see README.*
- [x] **Kept the variants, decided 2026-10-11.** Evaluated dropping them (the rented-GPU, 27%-drop stage) for evidence frames built from the local base faces plus the degradation chain alone; rejected because the variants give each person a body to stand in the scene, which a head-and-shoulders portrait does not.
- [x] ~~Identity check with regeneration inside the generation loop~~ and ~~caching the FLUX prompt embeddings~~: dropped along with further generation. The 54 variants that failed are simply faces without a variant; they go to humans who are enrolled but never captured.

**C2. trackid core: media in `cases.Ingest`** (a change in `../trackid`, like block A)

- [x] Optional storage ref on `EvidenceInput` and box/crop on `TraceInput`, set by the same upserts. No migration needed: `case_files`, `case_evidences.case_file_id` and `case_traces.box_x1..y2`/`detection_score`/`case_file_id` already existed (added for the UI's manual detect/crop/codify flow) and `GetCodificationSource` already read them in priority order -- only `cases.Ingest` itself didn't set them. Added `cases.FileInput` (a `StorageRef` a caller already uploaded via trackid's storage client, plus content type/size/hash -- `Ingest` stays a DB-only transaction, no cgo/storage dependency) and `cases.Box`; `EvidenceInput.File` (category `evidence`) and `TraceInput.Box`/`Crop` (`Crop` is category `face_crop`) are all optional and independent, matching every existing caller's zero-value literals.

**Done when:** re-ingesting a case with a box leaves one trace and `embedding.ComputeForCodification` embeds it. Verified the DB-only half directly (`cases/ingest_test.go`, `TestIngestEvidenceFileAndTraceBox`, against a real Postgres): two `Ingest` calls with the same evidence file and trace box leave exactly one `case_evidences`/`case_traces`/evidence `case_files` row, and `GetCodificationSource` -- `ComputeForCodification`'s own input -- resolves the stored `StorageRef` and box. Exercising `ComputeForCodification` itself needs a running storage backend and vision model; left to an integration test (block E).

**C3. `media` package and load step**

- [x] `media` renders faces in Go from the pool, deterministic per (seed, case, evidence). An evidence frame puts each trace's variant behind a counter against a plain wall -- no cut-out: a colour key failed on the studio halos and grey shirts, so each backdrop is shifted onto the wall colour from its left, right and top border strips and feathered in -- then degrades it as `degrade.py` does. The variants are waist-up (not full-length) with faces of 139-434 px, so the levels only scale down. `poor` (48 px) was dropped: the frame was mostly empty wall. Each facial evidence records its `Capture` (level and parameters) in `world.json`, `good` 30% / `medium` 70% (`GoodCaptureRate`), drawn from its own stream so the rest of the world did not change. Face boxes come from the detection run, now in the committed `pool_index.json`. Trialled on one evidence, then five (1-4 people, face_001's cream shirt as the hard case) before wiring.
- [x] A facial evidence holds each person once: a repeat capture goes to another evidence (`world.freeBucket`); 7 of 33 evidences had the same person twice before.
- [x] Uploads through trackid core, which owns the object layout: `identity.UploadFile` (`identity/<register>/<file_type>/<sha256>`) and `cases.UploadEvidenceFile` (`evidence/<case>/<sha256>`), added in `../trackid`.
- [x] `load.Ingest`: enrollment photos are the enrolled human's base face; each facial evidence gets its frame and each trace its box. NIST files stay `sim://` placeholders.
- [x] Fingerprint half (2026-10-11): `fpgen` (Go port of `tools/fpgen`, see Phase 4 below) and `media.Prints`. **One finger per human**, drawn from (seed, human id): every NIST file is a new rolled impression of it (stream: register, sequence), and a fingerprint evidence is a PNG **lift card** with one latent per trace, side by side in trace order, each trace's box its tile. One finger rather than ten because `biometricmatch.RunTemplates` matches every template against every other: ~200 templates take about a minute, a ten-print world (~1,650) an hour or more. `load` uploads both through trackid's helpers, and `frame -case` renders a lift card for checking by eye. Binding checked offline on the seed-1 world with SourceAFIS: a person's rolled impressions across renewals, duplicates and double-identity records match each other 39/39 (median 682) and other people 0/1,056; their lifts match them 9/20 (45%) and other people 0/320. Not yet loaded through trackid's live pipeline (see Phase 4's pipeline item).

**Done when:** the UI shows enrollment photos and evidence images with boxes. Data verified 2026-09-22 (166 photos, 40/40 facial evidences with a frame, 47/47 traces with a box); seeing it in the UI is the user's check. With fingerprints (2026-10-11), NIST files are rolled impressions and fingerprint evidences lift cards with a box per latent; the case page showed the wrong modality until the stale `frontend/dist` was rebuilt (see block A).

**C4. trackid's own pipeline instead of `decide`** (replanned 2026-09-22)

The sim no longer simulates decisions. The machine stage is trackid's: loaded faces enter its queue and go ingest → embedding → `biometricmatch.Run` (SYSTEM POSITIVE at cosine 0.4, which alone is CONFIRMED) → `cluster.Run`, exactly as an organization's import would. Human review is not simulated at all: users do it live in the UI, for learning, and `eval` can score their reviews against the same ground truth. This drops the old plan (split `decide`, embed in-process without Redis): the demo runs the real system.

- [x] `cases.WithEnqueuer` in `../trackid`, mirroring `identity.WithEnqueuer`: enqueues each codification whose trace has an image (its crop, or a box on an evidence with a file).
- [x] `load` ingests with trackid's queue, faces only at first (`load.FaceTasks`, removed 2026-10-11 once NIST files had images), waits for the worker (`load.Wait`: queue empty and every face embedding matched, nudging a sync when one was folded away), then runs identify, which is an operator step in trackid and not automatic. (Since 2026-10-27 there is no identify step: it went with Neo4j, block A, and the clusters give the identification.)
- [x] `decide` deleted; `eval` scores whatever trackid holds.
- [x] First run against the real pipeline (2026-09-22): 213/213 faces embedded (YuNet found every one), 267 SYSTEM POSITIVE from trackid's matcher, face recall 0.99 (135/136 true pairs) but precision **0.08** (1477 false pair links, 12 impure clusters); identify 26/27 correct, 1 unknown subject falsely linked. Cause: 134 of the 267 SYSTEM decisions join two different people. Every candidate scored ≥ 0.6 against trackid's fixed 0.4 threshold; this pool's impostor pairs sit at median 0.64, max 0.76 (C0's shift), so at 0.70 the same decisions keep 112/133 genuine and 22/134 impostors. The load needed `VISION_DETECTOR=yunet` with the YuNet model in `../trackid/.env` (it pointed at SCRFD's file).
- [x] Face threshold as versioned data with a review band, in `../trackid` (`match_thresholds`, migration 009; `biometricmatch.RunBand`; `cmd/match-threshold`). Correction to the item above: trackid's 0.4 was a cosine *distance*, i.e. similarity 0.60, which is why every candidate scored ≥ 0.6. Cutoffs are now similarities on the `confidence` scale: at or above confirm a SYSTEM POSITIVE (CONFIRMED), between review and confirm a SYSTEM INCONCLUSIVE (PENDING_REVIEW, the users' queue), each decision citing its version; with no version set the worker keeps similarity 0.60 with no band. Demo version #1: review 0.60, confirm 0.70 (C0's floor for this pool). Reloaded 2026-09-22: 134 SYSTEM POSITIVE (0.702-1.000) and 133 INCONCLUSIVE (0.602-0.699), exactly as predicted from the first run's scores; machine-only face precision **0.81** (from 0.08), recall 0.90, 28 false pair links (from 1477), 6 impure clusters, identify 19/27 correct with 0 wrong and 8 unresolved (their genuine links are in the review queue: 21 genuine among the 133), 0 unknown subjects falsely linked.
- [x] Re-checked 2026-10-11 by re-scoring the stored 2026-09-22 load with `eval` (no reload): precision 0.8133, recall 0.8971, 28 false pair links, 6 impure clusters, identify 19/0/8, 0 of 20 unknowns falsely linked, 134 POSITIVE (0.702-1.000) and 133 INCONCLUSIVE (0.602-0.699), threshold version #1 in place -- all as recorded above. The fingerprint reload the same day left the face numbers unchanged.
- [x] Code review the same day: `load.Wait` called its logger unguarded on the sync-nudge path (fixed with the fingerprint wait); `cmd/trackid-sim`'s package comment still described simulated decisions (fixed). `eval.CompareSnapshots` is still unused by any command (now a block D item); `load` has no tests (block E).
- [ ] A sim `roc` command: score every face pair in trackid against the ground truth, plot the ROC, fit a calibration and propose a threshold version with its provenance.

**Done when:** a load leaves trackid with SYSTEM decisions from its own matcher and clusters from them, and `eval` scores them. Fingerprints join the same flow after C3's fingerprint half.

**C5. Quality and reporting**

- [ ] Measured quality per image in `world.json` (OFIQ is C++, so either run it as an external step or start with a simple proxy: detector score, eye distance, blur); report metrics stratified by quality.
- [ ] A cheap first step before OFIQ: trackid now records the examiner's own suitability per questioned face (SUITABLE / LEAD_ONLY / UNSUITABLE, `examination.AssessTrace`). `eval`/`debrief` can set it against the capture level the world already records (`good`/`medium`). That is a human judgement, not a measured quality, so it complements this item rather than closing it.
- [ ] State in the report what synthetic cannot show: absolute error rates and demographic bias (the generators carry their own skew).

**Phase 4 (fingerprint images and matching)** was images-only, for the UI and manual ACE-V; the SourceAFIS decision below adds a real machine matcher too, so it no longer stops at images. The image generator's bar is unchanged, **not realism**: prints that look and behave like fingerprints are enough for manual ACE-V and the matcher check below -- forensic-grade prints are not the goal.

- [x] Generator: our own SFinGe-style pipeline rather than SFinGe/Anguli (closed source) or a GAN (research-only weights, trained on real prints), so there is no third-party licence to check. Sketched in `tools/fpgen/sketch_one.py` (Python prototype, CPU, ~5 s per print): zero-pole orientation with a core angle remap and a fingertip flow blended in above the core, space-variant Gabor growth from seeds stopped at coverage, extra minutiae inserted as phase-spiral dipoles (76-90 per print, 60-66 for arches), then a rolled impression (pressure, dry breaks, pores, creases) and a latent (partial irregular deposit, smear, textured surface). Accepted for the demo on 2026-09-21; see `tools/fpgen/samples/fp_classes.png`.
- [x] Matcher check with **SourceAFIS 3.18.1** (Java, Apache-2.0) as the reference: `tools/fpgen/gen_impressions.py` writes 100 fingers × 4 rolled + 2 latents, `tools/fpscore/Score.java` scores 31,100 pairs (600 templates in 8 s, pairs in 85 s on 8 cores), `tools/fpgen/plot_scores.py` → `samples/fp_scores.png`. NBIS was tried earlier in a separate pip library and rejected for performance.

| pairs | n | median | p99.9 | ≥ 40 |
|---|---|---|---|---|
| rolled genuine | 600 | 673 | — (min 420) | 100% |
| rolled impostor | 9,900 | 3.2 | 38.6 | 0.08% |
| latent genuine | 800 | 13.9 | — | 39.5% |
| latent impostor | 19,800 | 0.7 | 20.8 | 0% |

  - Identity survives: every rolled genuine pair is ≥ 10× the threshold, and latent rank-1 against 100 rolled is 50%.
  - **Same-class fingers were too alike** in the first run: rolled impostors ≥ 40 were 1.02% within a class against 0.03% across classes (0.31% overall, 30× SourceAFIS's stated FMR of 0.01% at 40). Core pinned near the centre, delta at a near-fixed offset and one ridge spacing for all fingers gave every loop the same flow, which forced minutiae into the same places. Wider core/delta placement and a per-finger ridge spacing brought it to 0.25% within class and 0.08% overall — still above 0.01%, so synthetic fingers remain somewhat more alike than real ones.
  - **Rolled genuine scores are too easy** (median 673): impressions of one finger differ by an 8 px elastic warp, ±15° and a shift, far less than real re-inkings. Harmless for app testing; it matters now that replacing the parametric ones is the actual plan (trackid's SYSTEM decisions above, and the sim's `-scores=vision` below once it extends to fingerprints) rather than a hypothetical -- see the hardening item below.
  - Latent genuine scores are bimodal: the half that fails is small or smeared deposits where the extractor finds almost nothing.
- [x] Ported to Go as `fpgen` (2026-10-11): `NewMaster`, `Rolled`, `Latent`, each a pure function of its `Stream(seed, key)`, pure Go with no new dependencies, ~1.8 s per master on 8 cores (the Python took ~5 s). Same constants and stages as `sketch_one.py`, not numpy's random streams or scipy's B-spline resampling, so prints are not pixel-equal; accepted on what the prototype was accepted on, `trackid-sim fingerprints -fingers 100` scored by `tools/fpscore` (same layout as `gen_impressions.py`): rolled genuine median 683 (Python 673), all ≥ 40; rolled impostor ≥ 40 0.04% (0.08%); latent genuine ≥ 40 39.5% (39.5%), median 17.2 (13.9); latent impostor 0% (0%). **One finger per human**, not ten masters (see C3).
- **Decided 2026-09-21: SourceAFIS for Java now, a Go port later.** Java stays outside every Go build, as `tools/facescore` keeps cgo out of the sim.
  - *Simulator*: `tools/fpscore` reads images and writes scores to a file the sim reads.
  - *trackid app*: a **Java sidecar service** (`extract(image) → template`, `match(probe, candidates) → scores`) as another container in `docker-compose`, behind a small Go interface in trackid (extract a template, score two templates). The asynq worker extracts on codification save and stores template bytes in Postgres; `biometricmatch` gets a fingerprint path that calls the interface. The same service can replace `tools/fpscore`, so there is one Java codebase.
  - Costs accepted: a JVM container in every deployment; for 1:N at scale the service holds the gallery in memory (loaded from Postgres), and keeping it in sync is the part to design. A matcher is computation, not a data source, so trackid's "logic reads only from Postgres" rule holds.
  - The Go port is therefore **not a prerequisite**: it replaces the sidecar behind the same interface when ready (one container fewer, no JVM), and the sidecar stays its reference.
- [x] **The sidecar and the Go interface, built in `../trackid` (`71f2608`).** `sourceafis-sidecar/` is a plain-JDK HTTP server (no Spring Boot) wrapping SourceAFIS 3.18.1: `POST /extract` → template bytes, `POST /match` → scores, a new `docker-compose` service on host port 58090, dependencies fetched from Maven Central the same way `tools/fpscore`'s own script does. `fingerprint/` is the Go client (`Client.Extract`/`Client.Match`) plus the Asynq wiring (extract on codification/identity save, sync match+cluster after); `db/migrations/025` (now `008_create_biometric_templates.sql`) adds `biometric_templates` (opaque `BYTEA`, keyed on `biometricfeature_id` -- no pgvector column, since a template isn't fixed-length, unlike `feature_embeddings`); `biometricmatch.RunTemplates` is the parallel path to `Run` for templates, threshold `fingerprint.MatchThreshold = 40` (SourceAFIS's own cutoff, not the face cosine shape); `identity.Ingest`/`cases.CreateTraces`/`SaveCodificationImage` route to fingerprint extraction or face embedding by feature/codification type; `cmd/backfill-identity-fingerprint-templates` catches up enrollments ingested before this wiring existed. **Done when** met directly: `biometricmatch/templates_db_test.go` runs `identity.Ingest` (rolled, KNOWN) + `cases.Ingest` (latent, QUESTIONED) → real sidecar extraction → `RunTemplates` → one SYSTEM POSITIVE `biometric_decisions` row, end to end. Verified first against `tools/fpgen`'s fixtures and `tools/fpscore`'s `scores.csv` with a manual curl trial (545.07 rolled/rolled, 71.57 latent/rolled, matching `scores.csv` exactly) before any of the DB/Asynq wiring.

  The earlier note here that this pattern was "proven... behind Django in another project" turned out to be only half right: that project (a sibling repo) embeds SourceAFIS in-process via JPype, a Python-Java bridge with no Go equivalent, so it validated the library's Java API shape (`FingerprintImage` → `FingerprintTemplate` → `FingerprintMatcher.match`), not a sidecar's HTTP boundary -- the HTTP boundary here is new and is what the tests above actually validate.
- [ ] Harden the rolled impressions (distortion model, partial overlap, harsher pressure) until some genuine pairs fall near SourceAFIS's threshold 40 (now `fingerprint.MatchThreshold` in `../trackid`, not just a reference number). Needed for the sim's fingerprint scores and for the Go port's acceptance test, which currently has no genuine pairs near the threshold.
- [ ] **Long term: a SourceAFIS package in trackid-vision** (its home confirmed 2026-10-11; not a prerequisite for anything): port SourceAFIS **3.18.1** to Go ourselves, as a pure-Go package beside `vision/` (no cgo, so importing it does not pull in onnxruntime), implementing the two methods trackid already calls (`fingerprint.Client.Extract`/`Match` in `../trackid`, used through `biometricmatch.TemplateMatcher`). It replaces the sidecar behind that interface (one container fewer, no JVM); the sidecar stays as its reference until then. Core first (~4k Java lines: primitives, extractor, matcher, features); ISO 19794-2 / ANSI 378 codecs later; transparency optional. Read and write SourceAFIS's own serialized template format, so templates already in `biometric_templates` stay valid across the switch. Apache-2.0: keep its LICENSE/NOTICE with the package. Accepted when its scores track the Java scores pair by pair on `tools/fpgen`'s fixtures (`tools/fpscore`'s `scores.csv`) and match decisions at 40 agree; bit-identical is not expected (Java vs Go math and operation order). [jtejido/go-sourceafis](https://github.com/jtejido/go-sourceafis) (unmaintained since 2023-11, untested, global config, source version unstated) is a cross-check, not a base.
- [x] Fingerprints join trackid's pipeline the way faces did (C4), not through a sim-side scorer (the old plan, a sim `-scores=vision` flag, went with C4's replan). `load.FaceTasks` is gone, so trackid's worker extracts templates on ingest and `biometricmatch.RunTemplates` writes the SYSTEM decisions; `load.Wait` also waits for every template to be matched and nudges `fingerprint.NewSyncTask`. Needs the `sourceafis-sidecar` container and `FINGERPRINT_SIDECAR_URL` for the worker; `start_dev.sh` starts the sidecar when that variable is set (since 2026-10-19, block A). **First live run 2026-10-11** (seed-1 world, 130 fingers drawn): 193/193 prints extracted, 0 failed; **56 SYSTEM POSITIVE** (40.5-854.9), 46 joining the same person and **10 joining two people**, all rolled vs rolled at 40.5-54.3, none involving a lift. That is the measured rolled impostor rate at scale (~13k different-person rolled pairs x 0.04-0.08% = 5-10), and one pair of people accounts for three of them, the same-class likeness noted above. `eval` FINGERPRINT: precision 0.71, recall 0.51 (TP 49, FP 20, FN 47), 8 impure clusters; identify 6/20 correct, 0 wrong, 14 unresolved (latent genuine ≥ 40 is ~40%); 0 of 12 unknown subjects falsely linked. Faces unchanged (precision 0.81, recall 0.90). Reloaded 2026-10-12 with the 40/60 band (database reset, same world): **43 SYSTEM POSITIVE, all joining the same person (39 rolled pairs, 4 lifts), and 18 INCONCLUSIVE for students, 15 joining two different people and 3 genuine lifts.** `eval` FINGERPRINT: **precision 1.00** (TP 47, FP 0, 0 impure clusters), recall 0.49 (FN 49), 14 of 43 multi-feature people fragmented; identify 4/20 correct, 0 wrong, 16 unresolved; 0 of 12 unknown subjects falsely linked. Faces unchanged (precision 0.81, recall 0.90), with their own band 0.60/0.70. The false links vary run to run (10 on the first load, 15 here, 6 person pairs in common, shared pairs with identical scores, all 40.4-54.3 or lower): likely SourceAFIS's asymmetry (probe vs candidate) with the probe depending on extraction order -- not verified. The band makes that harmless: no false link reached 60. The reload also showed `start_dev.sh` leaving the sidecar down (fixed 2026-10-19, block A): the worker then started without fingerprint handlers (checked once, at startup) and every extraction task fails into asynq's retry backoff (188 of them) until the sidecar is up and the worker restarted.
- Known and accepted: arches have a modest hump, the rolled outline is a fingertip rather than nail-to-nail, latents have no overlaps or curved surfaces, and extra minutiae arrive in pairs.

## D. Phase 5: the review exercise (replanned 2026-10-11)

A simulated exercise: the world, its images and every SYSTEM decision come from the sim and trackid's own pipeline (C4); the human decisions (VERIFICATOR, REVIEWER) are made by **students** in trackid's UI. The sim simulates the case load, never the examiner. The old items assumed simulated examiners -- a `susceptibility` and `review-rate` to sweep, a `-revoke-rate` writing late disagreeing decisions, a `sweep` over seeds for the P2/P3 tables -- and are dropped with them.

- [x] Starting state: all SYSTEM decisions are kept. A load leaves trackid's matcher output as it is -- SYSTEM POSITIVE confirmed, SYSTEM INCONCLUSIVE in the review queue, per the threshold version (C4) -- and the exercise never edits or removes them. Student decisions are added on top. Verified on the 2026-09-22 load (2026-10-11): 134 POSITIVE, 133 INCONCLUSIVE, 0 non-SYSTEM decisions.
- [x] Decision logic: settled in `../trackid` rather than in the sim. Students work **analyses**: every facial case gets a FACIAL_REVIEW (one-to-many, the matcher's candidates ranked with their score shown) and an admin opens a FACIAL_COMPARISON on chosen pairs (`c016617`). *Since 2026-10-28* trackid opens nothing automatically: a request names each case's task (`../trackid/docs/request-routing-plan.md`), so `load` asks a FACIAL_REVIEW of every facial case and a FINGERPRINT_COMPARISON (confronto papiloscópico) of every fingerprint case, empty: the coordinator adds its pairs from the matcher's candidates on the case page. Every facial case still has its review. Each analysis goes EXAMINATION (VERIFICATOR) → VERIFICATION (REVIEWER, blind to the first) → INCONSISTENCE (a third examiner, only on disagreements) → REPORT (`6dfe930`). The verifier sees only the two images, with no case, names, score or matcher decision (`4125ac4`), so blind vs exposed is fixed by the stage, not a setting; this hiding was in the screens only at first; since `f8194ed` (2026-10-19) the API sends a blind verifier and the third examiner nothing more than the screens show (see the dry run below). Face only: fingerprint analyses are not modelled yet.
- [x] Distribution: `../trackid`'s `distribution` hands out each step when its stage comes, to the least-loaded active examiner of the modality, never to someone holding another step of the same analysis; admins override (`c016617`, `6dfe930`). One student per step, so agreement is measured per analysis between its VERIFICATOR and REVIEWER. The accounts come from `trackid-sim users` (`879a01c`).
- [x] Score the students (2026-10-19): `eval.Decisions` reads every human decision with its analysis and the pair's SYSTEM decision; `eval.ScoreDecisions` tallies them against the world's truth (genuine/impostor × POSITIVE/INCONCLUSIVE/NEGATIVE, false identification and false exclusion rates) by examiner, by role and by SYSTEM band, plus VERIFICATOR/REVIEWER agreement per analysis. Decisions on features the world does not know (added in the UI) are counted as unscored. `eval` prints it after the cluster metrics. Checked on the current database: 3 test decisions (sim.ana, sim.bia, sim.caio), all genuine, all POSITIVE.
- [x] Revisions (2026-10-19): `trackid-sim snapshot -out out/snapshot.json` saves both layers before the exercise, and `eval -before out/snapshot.json` compares each layer with `eval.CompareSnapshots`. trackid records merges but not splits (the part holding a cluster's anchor keeps its id, the rest get new ones), so `CompareSnapshots` now counts a feature as split off when its old cluster still exists and its new id did not, and leaves only the rest as unexplained.

- [x] Dry run through the API (2026-10-19, scripted examiners on the 2026-09-22 load): found that a submit during a running sync never reached the REVIEWED layer (fixed in `../trackid`, `cluster.Settle`, `4b63cdd`), and that the API still sent a blind verifier the matcher's score and the first examiner's name (fixed: `f8194ed`; the third examiner is now blind too, by the instructor's choice, so no conclusion or authority leads them).
- [x] Coded accounts (2026-10-19): `users -examiners N` creates `instrutor` (admin, intelligence, coordinator; the profiles of `../trackid/docs/access-profiles-plan.md`) and facial examiners E01... with their own passwords; one code, drawn at random, is the planting account, kept deactivated between runs. Codes, passwords and the planter go only to `out/class-key.json`.
- [x] Planted errors (2026-10-19): `plant -analyses N -wrong F` examines N pending reviews as the planting account through `../trackid`'s examination package and decides a share F of their pairs wrongly on purpose (a NEGATIVE on one person, a POSITIVE on two); the rest right. The record goes only to `out/plant.json`; trackid holds nothing that tells a planted decision apart. `plant -reports` issues their reports once they reach REPORT. Review candidates are mostly same-person pairs, so `plant` picks reviews holding a different-people pair first and makes `-false-ids` (default half) of its wrong decisions false identifications on them, topping up with false exclusions when there are too few (and saying so). Checked: 6 wrong decisions, 3 of each.
- [x] Debrief (2026-10-19): `debrief` writes `out/debrief.html`, one self-contained page for the projector, by code only: each examiner against the truth, every planted error (caught by the blind verifier? how the third examiner settled it?), verifier agreement, and the cluster changes since the snapshot (`eval.PlantedOutcomes`). Each planted error shows its two faces, as small inline JPEGs read through `../trackid`'s `examination.PairImage` (`-images=false` leaves them out); the page stays one file (77 KB for 6 errors). Checked on a scripted class: 4 planted errors, the one missed on purpose shown as missed, 3 caught and settled right, 5 REVIEWED merges, 0 unexplained.

**Laudos in the exercise (decided 2026-10-27: in scope for the class).** Reviews give leads; a lead is not an identification. Students also take a lead to a laudo: a FACIAL_COMPARISON opened under the case's request, the questioned face against a reference, decided on trackid's conclusion scale, written in the WordCraft editor and issued.

- [ ] Decide the world rules the laudo needs first (the user's, not calibrated): whether requests send reference photos, how often and of whom, or whether references always come from enrollment photos.
- [ ] Decide the institute's settings for the class: the conclusion scale's wording, the minimum identification level (trackid's default is STRONG_SUPPORT_COMMON, on the OSAC scale since 2026-10-30) and the laudo's structure model.
- [ ] Comparisons from leads: who opens them (the instructor, or `plant`-style scripting for a share of leads), and on which pairs, so some laudos are asked on different-people pairs.
- [ ] Planted errors on laudos: `plant` decides some comparisons wrongly on the scale (an identification level on two people, an exclusion level on one), so verification of laudos is tested the way reviews are.
- [ ] Score conclusions: `eval.ScoreDecisions` by level on the scale against the truth, false identification counted at or above the minimum identification level, and links by basis (`eval` already splits lead vs laudo).
- [ ] `debrief` shows the laudos: each planted laudo error and how verification settled it, and links that rose from lead to laudo.
- [ ] Class deployment serves WordCraft (`WORDCRAFT_WEB_DIR`, `WORDCRAFT_CLI` in `../trackid/.env`); a dry run of a laudo end to end through the UI by scripted examiners, as was done for reviews.

**Done when:** a class reviews one loaded world in the UI, takes leads to laudos, and `eval` reports each student's decisions and conclusions against the truth and the cluster changes they caused. Before a class: load a fresh world (the dev database was rebuilt on 2026-10-20 and holds only the loaded world and the superuser, no class accounts), then `users`, `snapshot`, `plant`, distribute; after it, `plant -reports` and `debrief`.

## F. Stress test (2026-10-29)

Planned in `../trackid/docs/stress-test-plan.md` (GPU only, bursts rather than yearly totals, the demo world's mix scaled, all tooling here).

- [x] Stress stack beside the development one: `scripts/stress/{env,up,down}.sh`, compose project `trackid-stress` on trackid's own `docker-compose.yml` (its ports are variables since this) plus `compose.stress.yml` (pg_stat_statements). `up.sh` refuses a worker that is not on the GPU.
- [x] `load` split into drawing and sending (`prepareEnrollment`/`prepareCase`, then `ingest`), and `load.Burst`: parallel senders, operations released evenly over a window, each one timed and its error kept. `world.ReuseFaces` and `-face-reuse` for worlds beyond the pool.
- [x] `trackid-sim stress` (one burst, `record.json`), `stress-report`, `scripts/stress/sweep.sh` (levels until the time cap).
- [x] Concurrent users: `scripts/stress/k6/class.js` (examiners, coordinator, analysts, image lab; ramp, hold, spike) run by `scripts/stress/class.sh`, with `checks.sql`.
- [ ] Reports in the k6 mix (refresh and issue through WordCraft).
- [ ] Phase 3: users during a burst. A second world loaded beside the class's would reuse its case ids (`SIM-<year>-<n>`); it needs a case id prefix per world, or a burst of new cases on the class's own world.
- [ ] Ten fingers per human, for a ten-print burst.

## E. Make the repo buildable and citable (last, before it goes public)

- [ ] Tag a `trackid` release, then drop the `replace github.com/rodfileto/trackid => ../trackid` line in `go.mod` and `go get` the tag.
- [x] `LICENSE` (Apache-2.0, as `../trackid`) and `CITATION.cff` (2026-10-20).
- [ ] A CI workflow running `go vet ./... && go test ./...`; it needs the `trackid` tag above, since CI has no sibling checkout. `../trackid` has its own since 2026-10-20.
- [x] Check the redistribution terms of the IBGE name data before shipping any lexicon derived from it. IBGE data may be reused citing IBGE as the source, and a transformation must name who made it in a note: `fetch-lexicon` writes both into each file's header.
- [ ] Unit tests for `load` and for `media.Evidence`'s determinism; `fpgen` and the lift card have them since 2026-10-11. `load` has a DB test (`load/load_db_test.go`, occurrences); `media.Evidence` still has none.
- [ ] Add an integration test (build tag `integration`) that runs `generate` + `load` against `docker compose` and asserts the stability invariant (0 unexplained id changes) and F1 above a floor.
- [ ] Before any image goes into a release: re-read the licence of every generator (FLUX.1-schnell, Qwen-Image-Edit, and any later one), confirm it allows redistributing the outputs, and ship the model versions, seeds and manifests with the corpus. If in doubt, release the generator and manifests without the images.
- [ ] Connect the repo to Zenodo and cut `v0.1.0`.

**Done when:** a fresh clone builds with no sibling checkout and CI is green.
