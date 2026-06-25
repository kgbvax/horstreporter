# DX Cluster module — local agent API contract (v1)

Status: **partly shipped**. The rig **tune** path (§3) and composite **Tune + Turn** (§4) are
implemented against a WaveLogGate backend; Wavelog enrich (§1–2) and VFO-B preview/split remain
design. Defines the `horstoperator-agent` (`127.0.0.1:9955`) surface the browser calls directly.

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
  "lookup": { "wavelog": true, "was": true, "profile_age_sec": 142 }
}
```

`rig` appears only when a rig backend is configured (`-rig-transport`). With the
**WaveLogGate** backend (shipped first), `preview`/`split` are `false` — see §3.

---

## 1. `POST /v1/operate/enrich` — needed + worked-before (read path)

Browser batches the visible/candidate spots; agent echoes a merge key and answers from cache.
Browser sends only `call·band·mode`; the agent resolves DXCC itself (it owns the worked matrix).

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

### Response

```json
{
  "ok": true,
  "degraded": false,
  "profile_age_sec": 142,
  "results": [
    {
      "id": "3Y0J|17m|SSB",
      "dxcc": { "entity": "Bouvet I.", "id": 24, "cont": "AF", "cqz": 38 },
      "needed": ["dxcc"],
      "needed_was": null,
      "worked_before": { "worked": false }
    },
    {
      "id": "VK6LC|20m|CW",
      "dxcc": { "entity": "Australia", "id": 150, "cont": "OC", "cqz": 29 },
      "needed": ["band"],
      "needed_was": null,
      "worked_before": {
        "worked": true, "count": 3,
        "last": { "date": "2021-03-14", "band": "15m", "mode": "SSB" },
        "note_ref": "qso:88431"
      }
    }
  ]
}
```

- `degraded: true` when Wavelog is unreachable → `needed:null`; UI shows spots without chips rather
  than failing.
- `profile_age_sec` — staleness of the worked matrix (see caching).

### `needed` vocabulary

Stable tokens, independent of how Wavelog phrases things internally:

| token                        | meaning                                       | UI chip          |
| ---------------------------- | --------------------------------------------- | ---------------- |
| `dxcc`                       | all-time-new entity (ATNO)                    | `★ ATNO` (gold)  |
| `band`                       | entity worked before, new band-slot           | `NEW BAND`       |
| `mode`                       | entity worked before, new mode-slot           | `NEW MODE`       |
| `needed_was` `{state,need[]}`| US state needed for WAS                        | `WAS: AZ`        |

**Collapse rule (browser):** if `dxcc` is present, show only `★ ATNO` — `band`/`mode` are implied.

### Caching / refresh (agent-internal — the contract's backbone)

- Worked matrix (DXCC×band×mode) + WAS + worked-callsign index held in memory; refreshed every
  ~5 min and via `POST /v1/operate/profile/refresh`. The future Log module invalidates on QSO write.
- Per-`call·band·mode` results in an LRU + TTL cache with in-flight dedup. Spots repeat heavily →
  high hit rate; a batch of *M* unique calls costs at most *M* upstream lookups, then cached.
- Agent throttles upstream (small concurrency, coalesce dupes) to respect cloud Wavelog rate limits.
- Cache cleared on profile refresh.

---

## 2. `GET /v1/qso?ref=qso:88431` — lazy prior-contact note

Fetched only when the operator expands the `↺` marker on a card.

```json
{
  "ok": true, "call": "VK6LC",
  "qsos": [
    { "date":"2021-03-14","band":"15m","mode":"SSB","rst_s":"599","rst_r":"599",
      "name":"Wayne","note":"Ran 5W QRP — patient op. QSL via bureau." }
  ]
}
```

Browser shows same-band first, else most recent.

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

- **FT8/digital tuning** — tune the band's standard dial freq (e.g. 18.100) rather than the spot's
  exact Hz. Assumed yes, agent-side.
- **Split** — DXpeditions work split ("up 5"). v1: stay simple (VFO A on the spot), no auto-split.
- **WAS scope** — US-state-only; `needed_was` never fires for non-US spots.

## Interaction summary (how the browser uses this)

- **Shipped (WaveLogGate slice):** "Tune" button / double-click → `POST /v1/rig/tune` (active VFO);
  "Tune + Turn" → `POST /v1/operate` (tune + rotate). Both gated on agent present + `permit_control`
  + `capabilities.rig.tune`; `static/dxcluster.js` hides the actions otherwise. Mode inferred from the
  spot frequency client-side (`guessMode`), refined agent-side and by WaveLogGate's mode-on-QSY.
- **Deferred (rigctld/FLRig pivot):** hover → `POST /v1/rig/preview` (VFO-B pre-hear) + great-circle
  path on the map; `GET /v1/rig/state` reconciliation; split.
- **Deferred (Wavelog enrich slice):** spot enrichment via `/v1/operate/enrich`; `↺` marker → `GET /v1/qso`.
- Snooze → client-side localStorage in v1.
