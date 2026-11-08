# Removal candidates: backend features without a web UI consumer

Survey date: 2026-11-08, tree at `ad7b051` (main). This is a list only: nothing
has been deleted. Re-run the greps on the then-current tree before acting on any
entry.

## Method

- Backend inventory: every flag in `cmd/horstreporter/main.go`, every route
  registered on `appMux` (`main.go:668-722`), every background ingest/engine
  started from `main()`.
- Web UI: fetch URLs and SSE params in `static/*.js` (excluding `vendor/` and
  `*.test.js`), the Svelte controls in `src/main.js`, and controls in
  `static/index.html`.
- Other consumers checked: `~/dev/horstapp` (`lib`, `ios`, `android`),
  `cmd/horstprop`, `cmd/pathscope` + `internal/proxy`, dxlens
  (`dxlens_mount.go`, `../dxlens`), `cmd/horstoperator-agent`, `scripts/`,
  `deploy.sh`, and the prod-ARGS facts recorded in the project memory notes
  (prod ARGS themselves were not read; that needs SSH).
- Line counts are non-test Go/JS lines unless stated; tests usually add 50-150%.

### What the web UI calls

`/api/stream`, `/api/history`, `/api/dx_conditions`, `/api/hot_bands`,
`/api/prop_intel/v2`, `/api/almanac`, `/api/almanac/season`,
`/api/square_details`, `/api/capture_snapshot` (capture mode only), `/api/push/*`.
SSE params include `include_rbn`, `include_wspr` and `include_dxcluster`
(the "DX cluster", "RBN spots" and "WSPR spots" toggles).

Not called by the web UI: `/api/stats`, `/api/prop_intel`,
`/api/prop_intel/summary`, `/pathscope/`, `/dxlens/`.

## Summary

| # | Candidate | Group | Risk | Size (approx.) |
|---|-----------|-------|------|----------------|
| S1 | `-opmode-agent-url` deprecated flag | safe | low | ~5 |
| S2 | `DxBaselineEngine.LoadSpotsBetween` / `NumBuckets` (test-only callers) | safe | low | ~20 + tests |
| S3 | `docs/dead-code-inventory.md` (stale) | safe | low | doc only |
| C1 | DX cluster ingest (`dxcluster.go`, `-dxcluster-*`, `include_dxcluster`, UI toggle) | check first | medium | ~400 Go + ~650 test + ~150 scattered |
| C2 | Capture mode + `/api/capture_snapshot` | check first | low-medium | ~130 Go + ~40 JS |
| C3 | Almanac SNR backfill (one-off) | check first | low | ~965 + tests |
| C4 | `prop_baseline_backfill.go` (bounded 45-day seed) | check first | low | ~250 + tests |
| C5 | Almanac WSPR archive backfill | check first | medium | ~845 + tests |
| C6 | Pathscope stack (`/pathscope/` proxy, `cmd/pathscope`, cell feed, SW ingest, `internal/proplab`) | check first | medium | ~2,900 Go + DB tables |
| C7 | dxlens mount (`/dxlens/`, `../dxlens` replace) | check first | medium | ~450 + sibling module |
| C8 | `cmd/horstprop` + `internal/propcontract` | check first | low-medium | ~1,800 |
| C9 | QRZ enrichment (`-qrz-*`, `internal/qrzlookup`) | check first | low | ~150 + wiring |
| C10 | `-wspr-climatology-file` JSONL fallback/seed | check first | low | part of `wspr_climatology.go` |
| K1-K9 | see "Keep" | keep | - | - |

---

## Safe to remove

### S1. `-opmode-agent-url` flag

- **What:** `main.go:304`, warning at `main.go:344`. Help text says
  "Deprecated and ignored: backend never proxies to local operator agent".
- **Why unused:** the flag does nothing but log a warning. The browser talks to
  the agent at `127.0.0.1:9955` directly (`static/opmode.js`).
- **Who could depend on it:** only a prod `ARGS` line that still passes it.
  Removing the flag without stripping it makes the binary exit on startup.
- **Risk:** low, provided `opmode-agent-url` is added to `REMOVED_FLAGS` in
  `deploy.sh` in the same change.
- **Size:** ~5 lines.

### S2. `DxBaselineEngine.LoadSpotsBetween` and `NumBuckets`

- **What:** `dx_conditions.go:404` (`LoadSpotsBetween`, a thin wrapper of
  `LoadSpotsBetweenFiltered`) and `dx_conditions.go:507` (`NumBuckets`).
- **Why unused:** no non-test caller; `main.go:506` calls
  `LoadSpotsBetweenFiltered` directly. Only `dx_conditions_test.go` uses them.
- **Who could depend on it:** nothing outside `package main`.
- **Risk:** low (tests need switching to the filtered variant or dropping).
- **Size:** ~20 lines + test lines.

### S3. `docs/dead-code-inventory.md`

- **What:** older symbol-level inventory.
- **Why:** it predates the move of `package main` to `cmd/horstreporter`
  (`aeb8a94`), so its `file:line` references point at the old repo-root files,
  and several entries have since been acted on. This file
  (`docs/removal-candidates.md`) supersedes it at the feature level.
- **Risk:** low. Either delete it or re-verify it and fold the live entries in here.

---

## Check first

### C1. DX cluster ingest (still present)

- **What:** `cmd/horstreporter/dxcluster.go` (395) + `dxcluster_test.go` (656);
  six `-dxcluster-*` flags (`main.go:258-263`) and the `DXCLUSTER_USERNAME/PASSWORD`
  env fallbacks; the package global `dxClusterEnabled`; `include_dxcluster` in
  `server.go:377` / `stream.go` / `capture_snapshot.go`; `dxcluster_*` keys in
  `/api/stats` (`server.go:616`); the `dxcluster` source in
  `prop_intel_sources.go` and `prop_baseline_backfill.go`; the v2 wire code `d`
  (`stream_wire.go`, `static/stream-wire.js:8`); `includeDXCluster` in
  `history.go` and `LoadSpotsBetweenFiltered`. Frontend: the "DX cluster" toggle
  (`src/main.js:59-61`, `#dxcluster-root` in `static/index.html:244`, built into
  `static/dist/horst-ui.js`) and the `dxcluster` branches in `app.js`,
  `renderers.js`, `azimuth-runtime.js`, `session-ring.js`.
- **Why it looks unused:** prod ingest has been disabled since Sep 4
  (displacement loop with DA0BCC-7, see memory note *DX cluster displacement
  loop*), so the UI toggle filters a source that never arrives. Its only
  real consumer, the Chase Queue, was removed in `8df6e2b`, and the write-only DX
  annotations went in `89f5fb5`.
- **Note on the record:** the memory note *DX cluster + Chase Queue removed*
  (Oct 9) lists `dxcluster.go`, the `-dxcluster-*` flags, `include_dxcluster`,
  the `dxcluster` prop_intel source and wire code `d` as removed. That removal
  never landed on main; only the Chase Queue part did. So the removal was
  already decided once; it just was not merged.
- **Who could still depend on it:** prod `ARGS` if they still carry
  `-dxcluster-*` (crash-loop on an unknown flag, so `deploy.sh REMOVED_FLAGS`
  needs all six names); the RBN login callsign falls back to
  `DXCLUSTER_USERNAME` (`main.go:629`), so prod needs `RBN_CALLSIGN` (or
  `-rbn-callsign`) set before that fallback goes; legacy `dx_raw_spots` rows with
  `source_type='dxcluster'` (the Almanac SNR backfill tolerates them, keep that
  tolerance). `cmd/horstprop` reads `/api/stream` but does not pass
  `include_dxcluster`.
- **Risk:** medium (many touch points, wire format, deploy flags). Low
  functional risk, since prod has no DX-cluster data.
- **Size:** ~400 Go + ~650 test lines in `dxcluster*.go`, plus ~150 lines
  scattered across `main.go`, `server.go`, `stream*.go`, `prop_intel*.go` and
  ~30 lines of JS/Svelte.

### C2. Capture mode and `/api/capture_snapshot`

- **What:** `cmd/horstreporter/capture_snapshot.go` (128), the route at
  `main.go:677`; frontend capture mode (`?capture=1` / `?mode=capture`,
  `app.js:215` onwards, `loadCaptureSnapshot` around `app.js:1095`, capture
  handshake, `window.__horstCaptureConfig` in `src/main.js:21-30`), docs in
  `docs/api.md:155`.
- **Why it looks unused:** `docs/api.md` describes it as the snapshot for
  "server-driven frame capture". That driver was the horstvideo sidecar, which was
  retired with time travel (`deploy.sh` tears down `horstvideo.service`). Nothing
  in `scripts/` or any other binary sets `capture=1` now.
- **Who could still depend on it:** a manual screenshot workflow that opens
  capture URLs by hand; `scripts/profile-ui.mjs` or perf scripts if they use
  deterministic snapshots (grep found none).
- **Risk:** low-medium: the capture branches run through boot code in `app.js`
  and `src/main.js` (localStorage seeding is skipped in capture mode), so removal
  touches app start-up.
- **Size:** ~130 Go + ~40-60 JS lines.

### C3. Almanac SNR backfill (one-off)

- **What:** `almanac_snr_backfill.go` (965), flags `-almanac-snr-backfill`
  (default **true**) and `-almanac-snr-backfill-cutoff-unix`.
- **Why it looks unused:** a one-off job guarded by `dx_meta
  almanac_snr_backfill_done`. Per the memory note *QTH Almanac*, prod was
  backfilled (SNR since Sep 25). After that it only does a guard check at
  each startup.
- **Who could still depend on it:** a fresh install restoring old
  `dx_raw_spots` (no realistic case); prod `ARGS` if they pass
  `-almanac-snr-backfill-cutoff-unix=1790609737` (must go into `REMOVED_FLAGS`).
- **Check first:** `SELECT * FROM dx_meta WHERE key = 'almanac_snr_backfill_done'`
  on prod.
- **Risk:** low.
- **Size:** ~965 lines + tests (`almanac_snr_backfill_test.go`, `almanac_snr_test.go`).

### C4. `prop_baseline_backfill.go`

- **What:** a resumable background job that seeds `prop_region_baseline_daily`
  for pskr/rbn/dxcluster from the last 45 days of `dx_raw_spots`, with a
  `dx_meta` cursor.
- **Why it looks unused:** it is a bounded catch-up. Once the cursor is past the
  45-day window, the live `propBaseline` observe path keeps the table current. With
  `-dx-raw-spot-retention-days=4` on prod, it can only ever see 4 days anyway.
- **Who could still depend on it:** a fresh deploy or a lost
  `prop_region_baseline_daily` (it rebuilds at most 4 days on prod, so its
  value there is small).
- **Check first:** the cursor in `dx_meta`; whether `/api/prop_intel/v2` normals
  need it after a DB restore.
- **Risk:** low.
- **Size:** ~250 lines + tests.

### C5. Almanac WSPR archive backfill

- **What:** `almanac_wspr_backfill.go` (844), flags
  `-almanac-wspr-backfill-areas` (default empty = off) and
  `-almanac-wspr-backfill-years`.
- **Why it looks unused:** off by default, and the *QTH Almanac* memory note
  records "WSPR backfill off" on prod.
- **Who could still depend on it:** the same areas flag also gates the Almanac
  drill-down's WSPR layer and is pre-warmed after each fold. Removing the
  backfill means deciding whether that WSPR layer stays (fed only by live WSPR
  ingest) or goes too. `/api/almanac` is a web UI endpoint (`static/almanac.js`).
- **Risk:** medium (it is tied to a visible Almanac layer).
- **Size:** ~845 lines + tests.

### C6. Pathscope stack

- **What:** the `/pathscope/` reverse proxy (`main.go:711`, `-pathscope-url`,
  default `http://127.0.0.1:9960`, `internal/proxy`); the separate `cmd/pathscope`
  binary + `internal/pathscope` (~1,650 lines); its data plumbing inside
  horstreporter: `dx_cellfeed.go` (387, writes `proplab_cell_buckets`),
  `sw_ingest.go` (378, `-proplab-sw-enable`, writes `proplab_sw_series`),
  `-proplab-cell-retention-days`, `internal/proplab` (~460 lines).
- **Why it looks unused:** the web UI link was removed in `102393d`
  ("remove obsolete Pathscope link from UI"); nothing in `static/` or horstapp
  references `/pathscope` any more. The cell feed and SW ingest exist only
  to feed pathscope (memory: *Proplab production flags*).
- **Who could still depend on it:** the operator opening `/pathscope/` directly;
  prod runs `-proplab-sw-enable` and `-proplab-cell-retention-days=35`; the
  `pathscope.service` unit on prod. Storage cost is real: per the *DB storage
  audit* note, `proplab_cell_buckets` plus its indexes are several GB on a 75 GB
  disk.
- **Check first:** whether `pathscope.service` is running and in use; access logs
  for `/pathscope/`.
- **Risk:** medium (separate service, DB tables to drop, two flags in prod ARGS).
- **Size:** ~2,900 Go lines across the five pieces, plus the `proplab_*` tables.

### C7. dxlens mount

- **What:** `dxlens_mount.go` (449) mounts the sibling module `../dxlens` at
  `/dxlens/` (`main.go:722`) via a `go.mod replace`.
- **Why it looks unused:** no link to `/dxlens/` anywhere in `static/` or in
  `static/` history; horstapp does not call it. The visual-design memory note
  records that a dxlens redesign was rejected.
- **Who could still depend on it:** the operator using `/dxlens/` by direct
  URL; the `dxlensRegionStatsLookbackDays` constant also sets the
  35-day `-dx-region-baseline-retention-days` reasoning (`main.go:280`), and the
  shared baseline engine (`dxBaseline`) must stay for `dx_conditions`.
- **Risk:** medium (build dependency on a sibling repo; removal also simplifies
  `go.mod` and worktree setup).
- **Size:** ~450 lines here + the whole `../dxlens` module.

### C8. `cmd/horstprop` + `internal/propcontract`

- **What:** the standalone link-scoring binary (~1,800 lines with
  `internal/propcontract`), its build/install/deploy scripts.
- **Why it looks unused:** its only UI consumer (Chase Queue scores via the
  `/horstprop/*` proxy) is gone, and the proxy route no longer exists in
  `main.go`. The *DX cluster + Chase Queue removed* note lists it as
  "orphaned on purpose".
- **Who could still depend on it:** whatever the operator runs locally against
  it; it reads `/api/stream` and is the sanctioned home of link scoring
  (CLAUDE.md "scoring boundary"), so removing it also removes that slot.
- **Risk:** low-medium (separate binary; no effect on horstreporter itself).
- **Size:** ~1,800 lines + tests + scripts.

### C9. QRZ enrichment

- **What:** `-qrz-username`/`-qrz-password` (+ `QRZ_*` env), `internal/qrzlookup`,
  wiring at `main.go:576-595`.
- **Why it looks unused:** it is constructed only when DX cluster, RBN or WSPR
  ingest is on. Its per-spot use is in `dxcluster.go` (spotter locator); with C1
  gone, the remaining use is `dxBaseline.SetResolvers` (callsign targets to
  region for the regional baseline, with a cty.dat fallback).
- **Who could still depend on it:** the regional baseline for callsign
  targets (`dx_conditions` with a callsign `qth`), if prod has QRZ credentials.
- **Check first:** whether prod sets `QRZ_USERNAME`; if not, it is dead in prod.
- **Risk:** low.
- **Size:** ~150 lines + ~25 lines of wiring.

### C10. `-wspr-climatology-file` JSONL fallback

- **What:** the JSONL file path for `wsprClimatology` when Postgres is not
  configured, also imported as the WSPR seed of `-prop-baseline-file` on first run.
- **Why it looks unused:** prod runs with Postgres; the seed import is a
  first-run migration that has already happened.
- **Who could still depend on it:** dev setups without Postgres. The engine
  itself must stay: it backs `/api/prop_intel` v1 (`prop_intel.go:243`), which
  horstapp uses.
- **Risk:** low, but the gain is small; only worth doing together with a broader
  "no-Postgres fallback" decision for `-dx-baseline-file` / `-prop-baseline-file`.
- **Size:** a few dozen lines of file I/O inside `wspr_climatology.go`.

---

## Keep

- **K1. `/api/prop_intel` (v1) and `/api/prop_intel/summary`:** not called by the
  web UI, but horstapp uses both (`/api/prop_intel/summary` in six places,
  including the home-screen widgets).
- **K2. `/api/dx_conditions`:** web UI (`band-lab.js`) and horstapp.
- **K3. `/api/stats`:** not called by the web UI, but used by
  `cmd/horstoperator-agent/diagnostics.go`, `scripts/load-api.mjs`,
  `scripts/measure-*.mjs`, and it carries the Postgres flush-health signal
  (memory *Replay raw-spots gap*). Only its `dxcluster_*` keys go with C1.
- **K4. RBN and WSPR ingests:** visible as the "RBN spots" / "WSPR spots"
  toggles (`index.html:237-242`) and in the Propagation matrix.
- **K5. Opmode / operator agent:** a real optional feature. Note:
  `static/opmode.js` exports `rigTune` and `operate`, and the agent serves
  `/v1/rig/tune` and `/v1/operate`, but no module imports those two exports
  (only `getRigBand` is used). That is unwired rig control within a kept feature,
  not a removal candidate.
- **K6. Web Push (`/api/push/*`, `-push-*`):** `static/push.js`, enabled on prod.
- **K7. `-pprof`, `-compress`, `-max-clients`, log flags:** operational, used on
  prod (`-pprof` is in prod ARGS per the *Perf pass* note).
- **K8. `-cty-path`:** still read (`main.go:415`, `loadCtyResolver`) for the
  regional baseline's DXCC fallback. **Mismatch found:** `deploy.sh` lists
  `cty-path` in `REMOVED_FLAGS` (`78831cb`, which says `89f5fb5` dropped it), but
  `89f5fb5` only dropped the ingest-side cty plumbing; the flag still exists. The
  strip is harmless (the binary falls back to the embedded `cty.dat`), but it
  silently discards a prod override if one was set. Either remove `cty-path`
  from `REMOVED_FLAGS` or actually retire the flag.
- **K9. Stale browser keys `showChaseQueue`, `cqModes`, `cqStars`, `cqSortKey`,
  `cqSortAsc`:** left on purpose (stars are user data); nothing to remove in code.

## Before removing anything

1. Add every removed flag name to `REMOVED_FLAGS` in `deploy.sh` in the same
   commit, or prod crash-loops on the unknown flag.
2. Read prod `ARGS` in `/etc/default/horstreporter` and check `dx_meta` (C3, C4)
   before deciding the "check first" entries.
3. Re-run the coverage gate (`npm run test:go:coverage`, `npm run check`):
   removing well-tested code like `dxcluster_test.go` can move package coverage
   relative to `.coverage-baseline.json`.
