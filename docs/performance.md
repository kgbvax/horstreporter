# Performance notes

How to measure, what was found, and the invariants that keep it fast.
Last profiling pass: 2026-10-04 (dev machine, 12 cores; numbers are for comparing runs, not absolutes).

## Measuring

| Tool | What it answers |
|------|-----------------|
| `node scripts/measure-concurrency.mjs` | Backend under concurrency: req/s, p50/p95 per endpoint, peak process memory, GC pauses, ingest lock wait. 16 clients + a live ingest appender over a 1M-message synthetic `hub.history`. Knobs: `HORST_LATENCY_HISTORY`, `HORST_CONCURRENCY_CLIENTS`, `HORST_CONCURRENCY_SECONDS`, `HORST_CONCURRENCY_INGEST_PER_SEC`. |
| `node scripts/measure-prop-latency.mjs` | Serial per-endpoint latency (one request at a time). Cannot see memory that scales with concurrency. |
| `node scripts/load-api.mjs 30 8 20` + `curl 'localhost:6060/debug/pprof/profile?seconds=30'` | The same endpoints against a real running server (`-pprof`), for CPU/heap profiles. |
| `node scripts/profile-ui.mjs [label]` | Headless-Chromium UI profile: load, idle, wheel zoom, pan, style/projection switches. `CPUPROF=1` writes a `.cpuprofile` (`scripts/cpuprofile-report.mjs` summarises it). |
| `node scripts/stress-proxy.mjs` | Contest-scale synthetic SSE feed in front of a dev server (`URL=http://localhost:8090`). Sends `history_end`; without it the loading spinner animates forever and inflates idle cost. |
| `node scripts/measure-load-network.mjs` | Page load under an emulated slow link (`LAT`, `KBPS`). |

A/B against a baseline: `git worktree add ../base HEAD`, copy `static/dist/` into it (build output, gitignored), run its dev server on another port, and point `scripts/stress-proxy.mjs` at it with `TARGET_PORT`/`PORT`.

The Mercator perf gate mocks Leaflet and cannot see render jank; headless Chromium has no GPU. Treat both as regression tripwires, not as user-experience numbers.

## Results (2026-10-04)

Backend, 16 concurrent clients, 1M-message history, live ingest running:

| | before | after |
|---|---|---|
| requests/s | 72 | 269 |
| peak process memory | 3.5 GB | 0.94 GB |
| GC pause total (12 s) | 236 ms | 17 ms |
| ingest appends completed (of ~4800 offered) | 1364 | 3376 |
| `prop_intel/v2` p50 / p95 | 424 / 552 ms | 60 / 94 ms |
| `dx_conditions` p50 / p95 | 175 / 279 ms | 70 / 104 ms |
| `hot_bands` p50 / p95 | 173 / 288 ms | 70 / 103 ms |
| `prop_intel/summary` p50 / p95 | 140 / 225 ms | 26 / 55 ms |

Second pass (same day), harness as above:

| | after pass 1 | after pass 2 |
|---|---|---|
| requests/s | 269 | 356 |
| peak process memory | 0.94 GB | 0.69 GB |
| `dx_conditions` / `hot_bands` p50 | 70 / 70 ms | 44 / 44 ms |

The harness fixture has only nine locators, so most messages match any area and the index helps little there. On a feed-like mix (250k-message window, 15 minutes, area 5x5 matching a few percent) `EvaluateArea` went from 24 to 8.8 ms, and for an own-square qth from 39 to 6.5 ms (`BenchmarkEvaluate*` in `evaluate_area_bench_test.go`).

Causes and fixes:

- Every history reader copied its window (`make`+`copy`: 52 MB for 15 minutes at 1M messages/hour, 208 MB for the full hour) and, for the API engines, held `hub.RLock` while doing it. Concurrent requests multiplied the memory, and the ingest append waited behind the readers. Readers now take zero-copy views (`Hub.windowFromLocked`); `/api/stats` scans outside the lock too.
- `MQTTMessage` shrank from 208 to 152 bytes: the DX-cluster/RBN annotations (comment, operator name, country, ISO) are four strings that FT8 spots never use and now sit behind `X *DXExtra`. `hub.history` at 1M messages: fixture heap 201 MB to 148 MB. `TestMQTTMessageStaysCompact` holds the budget.
- `dx_conditions` / `hot_bands` walked the whole window per request. `area_index.go` keeps, per (area, operator-cluster) key, the history sequence numbers of the messages that can influence the evaluation, extends it with only the new arrivals on each request, and hands `EvaluateAreaWindow` the matching positions. Used for locator QTHs (own square, 3x3, or areas up to radius 6) while matches stay under half the window; callsign QTHs and wide areas scan the whole window as before. `TestEvaluateAreaWindowIndexMatchesFullScan` compares indexed and full-scan responses across 11 scenarios while the history grows and loses its front.
- `regionFromLocatorCached` took a shared `RWMutex` read lock per message; with many concurrent requests the reader counter was ~25% of API CPU. Each request now has a private memo (`regionMemo`) in front of it.
- `extractMatchedBandEventArea` upper-cased four strings and built a `fmt.Sprintf` key for every message before testing the area. It now rejects on the raw locators first, decodes each locator once and builds the key without `fmt`. `matchAndCreateSpot` (SSE history replay and live fan-out) went from 2 allocations / 135 ns to 0 / 78 ns per message.

Frontend (headless Chromium against a local dev server unless noted):

- First spots on a throttled link (80 ms RTT, 3 Mbit/s): 3.6 s to 1.85 s from this change alone (the 3.6 s already had the slim outline file and module hints). The live stream used to start only after the country-outline download finished (`await` chain in the boot IIFE); Mercator boot no longer waits.
- First-load transfer 1456 KB to 968 KB: `world-slim.geojson` (570 KB gzip, was 1046 KB), mascot image resized to its 2x display size (112 KB to 36 KB).
- `.geojson` had no MIME type, so production served it as `text/plain`, skipped the precompressed static variants and gzipped it on every request (~36 ms CPU each). Registered in `gzip.go`.
- Grayline overlay build 77 ms to 16 ms in a Node microbenchmark (`static/grayline.js`; 75 ms of the load-time long task in the browser profile). It runs on the main thread at load and each 5-minute bucket.
- `modulepreload` hints for the 32 ES modules (about 170 ms at 80 ms RTT); `test/index-modulepreload.test.js` keeps the list in step with `app.js`.
- Favicon and status line no longer rewritten every frame.

Third pass (after deploying the second): a 20 s CPU profile of prod showed `allBandBaselinePairs` (two GROUP BY queries, ~25k rows per request) at ~47% of `dx_conditions` / `hot_bands` CPU, and the per-area index skipped in dense regions because the operator's 6x6-square cluster block alone matches over half the feed. The baseline indexes are now cached for 60 s (`baseline_pairs_cache.go`: global index shared, cluster indexes per cluster, one load shared by concurrent requests, stale copy served for up to 10 minutes if Postgres fails, 5 s retry back-off). Not measured on prod yet.

Fourth pass: `prop_intel/v2` (43% of prod CPU after the cache deploy). Per 250k-message window on a feed-like mix, `EvaluateV2Area` went from 47 to 20 ms (global mesh) and 45 to 12 ms (wide area), 502k allocations to 7.8k; the v1 nowcast from 19 to 11 ms and 152k allocations to 7.9k. The scan (`scanPropIntelV2Window`, `scanPropIntelWindow`) now resolves locators with in-place case folding (`propIntelRemote`), maps them to regions through a memo keyed by the raw feed string (`rawRegionMemo`: one upper-cased copy per distinct locator instead of two per message), and accumulates into arrays indexed by (band, region, source) instead of three-string map keys. `bandInScope` is a switch. Reference-equivalence tests compare both scans with the old implementations. Concurrency harness: 356 to 461 req/s, `prop_intel/v2` p50 57 to 30 ms.

## Postgres (profiled 2026-10-05 on prod via `pg_stat_statements` + `EXPLAIN (ANALYZE, BUFFERS)`)

`pg_stat_statements` is enabled on prod (stats since 2026-09-22), so per-statement numbers are available with `select ... from pg_stat_statements order by total_exec_time desc`.

| query (request path) | calls / 12 d | mean | notes |
|---|---|---|---|
| activity-by-bin on `dx_raw_spots` (3 variants) | 12.4k | 155-590 ms, max 55 s | the `(locator, spot_time)` indexes cannot bound `spot_time` across a locator range, so a 15-minute window scans the locator's whole 4-day retention: JO32 5.3k index blocks, PM95 18k blocks / 2.5 s cold. First request for an unwarmed QTH took 2-7 s; PM95 hit the 6 s query timeout on every call |
| `dx_baseline_cluster ... WHERE cluster_anchor` | 11.8k | 118 ms | 1.33M rows in 642 MB (~480 B/row after 424M HOT updates), 14.8k rows spread over 8.8k blocks |
| `dx_baseline_global GROUP BY` | 11.8k | 39 ms | 3.3 MB table, fine |
| prop climatology (30 days) | 82 | 1.0 s, max 3.3 s | CTE with a redundant per-day `SUM` spilled ~16 MB at `work_mem` 4 MB |
| WSPR climatology | few | 580 ms | same shape |

Server settings worth knowing: `shared_buffers` 128 MB (default) on an 8 GB box with a 17 GB table, `work_mem` 4 MB, `effective_cache_size` 4 GB, `track_io_timing` on. Write statements total ~39k DB-seconds in 12 days (about 4% of one core): not a bottleneck.

Changes from this profile (fifth pass):

- **Activity series from hub history** (`hubActivityByBin`): for a locator QTH whose window the hub fully covers (inside the retention, after `liveHistoryCompleteSince`, clear of the restart gap: `hubCoversWindow`) the 12-bin series is computed from the history the process already holds, with the area index's positions, instead of querying Postgres. Callsign QTHs and uncovered windows still query Postgres.
- **Time-first fallback query**: for windows up to 60 minutes the query appends `|| ''` to the locator columns so the planner uses the `spot_time` index; cost follows the window (PM95, 15 min: 2.5 s to ~120 ms; 60 min ~580 ms on prod).
- **Climatology SQL**: the per-day CTE is gone (the primary key already makes it one row per day). md5-identical results on prod; prop 700 to ~210 ms warm, WSPR 580 to ~150 ms.
- **Climatology refresh**: an expired cache is served stale while a background refresh (15 s deadline) reloads it; the first load runs at startup; the query no longer runs under the engine mutex. That mutex is taken by `Observe` under the hub write lock, so every refresh used to stall ingest and all history readers for the query's duration (0.7-2.3 s every ~2 minutes of traffic).

Still open on the Postgres side: rewrite `dx_baseline_cluster` (`CLUSTER` or `pg_repack` in a quiet period; the 60 s cache means it only matters once a minute per cluster), raise `shared_buffers` (1-2 GB, restart) and `work_mem` for the app role, and the infrequent heavy reads (`count(*)` on `dx_raw_spots` 4.5 s mean, `/api/history` chunks 0.6-1.5 s, retention `DELETE`s up to 25 s).

## Invariants

- `hub.history` is append-only; readers use views. See CLAUDE.md.
- Per-message hot paths do not allocate and do not call `fmt`.
- Stream start must not wait on decorative assets.

## Not done / open

- `prop_intel/v2` still walks the whole window: it counts every message per region (a global mesh view), so the per-area index does not apply. Keeping per-(band, region, source) counts incrementally would remove the walk; what is left per message is the raw-locator memo lookup and the call matching of the qthSet path.
- The area index is skipped when the match share exceeds half the window, which the operator-cluster term (6x6 squares) triggers in dense regions such as Western Europe. Counting the cluster activity separately (per-cluster, per-band, per-second counters) would let the index cover those requests.
- A short TTL response cache would still help when several clients share a QTH (the area index already makes the per-request cost small).
- The in-memory (no-Postgres) baseline path (`snapshotEventsLocked`, `buildBandActivityByBin`, `cloneBuckets`) is dev-only and was not optimised; it is most of what remains in the indexed `EvaluateArea` benchmark.
- Per-client gzip writers for the SSE stream cost ~1 MB each (150 clients, about 160 MB). `BestSpeed` saves a quarter of that at about 30% larger frames; not done.
- `MQTTMessage` could shrink further (152 bytes now): `B`, `MD` and `Source` are low-cardinality strings that could be small enums, and `RP`/`TXPower` could share a word.
- Active-area style with 20k unique coordinates blocks the main thread for ~130 ms (capped DBSCAN); first azimuthal switch ~95 ms. Neither shows at realistic spot counts.
- `static/vendor/world.geojson` (3 MB) is still embedded in the binary though the app no longer fetches it.
- Postgres paths (cold baseline queries, raw-spot disk growth) were not profiled: no database in this environment.
