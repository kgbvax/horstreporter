-- v8 migration: drop the per-callsign (dx_baseline_target) and 11-region
-- (dx_baseline_region) scoring baseline tables. They are replaced by
-- dx_baseline_cluster (6×6 grid-cluster anchors), which is created by
-- initSchema on the next startup and backfilled from dx_raw_spots by
-- ensureDxBaselineCluster.
--
-- The dx_region_baseline_daily table (11-region, daily granularity for the
-- DXLens region calendar) is NOT dropped — it stays as-is.
--
-- Run this once on an existing deploy before starting the new binary.
-- The new binary's initSchema creates dx_baseline_cluster; the backfill
-- populates it from dx_raw_spots automatically.

DROP TABLE IF EXISTS dx_baseline_target;
DROP TABLE IF EXISTS dx_baseline_region;

-- The new table is created by initSchema (CREATE TABLE IF NOT EXISTS), so
-- no explicit CREATE is needed here. The backfill runs on first startup.