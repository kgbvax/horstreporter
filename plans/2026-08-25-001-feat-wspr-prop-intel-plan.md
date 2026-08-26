---
title: WSPR Propagation Intelligence - Plan
type: feat
date: 2026-08-25
topic: wspr-prop-intel
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-brainstorm
execution: code
---

# WSPR Propagation Intelligence - Plan

## Goal Capsule

- **Objective.** Build a WSPR-primary propagation-intelligence engine that tells the operator which bands and 11 DXPulse regions are possibly open for SSB and CW right now, with a first-class "from my QTH" view, and flags whether each (band × region) cell is rising, atypical, or both — flavoring atypical calls by cross-referencing the existing FT8 climatology to distinguish weak-but-real openings from strong events.
- **Product authority.** This plan owns the WSPR propagation-intelligence engine and its API/UI surface. The existing FT8 DX conditions baseline (`dx_conditions.go`) and its `dx_region_baseline_daily` Postgres table are read-only dependencies for the atypical cross-reference; they are not modified by this work.
- **Open blockers.** None blocking planning. Cold-start of the new WSPR climatology (~30 days to full depth) is an explicit assumption, not a blocker. FT8 climatology thinness in some (band × region × slot) cells degrades the atypical flavor gracefully.

---

## Product Contract

### Summary

A new WSPR-native propagation-intelligence engine supersedes the existing FT8-based `prop_intel.go` nowcast. It accumulates a WSPR climatology keyed by (band × 11 DXPulse region × slot-of-day), runs a nowcast over the 60-minute `hub.history` window to produce per-cell SSB-open and CW-open flags (from WSPR SNR + reported TX power), a `rising` slope flag, and an `atypical` z-score against the WSPR climatology. Each atypical call is flavored by reading the FT8 climatology for the same cell: `atypical-wspr-only`, `atypical-both`, or `atypical-wspr-silent-ft8`. The operator's own WSPR transmitter is a first-class "from-here" view distinct from the global WSPRnet mesh.

### Problem Frame

WSPR beacons run continuously at low power and decode paths far below what FT8 participants work, so they show whether a band is open *at all* even when nobody is operating. The repo already ingests WSPR (`wspr.go`) but keeps it out of the FT8-calibrated conditions baseline (`isNonConditionsMode`) and only feeds the live stream and a count-based activity chart. The existing `prop_intel.go` attempts a per-(band × region) nowcast with surge detection, but its surge z-score uses a memory-only recent-history baseline that almost never reaches statistical significance at the default 60-minute `hub.history` retention, and its persisted climatology (`dx_region_baseline_daily`) is FT8-only — WSPR is never observed into it. So the operator has no WSPR-native read of "which bands/regions are open for SSB/CW" and no trustworthy "is this surge atypical for this hour?" signal. The FT8 nowcast `prop_intel` currently produces is not the ask; the ask is a WSPR-native surface that leverages the continuous-beacon nature of WSPR and uses the FT8 climatology only as a cross-reference to flavor atypical calls.

### Key Decisions

- **WSPR climatology as the typical reference** (session-settled: user-directed — chosen over reusing the FT8 climatology as a proxy and over querying the wspr.live historical archive: a WSPR-native baseline has clean semantics for "atypical for WSPR" and does not depend on an external archive's availability). Governs R3, R6.
- **SNR + reported TX power budget model for SSB/CW viability** (session-settled: user-directed — chosen over path-existence-only, empirical calibration vs FT8, and a purely descriptive surface: WSPR physics supports a budget computation and the ingest already carries both fields). Governs R4.
- **11 DXPulse regions as the region axis** (session-settled: user-directed — chosen over the 6×6 grid-cluster and a hybrid: matches the existing FT8 region climatology, the existing `wspr-matrix.js` frontend, and the operator's coarse mental model; WSPR density per cluster×band×slot would be too thin). Governs R3, R6.
- **Two distinct flags: `rising` and `atypical`** (session-settled: user-directed — chosen over a level-only test and a combined rising-AND-high test: the operator wants to see "rising but typical" vs "atypical but stable" vs "atypical surge" as separate reads). Governs R5, R6.
- **First-class "from-here" view** (session-settled: user-directed — chosen over a filterable same-view and anonymous blending: the operator's own WSPR transmitter anchors a personalized directional propagation footprint, surfaced as a distinct view). Governs R7.
- **Approach C: WSPR-primary engine, FT8 climatology as read-only cross-reference** (session-settled: user-approved — the agent proposed with the tradeoff surfaced; the user assented). Governs R6. Supersedes the FT8 nowcast: the existing `prop_intel.go` FT8 path is retired by this feature.
- **Cold-start handling: surface atypical confidence from climatology depth, do not suppress the flag** — the `rising` flag works from day one (no climatology needed); the `atypical` flag is emitted with a confidence discount that tracks WSPR climatology depth until ~30 days of accumulation. Recorded as assumption A1.

### Requirements

#### WSPR climatology

- R1. The engine accumulates a WSPR climatology keyed by (band × 11 DXPulse region × slot-of-day), fed by the WSPR ingest path (`wspr.go`). Each WSPR spot contributes to exactly one region (the remote end's DXPulse region) per direction.
- R2. The climatology persists across restarts. The persistence path mirrors the existing FT8 baseline's dual mode: a Postgres table (parallel to `dx_region_baseline_daily`) when a store is configured, and a JSONL/in-memory fallback for the no-Postgres dev path.
- R3. The climatology stores per (band × region × slot) a count-based history sufficient to compute a mean and standard deviation of WSPR path rate, with at least 30 days of retention before entries are aged out. The 11 DXPulse regions (`dx_regions.go`) are the region axis; the 6×6 grid-cluster is not used as a climatology key.
- R4. The engine computes per (band × region) cell, over the live `hub.history` window, separate `ssb_open` and `cw_open` boolean flags from a path-budget model using WSPR SNR and reported TX power. A path with missing or zero TX power counts toward path-existence openness but not toward SSB/CW viability. Exact SSB/CW readability floors are decided in planning; the budget model is the subject of this requirement, not the floor values.

#### Nowcast and flags

- R5. Each cell carries a `rising` flag derived from the slope of WSPR path rate over a recent sub-window (on the order of 30–60 minutes). The flag is computed from live history alone and does not require climatology depth.
- R6. Each cell carries an `atypical` flag computed as a z-score of the current WSPR path rate against the WSPR climatology mean for the same (band × region × slot-of-day). When the z-score meets or exceeds a threshold, the flag is set. The flag carries a confidence value derived from climatology sample depth, so cold-start cells show a discounted confidence rather than no flag.
- R7. The engine serves a "from-here" view: WSPR cells where the operator's QTH (callsign or locator cluster) is one end of any path in the cell, surfaced as a distinct view alongside the global mesh view. The global mesh view is not filtered by the operator's QTH.

#### Atypical flavor (FT8 cross-reference)

- R8. When a cell is flagged `atypical`, the engine reads the existing FT8 climatology (`dx_region_baseline_daily`) for the same (band × region × slot-of-day) and labels the atypical call with exactly one flavor:
  - `atypical-wspr-only` — WSPR atypical, FT8 normal (within its typical band).
  - `atypical-both` — WSPR atypical and FT8 atypical (strong propagation event confirmed by both).
  - `atypical-wspr-silent-ft8` — WSPR atypical and FT8 effectively absent (weak-path opening only the beacon mesh catches).
- R9. When the FT8 climatology has insufficient depth for the same (band × region × slot) to support a confident normal/atypical judgment, the flavor degrades to `atypical-wspr-only` by default and the cell carries a note that the FT8 cross-reference was unavailable. This is the graceful fallback; it does not suppress the atypical flag.

#### API and frontend

- R10. The engine exposes its cells through an HTTP API. The existing `/api/prop_intel` endpoint is repurposed for this (the FT8 nowcast it currently serves is retired by this feature). The response carries per-cell: band, region, ssb_open, cw_open, rising, atypical (with z-score and confidence), atypical_flavor, and a from_here boolean. The endpoint accepts `qth`, `minutes`, and `surroundings` consistent with the existing API conventions.
- R11. The existing `wspr-matrix.js` band×region matrix is enriched to render the new per-cell flags (SSB/CW openness, rising, atypical with flavor) and to toggle between the from-here view and the global mesh view. The matrix remains a client-side aggregation of the API response; no new backend endpoint is introduced for the matrix itself.

#### Retiring the FT8 nowcast

- R12. The FT8 nowcast path in `prop_intel.go` (FT8 cells, FT8 P(open), FT8 surge z-score against the memory baseline) is removed. The FT8 `dx_region_baseline_daily` accumulator and schema are untouched and remain read-only inputs to R8. The `dx_conditions.go` DX Potential Score and per-band conditions are untouched.

### Key Flows

- F1. WSPR spot enters climatology
  - **Trigger:** A WSPR spot is ingested by `wspr.go` and persisted via `PersistRawSpot` (or its successor).
  - **Actors:** WSPR ingest, new WSPR climatology accumulator.
  - **Steps:** The spot's remote-end locator is resolved to a DXPulse region; the spot's band and the UTC slot-of-day are computed; the (band × region × slot) bucket count is incremented in memory and persisted to the configured store (Postgres or JSONL fallback).
  - **Outcome:** The climatology gains one observation for the cell. Covered by R1, R2, R3.

- F2. Nowcast evaluation
  - **Trigger:** A client requests `/api/prop_intel?qth=...&minutes=...`.
  - **Actors:** HTTP handler, new WSPR engine, `hub.history`.
  - **Steps:** Snapshot `hub.history` for the requested window; walk WSPR spots, aggregating per (band × region) cell (path count, SNR, TX power, sender set); compute `ssb_open`/`cw_open` from the budget model; compute `rising` from the sub-window slope; load the WSPR climatology for the current slot and compute the `atypical` z-score with confidence; for atypical cells, read the FT8 climatology and assign the flavor; tag from_here cells where the operator's QTH is one end.
  - **Outcome:** A response of cells with all flags. Covered by R4, R5, R6, R7, R8, R9, R10.

- F3. Cold-start atypical call
  - **Trigger:** A cell's WSPR climatology has fewer than the mature sample depth (roughly <30 days of observations for that slot).
  - **Actors:** New WSPR engine.
  - **Steps:** The z-score is computed against whatever depth exists; the confidence is discounted proportionally; the `atypical` flag is emitted with the discounted confidence rather than suppressed; the `rising` flag is unaffected.
  - **Outcome:** The operator sees a low-confidence atypical call during cold-start rather than a silent matrix. Covered by R6, A1.

### Acceptance Examples

- AE1. SSB/CW viability on a strong path
  - **Covers R4.**
  - **Given:** A WSPR spot on 20m from the operator's QTH to a JA station, SNR +5 dB, TX power 20 W.
  - **When:** The nowcast evaluates the 20m × JA cell.
  - **Then:** `ssb_open` is true and `cw_open` is true (the path budget exceeds both readability floors).

- AE2. CW-only viability on a weak path
  - **Covers R4.**
  - **Given:** A WSPR spot on 30m to a VK station, SNR -8 dB, TX power 5 W.
  - **When:** The nowcast evaluates the 30m × VK cell.
  - **Then:** `ssb_open` is false and `cw_open` is true (the budget exceeds the CW floor but not the SSB floor).

- AE3. Missing TX power falls back to existence-only
  - **Covers R4.**
  - **Given:** A WSPR spot on 40m to an EU station, SNR +2 dB, TX power 0 (missing/unreliable).
  - **When:** The nowcast evaluates the 40m × EU cell.
  - **Then:** The path counts toward the cell's existence-open state but neither `ssb_open` nor `cw_open` is set true by this spot alone.

- AE4. Atypical surge, FT8 confirms
  - **Covers R6, R8.**
  - **Given:** The 15m × SA cell's current WSPR path rate z-scores 2.5 above the WSPR climatology mean for this slot; the FT8 climatology for 15m × SA at this slot also shows an atypical rate.
  - **When:** The nowcast evaluates the cell.
  - **Then:** `atypical` is true, `atypical_flavor` is `atypical-both`, and the confidence reflects mature WSPR climatology depth.

- AE5. Atypical surge, FT8 silent
  - **Covers R6, R8.**
  - **Given:** The 10m × AF cell's WSPR rate z-scores 3.0 above typical; FT8 activity on 10m × AF at this slot is effectively absent (no FT8 climatology entries, or rate far below typical).
  - **When:** The nowcast evaluates the cell.
  - **Then:** `atypical` is true, `atypical_flavor` is `atypical-wspr-silent-ft8`.

- AE6. FT8 cross-reference unavailable
  - **Covers R9.**
  - **Given:** The 17m × OC cell is WSPR-atypical, but the FT8 climatology has no entries for 17m × OC at this slot.
  - **When:** The nowcast evaluates the cell.
  - **Then:** `atypical` is true, `atypical_flavor` is `atypical-wspr-only`, and the cell notes the FT8 cross-reference was unavailable.

- AE7. Rising but typical (daily onset)
  - **Covers R5, R6.**
  - **Given:** The 20m × EU cell's WSPR rate has a positive slope over the last 45 minutes (the band is opening for the day), but the current rate is within one standard deviation of the WSPR climatology mean for this slot.
  - **When:** The nowcast evaluates the cell.
  - **Then:** `rising` is true and `atypical` is false.

- AE8. Cold-start atypical call
  - **Covers R6.**
  - **Given:** The WSPR climatology has only 5 days of depth for the 12m × JA cell at this slot.
  - **When:** The nowcast evaluates the cell and the current rate z-scores 2.2 above the shallow mean.
  - **Then:** `atypical` is true with a discounted confidence reflecting the shallow climatology; `rising` is computed normally from live history.

- AE9. From-here view vs global mesh
  - **Covers R7.**
  - **Given:** The operator's QTH is in EU; a WSPR spot on 40m from the operator to a US station is in `hub.history`.
  - **When:** The nowcast is requested with the operator's QTH.
  - **Then:** The 40m × NA cell in the from-here view is populated (the operator is one end); the same cell in the global mesh view is also populated but not tagged from_here. A 20m × JA cell with no operator-end path appears in the global mesh view only, not in the from-here view.

### Success Criteria

- The operator can look at the band×region matrix and read, for each cell, four independent signals: SSB-open, CW-open, rising, atypical (with flavor). The signals do not collapse into a single "good/bad" gauge.
- The from-here view shows the operator's own WSPR footprint as a directional propagation read distinct from the global mesh.
- An atypical-surge flag on a normally-quiet (band × region × slot) cell is qualitatively confirmed on-air within the operator's next operating session — the real test, not automatable.
- Cold-start does not produce misleading high-confidence atypical calls; the confidence field tracks climatology depth honestly, and the operator can tell a mature atypical call from a cold-start one.

### Scope Boundaries

**In scope:**
- New WSPR climatology accumulator (band × region × slot) and its persistence (Postgres + JSONL fallback).
- WSPR nowcast over `hub.history`: per-cell SSB/CW flags, `rising`, `atypical` with flavor.
- From-here view as a distinct query-time filter.
- Repurposing `/api/prop_intel` to serve the new cells.
- Enriching `wspr-matrix.js` to render the new flags and the from-here/global toggle.
- Retiring the FT8 nowcast path in `prop_intel.go`.

**Out of scope / non-goals:**
- FT8 nowcast (retired by R12; not rebuilt).
- Per-cluster (6×6 grid) WSPR climatology. The region axis is the 11 DXPulse regions only.
- Empirical WSPR→SSB/CW calibration against FT8 ground truth (settled against in favor of the physics budget model).
- Modifying the FT8 `dx_region_baseline_daily` accumulator or its schema. It is read-only input to R8.
- Web Push integration for the new atypical-surge flags. The existing push path can be wired later.
- Modifying the DX Potential Score or per-band DX conditions in `dx_conditions.go`.

### Dependencies / Assumptions

- A1. Cold-start assumption. The WSPR climatology starts empty and needs ~30 days to reach mature depth. During cold-start, `atypical` confidence is discounted by climatology depth (R6, F3, AE8). The `rising` flag works from day one.
- A2. FT8 climatology coverage assumption. The existing FT8 `dx_region_baseline_daily` has enough depth in the (band × region × slot) cells that matter for the three-flavor cross-reference to be meaningful. Where FT8 coverage is thin, the flavor degrades to `atypical-wspr-only` (R9, AE6) — this is the documented fallback, not a silent failure.
- A3. WSPR TX power reliability assumption. `wsprSpot.Power` is reliable enough on average to drive the SSB/CW budget model, despite the `wspr.go:68` "often unreliable" note. Spots with missing or zero power fall back to existence-only openness (R4, AE3).
- D1. The WSPR ingest path (`wspr.go`) and its ClickHouse poller are stable and continue to feed `hub.history` and `PersistRawSpot`. This feature adds a climatology accumulator alongside the existing raw-spot persist; it does not replace the ingest.
- D2. The 11 DXPulse region classifier (`dx_regions.go`, `internal/region/region.go`) is stable and shared. The new engine uses it as the region axis without modification.

### Outstanding Questions

- O1. **Deferred to Planning.** The exact SSB and CW readability floors (SNR/2500 Hz thresholds for the budget model) and how TX power factors into the path budget. R4 carries the model; planning decides the constants.
- O2. **Deferred to Planning.** The `rising` sub-window length and slope threshold. R5 carries the flag; planning tunes the window (on the order of 30–60 minutes) and the trigger.
- O3. **Deferred to Planning.** The `atypical` z-score threshold and the exact confidence discount curve over climatology depth. R6 carries the flag and the confidence concept; planning sets the numbers.
- O4. **Deferred to Planning.** Whether the WSPR climatology Postgres table reuses the `dx_region_baseline_daily` shape with a `source` discriminator or gets its own table. R2 carries the persistence requirement; planning picks the schema.
- O5. **Deferred to Planning.** Whether `/api/prop_intel` keeps the path name or is renamed to reflect the WSPR-native scope (e.g. `/api/wspr_intel`). R10 carries the endpoint requirement; planning decides the name.

### Sources / Research

- Grounding dossier: `/tmp/compound-engineering-501/ce-brainstorm/wspr-dx-2026-08-25/grounding.md` (136 lines, verbatim quotes with `file:line` pointers covering `wspr.go`, `hub.go`, `dx_conditions.go`, `prop_intel.go`, `dx_regions.go`, `internal/region/region.go`, `dx_postgres.go`, `server.go`, `static/wspr-matrix.js`, `static/utils.js`).
- Key repo anchors: WSPR ingest at `wspr.go:15-274`; WSPR/RBN exclusion from the FT8 baseline at `dx_conditions.go:793-796, 1113-1130`; FT8 region climatology persistence at `dx_postgres.go:93-99, 411-419, 712-750, 1795-1866`; existing (FT8, memory-baseline) surge detector at `prop_intel.go:756-855`; 11-region classifier at `dx_regions.go:9-83` and `internal/region/region.go:14-99`; existing band×region WSPR matrix frontend at `static/wspr-matrix.js:1-123`.
- `CONCEPTS.md` — WSPR (line 60), Baseline (line 72), Grid cluster (line 76), Slot-of-day (line 79), Hot bands (line 101). The new feature adds "WSPR climatology", "from-here view", "atypical flavor" as candidate terms for later vocabulary capture.

Product Contract unchanged. No R-IDs renumbered, no scope changed. The Planning Contract, Implementation Units, Verification Contract, and Definition of Done below are new sections added by `ce-plan` enrichment.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Thread WSPR TX Power onto `MQTTMessage`** — the `Power` field lives only on the internal `wsprSpot` struct (`wspr.go:68`) and is discarded before `MQTTMessage` construction (`wspr.go:223-234`). R4's SSB/CW budget model needs `Power` available in `hub.history`. Add an optional `TXPower` field to `MQTTMessage` (`mqtt.go:12-36`), populated only by the WSPR ingest path. The field is zero for non-WSPR sources; the SSE stream and `matchAndCreateSpot` ignore it. Governs R4. (session-settled: user-approved — the agent proposed threading Power onto the message as the only viable path; the user assented to the SNR+Power budget model which requires it.)

- KTD2. **Parallel `wspr_region_baseline_daily` table, not a `source` discriminator on the existing FT8 table** — the WSPR climatology persists in its own Postgres table mirroring the `dx_region_baseline_daily` shape (band × region × slot × day_index × spot_count), with its own `observe` path on `handleWSPRSpot` and its own `wsprRegionCalendarStats` query mirroring `regionCalendarStats` (`dx_postgres.go:1812-1866`). This mirrors how `dx_baseline_cluster` is a separate table with its own flush arm (`dx_postgres.go:230-239`), keeps the FT8 accumulator untouched (R12), and avoids schema migration on the existing table. Governs R2, R3. Resolves O4.

- KTD3. **New in-memory + JSONL fallback for the WSPR climatology** — the FT8 region climatology has no in-memory fallback today (when Postgres is nil, `loadRegionBaselines` returns nil; `prop_intel.go:637-640`). R2 requires a fallback for the no-Postgres dev path. The WSPR climatology accumulator keeps an in-memory `map[string]*wsprClimatologyBucket` (keyed `band|slot|region`) with periodic JSONL save/load mirroring the `dx_baseline.json` pattern (`main.go:230, 346`), and a `wsprRegionCalendarStatsFromMemory` helper that computes mean/stddev from the in-memory buckets. The fallback is WSPR-only; the FT8 climatology is not given one (out of scope). Governs R2, R3.

- KTD4. **Atypical z-score uses the WSPR climatology's per-slot mean and stddev, not the memory baseline** — the existing `prop_intel.detectSurges` (`prop_intel.go:756-819`) computes a z-score against a memory-only recent-history baseline that almost never reaches statistical significance at 60-min retention. The WSPR `atypical` flag replaces this with a z-score against the WSPR climatology's `regionCalendarStatRow.Mean` and `.StdDev` for the current `(band, region, slot)`. The `rising` flag is computed separately from the live-history slope (no climatology needed). The memory-surge path is removed. Governs R5, R6. Resolves O3 (the z-score threshold and confidence discount curve are set in U3).

- KTD5. **Three-flavor atypical label reads the FT8 climatology read-only** — when a WSPR cell is atypical, the engine calls the existing `regionCalendarStats` for the FT8 table (`dx_postgres.go:1812-1866`) for the same `(band, region, slot)`, computes the FT8 rate for the current day_index, and applies a parallel z-score against the FT8 climatology to assign one of three flavors per R8. The FT8 accumulator and schema are not modified. When the FT8 climatology has no entries for the cell (or fewer than a minimum sample-days gate), the flavor is `atypical-wspr-only` per R9. Governs R8, R9.

- KTD6. **Repurpose `/api/prop_intel` in place; keep the path name** — the endpoint keeps its path (`main.go:516` handler registration, `server.go`/`prop_intel.go:949-1043` handler). The response shape changes: cells carry `ssb_open`, `cw_open`, `rising`, `atypical` (with `z_score`, `confidence`, `flavor`), and `from_here` instead of the old `p_open`/`expected_count`/`surge` fields. The handler accepts the same `qth`/`minutes`/`surroundings`/`cw_min_db` params. Resolves O5. Governs R10.

- KTD7. **`wspr-matrix.js` switches from client-side aggregation to consuming the API payload** — the current matrix aggregates `state.liveSpots` client-side (`static/wspr-matrix.js:57-123`). The enriched matrix polls `/api/prop_intel` and renders the backend's per-cell flags. A from-here/global toggle is added, persisted to `localStorage` and a query param, mirroring `show-wspr-spots` (`static/app.js:663, 693`). The client-side live-spot aggregation is removed. Governs R11.

- KTD8. **SSB/CW readability floors and TX Power budget model** — the budget model computes an effective path budget from WSPR SNR (dB, 2500 Hz reference) and reported TX Power (W), then compares against mode-specific readability floors. The floors are: SSB requires a path budget above roughly +10 dB SNR/2500 Hz at typical SSB power (so a WSPR path at SNR +5 dB and 20 W is viable for SSB); CW requires roughly -5 to 0 dB (weaker paths viable). Exact constants are tuned in U2's test scenarios. A path with missing or zero TX Power counts toward existence-open but not toward SSB/CW viability (R4, AE3). Governs R4. Resolves O1.

### Assumptions

- A4. **In-memory WSPR climatology depth tracking.** The in-memory fallback (KTD3) tracks per-bucket sample count but not per-day_index breakdown (the JSONL save keeps the aggregated count). The z-score from the in-memory fallback uses the aggregated mean/stddev, which is less precise than the Postgres per-day-index climatology but sufficient for dev/no-Postgres operation. Cold-start confidence (A1) applies to both paths.

- A5. **`MQTTMessage.TXPower` is ignored by non-WSPR paths.** Adding the field to `MQTTMessage` does not break the SSE stream, `matchAndCreateSpot`, `dx_conditions.go`, or `dx_postgres.go` because they do not reference it. The field is populated only by `handleWSPRSpot`; FT8/DX-cluster/RBN ingests leave it zero. Verified by scan: no code reads a power field on `MQTTMessage` today.

### High-Level Technical Design

```mermaid
flowchart TB
  subgraph Ingest["WSPR Ingest"]
    W[wspr.go handleWSPRSpot] --> P[PersistRawSpot]
    W --> C[WSPR Climatology observe]
    W --> H[hub.history via broadcastWSPRToAll]
  end

  subgraph Climatology["WSPR Climatology"]
    C --> MEM[In-memory buckets]
    C --> PG[(wspr_region_baseline_daily)]
    MEM --> JSONL[wspr_climatology.json fallback]
  end

  subgraph Nowcast["Nowcast Engine"]
    API[/api/prop_intel] --> E[Evaluate]
    E --> HS[Snapshot hub.history]
    E --> ACC[Aggregate per band x region]
    ACC --> SSB[SSB/CW flags from SNR+Power]
    ACC --> RISE[rising slope]
    E --> WC[Load WSPR climatology for slot]
    WC --> Z[Atypical z-score]
    Z --> FT8[Read FT8 climatology]
    FT8 --> FLAVOR[Assign atypical flavor]
    E --> FH[Tag from-here cells]
  end

  subgraph Frontend["Frontend"]
    M[wspr-matrix.js] --> API
    M --> TOG[from-here toggle]
  end

  H --> HS
  P --> PG
```

### Implementation Sequencing

The units are dependency-ordered. U1 (Power threading) is the foundation — nothing else can compute SSB/CW flags without it. U2 (climatology accumulator) and U3 (nowcast engine) can proceed in parallel after U1. U4 (atypical flavor) depends on U3. U5 (API) depends on U3 and U4. U6 (frontend) depends on U5. U7 (retire FT8 nowcast) is last — it removes the old code once the new path serves `/api/prop_intel`.

---

## Implementation Units

### U1. Thread WSPR TX Power onto MQTTMessage

- **Goal:** Make WSPR TX Power available in `hub.history` so the nowcast can compute SSB/CW viability from SNR+Power.
- **Requirements:** R4, AE1, AE2, AE3.
- **Dependencies:** None (foundation unit).
- **Files:**
  - `mqtt.go` — add `TXPower int` field to `MQTTMessage`.
  - `wspr.go` — populate `TXPower` from `wsprSpot.Power` in `handleWSPRSpot` (around line 219-234).
  - `wspr_test.go` — assert `TXPower` is set on the constructed `MQTTMessage`.
- **Approach:**
  1. Add `TXPower int` to `MQTTMessage` in `mqtt.go:12-36` with a comment noting it is WSPR-only.
  2. In `handleWSPRSpot` (`wspr.go:219-234`), set `m.TXPower = s.Power` before the persist/broadcast calls.
  3. Verify no other ingest path (FT8, DX-cluster, RBN) sets `TXPower`; it stays zero for non-WSPR.
- **Patterns to follow:** The existing `MQTTMessage` field additions (e.g., `Source` field) follow the same pattern: add field, populate at the ingest seam, ignore elsewhere.
- **Test scenarios:**
  - Happy path: a `wsprSpot` with `Power: 20` produces an `MQTTMessage` with `TXPower: 20`.
  - Edge case: a `wsprSpot` with `Power: 0` (missing) produces `TXPower: 0`.
  - Non-WSPR source: an FT8 `MQTTMessage` constructed via `handleMQTTMessage` has `TXPower: 0`.
  - Covers AE3: a spot with `TXPower: 0` does not set SSB/CW flags (asserted in U3's tests, but the field availability is asserted here).
- **Verification:** `go test ./...` passes; `wspr_test.go` asserts the `TXPower` field on constructed messages.

### U2. WSPR Climatology Accumulator

- **Goal:** Accumulate a WSPR climatology keyed by (band × 11 DXPulse region × slot-of-day), persisted to Postgres (new parallel table) and a JSONL/in-memory fallback.
- **Requirements:** R1, R2, R3, F1.
- **Dependencies:** U1 (for `TXPower` availability, though the climatology only needs band/region/slot — Power is used by the nowcast, not the climatology count).
- **Files:**
  - `wspr_climatology.go` (new) — `WsprClimatologyEngine` struct: in-memory buckets, Postgres store pointer, JSONL file path, `Observe(m MQTTMessage)`, `Load()`, `Save()`, `RegionCalendarStats(ctx, daysBack, now)`.
  - `dx_postgres.go` — add `wspr_region_baseline_daily` DDL, `observeWsprRegion(m, band, slot, region)`, `flushPendingWsprRegion()`, `wsprRegionCalendarStats(ctx, daysBack, now)`.
  - `main.go` — construct `wsprClimatology` engine, wire it to the Postgres store and JSONL path, call `Observe` from `handleWSPRSpot` after `PersistRawSpot`.
  - `wspr.go` — call `wsprClimatology.Observe(m)` in `handleWSPRSpot` (around line 244-248).
  - `wspr_climatology_test.go` (new) — unit tests for the accumulator.
- **Approach:**
  1. Define `wsprClimatologyBucket struct { Band string; SlotOfDay int; Region string; Count int64 }` and `wsprClimatologyKey` as `band|slot|region`.
  2. `Observe(m)`: resolve the remote-end locator to a DXPulse region via `region.FromLocator`; compute `utcSlotOfDay(m.T)`; increment the in-memory bucket; if Postgres store is non-nil, increment `pendingWsprRegion` for the flush path.
  3. Postgres path: new table `wspr_region_baseline_daily(band, slot_of_day, region, day_index, spot_count)` with PK on `(band, slot_of_day, region, day_index)`. The `observeWsprRegion` method enqueues into `pendingWsprRegion`; `flushPendingWsprRegion` batches the upsert via `pgx.Batch` mirroring the existing `flushPending` pattern (`dx_postgres.go:220-228`).
  4. `wsprRegionCalendarStats(ctx, daysBack, now)`: SQL mirroring `regionCalendarStats` (`dx_postgres.go:1812-1866`) but querying `wspr_region_baseline_daily`. Returns `[]wsprRegionCalendarStatRow{Band, Region, SlotOfDay, Mean, StdDev, Today, SampleDays}`.
  5. JSONL/in-memory fallback: when Postgres is nil, `RegionCalendarStats` computes mean/stddev from the in-memory buckets directly (aggregated count over the retention span, normalized to per-day rate). `Save()` writes the in-memory buckets to `wspr_climatology.json`; `Load()` reads them on startup.
  6. In `main.go`, construct `wsprClimatology = newWsprClimatologyEngine(wsprClimatologyFile)` and wire the store pointer if Postgres is configured. Add a `-wspr-climatology-file` flag mirroring `-dx-baseline-file`.
  7. In `handleWSPRSpot` (`wspr.go:244-248`), add `wsprClimatology.Observe(m)` after `PersistRawSpot`.
- **Patterns to follow:**
  - `DxBaselineEngine` (`dx_conditions.go:92-154`) for the engine struct shape (in-memory + store + JSONL).
  - `dx_baseline_cluster` separate-flush pattern (`dx_postgres.go:230-239`) for the parallel table.
  - `regionCalendarStats` SQL (`dx_postgres.go:1812-1866`) for the climatology query.
  - `dxPulseRegionBaselineKeysForSpot` (`dx_postgres.go:712-750`) for the key-emission pattern — but WSPR keys only the remote end's region (one key per spot, not two), since the climatology is global-mesh, not from-here.
- **Test scenarios:**
  - Happy path (Covers F1): a WSPR spot with remote locator in EU on 20m at UTC 10:00 increments the `(20m, EU, slot=20)` bucket.
  - Region resolution: a spot with remote locator `FN31` (NA) and another with `JO62` (EU) on the same band/slot increment different buckets.
  - Unknown region: a spot with an invalid remote locator does not increment any bucket (graceful skip).
  - Persistence (in-memory): after `Observe` calls, `RegionCalendarStats` returns the expected mean/stddev.
  - Persistence (Postgres): with a mock store, `observeWsprRegion` enqueues the right key; `flushPendingWsprRegion` issues the upsert. (No live PG harness — test the enqueue/flush logic, not the SQL.)
  - Cold-start (Covers AE8): a freshly loaded climatology with 5 days of depth returns `SampleDays: 5` and a non-zero StdDev.
  - JSONL round-trip: `Save()` then `Load()` preserves bucket counts.
- **Verification:** `go test ./...` passes; `wspr_climatology_test.go` covers all scenarios above.

### U3. WSPR Nowcast Engine

- **Goal:** Replace the FT8 nowcast in `prop_intel.go` with a WSPR-primary nowcast producing per-cell SSB/CW flags, `rising` slope, and `atypical` z-score with confidence.
- **Requirements:** R4, R5, R6, R7, F2, F3, AE1, AE2, AE3, AE7, AE8, AE9.
- **Dependencies:** U1 (TXPower), U2 (WSPR climatology for the z-score).
- **Files:**
  - `prop_intel.go` — rewrite `propIntelEngine.Evaluate` to consume WSPR spots only, compute SSB/CW flags, `rising`, and `atypical`. Replace the `propIntelCell` struct with the new fields. Remove the memory-surge baseline path (`computeSurgeBaselines`, `detectSurges`, `memorySurgeBaseline`, `surgeSubAcc`, `surgeCellSubs`).
  - `prop_intel_test.go` — rewrite tests for the new cell shape and flags.
- **Approach:**
  1. Replace `propIntelCell` fields: `{Band, Region, SSBOpen, CWOpen, Rising, Atypical *AtypicalInfo, FromHere, Confidence, ZScore, Sources}`. `AtypicalInfo{ZScore, Confidence, Flavor}`.
  2. `Evaluate(qth, surroundings, minutes, cwMinDb, history, now, surgeThreshold)`:
     - Snapshot `hub.history` for `[now-minutes*60, now]` (same pattern as existing handler, `prop_intel.go:984-1020`).
     - Walk WSPR spots only (`m.Source == "wspr"`); skip FT8/DX-cluster/RBN.
     - For each spot, resolve the remote end via `resolveRemoteEnd(m, qthSet)` (existing helper, `prop_intel.go:467-509`); skip if `region == Unknown`.
     - Aggregate per `(band, region)` cell: unique senders, SNR list, TXPower list, presence of the operator's QTH as one end (for `FromHere`).
     - Compute `SSBOpen`/`CWOpen` per R4 and KTD8: from the best path in the cell (highest SNR+Power budget); if any path's budget exceeds the SSB floor, `SSBOpen=true`; same for CW floor. Missing TXPower → flags false for that path.
     - Compute `Rising` per R5: split the window into sub-windows (on the order of 30-60 min; tuned here); compute the rate (unique senders per sub-window) for the first and second halves; `Rising = (secondHalfRate > firstHalfRate * slopeThreshold)`. The slope threshold is a constant (e.g. 1.5x); tuned in test scenarios.
     - Compute `Atypical` per R6 and KTD4: load the WSPR climatology for the current slot via `wsprClimatology.RegionCalendarStats`; for each cell, compute `z = (liveRate - climatology.Mean) / climatology.StdDev`; if `z >= surgeThreshold` and `climatology.StdDev > 0` and `SampleDays >= minSampleDays`, set `Atypical = &AtypicalInfo{ZScore: z, Confidence: confidenceCurve(SampleDays), Flavor: ""}`. The confidence curve scales from 0.3 at `SampleDays=1` to 1.0 at `SampleDays>=30`. If `SampleDays < minSampleDays`, do not set `Atypical` (insufficient data, not cold-start discount — cold-start still sets it per F3).
     - Cold-start (F3, AE8): if `SampleDays` is between `minSampleDays` and 30, set `Atypical` with a discounted `Confidence` (e.g. `0.3 + 0.7 * (SampleDays/30)`). Never suppress the flag; only discount confidence.
     - Compute `FromHere` per R7: a cell is `FromHere` if any spot in the cell has the operator's QTH as one end.
  3. Remove `poissonPOpen`, `computeSurgeBaselines`, `detectSurges`, `memorySurgeBaseline`, `surgeSubAcc`, `surgeCellSubs`, `loadRegionBaselines` (the FT8 version), and the FT8-specific nowcast constants (`propIntelNowcastWindowMin` is reused; `propIntelSurgeZThreshold` is reused as the atypical threshold).
- **Patterns to follow:**
  - `propIntelCellKey`/`propIntelCellAcc` accumulator pattern (`prop_intel.go:174-188`).
  - `resolveRemoteEnd` for QTH-aware remote resolution (`prop_intel.go:467-509`).
  - `region.FromLocator` for region axis population (`internal/region/region.go:93-99`).
  - `baselineScoreQuantilesForBand` for the cluster→global fallback pattern — adapted to "climatology → insufficient depth" fallback (`dx_conditions.go:1265-1314`).
- **Test scenarios:**
  - Covers AE1: a 20m JA cell with SNR +5 dB, Power 20 W → `SSBOpen=true`, `CWOpen=true`.
  - Covers AE2: a 30m VK cell with SNR -8 dB, Power 5 W → `SSBOpen=false`, `CWOpen=true`.
  - Covers AE3: a 40m EU cell with `TXPower=0` → cell exists (counted in `Sources`) but `SSBOpen=false`, `CWOpen=false` from that path.
  - Covers AE7: a 20m EU cell with a rising slope over 45 min but rate within 1 stddev of climatology mean → `Rising=true`, `Atypical=nil`.
  - Covers AE8: a 12m JA cell with 5-day climatology depth, z-score 2.2 → `Atypical` set with discounted `Confidence` (roughly 0.42 at 5/30 days).
  - Covers AE9: a 40m NA cell where the operator's QTH is one end → `FromHere=true`; a 20m JA cell with no operator-end path → `FromHere=false`.
  - Edge case: a cell with no climatology entries (zero `SampleDays`) → `Atypical=nil` (insufficient data, not cold-start).
  - Edge case: `climatology.StdDev == 0` → `Atypical=nil` (guard against divide-by-zero, mirroring `prop_intel.go:811`).
  - Integration (Covers F2): the `/api/prop_intel` handler returns cells with the new fields.
- **Verification:** `go test ./...` passes; `prop_intel_test.go` covers all scenarios above with hand-built history slices and a mock WSPR climatology.

### U4. Atypical Flavor (FT8 Cross-Reference)

- **Goal:** When a WSPR cell is flagged atypical, read the FT8 climatology and assign one of three flavors.
- **Requirements:** R8, R9, AE4, AE5, AE6.
- **Dependencies:** U3 (atypical flag must exist).
- **Files:**
  - `prop_intel.go` — add the flavor-assignment step in `Evaluate`, after the atypical z-score. Add a `flavorFT8CrossRef` helper.
  - `prop_intel_test.go` — tests for the three flavors and the unavailable fallback.
- **Approach:**
  1. After computing `Atypical` for all cells, for each atypical cell, call the existing `dxBaseline.store.regionCalendarStats(ctx, regionBaselineDaysBack, now)` for the same `(band, region, slot)`.
  2. If the FT8 climatology row exists and `SampleDays >= minSampleDays` and `StdDev > 0`: compute the FT8 z-score for the current day's rate (`Today` field from the row, or the live FT8 rate from `hub.history` — use `Today` for consistency with the climatology's own today value). If `ft8Z >= ft8AtypicalThreshold` → flavor `atypical-both`. Else if the FT8 rate for today is effectively zero or far below typical (`Today == 0` or `Today < Mean * 0.1`) → flavor `atypical-wspr-silent-ft8`. Else → `atypical-wspr-only` (WSPR odd, FT8 normal).
  3. If the FT8 climatology row is missing or `SampleDays < minSampleDays` (Covers AE6, R9): flavor `atypical-wspr-only` and set a note field on the cell (e.g. `FT8CrossRef: "unavailable"`).
  4. Access the FT8 store via the same `e.baseline.mu.RLock(); st := e.baseline.store` pattern (`prop_intel.go:674-677`).
- **Patterns to follow:**
  - `loadRegionBaselines` pattern (`prop_intel.go:637-640`) for accessing the FT8 store.
  - `regionCalendarStatRow` struct (`dx_postgres.go:1795-1806`) for the FT8 climatology data.
- **Test scenarios:**
  - Covers AE4: a 15m SA cell that is WSPR-atypical; FT8 climatology also shows atypical rate → `Flavor: "atypical-both"`.
  - Covers AE5: a 10m AF cell that is WSPR-atypical; FT8 `Today == 0` → `Flavor: "atypical-wspr-silent-ft8"`.
  - Covers AE6: a 17m OC cell that is WSPR-atypical; FT8 climatology has no entries for the cell → `Flavor: "atypical-wspr-only"`, `FT8CrossRef: "unavailable"`.
  - Edge case: FT8 climatology has entries but `StdDev == 0` → treat as unavailable (flavor `atypical-wspr-only`).
  - Edge case: FT8 climatology has entries, `Today > 0` but FT8 z-score is below threshold → `atypical-wspr-only` (WSPR odd, FT8 normal).
- **Verification:** `go test ./...` passes; `prop_intel_test.go` flavor tests pass with a mock FT8 store.

### U5. API Handler Repurpose

- **Goal:** Repurpose `/api/prop_intel` to serve the new WSPR cells with the from-here toggle.
- **Requirements:** R10, F2.
- **Dependencies:** U3, U4.
- **Files:**
  - `prop_intel.go` — update `propIntelHandler` (`prop_intel.go:949-1043`) and the response struct (`propIntelResponse`).
  - `main_test.go` — update `TestPropIntelIntegration` for the new response shape.
- **Approach:**
  1. Replace `propIntelResponse` fields: keep `{QTH, Minutes, Now}`, replace `Bands`/`Regions`/`Cells` with `Cells []wsprIntelCell` (the new cell struct from U3). Add a `FromHere bool` top-level field indicating whether the from-here filter is active (driven by a `from_here=true` query param).
  2. Add a `from_here` query param: when `true`, the response only includes cells where `FromHere == true`. Default is false (global mesh view).
  3. Keep `surge_threshold` as the atypical z-score threshold override (existing param).
  4. The handler still snapshots `hub.history` via the same `sync.Pool` pattern (`prop_intel.go:984-1020`).
- **Patterns to follow:**
  - Existing `propIntelHandler` history snapshot + sync.Pool pattern (`prop_intel.go:949-1043`).
  - `resolveQTHQuery` (`server.go:454-465`) for QTH resolution.
- **Test scenarios:**
  - Integration (Covers F2): `GET /api/prop_intel?qth=JO62&minutes=15` returns cells with `ssb_open`, `cw_open`, `rising`, `atypical`, `from_here` fields.
  - From-here filter: `GET /api/prop_intel?qth=JO62&minutes=15&from_here=true` returns only cells where the operator's QTH is one end.
  - Stats accounting: `/api/stats` reflects `prop_intel.requests` delta (existing test pattern, `main_test.go:1688-1719`).
- **Verification:** `go test ./...` passes; `main_test.go` integration test asserts the new response shape.

### U6. Frontend Matrix Enrichment

- **Goal:** Enrich `wspr-matrix.js` to render SSB/CW flags, rising, atypical with flavor, and a from-here/global toggle.
- **Requirements:** R11, AE9.
- **Dependencies:** U5 (API payload).
- **Files:**
  - `static/wspr-matrix.js` — replace client-side aggregation with `/api/prop_intel` polling; render new flags; add from-here toggle.
  - `static/style.css` — styles for the new flag indicators (SSB/CW icons, rising arrow, atypical flavor color coding).
  - `static/app.js` — wire the from-here toggle to localStorage and the URL param, mirroring `show-wspr-spots`.
- **Approach:**
  1. Replace `updateWsprMatrix()` (`static/wspr-matrix.js:57-123`): instead of iterating `state.liveSpots`, poll `/api/prop_intel?qth=${state.qth}&minutes=${state.minutes}&from_here=${state.wsprFromHere}` on a 30-60s interval (throttled). Cache the response; re-render when it changes.
  2. Render each cell with: background color intensity for path count (existing), plus icon/letter overlays for SSB (`S`), CW (`C`), rising (`↑`), atypical (`!` with flavor color: green for `atypical-both`, yellow for `atypical-wspr-only`, orange for `atypical-wspr-silent-ft8`).
  3. Add a from-here/global toggle button on the panel header; persist to `localStorage['wsprMatrixFromHere']` and add a `from_here` URL param on the API call.
  4. Remove the client-side `regionForLocatorCached` aggregation for the matrix (it's now backend-driven); keep `WSPR_REGIONS` and `BAND_ORDER` constants.
- **Patterns to follow:**
  - `show-wspr-spots` toggle pattern (`static/app.js:663, 693`) for the from-here toggle.
  - `band-lab.js` polling pattern (`static/band-lab.js:985-994`) for the API fetch + cache.
  - Existing matrix DOM rebuild fingerprint (`static/wspr-matrix.js:91-96`).
- **Test scenarios:**
  - `npm test` (vitest) covers the matrix rendering with a mock API response.
  - Happy path: a cell with `ssb_open=true`, `cw_open=true`, `rising=true`, `atypical={flavor:"atypical-both"}` renders the correct overlays.
  - From-here toggle: toggling the button changes the API call's `from_here` param and re-renders.
  - Empty response: "No WSPR paths open" message (existing behavior preserved).
- **Verification:** `npm test` passes; manual visual check in `-dev` mode.

### U7. Retire FT8 Nowcast Path

- **Goal:** Remove the FT8 nowcast code from `prop_intel.go` now that the WSPR engine serves `/api/prop_intel`.
- **Requirements:** R12.
- **Dependencies:** U3, U5 (the new path must serve the endpoint before the old code is removed).
- **Files:**
  - `prop_intel.go` — remove dead FT8 nowcast code: `poissonPOpen`, `computeSurgeBaselines`, `detectSurges`, `memorySurgeBaseline`, `surgeSubAcc`, `surgeCellSubs`, `loadRegionBaselines` (FT8 version), FT8-specific constants. Remove the FT8 `canonicalSource` mapping for FT8/DX-cluster sources (WSPR-only now).
  - `prop_intel_test.go` — remove FT8 nowcast tests (the surge tests at `prop_intel_test.go:377-671` that test the memory baseline; keep WSPR-cell tests).
  - `main.go` — remove any FT8 nowcast wiring if separate (check: `propIntel.baseline` may still be needed for U4's FT8 cross-reference — keep the `baseline` pointer, remove only the nowcast-specific wiring).
- **Approach:**
  1. After U3 and U5 are complete and tested, scan `prop_intel.go` for FT8-nowcast-specific code: `poissonPOpen`, the memory-surge structs and helpers, the FT8 `loadRegionBaselines`, the `canonicalSource` mapping for non-WSPR sources.
  2. Remove the dead code. Keep the `propIntel.baseline` pointer (used by U4 for the FT8 cross-reference) and the `resolveRemoteEnd` helper (used by U3).
  3. Update `prop_intel_test.go`: remove tests that exercise the FT8 nowcast or memory-surge baseline specifically; keep tests that exercise the WSPR nowcast, atypical z-score, and flavor.
- **Patterns to follow:** Standard dead-code removal; verify with `go vet` and `go test`.
- **Test scenarios:**
  - Test expectation: `go build ./...` succeeds with no unused-function warnings.
  - Test expectation: `go test ./...` passes with the reduced test set.
  - Test expectation: `go vet ./...` is clean.
- **Verification:** `go build ./... && go test ./... && go vet ./...` all pass.

---

## Verification Contract

| Command | What it proves | Units |
|---|---|---|
| `go test ./...` | All Go unit + integration tests pass | U1–U5, U7 |
| `go vet ./...` | No static analysis issues; dead code removed | U7 |
| `go build ./...` | Compiles with the new `TXPower` field and removed FT8 nowcast | U1, U7 |
| `npm test` | Frontend tests pass for the enriched matrix | U6 |
| `npm test -- --runInBand` | (If vitest run-in-band needed for CI) | U6 |

**Behavioral verification:**
- Manual: run `go run . -dev -port 8080`, open the UI, observe the WSPR matrix rendering SSB/CW flags, rising arrows, and atypical flavor colors. Toggle from-here vs global. Confirm cells update on a 30-60s interval.
- Manual: `curl -s 'http://localhost:8080/api/prop_intel?qth=JO62&minutes=15' | jq '.cells[0]'` shows the new cell fields.
- Manual: with no Postgres configured (`-dev`), confirm the in-memory WSPR climatology fallback produces atypical z-scores after a few minutes of WSPR ingest (cold-start confidence is visible).

---

## Definition of Done

**Global:**
- All implementation units U1–U7 are complete and verified per their unit-level Verification.
- `go test ./...`, `go vet ./...`, `go build ./...`, and `npm test` all pass.
- The `/api/prop_intel` endpoint serves WSPR-native cells with SSB/CW, rising, atypical (with flavor), and from_here fields.
- The `wspr-matrix.js` frontend renders the new flags and supports the from-here toggle.
- The FT8 nowcast code is removed; the FT8 `dx_region_baseline_daily` accumulator and `dx_conditions.go` DX Potential Score are untouched.
- No dead-end or experimental code from abandoned approaches remains in the diff.
- Product Contract is unchanged from the brainstorm; no R-IDs renumbered.

**Per-unit:**
- U1: `TXPower` field on `MQTTMessage`, populated by WSPR ingest, zero for other sources.
- U2: WSPR climatology accumulator with Postgres + JSONL fallback; `Observe` called from `handleWSPRSpot`.
- U3: WSPR nowcast produces SSB/CW, rising, atypical with confidence; cold-start discounts confidence per F3.
- U4: Atypical flavor assigned via FT8 cross-reference; graceful fallback to `atypical-wspr-only` when FT8 climatology is thin.
- U5: `/api/prop_intel` serves the new cells; `from_here` param works.
- U6: Matrix renders new flags; from-here toggle persisted to localStorage.
- U7: FT8 nowcast dead code removed; `go vet` clean.