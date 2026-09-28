# Almanac fold rollout (U3) — prod checklist

Prod-only verification for plan `docs/plans/2026-09-28-001-feat-qth-propagation-almanac-plan.md`
unit U3 (seasonal record, fold, prune gating). Run the steps in order and paste
the recorded numbers into the PR. They replace the System-Wide Impact estimates
in the plan.

Conventions: database `dxdata`, psql as the DB owner (`sudo -u postgres psql dxdata`
or the usual DSN). Day indexes are `unix / 86400` (UTC). Compute them in the
shell, never with `extract()` in SQL (it defeats the day_index index):

```bash
TODAY=$(( $(date -u +%s) / 86400 ))
echo "today=$TODAY  today-2=$((TODAY-2))  30d-ago=$((TODAY-30))"
```

Pass them into psql as `-v today=$TODAY -v lo=$((TODAY-30))`.

---

## 0. Preconditions (before deploying the binary)

- [ ] Postgres version and lz4 support. Record both.

  ```sql
  SHOW server_version;
  SELECT 'lz4' = ANY(enumvals) AS lz4_available
  FROM pg_settings WHERE name = 'default_toast_compression';
  ```

  If `lz4_available` is false, the optional maintenance statement
  `ALTER ... SET COMPRESSION lz4` is skipped (logged) and pglz applies. Redo
  step 1 with pglz in that case. If the size then exceeds the 2 GB/year stop
  condition, stop: the plan's fallback is nibble-packing (744 B, cap 15), which
  is not implemented.

- [ ] Disk headroom: `df -h <postgres data dir>`. Record Use%. Above 80%, the
  prune grace period is skipped from the first run.

## 1. Storage sizing (stop condition: > 2 GB/year)

Run this before enabling the fold. It needs no new tables.

1. Distinct (grid4, band, region) keys per month, from the last 30 days of the
   daily table. Record `keys_30d`.

   ```sql
   SET statement_timeout = '120s';
   SELECT count(*) AS keys_30d
   FROM (SELECT DISTINCT target_grid4, band, region
         FROM dx_region_baseline_daily
         WHERE day_index >= :lo) k;
   ```

   If this times out, use the sampled estimate and record it as sampled:

   ```sql
   SELECT count(*) * 10 AS keys_30d_sampled_upper
   FROM (SELECT DISTINCT target_grid4, band, region
         FROM dx_region_baseline_daily TABLESAMPLE SYSTEM (10)
         WHERE day_index >= :lo) k;
   ```

   The ×10 scale-up overstates the distinct count, so treat it as an upper
   bound.

2. Compressed size of a packed row. This builds real 31×48 rows for a sample
   of keys in a temp table and measures them. Record `avg_counts_bytes` and
   `compression`.

   ```sql
   CREATE TEMP TABLE almanac_size_probe (counts bytea);
   ALTER TABLE almanac_size_probe ALTER COLUMN counts SET COMPRESSION lz4; -- skip on pglz
   WITH keys AS (
     SELECT DISTINCT target_grid4, band, region
     FROM dx_region_baseline_daily TABLESAMPLE SYSTEM (1)
     WHERE day_index >= :lo
     LIMIT 2000
   ), cells AS (
     SELECT d.target_grid4, d.band, d.region,
            (d.day_index - :lo) * 48 + d.slot_of_day AS pos,
            LEAST(d.spot_count, 255) AS c
     FROM keys k
     JOIN dx_region_baseline_daily d USING (target_grid4, band, region)
     WHERE d.day_index >= :lo AND d.day_index < :lo + 31
   )
   INSERT INTO almanac_size_probe
   SELECT decode(string_agg(lpad(to_hex(COALESCE(c.c, 0)::int), 2, '0'), '' ORDER BY p), 'hex')
   FROM keys k
   CROSS JOIN generate_series(0, 1487) p
   LEFT JOIN cells c
     ON c.target_grid4 = k.target_grid4 AND c.band = k.band AND c.region = k.region AND c.pos = p
   GROUP BY k.target_grid4, k.band, k.region;

   SELECT count(*) AS sampled_rows,
          avg(pg_column_size(counts))::int AS avg_counts_bytes,
          max(pg_column_compression(counts)) AS compression
   FROM almanac_size_probe;
   ```

3. Estimate: `bytes/year ≈ keys_30d × 12 × (avg_counts_bytes + 80) / 0.7`,
   where 80 B covers the tuple header and key columns and 0.7 is the
   fillfactor. Add about 30% for the primary key index. Record the result.
   **Stop and report if it exceeds 2 GB/year.**

## 2. Deploy

- [ ] Add the flags to `ARGS` in `/etc/default/horstreporter`:
  `-almanac-disk-path <postgres data dir>`. `-almanac-fold-enable` defaults
  to true, so no flag is needed for it. Confirm there are no duplicate
  retention flags:

  ```bash
  grep -o -- '-[a-z-]*retention-days[= ][0-9]*' /etc/default/horstreporter | sort | uniq -c
  grep -o -- '-almanac-[a-z-]*' /etc/default/horstreporter | sort | uniq -c
  ```

- [ ] Before `./deploy.sh`, record the WAL position and the (still missing)
  sizes:

  ```sql
  SELECT pg_current_wal_lsn() AS wal_before;
  ```

- [ ] `./deploy.sh`. The new tables are created at startup. Check the
  optional maintenance lines in `/home/hk/horst.log`: a `skipped
  (almanac_season_counts lz4 compression)` line means pglz is in use.

## 3. First run: backlog, fold time, WAL, TOAST

The first fold tick runs about 90 s after startup. It initialises the watermark
to `min(day_index)` and records that day in `almanac_lost_days`. It then folds
up to 64 days per tick, which covers the whole ~33-day backlog.

- [ ] Per-day fold time and backlog length. Record the median and maximum day
  time and the number of days:

  ```bash
  grep 'almanac fold: day' /home/hk/horst.log | tail -40
  grep -c 'almanac fold: day' /home/hk/horst.log
  ```

- [ ] WAL generated by the backlog, then per day (divide by the backlog
  length):

  ```sql
  SELECT pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), '<wal_before>')) AS wal_backlog;
  ```

- [ ] Table and TOAST size after the backlog:

  ```sql
  SELECT c.relname,
         pg_size_pretty(pg_relation_size(c.oid))            AS heap,
         pg_size_pretty(pg_relation_size(c.reltoastrelid))  AS toast,
         pg_size_pretty(pg_indexes_size(c.oid))             AS indexes,
         pg_size_pretty(pg_total_relation_size(c.oid))      AS total
  FROM pg_class c
  WHERE c.relname IN ('almanac_season_counts', 'almanac_area_activity',
                      'almanac_ingest_slots', 'almanac_lost_days');
  SELECT count(*), count(DISTINCT year_month) FROM almanac_season_counts;
  ```

- [ ] One steady-state fold. Next day, record the WAL position before the
  first tick after 01:00 UTC and again after it (or bracket one
  `almanac fold: day` log line). Record the WAL bytes and the size delta for
  that single day.

- [ ] Month-scale churn. The backlog already overlays ~33 days into one or two
  months, which is the "simulated month of folds". Record the TOAST size after
  the backlog, and again after 7 days of steady folding, to see whether
  autovacuum keeps TOAST bloat bounded:

  ```sql
  SELECT relname, n_dead_tup, last_autovacuum
  FROM pg_stat_all_tables
  WHERE relname = 'almanac_season_counts'
     OR relid = (SELECT reltoastrelid FROM pg_class WHERE relname = 'almanac_season_counts');
  ```

## 4. Post-deploy integrity (Verification Contract: Fold integrity)

- [ ] `/api/stats`: `postgres.almanac_fold.enabled = true`,
  `watermark_date` = today−2 (after 01:00 UTC once the flush lag has passed),
  `fail_streak = 0`, `lost_days = 1`.

  ```bash
  curl -s https://<host>/api/stats | jq .postgres.almanac_fold
  ```

- [ ] `almanac_lost_days` holds only the partly pruned first day:

  ```sql
  SELECT day_index, reason, to_timestamp(recorded_at) FROM almanac_lost_days ORDER BY day_index;
  ```

- [ ] For 3 folded but not yet pruned days (e.g. today−3, today−4, today−5),
  the per-(day, grid4) sums from the seasonal record equal those from the
  daily table, with each cell capped at 255. Run once per day `:d` with its
  `:ym` (yyyymm) and `:dom`:

  ```bash
  D=$((TODAY-3)); YM=$(date -u -d @$((D*86400)) +%Y%m); DOM=$(date -u -d @$((D*86400)) +%-d)
  psql dxdata -v d=$D -v ym=$YM -v dom=$DOM -f integrity.sql
  ```

  ```sql
  -- integrity.sql
  WITH daily AS (
    SELECT target_grid4 AS grid4, sum(LEAST(spot_count, 255)) AS s
    FROM dx_region_baseline_daily
    WHERE day_index = :d
    GROUP BY 1
  ), season AS (
    SELECT grid4, sum(get_byte(counts, (:dom - 1) * 48 + i)) AS s
    FROM almanac_season_counts, generate_series(0, 47) i
    WHERE year_month = :ym AND layer = 'pskr'
    GROUP BY 1
  )
  SELECT count(*) FILTER (WHERE daily.s IS DISTINCT FROM season.s AND COALESCE(season.s, 0) + COALESCE(daily.s, 0) > 0) AS mismatched_grids,
         count(*) AS grids,
         sum(daily.s) AS daily_total, sum(season.s) AS season_total
  FROM daily FULL OUTER JOIN season USING (grid4);
  ```

  Expected: `mismatched_grids = 0`. A grid with nothing on day `:d` still has
  a season row (zeros for that day) and compares as 0.

- [ ] Ingest totals for a post-deploy day match the daily per-slot sums:

  ```sql
  SELECT i.slot_of_day, i.spot_total, d.s
  FROM almanac_ingest_slots i
  JOIN (SELECT slot_of_day, sum(spot_count) s FROM dx_region_baseline_daily
        WHERE day_index = :d GROUP BY 1) d USING (slot_of_day)
  WHERE i.day_index = :d AND i.layer = 'pskr' AND i.spot_total <> d.s;
  ```

  Expected: no rows.

## 5. Rollback

Set `-almanac-fold-enable=false` in `ARGS` and redeploy. The fold stops and
the `dx_region_baseline_daily` prune is ungated again. The new tables stay;
never drop them and never make them UNLOGGED.

## 6. U2 query plans (stop condition: any read > 1.5 s)

Status: **pending** — must be run on prod (prod-sized tables); not runnable
from a dev checkout. Verification Contract "Query plans" for unit U2
(`/api/almanac`, `almanac_store.go`). Run each statement twice: once cold
(right after a Postgres restart, or at least on a key not read recently) and
once warm. Record `Execution Time` and `Buffers` for both. If any statement
exceeds 1.5 s, stop (Goal Capsule stop condition) and budget the
`scripts/migrate_*.sql` CONCURRENTLY tail index only then.

Parameters for JO32 at r=2 (all 25 squares with their ring levels, in the
order the reader sends them; day bounds computed in the shell, never
`extract()`):

```bash
TODAY=$(( $(date -u +%s) / 86400 ))
START=$(( TODAY - 30 )); END=$(( TODAY - 1 ))
W=$(sudo -u postgres psql dxdata -Atc "SELECT v FROM dx_meta WHERE k = 'almanac_fold_watermark_day'")
YM1=$(date -u -d @$(( START * 86400 )) +%Y%m); YM2=$(date -u -d @$(( W * 86400 )) +%Y%m)
echo "today=$TODAY start=$START end=$END W=$W months=$YM1,$YM2"
sudo -u postgres psql dxdata -v today=$TODAY -v start=$START -v end=$END -v w=$W \
  -v months="{$YM1,$YM2}" \
  -v grids='{JO10,JO11,JO12,JO13,JO14,JO20,JO21,JO22,JO23,JO24,JO30,JO31,JO32,JO33,JO34,JO40,JO41,JO42,JO43,JO44,JO50,JO51,JO52,JO53,JO54}' \
  -v rings='{2,2,2,2,2,2,1,1,1,2,2,1,0,1,2,2,1,1,1,2,2,2,2,2,2}' \
  -v bands='{160m,80m,60m,40m,30m,20m,17m,15m,12m,10m}'
```

The reader runs these in one `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY`
transaction; EXPLAIN them the same way:

```sql
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;

-- (a) Aggregated seasonal read (days ≤ W; packed rows unpacked in Go).
EXPLAIN (ANALYZE, BUFFERS)
SELECT grid4, band, region, year_month, counts
FROM almanac_season_counts
WHERE grid4 = ANY(:'grids'::text[])
  AND band = ANY(:'bands'::text[])
  AND year_month = ANY(:'months'::int[])
  AND layer = 'pskr';

-- (b) Tail aggregate (days > W, summed per ring level). Also the today
--     overlay refresh, with :start replaced by :today - 1.
EXPLAIN (ANALYZE, BUFFERS)
SELECT r.ring, d.band, d.region, d.day_index, d.slot_of_day, SUM(d.spot_count)::bigint
FROM dx_region_baseline_daily d
JOIN unnest(:'grids'::text[], :'rings'::int[]) AS r(grid4, ring) ON d.target_grid4 = r.grid4
WHERE d.band = ANY(:'bands'::text[])
  AND d.day_index > :w
  AND d.day_index BETWEEN :start AND :today
GROUP BY r.ring, d.band, d.region, d.day_index, d.slot_of_day;

-- (c) Tail active days (widening masks).
EXPLAIN (ANALYZE, BUFFERS)
SELECT DISTINCT target_grid4, band, day_index
FROM dx_region_baseline_daily
WHERE target_grid4 = ANY(:'grids'::text[])
  AND band = ANY(:'bands'::text[])
  AND day_index > :w
  AND day_index BETWEEN :start AND :end
  AND spot_count > 0;

-- (d) Ingest totals and lost days (small; PK range scans).
EXPLAIN (ANALYZE, BUFFERS)
SELECT day_index, slot_of_day, spot_total FROM almanac_ingest_slots
WHERE layer = 'pskr' AND day_index BETWEEN :start AND :today;
EXPLAIN (ANALYZE, BUFFERS)
SELECT day_index FROM almanac_lost_days WHERE day_index BETWEEN :start AND :end;

ROLLBACK;
```

If the window spans three months (e.g. 30 days ending 1 March), add the middle
month to `months`.

Record:

| Query | cold ms | warm ms | shared hit / read | plan node (index used) |
|---|---|---|---|---|
| (a) seasonal | | | | |
| (b) tail aggregate | | | | |
| (c) tail active days | | | | |
| (d) ingest + lost | | | | |

Then the end-to-end check: `time curl -s "http://127.0.0.1:<port>/api/almanac?qth=JO32" | jq '.area, (.lanes | length), .agenda[0]'`
— populated arrays, and a second call within 120 s served from cache.

## 7. Almanac benchmark (U9)

`scripts/almanac-benchmark.mjs` checks `/api/almanac` against the acceptance
tests in `docs/reference/typical-hf-openings-jo62.md` section 3, with the plan's
adaptations: NA judged as one region, test 8 dropped, test 7 "OC (VK)" mapped
to the app's `VK` region, 10 m tests reported but non-gating (SFI-dependent),
test 13 (AN) informational. The test table lives at the top of the script.

It is read-only (one GET, no writes), so run it straight against prod from any
machine, not on the box:

```bash
node scripts/almanac-benchmark.mjs https://<host> --qth JO32 --sfi <today's SFI>
node scripts/almanac-benchmark.mjs https://<host> --qth JO32 --json > tmp/almanac-bench.json
node scripts/almanac-benchmark.mjs --file tmp/almanac-bench-raw.json   # re-evaluate a saved /api/almanac response
node scripts/almanac-benchmark.mjs --selftest                           # evaluator sanity check, no network
```

Per test it prints PASS / FAIL / INCONCLUSIVE, the window's median share
(min/max) over known slots, and `n/m` per slot (`?/m` = unknown, `m < m_min`).
INCONCLUSIVE means fewer than half the window's slots are known. "Any hour"
rarely-tests (KH6, 160 m, AN) are judged on the max slot. Exit code: 0 when no
gating test fails, 1 otherwise, 2 on fetch/usage error. Take the SFI from the
same day as the run (e.g. SWPC daily F10.7); without `--sfi` it prints
`unknown`.

Tuning the KTD4 constants (`almanac_consts.go`: `almanacOpenMinSpotsPSKR` k,
`almanacMinActiveDays30` M_min, `almanacUsuallyShare`, `almanacAliveFraction`):

1. Run the benchmark on prod with the deployed defaults and keep the text output.
2. Change one constant at a time, deploy, wait for the 6 h typical cache to
   roll (or a watermark change), and re-run. Do not tune to force a pass: the
   benchmark is evidence, not an oracle (test 10 JA is medium confidence;
   observer bias makes quiet paths read low).
3. In the PR description, paste the before/after output (or the gating summary
   line plus the per-test lines that changed), the date, `--sfi` value, `area.radius`,
   the window, the final constant values, and a one-line explanation for every
   remaining FAIL or INCONCLUSIVE.
