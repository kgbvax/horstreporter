# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run backend
go run . -dev -port 8080

# Run with DX cluster ingest
go run . -dev -port 8080 -dxcluster-enable -dxcluster-endpoint db0erf.de:7300

# Run local operator agent
go run ./cmd/horstoperator-agent -listen 127.0.0.1:9955 -station-lat 52.52 -station-lng 13.40

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
- `opmode.go` — operator mode endpoint wiring (browser calls local agent directly; backend never proxies)
- `dxlens_mount.go` — mounts the `dxlens` sibling module at `/dxlens/`
- `cmd/horstoperator-agent/` — standalone local agent bridging browser opmode to PSTrotator UDP

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

- `GET /api/stream` — SSE; params: `target`, `minutes` (default 15, max 60), `surroundings`
- `GET /api/dx_conditions` — DX score/conditions per band; params: `target`, `minutes`, `surroundings`, `cw_min_db`
- `GET /api/stats` — active connections, history size/minutes
- `GET /api/capture_snapshot` — deterministic filtered spot snapshot for server-driven frame capture
- `GET /dxlens/` — DXLens module UI (reads HorstReporter's in-memory baseline via adapter)

## What to avoid

- Don't modify anything under `static/vendor/`
- Don't introduce microservice splits; this is intentionally single-service/single-binary
- Don't assume Gin/Echo/React/Vite conventions
