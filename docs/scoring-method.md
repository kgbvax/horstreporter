# HorstProp Link-Quality Scoring — Domain View

This document describes *what* the **horstprop** score means and *how* it
reasons about an HF path, at the domain level. It deliberately avoids code
structure, types, and storage internals. For the full engineering spec see
[`horstprop.md`](./horstprop.md). For the separate **DX Potential** panel that
lives in the main HorstReporter UI (a different scoring system entirely) see
[`../static/dxscore.md`](../static/dxscore.md).

> **Two different scorers, don't confuse them.** HorstReporter's *DX Potential*
> panel scores *which bands look generally promising from a target right now*.
> The standalone **horstprop** service answers a narrower, per-spot question:
> *given this specific DX station on this specific frequency, how good is the
> path from my home station?* This document is only about **horstprop**.

## 1. The question the score answers

> *A DX station just appeared on the cluster. From **my** home station, on
> **this** band, how workable is that path — right now?*

The input is a single spot (DX callsign, frequency, optionally a grid). The
output is a compact verdict for that one path:

- a **score** 0–100 (higher = more workable),
- a **letter grade** A / B / C / D (or `?` when unknown),
- a **confidence** 0–1, and
- a **reason** plus a **per-layer breakdown** explaining how it was reached.

It is a **decision aid** for "is this spot worth chasing", not a guarantee.

## 2. First step: locate the DX and frame the geometry

Before scoring, the engine has to know *where* the DX is:

- if the spot carries a grid, use it;
- otherwise resolve the callsign to a DXCC-entity centroid (via cty.dat);
- if neither works, the path is **unresolved** — it returns a neutral 50 with
  very low confidence and says so, rather than guessing.

Once located, it derives the **distance and bearing** from the home station to
the DX. These are shared by every scoring layer.

## 3. The layered method

horstprop scores by combining up to three independent **layers** of evidence,
each of which can *abstain* when it has nothing useful to say. Silence is never
treated as a low score — a layer with no data simply steps aside.

### Layer 1 — Empirical ("is this path actually being heard?")

The strongest evidence is real reception. A rolling store is fed read-only from
HorstReporter's public stream and holds recent reception reports near the home
station's **area of interest**. When a spot comes in, Layer 1 looks for recent
reports on the same band whose paths land **near the DX region** (within a
configurable number of grid-square rings).

If such reports exist, it scores the path from their **signal strength** —
emphasising the best report, tempered by the median — and then **discounts for
age**: a report from 25 minutes ago counts for less than one from 2 minutes
ago. More reports and fresher reports raise confidence.

> Note: the public feed is **locator-level**, so Layer 1 reasons about
> *path/region*, not specific callsigns. It answers "is this corner of the
> world being heard near me on this band right now", which is exactly the
> empirical question that matters.

### Layer 2 — MUF gate ("is the band physically open on this path?")

The second layer is a **physics sanity check** rather than a score. Using the
KC2G real-time MUF nowcast, it samples several **control points** along the path
(the midpoint and points roughly 1500 km in from each end) and takes the
**lowest** Maximum Usable Frequency found — the weakest link governs the path.

It then compares the operating frequency against that path MUF and produces a
**gate multiplier** in 0–1:

- well below the MUF → **open**, gate ≈ 1 (no penalty);
- approaching the MUF → **marginal**, gate tapers down;
- above the MUF → **above_muf**, gate falls toward 0 (path likely closed).

The gate is applied **multiplicatively** to the base score: it can knock down an
otherwise-promising path that is physically shut, but it cannot by itself invent
a good score.

VHF and up (≥ 50 MHz, e.g. 6 m / 4 m / 2 m) is **exempt** — those bands open via
sporadic-E, tropo, and meteor scatter, which the F-layer MUF model does not
describe — so there the scoring leans on empirical evidence instead.

### Layer 3 — Propagation model (scaffolded, currently abstains)

A model layer (ITU-HFProp / VOACAP-style point-to-point prediction) is wired
into the same blend but **not built into this binary** yet; it always abstains
today. When present it would provide a base score and confidence from a physical
model for paths with no live observations.

## 4. How the layers blend

The layers are combined **empirical-first**:

1. **Base score**: Layer 1 (empirical) is the base when available; otherwise the
   model (Layer 3) is the fallback base; otherwise a neutral 50.
2. **Agreement adjustment**: when both empirical and model are present and
   *agree*, confidence rises; when they *disagree*, confidence is cut.
3. **Apply the gate**: the MUF gate multiplies the base score — but its bite is
   **softened by empirical strength**. Confident, fresh observations raise a
   gate floor, so a 30-minute-old interpolated MUF can't crush a path that is
   *demonstrably being heard*. Weak or absent empirical evidence lets the gate
   apply in full.
4. **Confidence**: derived from whichever evidence dominated — strong empirical,
   a decisive (closed) gate, or the MUF gate alone when it is the only signal.

The guiding principle: **real observations outrank the model**. The physics gate
is a check on optimism, not an override of reality.

## 5. From score to grade

The final 0–100 score is banded into a letter for quick reading:

| Grade | Score | Meaning |
|-------|-------|---------|
| **A** | ≥ 75  | Strong path — go for it |
| **B** | 55–74 | Good / workable |
| **C** | 35–54 | Marginal |
| **D** | < 35  | Poor |
| **?** | —     | Unknown: no score, or confidence below 0.2 |

A grade is only assigned when confidence is high enough to mean something;
otherwise the path is reported as **unknown** rather than guessed.

## 6. Reading a result

- **High score + high confidence** → path is live and open; chase it.
- **High base but gated down** → band is physically marginal/closed on this
  path despite interest; the reason line will say so.
- **Empirical override note** → observations are beating a stale MUF gate; trust
  the on-air evidence.
- **Neutral 50 / `?`** → DX couldn't be located, or no layer had anything; this
  is an honest "don't know", not a verdict.

## 7. Deliberate boundaries

- The score judges **one path from one home station**, not general band
  conditions.
- Empirical evidence is **region/path level** (locator-based), inheriting the
  public feed's coverage gaps that vary by geography, time, and active listeners.
- The MUF gate is an **F-layer** model and intentionally does not apply to VHF+.
- Confidence and the abstain-on-silence design mean the service prefers saying
  *"unknown"* over emitting a confident wrong answer.
- A good score is an invitation to listen and call — on-air confirmation always
  wins.
