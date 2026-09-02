# Database Schema (`dxdata` / Postgres)

This document describes the database objects created/managed by HorstReporter in `dx_postgres.go` (`dxPostgresStore.initSchema`).

## Overview

- Database: PostgreSQL
- Required extension: PostGIS
- Main purpose:
  - store raw received spots
  - store baseline buckets for scoring
  - store region/day baseline aggregates for DXPulse-like summaries
  - store simple metadata key/value entries

## Extensions

### `postgis`

Created at startup if missing:

- `CREATE EXTENSION IF NOT EXISTS postgis;`

Used for `dx_raw_spots.spot_geom geometry(Point, 4326)` plus a GiST index.

## Tables

### `dx_baseline_global`

Global baseline buckets (all reporters worldwide).

| Column | Type | Null | Notes |
|---|---|---|---|
| `band` | `TEXT` | NOT NULL | Normalized band token (`20m`, `40m`, etc.) |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket |
| `distance_tier` | `INTEGER` | NOT NULL | Distance tier bucket |
| `snr_tier` | `INTEGER` | NOT NULL | SNR tier bucket |
| `count` | `BIGINT` | NOT NULL | Number of observations |

Primary key:

- `(band, slot_of_day, distance_tier, snr_tier)`

---

### `dx_baseline_cluster`

Grid-cluster baseline buckets — the middle tier of the cluster → global
fallback. Keyed by the 6×6 Maidenhead-square cluster anchor (e.g. `JN68`).
Replaces the removed per-callsign `dx_baseline_target` and 11-region
`dx_baseline_region` tables (v8).

| Column | Type | Null | Notes |
|---|---|---|---|
| `cluster_anchor` | `TEXT` | NOT NULL | 6×6 grid-cluster anchor locator |
| `band` | `TEXT` | NOT NULL | Band token |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket |
| `distance_tier` | `INTEGER` | NOT NULL | Distance tier bucket |
| `snr_tier` | `INTEGER` | NOT NULL | SNR tier bucket |
| `count` | `BIGINT` | NOT NULL | Number of observations |

Primary key:

- `(cluster_anchor, band, slot_of_day, distance_tier, snr_tier)`

---

### `dx_raw_spots`

Raw event store for MQTT and DX cluster spots.

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | `BIGSERIAL` | NOT NULL | Primary key |
| `spot_time` | `BIGINT` | NOT NULL | Unix timestamp (seconds) |
| `band` | `TEXT` | NOT NULL | Band token |
| `sender_callsign` | `TEXT` | NOT NULL | Sender/source callsign |
| `receiver_callsign` | `TEXT` | NOT NULL | Receiver/destination callsign |
| `sender_locator` | `TEXT` | NOT NULL | Sender locator |
| `receiver_locator` | `TEXT` | NOT NULL | Receiver locator |
| `mode` | `TEXT` | NOT NULL | Mode (`FT8`, `DXCLUSTER`, etc.) |
| `signal_report_db` | `INTEGER` | NOT NULL | Signal report (dB) |
| `source_grid4` | `TEXT` | NOT NULL | Normalized 4-char source grid |
| `spot_geom` | `geometry(Point, 4326)` | NULL | Point geometry derived from source grid |
| `source_type` | `TEXT` | NOT NULL | Event source (`mqtt`, `dxcluster`), default `mqtt` |
| `spotter_callsign` | `TEXT` | NOT NULL | Spotter callsign, default empty string |
| `frequency_khz` | `DOUBLE PRECISION` | NULL | Optional DX cluster frequency |
| `comment` | `TEXT` | NOT NULL | Optional DX cluster comment, default empty string |

Primary key:

- `id`

Indexes:

- `idx_dx_raw_spots_spot_time` on `(spot_time)`
- `idx_dx_raw_spots_band_spot_time` on `(band, spot_time)`
- `idx_dx_raw_spots_source_type_spot_time` on `(source_type, spot_time)`
- `idx_dx_raw_spots_spot_geom` GiST on `(spot_geom)`

---

### `dx_region_baseline_daily`

Daily aggregated region baseline by target/band/time-slot.

| Column | Type | Null | Notes |
|---|---|---|---|
| `target_grid4` | `TEXT` | NOT NULL | Target 4-char grid |
| `band` | `TEXT` | NOT NULL | Band token |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket |
| `region` | `TEXT` | NOT NULL | Region bucket |
| `day_index` | `BIGINT` | NOT NULL | UTC day index |
| `spot_count` | `BIGINT` | NOT NULL | Count of spots in that bucket |

Primary key:

- `(target_grid4, band, slot_of_day, region, day_index)`

Indexes:

- `idx_dx_region_baseline_lookup` on `(target_grid4, band, slot_of_day, region, day_index)`

---

### `wspr_region_baseline_daily`

Daily aggregated WSPR climatology by band/time-slot/region. Written by the
WSPR climatology accumulator (`wspr_climatology.go`) from the optional WSPR
ingest; read by the propagation-intelligence engine
(`/api/prop_intel`) for the atypical z-score. Unlike
`dx_region_baseline_daily` it has no target axis — it is a global mesh
climatology (the receiver's region), matching the engine's global view.

| Column | Type | Null | Notes |
|---|---|---|---|
| `band` | `TEXT` | NOT NULL | Band token |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket (30-min) |
| `region` | `TEXT` | NOT NULL | Receiver's region bucket |
| `day_index` | `BIGINT` | NOT NULL | UTC day index |
| `spot_count` | `BIGINT` | NOT NULL | Count of WSPR spots in that bucket |

Primary key:

- `(band, slot_of_day, region, day_index)`

Writes are upserts (`ON CONFLICT ... DO UPDATE`) accumulating into the
current day's row from a capped in-memory pending queue, with a JSONL
fallback (`wspr_climatology.json`) when Postgres is not configured.

---

### `dx_meta`

Generic metadata key/value table.

| Column | Type | Null | Notes |
|---|---|---|---|
| `k` | `TEXT` | NOT NULL | Key (primary key) |
| `v` | `TEXT` | NOT NULL | Value |

Primary key:

- `k`

Used for markers like:

- `json_migrated_at`
- `dxpulse_region_baseline_built_at`
- `proplab_cell_buckets_built_at`

---

### `proplab_cell_buckets`

Propagation Lab midpoint-cell buckets. Each row summarises one 15-minute bucket for a midpoint 4-character Maidenhead square, band, and source lane.

| Column | Type | Null | Notes |
|---|---|---|---|
| `bucket_start` | `BIGINT` | NOT NULL | Unix seconds, 15-min aligned |
| `band` | `TEXT` | NOT NULL | Normalised band token |
| `cell4` | `TEXT` | NOT NULL | Midpoint 4-char Maidenhead square |
| `region` | `TEXT` | NOT NULL | DXPulse region for the cell |
| `lane` | `TEXT` | NOT NULL | Source lane: `ft8`, `rbn`, `dcx` |
| `spot_count` | `INT` | NOT NULL | Total spots observed |
| `link_count` | `INT` | NOT NULL | Distinct sender+receiver link count (dedup) |
| `reporter_count` | `INT` | NOT NULL | Distinct callsigns as reporters/witnesses |
| `snr_median` | `SMALLINT` | NOT NULL | Median SNR for this lane |
| `snr_p10` | `SMALLINT` | NOT NULL | 10th-percentile SNR |
| `dist_median_km` | `INT` | NOT NULL | Median path distance |
| `dist_max_km` | `INT` | NOT NULL | Maximum path distance |

Primary key: `(bucket_start, band, cell4, lane)`

Indexes:

- `idx_proplab_cell_band_bucket` on `(band, bucket_start)`
- `idx_proplab_cell_region_band_bucket` on `(region, band, bucket_start)`
- `idx_proplab_cell_bucket_time` on `(bucket_start)`

---

### `proplab_sw_series`

Space-weather index time series polled by the Propagation Lab.

| Column | Type | Null | Notes |
|---|---|---|---|
| `series` | `TEXT` | NOT NULL | Index name: `kp`, `sfi`, `xray_flux`, `aurora_gw` |
| `obs_time` | `BIGINT` | NOT NULL | Unix seconds |
| `value` | `DOUBLE PRECISION` | NOT NULL | Numeric value |

Primary key: `(series, obs_time)`

Index: `idx_proplab_sw_series_time` on `(series, obs_time DESC)`

---

### `proplab_drap_snapshots`

D-RAP (D-Region Absorption Prediction) Highest-Affected-Frequency snapshots.

| Column | Type | Null | Notes |
|---|---|---|---|
| `obs_time` | `BIGINT` | NOT NULL | Unix seconds |
| `haf_grid` | `BYTEA` | NOT NULL | 36×24 grid of HAF in deci-MHz |

Primary key: `obs_time`

---

### `proplab_events`

Scheduled and live operating events that can explain demand-side spot spikes.

| Column | Type | Null | Notes |
|---|---|---|---|
| `source` | `TEXT` | NOT NULL | `wa7bnm`, `ng3k`, `pota` |
| `event_id` | `TEXT` | NOT NULL | Stable upstream identifier |
| `title` | `TEXT` | NOT NULL | Human-readable event name |
| `band_mask` | `TEXT` | NOT NULL | Band/mode hint, e.g. `80m-10m CW/SSB` |
| `start_utc` | `BIGINT` | NOT NULL | Unix seconds |
| `end_utc` | `BIGINT` | NOT NULL | Unix seconds |
| `locator4` | `TEXT` | NOT NULL | 4-char square when known |

Primary key: `(source, event_id)`

Index: `idx_proplab_events_window` on `(end_utc, start_utc)`

## Automatic migration behavior

On startup, schema init includes compatibility migrations for old `dx_raw_spots` column names:

- `t` -> `spot_time`
- `b` -> `band`
- `sc` -> `sender_callsign`
- `rc` -> `receiver_callsign`
- `sl` -> `sender_locator`
- `rl` -> `receiver_locator`
- `md` -> `mode`
- `rp` -> `signal_report_db`
- `source4` -> `source_grid4`
- `geom` -> `spot_geom`

It also adds newer columns if missing:

- `source_type`
- `spotter_callsign`
- `frequency_khz`
- `comment`

## Autovacuum / maintenance tuning

Hot tables get per-table autovacuum overrides applied by `dxPostgresStore.initSchema` (and `cellfeedSchemaStmts` for `proplab_cell_buckets`).

| Table | vacuum scale | vacuum threshold | analyze scale | analyze threshold | Notes |
|---|---|---|---|---|---|
| `dx_baseline_cluster` | 0.01 | 50 000 | 0.005 | 50 000 | heavy churn from baseline updates |
| `dx_baseline_global` | 0.02 | 20 000 | 0.01 | 20 000 | smaller table, more frequent vacuum |
| `dx_region_baseline_daily` | 0.01 | 50 000 | 0.005 | 50 000 | daily aggregates, many HOT updates |
| `dx_raw_spots` | 0.05 | default | 0.02 | default | insert-only raw event stream |
| `proplab_cell_buckets` | 0.05 | 10 000 | 0.02 | 10 000 | additive upserts on conflict |

The Debian service installer also drops `/etc/cron.d/horstreporter-vacuum` — a nightly `VACUUM (ANALYZE)` on the hot tables at 03:43 UTC as a safety net against bloat. This is a non-blocking plain `VACUUM`; run `VACUUM FULL` manually during a maintenance window if you need to reclaim disk from indexes/table bloat.

## Notes

- There are no explicit foreign keys between these tables; relationships are logical/application-level.
- `spot_time` uses Unix epoch seconds (`BIGINT`), not PostgreSQL `timestamp`.
- Geometry is optional and may be `NULL` when no valid source coordinate can be derived.
