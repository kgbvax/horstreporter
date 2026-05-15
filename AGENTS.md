# HorstReporter AI Agent Instructions

## What this project is
- A Go web server that connects to the PSK Reporter MQTT stream, filters HF reception reports (spots), and serves a static browser frontend.
- The frontend lives in `static/` and uses Leaflet, Bootstrap, and Turf for map-based visualization.
- Key backend entry point: `main.go`.
- Key frontend entry point: `static/app.js` (plus `static/ui.js`, `static/state.js`, `static/renderer.js`, and other UI/logic files).

## What agents should know
- `main.go` embeds `static/` for production builds, but `--dev` mode serves `static/` from disk.
- The backend exposes at least two API endpoints:
  - `/api/stream` for SSE spot events
  - `/api/stats` for aggregated statistics
- The codebase is not a typical Go web framework app; it is a small custom server with static assets and MQTT handling.
- The frontend is plain ES modules and static assets; there is no React/Vue build pipeline.

## Recommended commands
- Run backend tests: `go test ./...`
- Run frontend tests: `npm test`
- The `package.json` frontend test script is `vitest run`.

## Editing guidance
- Prefer changes in `main.go`, `mqtt.go`, `hub.go`, `spot.go`, `server.go` for backend logic.
- Prefer changes in `static/*.js`, `static/index.html`, `static/style.css` for frontend behavior and UI.
- Avoid editing `static/vendor/` files unless absolutely necessary; these are third-party libraries.

## Useful docs and references
- Use `GEMINI.md` for feature and architecture context.
- There is no README in the repo root, so rely on code structure and `GEMINI.md` for guidance.

## What to avoid
- Do not add a new frontend build toolchain unless the change clearly requires it.
- Do not refactor vendor libraries in `static/vendor/`.
- Do not assume this is a multi-service microservice architecture; it is a single Go binary plus static site.
