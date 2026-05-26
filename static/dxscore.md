# DX Potential score

The **DX Potential** panel answers one practical question:

> *How promising are current band conditions for longer-distance contacts from my target, compared with what is usually observed at this time?*

It is a decision aid built from live PSK Reporter spots plus a rolling baseline. It is **not** a propagation oracle.

## What the panel shows

### Overall score (0–100)

Higher values generally mean better current DX structure (distance + activity + decode quality).

### Overall confidence (0–99%)

Confidence rises with:

- enough recent spots,
- enough baseline support for matching time/band buckets.

Low confidence means “interesting, but noisy/sparse.”

### Overall status + condition

Overall status is derived from score and confidence:

- **grey**: no/weak evidence (e.g. very low confidence)
- **green**: strong
- **yellow**: usable/mixed
- **red**: poor

The label (`Excellent`, `Good`, `Fair`, `Poor`) is mapped from status and score.

### Best bands

Bands are ranked by score (with confidence as tie-breaker). The panel shows the strongest candidates first.

### Per-band details

Each band row includes:

- status color and trend (rising/stable/falling),
- score and confidence,
- suggested operating mode (`ssb`, `cw`, `digital`, `none`),
- dominant direction,
- uniqueness/repeat ratio,
- long-haul ratio,
- recommendation text,
- sparkline of recent relative activity.

## Data and time model

The model uses:

- live PSK Reporter messages (current target only),
- a configurable recent window (defaults around 20 minutes),
- persistent baseline buckets by:
  - band,
  - hour-of-week,
  - distance tier,
  - SNR tier,
- optional target-surroundings expansion for locator targets.

So the comparison is always “now vs normal for this time slice,” not a global all-time average.

## How scoring works (current implementation)

## 1) Filter and deduplicate target-relevant events

Only spots matching your callsign/locator target are used (optionally including surrounding locator squares). Short-window deduplication reduces repeated reports.

## 2) Build per-band metrics

For each band, the engine tracks:

- links and unique links,
- spots/minute,
- distance metrics (avg/median/p90/max),
- SNR metrics (avg/median/p90/peak),
- long-haul ratio,
- direction sectors,
- trend and sparkline.

## 3) Distance and SNR tiers

Distance tiers:

- Tier 0: <500 km
- Tier 1: 500–1500 km
- Tier 2: 1500–3000 km
- Tier 3: 3000–7000 km
- Tier 4: >7000 km

SNR tiers:

- Tier 0: <= -16 dB
- Tier 1: -15 to -9 dB
- Tier 2: -8 to -2 dB
- Tier 3: > -2 dB

## 4) Compute band score from multiple normalized components

Band score combines:

- distance structure,
- activity relative to baseline,
- decode quality,
- then scales by confidence/support.

The result is clamped to 0–100.

## 5) Compute confidence separately

Confidence is primarily driven by:

- current spot support,
- baseline support for the matching bucket.

Sparse data lowers confidence even if raw score is high.

## 6) Assign per-band status

Band status uses baseline quantiles (q25/q75) where available:

- **green**: above q75
- **red**: below q25
- **yellow**: between q25 and q75
- **grey**: insufficient support (too few current spots or weak baseline quantile support)

## 7) Build overall result

Overall score/confidence are weighted by per-band activity, then mapped to overall status/condition/trend.

## Caveats

- PSK Reporter reflects reporting activity, not all on-air activity.
- Regional reporting density is uneven.
- Operator availability and propagation are related but different.
- Baseline quality improves with runtime/history.

Use this as “where to look first,” not as a guarantee.

## Practical reading guide

- **High score + high confidence**: good time to check top bands now.
- **High score + low confidence**: possible opening, verify manually.
- **Low score + high confidence**: likely genuinely weak/ordinary conditions.
- **Mixed top bands**: selective openings; try the listed bands in order.

The goal remains simple: quickly point you to bands that are most worth trying **right now**.