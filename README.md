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
