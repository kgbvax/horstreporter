---
title: Frontend Architecture Modernization - Plan
type: refactor
date: 2026-06-29
topic: frontend-architecture
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-brainstorm
execution: code
---

Product Contract preservation: unchanged.

# Frontend Architecture Modernization - Plan

## Goal Capsule

- **Objective:** Reduce the complexity of the static frontend so agent edits are faster and more reliable, in two tracks: a Svelte migration for the non-canvas UI, and a dedup/extraction pass for the canvas drawing code.
- **Product authority:** Project owner (Horst Reporter maintainer).
- **Open blockers:** None blocking planning. Incremental vs big-bang migration order and bundler choice are deferred to planning.

## Product Contract

### Summary

Migrate the non-canvas frontend (sidebar, status, control panels) to Svelte with a central reactive store and a bundler that emits into `static/`, keeping `//go:embed` and `-dev` disk serving intact. In parallel, reduce canvas drawing complexity by consolidating duplicated geo/color math, separating pure compute from paint, and introducing a thin draw-primitive layer. Behavior stays identical; the canvas math itself is not rewritten.

### Problem Frame

The frontend is ~10,400 lines across 16 plain ES modules with no central store: real state lives in the DOM and `localStorage`, read ad-hoc. Adding one control means editing ~4 files, and `app.js` carries ~25 near-identical listeners (`static/app.js:1239-1599`). The canvas half — `static/azimuth-runtime.js:1` (2201 lines), `static/renderers.js`, `static/map.js`, `static/band-lab.js` charts — is fully hand-rolled with copy-pasted draw boilerplate and duplicated math (`degToRad` 4×, haversine 3×, `hexToRgb`/`blendOverlayColors` and DXCC palette verbatim across modules). The pain is agent edit speed: verbose code and multi-file change spread make changes slow and error-prone.

### Key Decisions

- **Svelte over no-build reactive (Alpine/petite-vue) and over vanilla store.** Maximum terseness for the non-canvas half; the owner accepts adding a build step to the previously no-build single binary.
- **Canvas stays vanilla, not framework-wrapped.** A UI framework adds nothing to canvas/Leaflet/Turf; the Svelte shell imports the canvas modules as-is.
- **Canvas track is dedup + extraction, not a math rewrite (A+B), C deferred.** Consolidate copies and add draw primitives — keep the perf-gated marching-squares engine untouched so parity is cheap to verify. Replacing hand-rolled hull/contour/charts with Turf and a chart lib (C) is out for now due to parity risk on the perf-gated path.
- **Bundler emits into `static/`.** `//go:embed static` and the `-dev` disk-serving flow keep working without backend changes.

### Requirements

**Svelte migration (non-canvas)**
- R1. Non-canvas UI (sidebar, status, control panels) is rendered by Svelte with a central reactive store as the single source of truth, replacing DOM/`localStorage` ad-hoc reads.
- R2. The ~25 control listeners and the 4-file filter-change spread collapse to store-bound bindings.
- R3. A bundler emits into `static/`; production embed (`//go:embed`) and `-dev` disk serving both continue to work.
- R4. Canvas modules are imported unchanged by the Svelte shell.

**Canvas dedup + extraction**
- R5. Duplicated geo/color math is consolidated into one shared module (eliminate the 4 `degToRad`, 3 haversine, duplicated `hexToRgb`/`blendOverlayColors`, and DXCC palette copies).
- R6. Each `draw*` is split into pure "compute specs" and thin "paint" halves so the compute is unit-testable.
- R7. A draw-primitive layer collapses the ~20 copy-pasted canvas blocks and repeated tick loops; charts share a frame/gridline helper.
- R8. The stale `static/azimuth.js` is removed.

**Parity (both tracks)**
- R9. All visualization output is unchanged — no visual or feature changes; pure architecture and dedup.
- R10. The marching-squares SNR field and other perf-gated render paths keep current behavior and pass existing perf gates.

### Success Criteria

- Sidebar/panel features still work and the binary embeds and serves cleanly.
- Significant, observable reduction in non-canvas complexity (fewer files touched per change, store-bound controls) and removed canvas duplication.
- Existing `npm test` (vitest) and the mercator perf gate pass.

### Scope Boundaries

- Canvas math rewrite (replacing hull/contour with Turf, charts with a lib) — deferred.
- `dxpulse/` standalone app — untouched.
- No feature, layout, or visual changes — parity only.

### Outstanding Questions

Resolved during planning: bundler is Vite emitting into `static/dist/`; migration is incremental panel-by-panel; the two tracks (Svelte, canvas dedup) run independently. No blockers remain.

## Planning Contract

### Key Technical Decisions

- KTD1. **Vite + Svelte, output to `static/dist/`.** `main.go:79` embeds `//go:embed static`, so any built bundle under `static/` ships automatically; `main.go:351` serves `static/` from disk in `-dev`. Vite builds source `.svelte`/`.js` from a new `src/` into `static/dist/`; `index.html` loads the built entry. No backend change to embed or serving.
- KTD2. **`vite build --watch` for dev.** Keep `go run . -dev`; run Vite in watch mode rebuilding into `static/dist/` so disk serving (`main.go:351`) sees fresh output. No HMR server, no proxy — preserves single-binary simplicity.
- KTD3. **Incremental migration, panel-by-panel.** Stand up the store + build first, then convert one panel at a time, verifying parity each step. Vanilla canvas modules import unchanged (R4); avoids a big-bang rewrite.
- KTD4. **Shared geo/color core extracted into `static/utils.js`** (the existing test home, `static/utils.test.js`). Azimuth/map/band-lab import from it. Removes the 4 `degToRad`, 3 haversine, dup `hexToRgb`/`blendOverlayColors`, DXCC palette copies (R5).
- KTD5. **Compute/paint split keeps `state`/DOM out of paint.** `renderAzimuthScene` reads `document.getElementById` mid-render (`azimuth-runtime.js:2100`); move reads up, pass plain specs to paint helpers so compute is testable (R6).

### Assumptions
- Vendored libs (Leaflet/Turf) stay vanilla; the Svelte shell does not wrap them.
- Existing vitest + mercator perf gate (`npm run perf:gate:mercator`) are the parity guard; no new visual snapshot tooling.

## Implementation Units

### U1. Build pipeline + store scaffold
- **Goal:** Vite builds `src/` into `static/dist/`; `index.html` loads it; `-dev` watch works; embed unaffected.
- **Files:** `package.json` (add svelte, vite, deps + `build`/`dev` scripts), new `vite.config.js`, `src/store.js`, `static/index.html`, `main.go` (verify embed picks up `static/dist/`).
- **Test:** build emits `static/dist/`; `go run . -dev` serves built assets; `npm run build` + binary embed smoke check.

### U2. Migrate controls/sidebar to Svelte + store
- **Goal:** Replace the ~25 listeners (`app.js:1239-1599`) and `localStorage` marshalling (`config.js:27-152`) with store-bound components (R1, R2).
- **Files:** `src/` components, `static/app.js`, `static/config.js`, `static/ui.js`.
- **Test:** each control persists + triggers render; parity vs current behavior, one panel at a time.

### U3. Migrate status + panels (DX/band-lab/dxcluster shells)
- **Goal:** Status DOM (`app.js:1719-1779`) and panel scaffolding move to Svelte; canvas modules imported as-is (R4).
- **Files:** `src/` components; `static/dxcluster.js`, `static/band-lab.js`, `static/dx-conditions.js` DOM scaffolds.
- **Test:** SSE stream updates status; panels render; canvas unchanged.

### U4. Extract shared geo/color core
- **Goal:** Consolidate dup math into `static/utils.js` (R5); update importers; delete stale `static/azimuth.js` (R8, zero importers).
- **Files:** `static/utils.js`, `static/azimuth-runtime.js`, `static/map.js`, `static/band-lab.js`, `static/utils.test.js`, remove `static/azimuth.js`.
- **Test:** vitest for unified helpers; existing tests pass.

### U5. Compute/paint split + draw primitives
- **Goal:** Split `draw*` compute from paint (R6); add primitives for the ~20 boilerplate blocks + tick loops; shared chart frame/gridline helper (R7).
- **Files:** `static/azimuth-runtime.js`, `static/renderers.js`, `static/band-lab.js`, `static/utils.test.js`.
- **Test:** unit tests for compute specs; mercator perf gate stays green (R10).

## Verification Contract

| Command | Covers | Done signal |
|---|---|---|
| `npm test` | U2, U4, U5 | vitest green |
| `npm run perf:gate:mercator` | R10, U5 | perf within baseline |
| `npm run build && go build .` | U1, R3 | bundle in `static/dist/`, binary embeds |
| `go run . -dev` + manual sidebar check | R9, U2, U3 | panels work, parity |

## Definition of Done
- R1-R10 satisfied; sidebar/panels parity-verified; `static/azimuth.js` removed.
- `npm test` and mercator perf gate pass; binary embeds and serves clean.
- No dup geo/color copies remain; non-canvas line count materially down.
