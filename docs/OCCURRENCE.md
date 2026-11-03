# Occurrence layer (POLE+O: Event and Location)

Design for adding the **Occurrence** (boletim de ocorrência) and its **Location** to trackid's
Postgres model, and generating them in trackid-sim. Postgres only: no Neo4j in this step.

Decided (2026-10-12):

1. One occurrence → many biometric cases. The key lives on the case
   (`biometric_cases.occurrence_id`, nullable): a case not yet linked, or a CIVIL case with
   no BO, has none.
2. PostGIS for the spatial side.
3. Location is its own table, so a place that is hit again and again is one row.

4. Occurrence types follow **ISP-RJ's classification** (decided 2026-10-12), and ISP's
   counts per police-station area (CISP) drive where and how often the sim's occurrences
   happen. Codes are English, like every enum in the model; only the frontend translates.
   In trackid: `db/migrations/003_occurrences.sql`.
5. **Bank fraud is `FRAUD` (estelionato).** Much of what happens at banks today is fraud,
   not robbery: e.g. a stranger "helping" at the ATM who swaps the card or watches the PIN.
   That is a `FRAUD` occurrence located at a bank; its CCTV
   (branch vestibule, ATM camera) makes it a FACIAL case. ISP does not split estelionato by
   setting, so the share of FRAUD that happens in person at a bank is a documented
   assumption.
6. Geocoding: Nominatim (OpenStreetMap), configured in trackid (`geocode` package,
   `GEOCODER_*`), so the future address/coordinate picker uses the same path.

An occurrence is not necessarily a crime. As `biometric_cases.case_type` covers CIVIL
identification, the occurrence covers the BO that starts it: a missing person, a body found.
So the crime-only fields (`attempted`, offender count) are NULL/empty on a
civil occurrence, and its location is where the person was last seen or the body was found.

### Occurrence types: ISP column → code

| ISP column | Type |
|---|---|
| `hom_doloso` | `HOMICIDE` |
| `tentat_hom` | `HOMICIDE`, `attempted` |
| `feminicidio` | `FEMICIDE` |
| `tentativa_feminicidio` | `FEMICIDE`, `attempted` |
| `latrocinio` | `ROBBERY_HOMICIDE` |
| `lesao_corp_morte` | `BODILY_INJURY_DEATH` |
| `hom_por_interv_policial` | `POLICE_INTERVENTION_DEATH` |
| `hom_culposo` | `NEGLIGENT_HOMICIDE` |
| `lesao_corp_dolosa` | `ASSAULT` |
| `lesao_corp_culposa` | `NEGLIGENT_BODILY_INJURY` |
| `estupro` | `RAPE` |
| `sequestro` | `KIDNAPPING` |
| `sequestro_relampago` | `EXPRESS_KIDNAPPING` |
| `extorsao` | `EXTORTION` |
| `ameaca` | `THREAT` |
| `roubo_transeunte` | `STREET_ROBBERY` |
| `roubo_celular` | `PHONE_ROBBERY` |
| `roubo_em_coletivo` | `PUBLIC_TRANSPORT_ROBBERY` |
| `roubo_veiculo` | `VEHICLE_ROBBERY` |
| `roubo_carga` | `CARGO_ROBBERY` |
| `roubo_comercio` | `COMMERCIAL_ROBBERY` |
| `roubo_residencia` | `HOME_ROBBERY` |
| `roubo_banco` | `BANK_ROBBERY` |
| `roubo_cx_eletronico` | `ATM_ROBBERY` |
| `roubo_conducao_saque` | `FORCED_WITHDRAWAL_ROBBERY` |
| `roubo_apos_saque` | `POST_WITHDRAWAL_ROBBERY` |
| `roubo_bicicleta` | `BICYCLE_ROBBERY` |
| `outros_roubos` | `OTHER_ROBBERY` |
| `furto_veiculos` | `VEHICLE_THEFT` |
| `furto_transeunte` | `STREET_THEFT` |
| `furto_coletivo` | `PUBLIC_TRANSPORT_THEFT` |
| `furto_celular` | `PHONE_THEFT` |
| `furto_bicicleta` | `BICYCLE_THEFT` |
| `outros_furtos` | `OTHER_THEFT` |
| `estelionato` | `FRAUD` |
| `posse_drogas` | `DRUG_POSSESSION` |
| `trafico_drogas` | `DRUG_TRAFFICKING` |
| `pessoas_desaparecidas` | `MISSING_PERSON` |
| `encontro_cadaver` | `BODY_FOUND` |
| `encontro_ossada` | `SKELETAL_REMAINS_FOUND` |

Left out of ISP's columns: totals (`cvli`, `letalidade_violenta`, `roubo_rua`, `total_roubos`,
`total_furtos`, `registro_ocorrencias`), police productivity (`apreensao_drogas`,
`apreensao_drogas_sem_autor`, `recuperacao_veiculos`, `apf`, `aaapai`, `cmp`, `cmba`) and police
killed on duty (`pol_militares_mortos_serv`, `pol_civis_mortos_serv`, already within the
lethal types).

Not in ISP, kept because they open biometric cases (their frequency is an assumption):
`IDENTITY_FRAUD` (false identity / forged document: the sim's duplicate and impostor
enrollments), `UNIDENTIFIED_LIVING_PERSON`, `DISASTER_VICTIM`.

Lost against the earlier list: residential burglary and shop theft are inside ISP's
`outros_furtos` (`OTHER_THEFT`), and ISP's `encontro_cadaver` is any body found, identified or
not (`BODY_FOUND`).

### Data sources (all checked against the files, 2026-10-12)

| Source | What | Licence |
|---|---|---|
| ISP-RJ `BaseDPEvolucaoMensalCisp.csv` (ispdados.rj.gov.br/Arquivos/) | monthly counts per CISP and column, 2003–2026-08; columns `cisp`, `mes`, `ano`, `mcirc` (IBGE code), one per category | none stated (ISP's Open Data Plan names none): cite ISP as source; to confirm |
| ISP-RJ `CISPshp.rar` (same place) | CISP boundaries, 137 polygons statewide, WGS 84, `cisp` + `aisp`; credits ISP-RJ, SEPOL, SEPM | as above |
| BCB `Informes_Agencias` OData (olinda.bcb.gov.br) | bank branches with street, number (often empty), neighbourhood, CEP, IBGE code; 546 in the city of Rio, 13,675 in Brazil | ODbL |
| Nominatim (OpenStreetMap) | coordinates for the branches | ODbL; ≤ 1 request/s; credit "© OpenStreetMap contributors" |

Built into `world/data/rio/` (committed; raw downloads in `out/cache/`, not committed):
`trackid-sim fetch-isp` writes `isp_counts.json` (per ISP column: type, total and split
over the 41 CISPs, last 12 months, with source URL and sha256) and `cisp.geojson` (the 41
CISP polygons, 1,225 km²); `trackid-sim fetch-banks` writes `bank_branches.json` (each
bank branch with its point, precision, CISP and the OSM object matched). Branch addresses
are geocoded with ever coarser queries (street+CEP, street, CEP, neighbourhood) after
expanding the register's abbreviations ("R.SEN.DANTAS" → Rua Senador Dantas); a result is
kept only at neighbourhood precision or better and inside one of the CISPs.

Bank branches, built 2026-10-12 from the register as of 10/10/2026: 483 bank branches
(segments "Banco …" and Caixa; brokers and exchange houses left out), **481 placed**:
125 `GEOCODED` (house number or the building), 285 `STREET`, 71 `NEIGHBOURHOOD`; 2 not
placed (addresses with floor/shop suffixes). They fall in 39 of the 41 CISPs, most in
CISP 1 and 5 (Centro). Caveats for the sim:
- A `NEIGHBOURHOOD` point is the neighbourhood's centroid: 20 Centro branches share one.
  Not a place where 20 banks stand; spread them (e.g. inside the CISP) or leave them out
  when exact places matter.
- A `STREET` point is one point for the whole street: 12 branches share Avenida Rio
  Branco's.
- The public Nominatim answered `503` twice during the run; the tool backs off (1, 2, 4
  min) and its cache makes a rerun send nothing already answered.

City of Rio, Sep 2025 – Aug 2026 (41 CISPs): street robbery 15,642, phone robbery 15,461,
vehicle robbery 14,283, phone theft 31,700, other theft 54,137, fraud 77,711, homicide 1,010,
missing persons 2,835, bodies found 194 — and **bank robbery 0**, ATM robbery 8. Banks are
therefore real *places* (for FRAUD at a bank, withdrawal robberies, express kidnapping, ATM
robbery), not a source of bank robberies.

Geocoding trial (5 branches, public Nominatim, structured query with CEP): all matched the
right street and neighbourhood; 1 at house-number precision (`GEOCODED`), 4 at `STREET`
(OSM often has no house numbers; a long avenue's street point can be far from the branch).

### SINESP-VDE: evaluated and rejected (2026-10-12)

Checked against its data (`BancoVDE 2026.xlsx`, January–August 2026, from
gov.br/mj, `dnsp-base-de-dados/bancovde-2026.xlsx`), not only its documentation: 31
events. It monitors public-security activity rather than classifying occurrences:
- No furto/roubo outside vehicles, cargo and financial institutions (no residential
  burglary, shop or street robbery), where most latents and CCTV frames come from.
- No unidentified body or disaster victim (*Mortes a esclarecer* is a death of unclear
  cause, not an unknown person).
- Its largest categories are police activity (warrants served 24%, drug trafficking 18%),
  rarely a source of a biometric case; 5 are fire-brigade services.
- Mixed units (victims, occurrences, kg) and most categories per UF only.

## Why

Intelligence analysis by place and time: hotspots, distance between occurrences, series.
The analytic hook is the case link: when a biometric cluster joins traces from different
cases, their occurrences become a **series** to put on a map and a timeline.

## Schema (trackid)

Implemented in trackid as `db/migrations/003_occurrences.sql`, which is the source of truth;
the SQL below is the first design (its type list was since replaced by ISP-RJ's).

```sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;

-- A place where occurrences happen (POLE: Location). location_id is the importer's stable
-- business key, as case_id is for cases: deciding that two BOs share a place is the
-- importer's job, and trackid does not geocode or deduplicate addresses itself.
CREATE TABLE locations (
    id BIGSERIAL PRIMARY KEY,
    location_id TEXT NOT NULL UNIQUE,
    name TEXT,                                   -- e.g. a bank branch, a station
    geom GEOGRAPHY(Point, 4326) NOT NULL,
    -- How exact geom is. A MUNICIPALITY point is that municipality's centroid; hotspot
    -- analysis must be able to leave those out.
    precision TEXT NOT NULL CHECK (precision IN ('EXACT', 'GEOCODED', 'STREET', 'MUNICIPALITY')),
    municipality_code CHAR(7) NOT NULL,          -- IBGE
    uf CHAR(2) NOT NULL,
    neighbourhood TEXT,
    address TEXT,
    place_type TEXT CHECK (place_type IN ('PUBLIC_ROAD', 'RESIDENCE', 'COMMERCE', 'BANK',
        'TRANSPORT', 'RURAL', 'OTHER')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX locations_geom_idx ON locations USING GIST (geom);
CREATE INDEX locations_municipality_code_idx ON locations (municipality_code);

-- A registered occurrence (POLE: Event) -- the boletim de ocorrência.
CREATE TABLE occurrences (
    id BIGSERIAL PRIMARY KEY,
    agency TEXT NOT NULL,                        -- the unit that registered it
    occurrence_number TEXT NOT NULL,             -- the BO number, unique per agency
    location_id BIGINT NOT NULL REFERENCES locations(id),
    -- A window, not an instant: many crimes are found after the fact. occurred_to is NULL
    -- when the moment is known.
    occurred_from TIMESTAMPTZ NOT NULL,
    occurred_to TIMESTAMPTZ CHECK (occurred_to >= occurred_from),
    reported_at TIMESTAMPTZ,
    registered_at TIMESTAMPTZ NOT NULL,
    -- Crime-only: NULL/empty on a civil occurrence.
    attempted BOOLEAN,
    offender_count SMALLINT CHECK (offender_count >= 0),
    victim_count SMALLINT CHECK (victim_count >= 0),
    modus_operandi TEXT[] NOT NULL DEFAULT '{}', -- tags: FIREARM, MOTORCYCLE, FORCED_ENTRY, ...
    status TEXT NOT NULL CHECK (status IN ('REGISTERED', 'UNDER_INVESTIGATION', 'INQUIRY_OPENED', 'CLOSED')),
    description TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT occurrences_agency_number_key UNIQUE (agency, occurrence_number)
);
CREATE INDEX occurrences_location_id_idx ON occurrences (location_id);
CREATE INDEX occurrences_occurred_from_idx ON occurrences (occurred_from);
CREATE INDEX occurrences_modus_operandi_idx ON occurrences USING GIN (modus_operandi);
CREATE INDEX occurrences_description_idx ON occurrences USING GIN (to_tsvector('simple', description));

-- Occurrence types (docs/OCCURRENCE.md in trackid-sim). Codes only: display names are the
-- frontend's translations. case_type is the nature of the biometric case such an occurrence
-- opens, as in biometric_cases.case_type.
CREATE TABLE occurrence_types (
    code TEXT PRIMARY KEY,
    case_type TEXT NOT NULL CHECK (case_type IN ('CRIMINAL', 'CIVIL')),
    grp TEXT NOT NULL CHECK (grp IN ('PROPERTY', 'PERSON', 'FRAUD', 'CIVIL'))
);
INSERT INTO occurrence_types (code, case_type, grp) VALUES
    ('RESIDENTIAL_BURGLARY', 'CRIMINAL', 'PROPERTY'),
    ('SHOP_THEFT', 'CRIMINAL', 'PROPERTY'),
    ('VEHICLE_THEFT', 'CRIMINAL', 'PROPERTY'),
    ('STREET_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('COMMERCIAL_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('HOME_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('VEHICLE_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('CARGO_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('BANK_ROBBERY', 'CRIMINAL', 'PROPERTY'),
    ('ATM_ATTACK', 'CRIMINAL', 'PROPERTY'),
    ('EXPRESS_KIDNAPPING', 'CRIMINAL', 'PERSON'),
    ('HOMICIDE', 'CRIMINAL', 'PERSON'),
    ('FEMICIDE', 'CRIMINAL', 'PERSON'),
    ('ROBBERY_HOMICIDE', 'CRIMINAL', 'PERSON'),
    ('RAPE', 'CRIMINAL', 'PERSON'),
    ('IDENTITY_FRAUD', 'CRIMINAL', 'FRAUD'),
    ('MISSING_PERSON', 'CIVIL', 'CIVIL'),
    ('UNIDENTIFIED_BODY', 'CIVIL', 'CIVIL'),
    ('UNIDENTIFIED_LIVING_PERSON', 'CIVIL', 'CIVIL'),
    ('DISASTER_VICTIM', 'CIVIL', 'CIVIL');

-- A BO can carry several types (e.g. a robbery that ended in a homicide).
CREATE TABLE occurrence_classifications (
    occurrence_id BIGINT NOT NULL REFERENCES occurrences(id) ON DELETE CASCADE,
    type_code TEXT NOT NULL REFERENCES occurrence_types(code),
    PRIMARY KEY (occurrence_id, type_code)
);
CREATE INDEX occurrence_classifications_type_code_idx ON occurrence_classifications (type_code);

ALTER TABLE biometric_cases
    ADD COLUMN occurrence_id BIGINT REFERENCES occurrences(id) ON DELETE SET NULL;
CREATE INDEX biometric_cases_occurrence_id_idx ON biometric_cases (occurrence_id);

-- +goose Down
DROP INDEX biometric_cases_occurrence_id_idx;
ALTER TABLE biometric_cases DROP COLUMN occurrence_id;
DROP TABLE occurrence_classifications;
DROP TABLE occurrence_types;
DROP TABLE occurrences;
DROP TABLE locations;
-- The extension stays: other objects may depend on it.
```

### Infra and code notes

- **Image**: `pgvector/pgvector:pg16` has no PostGIS, and `postgis/postgis` has no pgvector.
  Build a small image, e.g. `db/postgres/Dockerfile`:
  `FROM pgvector/pgvector:pg16` + `apt-get install postgresql-16-postgis-3`. The existing
  volume keeps working: `CREATE EXTENSION` needs only the binaries.
- **sqlc** does not know `geography`. Keep it out of generated structs: queries take and
  return `lon`/`lat` as `float8`
  (`ST_SetSRID(ST_MakePoint(@lon, @lat), 4326)::geography`,
  `ST_X(geom::geometry)`, `ST_Y(geom::geometry)`). Spatial queries (radius, nearest)
  stay in SQL with `ST_DWithin` / `<->`.
- **Ingest**: an occurrence (with its location) is upserted before cases that point at it,
  either through its own `Ingest` or nested in `cases.Ingest`'s input. To decide when the
  first importer is written.

## Not in this step

- People in a BO (victim, suspect, witness, the person who reported it) and objects
  (vehicles, stolen items): POLE link tables, later. They bring identity resolution with
  them.

## Generation, re-evaluated (2026-10-12): implemented

Today's world (seed 1): 300 humans, 32 offenders, 42 events with a year only, 50 cases.
Too few points, and no place or time, for analysis by place and time. The fixed face pool
(200 base faces, 146 variants; frozen, no more generation) uses 138 faces: 62 spare. More
occurrences per offender cost no faces (an offender's variant is reused), so the world can
grow in occurrences, not in humans.

As built (`world/occurrences.go`, `world/civil.go`, `world/geo.go`; rules pinned by
`world/occurrences_test.go`), with these changes from the proposal:

- **One kind of crime per offender** (decided): each offender commits a single
  occurrence type; partners and one-off accomplices are offenders of the same type.
- Offenders are **apportioned** to types (largest remainder), not drawn: with 32
  offenders, bank FRAUD (1.15 expected) gets one in every seed instead of zero in some.
  Types far below one offender (ATM robbery 0.01, express kidnapping 0.08) get none —
  as rare as in Rio's data.
- **Lethal types are capped at 3 occurrences per offender** (the heavy tail had given one
  offender 25 homicides). Other types: 3 to 40, heavy-tailed.
- Default world (seed 1): 228 occurrences (213 criminal, 15 civil: 8 bodies found, 4
  unidentified living persons, 3 missing-person reports), 190 cases, 152 faces used of 200.
- **Repeat places** (added after the first load, which had none: 228 occurrences at 228
  places). *Offender habit*: an offender returns to one of their first 2 places with
  probability 0.20. *Repeat victimisation*: a fresh place is, with probability 0.20, one
  of its area's 3 hotspots for that kind of place, shared by every offender; bank branches
  are a shared pool already. Seed 1: 34% of criminal occurrences at a place used more
  than once (28 places), 6 of those places hit by different offenders. Rates are
  assumptions.
- **Limitation**: a point is uniform over its CISP's surface, so it can land in a park,
  forest (Tijuca, Pedra Branca) or on an island inside the CISP (Paquetá). Placing points
  on OSM streets would fix it.
- `load` ingests occurrences (trackid's `occurrences.Ingest`) before cases, which link
  through `cases.CaseInput.Occurrence`; ground truth (offenders, the unknown person) is
  not loaded. Checked by `load/load_db_test.go` against a throwaway database.

### Rules (decided)

- **Criminal occurrences** come only from scene-evidence types (below), in proportion to
  ISP's counts. Offenders, enrollments, duplicates, renewals and double identities stay as
  today.
- **Unknown-person occurrences are added, and kept simple**: an unknown person is a
  non-offender with **one identity** (at most one primary enrollment: no renewal,
  duplicate or double identity), who **never appears in a criminal case** (not a
  bystander, not anyone's partner). Their cases are CIVIL and can only be matched to their
  own enrollment.

### Inquiries (2026-10-30)

The BO and the inquiry (inquérito policial, IPL) are separate documents, and the world keeps
them apart (`world.Occurrence.Number`, `.Inquiry`, `.RequestInquiry`; trackid's
`occurrences.inquiry_number` and `case_requests.cited_inquiry_number`,
`db/migrations/003_occurrences.sql`):

- A criminal occurrence whose status is `INQUIRY_OPENED` or `CLOSED` has an inquiry of its own.
- **Registered by its inquiry**: with probability 0.10 a criminal occurrence has no BO; the
  inquiry, opened by portaria, is the document that registers it (status at least
  `INQUIRY_OPENED`). trackid keys it by the inquiry number.
- **Requests from another inquiry**: with probability 0.15 a criminal occurrence's requests come
  from an inquiry other than its own (a later one, or one gathering other facts); the request
  cites that inquiry, not the occurrence's.
- Inquiries are numbered per police station and year of registration,
  `SIM-IPL-<cisp>-<seq>/<year>`; BOs keep `SIM-<cisp>-<seq>/<year>`. Civil occurrences have no
  inquiry.
- The draws come from their own stream (seed, `0x1b1`), so nothing else in the world changes;
  BO sequences do, since an occurrence registered by its inquiry takes no BO number.
- Default world (seed 1): of 213 criminal occurrences, 180 have an inquiry, 25 of them with no
  BO, and 38 have requests from another inquiry. Rates are assumptions.
- `load` cites the BO and the requesting inquiry on each case's request, and names the case's
  occurrence directly, since another inquiry's number does not resolve to it.

### Criminal: which types, how often

The world holds occurrences that produced a biometric case, so a type's weight is
`ISP count × P(case | type)`, not the ISP count alone. Rates per type are
**assumptions** to review; ISP counts are Rio, Sep 2025 – Aug 2026.

| Type | ISP 12 mo | P(FACIAL case) | P(FINGERPRINT case) | Place |
|---|---:|---:|---:|---|
| STREET_ROBBERY | 15,642 | 0.30 | 0.02 | street point in the CISP |
| PHONE_ROBBERY | 15,461 | 0.30 | 0.02 | street point |
| STREET_THEFT | 11,521 | 0.15 | 0.02 | street point |
| PHONE_THEFT | 31,700 | 0.10 | 0.01 | street point |
| VEHICLE_ROBBERY | 14,283 | 0.20 | 0.30 | street point |
| VEHICLE_THEFT | 8,364 | 0.10 | 0.30 | street point |
| CARGO_ROBBERY | 1,598 | 0.20 | 0.30 | street point |
| COMMERCIAL_ROBBERY | 1,364 | 0.80 | 0.20 | street point (shop) |
| HOME_ROBBERY | 269 | 0.20 | 0.50 | street point (home) |
| OTHER_THEFT (incl. burglary, shop theft) | 54,137 | 0.05 | 0.05 | street point |
| ATM_ROBBERY | 8 | 0.90 | 0.30 | bank branch |
| POST_WITHDRAWAL_ROBBERY | 72 | 0.60 | 0.02 | bank branch |
| FORCED_WITHDRAWAL_ROBBERY | 12 | 0.90 | 0.05 | bank branch |
| EXPRESS_KIDNAPPING | 100 | 0.90 | 0.10 | bank branch (ATM) |
| FRAUD at a bank (share of 77,711) | 2% → 1,554 | 0.80 | 0.02 | bank branch |
| HOMICIDE | 1,010 | 0.20 | 0.40 | street point |
| ROBBERY_HOMICIDE | 48 | 0.40 | 0.40 | street point |
| FEMICIDE | 31 | 0.10 | 0.40 | street point (home) |

Attempts: ISP's `tentat_hom` adds HOMICIDE with `attempted = true` in its own proportion.
(Modus operandi tags, first generated here, were dropped from the model on 2026-10-12.)

### Where

- Each offender gets an **anchor CISP**, drawn in proportion to all their types' ISP
  counts, and a **specialty** (a type group, kept with probability 0.6 per occurrence) —
  so series are spatially and thematically coherent.
- An occurrence's CISP is drawn with weight `ISP count(type, CISP) × exp(−d / 5 km)`,
  d the distance between CISP centroids (journey-to-crime distance decay; 5 km an
  assumption).
- The point: uniform inside the CISP polygon (precision `EXACT`, the simulated truth), or,
  for bank types, a bank branch in that CISP (its geocoded precision; a `NEIGHBOURHOOD`
  branch is moved to a uniform point in its CISP so centroids do not become hotspots).
  Repeat places come for free: the same branch can be hit twice.

### When

Year uniform over YearFrom..YearTo (as today); month uniform; hour from a per-group
profile (an assumption: street robbery evening-heavy, commercial and bank daytime,
burglary weekday daytime). `occurred_to` is set for theft types (found later).

### Unknown persons (CIVIL)

- About 12 humans (assumption, within the 62 spare faces), drawn from non-offenders who
  are never used as bystanders; half enrolled (identifiable), half not.
- Each yields one `BODY_FOUND` (~70%) or `UNIDENTIFIED_LIVING_PERSON` (~30%)
  occurrence with a CIVIL case: FINGERPRINT (post-mortem or live ten-print as questioned
  lifts) and/or FACIAL. For about half, a `MISSING_PERSON` occurrence registered earlier,
  with no biometric case — the link students can find once the body is identified.
- Place: a uniform point in a CISP drawn in proportion to ISP's `encontro_cadaver` /
  `pessoas_desaparecidas`.

### Scale

Target about 300 criminal occurrences (from about 45 offenders: more events per offender,
same humans) plus about 12 unknown persons. Faces needed: today's 138 plus about 12.

### Pipeline changes

1. **trackid**: an occurrence ingest (location + occurrence + types, upserted by business
   key) and a way for `cases.Ingest` to link a case to its occurrence.
2. **world**: `Occurrence` (type, attempted, CISP, point, precision, time window, agency
   `DP-<cisp>`, number, status, offender/victim counts); `Case.EventID`
   becomes the public `OccurrenceID`. Ground truth (offenders, the exact point) stays out
   of Postgres.
3. **load**: occurrences first, then cases linked to them.
4. **Trial**: generate, plot the points over the CISP polygons, look, then load.

## trackid-sim

- The sim's internal `event` (`world/generate.go`) becomes a world `Occurrence` with a
  `Location`; `Case.EventID` (ground truth today) becomes the public `occurrence_id` link.
- Civil occurrences too: `MISSING_PERSON`, `UNIDENTIFIED_BODY`, … leading to CIVIL
  biometric cases, which the sim does not generate yet; and `IDENTITY_FRAUD` for its
  duplicate and impostor enrollments.
- **Where and how often, from ISP**: the type mix and each type's spread over CISPs come
  from ISP's counts for the city of Rio (last 12 months); a point is then placed inside the
  chosen CISP polygon, at a real place when the type has one (a geocoded bank branch for
  bank FRAUD, withdrawal robberies, express kidnapping, ATM robbery), else on a street.
  Derived data (per-CISP counts, Rio's CISP polygons, geocoded branches) is committed with
  its provenance; the raw downloads are not.
- **Spatial behaviour**, or the analysis finds nothing: each offender gets an anchor point
  (home municipality or neighbourhood), and occurrence places are drawn with
  journey-to-crime distance decay around it. Also: a share of repeat places (the same bank
  hit twice) and time-of-day/day-of-week profiles per occurrence type.
- Location ground truth (the point exact before any `precision` downgrade) stays out of
  Postgres, like `HumanID`.
- ~~Phase 2 validation: compare the generated spatial and temporal aggregates with published
  statistics.~~ Phase 2 removed 2026-10-20 (PLAN.md). One check was already made that day, on
  seeds 1-3 at 10,000 humans: the type mix and the split over CISPs follow ISP's counts (total
  variation distance 0.03-0.07; 50% of occurrences in 32-34% of CISPs, ISP 34%). The one
  deviation is homicide, at about half its ISP × P(case) share, from the cap of 3 lethal
  occurrences per offender.
