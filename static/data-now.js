// Shared grayline time basis (plan 2026-09-11-002 U5, KTD-9). app.js pins this
// clock to the timeline playhead while a replay is active and clears it on
// exit; live mode leaves it on the wall clock. Per KTD-9 the clock is narrow:
// only the grayline terminator time basis (map.js now, azimuth-runtime.js in
// U6) reads it — every cadence guard (render throttle, rebuild intervals,
// recompute throttles) stays on wall-clock Date.now().
let overrideMs = null;

// Pin the clock to a data time (ms). Non-finite values keep the wall clock.
export function setDataNowMs(value) {
    overrideMs = Number.isFinite(value) ? value : null;
}

// Grayline overlay rebuild cadence (ms): the terminator only moves meaningfully
// in 5-minute steps, so map.js and azimuth-runtime.js bucket the overlay time
// on this and app.js's live refresh fires once per bucket. Lives beside the
// clock it buckets because every consumer already imports this module.
export const GRAYLINE_BUCKET_MS = 5 * 60 * 1000;

// Restore the wall clock (timeline exit).
export function clearDataNowOverride() {
    overrideMs = null;
}

// The grayline time basis: overridden data time during timeline, wall clock otherwise.
export function dataNow() {
    return overrideMs ?? Date.now();
}

// Test/introspection accessor: the raw override (null when live).
export function getDataNowMs() {
    return overrideMs;
}