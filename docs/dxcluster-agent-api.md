# DX Cluster module — local agent API contract (v1)

Status: **partly shipped**. The rig **tune** path (§3), composite **Tune + Turn** (§4), are implemented; the lazy QSO note (§2) and VFO-B preview/split remain design. The Wavelog **enrich** endpoint (formerly §1) was removed with the Chase Queue.
Defines the `horstoperator-agent` (`127.0.0.1:9955`) surface the browser calls directly.

## Architecture recap

Three tiers, secrets stay operator-local:

- **HorstReporter backend** — shared, operator-agnostic, *no* operator secrets. Provides raw spots
  (`/api/stream` SSE, `sourceType:"dxcluster"`, unchanged), per-band baseline (`/api/dx_conditions`),
  and (later) a base "workability" score seam reusing `dx_conditions.go`.
- **horstoperator-agent** — trusted local box, holds rig/rotor access.
  Already reverse-proxies `/` → backend, so the **browser sees one origin**: `/v1/*` is local,
  everything else is proxied to the backend.
- **browser** — calls the agent directly for rig tune and rotor control.

The backend never proxies to the agent (one-directional rule preserved).

## Conventions

- All endpoints below are local to the agent under `/v1/`.
- **Two permission scopes**, both advertised in `/v1/status`:
  - `permit_control` — writes to hardware (rig/rotor). Reuses the existing 3-level gate
    (server / agent / user) already used by `/v1/antenna/rotate`.
- Error shape: non-2xx + `{"ok":false,"error":"…","code":"…"}`. `403` for permission denied.

### Extended `/v1/status.capabilities`

UI hides what isn't present.

```json
"capabilities": {
  "control": true, "mode_control": true, "modes": ["forward","backward","bidirectional"],
  "rig":    { "tune": true, "preview": false, "split": false }
}
```

`rig` appears only when a rig backend is configured (`-rig-transport none|waveloggate|log4om`,
see `cmd/horstoperator-agent -h`); with the **WaveLogGate**
backend (shipped first), `preview`/`split` are `false` (see §3).

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

## Interaction summary (how the browser uses this)

- **Shipped (WaveLogGate slice):** "Tune" button / double-click → `POST /v1/rig/tune` (active VFO);
  "Tune + Turn" → `POST /v1/operate` (tune + rotate). Both gated on agent present + `permit_control`
  + `capabilities.rig.tune`. Mode inferred from the
  spot frequency client-side (`guessMode`), refined agent-side and by WaveLogGate's mode-on-QSY.
- **Deferred (rigctld/FLRig pivot):** hover → `POST /v1/rig/preview` (VFO-B pre-hear) + great-circle
  path on the map; `GET /v1/rig/state` reconciliation; split.
- **Deferred:** lazy QSO note (`GET /v1/qso`).
- Snooze → client-side localStorage in v1.
