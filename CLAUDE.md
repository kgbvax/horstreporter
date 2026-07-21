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
- `opmode.go` — operator mode endpoint wiring (browser calls local agent directly; backend never proxies)
- `dxlens_mount.go` — mounts the `dxlens` sibling module at `/dxlens/`
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

- `GET /api/stream` — SSE; params: `target`, `minutes` (default 15, max 60), `surroundings`, `rings` (configurable "area of interest": with a locator `target`, matches any sender/receiver within `rings` grid-squares; capped at 30; used by horstprop's region feed)
- `GET /api/dx_conditions` — DX score/conditions per band; params: `target`, `minutes`, `surroundings`, `cw_min_db`
- `GET /api/stats` — active connections, history size/minutes
- `GET /api/capture_snapshot` — deterministic filtered spot snapshot for server-driven frame capture
- `GET /dxlens/` — DXLens module UI (reads HorstReporter's in-memory baseline via adapter)

## What to avoid

- Don't modify anything under `static/vendor/`
- Don't split the *core* backend into microservices; it is intentionally single-service/single-binary. (Separate operator-side binaries like `cmd/horstoperator-agent` and `cmd/horstprop` that consume the backend/log read-only over HTTP are the sanctioned pattern — they don't grow the core binary.)
- Keep the scoring boundary: per-spot/path **link** scoring lives only in `cmd/horstprop` (consumed by the Chase Queue via `/horstprop/v1/score`). horstreporter owns the shared, multi-station band/region **conditions** analytics (`dx_conditions.go`, `hot_bands.go`, `dxpulse.go`, dxlens) backed by the Postgres baseline. Don't add per-spot/path scoring to horstreporter.
- Keep the awards boundary: the operator's personal **award progress** ("wanted") lives only in the local operator agent (`internal/awards`, merged into the enrich `needed[]`). The operator's log/award data must stay local — never pulled to the shared core/server.
- Don't assume Gin/Echo/React/Vite conventions
