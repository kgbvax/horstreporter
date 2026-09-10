# HorstReporter

HorstReporter is a single Go binary with a plain-ES-modules frontend (no React/Vue
build pipeline). It ingests PSK Reporter MQTT data (`pskr/filter/v2/#`), computes
live HF propagation conditions from an FT8-SNR baseline, and serves a browser UI
plus SSE streams. Optional ingests add DX cluster, RBN, and WSPR data.

The architecture is intentionally single-service/single-binary. Separate
operator-side binaries (`cmd/horstoperator-agent`, `cmd/horstprop`,
`cmd/pathscope`) consume the backend or the operator's log read-only — that is
the sanctioned extension pattern, not a core split.

![HorstReporter live map: FT8 spots per band around JO32](docs/screenshot.png)

## Core DX analytics

- Real-time scoring per band, baseline-aware confidence and status
- Status classes: `green | yellow | red | grey`
- Recommendations: `best_bands`, `recommended_bands`, `worst_bands`, `avoid_bands`
- Trend direction + sparkline
- Mode feasibility: `ssb | cw | digital | none`
- Direction output: dominant azimuth sector (`N`, `NE`, ...)
- Propagation intelligence: `GET /api/prop_intel` (WSPR nowcast per band × region,
  always from your QTH) plus a 60s-cached `/api/prop_intel/summary` that feeds the
  horstapp mobile widgets

Canonical endpoint reference: [docs/api.md](docs/api.md) (response shapes, caches,
exclusions). Scoring method: [docs/scoring-method.md](docs/scoring-method.md).

## Run locally

```bash
# Backend
go run . -dev -port 8080

# Tests
go test ./...
npm test          # frontend
npm run check     # frontend typecheck + tests

# Mercator perf gate (catches draw/zoom regressions)
npm run perf:gate:mercator
```

In production mode static assets are embedded (`//go:embed static`); `-dev`
serves them from disk with no-cache.

## Optional ingests

All optional ingests are disabled by default and enabled explicitly via flags.
Secrets go through env vars or a systemd EnvironmentFile, never argv — see
[docs/deployment-secrets.md](docs/deployment-secrets.md).

### DX cluster (TCP)

Most clusters (e.g. DB0ERF, a DXSpider node) require a registered callsign and an
individually-assigned password. Login is prompt-aware.

```bash
DXCLUSTER_USERNAME=<yourcall> DXCLUSTER_PASSWORD=<password> \
  go run . -dev -port 8080 -dxcluster-enable -dxcluster-endpoint db0erf.de:7300
```

Flags: `-dxcluster-enable`, `-dxcluster-endpoint` (default `db0erf.de:7300`),
`-dxcluster-reconnect-seconds`, `-dxcluster-verbose`. Credentials via
`DXCLUSTER_USERNAME` / `DXCLUSTER_PASSWORD` (or the `-dxcluster-username` /
`-dxcluster-password` flags).

All parsed cluster spots are persisted to Postgres `dx_raw_spots` with
`source_type=dxcluster`; only spots with usable locators enter the live SSE/map
flow. Cluster spots stay out of the FT8-SNR baseline.

### QRZ enrichment (shared)

`QRZ_USERNAME` / `QRZ_PASSWORD` enable callsign→locator enrichment (`dx_locator`),
shared by the DX-cluster and RBN ingests.

### RBN (Reverse Beacon Network)

Public CW/RTTY telnet relay; it prompts for a callsign before streaming. Fills the
activity chart/live stream during CW/SSB contests when FT8 thins out. Minimal
scope: activity + live only, kept out of the FT8-SNR baseline like the DX cluster.

```bash
go run . -dev -port 8080 -rbn-enable -rbn-callsign <yourcall>
```

### WSPR (wspr.live)

ClickHouse HTTP poller, reference-only (also kept out of the FT8-SNR baseline).

```bash
go run . -dev -port 8080 -wspr-enable
```

## Cell bucket feed (for pathscope)

The former Propagation Lab and DXPulse features were removed (2026-08-04). What
remains is the ingest/persistence they also produced, because the
[pathscope](cmd/pathscope) module consumes it:

- `proplab_cell_buckets` — midpoint cell × band × lane 15-minute buckets written
  in-process from the live spot stream. Retention: `-proplab-cell-retention-days`
  (default `60`, `0` disables).
- `proplab_sw_series` — NOAA SWPC index series (kp, F10.7, x-ray, OVATION), gated
  by `-proplab-sw-enable`.

## Local operator agent (opmode)

`cmd/horstoperator-agent` is a standalone local agent exposing `/v1/*` endpoints
consumed directly by the browser opmode runtime. It bridges to PSTrotator via UDP
(built-in command profile, no user templates) and, with `-rig-transport`, can also
tune the rig via WaveLogGate or Log4OM. The backend never proxies to it: browser
opmode calls `http://127.0.0.1:9955/v1/*` directly.

```bash
go run ./cmd/horstoperator-agent \
  -listen 127.0.0.1:9955 \
  -backend-url https://horstreporter.kgbvax.net \
  -station-name DK3JF \
  -station-locator JO62qm \
  -pst-host 127.0.0.1 \
  -pst-port 12000
```

When `-backend-url` is set, the agent also reverse-proxies non-`/v1/*` requests to
the HorstReporter backend, so it can serve as a single browser origin.

Agent flags (core):

- `-listen` HTTP listen address (default `127.0.0.1:9955`)
- `-backend-url` optional backend base URL for reverse proxying non-`/v1/*` routes
- `-station-name` station display name
- `-station-locator` Maidenhead locator (**required**)
- `-control-permitted` enable/disable rotate+mode commands

PSTrotator UDP flags: `-pst-host`, `-pst-port`, `-pst-timeout-ms`, plus
diagnostics (`-pst-log-traffic`, `-pst-log-traffic-hex`, `-pst-log-max-bytes`).

Rig control: `-rig-transport none|waveloggate|log4om`
(`-rig-waveloggate-url`, `-rig-log4om-addr`).

### Chase Queue enrichment + award progress

The agent resolves Wavelog attributes for the Chase Queue and computes award
progress ("wanted": DXCC/WAS/POTA) in-process via `internal/awards`, so the
operator's log stays local — it is never pulled to the shared server. Configure
with `WAVELOG_API_KEY`, `WAVELOG_STATION_ID` (required for DCLNext), and
optionally `POTA_HUNTED_CSV` (hunted-parks export). Spec:
[docs/horstawards.md](docs/horstawards.md).

## HF link-quality scoring (horstprop)

`cmd/horstprop` is a separate binary that consumes HorstReporter read-only over
HTTP and scores individual paths/links (consumed by the Chase Queue via
`/horstprop/v1/score`). The scoring boundary is deliberate: horstreporter owns
the shared, multi-station band/region conditions analytics; per-spot/path link
scoring lives only in horstprop. Spec: [docs/horstprop.md](docs/horstprop.md).

```bash
go run ./cmd/horstprop -listen 127.0.0.1:9970
```

## Mercator performance gate

```bash
npm run perf:gate:mercator
```

Runs Mercator perf tests with report output enabled, writes scenario reports to
`tmp/perf-reports/`, compares against `.perf-baseline.json`, and fails if any
threshold is exceeded. Sub-commands: `npm run test:perf:mercator:report`,
`npm run test:perf:mercator:assert`.

## Server-driven frame capture (snapshot-based)

- `GET /api/capture_snapshot` returns a deterministic filtered spot snapshot for a
  target and timestamp.
- The frontend can boot in capture mode via `?capture=1` and signals readiness
  via `window.__horstCaptureReady`.
- Scripts (require Playwright via `npm i`, a running server, and `ffmpeg` for
  movie assembly):

```bash
npm run capture:dk3jf:frames -- --base-url http://127.0.0.1:8080 --target JO32 --duration-minutes 360 --step-seconds 60 --projection mercator --style active-area --min-snr-mode ssb
npm run capture:dk3jf:movie -- tmp/dk3jf-capture/<run-id> tmp/dk3jf-capture/<run-id>/dk3jf.mp4 24
```

This path does not alter regular live SSE behavior.

## Related repositories

- `../dxlens` — sibling module mounted at `/dxlens/` (via `go.mod replace`); DX
  conditions lens reading the in-memory baseline.
- `../horstapp` — sibling Flutter mobile app (iOS primary + Android) consuming
  this backend read-only over HTTPS: propagation-intelligence overview plus
  native home-screen widgets fed by `/api/prop_intel/summary`.