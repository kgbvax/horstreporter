# Conditions dock — regroup Band stats, Propagation and Typical openings

Date: 2026-09-28. Mockup: `tmp/dock-mockup/index.html` (hash states `#now`, `#usual`, `#usual:20m:NA`).

## Goal

Three analytical panels grew separately (Band stats: docked left column; Propagation
and Typical openings: floating cards over the map). They answer one question — *what
can I work from my QTH?* — at different horizons, but present as three unrelated
things with three chrome systems and three toggles. Replace them with **one docked
column on the right** that never covers the map, with a single two-way switch:
**Now** (live, last hour) and **Typical** (last 30 days by time of day).

This is a layout regrouping, not a restyle: existing component styles, band palette,
type and chip vocabulary stay as they are. Only container, placement, toggles and the
handful of new composites described below change.

## Decisions (agreed on the mockup)

| # | Decision |
|---|---|
| D1 | One right-docked, in-flow column `#cond-dock` (sibling of `#map-stack`, like `#band-lab-window` is today on the left). Never overlays the map. Phones: full-screen sheet, as Band stats does today. |
| D2 | Header: `Conditions from <QTH>` · `Chase queue` as tabs of the same dock; `×` closes. Chase queue (opmode, `dxcluster.js`) moves into the dock as a tab instead of docking its own `#chase-queue` column. |
| D3 | Horizon switch is the app's standard segmented control (`btn-group segmented-equal`): **Now / Typical**. No sublabels. |
| D4 | **Now** = one row per *enabled* band, all visible, nothing to expand: band header · verdict (`lively` / `quiet` / `no reports`; blank when normal; qualifiers `longer reach`, `low sample`) · `reports / normal` pair · mini distance-vs-SNR plot on shared axes · the Propagation matrix cells for that band. Source chips (`WSPR PSKR RBN DXC`) and the time-range select sit above the table. |
| D5 | Verdict words: `above normal` → **lively**, `below normal` → **quiet**, `not scored` → **no reports**. P90 not shown in the row. |
| D6 | Band trend line moves to the **left rail**: the existing `.band-spark` gains a solid zero line and the dashed normal-for-this-hour. The dock shows no trend line. |
| D7 | **Typical** = *Next 3 h* schedule (bars per band→region opening, 19:00–22:00 local axis, `n of 30`, outlined = open now, fade-out when the window runs past the axis) followed by *By region* almanac lanes. Lane click → existing by-month view; Back / Escape returns. |
| D8 | One continuous now-line per region block (2 px, `--text-color`); not-enough-data slots hatched per slot (existing `.almanac-run.is-unknown`). |
| D9 | Closed dock collapses to the existing hot-band pill row (`#hot-band-indicator`) plus a `Conditions` panel-toggle that reopens it. |
| D10 | Time travel stays with the map (bottom bar). Forecast layer untouched. |
| D11 | Copy: sentence case, no "local time" / "last 30 days" qualifiers in labels; footnotes one or two sentences. |

## Design

```
┌ #controls ─┬──────── #map-stack ────────┬── #cond-dock ──────────────────┐
│ filters +  │  map (zoom, time travel,   │ Conditions from JO32 | Chase   ×│
│ band rail  │  drill chip only)          │ [ Now | Typical ]               │
│ w/ spark   │                            │ Now: src chips … [1 h]          │
│ zero+normal│  collapsed: hot-band pills │   band | verdict | plot | cells │
│            │  + "Conditions" toggle     │ Typical: Next 3 h · By region   │
└────────────┴────────────────────────────┴─────────────────────────────────┘
```

Data stays as is: `band-lab.js` (`/api/dx_conditions`), `wspr-matrix.js`
(`/api/prop_intel/v2`), `almanac.js` (`/api/almanac`, `/api/almanac/season`),
`hot-band-indicator.js` (`/api/hot_bands`). The three modules keep their fetch,
cache and render logic; only their containers and the composition change.

## Units of work

### U1 — Dock container and toggles
- `static/index.html`: add `<aside id="cond-dock">` after `#map-stack` with header
  (tabs + close), horizon `btn-group`, `#cond-sub`, `#cond-body`. Remove
  `#band-lab-window`, `#wspr-matrix-window`, `#almanac-window` and the three
  `.panel-toggle` buttons; add `#cond-open` next to the hot-band pills.
- `static/style.css`: new `.cond-*` block (dock flex column, header, body; phone
  media query mirrors the existing `.band-lab-window` full-screen rule). Delete the
  `.wspr-matrix-window*`, `.almanac-window*` and `.band-lab-window*` placement blocks
  and the phone overrides for them. Keep all `.wspr-matrix-table`, `.wspr-badge`,
  `.wspr-src-chip`, `.almanac-*`, `.band-lab-card/chart` component rules.
- `static/app.js`: replace the three `init*` `onLayoutChange` callbacks with one dock
  callback (`map.invalidateSize()` + azimuth re-render — the dock is in-flow, like
  Band stats was). One `localStorage` key `condDockOpen` replaces
  `bandLabEnabled` / `wsprMatrixEnabled` / `almanacEnabled` (migrate: open if any was
  `true`, then remove). Horizon in `condHorizon`.
- `static/panel-drag.js`: no longer used by these panels; remove the two
  `makeDraggable` calls and the `wsprMatrixPos` / almanac `POS_KEY` storage.
- `static/wspr-matrix.js`: drop `--map-toggles-bottom/right` publishing (the toggle
  row no longer holds these buttons); `hot-band-indicator` centring rule simplifies.

### U2 — Now view
- New `static/cond-now.js` composes one table from the two existing modules:
  - Rows: enabled bands (same source as `wspr-matrix.js` `activeBands`).
  - Verdict cell: `band-lab.js` `bandActivityLabel` / `bandHeadText` → relabel
    `above normal`→`lively`, `below normal`→`quiet`, `not scored`→`no reports`;
    empty for `normal`. Count pair from `activity_by_bin` sum vs
    `baseline_activity` × window (already computed for the bars).
  - Plot cell: `band-lab.js` `computeScatterData` + `drawScatterChart` into a
    100×44 canvas with a **fixed** shared axis (distance cap = current
    `globalDistanceCapKm`, SNR −25…+10 dB) so rows compare.
  - Region cells: `wspr-matrix.js` `renderCell` unchanged (viridis/inferno, S/C
    badges, chevrons, drill-down `aria-selected`, roving tabindex).
  - Above the table: `renderSourceChips()` (existing) + the Band-stats time-range
    `<select>` (existing `band-lab-time-range` semantics and key).
  - Legend: matrix legend + one sentence for the plot floors.
- `band-lab.js` / `wspr-matrix.js`: export the pieces above instead of rendering
  into their own windows; `renderBandCards` (per-band cards with the bar chart) is
  removed — its "Reports over time + baseline" moves to the rail (U3). Keep
  `updateBandLab` / `updateWsprMatrix` as the data entry points app.js already
  calls; they now hand results to `cond-now.js`.

### U3 — Rail sparkline with zero + normal
- `static/renderers.js` `updateBandLabels`: the polyline stays; add
  `<line class="spark-zero">` at the baseline and `<line class="spark-normal">` at
  the band's expected rate. Expected rate comes from the last
  `/api/dx_conditions` response (`baseline_activity` × `baseline_local_scale`,
  same scaling `computeActivityChartData` applies); `band-lab.js` exposes it via a
  small getter (`getBandBaselineRate(band)`), `null` until the first response
  (then no normal line is drawn).
- `style.css`: two rules under the existing `.band-spark` block. The polyline's y
  scale must include the normal so the dashed line is visible when activity is
  below it.

### U4 — Typical view
- `static/almanac.js`: render into `#cond-body` when the horizon is `usual`.
  - New `scheduleHtml(agenda)` replaces the agenda list: rows sorted by start,
    grouped *Open now, usually* / *Opens within 3 h*, bar = window on a 3 h axis
    from now (30-min ticks), opacity = existing `0.15 + 0.85·n/m`, `n of 30` at
    right, outlined when `is-now`, mask fade when the window exceeds the axis.
    Uses `.almanac-lane-track` / `.almanac-run` styles; new `.cond-sched` grid
    only.
  - Lanes: unchanged markup (`.almanac-lanes`, `.almanac-region`, `.almanac-tracks`,
    `.almanac-now-layer`). The now-line already spans the region's tracks — make it
    2 px `--text-color` (it is; confirm the per-lane variant isn't used).
  - By-month drill-down unchanged.
  - Header text and legend trimmed per D11 (`Typical openings from <QTH>.`).

### U5 — Collapsed state and hot-band pills
- `static/hot-band-indicator.js`: container shows only while the dock is closed;
  append the `Conditions` panel-toggle (`#cond-open`) after the pills. Pill tag text
  uses the same relabel map as U2 (`lively`).
- Position: top-right of `#map-stack` (where the Propagation card sat), no longer
  centred, so it doesn't need the toggle-row width variables.

### U6 — Chase queue as a dock tab
- `static/dxcluster.js`: mount `#chase-queue` into the dock's body under the
  `Chase queue` tab instead of appending a flex sibling to `<body>`; its own toggle
  button goes away (tab replaces it). Injected styles: drop the column
  `flex/width/z-index` rules, keep the card styles. Opmode-only gating unchanged
  (tab hidden when opmode is off).

### U7 — Tests and gates
- Move/adjust unit tests: `wspr-matrix.__test`, `almanac.__test` and band-lab pure
  helpers keep their tests; add `cond-now.test.js` for the relabel map, the
  count-pair formatting and the shared-axis scaling; add a `scheduleHtml` test
  (grouping, clipping to 3 h, `runs-on`).
- `npm run check` (typecheck + coverage floor 53.06 % on `static/`) and
  `npm run perf:gate:mercator` must pass; the dock is in-flow so
  `map.invalidateSize()` on open/close is required (perf gate mocks L — check
  manually with a wheel-zoom profile, see memory note on the gate's blind spot).
- `docs/api.md`: no endpoint changes. Update the UI section mentioning the three
  panels; add a line in `CLAUDE.md` frontend list for `cond-now.js`.

## Sequencing

U1 → U2 → U3 can go in one PR (the dock is unusable without Now). U4 next (Typical
is currently a working floating panel, so it can stay floating for one commit if
needed). U5 and U6 last; U7 alongside each.

## Out of scope

- No restyle of chips, cells, lanes, badges or the band palette.
- No new backend endpoints; the `reports / normal` pair and the rail's normal line
  reuse `dx_conditions` fields already served.
- Forecast layer and Time travel unchanged.
- Mobile app (`../horstapp`) unaffected — it consumes the API, not this UI.

## Open points to confirm before U2

1. The Now row's mini plot uses a **fixed** SNR axis (−25…+10 dB) so rows compare;
   Band stats today autoscaled per band. Confirm fixed.
2. Time-range select applies to the whole Now view (verdict, pair, plot). The
   matrix cells stay on their own 15-min window (`prop_intel/v2`) — label the
   difference in the legend, or move the cells to the selected window (backend
   supports `minutes` on `prop_intel`)?
3. "Chase queue" tab when opmode is off: hidden entirely (proposed) or shown
   disabled with a hint?
