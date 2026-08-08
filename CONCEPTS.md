# Concepts

Shared domain vocabulary for this project — entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then accretes as ce-compound and ce-compound-refresh process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Spots & stations

### Spot
A single reception report: evidence that one station heard another on a band at a moment in time, carrying the reported signal strength and the two stations' grid locators. Spots are the atomic unit everything else is built from — the live map, the rolling history, and the DX scoring baseline all consume them.

The PSK Reporter feed omits fields that are already encoded in its message topic; a spot is only complete once those topic-derived fields are reconstructed. The PSK Reporter path keeps only FT8/FT4 spots — DX-cluster spots arrive on a separate path and carry more detail.

### Sender
The transmitting station in a Spot — the one whose signal was received.
*Avoid:* TX (used in code only).

### Receiver
The receiving, reporting station in a Spot — the one that heard the Sender and filed the report.
*Avoid:* RX, reporter (used in code only).

### SNR
The reported signal-to-noise ratio of a Spot, in dB. Used both as a strength filter (a viability floor below which a report is too weak to act on) and as a quality input to DX scoring. Higher is stronger; FT8/FT4 routinely decode well below 0 dB.

### Band
An amateur radio band a Spot was reported on (e.g. 20m, 40m). The app analyses a fixed in-scope set spanning the HF and low-VHF bands; out-of-scope microwave bands that occasionally arrive on the feeds are passed through for display but excluded from trend and hot-band analysis.

### Mode
The transmission mode of a Spot. The PSK Reporter ingest keeps only the FT8 and FT4 digital modes; a "DX-cluster" mode marks spots that arrived via the DX-cluster path rather than PSK Reporter.

## Location & targeting

### QTH
The operator's own station — the point-of-view for all analysis (live spot matching, DX scoring, hot-band alerts, the map's projection center). Entered as a callsign or as a Maidenhead locator in the QTH field at the top of the UI; the engine derives the QTH locator (via QRZ/cty.dat for callsigns) from it. "Target" is overloaded in ham radio — it can mean the DX station being chased or the operator's own station. We call the latter QTH to keep the meanings distinct; the DX being worked is just the remote station in a Spot.
*Avoid:* target (deprecated in code/API as of the QTH rename; `?target=` is no longer accepted — use `?qth=`).

### Locator
A Maidenhead grid locator — the compact alphanumeric encoding of a station's position (e.g. JO32, FN31AB) used throughout instead of latitude/longitude.
*Avoid:* grid.

Precision varies by length: a 2-char field, a 4-char square, a 6-char subsquare. The 4-char square (2° longitude × 1° latitude) is the standard working resolution for matching, mapping and clustering. Some baseline keys collapse a square further to its 2×2 block anchor so neighbouring squares aggregate together.


### Surroundings
An option that expands a Locator QTH to also include its eight neighbouring grid squares, widening coverage when a single square is too sparse. Applies only when the QTH is a valid Locator.
*Avoid:* adjacent squares (UI label only).

### Area of interest
A configurable-size region around a home square (a ring radius in grid-square space) that matches any Spot whose Sender or Receiver falls inside it. Additive to QTH matching, it backs region-wide feeds rather than single-station tracking.

## DX cluster

### DX
A long-distance contact or station — the distant end of a path worth chasing. The whole product orients around surfacing DX opportunity; a path counts as DX-grade once it is long enough to be intercontinental rather than local or regional.

### DX-cluster spot
A Spot sourced from a traditional DX-cluster feed rather than PSK Reporter. Unlike PSK Reporter spots (locator-only, QTH-filtered), these carry the spotted station's callsign, frequency, comment, and enriched country/operator metadata, and feed operator tools such as the Chase Queue.

### Spotter
The station that posted a DX-cluster spot — the DX-cluster analogue of a Receiver. The spotted station itself is the "DX call".

### WSPR
A Weak Signal Propagation Reporter spot — a one-way beacon transmission (a low-power transmitter heard by a receiver) ingested from the wspr.live ClickHouse feed. WSPR is a **propagation reference**: beacons run continuously at low power, so WSPR shows whether a path is open at all even when nobody is actively operating (the gap FT8 leaves on dead bands, off-peak hours, and during contests). WSPR SNR is on the same 2500 Hz scale as FT8 but stations transmit at wildly varying power (0.1–100 W+), so raw SNR conflates station capability with propagation — WSPR is therefore kept **out** of the FT8-calibrated conditions baseline (like RBN) and feeds the live stream, activity chart, and a per-band "path open" viability floor instead.

### DXCC entity
The country or territory a callsign belongs to under the ARRL DXCC list — resolved from a callsign-prefix table with an online lookup fallback, and attached to DX-cluster spots as country name and ISO code. The unit most awards and "wanted" decisions are counted in.

## DX condition scoring

### DX Potential Score
A 0–100 estimate of how worthwhile a band looks right now for the QTH, produced by comparing current viable activity against a historical Baseline for the same context. Surfaced per-band and as an overall roll-up. It is a probability hint, not a guarantee — always confirmed on-air.

### Baseline
The historical model of normal band activity that current conditions are scored against, accumulated continuously from observed spots and exposed with depth metadata (history span and event count) so a reader can judge how trustworthy a comparison is.

A baseline bucket is one aggregated count keyed by the context a spot occurred in: Band, Slot-of-day, Distance tier, and SNR tier. Two scopes of bucket are kept, and scoring falls back through them in order: **grid-cluster** (the operator's 6×6 Grid cluster), then **global** (all reporters worldwide). The cluster tier gives operators a baseline scoped to their part of the world instead of the global average, so "unusual opening" detection is accurate for their location.

### Grid cluster
A 6×6 block of Maidenhead squares (12° longitude × 6° latitude, ~900 cover the globe) anchored by its bottom-left square — the granularity the cluster baseline is keyed on. Small enough to distinguish US East Coast from West Coast, large enough that hundreds of reporters contribute in populated areas, so the baseline reaches statistical significance. A cluster is named by its anchor: Berlin (JO62) → cluster `JN68`, New York (FN31) → `EM86`. Coarser than a Locator block (2×2) and finer than a DXPulse region (11 global areas). Derived per Evaluate call from the QTH: a locator maps directly to its cluster anchor; a callsign is resolved via QRZ → locator → anchor, then falls back to the DXCC entity centroid → anchor.

### Slot-of-day
A 30-minute window of the UTC day (48 per day) used as the time dimension of a Baseline bucket. Day-of-week is intentionally collapsed — propagation patterns repeat daily, not weekly — so all observations for the same slot across days aggregate together.

### Distance tier
A coarse bucket of path length, from local through intercontinental, used to key Baseline buckets so a band's typical reach can be compared without storing every exact distance.

### SNR tier
A coarse bucket of signal strength used to key Baseline buckets — the SNR analogue of a Distance tier.

### Confidence
How trustworthy a Score is given the available evidence, expressed as a percentage. Driven by present-sample support and Baseline depth: thin live data or a shallow baseline lowers confidence and also discounts the band's effective Score weight.

### Condition status
The compact green / yellow / red / grey classification of a band or the overall result. Green, yellow and red rank a band against its own Baseline quantiles; grey means insufficient data rather than poor conditions. A human-readable label (Excellent / Good / Fair / Poor) is derived from the same status.

### Long-haul ratio
The fraction of a band's current matched paths that are DX-grade (intercontinental) rather than short-haul — the primary signal that a band is open to distant stations rather than merely busy locally. Also surfaced as "DX ratio".

### Trend
The short-term direction of a band's recent activity — rising, falling, or stable — derived by comparing the first and second halves of a recent per-band activity sparkline (a normalised mini time-series of quality-weighted spot counts).

 
### Hot bands
A recommender that surfaces a few bands worth attention right now for the QTH, each tagged by why: a "surprise" opening on a normally quiet band, a "dx_surge" of unusually long paths, or a "rising" trend. Bands the operator is already on are suppressed.

## Operator tooling


### horstoperator-agent
A trusted local agent running on the operator's own machine that holds operator-private secrets (logbook API key, rig and rotor access) and reverse-proxies the shared backend, so secrets and personal log data never leave the local box. The browser sees a single origin while operator-local endpoints stay local.

### Chase Queue
The operator-facing list of recent DX-cluster spots, enriched per station with award progress and link-quality scoring, that drives map highlights and rig/rotor actions. The surface where "is this worth chasing?" is answered.

### Award slot
The atomic unit of award progress — one workable or confirmable thing (a DXCC entity, a band, a mode, a US state, a park). A spot's "wanted" status is the candidate slots it could fill minus the slots already satisfied in the operator's own log; flags such as ATNO (all-time-new-one), +BAND, +MODE, WAS and POTA name common slot kinds.

### Beam direction
The pattern direction of the operator's UltraBeam RCU-06 antenna: **forward**, **180°** (reverse), or **bi-directional**. Distinct from rotation — rotation (azimuth) stays with the PSTrotator, while beam direction is read from and set on the UltraBeam over MQTT (the `ubctrl` topics). In operation mode it surfaces as three buttons on the left panel, with the live direction sourced from the antenna's own status rather than a local guess. The 180°/reverse state carries a deliberately escalating red alarm (ramping over 90 seconds, with a non-color text cue) because a forgotten reverse is a recurring operational footgun.
*Avoid:* mode (overloaded — the PSTrotator and Spot "mode" concepts are unrelated).
