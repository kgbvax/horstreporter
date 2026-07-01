---
title: Keep button state out of layout flow and visible text
date: 2026-06-30
category: conventions
module: frontend-controls
problem_type: convention
component: frontend_stimulus
severity: low
applies_when:
  - Styling a selected or active state on auto-width buttons
  - Toggling a button between two states that swap its label or icon
  - Storing a UI control's state to read back later in JS
tags: [button-reflow, font-weight, ui-state, data-attribute, bootstrap, toggle-button, icon-button, css]
---

# Keep button state out of layout flow and visible text

## Context

Two recurring frontend friction points surfaced while polishing the control
sidebar (`static/`, plain ES modules + Bootstrap):

1. The segmented/toggle buttons (Projection, Map Style, Min-SNR, Options
   toggles) visibly *resized* when selected — sibling buttons in the row
   shifted horizontally on every click.
2. The Go/Stop submit button (`#btn-submit`) stored its run state in its
   visible text (`textContent === 'Stop'`), which is read as state in three
   different code paths. That coupling silently breaks the moment the label
   stops being text.

Both come from the same root mistake: letting a button's **presentation**
(font weight, visible label text) carry **state or layout responsibility**.

## Guidance

- **A state change must not change an element's box dimensions.** Signal
  selected/active state with color, background tint, or border — never with a
  property that reflows width (font-weight, font-size, padding) on an
  auto-width control. Bold text is wider than normal text, so a
  `font-weight: 400 → 600` bump on selection physically resizes the button.
- **Store control state in a `data-*` attribute, not in visible content.**
  Read it back through a small helper, never by string-matching the label.
  This decouples logic from presentation and lets the label become an icon,
  get localized, or change wording without touching the state logic.
- **When a button has two states, make it fixed-width across both.** Icon-only
  buttons (e.g. play/stop) are naturally fixed-width because the glyph box is
  constant; `Go` (2 chars) vs `Stop` (4 chars) is not.

## Why This Matters

- **Layout shift is a real UX defect**, not just cosmetic — buttons that move
  under the cursor cause mis-clicks and read as jank, especially in dense
  segmented controls where every sibling reflows.
- **Text-as-state is a latent bug.** It works until someone changes the label.
  Swapping `Go`/`Stop` text for `<i>` icons would have silently broken every
  `textContent === 'Stop'` check (stream stop, restart-on-target-change,
  auto-start detection) with no compile-time or test signal unless a test
  happened to assert the old contract. Centralizing state in `data-mode`
  removes the whole class of breakage.
- The fix is also **cheaper to test**: a pure `setSubmitMode`/`isStreaming`
  helper pair is unit-testable in jsdom, whereas the old check was entangled
  with the live EventSource/DOM flow.

## When to Apply

- Adding or restyling a selected/active state on any auto-width button or pill.
- Any toggle button whose two states differ in label, icon, or width.
- Any time JS needs to read a control's current state back — reach for a
  `data-*` attribute and a helper, not `textContent`/`innerHTML` inspection.

## Examples

**1. Bold-on-select reflow (CSS) — remove the weight change.**

Before (`static/style.css`) — selecting a toggle jumps 400 → 600 and widens it:

```css
.btn-check:checked + .btn-outline-primary,
.btn-check:active + .btn-outline-primary {
    font-weight: 600;   /* glyphs widen → button + siblings reflow */
}
```

After — distinguish the active state by accent tint only (already provided by
the `--bs-btn-active-*` variables), leaving weight unchanged across states:

```css
/* Selected toggle reads via the accent tint alone; weight is left unchanged
   so selecting a button never reflows its width. */
```

**2. State in `data-mode`, not visible text (JS).**

Before (`static/app.js`) — state inferred from, and stored in, the label:

```js
btnSubmit.textContent = 'Stop';                    // write
if (btnSubmit.textContent === 'Stop') { /* … */ }  // read (×3 sites)
```

After — a `data-mode` attribute is the single source of truth; the label is
free to be an icon:

```js
// static/utils.js — pure, jsdom-testable helpers
export function setSubmitMode(btn, mode) {
    if (!btn) return;
    const stop = mode === 'stop';
    btn.dataset.mode = stop ? 'stop' : 'go';
    btn.innerHTML = stop ? '<i class="fas fa-stop"></i>'
                         : '<i class="fas fa-play"></i>';
    const label = stop ? 'Stop' : 'Go';
    btn.title = label;
    btn.setAttribute('aria-label', label);   // accessibility preserved
}
export function isStreaming(btn) {
    return btn?.dataset?.mode === 'stop';
}
```

```html
<!-- icon-only, fixed-width, state + accessible name carried by attributes -->
<button id="btn-submit" class="btn btn-primary"
        data-mode="go" title="Go" aria-label="Go"><i class="fas fa-play"></i></button>
```

Note: when you move state off `textContent`, any existing test asserting the
old text contract (e.g. `expect(btn.textContent).toBe('Stop')`) must be updated
to assert the new signal (`btn.dataset.mode === 'stop'` + the rendered icon).

## Related

- Plan: `docs/plans/2026-06-30-001-fix-button-font-consistency-plan.md`
- Icon labels still need an accessible name — always pair an icon-only button
  with `title` + `aria-label`.
