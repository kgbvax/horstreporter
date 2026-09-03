-- Migration: day_index indexes for the region-baseline daily tables.
--
-- dx_region_baseline_daily and wspr_region_baseline_daily are keyed
-- (target_grid4/band, band, slot_of_day, region, day_index) with day_index as
-- the LAST PK column. The reader (regionCalendarStats / WsprRegionCalendarStats:
--   WHERE day_index BETWEEN today-29 AND today)
-- and the hourly prune (pruneDayIndexedBaselineOlderThan:
--   WHERE day_index < cutoff)
-- therefore cannot use the PK — both would seq-scan the whole table. These
-- single-column day_index btrees make them sargable, which both keeps the
-- prune cheap and speeds up the prop_intel / dxlens region-climatology reads
-- (the 10GB seq-scan + GROUP BY was a prop_intel latency contributor on prod).
--
-- CONCURRENTLY avoids the insert-blocking lock a serial CREATE INDEX inside
-- initSchema would hold. Run this once on an existing deploy BEFORE starting a
-- binary that includes the indexes in initSchema — its CREATE INDEX IF NOT
-- EXISTS then no-ops. CONCURRENTLY cannot run inside a transaction: use psql
-- directly, not a migration runner that wraps statements in BEGIN/COMMIT.
--
--   df -h /var/lib/postgresql   -- check headroom first: each index is roughly
--                                 table_size / 8 (btree on a single bigint)
--   psql "$DATABASE_URL" -f scripts/migrate_add_region_day_index.sql
--
-- A failed/interrupted CREATE INDEX CONCURRENTLY leaves an INVALID index of the
-- same name; the plain IF NOT EXISTS below would then skip rebuilding it
-- forever. The \gexec lines below drop such invalid leftovers first (no row
-- returned → nothing executed when the index is valid or absent).

SELECT 'DROP INDEX CONCURRENTLY IF EXISTS idx_dx_region_baseline_daily_day_index'
WHERE EXISTS (
	SELECT 1 FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	WHERE c.relname = 'idx_dx_region_baseline_daily_day_index' AND NOT i.indisvalid
) \gexec

SELECT 'DROP INDEX CONCURRENTLY IF EXISTS idx_wspr_region_baseline_daily_day_index'
WHERE EXISTS (
	SELECT 1 FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	WHERE c.relname = 'idx_wspr_region_baseline_daily_day_index' AND NOT i.indisvalid
) \gexec

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dx_region_baseline_daily_day_index
	ON dx_region_baseline_daily (day_index);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_wspr_region_baseline_daily_day_index
	ON wspr_region_baseline_daily (day_index);