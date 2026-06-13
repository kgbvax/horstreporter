# DXPulse Experiment

DXPulse is a compact **band × region dashboard** for current propagation around a target Maidenhead square.

Instead of showing every spot on a map, it answers a faster question:

**Where is propagation open right now, on which bands, and how unusual is it?**

## Getting started

1. Enter a **4-character Maidenhead square** such as `JO42` or `FN31`.
2. Choose a **window** (how much recent live data to include).
3. Choose a **lookback** period (used by Anomaly mode as baseline history).
4. Optionally enable **Adjacent squares** to include the 8 surrounding 4-character squares.
5. Use the **Quality** or **Anomaly** buttons to switch views.

DXPulse refreshes automatically every 30 seconds.

## What the matrix shows

- **Rows** are bands (`80m`, `40m`, `20m`, etc.)
- **Columns** are broad destination regions (`EU`, `NA`, `JA`, `VK`, ...)
- Each cell summarizes recent PSK Reporter activity between your target area and that region on that band

A colored cell means there was recent propagation activity.
A gray cell means **no propagation** in the selected live window.

Inside active cells you will typically see:

- a short label such as **Excellent**, **Good**, **Near normal**, or **Far above normal**
- **spot count**
- **unique path count**
- either **baseline ratio** or **confidence**

## Quality view

**Quality** answers:

> How strong and useful does this path look right now?

This view uses only the **current live window**.
It does **not** compare against historical expectation.

Quality is derived from a mix of:

- current spot count
- unique sender/receiver paths
- number of unique remote grids
- recent signal statistics confidence

Typical labels are:

| Label | Meaning |
| --- | --- |
| `Excellent` | Strong activity with multiple paths and remote grids |
| `Good` | Solid activity and decent path diversity |
| `Fair` | Some propagation, but thinner support |
| `Poor` | Weak or sparse evidence |
| `No propagation` | No current spots in the selected window |

Use **Quality** when you want the practical answer:

- which regions are open now?
- which bands look healthiest?
- where should I point my attention first?

## Anomaly view

**Anomaly** answers:

> Is current propagation below normal, near normal, or above normal for this time of day?

This view compares the live window with a historical baseline built from the selected **lookback** period.
The baseline is aligned to the same approximate **UTC time slot** so you compare “now” with what is usually seen at about this time.

Typical labels are:

| Label | Meaning |
| --- | --- |
| `Far below normal` | Much weaker than the usual baseline |
| `Below normal` | Noticeably weaker than expected |
| `Near normal` | Close to baseline expectation |
| `Above normal` | Better than typical |
| `Far above normal` | Significantly stronger than typical |
| `Baseline sparse` | Not enough history to judge confidently |
| `No propagation` | No current spots in the live window |

When shown, values like `×2.8 baseline` mean the live spot count is roughly $2.8\times$ the expected baseline level for that cell.

Use **Anomaly** when you want to spot:

- unexpectedly good openings
- unusually weak conditions
- paths that are special *today*, not just busy in absolute terms

## How to read the summary cards

Below the matrix you’ll find three compact summaries:

### Best bands

Ranks the busiest / strongest bands for the current target and selected mode.

### Top regions

Shows the destination regions with the most current activity, plus the strongest contributing band for each.

### Hot cells

Lists the single strongest band-region combinations right now.

These cards are shortcuts for scanning; the matrix remains the full picture.

## Quality vs Anomaly — when to use which

- Use **Quality** for **absolute current usefulness**
- Use **Anomaly** for **relative surprise vs history**

A band can be:

- **high quality but not anomalous** → strong, but normal for this time
- **modest quality but highly anomalous** → not huge in absolute numbers, but much better than usual

That distinction is the whole point of having both views.

## Notes and caveats

- DXPulse works best with a **4-character locator target**
- Callsigns are not the intended input here
- Sparse cells can still be meaningful on quiet bands, but confidence will be lower
- `Baseline sparse` means the system lacks enough historical support for a trustworthy anomaly judgment
- Regions are intentionally broad; use the main HorstReporter map for detailed geographic exploration

## Best workflow

A good operating flow is:

1. Open **DXPulse Quality** to see what is currently strongest
2. Switch to **Anomaly** to see what is unusually good or poor
3. Jump back to the main map to inspect details geographically

In short: **DXPulse for fast scanning, HorstReporter map for detailed exploration.**
