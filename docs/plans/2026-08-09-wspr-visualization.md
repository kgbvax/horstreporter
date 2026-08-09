---
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
title: "WSPR Visualization — Region-Scoped Map Layer + Band × Region Matrix"
date: 2026-08-09
---

## Problem Frame

WSPR spots are now ingested (wspr.go) and broadcast to all connected clients, but they're mixed into the QTH-centric main map with no distinct visualization. The operator can't distinguish WSPR beacon paths from FT8/DX-cluster spots, and there's no surface that answers "which bands are open to which regions right now?" — the classic open-path-discovery question WSPR is uniquely suited to answer.

## Goal

Two client-side visualization surfaces for WSPR:

- **A: Region-scoped WSPR map layer** — a distinct overlay on the existing map showing WSPR paths where either end is in the operator's 6×6 grid cluster. Answers "which directions reach my area?"
- **B: Band × region matrix panel** — a compact grid (bands × 11 DXPulse regions) showing which bands have WSPR paths open to which regions. Answers "which bands are open to which regions right now?"

Both are client-side only (no backend changes). WSPR spots carry both `locator` (transmitter) and `reporterLocator` (receiver) in the SSE stream, so region classification works client-side.

## Settled Decisions

| Decision | Provenance | Rejected Alternative | Reason |
|---|---|---|---|
| WSPR broadcast to ALL clients (not QTH-filtered) | user-approved | QTH-filter like FT8 | WSPR is a global propagation reference; operator's station rarely appears as either end |
| Region scope = operator's 6×6 grid cluster | user-approved | surroundings (3×3) or global | Cluster balances relevance and data volume; matches baseline granularity |
| Matrix data source = client-side live window (state.liveSpots) | user-approved | backend /api/wspr_matrix endpoint | Matches "what's open right now" intent; no backend work needed |
| WSPR kept out of FT8 conditions baseline (reference-only, like RBN) | user-approved | Feed into conditions baseline with power normalization | WSPR power varies wildly (0.1–100W+); raw SNR conflates station capability with propagation |
| WSPR data source = wspr.live ClickHouse HTTP API | user-approved | WSPRnet form POST or CSV exports | Cleanest JSON API, real-time-ish, no auth |
| Poll window = 300s (5 min) | user-approved | 120s | WSPR transmits on 2-min cycles; 120s catches at most one cycle |

## Scope Boundaries

### In scope
- `static/utils.js` — JS region classifier + cluster anchor helper
- `static/renderers.js` — WSPR map layer (Mercator)
- `static/azimuth-runtime.js` — WSPR spots in azimuth view
- `static/state.js` — wsprLayer state
- `static/wspr-matrix.js` — new band × region matrix module
- `static/index.html` — matrix panel container + toggle button
- `static/app.js` — wire initWsprMatrix + updateWsprMatrix
- `static/style.css` — matrix panel + cell heatmap styles
- Tests for the JS region classifier and matrix aggregation

### Out of scope
- Backend changes (no new endpoints, no schema changes)
- Longer-window matrix (beyond the ≤60-min SSE stream) — follow-up if needed
- WSPR-specific marker tooltips (can be added later)
- The per-band "WSPR path open" badge in Band Stats (already implemented)

## Architecture

### Shared prerequisite: JS region classifier

Add `regionForLocator(loc)` to `static/utils.js`, mirroring `dx_regions.go:48-83` (the 11-region bounding-box classifier: EU, NA, SA, AF, AS, OC, AN, JA, VK, KH6, CAR). Also add `locatorClusterAnchorJS(loc)` (floor square coords to multiples of 6, matching `spot.go:locatorClusterAnchor`).

The frontend has no `locatorToLatLng` equivalent today. Add one (mirror `spot.go:locatorToLatLng`) so the region classifier can convert a locator to lat/lng before applying the bounding boxes.

### Feature A: Region-scoped WSPR map layer

Mirror the DX-cluster layer pattern (`renderers.js:syncDxClusterMarkers` / `renderDxClusterMarkers` / `clearDxClusterMarkers`):

1. **`splitSpotSources`** (renderers.js:95-106) — extend to also split out `wsprSpots` by `sourceType === 'wspr'`.
2. **New `syncWsprMarkers(spots)`** — fingerprint check, create `state.wsprLayer = L.layerGroup()`, call `renderWsprMarkers(wsprSpots)`.
3. **New `renderWsprMarkers(wsprSpots)`** — `L.circleMarker` with a **distinct style**: smaller radius (3), dashed border or different color (e.g. cyan/teal fill with white border), so WSPR is visually separable from DX-cluster (white border, band-color fill) and regular spots (grid squares). Bind a simple tooltip (band, SNR, tx→rx).
4. **Region-scope filter** — only render WSPR spots where `regionForLocator(spot.locator)` OR `regionForLocator(spot.reporterLocator)` matches the operator's region. Compute the operator's region from their QTH locator via `regionForLocator(qth)`. If the QTH is a callsign (no locator), skip the region filter and show all WSPR (fallback).
5. **Call `syncWsprMarkers`** from `updateMapVisualization` (renderers.js:224, alongside `syncDxClusterMarkers`).
6. **`clearWsprMarkers()`** — remove layer, null state. Call on stream stop (alongside `clearDxClusterMarkers`).
7. **`static/state.js`** — add `wsprLayer: null`.
8. **`static/azimuth-runtime.js`** — mirror `drawDxClusterSpots` with `drawWsprSpots` (distinct stroke style), region-scoped. Call from the same sites as `drawDxClusterSpots` (lines ~1719, 1750, 1768).
9. **Toggle** — `show-wspr-spots` checkbox already gates via `getRenderableMapSpots` (app.js:645-658). The layer draws from the already-gated spots.

### Feature B: Band × region matrix panel

Mirror the Band Stats panel pattern (`band-lab.js` / `index.html:218-242`):

1. **`static/index.html`** — add `#wspr-matrix-window` panel container (header, close button, `#wspr-matrix-body` grid). Add a `#wspr-matrix-toggle` button overlaid on the map (next to `#band-stats-toggle`).
2. **`static/wspr-matrix.js`** (new module):
   - `initWsprMatrix()` — wire toggle/close, restore visibility from `localStorage` key `wsprMatrixEnabled`.
   - `updateWsprMatrix()` — aggregate `state.liveSpots` where `sourceType === 'wspr'`:
     - For each WSPR spot, classify `regionForLocator(spot.locator)` (transmitter region) and `regionForLocator(spot.reporterLocator)` (receiver region).
     - Build `Map<band, Map<region, count>>` — each cell counts how many WSPR paths involve that region on that band (either as transmitter or receiver).
     - Render a grid: rows = bands (sorted by wavelength), columns = 11 regions (in `AllRegions` order: EU, NA, SA, AF, AS, JA, OC, VK, KH6, CAR, AN).
     - Each cell colored by count: 0 = empty/dark, 1+ = lit (intensity scales with count). A "path open" indicator.
     - Throttled (300ms, matching band-lab).
   - `setWsprMatrixVisible(visible)` — toggle `.is-hidden`, call `onLayoutChange` to reflow map.
3. **`static/app.js`** — call `initWsprMatrix()` at boot (alongside `initBandLab`), call `updateWsprMatrix()` on stream changes (alongside `updateBandLab`).
4. **`static/style.css`** — matrix grid layout, cell heatmap colors, panel positioning (mirror `.band-lab-window`).

## Implementation Units

### U1: JS region classifier + cluster anchor helper
**Files:** `static/utils.js`
**Test file:** `test/region-classifier.test.js`
**Patterns:** Mirror `dx_regions.go:48-83` (bounding boxes) and `spot.go:locatorClusterAnchor` (floor to multiples of 6). Also add `locatorToLatLngJS(loc)` mirroring `spot.go:locatorToLatLng`.
**Test scenarios:**
- `regionForLocator("JO62")` → "EU" (Berlin)
- `regionForLocator("FN31")` → "NA" (New York)
- `regionForLocator("QF22")` → "OC" (Australia)
- `regionForLocator("PM96")` → "JA" (Japan)
- `regionForLocator("W1AW")` → "" (callsign, not a locator)
- `regionForLocator("")` → "" (empty)
- `locatorClusterAnchorJS("JO62")` → "JN68"
- `locatorClusterAnchorJS("FN31")` → "EM86"

### U2: WSPR map layer (Mercator)
**Files:** `static/renderers.js`, `static/state.js`
**Test file:** `test/wspr-layer.test.js` (or extend existing renderer tests)
**Patterns:** Mirror `syncDxClusterMarkers` / `renderDxClusterMarkers` / `clearDxClusterMarkers` (renderers.js:104-174). Extend `splitSpotSources` (renderers.js:95-106).
**Test scenarios:**
- `splitSpotSources` with mixed spots → wsprSpots separated correctly
- `syncWsprMarkers` with WSPR spots in operator's region → markers created
- `syncWsprMarkers` with WSPR spots outside operator's region → markers filtered out
- `clearWsprMarkers` → layer removed, state nulled

### U3: WSPR spots in azimuth view
**Files:** `static/azimuth-runtime.js`
**Patterns:** Mirror `drawDxClusterSpots` (azimuth-runtime.js:510-531). Distinct stroke style. Region-scoped.
**Test scenarios:**
- WSPR spots drawn with distinct style (not mixed with regular or DX-cluster spots)
- Region-scoped: out-of-region WSPR spots not drawn

### U4: Band × region matrix panel
**Files:** `static/wspr-matrix.js` (new), `static/index.html`, `static/app.js`, `static/style.css`
**Test file:** `test/wspr-matrix.test.js`
**Patterns:** Mirror `band-lab.js` (init/toggle/update/throttle) and `index.html:218-242` (panel container).
**Test scenarios:**
- `updateWsprMatrix` with WSPR spots → grid cells lit for active band×region combos
- `updateWsprMatrix` with no WSPR spots → all cells dark
- Toggle shows/hides panel
- Region classification matches backend (EU/NA/SA/AF/AS/OC/AN/JA/VK/KH6/CAR)

## Dependencies and Sequencing

1. **U1 first** — the region classifier is a prerequisite for both U2 (region-scoping) and U4 (matrix aggregation).
2. **U2 and U3 in parallel** — Mercator and azimuth layers are independent.
3. **U4 after U1** — the matrix needs the classifier but is independent of the map layer.

## Risks

- **WSPR spot volume**: with the 300s poll window and broadcast-to-all, each poll can produce thousands of spots. The region-scoping filter (U2/U3) keeps the map manageable. The matrix (U4) aggregates by band×region so volume doesn't affect it.
- **wspr.live reliability**: third-party volunteer service. The poller already degrades gracefully. The visualization features are additive — if WSPR is down, the map/matrix just show no WSPR data.
- **JS region classifier accuracy**: must match `dx_regions.go` bounding boxes exactly. Test with known locators for each region.

## Execution Direction

Standard implementation. Frontend tests via `npm test` (vitest). Verify with `npm run check` (typecheck). No backend changes, so `go test` is a regression check only.