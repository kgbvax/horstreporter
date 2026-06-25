# DX Cluster module — local agent API contract (v1)

Status: **partly shipped**. The rig **tune** path (§3), composite **Tune + Turn** (§4), and Wavelog
**enrich** (§1) are implemented; the lazy QSO note (§2), WAS, and VFO-B preview/split remain design.
Defines the `horstoperator-agent` (`127.0.0.1:9955`) surface the browser calls directly.

## Architecture recap

Three tiers, secrets stay operator-local:

- **HorstReporter backend** — shared, operator-agnostic, *no* operator secrets. Provides raw spots
  (`/api/stream` SSE, `sourceType:"dxcluster"`, unchanged), per-band baseline (`/api/dx_conditions`),
  and (later) a base "workability" score seam reusing `dx_conditions.go`.
- **horstoperator-agent** — trusted local box, holds the Wavelog (cloud) API key + rig/rotor access.
  Already reverse-proxies `/` → backend, so the **browser sees one origin**: `/v1/*` is local,
  everything else is proxied to the backend.
- **browser** — merges spots + enrichment + score keyed by `call·band·mode`; renders the queue;
  drives the map highlight + great-circle path.

The backend never holds the Wavelog key and never proxies to the agent (one-directional rule preserved).

## Conventions

- All endpoints below are local to the agent under `/v1/`.
- **Two permission scopes**, both advertised in `/v1/status`:
  - `permit_control` — writes to hardware (rig/rotor). Reuses the existing 3-level gate
    (server / agent / user) already used by `/v1/antenna/rotate`.
  - `permit_lookup` — reads Wavelog (read-only; still sends queries to a cloud service, so it is a
    separate, lighter toggle — default **on** when Wavelog is configured).
- Error shape: non-2xx + `{"ok":false,"error":"…","code":"…"}`. `403` for permission denied.

### Extended `/v1/status.capabilities`

UI hides what isn't present.

```json
"capabilities": {
  "control": true, "mode_control": true, "modes": ["forward","backward","bidirectional"],
  "rig":    { "tune": true, "preview": false, "split": false },
  "lookup": { "wavelog": true, "was": false }
}
```

`rig` appears only when a rig backend is configured (`-rig-transport`); with the **WaveLogGate**
backend (shipped first), `preview`/`split` are `false` (see §3). `lookup` appears only when
`WAVELOG_API_KEY` is set; `was` is `false` (no Wavelog WAS endpoint — see §1).

---

## 1. `POST /v1/operate/enrich` — needed + worked-before (read path) — **shipped**

Browser batches the visible/candidate spots; the agent answers per spot and echoes the merge `id`.
Implemented as a **thin caching proxy over Wavelog `POST /api/private_lookup`** (`wavelog.go`):
Wavelog computes the worked/confirmed matrix server-side, so the agent does **not** mirror the log —
it calls `private_lookup` once per unique `call|band|mode`, caches the result (5-min TTL), and maps
it to the `needed` vocabulary. Config via env (loaded from `.env` if present, never logged):
`WAVELOG_API_KEY` (read key; enables the feature) and `WAVELOG_URL`
(default `https://log.dclnext.darc.de/index.php`).

### Request

```json
{
  "permit_lookup": true,
  "spots": [
    { "id": "3Y0J|17m|SSB",  "call": "3Y0J",  "band": "17m", "mode": "SSB" },
    { "id": "VK6LC|20m|CW",  "call": "VK6LC", "band": "20m", "mode": "CW"  }
  ]
}
```

`id` is opaque to the agent (echoed back). The browser uses `call|band|mode`.

### Response

```json
{
  "ok": true,
  "degraded": false,
  "results": [
    {
      "id": "3Y0J|17m|SSB",
      "dxcc": { "entity": "BOUVET", "id": "24", "cont": "AF", "flag": "🇧🇻" },
      "needed": ["dxcc"],
      "worked_before": { "worked": false, "worked_band": false, "worked_band_mode": false }
    },
    {
      "id": "VK6LC|20m|CW",
      "dxcc": { "entity": "AUSTRALIA", "id": "150", "cont": "OC" },
      "needed": ["band"],
      "worked_before": { "worked": true, "worked_band": false, "worked_band_mode": false }
    }
  ]
}
```

- `degraded: true` when one or more upstream lookups failed (network / 5xx). Those spots come back
  bare (`needed: []`, no `dxcc`) so the UI shows them un-chipped rather than failing.
- `dxcc` block comes straight from `private_lookup` (entity, DXCC id, continent, flag emoji).

### `needed` vocabulary (confirmation-based)

`private_lookup` exposes entity **confirmed** status (`dxcc_confirmed[_on_band[_mode]]`) but **not**
entity *worked* status — so `needed` is award-oriented: a slot is "needed" until it is *confirmed*.

| token  | derivation                                       | meaning                       | UI chip            |
| ------ | ------------------------------------------------ | ----------------------------- | ------------------ |
| `dxcc` | `!dxcc_confirmed`                                | entity not yet confirmed      | `ATNO` (gold)      |
| `band` | confirmed, but `!dxcc_confirmed_on_band`         | new band-slot                 | `New band`         |
| `mode` | band-confirmed, but `!dxcc_confirmed_on_band_mode` | new mode-slot               | `New mode`         |

**Collapse rule (agent-side):** if the entity isn't confirmed, only `dxcc` is returned — `band`/`mode`
are implied. `worked_before` carries the per-callsign `call_worked[_band[_mode]]` booleans; the UI
shows a muted `worked` marker when `worked_band_mode` and nothing is needed (likely a dupe). No
chip/emoji uses decorative symbols per the project's UI rules — flags are data, not decoration.

### Caching

- Per-`call|band|mode` 5-min TTL cache on the agent; a batch dedupes by key, so *M* unique calls cost
  at most *M* upstream lookups. Concurrency is capped (4) to respect Wavelog rate limits.
- Batch capped at 60 spots.

---

## 2. `GET /v1/qso?ref=…` — lazy prior-contact note — **deferred**

Not yet implemented. `private_lookup` gives worked/confirmed booleans but no QSO detail (date / RST /
note); the note popover needs `get_contacts_adif` filtering and arrives with a later slice.

---

## 3. Rig — `tune` (WaveLogGate-first, pivot-ready)

Browser sends the **logical** mode + the spot frequency; the **agent owns the rig-mode mapping**
(`rigModeForBackend`: SSB→USB/LSB by band, FT8/FT4→`data`, CW→`cw`, …) — rig/band-specific, not
the UI's job.

The agent talks to the rig through a pluggable **`rigController`** interface
(`cmd/horstoperator-agent/rig.go`) so the browser contract stays stable while the backend can be
swapped. The shipped backend is **WaveLogGate**: `Tune` issues
`GET http://127.0.0.1:54321/{freq_hz}/{mode}` (server-side, no CORS), WaveLogGate performs the CAT
write via its rigctld/FLRig connection and applies mode-on-QSY.

```json
// POST /v1/rig/tune   (dbl-click / "Tune" → active VFO)
{ "permit_control": true, "freq_hz": 18145000, "mode": "SSB" }
// → { "ok": true, "freq_hz":18145000, "rig_mode":"usb" }
```

- Gated by the same 3-level `permit_control` check as `/v1/antenna/rotate` (server `-control-permitted`
  + agent + UI). `503` if no rig backend is configured; `502` if the backend (WaveLogGate) errors.
- **Single VFO only.** WaveLogGate's callback drives one VFO and cannot set VFO-B / split (it only
  *reports* split state), so `preview` and `split` stay `false` in capabilities.

### Pivot (later, not in this slice)

A `rigctldBackend` (Hamlib net protocol, TCP 4532) and/or `flrigBackend` (XML-RPC) implementing the
same `rigController` interface adds VFO-B **preview** (pre-hear / dual-watch) and **split** — flipping
those capability flags true. Select via `-rig-transport hamlib|flrig`. The browser contract
(`/v1/rig/tune`, `/v1/operate`) is unchanged; WaveLogGate keeps doing Wavelog logging off the same
shared rigctld/FLRig. Endpoints `GET /v1/rig/state` and `POST /v1/rig/preview` arrive with that pivot.

---

## 4. `POST /v1/operate` — composite Tune + Turn

One call, one permission check, aggregated status; fans out to rig tune + the existing rotate path.
Partial-failure aware so the UI can say "tuned, rotor failed."

```json
// request
{ "permit_control": true,
  "freq_hz": 18145000, "mode": "SSB",
  "azimuth_deg": 189, "target_locator": "IB59",
  "target_lat": -54.4, "target_lng": 3.4, "station_lat": 50.0, "station_lng": 8.0 }
// response (200 even on partial failure; inspect per-leg ok)
{ "ok": true,
  "rig":     { "ok": true, "freq_hz":18145000, "rig_mode":"usb" },
  "antenna": { "ok": true, "azimuth_deg":189 } }
```

A leg with no input is skipped (`{"ok":false,"skipped":"no freq_hz"}` /
`"skipped":"no azimuth_deg"`) rather than erroring.

---

## Open details to confirm

- **FT8/digital tuning** — currently tunes the spot's exact freq with mode inferred client-side
  (`guessMode`: FT8 watering holes → data, CW segment → cw). Snapping to the band's standard dial
  freq is a possible refinement.
- **Split** — DXpeditions work split ("up 5"). v1: VFO A on the spot, no auto-split (needs the
  rigctld/FLRig pivot anyway).
- **Worked vs confirmed** — `needed` is confirmation-based because `private_lookup` exposes only
  entity *confirmed* status (§1). A worked-based "never made the contact" variant would need a
  different data source.
- **WAS** — no Wavelog endpoint (`private_lookup.state` is the *spot's* state, not your worked-states
  set); deferred.

## Interaction summary (how the browser uses this)

- **Shipped (WaveLogGate slice):** "Tune" button / double-click → `POST /v1/rig/tune` (active VFO);
  "Tune + Turn" → `POST /v1/operate` (tune + rotate). Both gated on agent present + `permit_control`
  + `capabilities.rig.tune`; `static/dxcluster.js` hides the actions otherwise. Mode inferred from the
  spot frequency client-side (`guessMode`), refined agent-side and by WaveLogGate's mode-on-QSY.
- **Shipped (Wavelog enrich slice):** visible spots batched into `POST /v1/operate/enrich` →
  need-chips (`ATNO`/`New band`/`New mode`) + muted `worked` marker, merged by `id`. Gated on agent +
  `capabilities.lookup.wavelog`; best-effort (failures leave cards un-chipped).
- **Deferred (rigctld/FLRig pivot):** hover → `POST /v1/rig/preview` (VFO-B pre-hear) + great-circle
  path on the map; `GET /v1/rig/state` reconciliation; split.
- **Deferred:** lazy QSO note (`GET /v1/qso`); WAS (no Wavelog endpoint).
- Snooze → client-side localStorage in v1.
