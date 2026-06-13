const MAX_SAMPLES_PER_METRIC = 600;

/** @type {Map<string, number[]>} */
const metricSamples = new Map();
/** @type {Map<string, number>} */
const counters = new Map();

function getNow() {
    if (typeof performance !== 'undefined' && typeof performance.now === 'function') {
        return performance.now();
    }
    return Date.now();
}

function safeNumber(value) {
    const n = Number(value);
    return Number.isFinite(n) ? n : 0;
}

function collectSample(metric, value) {
    const sample = safeNumber(value);
    if (!Number.isFinite(sample) || sample < 0) return;

    const list = metricSamples.get(metric) || [];
    list.push(sample);
    if (list.length > MAX_SAMPLES_PER_METRIC) {
        list.splice(0, list.length - MAX_SAMPLES_PER_METRIC);
    }
    metricSamples.set(metric, list);
}

function percentile(sortedValues, p) {
    if (!sortedValues.length) return 0;
    if (sortedValues.length === 1) return sortedValues[0];
    const rank = ((p / 100) * (sortedValues.length - 1));
    const lower = Math.floor(rank);
    const upper = Math.ceil(rank);
    if (lower === upper) return sortedValues[lower];
    const weight = rank - lower;
    return sortedValues[lower] + ((sortedValues[upper] - sortedValues[lower]) * weight);
}

export function isPerfProfilingEnabled() {
    if (globalThis.__HORST_PERF_TEST__ === true) return true;
    try {
        return globalThis?.localStorage?.getItem('perfProfiling') === 'true';
    } catch (_e) {
        return false;
    }
}

export function perfNow() {
    return getNow();
}

export function startPerfTimer() {
    if (!isPerfProfilingEnabled()) return 0;
    return getNow();
}

export function endPerfTimer(metric, startedAtMs) {
    if (!isPerfProfilingEnabled()) return;
    if (!startedAtMs) return;
    const elapsed = getNow() - startedAtMs;
    collectSample(metric, elapsed);
}

export function incrementPerfCounter(name, increment = 1) {
    if (!isPerfProfilingEnabled()) return;
    const by = safeNumber(increment);
    const current = counters.get(name) || 0;
    counters.set(name, current + by);
}

export function resetPerfMetrics() {
    metricSamples.clear();
    counters.clear();
}

export function getPerfSnapshot() {
    const metrics = {};

    metricSamples.forEach((values, name) => {
        if (!values.length) return;
        const sorted = [...values].sort((a, b) => a - b);
        const sum = sorted.reduce((acc, v) => acc + v, 0);
        metrics[name] = {
            count: sorted.length,
            minMs: sorted[0],
            maxMs: sorted[sorted.length - 1],
            avgMs: sum / sorted.length,
            medianMs: percentile(sorted, 50),
            p95Ms: percentile(sorted, 95)
        };
    });

    const counterValues = {};
    counters.forEach((value, key) => {
        counterValues[key] = value;
    });

    return {
        enabled: isPerfProfilingEnabled(),
        metrics,
        counters: counterValues
    };
}

export function installPerfDebugApi() {
    if (!globalThis) return;
    globalThis.__horstPerf = {
        isEnabled: isPerfProfilingEnabled,
        now: perfNow,
        startTimer: startPerfTimer,
        endTimer: endPerfTimer,
        incrementCounter: incrementPerfCounter,
        snapshot: getPerfSnapshot,
        reset: resetPerfMetrics
    };
}
