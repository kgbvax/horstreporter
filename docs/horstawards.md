# Award progress + the Chase Queue "wanted lookup"

The operator agent flags DX-cluster spots as **ATNO / +BAND / +MODE / WAS / POTA**
in the Chase Queue. It does this with a small award-progress engine
(`internal/awards`) that runs **in-process inside the operator agent**, so the
operator's personal log and hunted-parks list **never leave the local machine**.

## The model: wanted = attribute ∩ progress

Every award reduces to a **slot** — one thing you can work/confirm — and:

> needed = the candidate slots a spot could fill, minus the slots already satisfied.

Two inputs:

- **Attribute** — *what is this station?* (DXCC entity, US state, grid, CQ zone,
  POTA park). The agent already resolves this per-callsign via Wavelog
  `private_lookup`.
- **Progress** — *have I worked/confirmed that slot?* Owned by the in-process
  award engine, built from the operator's own log + hunted-parks CSV.

```
 LOCAL operator machine                                  external
 ─────────────────────                                   ────────
 browser (Chase Queue)
   │ POST /v1/operate/enrich
   ▼
 horstoperator-agent
   ├─ Wavelog private_lookup (attributes) ───────────────► Wavelog (log.dclnext…)
   ├─ awards.Manager.Evaluate(spot)  ← in-memory index
   │     ▲ slow refresh goroutines
   │     ├─ Wavelog get_contacts_adif (full log) ────────► Wavelog   (pulled LOCAL)
   │     └─ POTA hunted-parks CSV (local file)
   └─ merge: awards authoritative when loaded; Wavelog confirmed-flags as fallback
```

Nothing award/log-related is sent to the HorstReporter server. The hot path
(`Evaluate`) is a pure in-memory map lookup; progress refreshes on a slow schedule.

## Awards

| Program | Slot entity | Source | Notes |
| --- | --- | --- | --- |
| **DXCC** | DXCC id (overall/band/mode) | Wavelog ADIF | Satisfied at **confirmed**; collapses `dxcc`>`band`>`mode`. DXCC id `0` ignored. |
| **WAS** | US state (overall+band) | Wavelog ADIF | US-entity QSOs (DXCC 291/110/6) with a valid state; 50 states (DC excluded). |
| **POTA** | park ref `K-1234` | hunted-parks CSV (+ log POTA QSOs) | Satisfied at **worked**. Needs the spot's `pota_ref` (parsed from the cluster comment). |

A program only contributes `needed[]` when its progress is actually **loaded**, so
a missing/cold source never produces false wants. POTA coverage is declared only
when there's real POTA data (a CSV with parks, or POTA QSOs in the log).

Slot shape `Slot{Program, Entity, Band, ModeCl}` generalizes to WAZ/VUCC/IOTA
later (the request already carries cqz/grid/iota).

## Sources

| Source | Reads | Cadence |
| --- | --- | --- |
| `wavelog-adif` | full log via `POST {WAVELOG_URL}/api/get_contacts_adif` (paginated), folded into DXCC/WAS/POTA slots; confirmed from ADIF QSL fields | 30 min |
| `pota-csv` | the operator's POTA hunted-parks CSV export (tolerant: `Reference`/`Park` column or cell-scan) | 1 h |
| `pota` (API) | optional; only useful against an authenticated full-list endpoint (public `api.pota.app/profile` is counts-only) | 6 h |

Snapshots persist to a local JSON store (`-awards-data-dir`, default
`./horstawards-data`, gitignored) so the index is warm after an agent restart.

## Configuration (operator agent)

| Env / flag | Notes |
| --- | --- |
| `WAVELOG_API_KEY` (env/.env) | read-only key; enables the log pull. Reused from the agent's existing Wavelog config. |
| `WAVELOG_STATION_ID` (env) | **required for DCLNext** — `get_contacts_adif` returns 400 without it. |
| `WAVELOG_URL` (env) | defaults to DCLNext. |
| `POTA_HUNTED_CSV` (env) / `-pota-hunted-csv` | path to your POTA hunted-parks CSV; enables POTA. |
| `-awards-data-dir` / `HORSTAWARDS_DATA_DIR` | local snapshot store dir. |

The engine activates whenever a source is configured. `/v1/status` reports
`capabilities.lookup.awards` and an `awards` health block (index slots, programs
loaded, per-source freshness). See `docs/deployment-secrets.md`.

## Running

```bash
WAVELOG_STATION_ID=3427 POTA_HUNTED_CSV=hunted.csv ./run_operator_agent.sh
# (WAVELOG_API_KEY comes from .env)

# Sanity
curl -s 127.0.0.1:9955/v1/status | python3 -m json.tool        # capabilities.lookup.awards, awards{}
curl -s -X POST 127.0.0.1:9955/v1/operate/enrich -H 'Content-Type: application/json' \
  -d '{"permit_lookup":true,"spots":[{"id":"t","call":"W1AW","band":"20m","mode":"SSB"}]}'
```

`needed[]` gains `was` for a US state you still need, and `pota` for an un-hunted
park (when the spot carries a `pota_ref` and a CSV is loaded).

## Frontend

`static/dxcluster.js` `wantInfo` maps `needed[]` → badges (`dxcc`→ATNO gold,
`band`→+BAND, `mode`→+MODE, `was`→+STATE, `pota`→POTA) + the `wanted` sort rank.
`parsePotaRef` extracts the park ref from the spot comment into `pota_ref`. The
contract is additive; no emojis; restrained palette vars.

## Implementation

- `internal/awards/` — engine: `award` (Slot/Index/Evaluate, dxcc/was/pota),
  `adif`, `refdata`, `source` (wavelog_adif, pota_csv, pota, BuildIndex), `store`
  (atomic JSON), and `manager.go` (the `Manager`: sources + scheduler + index).
- `internal/awardcontract/` — shared `WantedSpot`/`WantedResult`/`WantedSlot`.
- `cmd/horstoperator-agent/` — builds `awards.Manager` in `newServer`, runs its
  refresh loops, and merges `Evaluate` results into `/v1/operate/enrich`.
