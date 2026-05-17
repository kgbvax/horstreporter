# HorstReporter AI Agent Instructions

## Project snapshot
- HorstReporter is a **single Go binary** that consumes PSK Reporter MQTT data, filters spots, and serves a static browser UI.
- Backend entry point: `main.go`.
- Frontend entry point: `static/app.js`.
- Frontend stack: plain ES modules + static assets (Leaflet/Bootstrap/Turf), **no React/Vue build pipeline**.

## Architecture boundaries
- Backend core files:
  - `main.go` (flags, server wiring, static serving, TLS)
  - `mqtt.go` (MQTT ingest + topic/message parsing)
  - `hub.go` (in-memory client hub + spot history)
  - `spot.go` (spot model + matching/locator utilities)
  - `server.go` (HTTP handlers)
- API endpoints:
  - `/api/stream` (SSE)
  - `/api/stats` (aggregated server stats)

## Quick commands agents should use
- Backend tests: `go test ./...`
- Frontend tests: `npm test` (runs `vitest run`)
- Dev run: `go run . -dev -port 8080`

## Editing guidance
- Backend logic changes should generally happen in `main.go`, `mqtt.go`, `hub.go`, `spot.go`, `server.go`.
- Frontend behavior/UI changes should generally happen in `static/*.js`, `static/index.html`, `static/style.css`.
- Prefer extending existing modules (`static/state.js`, `static/map.js`, `static/renderers.js`, `static/ui.js`) over creating new architecture layers.

## Repo-specific guardrails
- In production mode, static files are embedded via `//go:embed static`; in `-dev` mode they are served from disk.
  - If frontend changes do not appear, verify whether the app is running in `-dev` or from a previously built binary.
- Keep this as a single-service design. Do not introduce microservice assumptions.
- Do not add a frontend toolchain unless the task explicitly requires one.
- Avoid editing `static/vendor/` unless absolutely necessary (third-party code).
- Treat `hub.history`/client handling changes carefully; these affect SSE fan-out behavior across all clients.

## Testing expectations
- For backend edits, run `go test ./...`.
- For frontend edits, run `npm test`.
- Prefer small, focused changes and verify behavior at API boundaries (`/api/stream`, `/api/stats`) when relevant.

## Canonical references (link, don’t duplicate)
- Feature/architecture details: [`GEMINI.md`](./GEMINI.md)
- User-facing behavior/content: [`static/info.md`](./static/info.md)

## What to avoid
- Don’t refactor vendor libraries in `static/vendor/` as part of unrelated tasks.
- Don’t assume framework conventions from Gin/Echo/React/Vite; this repo is intentionally minimal and custom.
