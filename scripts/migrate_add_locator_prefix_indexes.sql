-- Migration: locator-prefix indexes for dx_raw_spots.
--
-- Backs the `sender_locator LIKE ANY('PREFIX%')` predicates in
-- activityByBinForTargets (Band stats "Reports over time" chart) and
-- recent24hBandSlotCountsForTokens (DXLens rose). Prefix LIKE is
-- btree-sargable under text_pattern_ops regardless of database collation,
-- turning the target-filtered queries from full-window filter scans (which
-- hit the 6s context timeout at >=90-minute windows on the prod box) into
-- BitmapOr index scans.
--
-- CONCURRENTLY avoids taking the insert-blocking lock that a serial
-- CREATE INDEX inside initSchema would hold on a large dx_raw_spots table.
--
-- Run this once on an existing deploy BEFORE starting a binary that includes
-- the indexes in initSchema — its `CREATE INDEX IF NOT EXISTS` then no-ops.
-- CONCURRENTLY cannot run inside a transaction: use psql directly, not a
-- migration runner that wraps statements in BEGIN/COMMIT.
--
--   psql "$DATABASE_URL" -f scripts/migrate_add_locator_prefix_indexes.sql

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dx_raw_spots_sender_loc_time
	ON dx_raw_spots (sender_locator text_pattern_ops, spot_time);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_dx_raw_spots_receiver_loc_time
	ON dx_raw_spots (receiver_locator text_pattern_ops, spot_time);