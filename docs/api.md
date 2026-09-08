# HorstReporter HTTP API

Canonical reference for all HTTP endpoints served by the main `horstreporter`
binary (`main.go`, one mux, one listener). Authority for request/response
shapes is this document; when it disagrees with the code, the code is the bug
or this doc is stale — check `git log docs/api.md` last.

Related contracts documented elsewhere:

- `docs/dxcluster-agent-api.md` — local operator agent at `http://127.0.0.1:9955/v1/*`
  (browser-direct; the main binary NEVER proxies it).
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
`reporterLocator` (omitempty); `sourceType`
(`""`|`"dxcluster"`|`"rbn"`|`"wspr"`); `band`; `sender`, `receiver` (only
for DX-cluster spots).

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
`trend`, `qth_cluster` (the inferred DXPulse region used for the regional
baseline tier; `""` when unresolvable), plus recommendation lists `best_bands`,
`recommended_bands`, `worst_bands`, `avoid_bands`; and `bands[]`, one object
per band with ~38 fields — activity (`current_links`, `unique_links`,
`repeat_ratio`, `spots_per_minute`, unique station/grid counts), geometry
(`avg/median/max/p90_distance_km`, `long_haul_ratio`, `dx_ratio`), signal
(`avg_snr`, `median_snr`, `peak_snr`, `p90_snr`), baseline comparison
(`baseline_activity`, `cluster_baseline_used`, ``,
`baseline_activity_by_slot`, `baseline_slot_used_by_cluster`,
``), `dominant_direction`, `azimuth_sectors`,
`region_counts`, trend + `sparkline`, `activity_by_bin`.

Baseline tier selection is per-band and per-slot: a band/slot with
qth-specific history uses it (`cluster_baseline_used: true`); otherwise it
falls back to the operator.s grid-cluster baseline
(`: true`); otherwise to the global baseline (both
false). The cluster baseline is always-on when Postgres is available and is
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

### `GET /api/prop_intel` — propagation intelligence nowcast (v1, frozen)

> **Deprecated — frozen for horstapp compatibility; superseded by
> `GET /api/prop_intel/v2` (below).** v1 stays live until the mobile app
> migrates; do not extend it.

Per-(band × region) WSPR nowcast of SSB/CW openness, rising slope, and
atypical-surge detection against the WSPR climatology
(`wspr_region_baseline_daily`). WSPR spots are the only source; cells are
emitted only for (band × region) pairs with live spots in the window —
clients fill the full `band_order` × `regions` canvas themselves. Reuses the
dxPulse 11-region classifier for the region axis. Stateless beyond the
`dxBaseline` singleton (FT8 cross-reference), the `wsprClimatology`
singleton, and the `hub.history` ring; every request re-derives its cells
from a snapshotted history window. See `prop_intel.go`.

Params: `qth` (required), `surroundings`, `minutes` (default 15,
max 180), `cw_min_db` (default -15), `surge_threshold` (float, default
2.0 — z-score above which a cell is flagged atypical; invalid or <= 0
values are silently reset to the default), `from_here` (`true` filters
cells to those where the operator's QTH is one end of any path).

Response:
```
{
  "qth": "JO32",
  "minutes": 15,
  "now": 1734567890,
  "from_here": false,
  "bands": ["20m", "10m"],
  "band_order": ["160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m",
                 "12m", "10m", "6m", "4m", "2m"],
  "regions": ["EU", "NA", "SA", "AF", "AS", "JA", "OC", "VK", "KH6", "CAR", "AN"],
  "region_names": {"EU": "Europe", "NA": "North America", ...},
  "cells": [
    {
      "band": "20m",
      "region": "CAR",
      "ssb_open": true,
      "cw_open": true,
      "rising": false,
      "atypical": {
        "z_score": 3.2,
        "confidence": 0.71,
        "flavor": "atypical-both",
        "ft8_cross_ref": "available"
      },
      "from_here": false,
      "sources": ["wspr"],
      "spot_count": 42
    }
  ]
}
```

- `bands` lists only the bands with live spots in the window;
  `band_order` is the full canonical 13-band list (160m…2m) so clients can
  render the full matrix canvas.
- `regions` always lists all 11 region codes; `region_names` maps them to
  display names.
- `ssb_open` / `cw_open`: the best path in the cell has enough budget for a
  100W SSB (+10 dB) / CW (`cw_min_db`, default -5 dB floor) signal:
  `effective_snr = wspr_snr + (50 dBm − tx_power_dbm)`. Cells whose spots
  carry no TX power exist but never flag SSB/CW open.
- `rising`: second-half vs first-half spot counts of the window; ratio ≥ 1.5
  (or spots only in the second half).
- `atypical` is present only when the live rate (extrapolated to the
  30-min slot) z-scores at least `surge_threshold` above the WSPR
  climatology mean for the same (band, region, slot), with ≥ 3 sample days
  and nonzero stddev. `confidence` scales 0.3→1.0 with climatology depth.
  `flavor` cross-references the FT8 climatology
  (`dx_region_baseline_daily`): `atypical-both` (FT8 also hot),
  `atypical-wspr-silent-ft8` (FT8 quiet), `atypical-wspr-only` otherwise;
  `ft8_cross_ref` notes whether that comparison was `available`.
- When at least one cell is atypical, the engine fans out Web Push
  notifications to matching subscriptions asynchronously (see
  `/api/push/*`); the push send never blocks this response.
- Counters are surfaced in `/api/stats` under `prop_intel.requests`,
  `prop_intel.errors`, `prop_intel.surges_detected`.

### `GET /api/prop_intel/summary` — compact widget payload (v1, frozen)

> **Deprecated** together with v1 — see `/api/prop_intel/v2` below. Frozen for
> horstapp's iOS/Android widgets.

Same engine and query parameters as `/api/prop_intel`, reduced to the
precomputed glance used by the mobile app's iOS/Android home-screen widgets
(the widget extension fetches this directly when the data shared by the
app is stale). The response is cached for 60s (single entry, keyed on the
raw query) and served with `Cache-Control: max-age=60`.

Response:
```
{
  "now": 1734567890,
  "qth": "JO32",
  "minutes": 15,
  "from_here": false,
  "headline": "20m atypical surge to Caribbean (z=3.2)",
  "headline_kind": "atypical",
  "top_bands": [
    {"band": "20m", "regions": ["EU", "NA"], "ssb": true, "cw": true,
     "rising": false, "spots": 37}
  ],
  "grid": [{"b": "20m", "r": "NA", "i": 0.62, "f": 7}]
}
```

- `headline` / `headline_kind`: the single most newsworthy cell — atypical
  (highest confidence, ties on z-score) beats rising (open paths
  preferred) beats a quiet band count. `headline_kind` is `atypical`,
  `rising`, or `quiet`.
- `top_bands`: up to 4 bands, busiest first, with rolled-up mode/rising
  flags and the live regions busiest-first.
- `grid`: one entry per live cell; `i` is the spot count relative to the
  busiest cell (0–1, 2 decimals — the chip-ramp input), `f` is a flag
  bitmask: ssb=1, cw=2, rising=4, atypical=8, from_here=16.

### `GET /api/prop_intel/v2` — unified multi-source propagation nowcast

The consolidated successor of v1: a per-(band × region) nowcast across ALL
four ingest sources, with per-source evidence plus a combined rollup. The web
panel (wspr-matrix.js) is the primary consumer; horstapp migrates here when
it adopts multi-source rendering. See `prop_intel_v2.go`,
`prop_intel_sources.go` (per-source profiles), and `prop_baseline.go`
(unified climatology, `prop_region_baseline_daily`).

Params: v1's (`qth` required, `surroundings`, `minutes`, `surge_threshold`,
`from_here`) plus:

- `ssb_min_db` / `cw_min_db`: global Min SNR overrides (the UI's Min SNR
  control, sent by the web panel). When present they REPLACE the per-source
  profile floors for the SNR-floored sources (`pskr`, `rbn` CW) with the
  given values, clamped ssb −10..30 / cw −40..20. Absent (and for `wspr`'s
  budget model and `dxcluster`'s presence rule) the profile floors apply.

- `sources`: source selection, CSV or repeated (`?sources=wspr,pskr` or
  `?sources=wspr&sources=rbn`). Public names: `wspr` (WSPR beacons),
  `pskr` (PSKReporter FT8/FT4), `rbn` (RBN CW/RTTY skimmers),
  `dxcluster` (DX cluster spots). Unknown names are dropped; absent/empty
  selects all four.

Response: v1's envelope plus `sources_requested`; `cells[]` per (band ×
region):
```
{
  "band": "20m", "region": "NA", "from_here": true, "spot_count": 42,
  "open": true, "ssb_open": true, "cw_open": true, "rising": false,
  "atypical": {"z_score": 3.2, "confidence": 0.71},
  "active_sources": ["wspr", "pskr"],
  "open_agreement": 1.0,
  "atypical_agreement": 0.5,        // omitted when no source is atypical
  "sources": [
    {"source": "wspr", "spot_count": 30, "open": true,
     "ssb_open": true, "cw_open": true, "open_basis": "budget",
     "rising": false, "atypical": {...}, "sample_days": 12},
    {"source": "pskr", "spot_count": 12, "open": true,
     "ssb_open": true, "cw_open": true, "open_basis": "snr_floor",
     "unknown_power": true, "rising": false, "sample_days": 9}
  ]
}
```

- Per-source open semantics are honest about the mechanism (`open_basis`):
  - `wspr` — unchanged v1 budget model (`budget`).
  - `pskr` — report-SNR floors: digital ≥ −24 dB, CW ≥ −18, SSB ≥ −5. TX
    power is unknown, so these are estimates (`unknown_power: true`).
  - `rbn` — CW skimmer only: CW open ≥ +8 dB; `ssb_open` is ABSENT (nil),
    not false — a CW skimmer can never prove SSB.
  - `ssb_min_db`/`cw_min_db` override these floors when the global Min SNR
    control is set (see params).
  - `dxcluster` — no amplitude: `open` = ≥ 2 spots in the window (a single
    spot may be a busted callsign); no mode flags (`presence`).
- `atypical` per source z-scores the live rate against THAT source's
  climatology (≥ 3 sample days, nonzero stddev, same cold-start confidence
  discount as v1). The rollup cell carries the highest-confidence atypical;
  `atypical_agreement` is the fraction of active sources that surged
  (replaces v1's FT8 flavor cross-reference).
- `open_agreement` is vacuously 1.0 with a single active source — render
  the `active_sources` count, not the fraction.
- Atypical cells fan out Web Push like v1 (adapted to the v1 push payload;
  push labels are unchanged).

### `GET /api/push/vapid-public-key` — Web Push public key

Returns the server's VAPID public key (base64url) for the browser
subscription flow. The private key is NEVER exposed here. Returns 503
when push is not configured (`-push-enable` false or VAPID keys
missing).

No params.

Response: `{"public_key": "<base64url>"}`.

### `POST /api/push/subscribe` — register a Web Push subscription

Stores (or updates) a browser Push API subscription. Validates the
subscription shape (HTTPS endpoint + `p256dh` + `auth` keys) before
storing. Per-client-IP rate limiting (`pushSubscribeRatePerHour`/hour =
10/h) mitigates abuse from unauthenticated clients. A re-POST of an
existing endpoint updates the QTH/preferences in place (idempotent
re-subscription — the plan's restart-recovery requirement). When the
store is at capacity (`pushMaxSubscriptions` = 1000), the oldest
subscription is evicted (FIFO) before the new one is added.

Request body:
```
{
  "endpoint": "https://fcm.googleapis.com/fcm/abc",
  "keys": { "auth": "<base64url>", "p256dh": "<base64url>" },
  "qth": "JO32",
  "preferences": { "all": true }
}
```

`preferences` is a map of `"band:region"` → bool (e.g.
`"10m:CAR": true`) or the special key `"all"` for every surge. An empty
map with no `"all"` entry means no pushes (subscription stored but
inert).

Response: `{"ok": true, "endpoint": "...", "registered": true}`.

Errors: 405 (non-POST), 503 (push not configured), 429 (rate limit),
400 (invalid JSON / validation failure).

### `POST /api/push/unsubscribe` — remove a Web Push subscription

Removes a subscription by endpoint. No-op when the endpoint was never
stored (the browser may unsubscribe after a server restart that already
lost the record). Accepts the request even when push is disabled so the
browser can clean up its side without a 503.

Request body: `{"endpoint": "https://fcm.googleapis.com/fcm/abc"}`.

Response: `{"ok": true}`.

Errors: 405 (non-POST), 400 (invalid JSON).

### `GET /api/push/subscription-status` — check subscription state

Checks whether a subscription endpoint is registered server-side. Used by the
frontend re-subscription-after-restart flow: a `registered: false` response
triggers a re-POST to `/api/push/subscribe`.

Params: `endpoint` (required, the push service URL).

Response: `{"registered": true|false}`.

Errors: 405 (non-GET), 400 (missing endpoint), 503 (push not configured).

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
and byte accounting, DX baseline event counts, DX-cluster / RBN / WSPR ingest
counters, plus a `prop_intel` block (`requests`, `errors`, `surges_detected`)
and a `push` block (`surges_detected`, `push_sent`, `push_errors`). The
de-facto health endpoint.

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
- The dormant tables (`proplab_dest_buckets`, `proplab_drap_snapshots`,
  `proplab_events`) are dropped by the startup migration; deployments that
  predate the drop can remove them manually.

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

- **No opmode proxy/control** — direct by design; browser → local agent.
  (`/api/opmode/status` existed briefly but nothing consumed it; removed.)
- **No awards API** — operator award progress is computed inside the local
  agent (`internal/awards`); never pulled server-side ("wanted" reaches the
  Chase Queue through the agent's enrich response).
- **No per-spot/path link scoring** — that is horstprop's contract,
  consumed via `/horstprop/v1/score`.
- **No `/api/proplab/*`** and **no `/api/dxpulse/*`** — both features were
  removed (see "Removed features"); only the pathscope feed tables remain.
