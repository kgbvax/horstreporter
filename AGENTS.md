# HorstReporter AI Agent Instructions

## Project snapshot
- HorstReporter is a **single Go binary** that ingests PSK Reporter MQTT traffic, filters spots for a target, and serves a static browser UI.
- Backend entry point: `main.go`.
- Frontend entry point: `static/app.js`.
- Frontend stack: plain ES modules + static assets (Leaflet/Bootstrap/Turf), **no React/Vue build pipeline**.

## Current core behavior
- MQTT ingest subscribes to `pskr/filter/v2/#`, reconstructs omitted fields from topic segments, and currently keeps **FT8/FT4** reports.
- Streaming API pushes filtered spots over SSE.
- History is retained in-memory (rolling window, pruned periodically) and used both for initial stream backfill and stats.
- Optional DX baseline engine tracks historical band conditions and serves current DX potential scoring.

## Architecture boundaries
- Backend core files:
  - `main.go` (flags, server wiring, static serving, TLS, pruning/save loops)
  - `mqtt.go` (MQTT ingest + topic/message parsing + mode filtering)
  - `hub.go` (in-memory client hub + spot history)
  - `spot.go` (spot model + matching/locator utilities)
  - `server.go` (HTTP handlers)
  - `dx_conditions.go` (DX baseline + scoring engine)
- Frontend core files:
  - `static/app.js` (app boot, stream lifecycle, projection/style gating, options wiring)
  - `static/renderers.js` (Mercator map rendering modes)
  - `static/azimuth-runtime.js` (Azimuthal rendering runtime)
  - `static/map.js` (Leaflet map setup + overlays)
  - `static/ui.js` (UI helpers and control wiring)
  - `static/dx-conditions.js` (DX panel polling/rendering)

## API endpoints and important behavior
- `/api/stream` (SSE)
  - Primary query param: `target`.
  - Backward compatibility: falls back to `callsign`/`locator` if `target` is missing.
  - `minutes`: defaults to 15, capped at 60.
  - `surroundings=true`: expands target to neighboring squares **only when target is a valid locator**.
  - At max client pressure, returns SSE event `server_error` with human-readable message.
- `/api/stats` (JSON)
  - Returns `active_connections`, `history_size`, `history_minutes`, `history_size_kb`.
- `/api/dx_conditions` (JSON)
  - Accepts `target`, `minutes`, `surroundings`, `cw_min_db`.
  - Returns current DX score/condition with confidence, best bands, per-band details, and baseline depth metadata.

## Frontend visualization modes
- Projection modes:
  - **Mercator** (Leaflet)
  - **Azimuthal** (canvas runtime)
- Style modes:
  - `grid-snr`, `heatmap`, `active-area` are available in Mercator.
  - In Azimuthal mode, style is normalized to grid-style rendering; heatmap/active-area are disabled in UI and runtime.

## Runtime and ops notes
- Static file serving:
  - Production mode uses embedded files via `//go:embed static`.
  - `-dev` serves from disk with no-cache middleware.
  - If frontend changes do not appear, verify `-dev` and whether a stale prebuilt binary is running.
- Main flags commonly used:
  - `-port`, `-dev`, `-compress`, `-max-clients`
  - `-record-spots` (JSONL recorder)
  - `-dx-baseline-file` (persistent DX baseline buckets)
  - TLS: `-cert`/`-key` or `-domain` (Let's Encrypt)

## Quick commands agents should use
- Backend tests: `go test ./...`
- Frontend tests: `npm test` (runs `vitest run`)
- Dev run: `go run . -dev -port 8080`

## Editing guidance
- Backend logic changes should generally happen in `main.go`, `mqtt.go`, `hub.go`, `spot.go`, `server.go`, `dx_conditions.go`.
- Frontend behavior/UI changes should generally happen in `static/*.js`, `static/index.html`, `static/style.css`.
- Prefer extending existing modules (`static/state.js`, `static/map.js`, `static/renderers.js`, `static/ui.js`) over introducing new architecture layers.
- Treat `hub.history` and client lifecycle changes carefully; these impact fan-out and initial history dumps for all SSE clients.

## Testing expectations
- For backend edits, run `go test ./...`.
- For frontend edits, run `npm test`.
- Prefer small, focused changes and verify behavior at API boundaries (`/api/stream`, `/api/stats`, `/api/dx_conditions`) when relevant.

## Canonical references
- User-facing behavior/content: [`static/info.md`](./static/info.md)
- DX scoring explanation: [`static/dxscore.md`](./static/dxscore.md)

## What to avoid
- Don’t refactor third-party libraries under `static/vendor/` unless absolutely necessary.
- Don’t assume framework conventions from Gin/Echo/React/Vite; this repo is intentionally minimal and custom.
- Don’t introduce microservice assumptions; keep this as a single-service design.
