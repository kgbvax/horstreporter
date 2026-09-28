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

- [ ] Postgres version. Record it.

  ```sql
  SHOW server_version;
  ```

  Column compression (lz4/pglz) no longer matters: seasonal rows are stored
  in a sparse encoding that stays below the TOAST threshold (see step 1).

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

2. Sparse row size. `almanac_season_counts.counts` stores only the non-zero
   cells of a key-month's 31×48 grid (`almanac_sparse.go`): a version byte,
   then per cell `uvarint(gap)` + one count byte, where
   `pos = (dom-1)*48 + slot` and `gap = pos - prevPos - 1`. A gap takes 1 byte
   below 128 and 2 bytes otherwise (gaps are < 1488), so

   `bytes/row = 1 + Σ(varint(gap) + 1) ≈ 1 + 2 × cells` (+1 per gap ≥ 128,
   at most ~11 per row).

   The size depends only on the number and spacing of non-zero daily rows per
   key-month, so it can be computed straight from the daily table. Treat the
   last 31 days as one key-month. Record `key_months`, `avg_cells`, the
   percentiles and `avg_counts_bytes`.

   ```sql
   SET statement_timeout = '300s';
   WITH cells AS (
     SELECT target_grid4, band, region,
            (day_index - :lo) * 48 + slot_of_day AS pos
     FROM dx_region_baseline_daily
     WHERE day_index >= :lo AND day_index < :lo + 31
       AND spot_count > 0
       -- On timeout, sample 10% of the keys (and scale key_months by 10):
       -- AND abs(hashtext(target_grid4 || band || region)) % 10 = 0
     GROUP BY 1, 2, 3, 4
   ), gaps AS (
     SELECT target_grid4, band, region,
            pos - lag(pos, 1, -1) OVER (PARTITION BY target_grid4, band, region ORDER BY pos) - 1 AS gap
     FROM cells
   ), rows AS (
     SELECT count(*) AS cells,
            1 + sum(CASE WHEN gap < 128 THEN 2 ELSE 3 END) AS bytes
     FROM gaps
     GROUP BY target_grid4, band, region
   )
   SELECT count(*)                                            AS key_months,
          round(avg(cells), 1)                                AS avg_cells,
          percentile_disc(0.5) WITHIN GROUP (ORDER BY cells)  AS p50_cells,
          percentile_disc(0.9) WITHIN GROUP (ORDER BY cells)  AS p90_cells,
          max(cells)                                          AS max_cells,
          round(avg(bytes))                                   AS avg_counts_bytes,
          count(*) FILTER (WHERE bytes > 2000)                AS rows_over_toast
   FROM rows;
   ```

3. Estimate: `bytes/year ≈ key_months × 12 × (avg_counts_bytes + 80) / 0.7`,
   where 80 B covers the tuple header, varlena header and key columns and 0.7
   is the fillfactor. Add about 30% for the primary key index. Record the
   result. **Stop and report if it exceeds 2 GB/year.**

   Reference (prod, 2026-09): 249.6k key-months per month, avg 94.8 non-zero
   cells (p50 8, p90 315, max 1343 of 1488). Sparse rows average ≈191–202 B
   (p90 ≈631 B, max ≈2.7 KB), so `249.6k × 12 × (~200 + 80) / 0.7 × 1.3 ≈
   1.55 GB/year`. The dense format it replaced measured 1492 B/row, ≈8.7
   GB/year.

   TOAST lesson: Postgres only attempts compression when a tuple exceeds the
   compile-time `TOAST_TUPLE_THRESHOLD` (~2 KB). `toast_tuple_target` only
   sets how far a tuple is shrunk once that gate has fired; it does not lower
   the gate. The old dense 1488-byte value sat just under it, so it was never
   compressed (lz4 or not), whatever `toast_tuple_target` said. The sparse
   encoding does the compression itself; the rare rows over ~2 KB
   (`rows_over_toast`) get Postgres' default compression on top.

4. SNR histograms (encoding v2, plan KTD13; target ≤ 3.5 GB/year). Since the
   SNR share release every cell also stores a mask byte plus one saturating
   byte per non-empty SNR bin ([<−20], [−20,−15), [−15,−10), [−10,−5),
   [−5,0), [≥0] dB), so `bytes/cell ≈ varint(gap) + 2 + bins`. Unit-test
   estimate (`TestAlmanacSparseV2SizingEstimate`, 95-cell row, 2.5 bins/cell
   avg): +308 B/row (3.24 B/cell) → ≈2.9 GB/year incl. the v1 baseline above.
   Cells before the SNR start (and DX-cluster-only cells) cost +1 B (mask 0).
   Once the SNR columns have filled for a few days, measure the real bin
   count (run with `:lo` ≥ the `almanac_snr_since_day` value):

   ```sql
   SET statement_timeout = '300s';
   WITH c AS (
     SELECT target_grid4, band, region, day_index, slot_of_day,
            sum(snr_spots) n, sum(snr_ge_m20) g20, sum(snr_ge_m15) g15,
            sum(snr_ge_m10) g10, sum(snr_ge_m5) g5, sum(snr_ge_0) g0
     FROM dx_region_baseline_daily
     WHERE day_index >= :lo AND spot_count > 0
     GROUP BY 1, 2, 3, 4, 5
   )
   SELECT count(*) AS cells,
          round(avg((n - g20 > 0)::int + (g20 - g15 > 0)::int + (g15 - g10 > 0)::int
                  + (g10 - g5 > 0)::int + (g5 - g0 > 0)::int + (g0 > 0)::int), 2) AS avg_bins,
          round(avg((n > 0)::int), 3) AS share_cells_with_snr
   FROM c;
   ```

   `bytes/year ≈ 1.6 GB + key_months × 12 × avg_cells × (1 + avg_bins) / 0.7`.
   **Stop and report if it exceeds 3.5 GB/year.**

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
  optional maintenance lines in `/home/hk/horst.log` for `skipped
  (almanac_season_counts ...)` lines.

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

- [ ] Month-scale churn. The backlog already folds ~33 days into one or two
  months, which is the "simulated month of folds". Each fold rewrites every
  affected row (a new heap tuple per key). Record the heap size and dead
  tuples after the backlog, and again after 7 days of steady folding, to see
  whether autovacuum keeps heap bloat bounded (TOAST should stay near empty):

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

**After the SNR share release (sparse encoding v2), never run a pre-v2 binary
with the fold enabled.** An older binary reads version-2 rows as malformed,
treats them as absent and rewrites the month row with only the folded day —
the rest of that key-month's history is lost. Rollback order: first set
`-almanac-fold-enable=false`, then deploy the older binary. Its reader also
shows v2 months as empty (display only; nothing is written). The SNR columns
of `dx_region_baseline_daily` are harmless to an older binary (it writes
`spot_count` only; the SNR counters of those days stay 0, so after a
roll-forward those days read "no SNR" as closed-for-floor — move
`almanac_snr_since_day` past the gap if that matters).

## 5a. SNR share rollout (KTD13)

- [ ] Before the deploy: `SELECT count(*) FROM information_schema.columns
  WHERE table_name = 'dx_region_baseline_daily' AND column_name LIKE 'snr_%';`
  (0 before, 6 after).
- [ ] The first start runs one `ALTER TABLE dx_region_baseline_daily ADD
  COLUMN IF NOT EXISTS snr_spots … snr_ge_0 INTEGER NOT NULL DEFAULT 0` (all
  six in one statement) under `SET LOCAL lock_timeout = '5s'`. With a constant
  default this is a catalog-only change on PG ≥ 11 (no rewrite), but it needs
  the ACCESS EXCLUSIVE lock on the hot ingest table: behind a long-running
  query it waits at most 5 s (queueing the flush meanwhile), then gives up.
  Later starts skip the ALTER entirely when the columns exist (the check is
  on `information_schema`, because `IF NOT EXISTS` still takes the lock).
- [ ] On a lock timeout the log shows `adding the SNR columns … failed` at
  ERROR; the process keeps running without SNR data (`snr_available: false`)
  and retries at the next restart. To add them by hand in a quiet moment:
  `SET lock_timeout = '5s'; ALTER TABLE dx_region_baseline_daily ADD COLUMN IF
  NOT EXISTS snr_spots INTEGER NOT NULL DEFAULT 0, …;` then restart.
- [ ] After the start: `SELECT v FROM dx_meta WHERE k = 'almanac_snr_since_day';`
  = deploy day + 1 (the partial deploy day is excluded). Floors read
  "not enough data" until `m_min` (10) covered days have accumulated.
- [ ] Next day: `SELECT sum(spot_count), sum(snr_spots), sum(snr_ge_m10)
  FROM dx_region_baseline_daily WHERE day_index = :today;` — `snr_spots` a
  little below `spot_count` (DX-cluster spots carry no SNR), cumulative
  counters decreasing with the tier.

## 5b. SNR backfill (one-off)

The deploy that added the SNR columns (2026-09-28 15:35:37 UTC, since-day
20725) collected SNR only from then on. The next deploy runs
`almanac_snr_backfill.go` once: the preceding days are filled from
`dx_raw_spots` (retention ~4 days), re-folded, and `almanac_snr_since_day`
moves down.

- [ ] Add `-almanac-snr-backfill-cutoff-unix 1790609737` to prod ARGS (the
  column-add time was not recorded in dx_meta by that deploy). It can stay:
  it is ignored once `almanac_snr_backfill_done` is set.
- [ ] Before the deploy, note the coverage:
  `SELECT to_timestamp(min(spot_time)) FROM dx_raw_spots WHERE source_type = 'mqtt';`
  Every day starting on/after that instant up to 2026-09-27 is SET; the
  2026-09-28 rows before 15:35:37 are ADDed.
- [ ] Log (3 min after start): `almanac SNR backfill: raw PSKReporter rows
  from …`, then one line per day (`… raw rows (… accepted), … keys (… with
  SNR) in … batch(es) (… already committed), … rows updated, … keys missing
  …; read …, total …`), `re-folded with SNR` for days ≤ the watermark, and
  `almanac SNR backfill done`. `hit lock_timeout … retrying` lines are
  expected occasionally on the deploy day (live flush contention). Any ERROR
  `almanac SNR backfill failed` → the next start retries: SET days are
  idempotent; the deploy day resumes after
  `almanac_snr_backfill_partial_progress` and is never added twice.
- [ ] Batches: past days 20 000 keys per transaction (30 s statement / 5 s
  lock timeout); deploy day 5 000 keys (10 s / 2 s). Watch
  `baseline_flush_fail_streak` in `/api/stats` stay 0 while it runs.
- [ ] `/api/stats` → `postgres.almanac_snr_backfill`: `done: true`,
  `since_day` = the earliest backfilled day, no `last_error`.
- [ ] Verify the daily counters (each backfilled day > 0, `snr_spots` a
  little below `spot_count`, cumulative counters decreasing with the tier):

  ```sql
  SELECT day_index, to_timestamp(day_index * 86400)::date AS day,
         sum(spot_count) AS spots, sum(snr_spots) AS snr_spots,
         sum(snr_ge_m20) AS ge_m20, sum(snr_ge_m10) AS ge_m10, sum(snr_ge_0) AS ge_0
  FROM dx_region_baseline_daily
  WHERE day_index >= (SELECT v::bigint FROM dx_meta WHERE k = 'almanac_snr_since_day')
  GROUP BY day_index ORDER BY day_index;
  SELECT k, v FROM dx_meta WHERE k LIKE 'almanac_snr%' ORDER BY k;
  ```

- [ ] The Almanac caches are purged after a successful run, so SNR floors
  cover the backfilled days at once.

## 5c. WSPR archive backfill (off until enabled)

- **Before enabling:** email the wspr.live admin. Say what is fetched (two
  aggregate GETs per UTC day for one area, ~2,200 requests for 3 years), the
  pace, and the User-Agent to look for:
  `horstreporter/1.0 (+https://horstreporter.kgbvax.net; DL9ET; almanac backfill)`.
- **Pacing:** one request at a time, 2 s pause after each one completes, at
  most 18 per rolling minute. Failures back off 15 s, 30 s (the backoff
  replaces the pause), and the run aborts after 3 in a row.
- **Duration:** about 2–2.5 h per area for 3 years (the 18/min cap binds when
  queries are fast; slower queries stretch it).
- **Enable:** add `-almanac-wspr-backfill-areas JO32` to ARGS, then watch
  `/api/stats` and the `ALMANAC_WSPR` log lines.

## 6. U2 query plans (stop condition: any read > 1.5 s)

Result (prod, 2026-09-28, JO32 r=2, watermark today−2, cold): (a) seasonal 64 ms, (b) tail aggregate 368 ms, (c) tail active days 181 ms, ingest/lost < 1 ms. No single read exceeds 1.5 s. The whole cold request (typical plus today-overlay reads) landed at 1.3–1.6 s, so `almanacQueryTimeout` was raised to 4 s. The tail index is not needed.

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

-- (a) Aggregated seasonal read (days ≤ W; sparse rows decoded in Go).
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
