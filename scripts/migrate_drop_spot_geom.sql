-- Drop dx_raw_spots.spot_geom: the column is write-only (zero readers in
-- horstreporter, dxlens, horstapp; verified by grep Sep 2026), its GIST index
-- was already dropped as ~5GB unused. ALTER TABLE DROP COLUMN is catalog-only
-- (instant, no table rewrite): future INSERTs stop paying the ~25B/row geometry
-- tuple width; bytes already in existing heap rows are reclaimed only by a
-- table rewrite (e.g. VACUUM FULL dx_raw_spots during a quiet period) or
-- naturally over time via the retention prunes.
-- Run: psql -d dxdata -f scripts/migrate_drop_spot_geom.sql
-- Idempotent; the binary's startup DDL runs the same DROP COLUMN IF EXISTS.
ALTER TABLE dx_raw_spots DROP COLUMN IF EXISTS spot_geom;

-- Legacy pre-rename name (geom) from very old deploys, if still present.
ALTER TABLE dx_raw_spots DROP COLUMN IF EXISTS geom;