# DX Cluster module — local agent API contract (v1)

Status: **draft / design**. Slice A of the HorstOperator DX Cluster ("Chase Queue") module.
Defines the new `horstoperator-agent` (`127.0.0.1:9955`) surface the browser calls directly.

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
  "rig":    { "tune": true, "preview": true, "modes": ["USB","LSB","CW","DATA"] },
  "lookup": { "wavelog": true, "was": true, "profile_age_sec": 142 }
}
```

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

## 3. Rig — `tune`, `preview`, `state`

Browser sends the **logical** mode + the spot frequency; the **agent owns the rig-mode mapping**
(SSB→USB/LSB by band, FT8→DATA + standard dial freq, …) — rig/band-specific, not the UI's job.

```json
// POST /v1/rig/tune   (dbl-click → VFO A)
{ "permit_control": true, "freq_hz": 18145000, "mode": "SSB", "vfo": "A" }
// → { "ok": true, "vfo":"A", "freq_hz":18145000, "rig_mode":"USB" }

// POST /v1/rig/preview  (hover → VFO B pre-hear; clear on mouseleave)
{ "permit_control": true, "freq_hz": 18145000, "mode": "SSB", "vfo": "B", "enable_dual_watch": true }
{ "permit_control": true, "clear": true }          // dismiss preview

// GET /v1/rig/state   (UI reflects truth, detects manual retune)
{ "ok": true, "vfo_a": {"freq_hz":18145000,"rig_mode":"USB"},
  "vfo_b": null, "dual_watch": false, "ptt": false }
```

- **Preview is opt-in.** Behind a "Preview on hover" toggle (default off). Browser debounces ~250 ms
  before issuing, and sends `clear` on mouseleave.
- Rig transport: Hamlib `rigctld` / flrig over a local socket (hardware-local, like the rotor UDP bridge).

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
// response
{ "ok": true,
  "rig":     { "ok": true, "vfo":"A", "freq_hz":18145000, "rig_mode":"USB" },
  "antenna": { "ok": true, "azimuth_deg":189, "fast_polling_enabled":true, "poll_interval_ms":500 } }
```

---

## Open details to confirm

- **FT8/digital tuning** — tune the band's standard dial freq (e.g. 18.100) rather than the spot's
  exact Hz. Assumed yes, agent-side.
- **Split** — DXpeditions work split ("up 5"). v1: stay simple (VFO A on the spot), no auto-split.
- **WAS scope** — US-state-only; `needed_was` never fires for non-US spots.

## Interaction summary (how the browser uses this)

- New spots arrive on `/api/stream` → browser batches into `POST /v1/operate/enrich` → merges by `id`.
- Hover (if preview enabled) → debounced `POST /v1/rig/preview`; mouseleave → `clear`. Also lights the
  great-circle path on the map.
- Double-click → `POST /v1/rig/tune` (VFO A); card marked tuned, reconciled against `GET /v1/rig/state`.
- "Tune + Turn" → `POST /v1/operate`.
- `↺` marker expand → `GET /v1/qso`.
- Snooze → client-side localStorage in v1.
