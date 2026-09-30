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
  A locator qth is matched at its 4-char square: `FN76OJ` and `FN76` select the
  same reports (the full locator is only used for map centre and distances).
  Callsign qths are matched as given.
  (The QTH is the operator's own station, the point-of-view for all analysis;
  the older `target` param name is no longer accepted.)
- **`minutes`**: window size in minutes; invalid or ≤ 0 resets to the
  default; capped at a per-endpoint maximum.
- **`surroundings`**: `"true"` expands a locator qth to the 3×3 block of
  grid squares around it.
- **`rings`** (live views: `/api/stream`, `/api/dx_conditions`,
  `/api/hot_bands`, `/api/prop_intel/v2`): the area of interest around a locator
  qth, as a block of grid squares. An integer `0..30` is a fixed radius (rings
  around the home square; 0 = off). `auto` lets the server widen the block, ring
  by ring up to 3 rings (7×7 squares), until at least 3 bands have 25 or more
  reports in the last 20 minutes; the base is the own square, or the 3×3 block
  with `surroundings`. A dense area stays at its base, so `rings=auto` changes
  nothing there. The decision is cached per (grid4, base) for 10 minutes and
  shared by all four endpoints, and is not made while the in-memory history
  does not cover its 20-minute window (right after a restart) or the whole
  7×7 block holds fewer than 25 reports. Callsign qths are never widened.
  The response says what was used, see below.
  `area` (dx_conditions, hot_bands, prop_intel/v2 responses; the `area` stream
  event): `{"centre":"FN76","base_radius":0,"radius":2,"widened":true}`,
  present only when the request carried `rings`.

## Core endpoints

### `GET /api/stream` — live spot stream (SSE)

Server-sent events: initial history dump, then live spots.

Params: `qth` (required), `minutes` (default 15, max 60), `surroundings`,
`rings` (int 0..30, or `auto`; see Conventions — area-of-interest: any
sender/receiver within N grid squares of a locator qth; 0 disables; used by
horstprop's region feed, and `auto` by the web app).

With `rings`, the first frame is `event: area` carrying the `area` object above
(before the history dump, and again after every reconnect). Without `rings`
the stream is unchanged.

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
`region_counts`, trend + `sparkline`, `activity_by_bin`; band vs its own
normal (`activity_ratio`, `activity_level` =
`above`|`normal`|`below`|`low_sample`|`no_baseline`, `regional_spots`,
`regional_expected`, `baseline_local_scale`) and reach
(`baseline_p90_distance_km`, `reach_ratio`, `reach_level` =
`longer`|`typical`|`shorter`, omitted when unsupported).

Params also: `rings` (see Conventions). The response then carries `area`, and a
band gets `area_widened: true` when it only has a full sample (25 links)
because the area was widened. The baseline tiers are unaffected (the cluster
stays the baseline unit). For radius ≥ 2, `activity_by_bin` is binned from the
live history (at most its retention) instead of Postgres.

`activity_ratio` compares like with like: spots with an end in the operator's
6×6-square cluster, counted the way the cluster baseline is written (per end,
all SNR, FT8/FT4/DX-cluster only, no dedup), over the live-history span (≤ 60
min), against the per-slot cluster baseline integrated over that same span.
`above` ≥ 1.5×, `below` ≤ 0.67× (on the 2-decimal ratio); `low_sample` when both observed and expected
are under 20; `no_baseline` when a compared slot has no cluster baseline or
fewer than 100 raw counts. With `above`/`normal`/`below` it also drives the
score's activity term (ratio/2, else a neutral 0.5) and `/api/hot_bands`
ratios. The live span starts no earlier than the in-memory history is complete
(process start, or the startup backfill window). `baseline_local_scale` (local/regional
reports, same span) converts `baseline_activity_by_slot` into
`activity_by_bin` units for charting.

Cluster-sourced baseline rates divide by `cluster_baseline_history_minutes`
(top level): `baseline_history_minutes` × the cluster table's coverage,
refreshed hourly — from `dx_meta` `cluster_baseline_first_observed_at` (written
when the cluster backfill rebuilds the table) when present, else estimated as
Σcluster / 2·Σglobal — so a rebuilt cluster table isn't normalised by the
global table's longer span. `spots_per_minute` divides by the live span:
min(`minutes`, live-history retention), shortened after a restart to the part
of it the in-memory history actually covers (the gap between the newest
backfilled spot and ingest resuming is skipped on both sides of the ratio).

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
max 180), `cw_min_db`, `current_band` (excluded from `recommendations`).
Opt-in (omit all three and the response is unchanged: at most 3
recommendations, no `holding` key):
- `bands` — comma list of band labels (e.g. `80m,40m,30m,20m,17m,15m,12m,10m,6m`);
  recommendations are restricted to these bands *before* the result cap, so
  VHF/160m entries can't crowd them out. Unknown/out-of-scope labels are
  ignored; an empty (or all-unknown) list means no filter.
- `max` — overrides the 3-result cap, clamped to 1..12.
- `include` — comma list of bands to report in `holding`, whether or not they
  still qualify as a recommendation. Independent of `bands`; `current_band`
  does not exclude from it. A listed band appears only if it is in scope and
  present in the evaluation (had live spots in the window); a listed band
  with no live spots is simply missing (not a zero entry), and `holding` is
  omitted altogether when none of the listed bands qualify or no baseline
  engine is running. Clients should read a missing band as "no activity".

Response: `{…, recommendations: [{band, kind ("surprise"|"dx_surge"|"rising"),
priority ("high"|"normal"), reason, rank_score, spots_per_minute,
baseline_activity, activity_ratio, activity_level?, sustained_bins, p90_distance_km,
baseline_p90_distance_km?, distance_ratio?, trend, trend_delta, status}],
holding?: [{band, spots_per_minute, status, activity_level}]}`.
`activity_level` is present only when `activity_ratio` is the like-for-like
regional ratio from `/api/dx_conditions` (then it reads as "× normal"); in
`holding` it is always the raw `/api/dx_conditions` level (incl.
`low_sample`/`no_baseline`). Also `rings` (see Conventions); the response then
carries `area` (with `widened` when `rings=auto` widened it).

Every band must first have ≥ 2 trailing sparkline bins at ≥ 30 (of 100) and
≥ 0.5 spots/min. Classes, first match wins:
- `surprise` (high) — cluster baseline, ≥ 1 day of history, usual rate
  ≤ 0.1 spots/min, `activity_ratio` ≥ 5, live p90 ≥ 800 km, and either a
  regional ratio (`activity_level` above/normal/below) or — in the fallback
  your-squares-vs-baseline path — a live spot count with Poisson upper tail
  P(X ≥ count | baseline rate × live span) < 0.01.
- `dx_surge` (normal) — cluster baseline, ≥ 1 day of history,
  `/api/dx_conditions` `reach_level` "longer" with `reach_ratio` ≥ 1.5 and live
  p90 ≥ 5000 km. `baseline_p90_distance_km` / `distance_ratio` are that
  band's `baseline_p90_distance_km` / `reach_ratio`. `reach_ratio` compares a
  distance-tier-interpolated live p90 with the baseline's, while
  `p90_distance_km` (and the 5000 km floor) is the raw live percentile, so
  `p90_distance_km / baseline_p90_distance_km` need not equal `distance_ratio`.
- `rising` (normal) — trend "rising" with `trend_delta` ≥ 1.5, status
  green/yellow, `activity_ratio` ≥ 1.5, ≥ 3 sustained bins and ≥ 1.0 spots/min.
  `activity_ratio` needs something to compare against (a regional ratio or a
  baseline rate > 0); a band with neither has ratio 0 and never qualifies, so
  a slope alone never makes a band "rising".

Stateless: `rising` tracks the slope and drops out once an opening plateaus,
so clients following a band over time should `include` it and read `holding`
rather than treat absence from `recommendations` as "closed".

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

**Additive optional fields.** The payload is frozen except for additive
optional fields. `almanac` (object, optional) is a "usually open now / next"
glance from the QTH Almanac for the requesting QTH.
- **Source:** it is served only from the warm Almanac cache. The request path
  never queries Postgres or QRZ.
- **When it is omitted:**
  - Postgres or the Almanac is unavailable.
  - The area's cache is cold. Areas in `-almanac-wspr-backfill-areas` are
    pre-warmed after every fold run.
  - The `qth` is a callsign whose area has not yet been resolved by an
    earlier `/api/almanac` request.
  - The area is data-poor: no lane has any slot with `m >= m_min` (all
    lanes "not enough data", or no lanes). Such an area is never reported
    as closed.
- **Client handling:** treat a missing field as "no data". When the field is
  absent, the body is byte-identical to the pre-Almanac payload.
- **Fields:**
  - `grid4`: the Almanac centre.
  - `approximate`: true when a callsign resolved only to its DXCC centroid.
  - `text`: one line, no emojis, e.g.
    `Now usually: 20m NA, 17m AS. Next: 40m OC ~21:00`.
  - `entries`: the first ≤3 `/api/almanac` agenda windows, ongoing first and
    then by start time. Each entry has `band`, `region`, `start`/`end` (UTC
    `HH:MM`), `status` (`ongoing`|`upcoming`), `starts_in_min`, `n`/`m` (the
    peak slot opened on n of m active days) and `open_today`.

```
"almanac": {
  "grid4": "JO32", "approximate": false,
  "text": "Now usually: 20m NA. Next: 40m OC ~21:00",
  "entries": [
    {"band":"20m","region":"NA","start":"13:00","end":"18:00","status":"ongoing","starts_in_min":0,"n":22,"m":28,"open_today":true}
  ]
}
```

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

- `rings`: see Conventions. `from_here` is then true for a path with an end
  anywhere in the area, and the response carries `area`. `atypical` is omitted
  while the area is widened (the climatology is not scaled to a wider block, so
  its larger live counts would read as surges); surge push fan-out follows.

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

### `GET /api/almanac` — QTH propagation Almanac (30-day "opened N of M days")

For the operator's area: per band, far-end region and 30-min UTC slot, on how
many of the last 30 complete UTC days the slot was open (`n`) out of the days
it could be observed (`m`), plus an agenda of openings that are usual now or
in the next 12 h. Source: PSKReporter FT8/FT4 + DX-cluster spots with
locators (the region baseline); the WSPR backfill layer never enters this
view. Days ≤ the fold watermark come from the seasonal record, later days
from `dx_region_baseline_daily`, in one read-only REPEATABLE READ
transaction (each day counted once).

Params: `qth` (or `callsign` / `locator`, see Conventions). A 6/8-char
locator is truncated to its grid4; a callsign resolves via QRZ, falling back
to the DXCC centroid (`area.approximate: true`). Optional `min_snr` (integer
dB, −40…+30): an SNR floor, snapped to the nearest of the 5 dB tiers
−20/−15/−10/−5/0 dB (a tie goes to the higher tier); absent = any SNR
(exactly the behaviour without the parameter). The web UI sends the Min SNR
slider value of the active CW/SSB mode.

Status: `400` missing or invalid qth, or `min_snr` not an integer in
−40…+30 · `404` callsign that cannot be located ·
`503` no Postgres, read over the 4 s budget, or a failed read in the last
30 s (negative cache; `Retry-After: 30`).

Caching (keyed by the centre grid4 plus the applied SNR tier — not the raw
`min_snr` — LRU of 256): the typical part (lanes, radius) is kept 6 h, or
until the fold watermark changes or the UTC day rolls; the today overlay
(`open_today`) is re-read after 120 s. Served with
`Cache-Control: max-age=60`.

Response:
```
{
  "qth": "JO32AB",
  "area": {"grid4": "JO32", "source": "locator", "approximate": false,
           "radius": 1, "squares": ["JN41", "JN42", "…"]},
  "window": {"start_day": "2026-09-15", "end_day": "2026-10-14", "days": 30,
             "start_day_index": 20711, "end_day_index": 20740},
  "slot_minutes": 30, "m_min": 10, "k": 2, "usually_share": 0.5,
  "watermark_day": 20738, "now_slot": 25,
  "generated_at": 1792067400, "today_as_of": 1792067400,
  "min_snr": -12, "snr_tier": -10,
  "snr_available": true, "snr_since": "2026-09-29",
  "lanes": [
    {"band": "20m", "region": "NA", "n": [0, 0, …48], "m": [30, 30, …48],
     "open_today": false, "share": [null, 0.64, …48]}
  ],
  "agenda": [
    {"band": "20m", "region": "NA", "start_slot": 26, "len_slots": 10,
     "start": "13:00", "end": "18:00", "crosses_midnight": false,
     "all_day": false, "status": "upcoming", "starts_in_min": 30,
     "peak_slot": 27, "peak_n": 24, "peak_m": 30, "open_today": false,
     "peak_share": 0.64}
  ]
}
```

- Slot `s` covers `s*30 … s*30+30` minutes after 00:00 UTC. All times UTC.
- `m[s]`: window days on which slot `s` was *alive* (ingest total > 0 and
  ≥ 10% of that slot's 30-day median; lost days never count) and the area was
  *active* on the band (≥ 1 spot to any region, its own included, in slot `s`
  or `s+1`). A quiet night therefore reads unknown, not closed. `m` is a
  band-level quantity, repeated in every region lane of the band.
- `n[s]`: of those days, how many had ≥ `k` spots on the band to the region in
  slot `s`, summed across the area's squares. Days without spots count as
  closed (never skipped).
- A cell with `m[s] < m_min` is "not enough data" — distinct from closed.
- `area.radius`: rings around the centre square the area was widened to
  (0–2): the smallest radius at which more than half of the in-scope bands
  (160–10 m) have at least 8 known slots (4 h), a slot being known when it has
  `m_min` alive, area-active days, exactly the condition under which a lane
  shows it instead of "not enough data". (Counting days with any spot at any
  time of day let a sparse square pass while its lanes stayed unknown.)
  `squares` lists them.
- Lanes: every region for each band with at least one observed slot; bands
  never active in the area are omitted.
- `agenda`: windows of slots with `n/m ≥ usually_share` (known cells only),
  single-slot gaps bridged, crossing midnight allowed. Listed when `ongoing`
  (contains now) or `upcoming` within 12 h (`starts_in_min`); ongoing first,
  then by start. `end` is exclusive. `peak_*` is the window's best slot.
- `open_today`: the current or previous slot today already reached `k`
  within the same squares (from the unfolded daily tail).
- `watermark_day`: fold watermark (UTC day index) the lanes were read at;
  `-1` when the fold has never run.
- SNR floor (`min_snr` given; KTD13 of the Almanac plan): `min_snr` echoes
  the request, `snr_tier` is the applied tier floor (both `null` without a
  floor). A day is open in a slot when ≥ `k` of its spots reached the floor
  (spots with a real SNR only: PSKReporter; DX-cluster spots have none, so a
  cluster-only cell is not open). Days before `snr_since` have no SNR data
  and are left out of both `n` and `m` (unknown, never closed); `m` still
  uses the all-spot alive/active rules otherwise. `share[s]` is the pooled
  share of spots ≥ the floor among the SNR-carrying spots over the slot's
  `m` days (`null` when there were none); absent without a floor.
  `agenda[].peak_share` is that share at the peak slot. The agenda and
  `open_today` apply the floor too.
- `snr_available` / `snr_since`: SNR data is being collected (the SNR
  columns exist) and the first UTC day it covers (the day after the deploy
  that added them). `snr_available: false` → every floor reads unknown.
- Preliminary floor views: with a floor, `snr_days` is the number of window
  days carrying SNR data (on/after `snr_since`, not lost, ingest-alive in at
  least one slot) and `m_min` is the
  effective `min(10, max(2, snr_days))` that the lanes and the agenda were
  judged against. While that is below 10, `preliminary: true` (omitted
  otherwise). `snr_days` is absent without a floor. The widening radius is
  always chosen on the all-SNR activity at the full `m_min` of 10.
- The widget summary (`/api/prop_intel/summary` `almanac`) always uses any
  SNR.

### `GET /api/almanac/season` — Almanac seasonal drill-down (month × hour)

For one band and far-end region of the operator's area: one row per calendar
month (Jan–Dec) with 48 half-hour UTC slots of `n` (open days) and `m`
(observed days), taken from the seasonal record. Each month shows the most
recent year with at least `m_min` (8) active days — the PSKReporter/cluster
layer (`pskr`) first, else the backfilled WSPR layer (`wspr`) — labelled
with its year and layer; months with neither read `not_collected`.
The WSPR layer is read only when every square at the chosen radius lies
within the r=2 ring of some `-almanac-wspr-backfill-areas` centre (the
rings the backfill writes); for any other centre WSPR months are absent.
The watermark, the PSKR seasonal rows (days ≤ watermark), the PSKR daily
tail (later days, through yesterday) and the WSPR rows are read in one
read-only REPEATABLE READ transaction, so the current month joins folded and
unfolded days without counting any day twice. Lookback: the current month
and the 59 before it.

Params: `qth` (as `/api/almanac`), `band` (`160m`…`10m`, case-insensitive),
`region` (one of the 11 region codes, case-insensitive), optional `min_snr`
(as `/api/almanac`).

Status: `400` missing or invalid qth, band, region or `min_snr` · `404` callsign that
cannot be located · `503` no Postgres, read over the 4 s budget, or a
failed read in the last 30 s (`Retry-After: 30`).

Radius: the same as the landing view. The server takes `area.radius` from the
`/api/almanac` result for the same centre grid4 (computing it first when not
cached) and reads the squares within that radius, so lanes and drill-down
always describe the same area.

Caching (keyed by centre grid4 + SNR tier + band + region, LRU of 256): 6 h, or until
the fold watermark changes, the UTC day rolls or the landing radius changes.
Served with `Cache-Control: max-age=60`.

Response:
```
{
  "qth": "JO32",
  "area": {"grid4": "JO32", "source": "locator", "approximate": false,
           "radius": 1, "squares": ["JO32", "…"]},
  "band": "20m", "region": "OC",
  "slot_minutes": 30, "m_min": 8, "k": {"pskr": 2, "wspr": 1},
  "through_day": "2026-10-14", "watermark_day": 20738,
  "months": [
    {"month": 1, "name": "Jan", "status": "not_collected", "year": null,
     "layer": null, "days": 0, "k": null, "n": null, "m": null},
    {"month": 10, "name": "Oct", "status": "ok", "year": 2026, "layer": "pskr",
     "days": 14, "k": 2, "n": [0, …48], "m": [14, …48]},
    {"month": 12, "name": "Dec", "status": "ok", "year": 2025, "layer": "wspr",
     "days": 31, "k": 1, "n": [0, …48], "m": [31, …48]}
  ]
}
```

- `months` always has 12 entries, index 0 = January.
- Per layer, the same rules as `/api/almanac`: `m[s]` counts days on which
  slot `s` was alive (that layer's own ingest total > 0 and ≥ 10% of the
  slot's median over the month; lost PSKR days never count) and the area was
  active on the band (≥ 1 spot to any region in `s` or `s+1`); `n[s]` counts
  those days with ≥ `k` spots to the region (k = 2 PSKR, 1 WSPR).
- `days`: the month's days with at least one alive, active slot (the
  `m_min` test). Individual slots with `m[s] < m_min` are "not enough data".
- `through_day`: the last day included (yesterday; today is partial).
- SNR floor: the response carries `min_snr`, `snr_tier`, `snr_available`,
  `snr_since` as `/api/almanac`, and each `ok` month a `share[48]`. With a
  floor only the `pskr` layer is used (no WSPR fallback) and days before
  `snr_since` are left out, so earlier months read `not_collected`.
  With a floor each `ok` month also carries `snr_days` (its days on/after
  `snr_since`, not lost, ingest-alive in at least one slot) and its own effective `m_min` =
  `min(8, max(2, snr_days))`, plus `preliminary: true` when that is below 8;
  the top-level `preliminary: true` flags that some month is preliminary.
  With a floor, WSPR months are left out (no comparable SNR); the top-level
  `wspr_hidden: true` (omitted when false) says a WSPR backfill covers this area
  but the floor hides it, and the UI shows a note.
  The top-level `m_min` stays 8.

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

When Postgres is configured, a `postgres` block carries persistence health:
`raw_flush_last_ok_unix`, `raw_flush_fail_streak`,
`baseline_flush_last_ok_unix`, `baseline_flush_fail_streak`, and an
`almanac_fold` sub-object (Almanac seasonal-record fold health):

| Field | Meaning |
|---|---|
| `enabled` | `-almanac-fold-enable` is on and the fold driver is running |
| `watermark_day` | last folded UTC day index (`unix/86400`); `-1` until known or when disabled. Healthy: today−2 |
| `watermark_date` | the same day as `YYYY-MM-DD` (omitted while unknown) |
| `fail_streak` | consecutive failed fold runs (0 = healthy) |
| `last_ok_unix` | unix time of the last successful fold run |
| `lost_days` | rows in `almanac_lost_days`: days pruned before they were folded (read as "unknown", never "closed"). Expected: 1 (the partly pruned first day) |
| `late_region_drops` | live spots since start whose region-baseline keys were dropped because the spot time was outside [now − 24 h, now + 10 min] |
| `prune_gate_fail_streak` | consecutive daily prunes that skipped `dx_region_baseline_daily` because the fold gate (watermark / forced advance) could not be evaluated; the table is never pruned ungated. Logged at ERROR from 3, then every 24. 0 = healthy |

A sibling `almanac_snr_backfill` sub-object reports the one-off SNR backfill
(`null` when `-almanac-snr-backfill=false`):

| Field | Meaning |
|---|---|
| `enabled` | the backfill was started in this process |
| `running` | a run is in progress (it starts 3 min after startup) |
| `done` | dx_meta `almanac_snr_backfill_done` is set (this run or an earlier one) |
| `days_backfilled` | days written by this run (full days + the partial deploy day) |
| `since_day` | `almanac_snr_since_day` after a successful run (omitted until then) |
| `last_error` / `last_error_unix` | the last failure (omitted when none); the done key stays unset and the next start retries |

#### Almanac fold flags

- `-almanac-fold-enable` (default `true`): fold every final day of
  `dx_region_baseline_daily` (day ≤ today−2, a baseline flush succeeded after
  the day's end + 1 h, no pending delta for it) into the permanent seasonal
  record (`almanac_season_counts`, `almanac_area_activity`,
  `almanac_ingest_slots`), one day per transaction, on its own 15-minute
  ticker — independent of `-dx-region-baseline-retention-days` (the fold also
  runs with retention 0). While enabled, the `dx_region_baseline_daily` prune
  deletes only `day_index < min(cutoff, watermark + 1)`; unfolded days may
  outlive the cutoff by a 7-day grace period, after which a forced prune
  advances the watermark, records the days in `almanac_lost_days` and logs at
  ERROR. `false`: no fold and the prune is ungated. The `wspr_` and
  `prop_region_baseline_daily` prunes are never gated.
- `-almanac-disk-path` (default empty): filesystem path whose usage is probed
  (statfs) before the prune — on prod, the Postgres data directory. Above 80%
  used the grace period is skipped. Fail-safe: an unset, missing or
  unreadable path counts as over 80%, i.e. no grace.
- `-almanac-wspr-backfill-areas` (default empty = off): comma-separated grid4
  areas, e.g. `JO32`. For each area, the r=2 ring (5×5 grid4s) gets the `wspr`
  layer of the Almanac seasonal record, backfilled from the wspr.live archive.
  - **Coverage:** complete months only, newest first. It never touches the
    current month and never goes before 2008-03.
  - **Requests:** each UTC day makes two aggregate GETs, a ring aggregate and a
    global per-slot count (used for the WSPR ingest-alive totals). Requests run
    one at a time with a 2 s pause after each one completes, capped at 18 in
    any rolling minute so that, with the live poller's ~1/min, the total stays
    under wspr.live's 20/min.
  - **Identification:** requests send `User-Agent: horstreporter/1.0
    (+https://horstreporter.kgbvax.net; DL9ET; almanac backfill)` (the live
    `-wspr-enable` poller sends the same without `; almanac backfill`).
  - **Failures:** exponential backoff from 15 s, and the run aborts after 3
    consecutive failures. The last error is stored in dx_meta
    `almanac_wspr_backfill_last_error`.
  - **Commits:** each month commits in one transaction that replaces the WSPR
    layer for (ring grid4s, month), so a re-run never double-counts.
  - **Resume:** progress is kept in dx_meta
    `almanac_wspr_backfill_done_<AREA>_<yyyymm>` and
    `almanac_wspr_ingest_done_<yyyymm>`, so a restart resumes where it stopped.
  - **Disk guard:** it refuses to start, and stops before the next month, when
    the `-almanac-disk-path` disk is over 80% full or the probe fails.
  - It is never triggered by a visitor. It uses `-wspr-endpoint` as the base
    URL.
- `-almanac-wspr-backfill-years` (default 3): how many years of complete months
  to backfill, never earlier than 2008-03.

#### Almanac SNR data (no flag)

- `dx_region_baseline_daily` carries six SNR counters next to `spot_count`
  (INTEGER NOT NULL DEFAULT 0): `snr_spots` (spots with a real SNR —
  PSKReporter; DX-cluster spots are counted in `spot_count` only) and the
  cumulative `snr_ge_m20`, `snr_ge_m15`, `snr_ge_m10`, `snr_ge_m5`,
  `snr_ge_0` (spots with SNR ≥ −20 … ≥ 0 dB). Written by the live ingest
  and, once, for the days before the SNR start, by the SNR backfill (below);
  the raw-spot rebuild leaves them 0.
- Startup adds them when missing: one `ALTER TABLE … ADD COLUMN IF NOT
  EXISTS` for all six (metadata-only, constant default) under
  `lock_timeout = 5s`, skipped when `information_schema` already lists them.
  A failure is logged at ERROR and is not fatal: the process runs without SNR
  data (flush writes `spot_count` only, `snr_available: false`) and retries at
  the next start.
- dx_meta `almanac_snr_since_day`: the first UTC day index with complete SNR
  counters (the day after the columns were first confirmed); written once.
- The seasonal record (`almanac_season_counts.counts`) stores, from that day
  on, a 6-bin SNR histogram per cell (sparse encoding v2, see
  `almanac_sparse.go`); the WSPR backfill writes the same bins from
  wspr.live's `snr`.
- dx_meta `almanac_snr_columns_added_unix`: unix time the ALTER added the
  columns (live SNR counting starts there); written once by that start.

#### Almanac SNR backfill flags

- `-almanac-snr-backfill` (default `true`): one-off background job
  (`almanac_snr_backfill.go`, 3 min after startup) that fills the SNR counters
  for the days before `almanac_snr_since_day` from `dx_raw_spots`, then lowers
  `almanac_snr_since_day`. Guarded by dx_meta `almanac_snr_backfill_done`;
  `false` skips it.
  - **Rows:** only `source_type = 'mqtt'` (PSKReporter). They pass the live
    gates (FT8/FT4, known band, both locators) and key emitter unchanged;
    DX-cluster / RBN / WSPR rows are never read. The live 24 h late-spot
    clamp is not applied, and every counter is capped at the row's
    `spot_count`. `spot_count` itself is never touched.
  - **Range:** every UTC day before the deploy day that `dx_raw_spots` covers
    completely (`min(spot_time)` ≤ the day's start) is SET (idempotent), plus
    the deploy day (`almanac_snr_since_day − 1`) for spots before the cutoff,
    ADDed to the live counters exactly once. When the deploy day itself is
    not fully covered, nothing is backfilled.
  - **Writes:** batched, keys in a fixed order, keys without SNR skipped.
    Each batch is its own transaction: COPY its keys into a temp table, then
    `UPDATE … FROM`, pinned to a primary-key nested loop. SET batches (past
    days) hold 20 000 keys under `statement_timeout 30s` / `lock_timeout 5s`.
    ADD batches (deploy day, whose rows the live flush upserts) hold 5 000
    keys under `10s` / `2s`. A lock timeout backs off (1 s, 2 s, … 6
    attempts). Each ADD batch advances dx_meta
    `almanac_snr_backfill_partial_progress` in its own transaction, so a
    retry resumes after the last committed batch and never adds twice; the
    last one sets `almanac_snr_backfill_partial_done`. Keys missing in
    `dx_region_baseline_daily` are skipped and counted in the log. 200 ms
    pause between batches, 5 s between days.
  - **Re-fold:** days the fold watermark has passed are re-folded with SNR
    (same transaction as the fold, watermark unchanged). The since-day moves
    down (never up) and the done key is set together, under the watermark
    lock. On any failure the log shows an ERROR, the done key stays unset,
    the since-day is unchanged, and the next start retries.
- `-almanac-snr-backfill-cutoff-unix` (default `0` = dx_meta
  `almanac_snr_columns_added_unix`): unix time live SNR counting started
  (end of the partial deploy day's window). Must lie inside the deploy day.
  The prod deploy that added the columns predates that dx_meta key and needs
  `-almanac-snr-backfill-cutoff-unix 1790609737` (2026-09-28 15:35:37 UTC);
  without a cutoff the run fails and retries at the next start.

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
