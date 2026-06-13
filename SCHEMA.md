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

Global baseline buckets (not target-specific).

| Column | Type | Null | Notes |
|---|---|---|---|
| `band` | `TEXT` | NOT NULL | Normalized band token (`20m`, `40m`, etc.) |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket |
| `source4` | `TEXT` | NOT NULL | 4-char source grid |
| `distance_tier` | `INTEGER` | NOT NULL | Distance tier bucket |
| `snr_tier` | `INTEGER` | NOT NULL | SNR tier bucket |
| `count` | `BIGINT` | NOT NULL | Number of observations |

Primary key:

- `(band, slot_of_day, source4, distance_tier, snr_tier)`

Indexes:

- `idx_dx_baseline_global_band_slot` on `(band, slot_of_day)`

---

### `dx_baseline_target`

Target-specific baseline buckets.

| Column | Type | Null | Notes |
|---|---|---|---|
| `target_token` | `TEXT` | NOT NULL | Normalized target token (callsign/grid tokens) |
| `band` | `TEXT` | NOT NULL | Band token |
| `slot_of_day` | `INTEGER` | NOT NULL | UTC slot bucket |
| `source4` | `TEXT` | NOT NULL | 4-char source grid |
| `distance_tier` | `INTEGER` | NOT NULL | Distance tier bucket |
| `snr_tier` | `INTEGER` | NOT NULL | SNR tier bucket |
| `count` | `BIGINT` | NOT NULL | Number of observations |

Primary key:

- `(target_token, band, slot_of_day, source4, distance_tier, snr_tier)`

Indexes:

- `idx_dx_baseline_target_token_band_slot` on `(target_token, band, slot_of_day)`

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

## Notes

- There are no explicit foreign keys between these tables; relationships are logical/application-level.
- `spot_time` uses Unix epoch seconds (`BIGINT`), not PostgreSQL `timestamp`.
- Geometry is optional and may be `NULL` when no valid source coordinate can be derived.
