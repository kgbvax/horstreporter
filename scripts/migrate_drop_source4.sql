-- migrate_drop_source4.sql  (v2 - resumable, per-chunk transactions)
--
-- Schema migration to v6:
--   1. Drop the `source4` dimension from dx_baseline_global and
--      dx_baseline_target.
--   2. Collapse 4-character Maidenhead locator target tokens into 2x2
--      blocks (e.g. JO32 / JO33 -> JO22).
--
-- Each chunk is a top-level INSERT (its own transaction), and uses
-- ON CONFLICT DO NOTHING so the script is safe to re-run after a partial
-- failure: completed chunks are skipped, only missing ones get inserted.
--
-- USAGE (live deploy):
--   1. systemctl stop horstreporter
--   2. Run via nohup so it survives SSH disconnects:
--        ssh ... 'nohup setsid sudo -u postgres psql -d dxdata \
--          -v ON_ERROR_STOP=1 -f /tmp/migrate_drop_source4.sql \
--          >/tmp/migrate_source4.log 2>&1 </dev/null &'
--   3. Watch /tmp/migrate_source4.log for progress; monitor `df -h /`.
--   4. After completion, deploy the v6 horstreporter binary.

\timing on
\set ON_ERROR_STOP on
\echo
\echo '=== Phase 2a: dx_baseline_global ==='
\echo 'Skip if the table already has the v6 shape (no source4 column).'

-- Idempotent guard: only do Phase 2a if source4 still exists.
DO $check_2a$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name='dx_baseline_global' AND column_name='source4'
  ) THEN
    RAISE NOTICE '2a: source4 exists, will rewrite dx_baseline_global';
  ELSE
    RAISE NOTICE '2a: dx_baseline_global is already v6, skipping';
  END IF;
END
$check_2a$;

DO $migrate_2a$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name='dx_baseline_global' AND column_name='source4'
  ) THEN
    RETURN;
  END IF;

  CREATE TABLE dx_baseline_global_new (
      band TEXT NOT NULL,
      slot_of_day INTEGER NOT NULL,
      distance_tier INTEGER NOT NULL,
      snr_tier INTEGER NOT NULL,
      count BIGINT NOT NULL,
      PRIMARY KEY (band, slot_of_day, distance_tier, snr_tier)
  );

  INSERT INTO dx_baseline_global_new (band, slot_of_day, distance_tier, snr_tier, count)
  SELECT band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
  FROM dx_baseline_global
  GROUP BY band, slot_of_day, distance_tier, snr_tier;

  DROP TABLE dx_baseline_global;
  ALTER TABLE dx_baseline_global_new RENAME TO dx_baseline_global;
END
$migrate_2a$;

\echo '=== Phase 2a complete ==='
\echo

\echo '=== Phase 2b: dx_baseline_target ==='

-- Create the helper function and new table only if they don't yet exist.
CREATE OR REPLACE FUNCTION block_token(tok TEXT) RETURNS TEXT AS $$
  SELECT CASE
    WHEN tok ~ '^[A-R][A-R][0-9][0-9]$' THEN
      substr(tok, 1, 2)
      || ((substr(tok, 3, 1)::int / 2) * 2)::text
      || ((substr(tok, 4, 1)::int / 2) * 2)::text
    ELSE tok
  END
$$ LANGUAGE SQL IMMUTABLE;

CREATE TABLE IF NOT EXISTS dx_baseline_target_new (
    target_token TEXT NOT NULL,
    band TEXT NOT NULL,
    slot_of_day INTEGER NOT NULL,
    distance_tier INTEGER NOT NULL,
    snr_tier INTEGER NOT NULL,
    count BIGINT NOT NULL,
    PRIMARY KEY (target_token, band, slot_of_day, distance_tier, snr_tier)
);

SET work_mem = '256MB';

-- ============================================================================
-- One INSERT per first-character prefix. Each is its own transaction; if
-- psql dies, completed chunks survive. ON CONFLICT DO NOTHING means a
-- re-run safely skips already-completed chunks.
-- ============================================================================

\echo 'chunk A'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'A' AND target_token < 'B'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk B'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'B' AND target_token < 'C'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk C'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'C' AND target_token < 'D'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk D'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'D' AND target_token < 'E'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk E'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'E' AND target_token < 'F'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk F'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'F' AND target_token < 'G'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk G'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'G' AND target_token < 'H'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk H'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'H' AND target_token < 'I'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk I'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'I' AND target_token < 'J'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk J'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'J' AND target_token < 'K'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk K'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'K' AND target_token < 'L'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk L'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'L' AND target_token < 'M'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk M'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'M' AND target_token < 'N'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk N'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'N' AND target_token < 'O'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk O'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'O' AND target_token < 'P'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk P'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'P' AND target_token < 'Q'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk Q'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'Q' AND target_token < 'R'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk R'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'R' AND target_token < 'S'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk S'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'S' AND target_token < 'T'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk T'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'T' AND target_token < 'U'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk U'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'U' AND target_token < 'V'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk V'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'V' AND target_token < 'W'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk W'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'W' AND target_token < 'X'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk X'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'X' AND target_token < 'Y'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk Y'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'Y' AND target_token < 'Z'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk Z'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= 'Z' AND target_token < '['
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 0'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '0' AND target_token < '1'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 1'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '1' AND target_token < '2'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 2'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '2' AND target_token < '3'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 3'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '3' AND target_token < '4'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 4'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '4' AND target_token < '5'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 5'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '5' AND target_token < '6'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 6'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '6' AND target_token < '7'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 7'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '7' AND target_token < '8'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 8'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '8' AND target_token < '9'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk 9'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token >= '9' AND target_token < ':'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk LEADING_SYMBOLS (anything before "0")'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE target_token < '0'
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo 'chunk MISC (between digits and uppercase A, and beyond Z)'
INSERT INTO dx_baseline_target_new (target_token, band, slot_of_day, distance_tier, snr_tier, count)
SELECT block_token(target_token), band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
FROM dx_baseline_target
WHERE (target_token >= ':' AND target_token < 'A')
   OR target_token >= '['
GROUP BY block_token(target_token), band, slot_of_day, distance_tier, snr_tier
ON CONFLICT (target_token, band, slot_of_day, distance_tier, snr_tier) DO NOTHING;

\echo '=== Phase 2b inserts complete; finalising swap ==='

BEGIN;
DROP TABLE dx_baseline_target;
ALTER TABLE dx_baseline_target_new RENAME TO dx_baseline_target;
COMMIT;

DROP FUNCTION block_token(TEXT);

\echo
\echo '=== Sanity checks ==='
SELECT pg_size_pretty(pg_total_relation_size('dx_baseline_global')) AS global_size,
       pg_size_pretty(pg_total_relation_size('dx_baseline_target')) AS target_size,
       (SELECT COUNT(*) FROM dx_baseline_target)                     AS target_rows;

SELECT COUNT(*) AS odd_digit_locator_blocks
FROM dx_baseline_target
WHERE target_token ~ '^[A-R][A-R][13579][0-9]$'
   OR target_token ~ '^[A-R][A-R][0-9][13579]$';

\echo '=== Done. Restart horstreporter (v6 binary) now. ==='
