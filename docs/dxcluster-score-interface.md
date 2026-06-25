# DX Cluster module — contact-likelihood score interface (v1)

Status: **draft / design**. Slice B of the HorstOperator DX Cluster ("Chase Queue") module.
Defines what the per-spot score *means*, its data contract, where it is computed, and the v1 heuristic.

## What the score is

`score` = an estimate of **P(I complete a QSO with this station in the near term)**, expressed 0–100,
plus a `decision` (GO / WATCH / WAIT) and a `confidence` (how much evidence backs it). It answers the
one question the Chase Queue exists for: *is this spot worth my attention (and worth turning the beam)?*

It is **not** "is the band open in the abstract" — that is one input. It folds in observed path
activity, geometry, the DX's workability (pileup/rarity), and mode.

## Where it is computed (and the seam)

The score is defined as a **pluggable interface** so the source can move without touching the UI:

- **v1 = client-side heuristic.** The browser already holds everything cheap signals need: the full
  rolling spot window (`state.liveSpots`) and per-band conditions (`/api/dx_conditions`, already fetched
  by Band Lab). No new backend endpoint required.
- **seam → backend** `POST /api/score` (operator-agnostic: station+spot geometry + conditions, no
  secrets) when we want server-side path modelling or heavier compute.
- **seam → external predictor** (VOACAP / ML — the "other backend") replaces only the `base`
  component below; the decision mapping and UI stay put.

```
scorer(input: ScoreInput): ScoreResult
  v1Heuristic   — client-side, this doc
  backendScorer — POST /api/score   (later)
  externalScorer — VOACAP/ML        (later, swaps `base` only)
```

## Contract

### ScoreInput

```jsonc
{
  "spot":    { "call":"JA3XYZ", "lat":35.6, "lng":139.7, "band":"15m", "mode":"FT8",
               "snr": -8, "ageSeconds": 12, "comment":"calling EU" },
  "station": { "lat":50.0, "lng":8.0 },          // operator; not secret
  "band_conditions": { "score": 62, "confidence": 0.7, "trend":"improving" }, // from /api/dx_conditions
  "path_window":  [ /* spots from liveSpots, same band, last N min */ ],
  "needed":   ["mode"],                           // from enrich — feeds rarity/pileup
  "antenna":  { "azimuth_deg": 70, "beamwidth_3db_deg": 60, "reachable": true } // optional, agent
}
```

### ScoreResult

```jsonc
{
  "score": 64,
  "decision": "watch",            // go | watch | wait  — see thresholds
  "confidence": 0.58,             // 0..1
  "factors": [                    // explainability; drives the score tooltip
    { "key":"path",     "label":"Path alive — 9 spots EU↔JA on 15m",  "contribution": +38 },
    { "key":"band",     "label":"15m fair (DX 62, improving)",         "contribution": +16 },
    { "key":"geometry", "label":"9,200 km · one-hop favourable",       "contribution": +8  },
    { "key":"pileup",   "label":"New-mode only — modest pileup",       "contribution": -2  },
    { "key":"mode",     "label":"FT8 — completes at low SNR",          "contribution": "×1.15" }
  ]
}
```

`factors` are the *reason* — shown on hover/expand so the operator can see **why** it says what it says
(an operating tool must be auditable, not a black box). Sum of additive contributions ≈ pre-mode score;
mode is a multiplier.

## v1 heuristic

Additive base, then a mode multiplier, clamped to 0–100.

| factor    | weight | source                | intent |
| --------- | -----: | --------------------- | ------ |
| **path**  |  ~0.45 | `path_window` (client) | dominant, empirical: is *this* path actually alive now |
| **band**  |  ~0.25 | `/api/dx_conditions`   | general propagation for the band |
| **geometry** | ~0.15 | station↔spot great-circle + grayline | hop-distance sweet spots; big bonus if path crosses grayline now |
| **pileup** | ~0.15 | `needed` + spot-rate in window | rarity/pileup penalty (ATNO = biggest); QRT/QSY in comment → expire |

**Path factor (the heart).** From `liveSpots`, count spots in the last *N* min on the same band whose
endpoints connect *near me* and *near the DX*:

```
near_me(p)  = dist(p.endpoint, station) < R_me   (e.g. 1500 km)
near_dx(p)  = dist(p.endpoint, spot)    < R_dx   (e.g. 1500 km)
contributes(p) = same band && ( (near_dx(sender) && near_me(receiver))
                              || (near_me(sender) && near_dx(receiver)) )
path_raw = Σ recency(p) · snrMargin(p, mode)        # saturating
```
More matching spots, more recent, stronger SNR over the mode floor → path open → high score.
**Zero path activity ⇒ path≈0 ⇒ low confidence** (the band may be theoretically open but dead on this path).

**Mode multiplier.** Completion threshold differs by mode (FT8 ≈ −20 dB, CW ≈ −10, SSB ≈ +3). Marginal
paths still work on FT8, so mode scales the effective signal margin: `×1.15` FT8, `×1.0` CW, `×0.9` SSB
(tunable). `snrMargin(p,mode)` measures observed SNR above that mode's floor.

**Confidence.** Driven by *evidence volume*, not score: `confidence = f(path_spot_count, band_confidence)`.
Thin evidence → low confidence → decision capped below GO (see below). This reuses the principle from the
Band Stats confidence fix (confidence is a real value, never a flat 100%).

## Decision thresholds — reuse Band Lab exactly

Mirror `buildGlobalDecision` (`static/band-lab.js:766`) so GO/WATCH/WAIT mean the same thing app-wide:

```
GO    : score >= 70 && confidence >= 0.55
WATCH : score >= 50 || confidence >= 0.40
WAIT  : otherwise
```

Confidence-gating is what stops the queue shouting GO on a single lucky spot: high score + thin evidence
lands in WATCH until the path proves itself. The meter color in the UI is the `decision`; the number is
`score`.

## Open questions

- **Path radii / window** — `R_me`, `R_dx` (1500 km?) and window length (15 min?). Tune against real data.
- **Grayline weight** — how big a bonus when the path crosses the terminator (it can be the difference).
- **Comment mining** — v1 only flags `QRT`/`QSY` (expire). Worth parsing `up N`/`listening EU` later.
- **Antenna factor** — fold the agent's `reachable`/beam-gain into a small adjustment, or leave v1 neutral.
- **Dependency** — confirm `/api/dx_conditions` per-band `score`/`confidence` is trustworthy post the
  recent baseline-normalization + confidence fixes before leaning 0.25 of the score on it.
