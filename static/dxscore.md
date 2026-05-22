# DX Potential score

The **DX Potential** panel tries to answer a practical operating question:

> *How interesting are the current band conditions for making longer-distance contacts from my target right now, compared with what is usually seen?*

This is **not** a propagation oracle, and it is **not** a contest score. It is a compact summary built from live PSK Reporter spots and a rolling baseline.

## What the numbers mean

### Overall score

The **Score** is a value from **0 to 100**.

- **75–100** → **Excellent**
- **60–74.9** → **Good**
- **45–59.9** → **Fair**
- **0–44.9** → **Poor**

Interpretation:

- A **higher score** means the current spot pattern looks more favorable for meaningful DX than usual.
- A **lower score** means the current pattern is either ordinary, locally biased, sparse, or weaker than typical.

### Confidence

The **Confidence** value tells you how much trust to place in the score.

Confidence increases when there is:

- enough **current activity** in the recent window,
- enough **baseline history** in the matching seasonal bucket,
- a stable comparison between “now” and “normal”.

Confidence is lower when:

- only a handful of spots exist,
- the baseline is still young,
- the bucket is sparse or unusual.

So a score of **82 with 20% confidence** is “interesting but shaky”, while **68 with 85% confidence** is a much more trustworthy indication.

### Best bands

The **Best bands** line lists the top bands according to the current model, taking both score and confidence into account.

These are the bands where the *current mix* of distances and decode strengths looks most favorable relative to the baseline.

## What data is used

The current implementation uses:

- live **FT8/FT4** spots from PSK Reporter,
- a **recent time window** (typically the last 15–30 minutes),
- a persistent **baseline** built from previously observed spots,
- comparison buckets grouped by:
  - **band**,
  - **hour of week**,
  - **distance tier**,
  - **SNR tier**.

This means the algorithm compares “what is happening now” against “what usually happens around this weekday/time on this band”.

## How the scoring works

## 1. Current spots are filtered for your target

The live window contains only spots relevant to the current target selection:

- callsign or locator target,
- optionally including surrounding locator squares.

The algorithm then groups those current spots by **band**, **distance tier**, and **SNR tier**.

## 2. Each spot is classified into distance and SNR buckets

### Distance tiers

Spots are grouped by approximate path length between sender and receiver locator:

- **Tier 0:** 0–500 km
- **Tier 1:** 500–1500 km
- **Tier 2:** 1500–3000 km
- **Tier 3:** 3000–7000 km
- **Tier 4:** >7000 km

Why this matters:

- short paths can indicate local/regional activity,
- longer paths are more relevant to what hams usually mean by “DX potential”.

### SNR tiers

Spots are also grouped by decode strength:

- **Tier 0:** below -18 dB
- **Tier 1:** -18 to -10 dB
- **Tier 2:** -10 to -2 dB
- **Tier 3:** above -2 dB

Why this matters:

- weak but decodable long-distance reports can still indicate excellent propagation,
- very strong local reports should not dominate the DX score too much.

## 3. A baseline bucket is selected

For each band, the algorithm looks up the corresponding **hour-of-week baseline**.

That captures repeating seasonal behavior such as:

- daytime vs nighttime differences,
- weekday/hour patterns,
- the fact that some bands are normally busy or quiet at certain times.

This is important because “20 spots on 20m” may be ordinary at one time, but remarkable at another.

## 4. The current distribution is compared with the baseline distribution

For each band, the algorithm compares the **share of current spots** in each `(distance tier, SNR tier)` cell to the **share normally seen** in the same cell.

In plain language:

- Are there **more medium/long-distance paths than usual**?
- Are **better decode-strength patterns** appearing than normal?
- Is the current activity skewed toward **real reach** rather than just local chatter?

The model uses small smoothing priors so that empty or sparse baseline cells do not produce wild jumps.

## 5. Different buckets get different importance

Not all spots are equally interesting for DX.

The current weighting gives:

- **more weight to longer distances**,
- **moderate-to-weak decodable paths meaningful weight**,
- **less dominance to strong short-haul spots**.

That design is intentional. A huge pile of nearby loud signals should not masquerade as “great DX”.

## 6. The band score is normalized to 0–100

The weighted comparison is turned into a human-friendly band score:

- **50** is roughly neutral / normal,
- above **50** means better-than-baseline DX structure,
- below **50** means weaker-than-baseline or locally biased conditions.

The score is then clipped to a stable 0–100 range to avoid extreme swings.

## 7. Confidence is calculated separately

The confidence model currently considers two main ingredients:

- **How many current samples** exist on the band,
- **How many baseline samples** exist for the matching seasonal bucket.

That means:

- a strong score based on one or two spots will not look highly reliable,
- a moderate score backed by rich current and historical data can carry high confidence.

## 8. Overall DX Potential is built from the best bands

The overall score combines the per-band results using a weighted blend.

Bands with better:

- score,
- confidence,
- and sample support

have more influence on the final overall number.

This avoids the overall score being hijacked by one noisy band with too little evidence.

## Why this approach makes sense

The reasoning behind this design is:

1. **Raw spot count alone is not enough.**
   A band can look “busy” without being good for DX.

2. **Distance matters.**
   Longer successful paths are more relevant to the operator’s real DX opportunity.

3. **Signal strength matters, but not in a simplistic way.**
   Very strong local signals are common; weaker long paths can be more informative.

4. **Time context matters.**
   Conditions should be judged relative to what is *normal for this band and this time*.

5. **Confidence matters.**
   Any honest score needs to admit when the evidence is thin.

## Important caveats

This score should be read as a **decision aid**, not ground truth.

Limitations include:

- PSK Reporter reflects digital activity, not all on-air activity,
- station density is not uniform by region,
- strong reporting areas can bias visibility,
- propagation quality and operator availability are not the same thing,
- baseline quality improves only after enough historical data has accumulated.

In other words: the score tells you **what the spot structure suggests**, not what the band *guarantees*.

## Practical reading guide

- **High score + high confidence** → strong hint to check those top bands now.
- **High score + low confidence** → interesting opening may be starting; verify manually.
- **Low score + high confidence** → likely ordinary or poor DX structure right now.
- **Mixed band scores** → conditions may be selective; inspect the listed top bands rather than the overall number only.

## Current implementation status

This is an intentionally practical first version. Future improvements may include:

- richer baseline retention,
- target-aware baselines instead of only global historical reference,
- improved handling of uniqueness/repeated links,
- additional band-quality features,
- trend arrows and historical mini-charts.

For now, the goal is simple: **help the operator quickly decide where DX is most likely worth checking right now**.