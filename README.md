# HorstReporter

HorstReporter is a single Go binary that ingests PSK Reporter MQTT data, computes live HF propagation conditions, and serves a browser UI.

This repository now includes a requirements-aligned DX conditions engine and Home Assistant integration via MQTT discovery.

> Note: Home Assistant MQTT publishing has been removed in this branch because the target HA deployment is firewalled from MQTT ingress.

## What is implemented

### Core DX analytics

- Real-time scoring per band
- Baseline-aware confidence and status
- Status classes: `green | yellow | red | grey`
- Recommendations:
  - `best_bands`
  - `recommended_bands`
  - `worst_bands`
  - `avoid_bands`
- Trend direction + sparkline
- Mode feasibility: `ssb | cw | digital | none`
- Direction output: dominant azimuth sector (`N`, `NE`, ...)

### Home Assistant integration status

- Direct Home Assistant MQTT publishing is currently **disabled/removed**.
- DX analytics remain available through `GET /api/dx_conditions` and can be consumed by external adapters or polling integrations.

### Propagation Lab (A/B/C experiments)

A standalone `/proplab/` page compares three ways of deciding which bands and regions are active:

- **A / Baseline** — the current production `dx_conditions` engine.
- **B / Ladder** — within-data physics: midpoint-cell MUF ladder, band-coherence, CUSUM onsets, terminator hints.
- **C / Fusion** — conditional-quantile baseline with event/SW fusion.

Run with the lab enabled (default):

```bash
go run . -dev -port 8080
```

Then open `http://localhost:8080/proplab/`. Each variant has its own parameter panel; overrides are sent as query parameters. Optional ingest flags for Fusion context:

- `-proplab-sw-enable` — space-weather ingest (NOAA SWPC feeds; placeholder poller)
- `-proplab-events-enable` — contest/DXpedition/POTA calendar ingest (placeholder poller)
- `-proplab-cell-retention-days` — PG retention for proplab tables (default `60`, `0` disables)
- `-proplab-disable` — turn the lab engines off entirely

Backend endpoints:

- `GET /api/proplab/v1/params` — default B/C parameters
- `GET /api/proplab/v1/ladder?target=JO62qm&...` — variant B verdict
- `GET /api/proplab/v1/fusion?...` — variant C verdict

## Run locally

### Development mode

Use dev mode when iterating on static UI assets:

```bash
go run . -dev -port 8080
```

### Test suites

```bash
go test ./...
npm test
```

### Optional DX cluster ingest

DX cluster ingest is disabled by default. Enable it explicitly and point it to a cluster endpoint:

```bash
go run . -dev -port 8080 -dxcluster-enable -dxcluster-endpoint db0erf.de:7300
```

Available flags:

- `-dxcluster-enable` enable optional DX cluster ingest (default: `false`)
- `-dxcluster-endpoint` cluster endpoint in `host:port` (default: `db0erf.de:7300`)
- `-dxcluster-reconnect-seconds` reconnect delay after disconnect (default: `15`)
- `-dxcluster-verbose` emit detailed DX cluster connection lifecycle logs (default: `false`)
- `-dxcluster-username` callsign sent when connecting to DX cluster
- `-dxcluster-password` optional password sent when connecting to DX cluster
- `-qrz-username` QRZ username for callsign→locator enrichment
- `-qrz-password` QRZ password for callsign→locator enrichment

QRZ credentials can also be provided via environment variables (preferred):

- `QRZ_USERNAME`
- `QRZ_PASSWORD`

DX cluster credentials can also be provided via environment variables:

- `DXCLUSTER_USERNAME`
- `DXCLUSTER_PASSWORD`

When DX cluster ingest is enabled:

- all parsed cluster spots are persisted to Postgres `dx_raw_spots` with `source_type=dxcluster`
- only spots with usable locators are forwarded into live SSE/map flow

### Local PSTrotator operator agent (opmode)

This repo now includes a local operator agent at `cmd/horstoperator-agent`.
It exposes `/v1/*` endpoints consumed directly by the browser opmode runtime and bridges to PSTrotator via UDP.

Run the local agent (example):

```bash
go run ./cmd/horstoperator-agent \
  -listen 127.0.0.1:9955 \
  -backend-url https://horstreporter.kgbvax.net \
  -station-name DK3JF \
  -station-locator JO62qm \
  -pst-host 127.0.0.1 \
  -pst-port 12000
```

Then run HorstReporter (backend proxy is intentionally disabled by design):

```bash
go run . -dev -port 8080 \
  -opmode-enable \
  -opmode-control-enable
```

Important: the backend never contacts the local agent. Browser opmode calls `http://127.0.0.1:9955/v1/*` (or `http://localhost:9955/v1/*`) directly.

When `-backend-url` is set, the local agent also reverse-proxies non-`/v1/*` requests to HorstReporter backend. This allows using the local agent as a single browser origin (open `http://127.0.0.1:9955/`) while keeping opmode local/direct.

Agent flags (core):

- `-listen` HTTP listen address for local agent (default: `127.0.0.1:9955`)
- `-backend-url` optional HorstReporter backend base URL used for reverse proxy of non-`/v1/*` routes
- `-station-name` station display name
- `-station-locator` Maidenhead locator (**required**; station position is derived from it)
- `-control-permitted` enable/disable rotate+mode commands
- `-beamwidth-3db-deg` reported antenna beamwidth for UI overlays

PSTrotator UDP flags:

- `-pst-host` PSTrotator host (supports localhost or LAN host)
- `-pst-port` PSTrotator UDP port
- `-pst-timeout-ms` UDP timeout

The agent now uses a built-in PSTrotator command profile (no user regex/command templates required):

- azimuth query: `<PST>AZ?</PST>`
- mode query: `<PST>MODE?</PST>`
- rotate command: `<PST><TRACK>0</TRACK><AZIMUTH><deg></AZIMUTH></PST>`
- mode command mapping:
  - `forward`/`backward` → `<PST><TRACK>0</TRACK></PST>`
  - `bidirectional` → `<PST><TRACK>1</TRACK></PST>`
- command terminator: `CR` (`\r`)
- response receive behavior: listens for replies on `pst-port + 1` (per manual)

Optional UDP diagnostics flags:

- `-pst-log-traffic` enable UDP TX/RX logging
- `-pst-log-traffic-hex` include a truncated hex dump in UDP logs
- `-pst-log-max-bytes` max bytes shown in UDP payload previews (default: `256`)

### Mercator performance gate (local, provider-agnostic)

Use the local perf gate to catch draw/zoom/pan regressions before merging:

```bash
npm run perf:gate:mercator
```

What it does:

- runs Mercator perf tests with report output enabled
- writes scenario reports to `tmp/perf-reports/`
- compares measured metrics against `.perf-baseline.json`
- fails if any threshold is exceeded

Useful sub-commands:

```bash
npm run test:perf:mercator:report
npm run test:perf:mercator:assert
```

### Server-side DK3JF capture experiment (snapshot-based)

This branch now includes a first implementation slice for server-driven movie capture:

- `GET /api/capture_snapshot` returns a deterministic filtered spot snapshot for a target and timestamp.
- Frontend can be booted in capture mode via URL query params (`capture=1`) and exposes readiness via `window.__horstCaptureReady`.
- Scripts:
  - `npm run capture:dk3jf:frames -- --base-url http://127.0.0.1:8080 --target JO32 --duration-minutes 360 --step-seconds 60 --projection mercator --style active-area --min-snr-mode ssb`
  - `npm run capture:dk3jf:movie -- tmp/dk3jf-capture/<run-id> tmp/dk3jf-capture/<run-id>/dk3jf.mp4 24`

Notes:

- Frame capture requires Playwright (`npm i`) and a running HorstReporter server.
- Movie assembly requires `ffmpeg` on the host.
- This is an experiment path and does not alter regular live SSE behavior.

## Home Assistant usage (firewalled setup)

Because MQTT publish is removed, use the HTTP endpoint as integration source:

- `GET /api/dx_conditions?target=<CALL_OR_GRID>&minutes=<N>&surroundings=true|false`

This response includes status, score, confidence, mode, direction, and recommendation fields per band.

## Verification checklist (Phase 4)

1. Start app and query `GET /api/dx_conditions` for your target.
2. Confirm low-data cases emit:
   - `status = grey`
   - `mode = none`
3. Confirm frontend still renders DX panel values and trend/sparkline.

## Notes

- In production mode static assets are embedded (`//go:embed static`).
- In `-dev` mode static files are served from disk.
- Current architecture is intentionally single-service and single-binary.
