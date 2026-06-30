---
title: "fix: Stop button reflow on select and bump control font sizes"
date: 2026-06-30
type: fix
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
execution: code
product_contract_source: ce-plan-bootstrap
depth: lightweight
---

# fix: Stop button reflow on select and bump control font sizes

## Summary

A set of small, related frontend polish fixes:

1. **Stop selected buttons from changing size.** The segmented/toggle controls (Projection, Map Style, Min-SNR, and the Options toggles) jump from font-weight 400 to 600 when selected, which widens the glyphs and reflows the button. Remove the weight change so selection is signalled by background tint only — no bold on select.
2. **Increase three control font sizes by ~1pt.** Band-selector pills, the **Stats** toggle, and the **Chase Queue** toggle each grow by roughly one pixel.
3. **Stop the Go/Stop button from reflowing, and make it compact.** The `#btn-submit` button swaps its visible text between `Go` and `Stop`, so it resizes between states and is wider than needed. Replace the text with **play** / **stop** Font Awesome icons (icon-only, fixed-width), and move the running-state signal off `textContent` onto a data attribute so behavior is preserved.

**Product Contract preservation:** No upstream brainstorm; direct planning (`ce-plan-bootstrap`).

The first two fixes (U1–U3) are pure CSS/markup. The third (U4) additionally touches `static/app.js`. No Svelte/bundle rebuild is required in any case: the segmented controls are Svelte components (`src/*.svelte` → `static/dist/horst-ui.js`) that emit only Bootstrap class names with no scoped font styling, and `#btn-submit` lives in plain `static/index.html` + `static/app.js`. Changes are confined to `static/style.css`, `static/index.html`, and `static/app.js`.

## Problem Frame

The selected/unselected font-weight difference on `.btn-check` toggles causes visible layout shift: a button physically resizes the moment it becomes active because bold text is wider than normal text. In segmented groups (Mercator/Azimuthal, Grid/Active Area, SNR thresholds) this reflow is especially noticeable. Separately, the band pills and the two floating panel toggles read a touch small and should each grow by ~1pt.

## Requirements

- **R1** — Selecting any `.btn-check` toggle/segmented button must not change its rendered size. The active state must be distinguished without switching to bold (resting and selected share the same font-weight).
- **R2** — Band-selector pill text increases by ~1pt (one ~1px step).
- **R3** — The **Stats** floating toggle font size increases by ~1pt.
- **R4** — The **Chase Queue** floating toggle font size increases by ~1pt.
- **R5** — No Svelte rebuild is introduced; changes are confined to CSS, static markup, and `static/app.js`.
- **R6** — The Go/Stop button (`#btn-submit`) shows a **play** icon when idle and a **stop** icon when streaming, with no visible text label, so it is compact and fixed-width across both states.
- **R7** — The button remains accessible (a `title`/`aria-label` conveys "Go"/"Stop"), and the existing start/stop stream behavior is unchanged — running-state detection no longer depends on the button's visible text.

## Key Technical Decisions

- **"1pt" interpreted as a ~1px step** (confirmed with user). The codebase mixes `px` and `rem`, so values map to clean increments rather than a literal 1.333px typographic point:
  - `.panel-toggle` (Stats + Chase Queue): `12px → 13px`.
  - Band pills (`#band-container` inherited size): `0.85rem → 0.9rem` (≈13.6px → 14.4px).
- **Remove the bold-on-select rule entirely** (confirmed with user). Both resting and selected states use Bootstrap's default weight (400). The active state is already visually distinct via `--bs-btn-active-bg` (`--accent-tint-strong`), so no weight change is needed. This is preferred over equalising at a heavier constant weight.
- **Stats and Chase Queue share one rule.** Both buttons use the `.panel-toggle` class (`Chase Queue` is created in `static/dxcluster.js` with `className = 'panel-toggle'`), so a single edit to `.panel-toggle` covers R3 and R4.
- **Band pills are sized via the container, not per-pill.** `.band-pill` has no explicit `font-size`; the pills inherit `0.85rem` from the inline style on `#band-container`. Editing that inline value bumps the pills without touching the `Show all` / `Cycle` buttons (which carry their own `btn-sm` sizing).
- **Go/Stop state moves off `textContent` onto a data attribute.** Today the running state is inferred from `btnSubmit.textContent === 'Stop'` (read at `static/app.js:809`, `:1453`, `:1569`). Once the label becomes an icon, that check breaks. Introduce `data-mode="go"|"stop"` on `#btn-submit` as the single source of truth, set via a small helper alongside the icon, and rewrite the three read sites to test `dataset.mode`. This is a behavior-preserving refactor, not a logic change. Use Font Awesome `fa-play` (idle) and `fa-stop` (streaming), matching the existing icon usage on `#btn-cycle` (`fa-play`/`fa-pause`).

## Implementation Units

### U1. Remove bold-on-select from toggle/segmented buttons

**Goal:** Selecting a `.btn-check` button no longer changes its font-weight, eliminating the width reflow (R1).

**Requirements:** R1, R5

**Dependencies:** none

**Files:**
- `static/style.css` (modify the `.btn-check:checked + .btn-outline-*` / `.btn-check:active + .btn-outline-*` rule at lines ~909–914)

**Approach:** Delete the `font-weight: 600;` declaration (and the now-empty selector block) so checked/active toggles inherit the default weight. The selected state remains visually distinct through the existing `--bs-btn-active-bg`/`--bs-btn-active-color` accent tint. Confirm the comment above the block ("Selected toggle / segment reads clearly without becoming a solid block.") is still accurate or remove it if it now describes nothing.

**Patterns to follow:** Keep the accent-tint distinction already established by `.btn-outline-primary` / `.btn-outline-secondary` active variables (`static/style.css:889–906`).

**Test scenarios:**
- Covers R1. With dev server running (`go run . -dev -port 8080`), measure a segmented button's rendered width (e.g. the Projection `Mercator`/`Azimuthal` pair) while unselected vs selected — width must be identical; no horizontal shift of sibling buttons.
- Selecting a toggle still produces a clearly visible active state (accent-tint background) distinct from resting.
- Verify across all affected groups: Projection, Map Style (Grid/Active Area), Min-SNR (All/≥CW/≥SSB), and the Options checkboxes (DXCC Labels, Forecast, DK3JF Mode, Permit Antenna Control).
- `Test expectation: none (unit) — CSS-only change with no JS behavior; verification is visual/e2e.`

**Verification:** Toggling any segmented/checkbox button shows no size change; the active state is still obvious from the background tint.

### U2. Increase band-pill font size by ~1pt

**Goal:** Band-selector pill text grows ~1px (R2).

**Requirements:** R2, R5

**Dependencies:** none

**Files:**
- `static/index.html` (modify the inline `style="font-size: 0.85rem;"` on `#band-container`, line ~45)

**Approach:** Change the `#band-container` inline `font-size` from `0.85rem` to `0.9rem`. This bumps the inherited size for the band pill rows (`.band-pill` / `.band-pill-name`, which set no explicit size) without affecting the `Show all` and `Cycle` buttons (they carry their own `btn-sm` sizing).

**Patterns to follow:** Existing `rem`-based sizing already used throughout the sidebar (`static/style.css`).

**Test scenarios:**
- Covers R2. Band pill labels (e.g. `160m`, `20m`, `2m`) render visibly larger than before; the two-column band grid still fits the sidebar width with no overflow or wrapping of pill names.
- `Show all` and `Cycle` buttons remain unchanged in size.
- `Test expectation: none (unit) — static markup style change; verification is visual.`

**Verification:** Band pills read ~1px larger; sidebar layout and column alignment intact at default and narrow widths.

### U3. Increase Stats and Chase Queue toggle font size by ~1pt

**Goal:** Both floating panel toggles grow ~1px (R3, R4).

**Requirements:** R3, R4, R5

**Dependencies:** none

**Files:**
- `static/style.css` (modify the `.panel-toggle` rule at line ~964)

**Approach:** Change the `font` shorthand on `.panel-toggle` from `600 12px sans-serif` to `600 13px sans-serif`. A single edit covers both the **Stats** toggle (`#band-stats-toggle`, `static/index.html:239`) and the **Chase Queue** toggle (`#cq-toggle`, created in `static/dxcluster.js` with the shared `panel-toggle` class). Weight stays 600 (constant in both states — this control is not affected by R1).

**Patterns to follow:** The single shared `.panel-toggle` pill style documented at `static/style.css:959–962`.

**Test scenarios:**
- Covers R3. The **Stats** toggle (top-left of the map) renders ~1px larger.
- Covers R4. The **Chase Queue** toggle (top-right control cluster) renders ~1px larger and matches the Stats toggle sizing.
- Both toggles still fit their docked positions without clipping or overlapping neighbouring controls.
- `Test expectation: none (unit) — CSS-only change; verification is visual.`

**Verification:** Both floating toggles read ~1px larger and remain visually consistent with each other.

### U4. Convert Go/Stop button to play/stop icons with attribute-based state

**Goal:** Make `#btn-submit` compact and fixed-width across states by replacing its `Go`/`Stop` text with play/stop icons, and move running-state detection off `textContent` so behavior is preserved (R6, R7).

**Requirements:** R6, R7, R5

**Dependencies:** none

**Files:**
- `static/index.html` (modify `#btn-submit`, line ~32 — initial icon markup + accessible label)
- `static/app.js` (add a state helper; update the 7 write sites and 3 read sites listed below)
- `static/utils.test.js` (add focused unit coverage for the state helper if it is extracted to a testable function)

**Approach:**
- In `static/index.html`, change `<button ... id="btn-submit" ...>Go</button>` to start in "go" mode: render `<i class="fas fa-play"></i>`, set `data-mode="go"`, and add `title="Go"` / `aria-label="Go"`.
- In `static/app.js`, add a single helper (e.g. `setSubmitMode(btn, mode)`) that, for `mode` `'go'` or `'stop'`, sets `btn.dataset.mode`, swaps the icon (`fa-play` ↔ `fa-stop`), and updates `title`/`aria-label`. Add a companion read (e.g. `isStreaming(btn)` → `btn?.dataset.mode === 'stop'`) or inline the `dataset.mode` check.
- Replace the **write sites** that set text — `static/app.js:800, 1060, 1454, 1514, 1558, 1588, 1691` (set `'Go'`) and `:1664` (set `'Stop'`) — with `setSubmitMode(btn, 'go'|'stop')`.
- Replace the **read sites** — `static/app.js:809, 1453, 1569` (`textContent === 'Stop'`) — with the `dataset.mode === 'stop'` check.

**Patterns to follow:** The existing icon-swap pattern on `#btn-cycle` in `static/app.js` (`startBandCycle`/`stopBandCycle` set `btn.innerHTML` to `<i class="fas fa-pause"></i> Cycle` / `<i class="fas fa-play"></i> Cycle`). Keep `#btn-submit` as `btn-primary` (solid accent); the U1 fix does not apply here (this button is not a `.btn-check` toggle).

**Test scenarios:**
- Covers R7. State helper: `setSubmitMode(btn, 'stop')` then the read predicate returns running/true; `setSubmitMode(btn, 'go')` then it returns idle/false. (Unit-testable in `static/utils.test.js` if the helper is exported.)
- Covers R6. Clicking the idle (play) button starts the stream and the icon becomes **stop**; clicking again stops the stream and the icon returns to **play** — no text appears in either state.
- Covers R7. Button rendered width is identical in idle and streaming states (no reflow of the target-input row).
- Surroundings/target-change paths that previously force a restart by setting `Go` (`__horstSurroundingsChanged` at `:1452`, and the reset paths at `:1060`, `:1514`, `:1558`, `:1691`) still correctly reset the button to idle and re-submit where applicable.
- Accessibility: `title`/`aria-label` reflects the current action ("Go" when idle, "Stop" when streaming).

**Verification:** Go/Stop button shows only an icon, stays the same size in both states, starts/stops the stream exactly as before, and all former `textContent`-based state reads now key off `data-mode`.

## Verification Contract

- `go test ./...` passes (no backend change expected; run as a regression guard).
- `npm test` passes — includes any new state-helper unit coverage from U4.
- Manual visual + behavioral check via `go run . -dev -port 8080`:
  - No size change when selecting any segmented/toggle button (U1).
  - Band pills, Stats, and Chase Queue toggles are each ~1px larger (U2, U3).
  - Go/Stop button is icon-only, fixed-width, and starts/stops the stream correctly including the target-change/surroundings restart paths (U4).
  - Light and dark themes both look correct.
- No rebuild of `static/dist/horst-ui.js` is required or performed.

## Scope Boundaries

In scope: the font-weight reflow fix on `.btn-check` toggles, the three ~1pt font-size bumps, and the Go/Stop button icon conversion with its state-signal refactor.

Out of scope:
- Any change to button colors, borders, padding, or radius (beyond what the icon-only Go/Stop button needs to stay compact).
- Restyling the band pills beyond font size.
- Touching the Svelte source (`src/*.svelte`) or rebuilding the bundle.
- Backend, MQTT, or API behavior.
- Changing the start/stop stream logic itself — U4 preserves behavior, only the state signal moves.

### Deferred to Follow-Up Work

- If a regression guard against future reflow is wanted, a Playwright assertion comparing `offsetWidth` of a segmented button across selected/unselected states could be added to the existing e2e suite (`test:e2e`). Not required for this fix.

## Open Questions

- None blocking. The ~1px-step interpretation of "1pt" and the remove-the-bold-rule approach were both confirmed by the user during planning.
