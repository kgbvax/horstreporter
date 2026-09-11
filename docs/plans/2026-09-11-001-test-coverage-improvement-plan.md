---
title: "Test Coverage Improvement - Plan"
type: test
date: 2026-09-11
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Test Coverage Improvement - Plan

## Goal Capsule

- **Objective:** Close the measured test-coverage gaps in the core backend Go main package (44.4%), `cmd/horstoperator-agent` (32.3%), and the frontend `static/` modules (45% statements) with behavior-level tests that follow the repo's established patterns; add a no-regression coverage guard.
- **Authority hierarchy:** Product Contract requirements bind; KTDs record how; `ce-work` resolves implementation-detail questions.
- **Stop conditions:** a planned test would require introducing a new test dependency, touching `internal/awards*`, or refactoring `static/dxcluster.js` module-init behavior — stop that thread and surface it.
- **Execution profile:** additive test work with small production-side extractions (pure helpers only); no behavior changes to production code except new exports/moves.
- **Tail:** `ce-work` owns landing per-unit commits and the final gate run.

---

## Product Contract

### Summary

Add test suites for the three measured gap areas — core backend Go, the operator agent, and the frontend `static/` modules — by mirroring the repo's strongest existing test patterns, extract pure seams where code is currently DOM/network-coupled, and set a no-regression coverage floor. The awards engine is excluded from all test work because it will be removed.

### Problem Frame

A coverage assessment (measured 2026-09-11 with `go test ./... -cover` and `vitest run --coverage`) found a clear gap concentrated where incident history says the risk is highest:

- Core backend main package: 44.4% statements, 146 functions at 0% — including the Postgres store (`dx_postgres.go`), all ingest paths (`mqtt.go`, `dxcluster.go`, `rbn.go`, `wspr.go`, `sw_ingest.go`, `dx_cellfeed.go`), the uncovered functions of the persistence/flush loops (`prop_baseline.go` and `wspr_climatology.go` are partially covered; their 0% functions sit in the flush/persistence paths), and several HTTP handlers. This is the code behind the Sep'26 qth-SQL/corruption remediation, the UNLOGGED `dx_raw_spots` data-loss incident, and the rings-parameter regressions.
- `cmd/horstoperator-agent`: 32.3% — `config.go`, `diagnostics.go`, `debug_http.go` essentially untested.
- Frontend `static/`: `ui.js` (466 lines), `dxcluster.js` (744 lines), and `sw.js` at 0% statements; `band-lab.js` 21%, `hot-band-indicator.js` 8%, `horst-kevin.js` 25%, `azimuth-runtime.js` 30%; the 40% tier (`map.js`, `timeline.js`, `afterglow.js`, `app.js`, `panel-drag.js`).
- No CI exists and no coverage floor is enforced anywhere; there is no mechanism preventing silent regression.

Meanwhile the repo has proven test patterns — `internal/proplab` at 90.7%, `internal/cty` at 91.5%, `wspr-matrix.js` at 97% — so the gap is coverage investment, not missing technique.

### Requirements

Core backend:

- R1. Add behavior-level tests for the core backend main package, prioritizing clusters with incident history: the Postgres store's pure logic (SQL-arm builders, pending-queue merge/trim, flush-health accounting), ingest parsing/session paths, the uncovered flush/persistence functions (`go tool cover -func` names them precisely), and HTTP handlers.
- R2. DB-coupled code is tested without a live Postgres: extract pure helpers and test them (the `TestAppendTargetArms` pattern; `buildRawSpotInsertSQL` is currently untested — its first test is U2's work, shaped after `TestAppendTargetArms`), plus `t.TempDir()` JSON round-trips for the still-uncovered baseline-engine paths (those files are partly covered already — target the 0% functions).

Frontend:

- R3. Raise `static/` module coverage by testing exported pure seams; DOM-coupled init closures stay untested per the established convention ("DOM-dependent controller paths run under the browser").
- R4. Modules that auto-run initialization at import time get their pure logic extracted into side-effect-free helper modules, not exported-and-tested in place.

Operator agent:

- R5. Add handler-level `httptest` tests for `cmd/horstoperator-agent` covering config parsing and the diagnostics/debug HTTP gates.

Tooling and guardrails:

- R6. No new test dependencies: stdlib `testing` on the Go side, existing vitest/jsdom + hand-rolled mock patterns on the frontend side. No pgxmock, gomock, testify, or snapshot tooling.
- R7. Add a no-regression coverage floor wired into the existing always-required gates: vitest built-in `coverage.thresholds` for `static/` set at the measured post-work baseline, bound into `npm run check`; a documented Go per-package baseline checked by a small script run as part of the Go test gate; no CI is introduced (none exists today — introducing it is out of scope). Floor accounting scope: vitest thresholds scoped to `static/` only, with `coverage.exclude` for `src/`, `scripts/`, `static/vendor/**`, and `sw.js`; the Go baseline file records only in-scope packages (no `internal/awards*`, `internal/awardcontract`).
- R8. All existing gates keep passing: `go test ./...`, `npm run check`, and the Mercator perf gate (`npm run perf:gate:mercator`).

### Success Criteria

- Core backend main package coverage ≥ 65% statements (from 44.4%); `cmd/horstoperator-agent` ≥ 55% (from 32.3%); `static/` statements ≥ 65% (from 45%), measured with the same commands as the assessment.
- `ui.js`, `dxcluster.js`, and the in-scope <30% modules (`band-lab.js`, `hot-band-indicator.js`) each have their pure logic under test; `horst-kevin.js` and `sw.js` are excluded per the scope boundary.
- The vitest coverage floor actively fails a run when `static/` statements drop below the recorded baseline.
- No production behavior change: `go test ./...` green, `npm run check` green, perf gate green.

### Scope Boundaries

- **Excluded per user decision:** the awards engine (`internal/awards*`, `internal/awardcontract`) — slated for removal; no test investment. If coverage tooling reports it, the floor/gates must exclude it.
- **Deferred to follow-up work:** introducing CI; Playwright e2e expansion beyond the two existing specs; unit tests for the Svelte layer (`src/*.svelte`, `src/store.js`); refactoring `ui.js` or `static/dxcluster.js` init structure; testing `horst-kevin.js` persona copy (low logic value).
- **Out of scope:** `static/vendor/`, `scripts/*.mjs` perf tooling, `sw.js` as a coverage target (its behavior is already exercised by `test/push.test.js`; v8 coverage merely cannot attribute eval'd code — no work needed, and it is excluded from threshold accounting).
- **Residual risk accepted:** real-SQL execution semantics, table DDL/durability properties (e.g. UNLOGGED), and flush-loop behavior against a live Postgres stay untestable under the no-live-PG approach (R2); a live-PG integration test tier is deferred follow-up work.

---

## Planning Contract

### Key Technical Decisions

- KTD1. Extend existing patterns, add no test dependencies. The repo has zero mocking libraries, and its best-covered packages (proplab 90.7%, cty 91.5%, wspr-matrix 97%) prove stdlib table tests and hand-rolled jsdom mocks suffice. (session-settled: user-approved — broad plan with floor assessment, chosen over a metric-chasing or new-harness approach)
- KTD2. Test through extracted pure seams rather than DOM/network simulation. Backend: extract pure helpers from coupled files (precedent: `dx_postgres_test.go` SQL-arm tests, `dx_baseline_v6_test.go` TempDir round-trips, `main_test.go` global-swap httptest handlers). Frontend: export/extract pure functions and test them (precedent: `afterglow.js` `__internals`, `canvas-draw.test.js` recording-ctx).
- KTD3. For `static/dxcluster.js`, extract pure helpers (`trimComment`, `escapeHtml`, `gradeToDecision`, `degToCardinal`, `fmtFreq`, `fmtAge`, `meterPct`, band-plan/mode classification, POTA regex handling) into a new side-effect-free module `static/dxcluster-helpers.js` imported by `dxcluster.js`, rather than exporting in place — the module auto-runs `init()` at import time, which would force every test to stub the whole DOM graph. Its auto-init closure path stays untested this round.
- KTD4. Coverage floor shape: vitest `coverage.thresholds` (built into `@vitest/coverage-v8`, zero new deps) floors `static/` statements at the measured post-work value; Go gets a documented per-package baseline in a recorded baseline file checked by the U8 script (mirroring the `.perf-baseline.json` pattern) plus a tiny check script. Both are wired into the already-mandated gates (`npm run check` for the vitest floor; the Go baseline check runs as part of the Go test gate), so "actively fails a run" refers to commands R8 already requires green. Honest assessment recorded: with no CI, enforcement stays local (the gates fire only when someone runs them); a remotely enforced floor arrives with CI, which is deferred. A floor set at baseline prevents regression without pressure to game numbers.
- KTD5. The awards engine is excluded from coverage targets and from any floor/threshold accounting. (session-settled: user-directed — chosen over including it: "this will go away")

### High-Level Technical Design

| Gap cluster | Today | Unit | Pattern to mirror |
|---|---|---|---|
| Backend pure logic (locator math, DSN masking, hot-bands, hub clamps, baseline helpers) | 0–44% | U1 | `rbn_test.go` table tests, `main_test.go` global swap |
| Postgres store seams + flush health | ~0% | U2 | `dx_postgres_test.go` SQL-arm tests, TempDir round-trips |
| Ingest sessions/pollers (dxcluster TCP, RBN prompt, WSPR/SWPC HTTP) | 0% | U3 | `net.Pipe` on `dxClusterLogin`'s `net.Conn`; `wavelog_test.go` mock-upstream `httptest.NewServer` |
| HTTP handlers + baseline flush loops | 0% | U4 | `main_test.go` global-state httptest; TempDir round-trips |
| Frontend pure seams (dxcluster helpers, hot-band-indicator, band-lab) | 0–21% | U5 | `scatter-data.test.js` / `band-lab-activity.test.js` |
| Render logic (azimuth-runtime plan/labels, map scenes) | 30–42% | U6 | `canvas-draw.test.js` recording-ctx, `map.test.js` Leaflet mock |
| Operator agent config + diagnostics | ~0% | U7 | `wavelog_test.go` handler-level httptest |
| Coverage floor + tracking | none | U8 | vitest thresholds + advisory Go baseline script |

Sequencing: U1–U4 (backend, dependency order U1 → U2 → U3 → U4), U5–U6 (frontend, parallel to backend), U7 independent, U8 last (floor set from the measured post-work baseline, so it lands after the coverage work).

### Assumptions

- Coverage targets (≥65% / ≥55% / ≥65%) are agent-set bets sized to the gap clusters, not user-mandated numbers; U8 records whatever the measured post-work baseline is and floors at that.
- `dxClusterLogin`'s `net.Conn` parameter is the only seam needed for the TCP session tests; if the session loop itself needs more surgery, that thread stops per the Goal Capsule stop conditions.

### Sources / Research

- Measured baselines: `go test ./... -cover` (442 tests, 26 packages); `npm test -- --coverage` (`@vitest/coverage-v8`); per-function `go tool cover -func` for the main package (146 functions at 0%).
- Pattern references: `rbn_test.go`, `dxcluster_test.go`, `dx_postgres_test.go` (`TestAppendTargetArms`), `main_test.go` (global-state httptest), `dx_baseline_v6_test.go` (TempDir round-trips), `test/map.test.js` (Leaflet mock + `vi.resetModules`), `test/app.dk3jf.test.js` (full dependency isolation), `test/canvas-draw.test.js` (recording ctx), `static/timeline.test.js` (documented pure-logic/DOM split), `static/afterglow.js` `__internals`, `cmd/horstoperator-agent/wavelog_test.go` (mock-upstream server).
- Institutional learnings: `docs/solutions/conventions/keep-button-state-out-of-layout-and-text.md` — test `data-*` attributes/helpers, not visible text; extract pure helpers to make UI code testable. Fragile areas with incident history: `hub.history` fan-out, qth SQL/baseline cluster paths, `dx_raw_spots` flush health, rings parameter.

---

## Implementation Units

### U1. Core-backend pure-logic tests

- **Goal:** Cover the easily-testable pure functions in the main package that currently sit at 0%.
- **Requirements:** R1, R6
- **Dependencies:** none
- **Files:** `main_test.go` (extend), `spot_test.go` (new), `hot_bands_test.go` (extend), `hub_test.go` (new), `dx_conditions_test.go` (extend)
- **Approach:** Table tests for `spot.go` `latLngToLocator` (inverse of the existing locator parsing), `main.go` `maskDSN`/`dsnSource`, `dx_conditions.go` `baselineP90DistanceForBand`, `baselineClusterKeyFromBase`, `NumBuckets`, `hub.go` `ageClamped`/`safeSend`/`broadcastMsg`. `hot_bands.go` `HotBands`/`lookupBaselineP90` via the `main_test.go` global-swap pattern (seed `dxBaseline`, restore with `defer`).
- **Patterns to follow:** `rbn_test.go` tables; `main_test.go` global swap.
- **Test scenarios:**
  - `latLngToLocator` round-trips against known locators used elsewhere in the suite (JO62qm et al.); boundary at grid-square edges.
  - `maskDSN` hides credentials in common DSN shapes (URL and key-value forms); passes through already-safe strings.
  - `dsnSource` returns the expected source for each configured env/flag combination.
  - `ageClamped` clamps negative and out-of-range ages to window bounds.
  - `HotBands` ranks bands by the seeded baseline; empty baseline yields a stable empty result.

### U2. Postgres-store seam extraction and tests

- **Goal:** Cover the store logic that caused the raw-spots data-loss incident, without a live Postgres.
- **Requirements:** R1, R2, R6
- **Dependencies:** U1 (pattern established)
- **Files:** `dx_postgres.go` (extract helpers only), `dx_postgres_test.go` (extend), `dx_cellfeed.go` (extract helpers only), `dx_cellfeed_test.go` (new)
- **Approach:** Extract and test the pure pieces of `flushPending`/`trimPendingRawSpotsLocked`/`mergeRawSpotsBack` (queue trimming, dedup, merge ordering), the SQL-arm builders behind `insertRawSpot`/`bandPairs`/`scanBandSlotPairs` (assert arms and `$n` positions like `TestAppendTargetArms`), and `FlushHealth` accounting given synthetic success/failure counters. `dx_cellfeed.go`: `toProplabSpot` mapping and the cell-bucket upsert/prune SQL-arm builders. Do not instantiate a real pool — follow the existing `dxPostgresStore` direct-struct-instantiation precedent.
- **Patterns to follow:** `dx_postgres_test.go` (`TestAppendTargetArms`, pending-map regression test); `buildRawSpotInsertSQL` gets its first test in this unit.
- **Test scenarios:**
  - Pending-queue trim keeps the newest entries within capacity and preserves ordering (regression for the UNLOGGED-spots remediation).
  - `mergeRawSpotsBack` dedups correctly when the server returns overlapping rows.
  - `FlushHealth` reports degraded after synthetic failures and recovers after success.
  - Raw-spot insert SQL contains the LOGGED/unlogged handling — `buildRawSpotInsertSQL`'s first test asserts it.
  - `toProplabSpot` maps a real-shaped spot struct to the bucket-engine input, including out-of-range inputs.
  - Cell-feed upsert and prune SQL arms match expected parameter placement.

### U3. Ingest session and poller tests

- **Goal:** Cover the DX-cluster TCP session, RBN prompt handling, MQTT ingest entry, and the WSPR/SWPC HTTP pollers.
- **Requirements:** R1, R6
- **Dependencies:** U1
- **Files:** `dxcluster_test.go` (extend), `rbn_test.go` (extend), `mqtt_test.go` (new), `wspr_test.go` (extend), `sw_ingest_test.go` (extend)
- **Approach:** Drive `dxClusterLogin` and the session/prompt handling through `net.Pipe` (the login path already takes a `net.Conn`); test `handleDXClusterSpot` and `isDXClusterSpotUsableForLive` directly; exercise `awaitRBNPrompt` with a scripted pipe; test `ingestPSKRMessage` through the existing parse+ingest seam. For `wspr.go` and `sw_ingest.go`, stand up mock upstream servers (`httptest.NewServer`) serving fixture payloads for the kp/F10.7/xray/OVATION fetchers and the WSPR spot fetch, asserting parse + error degradation (non-200, malformed body). The `swKpURL`/`swF107URL`/`swXrayURL`/`swOvationURL` package constants become defer-swappable package vars (zero behavior change, same pattern as `main_test.go`'s global swap) so the mock server can intercept them.
- **Patterns to follow:** `rig_log4om_test.go` (real UDP listener), `wavelog_test.go` (mock upstream), `rbn_test.go` (telnet-line tables).
- **Test scenarios:**
  - DX-cluster login sequence over `net.Pipe`: correct command ordering, password prompt handling, login-failure backoff state (the displacement-loop fix).
  - `handleDXClusterSpot` accepts a well-formed spot, drops unusable ones, and populates the expected live-stream fields.
  - RBN prompt detection against scripted server output; no hang when the prompt never arrives (timeout path).
  - `ingestPSKRMessage` happy path, malformed topic, and mode-filter rejection (FT8/FT4 only).
  - WSPR poller: 200-with-rows parses into spots; non-200 and empty-result paths degrade without panicking.
  - Each SWPC fetcher (kp, F10.7, xray, OVATION): fixture parse, HTTP error, and malformed-body degradation.

### U4. HTTP handlers and baseline flush-loop tests

- **Goal:** Cover remaining untested handlers (`hotBandsHandler`, `propIntelV2Handler`, `cachedStaticHandler`) and the `prop_baseline.go`/`wspr_climatology.go` flush paths.
- **Requirements:** R1, R2
- **Dependencies:** U1, U2
- **Files:** `main_test.go` (extend), `prop_baseline_test.go` (extend), `wspr_climatology_test.go` (extend), `hot_bands_test.go` (extend)
- **Approach:** Handler tests via the `main_test.go` global-state pattern (seed hub history / baseline engines, `httptest.NewServer` or `NewRecorder`); flush-loop tests via `t.TempDir()` JSON round-trips plus synthetic pending queues, including `FlushPendingAsync` and `capPendingWsprRegion` overflow behavior.
- **Patterns to follow:** `main_test.go`; `dx_baseline_v6_test.go` round-trips; `history_test.go` param-clamp tests.
- **Test scenarios:**
  - `hotBandsHandler` returns per-band JSON with expected keys when a baseline is seeded; degrades gracefully with no baseline.
  - `propIntelV2Handler` respects `from_here` filtering and qth params; returns well-formed (band × region) structure.
  - `cachedStaticHandler` serves an embedded asset and sets the expected cache headers.
  - Prop-baseline flush: pending entries survive a round-trip, capacity cap drops oldest, async flush drains.
  - WSPR region baseline: calendar-stats computed from a synthetic series; `capPendingWsprRegion` overflow drops oldest.

### U5. Frontend pure-seam coverage

- **Goal:** Cover the 0–21% frontend modules' pure logic without touching their init closures.
- **Requirements:** R3, R4, R6
- **Dependencies:** none
- **Files:** `static/dxcluster-helpers.js` (new, extracted), `static/dxcluster.js` (imports from it), `static/dxcluster-helpers.test.js` (new), `static/hot-band-indicator.js` (export `hexToRgba`), `test/hot-band-indicator.test.js` (new), `test/band-lab-activity.test.js` (extend), `static/ui.js` (extract helpers only), `test/ui.test.js` (new)
- **Approach:** Extract the dxcluster helpers per KTD3 and table-test them; export `hexToRgba` from `hot-band-indicator.js` (module has no auto-init side effect) and test the color-ramp edges; extend band-lab tests toward the remaining pure exports (`utcSlotOfDayFromMs` variants, chart-data edge cases); extract `ui.js`'s pure helpers (formatting/clamp/derived-state helpers — not the init/control wiring, which stays untouched) into testable form and test them, backing the `ui.js` success criterion.
- **Patterns to follow:** `test/scatter-data.test.js`; `static/afterglow.js` `__internals`; `static/timeline.test.js` header convention.
- **Test scenarios:**
  - `escapeHtml` escapes `<>&"'` and passes through safe text; `trimComment` handles over-long and empty comments.
  - `gradeToDecision` covers every grade class including the boundary between two adjacent grades.
  - `fmtFreq`/`fmtAge`/`degToCardinal`/`meterPct` table cases with boundary values (0, negative, huge).
  - Band-plan classification: frequency just inside/outside each `BAND_PLAN` edge, digi-dial and CW-edge boundaries, POTA regex match/no-match.
  - `hexToRgba` at ramp endpoints and midpoints; invalid hex input degrades to a defined value.

### U6. Render-logic coverage (azimuth-runtime, map)

- **Goal:** Raise azimuth-runtime (30%) and map.js (42%) through their existing pure seams.
- **Requirements:** R3, R6
- **Dependencies:** none
- **Files:** `test/azimuth.test.js` (extend), `test/map.test.js` (extend)
- **Approach:** Test `createAzimuthRenderPlan` (the highest-value target: scene composition from a spot set + QTH + zoom), `clampAzimuthZoom`, `computeAzimuthLabelSpecs`, `selectProminentDxccLabels`, `projectAeqdNormalized` via the `test/azimuth.js` shim. For map.js, extend the Leaflet-mock tests to the overlay/scene functions using prebuilt FeatureCollections and the `recordingCtx` pattern for any canvas calls.
- **Patterns to follow:** `test/canvas-draw.test.js` recording ctx; `test/map.test.js` Leaflet mock + `importFreshMapModule()`.
- **Test scenarios:**
  - `createAzimuthRenderPlan` with zero spots, a single spot, and many spots yields the expected plan shape (layers, label set, draw order).
  - `clampAzimuthZoom` clamps at both ends; identity within range.
  - `computeAzimuthLabelSpecs`/`selectProminentDxccLabels` prioritize prominent DXCC labels without overlap on a dense fixture; degenerate on an empty fixture.
  - `projectAeqdNormalized` round-trips a known QTH-to-destination projection within tolerance.
  - Map overlay functions build expected GeoJSON for a fixture spot set and register/unregister handlers on the Leaflet mock.
- **Execution note:** After touching any draw-path code, run `npm run perf:gate:mercator` — the perf gate must stay green (R8).

### U7. Operator-agent config and diagnostics tests

- **Goal:** Raise `cmd/horstoperator-agent` from 32.3% by covering `config.go`, `diagnostics.go`, and `debug_http.go`.
- **Requirements:** R5
- **Dependencies:** none
- **Files:** `cmd/horstoperator-agent/config_test.go` (new/extend), `cmd/horstoperator-agent/diagnostics_test.go` (new), `cmd/horstoperator-agent/debug_http_test.go` (extend)
- **Approach:** Table tests for config parsing (env vars, `.env`-file loading, flag precedence, locator validation); handler-level `httptest.NewRecorder` tests for diagnostics and debug HTTP endpoints including their permit/gate behavior (off by default, enabled by flag).
- **Patterns to follow:** `wavelog_test.go` handler tests; `rig_test.go`.
- **Test scenarios:**
  - Config resolution order: flag beats env beats `.env`; invalid locator rejected with a clear error.
  - Wavelog credential loading from `.env` (secret-stays-out-of-argv convention) parses keys without leaking values into errors.
  - Diagnostics endpoint gates: 404/403-equivalent when disabled, structured payload when enabled.
  - Debug HTTP endpoints degrade gracefully when the upstream agent state is missing.

### U8. Coverage floor and tracking

- **Goal:** Make the new coverage level durable: vitest no-regression floor, documented Go baseline, advisory check script.
- **Requirements:** R7, R8
- **Dependencies:** U1–U7 (floor is set from the measured post-work baseline)
- **Files:** `vitest.config.js` (add `coverage.thresholds`), `package.json` (add `test:coverage` script), `scripts/coverage-check.mjs` (new, Go baseline check), `CLAUDE.md` (document the new commands)
- **Approach:** Add `npm run test:coverage` (`vitest run --coverage`); configure vitest `coverage.thresholds.statements` for `static/` at the measured post-work value (floor, not aspiration); exclude `static/vendor/**`, `src/**`, `scripts/**`, and `sw.js` from thresholds. Add a small script that runs `go test ./... -cover`, compares per-package coverage against a recorded baseline file (in-scope packages only — no `internal/awards*`), and fails on regression beyond a small tolerance. Wire the vitest floor into `npm run check` and the Go baseline check into the test gate, per R7/KTD4; with no CI, enforcement stays local, documented in CLAUDE.md.
- **Patterns to follow:** `scripts/perf-assert.mjs` (threshold-assert script shape); `.perf-baseline.json` (recorded-baseline file).
- **Test scenarios:**
  - The Go check script fails when a synthetic baseline sits above actual coverage and passes when it matches.
  - Vitest thresholds: a run with a deliberately floored test stub fails; the normal suite passes.
- **Test expectation:** the floor tooling is verified by running it against a synthetic regression (a temporarily skipped test file) before committing.

---

## Verification Contract

| Gate | Command | Proves |
|---|---|---|
| Go unit tests | `go test ./...` | All Go units (U1–U4, U7, U8 script) |
| Go coverage measurement | `go test ./... -cover` | Success-criteria coverage levels |
| Frontend unit tests | `npm test` (vitest) | U5, U6 |
| Typecheck + tests | `npm run check` | Frontend units + no type regressions |
| Coverage floor | `npm run test:coverage` | U8 floor active and passing |
| Perf gate | `npm run perf:gate:mercator` | R8 — render-path changes (U6) cause no draw/zoom regression |

Exit criterion for the optimization-shaped goal: per-package coverage at or above the Success Criteria levels, measured with `go test ./... -cover` and `npm test -- --coverage`, and the U8 floor failing a synthetic regression.

---

## Definition of Done

- All units U1–U8 landed; each unit's test scenarios exist and pass.
- `go test ./...`, `npm run check`, `npm run test:coverage`, and `npm run perf:gate:mercator` all green.
- Success-criteria coverage levels met (core backend ≥ 65%, operator agent ≥ 55%, `static/` ≥ 65% statements) — or, if a level is missed, the gap is documented with a named reason in the final report rather than silently skipped.
- No new test dependencies added; `internal/awards*` untouched; no production behavior change beyond pure-helper extraction/exports.
- The vitest floor is demonstrably active (fails a synthetic regression) and the Go baseline script is documented in CLAUDE.md.
- No dead-end experimental test scaffolding left in the working tree; `git status` clean at the end.

---

## Risks & Dependencies

- **Global-state test coupling (backend):** `main_test.go`'s swap-and-defer pattern serializes poorly and can flake if a new test leaks state. Mitigation: each unit keeps its seed/restore self-contained; never run subtests in parallel against shared globals.
- **Module auto-init side effects (frontend):** any test importing a module that auto-runs `init()` risks jsdom flakiness — this is why U5 extracts `dxcluster-helpers.js` instead of exporting in place (KTD3). If another module turns out to auto-init, apply the same extraction, don't stub around it.
- **Mock-upstream drift (U3):** WSPR/SWPC response shapes can change upstream; fixture tests can silently rot. Mitigation: fixtures carry a captured-at date comment, and the plan accepts parse-only coverage (no behavioral contract beyond "degrade, don't panic").
- **Time and interval coupling:** flush loops and pollers use real timers; tests must inject intervals/tick functions where the existing helpers allow, or use synthetic clock parameters — do not add `time.Sleep`-based tests.
- **Coverage-floor pressure:** a floor at the measured baseline is a no-regression guard, not an aspiration; if future work finds the floor encouraging shallow tests, raise the floor after improving coverage, never by gaming it.