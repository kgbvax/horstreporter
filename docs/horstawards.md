# horstawards — award progress + the "wanted lookup"

`horstawards` is a standalone service that owns the operator's **award-progress
index** and answers one question fast: *for this spot, which award slots would it
fill that I haven't satisfied yet?* The Chase Queue uses the answer to flag spots
as **ATNO / +BAND / +MODE / WAS / POTA**.

It mirrors the `horstprop` pattern: a separate binary in the same module,
decoupled over HTTP, sharing only the contract types in `internal/awardcontract`.
It stays out of the shared core (same boundary that keeps per-spot scoring in
`horstprop`).

## Topology

horstawards runs **co-located on the HorstReporter server** (`kgbvax.net`),
bound to `127.0.0.1:9956`, alongside the backend and `horstprop`. The backend
reverse-proxies **`/horstawards/`** to it (read-only: only `/v1/wanted` and
`/v1/health` — `/v1/refresh` stays server-internal). The **operator agent is the
one local piece** (it drives the rotator/rig and the browser hits it at
`127.0.0.1:9955`); it reaches horstawards through that proxy at
`https://horstreporter.kgbvax.net/horstawards`. The Wavelog read key and the POTA
hunted-parks CSV live on the server next to horstawards.

## The model: wanted = attribute ∩ progress

Every award reduces to a **slot** — one thing you can work/confirm — and a
decision:

> needed = the candidate slots a spot could fill, minus the slots already satisfied.

Two independent inputs feed it:

- **Attribute** — *what is this station?* (DXCC entity, US state, CQ zone, grid,
  POTA park). The **operator agent** already resolves this per-callsign via
  Wavelog `private_lookup` and forwards it.
- **Progress** — *have I worked/confirmed that slot?* This is what horstawards
  owns, pulled from the operator's log (and optionally the POTA API).

```
 LOCAL (operator machine)          │   SERVER (kgbvax.net)
                                   │
 Chase Queue (browser)            │
   │ POST /v1/operate/enrich      │
   ▼                              │
 horstoperator-agent ── resolves attributes via Wavelog private_lookup
   │  └─ POST https://kgbvax.net/horstawards/v1/wanted
   │            (backend reverse-proxy /horstawards/ ─► 127.0.0.1:9956)
   │                              │        ▼
   └─ merges: awards authoritative│   horstawards (in-memory progress index)
      when loaded; Wavelog fallback│        ▲ slow refresh goroutines
                                   │  ┌─────┴───────────────┐
                                   │  Wavelog ADIF / pota-csv / POTA API
                                   │  └── atomic JSON snapshot store ──┘
```

The query path is a pure in-memory map lookup — no disk, no upstream call per
spot. Progress is refreshed on a slow schedule (logs change by the QSO;
confirmations lag days), which is why the hot path can stay this cheap.

## Awards in v1

| Program | Slot entity | Source of progress | Notes |
| --- | --- | --- | --- |
| **DXCC** | DXCC id (overall / per-band / per-band-mode) | Wavelog ADIF | Satisfied at **confirmed**. Collapses to `dxcc` > `band` > `mode`, matching the agent's prior Wavelog logic. DXCC id `0` (no/deleted/unresolvable) is ignored. |
| **WAS** | US state (overall + per-band) | Wavelog ADIF | Only for US-entity QSOs (DXCC 291/110/6) with a valid state. 50 states (DC excluded). |
| **POTA (park)** | park ref `K-1234` | Wavelog ADIF (`SIG=POTA`/`SIG_INFO`/`POTA_REF`); POTA API supplements | Satisfied at **worked** (hunting needs no confirmation). The spot's park ref is parsed from the cluster comment by the frontend and carried through `pota_ref`. Fires only when POTA coverage is loaded (operator has POTA QSOs in the log, or the POTA API source is configured). |

The slot model `Slot{Program, Entity, Band, ModeCl}` and the request's
`cqz`/`grid`/`iota` fields already carry the shape for **WAZ / VUCC / IOTA** —
adding one is a new `award/<prog>.go` file plus a `needed[]` vocabulary value, no
schema change.

> An earlier design also flagged "new state for POTA" from a US station's home
> state alone. That was dropped: a station's home state does not mean it is
> activating a park, so it produced a `pota` want on essentially every US spot.
> POTA "wanted" now requires a real park ref.

### Coverage gating (no false positives)

A program only contributes `needed[]` when its progress is actually **loaded**.
If a source is down or unconfigured, that program is absent from the index and
the engine reports nothing for it — it never assumes "everything is new". POTA
coverage in particular is declared **only when the log actually contains POTA
QSOs** (or the POTA API returns a parks list), so a non-POTA operator never sees
spurious POTA wants.

## Sources

| Source | Pulls | Cadence | Required? |
| --- | --- | --- | --- |
| `wavelog-adif` | full log via `POST /api/get_contacts_adif` (paginated by `fetchfromid`), folded locally into DXCC + WAS + POTA slots | 30 min | Yes (set `WAVELOG_API_KEY` **and `WAVELOG_STATION_ID`** — DCLNext returns HTTP 400 without the station id) |
| `pota-csv` | the operator's **hunted-parks CSV export** from POTA, parsed into park slots | 1 h | No — **recommended** POTA source (`-pota-hunted-csv` / `POTA_HUNTED_CSV`) |
| `pota` | hunted parks from a POTA endpoint returning a full parks list | 6 h | No (only useful with an authenticated endpoint) |

> **Use the CSV for POTA.** The **public** `api.pota.app/profile/{call}` returns
> only aggregate counts (`stats.hunter.parks`) and a short `recent_activity` list
> — not a full hunted-park list — so it can't seed park-level coverage. POTA lets
> you export your hunted parks as CSV; point `-pota-hunted-csv` at that file and
> the `pota-csv` source loads the authoritative list. The parser is tolerant of
> column layout (uses a `Reference`/`Park` column if present, else scans cells for
> a POTA-ref token). Wavelog-logged POTA QSOs also seed park coverage; both fold
> together. The `pota` API source remains only for an authenticated full-list
> endpoint.

A full re-pull every refresh keeps the index always-correct and automatically
catches QSL upgrades on old QSOs. "Confirmed" is derived from the ADIF QSL fields
(`QSL_RCVD` / `LOTW_QSL_RCVD` / `EQSL_QSL_RCVD` = `Y`).

> **POTA API caveat:** the exact `api.pota.app` endpoint/JSON for a per-callsign
> hunted-park list is not officially documented. The adapter targets a documented
> assumed shape, is **fail-soft** (any error → it is skipped, POTA still works
> from the log), and only marks coverage when it parses a parks list — so a schema
> mismatch never produces wrong "wanted". Tune `internal/source/pota.go` against
> the live API as needed.

## Persistence

An atomic JSON file (`<data-dir>/awards.json`, temp-write + rename) keyed by
source name. The access pattern is "load all at startup → rebuild the in-memory
index; write whole snapshots on refresh", so a key/value document fits and keeps
the build pure-Go (`CGO_ENABLED=0`) with **zero** new dependencies. After a
restart the service serves the last persisted index immediately (warm), refreshing
in the background.

## HTTP API

Listens on `127.0.0.1:9956` by default.

### `POST /v1/wanted`

```json
{ "permit_lookup": true, "spots": [
  {"id":"3Y0J|17m|SSB","call":"3Y0J","dxcc_id":"24","state":"","cqz":"38",
   "grid":"IB59","iota":"","pota_ref":"","band":"17m","mode":"SSB"} ] }
```
```json
{ "ok": true, "degraded": false, "results": [
  {"id":"3Y0J|17m|SSB","needed":["dxcc"],
   "slots":[{"program":"dxcc","entity":"24","status":"unworked"}]} ] }
```

- `permit_lookup` must be `true` (403 otherwise). Batch capped at 60 spots.
- `needed[]` vocabulary: `dxcc | band | mode | was | pota`.
- `slots[]` is additive per-slot detail (`status`: `unworked|worked|confirmed`).
- `degraded` is `true` when the index has never loaded or is older than
  `HORSTAWARDS_STALE_AFTER` (default 6 h).

### `GET /v1/health`

Service/version/uptime, `degraded`, `index_slots`, `programs_loaded`, and a
per-source summary (last refresh time + stats).

### `POST /v1/refresh`

Triggers an immediate async refresh of all sources (202). Requires
`{"permit_refresh": true}` in a JSON body (`Content-Type: application/json`); the
content-type requirement plus the loopback-only CORS policy close cross-site
refresh triggers, and it is **not** exposed through the public `/horstawards/`
proxy — trigger it server-internally (`curl 127.0.0.1:9956/v1/refresh …`). The
POST endpoints reject non-JSON requests with 415.

## Running

```bash
# Local dev (reads WAVELOG_API_KEY from a repo-root .env). POTA optional.
go run ./cmd/horstawards -listen 127.0.0.1:9956 -wavelog-station-id <id> -pota-hunted-csv hunted.csv

# Sanity check (on the box running horstawards)
curl -s 127.0.0.1:9956/v1/health
curl -sX POST 127.0.0.1:9956/v1/wanted -H 'Content-Type: application/json' \
  -d '{"permit_lookup":true,"spots":[{"id":"t","call":"W1AW","dxcc_id":"291","state":"CT","band":"20m","mode":"SSB"}]}'
```

**Production (the real topology):** horstawards runs on the server next to the
backend. Deploy it with `./deploy_horstawards.sh` (defaults to
`horstreporter.kgbvax.net`); the installer creates `/etc/default/horstawards`
(chmod 600) for `WAVELOG_API_KEY` + `WAVELOG_STATION_ID` and the `ARGS`
(`-pota-hunted-csv …`). The backend already mounts the `/horstawards/` proxy
(`-horstawards-url`, default `http://127.0.0.1:9956`). Then enable it on the
**local** agent:

```bash
HORSTAWARDS_URL=https://horstreporter.kgbvax.net/horstawards ./run_operator_agent.sh
```

Configuration and secrets: see `docs/deployment-secrets.md`.

## Composition with the agent

The agent stays the only Chase-Queue endpoint and the only holder of Wavelog
creds. In `handleEnrich` it resolves attributes via Wavelog, calls `/v1/wanted`
once per batch, and merges:

- **horstawards loaded & healthy** → its `needed[]` is authoritative (it computes
  dxcc/band/mode/was/pota from the operator's own log) and supersedes the Wavelog
  confirmed-flag verdict; `slots[]` is attached.
- **horstawards absent / cold / erroring** → the Wavelog-derived `needed[]` is
  kept and the response is marked `degraded`. Awards being down never drops the
  existing enrichment.

The agent advertises availability via `/v1/status` → `capabilities.lookup.awards`.

## Frontend (follow-up)

`static/dxcluster.js` surfaces the `needed[]` vocabulary as Chase Queue badges:
`dxcc`→ATNO (gold), `band`→+BAND, `mode`→+MODE, `was`→+STATE, `pota`→POTA, plus
the `wanted` sort rank (`wantInfo`). The change was additive (unknown values are
ignored), and the badge colors are restrained CSS custom props (no emojis).

POTA park refs are parsed from the cluster comment by `parsePotaRef` in
`dxcluster.js` (a letter-led `PREFIX-NNNNN` pattern), carried in the enrich
request's `pota_ref`, and forwarded by the agent into `WantedSpot.POTARef`. POTA
"wanted" then fires for operators with POTA coverage loaded — i.e. a hunted-parks
CSV via `-pota-hunted-csv` (recommended), POTA QSOs in the Wavelog log, or the
POTA API source. An operator who does no POTA sees no POTA badges.
