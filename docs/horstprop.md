# horstprop — HF link-quality scoring service

Canonical spec for `horstprop`. Derived from the build brief (`horstprop.in.md`), with the
Phase-0 discovery questions (§3) resolved against the HorstReporter codebase and the
placement reconciled to the decision to ship it as `cmd/horstprop` in this repo.

> Working name. Rename freely (`linkscore`, `dxqual`, …).

---

## 0. Placement & relationship to other docs

- **Lives in this repo as `cmd/horstprop/`** — a *separate binary and process*, independently
  deployable (its own container image + systemd unit), mirroring `cmd/horstoperator-agent/`.
  This satisfies the brief's intent: the HorstReporter **binary does not grow**, horstprop is
  consumed/consumes **read-only over HTTP**, and it imports **no HorstReporter runtime code**.
  (It shares only the contract types under `internal/propcontract`.) The brief's literal "new
  repo" was relaxed to "same repo, separate binary" per the operator's decision.
- **Contract authority:** this document is authoritative for horstprop's scoring contract.
  It **supersedes** the earlier `docs/dxcluster-score-interface.md` sketch (a client-side v1
  stopgap with a `go/watch/wait` decision + `factors`). horstprop's output is instead a
  **`0–100` score + `grade` + per-layer breakdown** (§5.5). The `internal/propcontract` types
  and the `cmd/horstprop` stub must be realigned to this contract before feature work.

---

## 1. Goal

A **standalone service** that scores the HF link quality of a DX spot — the path from the
operator's home station to the spotted DX station — and returns a single `0–100` value plus a
confidence figure and a human-readable reason.

Two distinct data roles, do not confuse them:

- **The empirical propagation data** (reception reports) is supplied by the existing
  **HorstReporter** backend, which already aggregates it. This feeds Layer 1. horstprop
  consumes that feed; it does **not** connect to RBN or PSKReporter itself.
- **The DX spots to be scored** are supplied by the operator's **DX-cluster client**, via this
  service's scoring API. They do **not** come from HorstReporter.

Empirical-first: real observations of what is being heard *right now* outrank any model. The
model is a fallback for paths with no live data.

---

## 2. Context & hard constraints

- HorstReporter already exists and provides the **aggregated reception-report feed**.
  **Do not add this scoring into HorstReporter.** Separate, independently deployable binary;
  HorstReporter stays small and is consumed **read-only**.
- HorstReporter also exposes a **DX-cluster feed**. **Do not use those DX-cluster spots** for
  anything. Spots to score come only from the operator's cluster client (§3 A3).
- Do **not** connect directly to RBN (telnet) or PSKReporter (MQTT) for Layer 1 — consume
  HorstReporter's already-aggregated feed.
- The three scoring layers, in order of trust:
  1. **Empirical** — reception reports, **via HorstReporter**.
  2. **MUF gate** — KC2G near-real-time ionospheric state (fetched directly).
  3. **Model** — self-hosted ITU-R P.533 (ITURHFProp) or VOACAP, precomputed and cached.
- **Build in phases. Defer Layer 3 (the model) to a later phase.** Layers 1 and 2 deliver a
  useful score on their own.
- **Never scrape VOACAP Online (voacap.com).** Self-host the engine for Layer 3.

---

## 3. Phase-0 discovery — resolved against the codebase

| # | Question | Resolution |
|---|----------|------------|
| A1 | HorstReporter language / tooling / conventions | **Go 1.24, stdlib `net/http`, no web framework**, single binary + `cmd/` sub-binaries, plain-ES-modules frontend. **horstprop matches: Go + `net/http`.** |
| A2 | How HorstReporter exposes the reception feed (transport + shape) | **Transport:** `GET /api/stream` (SSE, live), `target` required. **Shape (`toStreamSpot`) is LOCATOR-LEVEL:** `lat/lng, snr, ageSeconds, locator, reporterLocator, sourceType, band` — **no callsigns, freq, or mode** (see §5.2). `PropFeedSource` adapts this; **filter `sourceType=="mqtt"`** (PSKReporter), **drop `"dxcluster"`**. **Coverage = RESOLVED (Option C):** a read-only `rings` param was added to `/api/stream` — with a locator `target` it matches any sender/receiver within `rings` grid-squares (O(1), capped at 30). horstprop uses **`target=<home>&rings=<area-rings>`** as one persistent ambient feed; the area size is operator-configurable (`-area-rings`, default 3). Layer 1 then keys reports where `rx_locator` is near home. |
| ⚠️ A2-gap | RBN availability | **HorstReporter has no RBN/telnet ingest today — the feed is PSKReporter-only.** Layer 1 is therefore PSK/FT8-dominated until RBN is added upstream. Treat RBN-derived CW/RTTY skimmer SNR as a *future* input; do not assume it exists. |
| A3 | How DX spots to score reach the service | **Request/response scoring API:** the cluster client POSTs a spot `(dx_call, freq_hz, …)`, gets a Score. **Never** read HorstReporter's cluster feed for spots. |
| A4 | How the score is delivered | Scoring-API response (+ optional push channel, Phase 4). **Never** push into HorstReporter — it exposes no write path anyway. |
| A5 | DX-station location source | Spot-supplied grid/locator when present, else **DXCC-entity centroid**. HorstReporter works in **Maidenhead locators and has no DXCC/cty.dat resolution** — horstprop must bring its own country file (AD1C `cty.dat` / Club Log). Great-circle/bearing/haversine + locator math can be ported from `dx_conditions.go` (`haversineKm`, `bearingDirection`, `distanceKmForLocators`, `normalizeSource4`). |
| A6 | Deployment host | Linux shack box; ship a container image + systemd unit. |

Verify at build time (don't assume): the exact KC2G machine-readable endpoint (§6).

---

## 4. Architecture

Single service, internally layered. Each layer is independent and may be **absent** (not yet
built, or its source down) without breaking the others.

```
   cluster client ──(spot: dx, freq)──▶┌───────────────────────────┐
                                        │ Scoring API               │
                                        └─────────────┬─────────────┘
                                                      ▼
                                        ┌───────────────────────────┐
                                        │ Geometry: call→location,  │
                                        │ bearing, distance, band   │
                                        └─────────────┬─────────────┘
                                                      ▼
        ┌───────────────── Scoring engine (empirical-first blend) ──────────────┐
        │                                                                        │
  ┌─────▼──────┐            ┌──────────────┐            ┌──────────────────────┐ │
  │ L1 Empirical│           │ L2 MUF gate  │            │ L3 Model (Phase 3+)  │ │
  │ rolling store│          │ KC2G nowcast │            │ P.533 / VOACAP cache │ │
  └─────▲──────┘            └──────▲───────┘            └──────────▲───────────┘ │
        │                          │                               │             │
  HorstReporter feed         KC2G (direct)              self-hosted engine       │
  (reception reports)                                   + solar (Phase 3)         │
        └──────────────── blend ─────────────────────────────────┘              │
                                                      ▼                          │
                                        ┌───────────────────────────┐           │
                                        │ Score {0–100, conf, reason}│◀──────────┘
                                        └─────────────┬─────────────┘
                                                      ▼
                                           response to cluster client
```

Suggested modules: `feed/` (`PropFeedSource` from HorstReporter), `geo/` (locator math +
callsign resolver), `sources/kc2g/`, `sources/solar/`, `model/` (Phase 3), `engine/` (blend),
`api/` (scoring API), `store/` (L1 rolling store + caches), `config/`.

---

## 5. Core data contracts

### 5.1 Spot to score (input — from the cluster client via the scoring API)
```jsonc
{ "dx_call": "VK9XX", "freq_hz": 14074000, "timestamp": "2026-06-24T13:22:05Z",
  "mode": "FT8", "grid": null }
```

### 5.2 PropReport (Layer 1 input — adapted from HorstReporter's stream; see A2)

**Discovered constraint (Phase-0 inspection):** HorstReporter's *public* stream shape
(`toStreamSpot`) is **locator-level** — it exposes `lat/lng, snr, ageSeconds, locator,
reporterLocator, sourceType, band` and **NOT** callsigns, frequency, or mode. Layer 1 is
therefore **locator/path based**, not callsign based (which is fine: "is the DX-side grid being
heard near home on this band, recently, and how strong" is a locator+SNR question).

```jsonc
{ "tx_locator": "OH29", "rx_locator": "JO31", "snr_db": 14, "band": "20m",
  "source": "mqtt", "observed_at": "2026-06-24T13:11:40Z" }
```
> The `PropFeedSource` adapter maps each `streamSpot` to this shape and **drops
> `sourceType=="dxcluster"`**. `tx_locator` = the heard/DX-side grid (`locator`); `rx_locator` =
> the monitoring receiver grid (`reporterLocator`). DXCC entity is derived by horstprop (A5) when
> needed; it is not present upstream. `observed_at` is stamped from `ageSeconds` at ingest.

### 5.3 Geometry (derived, shared by all layers)
```jsonc
{ "dx_lat": -10.5, "dx_lon": 105.6, "loc_source": "centroid|grid",
  "bearing_deg": 92.4, "distance_km": 12180, "band": "20m" }
```

### 5.4 LayerResult (each layer returns this or `null` when unavailable)
```jsonc
{ "available": true, "score": 0-100, "confidence": 0.0-1.0, "detail": { /* layer-specific */ } }
```
The MUF gate is special: instead of `score` it returns `gate` ∈ `[0,1]` (a multiplier).

### 5.5 Score (output)
```jsonc
{
  "dx_call": "VK9XX", "freq_hz": 14074000, "band": "20m",
  "timestamp": "2026-06-24T13:22:05Z",
  "bearing_deg": 92.4, "distance_km": 12180,
  "score": 71, "grade": "B", "confidence": 0.78,
  "layers": {
    "empirical": { "available": true, "score": 74, "confidence": 0.8,
                   "detail": { "n_reports": 6, "best_snr_db": 14, "window_min": 11, "match": "dxcc" } },
    "muf_gate":  { "available": true, "gate": 0.96, "confidence": 0.6,
                   "detail": { "muf_mhz": 21.4, "freq_mhz": 14.07, "state": "open", "age_min": 28 } },
    "model":     { "available": false }
  },
  "reason": "Heard by 6 EU receivers in last 11 min (best +14 dB); path MUF ~21.4 MHz > 14.07 MHz — open."
}
```

`freq→band` and `score→grade` are config-driven lookup tables. Suggested grade bands:
`A ≥ 75`, `B 55–74`, `C 35–54`, `D < 35`, `?` when `score` is null or confidence < 0.2.

---

## 6. External data sources

| Source | Access | Key facts & limits |
|--------|--------|--------------------|
| **HorstReporter** (Layer 1 data + DX-cluster feed) | `/api/stream` (SSE) or `/api/capture_snapshot` | Supplies the aggregated **PSKReporter** reception feed — Layer 1 source. **Read-only.** `PropFeedSource` adapter abstracts the transport. **Its DX-cluster feed must not be used.** **No RBN today** (A2-gap). |
| RBN / PSKReporter | **via HorstReporter only** | horstprop does **not** connect directly. For interpreting the data: RBN SNR (if/when present) is ~45 s in a 50 Hz channel, **relative to each skimmer's antenna/noise** — use comparatively, average across receivers. PSKReporter reports carry SNR + `receiverLocator`. Coverage dense in EU/NA, thin elsewhere. |
| **KC2G** (Layer 2) | `https://prop.kc2g.com/` — per-grid map at `…/api/moflof.svg?grid=<GRID>&metric=mof_sp` (and `lof_sp`). **Prefer a JSON/GeoJSON station feed** — discover the exact endpoint from `github.com/arodland/prop` or the site's network calls. | Near-real-time MUF(3000)/foF2 from worldwide ionosondes via GIRO. Interpolated, ~half an hour old; refreshed ~every 5 min. Poll ~5–10 min, cache. |
| **Solar indices** (Phase 3) | HamQSL XML (`hamqsl.com/solarxml.php`) and/or NOAA SWPC `services.swpc.noaa.gov/text/wwv.txt`. | Current SFI / A / K / SSN. Scales the monthly-median model. ~3-hourly. |
| **Propagation engine** (Phase 3) | **Self-hosted** ITURHFProp (`github.com/ITU-R-Study-Group-3/ITU-R-HF`, ITU-R P.533-14) or `voacapl` (NTIA Linux port). | Outputs SNR, basic circuit reliability (BCR), S-meter estimate, OPMUF. Monthly-median — **not** a now-cast. **Never** call VOACAP Online. |

---

## 7. Scoring model

Geometry is computed once per spot. Each layer is then evaluated; missing layers return `null`.
The blend is empirical-first with the MUF gate applied multiplicatively.

### 7.1 Layer 1 — Empirical (from the HorstReporter feed)
The feed is the **area-of-interest stream** `target=<home>&rings=<area-rings>` (A2). It is
**locator-level** (no callsigns), so Layer 1 is **path/locator based**: ingest into a **rolling
store** of recent `PropReport`s, TTL window default **30 min**, keyed by `(band, tx-locator-region)`
— the DX-side grid heard within the home area — with `rx_locator ≈ home` as the home-side filter.
DXCC-entity keying is available on the *scored spot* side (cty.dat, A5) but not on the feed.

For a spot, look up reports that establish the DX is propagating toward us:
- the DX callsign heard by receivers **near home / in EU** on this band, and/or
- recent reports whose path endpoints approximate **home→DX** on this band.

Score from the matching reports:
- `snr_score` = normalize the best (and median) SNR. SNR scales differ by source — keep
  **separate calibration curves** (RBN CW/RTTY vs. PSK/FT8). Rough RBN anchors:
  `-5 dB→~20`, `+10 dB→~60`, `+25 dB→~90`. FT8 anchors: `-18 dB→~25`, `-5 dB→~55`, `+5 dB→~85`.
- `score` = `snr_score` adjusted by report count and recency.
- `confidence` rises with report count, recency, path-match quality (exact DXCC > region), and
  mode diversity. ~0.5–1.0 when present.
- **Absence of reports ≠ closed band.** When no match, Layer 1 returns `null` — do **not** emit
  a low score from silence.

### 7.2 Layer 2 — MUF gate (KC2G)
Evaluate the **minimum MUF along the path** (weakest control point): sample MUF(3000) at the path
midpoint and at control points ~1500 km in from each end; take the minimum. Let
`r = freq_MHz / muf_min_MHz`.

- `r ≤ 0.85` (≈ OWF) → **open**, `gate = 1.0`.
- `0.85 < r ≤ 1.0` → **marginal**, `gate` scales linearly `1.0 → 0.4`.
- `r > 1.0` → **above MUF**, `gate` falls `0.4 → 0.0` quickly.

`confidence ≈ 0.6`, reduced as `age_min` grows. Surface `muf_min`, `freq`, `state`, `age_min`.

### 7.3 Layer 3 — Model (Phase 3+)
Lookup REL and SNR from a **precomputed grid** keyed by
`(band, distance_bucket, azimuth_bucket, utc_hour, month, SSN)`. Convert REL (0–1) to 0–100; use
predicted SNR as a cross-check. Apply **solar scaling** (current SFI/SSN vs the model's assumed
SSN). `confidence ≈ 0.5` baseline. Precompute O(1) lookups — **never** run the engine per spot.

### 7.4 Blend (end-state)
```text
geometry = resolve(spot)
L1 = empirical(spot, geometry)   # LayerResult | null  (from HorstReporter store)
L2 = muf_gate(geometry)          # gate in [0,1], conf  | null
L3 = model(geometry, now)        # LayerResult | null   (Phase 3+)

if   L1: base, base_conf = L1.score, L1.confidence
elif L3: base, base_conf = L3.score, L3.confidence
else:    base, base_conf = 50, 0.15          # unknown / neutral

gate     = L2.gate if L2 else 1.0
score    = round(clamp(base * gate, 0, 100))
conf     = combine(base_conf, L2)
```
- `combine`: start from `base_conf`; a confident MUF gate that strongly closes the path raises
  confidence in the (low) result. When **both** L1 and L3 are present and agree, raise
  confidence; when they disagree, lower it.
- Assemble `reason` from whichever layers contributed.
- Degrade gracefully: in Phases 1–2 `L3` is always `null`; in Phase 1 `L2` is always `null`. The
  same code path works throughout.

---

## 8. Phased delivery plan

Each phase ends with green tests and a runnable service.

### Phase 0 — Skeleton, feed & scoring API
- `cmd/horstprop` skeleton, separate from the HorstReporter binary. Resolve §3 (done above).
- `PropFeedSource` adapter consuming HorstReporter's `/api/stream` (or `/api/capture_snapshot`),
  mapping `Spot` → `PropReport`, **dropping `sourceType=="dxcluster"`**.
- Scoring API (A3/A4): accept a spot `(dx, freq)`, return a Score. **Reject any attempt to source
  spots from HorstReporter's cluster feed.**
- Config: home grid (default `JO32we`), station callsign (default `DL9ET`), windows/thresholds,
  per-source endpoints, source on/off toggles.
- Geometry module: Maidenhead ↔ lat/lon; callsign → location via `cty.dat`/Club Log with grid
  override; great-circle bearing + distance (port from `dx_conditions.go`); `freq → band` table.
- Engine skeleton returning a Score with `score: null`, `grade: "?"`, all layers
  `available: false`.
- Health/readiness endpoint, structured logging, basic metrics.
- Test harness with recorded fixtures; CI; Dockerfile + systemd unit.
- **AC:** prop reports flow from HorstReporter into the service; POSTing a spot returns a
  well-formed (empty) Score with correct geometry; all tests pass; zero writes to HorstReporter;
  its cluster feed is never read.

### Phase 1 — Layer 1 (empirical) ✅ done
- Rolling store from the feed (`PropReport`s), keyed by `(band, dxcc/region)`, TTL window, capped;
  optional SQLite for restart resilience.
- Empirical scorer per §7.1, wired into the engine (empirical-first).
- **AC:** a posted spot with recent matching reports gets an empirical score + a citing reason;
  no match → `score: null` (not a low score); feed parsing covered by replayable fixtures.

### Phase 2 — Layer 2 (MUF gate) ✅ done

> **Empirical-first blend (decided).** Per §1 ("observations outrank any model"), the gate is
> softened by a confidence-weighted floor rather than applied as a pure multiplier:
> `effGate = gate + (1-gate)·trust`, where `trust = clamp((L1.conf-0.5)/0.42, 0,1) · 0.85`.
> Strong, confident empirical evidence resists a closing MUF (the 15m case stays ~B); weak or
> absent empirical lets the gate fully apply (a quiet high band still goes to ~0). A little model
> authority (15%) is kept in case the band closed since the last report.
- KC2G fetcher (discover the JSON/GeoJSON endpoint), poll ~5–10 min, cache with age tracking.
- Path-MUF evaluation per §7.2; gate wired into the blend multiplicatively.
- **AC:** above-MUF spots suppressed; open paths pass; gate/MUF/age in the breakdown; with no
  empirical data the service still emits a gated score at low confidence; KC2G parsing fixtured.

### Phase 3 — Layer 3 (model) — SCAFFOLDED; continue on a Linux host

The **seam is in place** and abstains until built — scoring runs on Layers 1+2 with no change.
What exists in the repo today:

- **`cmd/horstprop/internal/model`** — the `Provider` interface (`Predict(Request) (Prediction, bool)`),
  `Request{Home, DX, Band, FreqMHz, UTCHour, Month}`, `Prediction{Score, Confidence, Detail}`, and
  `Disabled{}` (always abstains, the current default).
- **`engine.modelPredict`** already calls the provider and folds the result into the blend
  (`engine.go`): when L1 is absent the model is the fallback **base**; when both L1 and L3 are
  present, agreement (|ΔScore| ≤ 15) raises confidence and disagreement lowers it (§7.4).
- **Config/flag** `-model-enable` / `HORSTPROP_MODEL_ENABLE` and `config.ModelEnable` exist;
  `main.go` logs and keeps `Disabled{}` until a real provider is wired.

**To finish Phase 3 on the Linux shack box:**

1. **Install the engine** (do NOT call VOACAP Online): build **ITURHFProp** (preferred, ITU-R
   P.533-14, `github.com/ITU-R-Study-Group-3/ITU-R-HF`) or `voacapl` (NTIA Linux port). Add it to the
   Dockerfile/host image. Add config for the binary path + working/data dirs.
2. **Antenna models** for the station: Ultrabeam 3-el beam (azimuth-dependent gain), 40/80 m
   fan-dipole, K9AY (RX-only, low-band receive). Start with simple gain models; refine with antenna
   files later. Put these behind config so they aren't hard-coded.
3. **Precompute the prediction grid** keyed by `(band, distance_bucket, azimuth_bucket, utc_hour,
   month, SSN)` for the current month; cache to disk; scheduled refresh (monthly, or on material SSN
   shift). **Never run the engine per spot** (§7.3 / §9) — `Predict` must be an O(1) cache lookup.
4. **Solar-index ingester** (§6): HamQSL XML and/or NOAA SWPC `wwv.txt`; current SFI/SSN scales the
   monthly-median model and nudges confidence.
5. **Implement `model.Provider`** in a new package (e.g. `internal/model/iturhf`) that loads the grid
   and returns a `Prediction`. Wire it in `main.go`: replace `var mdl model.Provider = model.Disabled{}`
   with the real provider when `cfg.ModelEnable`. Bump the health `scorer` label to `phase3-model`.
6. **Tests**: fixture-based grid lookups; the blend already has fake-provider coverage
   (`engine/model_test.go`) — extend with agree/disagree confidence cases.

- **AC:** every spot scored even when empirical is silent or the gate is closed; model reads are
  O(1) from cache; refresh documented; **no calls to VOACAP Online**.

### Phase 4 — Surfacing ✅ (calibration/push still optional)
- **Done:** the scoring API is browser/curl-friendly and fully introspectable.
  - `POST /v1/score` (Spot JSON body, §5.1) and `GET /v1/score?dx_call=&freq_hz=&mode=&grid=` →
    a Score (§5.5).
  - `GET /v1/debug?dx_call=&freq_hz=&grid=` → `{score, diagnostics}`, where `diagnostics` exposes the
    resolved geometry, the sampled **control points** with their interpolated MUF + age, the
    `store_match_count` / `store_reports_total`, and `muf_fresh_stations`.
  - `GET /v1/health` → service/scorer/home + `feed_enabled`, `reports_stored`, `muf_fresh_stations`.
- **Still optional/deferred:** calibrate SNR→score curves and layer weights against logged QSO
  history (Log4OM); a push/subscribe channel for the cluster client.

## API surface (current)

| Endpoint | Method | Purpose |
|---|---|---|
| `/v1/score` | POST (Spot JSON) / GET (query) | score one spot → Score (§5.5) |
| `/v1/debug` | GET | Score + per-layer + control-point diagnostics |
| `/v1/health` | GET | liveness + store/MUF/feed status |

## Chase Queue integration (frontend)

The DX Cluster "Chase Queue" (`docs/dxcluster-*`) consumes horstprop's `grade` for the score meter.
Mapping to the existing visual vocabulary: **A/B → "go" (green)**, **C → "watch" (amber)**,
**D → "wait" (slate)**, **"?" → muted/unknown**. Show the numeric `score` alongside, and surface
`reason` on hover. The browser calls `GET /v1/score` per visible spot; horstprop serves from cache
(no hot-path network) and sends `Access-Control-Allow-Origin: *` so a browser page can read it.

**Wired in the prototype** (`tmp/dx-cluster-proto/index.html`): each card fetches its live grade +
score from `:9970`, recolours the meter, shows a grade badge, re-renders the hero dial, and a status
pill reports the live/offline state (mock values are kept when horstprop is down). To run the demo:
`go run . -dev -port 8080 -dx-postgres-fail-fast=false` and `go run ./cmd/horstprop -horst-url
http://127.0.0.1:8080 -area-rings 12`, then open the prototype.

---

## 9. Non-functional requirements

- **Rate-limit / good citizen:** RBN/PSKReporter throttling is HorstReporter's job. For sources
  this service touches directly: poll KC2G no faster than its ~5 min refresh; never scrape VOACAP
  Online. Enforce in the source clients.
- **Caching:** key by `(band, path-bucket, utc-hour)`; per-spot scoring must be O(1) lookups, no
  synchronous network calls in the hot path.
- **Persistence:** in-memory primary; optional SQLite so the L1 store / caches survive a restart.
- **Resilience:** each source independent; if one is down its layer reports `available: false`
  and scoring continues. No source failure may crash the service.
- **Config & secrets:** file + env. No secrets in the repo; station callsign + contact email in
  env/`.env` (gitignored). Don't log full feeds at info level.
- **Observability:** structured logs, per-layer timing, a debug score endpoint, counters (reports
  in, spots scored, per-layer availability, source reconnects).
- **Testing:** deterministic, fixture-based; **no network in unit tests**; a `--dry-run` mode
  replaying captured feeds. Cover geometry, band mapping, SNR normalization, and the blend
  including all layer-absent permutations.
- **Packaging:** container image + systemd unit; runs on the Linux shack box.

---

## 10. Do not

- Do **not** add this to HorstReporter or modify it beyond read-only consumption.
- Do **not** use HorstReporter's DX-cluster spots — only its reception-report feed.
- Do **not** connect directly to RBN or PSKReporter — consume HorstReporter's feed.
- Do **not** scrape or automate VOACAP Online — self-host the engine (Phase 3).
- Do **not** treat empirical silence as a closed band — Layer 1 abstains when it has no data.
- Do **not** run the propagation engine per spot — precompute and cache.
- Do **not** hardcode secrets or commit `.env`.

---

## 11. Start here

Begin with **Phase 0**. §3 discovery is resolved above. Next: realign `internal/propcontract`
and the `cmd/horstprop` stub to the §5 contract (score + grade + layers, *not* the earlier
`go/watch/wait` sketch), define the `PropFeedSource` interface over `/api/stream`, the scoring-API
contract (§5.1/§5.5), and the config schema. Confirm the two open reconciliation points in §0 with
the operator before writing feature code.
