# HorstReporter HTTP API

Canonical reference for all HTTP endpoints served by the main `horstreporter`
binary (`main.go`, one mux, one listener). Authority for request/response
shapes is this document; when it disagrees with the code, the code is the bug
or this doc is stale — check `git log docs/api.md` last.

Related contracts documented elsewhere:

- `docs/dxcluster-agent-api.md` — local operator agent at `http://127.0.0.1:9955/v1/*`
  (browser-direct; the main binary NEVER proxies it, except the read-only
  `/api/opmode/status`).
- `docs/horstprop.md` — the horstprop scoring service (`/horstprop/*` here is
  only a reverse proxy to it).
- `docs/horstawards.md` — award progress; entirely agent-side, no server API.

## Transport

- **Port**: `-port` (default 8080). **TLS**: `-domain` → Let's Encrypt
  autocert (HostWhitelist, cache in `certs/`); `-cert` + `-key` → static
  ListenAndServeTLS; otherwise plain HTTP.
- **Static UI**: `GET /*` fallback. `-dev` serves `static/` from disk with
  `Cache-Control: no-cache, no-store, must-revalidate`; production serves the
  embedded `//go:embed static` with SHA-256 ETags + `Cache-Control: no-cache`
  (revalidate → 304).
- **pprof**: `-pprof` flag starts a separate listener on `localhost:6060` —
  not part of this API.
- JSON responses are pretty-indented bodies with `Content-Type:
  application/json` (proplab endpoints add `Cache-Control: no-store`).
- There is **no auth** on any endpoint; production is a public read-only
  service. There is **no health/readiness endpoint** — `GET /api/stats` is
  the closest.

## Conventions

- **Target resolution**: endpoints taking a station context accept
  `target` | `callsign` | `locator` query params (checked in that order).
  Values are uppercased. A callsign needs a locator enrichment to resolve to
  coordinates. Where required, a missing/unresolvable target → `400`.
- **`minutes`**: window size in minutes; invalid or ≤ 0 resets to the
  default; capped at a per-endpoint maximum.
- **`surroundings`**: `"true"` expands a locator target to the 3×3 block of
  grid squares around it.

## Core endpoints

### `GET /api/stream` — live spot stream (SSE)

Server-sent events: initial history dump, then live spots.

Params: `target` (required), `minutes` (default 15, max 60), `surroundings`,
`rings` (int, 0..30 — area-of-interest: any sender/receiver within N grid
squares of a locator target; 0 disables; used by horstprop's region feed).

Response `text/event-stream`, CORS `*`. Frames:

```
data: {"lat":…,"lng":…,"snr":…,"ageSeconds":…,"locator":"JO62qm", …}
…one per historical spot…
event: history_end
data: {}
…then live frames in the same shape…
```

Spot fields: `lat`, `lng` float; `snr` int; `ageSeconds`; `locator`;
`reporterLocator` (omitempty); `sourceType` (`""`|`"dxcluster"`|`"rbn"`);
`band`; `sender`, `receiver` (only for DX-cluster spots).

Overflow beyond `-max-clients` (default 150) → an `event: server_error`
frame, not an HTTP error. With `-compress` and `Accept-Encoding: gzip` the
stream is gzip-encoded, flushed per event.

### `GET /api/capture_snapshot` — deterministic spot snapshot

Same window semantics as `/api/stream` but a single JSON response, sorted
deterministically (age, locator, band, snr desc, reporter locator). Used for
server-driven frame capture.

Params: `target` (required), `snapshot_at` (unix seconds, default now; bad
value → 400), `minutes` (default 15, max 720), `surroundings`,
`min_snr_mode` (`"ssb"`|`"cw"`), `ssb_min_db` (default 0), `cw_min_db`
(default -15), `selected_band`, `enabled_bands` (CSV), `include_dxcluster`
(default true), `include_rbn` (default true).

Response: `{target, surroundings, snapshot_at, window_minutes, generated_at,
count, spots: [<stream spot shape>]}`.

### `GET /api/dx_conditions` — DX baseline scoring per band

Params: `target` (required), `minutes` (default 20, max 180), `cw_min_db`
(default -15), `surroundings`.

Response: top-level `overall_score`, `confidence`, `status`, `condition`,
`trend`, plus recommendation lists `best_bands`, `recommended_bands`,
`worst_bands`, `avoid_bands`; and `bands[]`, one object per band with
~35 fields — activity (`current_links`, `unique_links`, `repeat_ratio`,
`spots_per_minute`, unique station/grid counts), geometry (`avg/median/
max/p90_distance_km`, `long_haul_ratio`, `dx_ratio`), signal (`avg_snr`,
`median_snr`, `peak_snr`, `p90_snr`), baseline comparison
(`baseline_activity`, `target_baseline_used`, `baseline_activity_by_slot`,
`baseline_slot_used_by_target`), `dominant_direction`, `azimuth_sectors`,
`region_counts`, trend + `sparkline`, `activity_by_bin`.

Degrades to an empty "Poor" skeleton when the Postgres baseline is
unavailable. No caching; window data from the in-memory rolling history.

### `GET /api/hot_bands` — unusual-activity band alerts

Params: `target` (required), `surroundings`, `minutes` (default 20,
max 180), `cw_min_db`, `current_band`.

Response: `{…, recommendations: [{band, kind ("surprise"|"dx_surge"|"rising"),
priority ("high"|"normal"), reason, rank_score, spots_per_minute,
baseline_activity, activity_ratio, sustained_bins, p90_distance_km,
baseline_p90_distance_km?, distance_ratio?, trend, trend_delta, status}]}`.

### `GET /api/dxpulse/v1/matrix` — band × region matrix

Params: `target`|`locator` (required, valid locator), `surroundings`,
`mode` (`"quality"` default | `"anomaly"`), `minutes` (default 15, max 60),
`lookback_days` (anomaly only; default 45, max 90).

Response: `bands[]`, `regions[]`, `matrix[][]` of cells `{band, region,
state, label, color_bucket, current_spot_count, current_unique_paths,
current_unique_remote_grids, avg_snr?, median_snr?,
last_seen_age_seconds?, baseline_*`, `confidence`}`. Anomaly mode adds
`baseline_expected_spot_count`, `baseline_ratio`, `baseline_support`,
`baseline_support_days` and reads the Postgres baseline.

### `GET /api/dxpulse/v1/summary` — condensed matrix view

Same params. Response: `best_bands[{band, state, label,
current_spot_count, confidence}]`, `top_regions[{region, total_spot_count,
active_bands, best_band?, best_band_state?, best_band_strength}]` (max 5),
`hot_cells[{band, region, state, label, current_spot_count, confidence,
strength}]`.

### `GET /api/square_details` — one grid square's reports

Params: `locator` (required, valid Maidenhead or 400), plus the same context
filters as capture (`target`, `surroundings`, `minutes` default 15 max 60,
SNR modes/floors, `selected_band`, `enabled_bands`).

Response: `{locator, count, min_snr, max_snr, avg_snr, best_band,
band_counts{band:n}, top_reports[{sender, receiver, band, snr}]}` — top 10
by SNR, deduped per sender|receiver|band.

### `GET /api/dxspots` — DX-cluster spots

Params: `minutes` (default 15, max 60). CORS `*`.

Response: JSON array `{dx_call, spotter, freq_khz, band, dx_locator?,
spotter_locator?, age_seconds, comment?, op_name?, country?,
country_iso?}` — deduped to latest per (DX call, band), newest first; spots
with freq ≤ 0 skipped. Requires DX cluster ingest to be enabled.

### `GET /api/stats` — server counters

No params. Active connections, hub history size/minutes/KB, session totals
and byte accounting, DX baseline event counts, DX-cluster ingest counters.
The de-facto health endpoint.

### `GET /api/opmode/status` — operator-mode wiring info

No params. `{enabled: true, configured: false, control_enabled, mode:
"direct", proxy_active: false, direct_capable: true, agent_reachable:
false, error: "backend proxy disabled by design; browser must connect to
local agent directly"}`. There is deliberately no opmode proxy — the
browser calls the local agent itself.

## Propagation Lab (`/api/proplab/v1/*`)

All GET (405 otherwise). 503 `Propagation Lab disabled` with
`-proplab-disable` (except `/params`). Verdicts are cached 5 s keyed on the
request. JSON `Cache-Control: no-store`.

### `GET /api/proplab/v1/params`

No params. `{b: <LadderParams>, c: <FusionParams>}` — the server's current
defaults in their JSON forms (`min_links`, …, `cusum_min_bucket_links`,
`expected_lookback_days`, … for B; `lookback_days`, `quantile_lo`,
`guardband_sigma`, `open_ratio`, … for C). The A/B/C lab page uses these to
seed its parameter forms.

### `GET /api/proplab/v1/ladder` — variant B verdict (midpoint MUF ladder)

Params: `target` + `surroundings` (empty target is legal), plus **any
LadderParams field as a query override** (no bounds checking; parse
failures fall back to server defaults).

Response `LadderVerdict`: `{generated_at, params, bands: [{band, state
("open"|"rising"|"activity_spike"|"closed"|"unconfirmed"), reason,
confidence, spots_per_minute, links_per_minute, muf_cells[], es_cells[],
onset_min_ago (-1 = none), onset_region?, forecast_hints[]}], open_runs
[][string], empirical_muf (0 = none), data_thin}`.

Onset semantics: `onset_min_ago` ≥ 0 only while a CUSUM threshold crossing
is fresh (≤ 4 h) and ≥ 2 distinct band cells are alarmed; the detector
compares against the slot-conditioned expected-activity baseline.

### `GET /api/proplab/v1/fusion` — variant C verdict (quantile + SW fusion)

Params: **any FusionParams field as query override**. Target is not used —
fusion is global.

Response `FusionVerdict`: `{generated_at, params, bands: [{band, region,
state, label, reason, confidence, spots_per_minute, links_per_minute,
baseline_p50, activity_ratio, closure_type
("muf_limited"|"absorption_limited"|"auroral"|""), explained_by[]}],
data_thin, sw_available, has_drap, drap_age_min, drap_haf{region:float},
events_active}`.

`explained_by` lists anomaly events explaining elevated activity — contest /
DXpedition calendar entries only; routine POTA activator spots are
deliberately excluded (they are baseline activity, not events).

### `GET /api/proplab/v1/reachability` — product view (reachability index)

The operator-facing payload: scalar reachability per (band × DX-destination
region), surges, usual-opening schedule, empirical MUF headroom. **No param
overrides by design** — the index must mean the same thing for everyone.

Params: `target` + `surroundings` only. **Empty target is valid** → global
view (no scope filter); the UI labels the input "QTH".

Response `ReachVerdict`:

- `qth`, `surroundings`, `generated_at`
- `index_scale` — disclaimer string ("heuristic 0-100; not a calibrated
  probability")
- flags: `data_thin`, `schedule_unavailable`, `sw_available`, `has_drap`,
  `drap_age_min`, `events_active`
- `muf` — path-midpoint physics from the ladder: `empirical_mhz`,
  `open_runs`, `next_rung_band`, `next_rung_mhz`, `next_rung_open`
- `cells[]` — one per live (band, dx-region) pair: `index` (0-100; **null**
  when witnesses == 0 — "no data" is distinct from "observed dead"),
  `links_per_min`, `baseline_p50`, `activity_ratio`, `witnesses`,
  `persistence` (0-1, active buckets / 2 h), `closure_cause`, `capped`,
  `cell_data_thin`, `spots_per_min`, `explained_by[]`
- `surges[]` — `{kind ("onset"|"activity_jump"|"new_region"), band, region,
  first_seen, strength (0-100), detail}`; carried 4 h, suppressed on first
  tick
- `schedule[]` — usual openings for pairs NOT currently reachable: `{band,
  region, open_utc, close_utc, minutes_to_open, presence}` sorted by
  `minutes_to_open`. `presence` = fraction of the pair's distinct active
  days the slot was open (a slot must open on ≥ 3 distinct days to appear
  at all).

Index semantics (calibration-verified 2026-08-03, holdout-tested): raw
activity = `min(score(live/baseline ratio), score(absolute live lpm))`
anchored r/lpm 0.5 → 0, 1.0 → 15, 2.0 → 50, 8.0 → 83, ≥ 16 → 100; ×
witness (1 → 0.5, 2 → 0.75, ≥ 3 → 1.0) × persistence (0.8+0.2p) × 0.7 when
no baseline; hard caps from typed closure causes (absorption ≤ 15, auroral
≤ 25, MUF-limited ≤ 30, contest/DXpedition-explained ≤ 20).

## Reverse proxies

These paths forward to sibling binaries; the main binary only proxies
(upstream failure → 502).

- **`/horstprop/*`** → `-horstprop-url` (default `http://127.0.0.1:9970`),
  prefix stripped before forwarding (`/horstprop/v1/score` →
  `127.0.0.1:9970/v1/score`). Contract: `docs/horstprop.md`.
- **`/pathscope/*`** → `-pathscope-url` (default `http://127.0.0.1:9960`),
  path NOT stripped (upstream expects it); SSE-friendly flush. Contract:
  the pathscope module.

## DXLens module (`/dxlens/*`)

Mounted only when the Postgres DX baseline is available. The embedded
`dxlens` sibling module serves its own mux under the prefix; JSON responses
`Cache-Control: no-store`, 503 `{"error":"no snapshot available yet"}` until
the first snapshot builds.

- `GET /dxlens/api/v1/meta` — snapshot metadata (`version`, event/bucket
  counts, coverage flags).
- `GET /dxlens/api/v1/heatmap` — band × slot-of-day activity heatmap.
  Params: `target` (locator; omit = global), `surroundings`, `mode`
  (`"typical"` default | `"recent_24h"` | `"compare"`). Per-target results
  cached 5 min; global 60 s; prewarmed at startup.
- `GET /dxlens/api/v1/region_calendar` — region × slot matrix. Params:
  `band`, `target`?, `surroundings`. `source` is `"stats"` (30-day
  statistics: p25/p50/p75/…) or `"events"`.
- `GET /dxlens/api/v1/now` — what's hot in a 30-min slot. Params: `slot`
  (0-47, default current) or legacy `hour` (0-23).
- `POST /dxlens/api/v1/refresh` — embedded provider cannot refresh →
  `{reloaded: false, reason: "provider does not support refresh"}`
  (meaningful only in the standalone dxlens binary).
- `GET /dxlens/*` — the DXLens web UI (static).

## Explicitly NOT here

- **No opmode proxy/control** (`/api/opmode/*` beyond `/status`) — direct
  by design; browser → local agent.
- **No awards API** — operator award progress is computed inside the local
  agent (`internal/awards`); never pulled server-side ("wanted" reaches the
  Chase Queue through the agent's enrich response).
- **No per-spot/path link scoring** — that is horstprop's contract,
  consumed via `/horstprop/v1/score`.
- **No `/api/proplab/v1/dest`** or other proplab internals — the store
  tables back `/reachability` only.
