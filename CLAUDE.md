# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run backend
go run . -dev -port 8080

# Run with DX cluster ingest. Most clusters (e.g. DB0ERF) require a registered
# callsign + individually-assigned password — pass via env so secrets stay out of
# the shell history / process args (flags -dxcluster-username/-password also work).
DXCLUSTER_USERNAME=<yourcall> DXCLUSTER_PASSWORD=<password> \
  go run . -dev -port 8080 -dxcluster-enable -dxcluster-endpoint db0erf.de:7300
# Optional: QRZ_USERNAME/QRZ_PASSWORD enable callsign→locator enrichment (dx_locator),
# shared by DX-cluster and RBN ingests.
# Optional RBN (Reverse Beacon Network) CW/RTTY telnet ingest — public relay, no password,
# but it prompts for a callsign before streaming (set -rbn-callsign / RBN_CALLSIGN). Fills the
# activity chart/live stream during CW/SSB contests when FT8 thins out. Minimal scope:
# activity + live only, kept out of the FT8-SNR conditions baseline (rbn.go).
#   go run . -dev -port 8080 -rbn-enable -rbn-callsign <yourcall>
# Optional WSPR ingest — wspr.live ClickHouse HTTP poller (reference-only, like RBN):
#   go run . -dev -port 8080 -wspr-enable

# Run local operator agent
go run ./cmd/horstoperator-agent -listen 127.0.0.1:9955 -station-locator JO62qm
# Rig control + Chase Queue enrichment (operator-local): add
#   -rig-transport waveloggate   (tune via WaveLogGate)
# and set Wavelog creds via env / a repo-root .env (NOT flags — keep secrets out of argv):
#   WAVELOG_API_KEY=… WAVELOG_URL=https://log.dclnext.darc.de/index.php
# Secrets convention for both binaries (env vars / systemd EnvironmentFile): see docs/deployment-secrets.md

# Run HF link-quality scoring service (separate binary; consumes HorstReporter read-only)
go run ./cmd/horstprop -listen 127.0.0.1:9970

# Award progress (Chase Queue "wanted": DXCC/WAS/POTA) runs IN-PROCESS in the
# operator agent (internal/awards) so the operator's log stays local. Enable it by
# configuring Wavelog for the agent: WAVELOG_API_KEY (.env) + WAVELOG_STATION_ID
# (required for DCLNext), and optionally POTA_HUNTED_CSV (hunted-parks export).
#   WAVELOG_STATION_ID=3427 POTA_HUNTED_CSV=hunted.csv ./run_operator_agent.sh
# Spec: docs/horstawards.md
# Then point the agent at it so the Chase Queue "wanted" badges gain WAS/POTA:
#   go run ./cmd/horstoperator-agent ... -horstawards-url http://127.0.0.1:9956
# Spec: docs/horstawards.md

# Backend tests
go test ./...

# Frontend tests
npm test

# Frontend typecheck + tests
npm run check

# Cell bucket feed (path-scope data plumbing; Propagation Lab was removed 2026-08-04)
#   -proplab-cell-retention-days  # retention for proplab_cell_buckets / proplab_sw_series (default 60; 0 disables)
#   -proplab-sw-enable            # NOAA SWPC index series ingest (kp/F10.7/xray/OVATION; consumed by pathscope)

# Mercator perf gate (catch draw/zoom regressions)
npm run perf:gate:mercator
```

## Architecture

Single Go binary + plain-ES-modules frontend (no React/Vue build pipeline).

**Backend core files:**
- `main.go` — flags, server wiring, static serving, TLS, history pruning
- `mqtt.go` — MQTT ingest (`pskr/filter/v2/#`), topic parsing, FT8/FT4 mode filtering
- `hub.go` — in-memory client hub + rolling spot history (fan-out to SSE clients)
- `spot.go` — spot model, matching/locator utilities
- `server.go` — HTTP handlers
- `dx_conditions.go` — DX baseline scoring engine
- `dx_postgres.go` — Postgres persistence for raw spots and baseline
- `dxcluster.go` — optional DX cluster TCP ingest
- `rbn.go` — optional RBN (Reverse Beacon Network) CW/RTTY raw telnet ingest; `source_type='rbn'`, activity + live only (kept out of the FT8-SNR baseline)
- `wspr.go` — optional WSPR ingest (wspr.live ClickHouse HTTP poller); `source_type='wspr'`, reference-only (kept out of the FT8-SNR baseline, like RBN)
- `opmode.go` — operator mode endpoint wiring (browser calls local agent directly; backend never proxies)
- `dxlens_mount.go` — mounts the `dxlens` sibling module at `/dxlens/`
- `dx_cellfeed.go` — `CellBucketFeed`: midpoint-cell bucket accumulator + 60s persistence tick + retention pruning (writes `proplab_cell_buckets` for pathscope; all that remains of the removed Propagation Lab)
- `internal/proplab/` — primitives for that feed: `buckets.go` (`CellBucketEngine`), plus `geo.go`, `band.go`, `time.go`, `spot.go`, `region.go`, `stats.go`, `dx_midpoint.go`, `lane.go`, `types.go` (engines were deleted; name kept for stability)
- `sw_ingest.go` — optional NOAA SWPC / OVATION polling into `proplab_sw_series` (consumed by pathscope)
- `cmd/horstoperator-agent/` — standalone local agent bridging browser opmode to PSTrotator UDP; resolves Wavelog attributes for the Chase Queue and computes award "wanted" in-process via `internal/awards` (operator log stays local)
- `cmd/horstprop/` — standalone HF link-quality scoring service (separate binary; consumes HorstReporter read-only over HTTP; see `docs/horstprop.md`)
- `internal/awards/` — local award-progress engine embedded in the agent: slot index (DXCC/WAS/POTA) from the operator's Wavelog log + POTA hunted-parks CSV; `Manager` + `award`/`adif`/`refdata`/`source`/`store` (see `docs/horstawards.md`)
- `internal/propcontract/` — score contract types shared between the backend and `cmd/horstprop`
- `internal/awardcontract/` — award `WantedSpot`/`WantedResult` types shared between the agent and `internal/awards`

**Frontend core files (`static/`):**
- `app.js` — app boot, SSE stream lifecycle, projection/style gating
- `renderers.js` — Mercator map rendering modes
- `azimuth-runtime.js` — Azimuthal (canvas) rendering
- `map.js` — Leaflet map setup + overlays
- `ui.js` — UI helpers and control wiring
- `state.js` — shared frontend state

**Key architectural constraints:**
- `hub.history` is the in-memory rolling window; changes affect all SSE client fan-out and history dumps
- Production embeds `static/` via `//go:embed static`; `-dev` serves from disk with no-cache
- The `dxlens` module is a sibling directory (`../dxlens`), referenced via `go.mod replace`
- Browser opmode calls `http://127.0.0.1:9955/v1/*` directly; the backend never proxies to the local agent

## API endpoints

Canonical reference: `docs/api.md` (includes response shapes, caches, and explicit exclusions).

- `GET /api/stream` — SSE; params: `qth`, `minutes` (default 15, max 60), `surroundings`, `rings` (configurable "area of interest": with a locator `qth`, matches any sender/receiver within `rings` grid-squares; capped at 30; used by horstprop's region feed)
- `GET /api/dx_conditions` — DX score/conditions per band; params: `qth`, `minutes`, `surroundings`, `cw_min_db`
- `GET /api/prop_intel` — WSPR propagation-intelligence nowcast per (band × region); params: `qth`, `minutes`, `surroundings`, `cw_min_db`, `surge_threshold`, `from_here`. Sibling summary endpoint `GET /api/prop_intel/summary` (60s cached) feeds the mobile app's home-screen widgets
- `GET /api/stats` — active connections, history size/minutes
- `GET /api/capture_snapshot` — deterministic filtered spot snapshot for server-driven frame capture
- `GET /dxlens/` — DXLens module UI (reads HorstReporter's in-memory baseline via adapter)

## Related repositories

- `../horstapp` — sibling Flutter mobile app (iOS primary + Android) consuming this
  backend read-only over HTTPS: propagation-intelligence overview (band × region
  matrix, band detail from `/api/dx_conditions`) plus native iOS/Android home-screen
  widgets fed by `/api/prop_intel/summary`. Same sibling pattern as `../dxlens`.
  Visual language is ported from `static/wspr-matrix.js` — when changing the chip
  ramps, ink flip, or badge glyphs there, mirror the change in the app's
  `lib/core/theme/matrix_style.dart` and the widget's Swift/Kotlin constants.

## What to avoid

- Don't modify anything under `static/vendor/`
- Don't split the *core* backend into microservices; it is intentionally single-service/single-binary. (Separate operator-side binaries like `cmd/horstoperator-agent` and `cmd/horstprop` that consume the backend/log read-only over HTTP are the sanctioned pattern — they don't grow the core binary.)
- Keep the scoring boundary: per-spot/path **link** scoring lives only in `cmd/horstprop` (consumed by the Chase Queue via `/horstprop/v1/score`). horstreporter owns the shared, multi-station band/region **conditions** analytics (`dx_conditions.go`, `hot_bands.go`, dxlens) backed by the Postgres baseline. Don't add per-spot/path scoring to horstreporter.
- Keep the awards boundary: the operator's personal **award progress** ("wanted") lives only in the local operator agent (`internal/awards`, merged into the enrich `needed[]`). The operator's log/award data must stay local — never pulled to the shared core/server.
- Don't assume Gin/Echo/React/Vite conventions

<!-- rtk-instructions v2 -->
# RTK (Rust Token Killer) - Token-Optimized Commands

## Golden Rule

**Always prefix commands with `rtk`**. If RTK has a dedicated filter, it uses it. If not, it passes through unchanged. This means RTK is always safe to use.

**Important**: Even in command chains with `&&`, use `rtk`:
```bash
# ❌ Wrong
git add . && git commit -m "msg" && git push

# ✅ Correct
rtk git add . && rtk git commit -m "msg" && rtk git push
```

## RTK Commands by Workflow

### Build & Compile (80-90% savings)
```bash
rtk cargo build         # Cargo build output
rtk cargo check         # Cargo check output
rtk cargo clippy        # Clippy warnings grouped by file (80%)
rtk tsc                 # TypeScript errors grouped by file/code (83%)
rtk lint                # ESLint/Biome violations grouped (84%)
rtk prettier --check    # Files needing format only (70%)
rtk next build          # Next.js build with route metrics (87%)
```

### Test (60-99% savings)
```bash
rtk cargo test          # Cargo test failures only (90%)
rtk go test             # Go test failures only (90%)
rtk jest                # Jest failures only (99.5%)
rtk vitest              # Vitest failures only (99.5%)
rtk playwright test     # Playwright failures only (94%)
rtk pytest              # Python test failures only (90%)
rtk rake test           # Ruby test failures only (90%)
rtk rspec               # RSpec test failures only (60%)
rtk test <cmd>          # Generic test wrapper - failures only
```

### Git (59-80% savings)
```bash
rtk git status          # Compact status
rtk git log             # Compact log (works with all git flags)
rtk git diff            # Compact diff (80%)
rtk git show            # Compact show (80%)
rtk git add             # Ultra-compact confirmations (59%)
rtk git commit          # Ultra-compact confirmations (59%)
rtk git push            # Ultra-compact confirmations
rtk git pull            # Ultra-compact confirmations
rtk git branch          # Compact branch list
rtk git fetch           # Compact fetch
rtk git stash           # Compact stash
rtk git worktree        # Compact worktree
```

Note: Git passthrough works for ALL subcommands, even those not explicitly listed.

### GitHub (26-87% savings)
```bash
rtk gh pr view <num>    # Compact PR view (87%)
rtk gh pr checks        # Compact PR checks (79%)
rtk gh run list         # Compact workflow runs (82%)
rtk gh issue list       # Compact issue list (80%)
rtk gh api              # Compact API responses (26%)
```

### JavaScript/TypeScript Tooling (70-90% savings)
```bash
rtk pnpm list           # Compact dependency tree (70%)
rtk pnpm outdated       # Compact outdated packages (80%)
rtk pnpm install        # Compact install output (90%)
rtk npm run <script>    # Compact npm script output
rtk npx <cmd>           # Compact npx command output
rtk prisma              # Prisma without ASCII art (88%)
rtk uv run <cmd>        # Compact uv project command output
```

### Files & Search (60-75% savings)
```bash
rtk ls <path>           # Tree format, compact (65%)
rtk read <file>         # Code reading with filtering (60%)
rtk grep <pattern>      # Search grouped by file (75%). Format flags (-c, -l, -L, -o, -Z) run raw.
rtk find <pattern>      # Find grouped by directory (70%)
```

### Analysis & Debug (70-90% savings)
```bash
rtk err <cmd>           # Filter errors only from any command
rtk log <file>          # Deduplicated logs with counts
rtk json <file>         # JSON structure without values
rtk deps                # Dependency overview
rtk env                 # Environment variables compact
rtk summary <cmd>       # Smart summary of command output
rtk diff                # Ultra-compact diffs
```

### Infrastructure (85% savings)
```bash
rtk docker ps           # Compact container list
rtk docker images       # Compact image list
rtk docker logs <c>     # Deduplicated logs
rtk kubectl get         # Compact resource list
rtk kubectl logs        # Deduplicated pod logs
```

### Network (65-70% savings)
```bash
rtk curl <url>          # Compact HTTP responses (70%)
rtk wget <url>          # Compact download output (65%)
```

### Meta Commands
```bash
rtk gain                # View token savings statistics
rtk gain --history      # View command history with savings
rtk discover            # Analyze Claude Code sessions for missed RTK usage
rtk proxy <cmd>         # Run command without filtering (for debugging)
rtk init                # Add RTK instructions to CLAUDE.md
rtk init --global       # Add RTK to ~/.claude/CLAUDE.md
```

## Token Savings Overview

| Category | Commands | Typical Savings |
|----------|----------|-----------------|
| Tests | vitest, playwright, cargo test | 90-99% |
| Build | next, tsc, lint, prettier | 70-87% |
| Git | status, log, diff, add, commit | 59-80% |
| GitHub | gh pr, gh run, gh issue | 26-87% |
| Package Managers | pnpm, npm, npx | 70-90% |
| Files | ls, read, grep, find | 60-75% |
| Infrastructure | docker, kubectl | 85% |
| Network | curl, wget | 65-70% |

Overall average: **60-90% token reduction** on common development operations.
<!-- /rtk-instructions -->