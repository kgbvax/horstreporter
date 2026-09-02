-- Migration: locator-prefix indexes for dx_raw_spots.
--
-- Backs the sargable prefix-range arms (`locator ~>=~ $lo AND locator ~<~ $hi`)
-- in activityByBinForTargets (Band stats "Reports over time" chart) and
-- recent24hBandSlotCountsForTokens (DXLens rose). ~>=~/~<~ are members of the
-- text_pattern_ops opfamily (collation-independent), so the planner turns the
-- OR-ed arms into BitmapOr index scans instead of full-window filter scans
-- (which hit the 6s context timeout at >=90-minute windows on the prod box).
--
-- CONCURRENTLY avoids taking the insert-blocking lock that a serial
-- CREATE INDEX inside initSchema would hold on a large dx_raw_spots table.
--
-- Run this once on an existing deploy BEFORE starting a binary that includes
-- the indexes in initSchema — its `CREATE INDEX IF NOT EXISTS` then no-ops.
-- CONCURRENTLY cannot run inside a transaction: use psql directly, not a
-- migration runner that wraps statements in BEGIN/COMMIT.
--
--   df -h /var/lib/postgresql   -- check headroom first: each index is
--                                 roughly table_size / 8 (btree on a short
--                                 text + timestamp)
--   psql "$DATABASE_URL" -f scripts/migrate_add_locator_prefix_indexes.sql
--
-- A failed/interrupted CREATE INDEX CONCURRENTLY leaves an INVALID index of
-- the same name; the plain `IF NOT EXISTS` below would then skip rebuilding
-- it forever. The \gexec lines below drop such invalid leftovers first (no
-- row returned → nothing executed when the index is valid or absent).

SELECT 'DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_sender_loc_time'
WHERE EXISTS (
	SELECT 1 FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	WHERE c.relname = 'idx_dx_raw_spots_sender_loc_time' AND NOT i.indisvalid
) \gexec

SELECT 'DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_receiver_loc_time'
WHERE EXISTS (
	SELECT 1 FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid
	WHERE c.relname = 'idx_dx_raw_spots_receiver_loc_time' AND NOT i.indisvalid
) \gexec

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dx_raw_spots_sender_loc_time
	ON dx_raw_spots (sender_locator text_pattern_ops, spot_time);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dx_raw_spots_receiver_loc_time
	ON dx_raw_spots (receiver_locator text_pattern_ops, spot_time);