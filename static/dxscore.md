# DX Potential Score

## For operators: how to use this panel in practice

The **DX Potential** panel helps answer one question:

> *Which bands are most likely worth trying right now from this qth?*

Use it as a **decision aid**, not an oracle.

### What to look at first

- **Best bands**: quick shortlist for where to start calling.
- **Score (0–100)**: higher means conditions currently look more favorable.
- **Confidence (%)**: how trustworthy the score is with current evidence.
- **Condition**: a compact status summary (green/yellow/red/grey).
- **Band rows**: each band’s score, confidence, direction, repeat/long-haul behavior, and trend sparkline.

### How to interpret quickly

- **High score + high confidence** → good candidate to try now.
- **High score + low confidence** → promising but still uncertain; verify on-air.
- **Low score + high confidence** → likely poor at the moment.
- **Grey / low confidence** → not enough data yet.

### Practical operating tips

- If `surroundings` is enabled for a locator, nearby squares are included and can improve stability.
- `CW Min dB` acts as a viability floor for DX scoring input.
	- Spots below that threshold are ignored for DX potential calculations.
	- This reduces overly optimistic results from very weak reports.
- Watch **trend** and **baseline history**:
	- rising trend can signal an opening forming,
	- larger baseline history generally means more reliable comparisons.

### Important caveats

- PSK Reporter coverage varies by geography, time, and active users.
- A good score is a **probability hint**, not a guaranteed QSO.
- Always confirm by listening/calling on-air.

## For interested hams: how the scoring mechanism works

The engine compares **current viable activity** against a **historical baseline** for the same context.

### 1) Input and filtering

For the selected qth (plus optional surrounding locator squares):

- use spots inside the selected time window,
- keep matched qth-related paths,
- deduplicate near-duplicates in short time buckets,
- reject spots below `CW Min dB` for DX potential computations.

### 2) Per-band feature extraction

For each band, compute metrics including:

- activity and uniqueness (spots/min, unique links, repeat ratio),
- distance profile (avg/median/p90/max, long-haul ratio),
- SNR profile (avg/median/p90/peak),
- azimuth direction distribution,
- recent sparkline/trend behavior.

### 3) Baseline model

Baseline buckets are keyed by context such as:

- band,
- slot-of-day (30-minute UTC window),
- distance tier,
- SNR tier,
- in three scopes: **qth-specific** (your own callsign or locator block), **regional** (your DXPulse region — EU, NA, AS, …), and **global** (all reporters worldwide).

Scoring falls back through the tiers: your own history first, then your region's, then the global average. The regional tier matters when your qth has little history of its own (a rare callsign, a new operator): instead of comparing against the whole world, the engine compares against what's typical for your part of the world, so "unusual opening" alerts are accurate for your location. Your region is derived from your qth — a locator maps directly, a callsign is resolved via QRZ and then falls back to the DXCC entity centroid.

This makes comparisons time-aware and location-aware instead of global-only.

### 4) Band scoring and confidence

Each band gets a raw score from normalized components (distance structure, relative activity, and SNR quality), then confidence scaling is applied based on:

- present sample support,
- baseline support depth.

Low support reduces confidence and effective score weight.

### 5) Status classification and overall roll-up

- Band status is classified (green/yellow/red/grey) using baseline quantile context and support checks.
- Overall score/confidence/trend are weighted combinations of per-band results.
- Baseline depth is surfaced as **history minutes** and **event count** for transparency.