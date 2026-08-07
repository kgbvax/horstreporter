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

- **QTH resolution**: endpoints taking a station context accept
  `qth` | `callsign` | `locator` query params (checked in that order).
  Values are uppercased. A callsign needs a locator enrichment to resolve to
  coordinates. Where required, a missing/unresolvable qth → `400`.
  (The QTH is the operator's own station, the point-of-view for all analysis;
  the older `target` param name is no longer accepted.)
- **`minutes`**: window size in minutes; invalid or ≤ 0 resets to the
  default; capped at a per-endpoint maximum.
- **`surroundings`**: `"true"` expands a locator qth to the 3×3 block of
  grid squares around it.

## Core endpoints

### `GET /api/stream` — live spot stream (SSE)

Server-sent events: initial history dump, then live spots.

Params: `qth` (required), `minutes` (default 15, max 60), `surroundings`,
`rings` (int, 0..30 — area-of-interest: any sender/receiver within N grid
squares of a locator qth; 0 disables; used by horstprop's region feed).

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

Params: `qth` (required), `snapshot_at` (unix seconds, default now; bad
value → 400), `minutes` (default 15, max 720), `surroundings`,
`min_snr_mode` (`"ssb"`|`"cw"`), `ssb_min_db` (default 0), `cw_min_db`
(default -15), `selected_band`, `enabled_bands` (CSV), `include_dxcluster`
(default true), `include_rbn` (default true).

Response: `{qth, surroundings, snapshot_at, window_minutes, generated_at,
count, spots: [<stream spot shape>]}`.

### `GET /api/dx_conditions` — DX baseline scoring per band

Params: `qth` (required), `minutes` (default 20, max 180), `cw_min_db`
(default -15), `surroundings`.

Response: top-level `overall_score`, `confidence`, `status`, `condition`,
`trend`, `operator_region` (the inferred DXPulse region used for the regional
baseline tier; `""` when unresolvable), plus recommendation lists `best_bands`,
`recommended_bands`, `worst_bands`, `avoid_bands`; and `bands[]`, one object
per band with ~38 fields — activity (`current_links`, `unique_links`,
`repeat_ratio`, `spots_per_minute`, unique station/grid counts), geometry
(`avg/median/max/p90_distance_km`, `long_haul_ratio`, `dx_ratio`), signal
(`avg_snr`, `median_snr`, `peak_snr`, `p90_snr`), baseline comparison
(`baseline_activity`, `qth_baseline_used`, `regional_baseline_used`,
`baseline_activity_by_slot`, `baseline_slot_used_by_qth`,
`baseline_slot_used_by_region`), `dominant_direction`, `azimuth_sectors`,
`region_counts`, trend + `sparkline`, `activity_by_bin`.

Baseline tier selection is per-band and per-slot: a band/slot with
qth-specific history uses it (`qth_baseline_used: true`); otherwise it
falls back to the operator's regional baseline
(`regional_baseline_used: true`); otherwise to the global baseline (both
false). The regional baseline is always-on when Postgres is available and is
backfilled from `dx_raw_spots` on first startup after the table is created.

Degrades to an empty "Poor" skeleton when the Postgres baseline is
unavailable. No caching; window data from the in-memory rolling history.

### `GET /api/hot_bands` — unusual-activity band alerts

Params: `qth` (required), `surroundings`, `minutes` (default 20,
max 180), `cw_min_db`, `current_band`.

Response: `{…, recommendations: [{band, kind ("surprise"|"dx_surge"|"rising"),
priority ("high"|"normal"), reason, rank_score, spots_per_minute,
baseline_activity, activity_ratio, sustained_bins, p90_distance_km,
baseline_p90_distance_km?, distance_ratio?, trend, trend_delta, status}]}`.

### `GET /api/square_details` — one grid square's reports

Params: `locator` (required, valid Maidenhead or 400), plus the same context
filters as capture (`qth`, `surroundings`, `minutes` default 15 max 60,
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

## Removed features

**Propagation Lab** (`/api/proplab/v1/params|ladder|fusion|reachability`,
`/proplab/` UI) and **DXPulse** (`/api/dxpulse/v1/matrix|summary`,
`/dxpulse/` UI) were removed 2026-08-04. What remains is the data
plumbing their sibling consumer needs:

- `proplab_cell_buckets` is still written by the in-process cell bucket
  feed (midpoint cell × band × lane, 15-min buckets) — read by
  **pathscope**. The `-proplab-cell-retention-days` flag now governs this
  feed's tables.
- `proplab_sw_series` is still written by `-proplab-sw-enable` — also
  read by pathscope (kp / F10.7 / x-ray / OVATION series).
- The now-dormant tables (`proplab_dest_buckets`, `proplab_drap_snapshots`,
  `proplab_events`) are no longer written nor pruned by the service.

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
  Params: `qth` (locator; omit = global), `surroundings`, `mode`
  (`"typical"` default | `"recent_24h"` | `"compare"`). Per-qth results
  cached 5 min; global 60 s; prewarmed at startup.
- `GET /dxlens/api/v1/region_calendar` — region × slot matrix. Params:
  `band`, `qth`?, `surroundings`. `source` is `"stats"` (30-day
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
- **No `/api/proplab/*`** and **no `/api/dxpulse/*`** — both features were
  removed (see "Removed features"); only the pathscope feed tables remain.
