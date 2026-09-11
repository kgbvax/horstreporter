# Residual Review Findings

Source: ce-code-review run `20260911-184214-0539a4d3` (invoked by `/compound-engineering:lfg`, step 6)
Feature: client session ring + grayline playhead clock (plan `docs/plans/2026-09-11-002-feat-session-ring-grayline-plan.md`)
Branch: `ui-stalled-stop-button` @ `ed9c5a44` + simplify-pass edits; review-fix commit `78df24e`
Full artifact: `/tmp/compound-engineering-501/ce_code_review/20260911-184214-0539a4d3/review.json`

Applied in step 5 (commit `78df24e`, pushed): #1 (P1 cohort hole — widening restart clears the ring), #3 (P2 invalidateBundles aborts the in-flight fetch), #9 (P3 dead tryRingBundle filter parameter).

## Filed

- P2 static/timeline.js:321 — Timeline qth vouch uses DOM values, ignores ring's actual cohort qth — https://github.com/kgbvax/horstreporter/issues/1
- P2 static/app.js:1998 — server_error mid-timeline branch has no test — https://github.com/kgbvax/horstreporter/issues/2
- P2 static/session-ring.js:253 — session-ring O(1) coverage patch and eviction rework branches untested — https://github.com/kgbvax/horstreporter/issues/3
- P2 static/timeline.js:331 — ringCohortSatisfies SNR and empty-band guard legs untested — https://github.com/kgbvax/horstreporter/issues/4
- P2 test/app.session-ring.test.js:123 — App test harness copy-pasted across 4 test files — https://github.com/kgbvax/horstreporter/issues/5

(These are the actionable findings NOT applied in step 5: single-reviewer confidence-75 does not clear the apply bar (no cross-persona agreement), or `advisory` class. #2 is validator-confirmed at confidence 100 but `advisory` — report-only by rubric.)

## Failed

(none)

## No sink

(none)

## Settled conflicts (report-only)

- P2 static/app.js:2353 — Timeline entry failure with no stream running dead-ends on a failed /api/history fetch instead of starting a live stream. **Conflicts with KTD-12** (entry failure must not stack a second EventSource; the dead-end is deliberate per the in-code comment). Preference-grade: proceeds, not applied; revisit only if KTD-12 itself is renegotiated (`if (!state.eventSource) startLiveStream(false);` in the catch would restore the pre-diff self-heal while preserving KTD-12).

## Residual risks (advisory, from the review report)

- P3 static/app.js:2390 — no grayline resync on timeline exit: terminator stays at the last playhead bucket until the next 60s live-refresh tick (self-healing, cosmetic).
- P3 static/app.js:109 — mid-timeline filter-change refresh failure swallowed with console.warn; UI filter and rendered moment silently diverge.
- P3 static/app.js:2099 — mid-timeline fatal-drop restart runs startLiveStream(true) against a possibly-invalid edited qth input; alert leaves timeline with no stream and a stale status.
- P3 scripts/tl-check.mjs:355 — in-coverage rewind assertion can self-skip (rewindSkipped) and still report green; manual gate, no CI.
- P3 static/session-ring.test.js:664 — U7 render-parity 'recorded' leg is hand-constructed; a history.go field change would not fail the JS parity test.
- Derived spot-time t inherits client/server clock skew; wall-clock steps between a live frame and its reconnect-dump re-delivery can duplicate a spot (dedup miss, benign for coverage).
- ringCohortSatisfies does not compare surroundings/rings; safety rests on __horstSurroundingsChanged's unconditional sessionRing.clear() — any future cohort-affecting stream param must add the same clear.
- Mid-timeline fatal-drop leaves the tab with no live ingest until the user exits (deliberate KTD-11/12); ring adds a third bounded memory pool with no hot-band measurement.
- Coverage floor is local-only (no CI); Mercator perf gate mocks L (known blind spot) — new grayline refresh paths unverified against real-map jank.

## Testing gaps (from the review report)

- AbortError early-return in startTimelineMode entry failure untested (static/app.js:2346).
- isAzimuthEnabled() skip leg of the grayline live-refresh interval untested (static/app.js:2391).
- No test asserts a grayline rebuild on timeline exit.
- No race test for invalidateBundles vs an in-flight fetch (regression test bundled with the applied #3 fix).
- test/app.timeline-lifecycle.test.js:675 'Drive the 5s prune interval directly' comment — prune body never driven.
- session-ring __internals dedup-key test omits `t`, so spotIdentity's t participation is never exercised.
- No widening-reconnect cohort-hole regression test (bundled with the applied #1 fix).