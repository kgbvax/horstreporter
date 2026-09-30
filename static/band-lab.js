import { state } from './state.js';
import { bandColors, getEnabledBands, getMinSnrMode, getSelectedBand, locatorToBounds, haversineKm, hexToRgba } from './utils.js';
import { dashedLine, fillCircle } from './canvas-draw.js';
import { parseAreaPayload } from './live-area.js';

const UPDATE_THROTTLE_MS = 300;
const DX_FETCH_INTERVAL_MS = 15000;
const ACTIVITY_BINS = 12;
const TIME_RANGE_KEY = 'bandLabTimeRangeMinutes';
const BAND_LAB_TIME_RANGE_MINUTES = [15, 30, 60, 120];

// SNR thresholds from the Display section (src/SnrThresholds.svelte). Only the
// slider for the active min-SNR mode is mounted; the other value lives in the
// Svelte store. These are the slider defaults when neither is available.
const DEFAULT_SSB_MIN_DB = 0;
const DEFAULT_CW_MIN_DB = -15;
const SSB_GUIDE_COLOR = '#f97316';
const CW_GUIDE_COLOR = '#22c55e';

// Summary verdict: below this confidence (0-1) the panel says there is not
// enough data instead of passing on the backend's overall status.
const VERDICT_MIN_CONFIDENCE = 0.4;

// Canvas charts live on the Band stats panel, whose background follows the
// theme. Axis text / gridlines that were tuned for a light surface vanish on
// the dark surface, so pick them per theme. Data-driven colors (band colors,
// SSB/CW guide lines, baseline markers) are readable on both and stay fixed.
const CHART_PALETTE_LIGHT = { axisText: '#334155', grid: '#64748b', frame: '#64748b' };
const CHART_PALETTE_DARK = { axisText: '#cbd5e1', grid: '#94a3b8', frame: '#64748b' };
function chartPalette() {
    return document.body.getAttribute('data-theme') === 'dark'
        ? CHART_PALETTE_DARK
        : CHART_PALETTE_LIGHT;
}

const runtime = {
    enabled: false,
    initialized: false,
    lastUpdateAt: 0,
    // Trailing-edge timer for throttled updates, so the last change in a burst
    // (e.g. dragging an SNR threshold slider) is always drawn.
    trailingTimer: null,
    updateSeq: 0,
    lastDxFetchAt: 0,
    dxCache: null,
    dxCacheKey: '',
    dxInFlight: null,
    dxInFlightKey: '',
    dxAbortController: null,
    // Last fetch attempt (success or failure), so a failing endpoint is
    // retried once per fetch interval rather than on every spot update.
    dxAttemptKey: '',
    dxAttemptAt: 0,
    // Per-update snapshot the Now rows (cond-now.js) read: grouped live spots,
    // qth centre, shared distance cache / axis cap and the dx_conditions map.
    rowsSnap: null,
    rowListeners: new Set(),
    // Dirty-check state: skip the full pipeline when neither the spot set nor
    // the filters changed since the last update.
    lastSpotFingerprint: '',
    lastFilterFingerprint: ''
};

export function initBandLab() {
    const content = document.getElementById('band-lab-content');
    const helpToggle = document.getElementById('band-lab-legend-help-toggle');
    const helpPanel = document.getElementById('band-lab-legend-help');
    const timeRangeSelect = document.getElementById('band-lab-time-range');
    if (!content) return;

    if (timeRangeSelect) {
        const persistedMinutes = Number(localStorage.getItem(TIME_RANGE_KEY));
        const fallbackMinutes = Number.parseInt(document.getElementById('minutes')?.value || '15', 10);
        const initialMinutes = BAND_LAB_TIME_RANGE_MINUTES.includes(persistedMinutes)
            ? persistedMinutes
            : BAND_LAB_TIME_RANGE_MINUTES.includes(fallbackMinutes)
                ? fallbackMinutes
                : 15;
        timeRangeSelect.value = String(initialMinutes);
    }

    if (!runtime.initialized) {
        helpToggle?.addEventListener('click', (e) => {
            e.stopPropagation();
            const expanded = helpToggle.getAttribute('aria-expanded') === 'true';
            setLegendHelpVisible(helpToggle, helpPanel, !expanded);
        });

        timeRangeSelect?.addEventListener('change', () => {
            const nextValue = Number.parseInt(timeRangeSelect.value || '15', 10);
            if (BAND_LAB_TIME_RANGE_MINUTES.includes(nextValue)) {
                localStorage.setItem(TIME_RANGE_KEY, String(nextValue));
            }
            updateBandLab({ force: true });
        });

        // The SNR threshold sliders and min-SNR radios are Svelte-mounted and
        // the sliders re-mount on every mode switch, so listen at the document
        // rather than on the elements. The charts filter by the thresholds and
        // draw them as guides, so redraw on every change. The render loop also
        // calls updateBandLab, but it skips frames while the map is paused or
        // being dragged.
        document.addEventListener('input', onSnrControlChange);
        document.addEventListener('change', onSnrControlChange);

        runtime.initialized = true;
    }

    updateBandLab({ force: true });
}

// Dock entry point: switch the data pipeline on/off (the dock owns visibility).
export function setBandLabVisible(visible) {
    if (runtime.enabled === visible) return;
    runtime.enabled = visible;
    if (visible) {
        updateBandLab({ force: true });
    } else {
        // Stale numbers must not linger (e.g. the rail's dashed normal line).
        cancelTrailingUpdate();
        runtime.rowsSnap = null;
        notifyRows();
    }
}

function onSnrControlChange(event) {
    const target = event?.target;
    if (!target) return;
    if (target.id === 'ssb-min-db' || target.id === 'cw-min-db' || target.name === 'min-snr') {
        updateBandLab();
    }
}

export function updateBandLab(options = {}) {
    if (!runtime.enabled) return;

    const now = Date.now();
    const force = options.force === true;
    const sinceLast = now - runtime.lastUpdateAt;
    if (!force && sinceLast < UPDATE_THROTTLE_MS) {
        scheduleTrailingUpdate(UPDATE_THROTTLE_MS - sinceLast);
        return;
    }
    cancelTrailingUpdate();
    runtime.lastUpdateAt = now;

    const summaryEl = document.getElementById('band-lab-summary');
    if (!summaryEl) return;

    const qth = String(document.getElementById('qth')?.value || '').trim().toUpperCase();
    const minutes = getBandLabLookbackMinutes();
    const surroundings = document.getElementById('surroundings')?.checked === true;

    if (!qth) {
        summaryEl.innerHTML = '<div class="text-muted">Enter your locator to see band conditions.</div>';
        runtime.rowsSnap = null;
        notifyRows();
        return;
    }

    // Dirty-check: skip the whole pipeline when neither the spot set nor the
    // filters changed since the last update (quiet periods between spot bursts).
    const spots = state.liveSpots;
    const thresholds = getSnrThresholdsDb();
    const spotFingerprint = `${spots.length}:${spots[0]?.ageSeconds ?? ''}:${spots[spots.length - 1]?.ageSeconds ?? ''}`;
    const filterFingerprint = `${qth}|${minutes}|${surroundings ? 1 : 0}|${getMinSnrMode()}|${thresholds.ssbMinDb}|${thresholds.cwMinDb}|${getSelectedBand()}|${Array.from(getEnabledBands()).sort().join(',')}`;
    if (!force && spotFingerprint === runtime.lastSpotFingerprint && filterFingerprint === runtime.lastFilterFingerprint) {
        return;
    }
    runtime.lastSpotFingerprint = spotFingerprint;
    runtime.lastFilterFingerprint = filterFingerprint;

    const filtered = filterSpots(spots, minutes, thresholds);
    const grouped = groupSpotsByBand(filtered);
    // The mini plots ignore the SNR floor so they look the same whatever the
    // filter; spots below it are drawn gray instead.
    const plotGrouped = groupSpotsByBand(filterSpots(spots, minutes, thresholds, { applySnr: false }));
    const requestSeq = ++runtime.updateSeq;
    const requestKey = `${qth}|${minutes}|${surroundings ? 1 : 0}`;
    const hasDxForKey = Boolean(runtime.dxCache) && runtime.dxCacheKey === requestKey;

    // Render immediately from live spots to avoid a blank panel while dx_conditions loads.
    renderSummary(summaryEl, { loading: !hasDxForKey });
    snapshotRows(grouped, plotGrouped, qth, minutes, hasDxForKey, thresholds);

    // Refetch when the cached response is for another key OR older than the
    // fetch interval. Checking the key alone meant dx_conditions was fetched
    // once per qth/window and the labels, summary and baseline froze.
    if (dxNeedsRefresh(runtime, requestKey, now)) {
        return ensureDxConditions(qth, minutes, surroundings).then(() => {
            // Ignore stale async responses after newer updates were scheduled.
            if (!runtime.enabled || requestSeq !== runtime.updateSeq) return;
            const ready = Boolean(runtime.dxCache) && runtime.dxCacheKey === requestKey;
            renderSummary(summaryEl);
            snapshotRows(grouped, plotGrouped, qth, minutes, ready, thresholds);
        });
    }
    return null;
}

// dxNeedsRefresh reports whether the cached dx_conditions response must be
// refetched for requestKey at time nowMs.
export function dxNeedsRefresh(rt, requestKey, nowMs) {
    if (rt.dxAttemptKey === requestKey && (nowMs - rt.dxAttemptAt) < DX_FETCH_INTERVAL_MS) return false;
    if (!rt.dxCache || rt.dxCacheKey !== requestKey) return true;
    return (nowMs - rt.lastDxFetchAt) >= DX_FETCH_INTERVAL_MS;
}

function scheduleTrailingUpdate(delayMs) {
    if (runtime.trailingTimer) return;
    runtime.trailingTimer = setTimeout(() => {
        runtime.trailingTimer = null;
        updateBandLab();
    }, Math.max(0, delayMs));
}

function cancelTrailingUpdate() {
    if (!runtime.trailingTimer) return;
    clearTimeout(runtime.trailingTimer);
    runtime.trailingTimer = null;
}

// getSnrThresholdsDb returns the SSB and CW min-SNR thresholds (dB) set under
// Display. The mounted slider wins; an unmounted one (only the active mode's
// slider exists) is read from the Svelte store, then the slider default.
export function getSnrThresholdsDb() {
    const store = readUiStoreSnapshot();
    return {
        ssbMinDb: readThresholdDb('ssb-min-db', store?.ssbMinDb, DEFAULT_SSB_MIN_DB),
        cwMinDb: readThresholdDb('cw-min-db', store?.cwMinDb, DEFAULT_CW_MIN_DB),
    };
}

function readThresholdDb(elementId, storeValue, fallback) {
    const el = document.getElementById(elementId);
    const fromDom = el ? Number.parseInt(el.value, 10) : Number.NaN;
    if (Number.isFinite(fromDom)) return fromDom;
    const fromStore = Number.parseInt(String(storeValue ?? ''), 10);
    if (Number.isFinite(fromStore)) return fromStore;
    return fallback;
}

function readUiStoreSnapshot() {
    const store = typeof window !== 'undefined' ? window.__horstUiStore : null;
    if (!store || typeof store.subscribe !== 'function') return null;
    let snapshot = null;
    try {
        const unsubscribe = store.subscribe((value) => { snapshot = value; });
        if (typeof unsubscribe === 'function') unsubscribe();
    } catch {
        return null;
    }
    return snapshot;
}

function filterSpots(spots, minutes, thresholds = getSnrThresholdsDb(), { applySnr = true } = {}) {
    const minSnrMode = getMinSnrMode();
    const { ssbMinDb, cwMinDb } = thresholds;
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    const maxAgeSeconds = minutes * 60;

    return spots.filter((spot) => {
        if (!spot) return false;
        if (spot.ageSeconds > maxAgeSeconds) return false;
        if (!enabledBands.has(spot.band)) return false;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
        if (applySnr && minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (applySnr && minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
        return true;
    });
}

export function getBandLabLookbackMinutes() {
    const selectedValue = Number.parseInt(document.getElementById('band-lab-time-range')?.value || '', 10);
    if (BAND_LAB_TIME_RANGE_MINUTES.includes(selectedValue)) {
        return selectedValue;
    }

    const persisted = Number(localStorage.getItem(TIME_RANGE_KEY));
    if (BAND_LAB_TIME_RANGE_MINUTES.includes(persisted)) {
        return persisted;
    }

    const minutesInputValue = Number.parseInt(document.getElementById('minutes')?.value || '15', 10);
    if (BAND_LAB_TIME_RANGE_MINUTES.includes(minutesInputValue)) {
        return minutesInputValue;
    }

    return 15;
}

function groupSpotsByBand(spots) {
    const out = new Map();
    for (const spot of spots) {
        const band = String(spot.band || '').trim();
        if (!band) continue;
        if (!out.has(band)) out.set(band, []);
        out.get(band).push(spot);
    }
    return out;
}

function renderSummary(summaryEl, options = {}) {
    if (options.loading) {
        summaryEl.innerHTML = '<div class="text-muted">Updating baseline and trend…</div>';
        return;
    }

    const resp = runtime.dxCache;
    if (!resp) {
        summaryEl.innerHTML = '<div class="text-muted">Collecting baseline and trend…</div>';
        return;
    }

    const score = Number(resp.overall_score || 0);
    const confidence = confidence01(resp.confidence);
    const { bestBands, recBands } = enabledBestBands(resp, getEnabledBands());

    const top = recBands.length > 0 ? recBands : bestBands;
    const recommendation = top.length > 0
        ? `<strong>Best now:</strong> ${escapeHtml(top.slice(0, 3).join(', '))}`
        : 'No clear best band yet';

    // The go verdict requires at least one enabled band the backend actually
    // recommends (green/yellow). best_bands is just the top scores and is
    // populated whenever any band has spots — feeding it here would let the
    // verdict pass with zero recommended bands. The "Best now" line keeps the
    // fallback.
    const decision = buildGlobalDecision(resp.status, confidence, recBands.length);
    const confidencePct = Math.round(confidence * 100);

    summaryEl.innerHTML = `
        <div class="band-lab-summary-grid">
            <div class="band-lab-decision-row">
                <span class="band-lab-decision-badge ${decision.className}">${escapeHtml(decision.label)}</span>
                <span class="band-lab-confidence">Confidence ${confidencePct}%</span>
            </div>
            <div><strong>Score:</strong> ${Number.isFinite(score) ? score.toFixed(1) : 'n/a'}</div>
            <div class="band-lab-summary-reco">${recommendation}</div>
        </div>
    `;
}

// Best / recommended bands restricted to the user's enabled set. The backend's
// best_bands / recommended_bands are the top 3 of ALL bands, so filtering them
// would drop an enabled band ranked 4th or lower; re-derive from the full,
// score-sorted `bands` list with the same rule (top 3; green/yellow =
// recommended). Falls back to filtering the short lists if `bands` is absent.
export function enabledBestBands(resp, enabled) {
    const all = Array.isArray(resp?.bands) ? resp.bands.filter((b) => b && enabled.has(b.band)) : null;
    if (all) {
        const top = all.slice(0, 3);
        return {
            bestBands: top.map((b) => b.band),
            recBands: top.filter((b) => b.status === 'green' || b.status === 'yellow').map((b) => b.band),
        };
    }
    const pick = (list) => (Array.isArray(list) ? list : []).filter((b) => enabled.has(b));
    return { bestBands: pick(resp?.best_bands), recBands: pick(resp?.recommended_bands) };
}

// Snapshot what the Now rows need (cond-now.js) and tell the listeners. The
// row DOM lives in the Conditions dock; this module only supplies the data
// and the mini plot.
function snapshotRows(grouped, plotGrouped, qth, minutes, dxReady, thresholds) {
    const qthCenter = getQthCenter(qth);
    // Only the response for the current qth/window: a cached one for another
    // key would label the rows with the previous qth's verdicts until the
    // fetch lands.
    const dxBands = toBandMetricMap(dxReady ? runtime.dxCache : null);
    // Compute the qth→spot distance once and reuse it in the axis cap and
    // every band's plot.
    const distanceCache = qthCenter ? computeDistanceCache(plotGrouped, qthCenter) : null;
    const capKm = qthCenter ? getGlobalDistanceCapKm(plotGrouped, qthCenter, distanceCache) : null;
    const area = dxReady ? parseAreaPayload(runtime.dxCache?.area) : null;
    runtime.rowsSnap = { grouped, plotGrouped, qthCenter, distanceCache, capKm, dxBands, dxReady, area, minutes, thresholds, snrAxis: miniSnrAxis(plotGrouped) };
    notifyRows();
}

function notifyRows() {
    for (const fn of runtime.rowListeners) fn();
}

// cond-now.js subscribes to be told when a fresh snapshot exists. Returns the
// unsubscribe function.
export function subscribeBandRows(fn) {
    runtime.rowListeners.add(fn);
    return () => runtime.rowListeners.delete(fn);
}

// The live area the rows were computed over (live-area.js), or null before the
// first dx_conditions answer for this qth/window or when none was reported.
export function getLiveArea() {
    return runtime.rowsSnap?.area ?? null;
}

// The row model for one band: dx metrics (null until they arrive or when the
// band is unscored), whether dx for this qth/window has landed, the live
// report count and the window in minutes. null before the first snapshot.
export function getBandRow(band) {
    const snap = runtime.rowsSnap;
    if (!snap) return null;
    const points = snap.grouped.get(band) || [];
    return {
        band,
        metrics: snap.dxBands.get(band) || null,
        dxReady: snap.dxReady,
        reports: points.length,
        minutes: snap.minutes,
    };
}

// Normal rate (spots/min, this time of day) of a band in your squares' units,
// for the rail sparkline's dashed line: the region baseline scaled by
// baseline_local_scale, as the removed activity chart did. null until the
// band's dx_conditions metrics have arrived, or without a baseline.
export function getBandNormalRate(band) {
    const m = runtime.rowsSnap?.dxBands.get(band);
    if (!m || m.activity_level === 'no_baseline') return null;
    const base = Number(m.baseline_activity);
    if (!Number.isFinite(base) || base <= 0) return null;
    const rawScale = m.baseline_local_scale;
    const scale = typeof rawScale === 'number' && Number.isFinite(rawScale) && rawScale >= 0 ? rawScale : 1;
    const rate = base * scale;
    return rate > 0 ? rate : null;
}

// SNR axis of the mini plot, shared by every row and independent of the
// Display filter: -25 dB up to +10 dB, or the strongest report of any band
// rounded up to 5 dB (max +30), so strong reports do not pile up on the top edge.
export function miniSnrAxis(grouped) {
    let hi = 10;
    for (const points of grouped.values()) {
        for (const point of points) {
            const snr = Number(point?.snr);
            if (Number.isFinite(snr) && snr > hi) hi = snr;
        }
    }
    return { lo: -25, hi: Math.min(30, Math.ceil(hi / 5) * 5) };
}

// SNR floor of the active Min SNR mode (the "workable" threshold), or null
// when no floor is set. Reports below it are drawn gray in the mini plot.
function activeFloorDb(thresholds) {
    const mode = getMinSnrMode();
    const floor = mode === 'ssb' ? thresholds.ssbMinDb : mode === 'cw' ? thresholds.cwMinDb : null;
    return Number.isFinite(floor) ? floor : null;
}

// Mini distance-vs-SNR plot for the Now rows: same samples and distance axis
// as the removed per-band scatter, drawn small with no labels. The plots do
// not depend on the Display filter: every report is drawn, those below the
// active SNR floor in gray and the rest in the band color. The axes are shared
// by all rows: distance 0..(p95 over all bands) on a square-root scale (so the
// many short paths do not crowd the left edge), SNR per miniSnrAxis. Dashed
// lines are the SSB / CW thresholds from Display.
export function drawBandMiniPlot(canvas, band) {
    const snap = runtime.rowsSnap;
    const prepared = prepareCanvas(canvas, 96, 44);
    if (!prepared) return;
    const { ctx, w, h } = prepared;
    ctx.clearRect(0, 0, w, h);
    const pal = chartPalette();
    const pad = { l: 2, r: 4, t: 3, b: 3 };
    const pw = w - pad.l - pad.r;
    const ph = h - pad.t - pad.b;
    drawChartFrame(ctx, pad, pw, ph, pal);

    const points = snap?.plotGrouped.get(band) || [];
    const data = snap ? computeScatterData(points, snap.qthCenter, snap.capKm, snap.distanceCache, []) : null;
    const axis = snap?.snrAxis || { lo: -25, hi: 10 };
    const yOf = (snr) => {
        const t = (Math.max(axis.lo, Math.min(axis.hi, snr)) - axis.lo) / (axis.hi - axis.lo);
        return pad.t + ph - t * ph;
    };
    for (const guide of snrGuideSpecs(snap?.thresholds || getSnrThresholdsDb())) {
        if (guide.snr < axis.lo || guide.snr > axis.hi) continue;
        ctx.strokeStyle = hexToRgba(guide.color, 0.85);
        const y = yOf(guide.snr);
        dashedLine(ctx, pad.l, y, pad.l + pw, y, [3, 3], 1);
    }
    if (!data) return;
    const dot = hexToRgba(bandColors[band] || '#4f46e5', 0.55);
    const clip = hexToRgba(bandColors[band] || '#4f46e5', 0.9);
    const gray = hexToRgba(pal.grid, 0.5);
    const floor = activeFloorDb(snap.thresholds || getSnrThresholdsDb());
    const below = (sample) => floor !== null && sample.s < floor;
    ctx.font = '9px sans-serif';
    ctx.textAlign = 'right';
    // Gray first, so the workable reports stay on top where they overlap.
    for (const grayPass of [true, false]) {
        for (const sample of data.samples) {
            if (below(sample) !== grayPass) continue;
            const y = yOf(sample.s);
            if (sample.clipped) {
                ctx.fillStyle = grayPass ? gray : clip;
                ctx.fillText('\u203a', pad.l + pw + 2, y + 3);
                continue;
            }
            ctx.fillStyle = grayPass ? gray : dot;
            fillCircle(ctx, pad.l + Math.sqrt(sample.d / data.maxDist) * pw, y, 1.6);
        }
    }
    ctx.textAlign = 'left';
}

// Robust axis cap for the distance axis: the p95 of every report's distance
// from the qth across all bands, NOT the raw max. A single antipodean spot
// would otherwise set the axis max for every band's scatter and compress the
// whole population against the left edge. p95 trims extreme outliers while
// keeping the bulk; the score baseline already uses p90 server-side, so this
// is consistent in spirit. Floored at 500 km so tiny populations still get a
// readable axis. Points beyond the cap are clamped to the edge and marked
// (see drawScatterChart), never silently dropped.
// Precompute the qth→spot haversine distance once per update so both
// getGlobalDistanceCapKm and computeScatterData reuse it instead of each doing
// a full O(n) trig pass (4x total per update before this dedup).
function computeDistanceCache(grouped, qthCenter) {
    const cache = new Map();
    for (const points of grouped.values()) {
        for (const point of points) {
            if (cache.has(point)) continue;
            cache.set(point, haversineKm(qthCenter.lat, qthCenter.lng, Number(point.lat), Number(point.lng)));
        }
    }
    return cache;
}

function getGlobalDistanceCapKm(grouped, qthCenter, distanceCache) {
    const distances = [];
    for (const points of grouped.values()) {
        for (const point of points) {
            const d = distanceCache ? distanceCache.get(point) : haversineKm(qthCenter.lat, qthCenter.lng, Number(point.lat), Number(point.lng));
            if (Number.isFinite(d)) distances.push(d);
        }
    }
    if (distances.length === 0) return 500;
    distances.sort((a, b) => a - b);
    const cap = quantileSorted(distances, 0.95);
    return Number.isFinite(cap) ? Math.max(500, cap) : 500;
}

function getQthCenter(qth) {
    if (!/^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(qth)) return null;
    const bounds = locatorToBounds(qth);
    if (!bounds) return null;
    return {
        lat: (bounds[0][0] + bounds[1][0]) / 2,
        lng: (bounds[0][1] + bounds[1][1]) / 2
    };
}

// snrGuideSpecs: the dashed SNR guide lines on the Distance vs SNR chart, one
// per threshold set under Display (see getSnrThresholdsDb).
export function snrGuideSpecs(thresholds = {}) {
    const ssb = Number.isFinite(thresholds.ssbMinDb) ? thresholds.ssbMinDb : DEFAULT_SSB_MIN_DB;
    const cw = Number.isFinite(thresholds.cwMinDb) ? thresholds.cwMinDb : DEFAULT_CW_MIN_DB;
    return [
        { snr: ssb, color: SSB_GUIDE_COLOR, label: `SSB ${ssb} dB` },
        { snr: cw, color: CW_GUIDE_COLOR, label: `CW ${cw} dB` },
    ];
}

// Pure: turn spots + qth center into scatter samples + axis ranges, or null
// when there is no usable distance data. Extracted from drawScatterChart so the
// math is unit-testable without a canvas. guideSnrs are the SNR guide values
// (the SSB / CW thresholds); the SNR axis always spans them so a guide set
// outside the -20..20 dB default range stays visible.
export function computeScatterData(points, qthCenter, globalDistanceCapKm, distanceCache, guideSnrs = [DEFAULT_SSB_MIN_DB, DEFAULT_CW_MIN_DB]) {
    if (!qthCenter || !points || points.length === 0) return null;
    const samples = points
        .map((p) => ({
            d: distanceCache ? distanceCache.get(p) : haversineKm(qthCenter.lat, qthCenter.lng, Number(p.lat), Number(p.lng)),
            s: Number(p.snr || 0)
        }))
        .filter((v) => Number.isFinite(v.d) && Number.isFinite(v.s));
    if (samples.length === 0) return null;
    // Axis max is the robust global p95 cap, not the raw max — so one distant
    // outlier can't stretch the scale. Points beyond the cap are clamped to the
    // right edge and flagged `clipped` so the renderer marks them with a
    // chevron instead of plotting them off-chart or inflating the axis.
    const maxDist = Math.max(500, Number(globalDistanceCapKm) || 0);
    for (const s of samples) s.clipped = s.d > maxDist;
    const guides = (Array.isArray(guideSnrs) ? guideSnrs : []).map(Number).filter(Number.isFinite);
    const minSnr = Math.min(-20, ...guides, ...samples.map((s) => s.s));
    const maxSnr = Math.max(20, ...guides, ...samples.map((s) => s.s));
    const snrRange = Math.max(10, maxSnr - minSnr);
    return { samples, maxDist, minSnr, maxSnr, snrRange };
}

// utcSlotOfDayFromMs mirrors backend dx_conditions.go:utcSlotOfDay (a 30-min
// UTC slot index 0..47). Frontend version takes ms so it composes with
// Date.now() and test stubs.
export function utcSlotOfDayFromMs(timestampMs) {
    const d = new Date(timestampMs);
    return d.getUTCHours() * 2 + Math.floor(d.getUTCMinutes() / 30);
}

// computeActivityChartData turns live spots + dx_conditions per-band metrics
// into the numbers the chart needs. Returns a plain object so it can be unit
// tested without a canvas.
//
// Shape:
//   binRates[i]               — spots/min in bin i (i=0 oldest, i=11 newest)
//   baselineRatesPerBin[i]    — normal spots/min for the slot containing bin i's centre,
//                               scaled to your squares by baseline_local_scale
//   baselineClusterUsedPerBin[i] — true when the per-slot qth baseline was used
//   yMax                       — y-axis upper bound in spots/min
//   binMinutes                 — width of one bin in minutes
//   sloChanges                 — bin indices where the slot index changed vs the previous bin
export function computeActivityChartData(points, bandMetrics, minutes, nowMs) {
    const safeMinutes = Math.max(1, Number(minutes) || 1);
    const totalWindowSec = safeMinutes * 60;
    const binSizeSec = totalWindowSec / ACTIVITY_BINS;
    const binMinutes = binSizeSec / 60;

    const counts = new Array(ACTIVITY_BINS).fill(0);
    if (Array.isArray(points)) {
        for (const point of points) {
            if (!point) continue;
            const age = Number(point.ageSeconds || 0);
            if (!Number.isFinite(age) || age < 0 || age > totalWindowSec) continue;
            const idx = Math.min(ACTIVITY_BINS - 1, Math.floor((totalWindowSec - age) / binSizeSec));
            counts[idx] += 1;
        }
    }
    const liveBinRates = counts.map((c) => c / binMinutes);

    // Prefer the backend's Postgres-backed activity series when present: it
    // covers the full selected window (incl. 120 min) from dx_raw_spots,
    // whereas liveSpots is bounded by the ≤60-min SSE stream/retention and
    // leaves the older bins empty. Fall back to live-spot counts for backends
    // that don't supply activity_by_bin (e.g. dev without Postgres).
    const backendByBin = Array.isArray(bandMetrics?.activity_by_bin)
        ? bandMetrics.activity_by_bin
        : null;
    let binRates;
    if (backendByBin && backendByBin.length === ACTIVITY_BINS) {
        binRates = backendByBin.map((v) => (Number.isFinite(v) && v > 0 ? Number(v) : 0));
    } else {
        binRates = liveBinRates;
    }

    const baselineBySlot = Array.isArray(bandMetrics?.baseline_activity_by_slot) ? bandMetrics.baseline_activity_by_slot : [];
    const slotUsedByCluster = Array.isArray(bandMetrics?.baseline_slot_used_by_cluster) ? bandMetrics.baseline_slot_used_by_cluster : [];
    const currentSlotBaselineRate = Math.max(0, Number(bandMetrics?.baseline_activity || 0));
    const currentSlotClusterUsed = bandMetrics?.cluster_baseline_used === true;
    // The baseline is the 6x6-square region's normal; the bars are your
    // squares only. baseline_local_scale (your squares' share of the region's
    // reports on this band, same live span) puts the line in bar units, so
    // bars vs line over the recent window shows the ratio the card label
    // reports. 0 = no regional reports to scale by, so no line. Absent (older
    // backend) = draw the unscaled line as before.
    const rawScale = bandMetrics?.baseline_local_scale;
    const baselineScale = typeof rawScale === 'number' && Number.isFinite(rawScale) && rawScale >= 0 ? rawScale : 1;

    const baselineRatesPerBin = new Array(ACTIVITY_BINS).fill(0);
    const baselineClusterUsedPerBin = new Array(ACTIVITY_BINS).fill(false);
    const slotChanges = [];
    let prevSlot = -1;
    for (let i = 0; i < ACTIVITY_BINS; i++) {
        // Bin i covers ages [totalWindowSec - (i+1)*binSizeSec, totalWindowSec - i*binSizeSec].
        // Centre age = totalWindowSec - (i + 0.5) * binSizeSec.
        const centreAgeSec = totalWindowSec - (i + 0.5) * binSizeSec;
        const centreMs = nowMs - centreAgeSec * 1000;
        const slot = utcSlotOfDayFromMs(centreMs);
        let rate = 0;
        let used = false;
        if (slot >= 0 && slot < baselineBySlot.length) {
            const v = Number(baselineBySlot[slot]);
            if (Number.isFinite(v) && v > 0) {
                rate = v;
                used = Boolean(slotUsedByCluster[slot]);
            }
        }
        // Fallback: if the per-slot array didn't carry data, fall back to the
        // current-slot single value (preserves the v1 behaviour for backends
        // that haven't returned the new field yet).
        if (rate === 0 && baselineBySlot.length === 0 && currentSlotBaselineRate > 0) {
            rate = currentSlotBaselineRate;
            used = currentSlotClusterUsed;
        }
        baselineRatesPerBin[i] = rate * baselineScale;
        baselineClusterUsedPerBin[i] = used;
        if (i === 0) {
            prevSlot = slot;
        } else if (slot !== prevSlot) {
            slotChanges.push(i);
            prevSlot = slot;
        }
    }

    const yMaxCandidate = Math.max(0, ...binRates, ...baselineRatesPerBin);
    // Always reserve some headroom so an entirely-zero chart still renders sensibly.
    const yMax = yMaxCandidate > 0 ? yMaxCandidate * 1.1 : 0.5;

    return {
        binRates,
        baselineRatesPerBin,
        baselineClusterUsedPerBin,
        yMax,
        binMinutes,
        slotChanges,
    };
}

// formatWindowMinutes renders a look-back window as "15 min" / "1 h" / "2 h".
export function formatWindowMinutes(minutes) {
    const m = Math.max(0, Math.round(Number(minutes) || 0));
    if (m >= 60 && m % 60 === 0) return `${m / 60} h`;
    return `${m} min`;
}

function drawChartFrame(ctx, pad, pw, ph, pal = CHART_PALETTE_LIGHT) {
    ctx.strokeStyle = hexToRgba(pal.frame, 0.45);
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad.l, pad.t + ph);
    ctx.lineTo(pad.l + pw, pad.t + ph);
    ctx.lineTo(pad.l + pw, pad.t);
    ctx.stroke();
}

function toBandMetricMap(dxResp) {
    const map = new Map();
    const arr = Array.isArray(dxResp?.bands) ? dxResp.bands : [];
    for (const item of arr) {
        if (!item || !item.band) continue;
        map.set(String(item.band), item);
    }
    return map;
}

function compareBand(a, b) {
    const as = parseBandMeters(a);
    const bs = parseBandMeters(b);
    if (!Number.isFinite(as) && !Number.isFinite(bs)) return a.localeCompare(b);
    if (!Number.isFinite(as)) return 1;
    if (!Number.isFinite(bs)) return -1;
    return bs - as;
}

function parseBandMeters(band) {
    const m = String(band || '').match(/^(\d+(?:\.\d+)?)m$/i);
    return m ? Number(m[1]) : Number.NaN;
}

// dxReady: dx_conditions for the current qth/window has arrived. A band with
// live spots but no entry in it (only RBN/WSPR reports, or none at or above
// the conditions SNR floor) is "not scored" rather than looking like it is
// still loading.
export function bandHeadText(band, bandMetrics, dxReady = false) {
    const label = bandActivityLabel(bandMetrics) || (dxReady && !bandMetrics ? 'not scored' : '');
    return label ? `${band}: ${label}` : band;
}

const ACTIVITY_LEVEL_TEXT = {
    above: 'above normal',
    normal: 'normal',
    below: 'below normal',
};

// Card label: the band against its own normal for this time of day, from the
// backend's like-for-like regional comparison (activity_level /
// activity_ratio), plus the DX-reach qualifier when the band's P90 path length
// is off its normal. It deliberately does not compare bands with each other:
// 10m carries a fraction of 40m's FT8 volume even when wide open. Empty until
// dx metrics for the band have arrived, so the card never flashes a verdict
// computed from missing data.
export function bandActivityLabel(bandMetrics) {
    const level = String(bandMetrics?.activity_level || '');
    if (!level) return '';
    let text;
    if (level === 'low_sample') {
        text = 'low sample';
    } else if (level === 'no_baseline') {
        text = 'no baseline';
    } else if (ACTIVITY_LEVEL_TEXT[level]) {
        text = `${ACTIVITY_LEVEL_TEXT[level]} ${formatRatio(bandMetrics.activity_ratio)}`;
    } else {
        return '';
    }
    const reach = String(bandMetrics?.reach_level || '');
    if (reach === 'longer' || reach === 'shorter') {
        text += `, ${reach} reach`;
    }
    return text;
}

function formatRatio(ratio) {
    const r = Number(ratio);
    if (!Number.isFinite(r) || r < 0) return '';
    // Two decimals below 10 so the shown ratio never contradicts the level
    // at a threshold (1.49 must not read "normal 1.5×").
    return r >= 10 ? `${Math.round(r)}×` : `${r.toFixed(2)}×`;
}

// The backend reports confidence on a 0-99 scale, but the decision/tier
// thresholds and the summary display below expect a 0-1 fraction. Normalise at
// the read sites so a ~98.7 value doesn't clamp to a permanent "100%".
function confidence01(raw) {
    const v = Number(raw);
    if (!Number.isFinite(v)) return 0;
    return Math.max(0, Math.min(1, v / 100));
}

const VERDICT_NO_DATA = { label: 'Not enough data yet', className: 'is-wait' };
const VERDICT_GOOD = { label: 'Good: worth turning the radio on', className: 'is-go' };
const VERDICT_FAIR = { label: 'Fair: worth monitoring', className: 'is-watch' };
const VERDICT_POOR = { label: 'Poor: low payoff now', className: 'is-wait' };

// buildGlobalDecision is the panel's single verdict. It follows the backend's
// overall status (dx_conditions.go classifyOverallStatus: green >= 65,
// yellow >= 35, else red; grey below 15 confidence) so it cannot contradict
// the condition the backend reports. confidence is 0-1 (see confidence01).
// Below VERDICT_MIN_CONFIDENCE, or for grey / missing status, there is not
// enough data. Green needs at least one recommended enabled band; without one
// it reads as fair.
export function buildGlobalDecision(status, confidence, recommendedCount) {
    const conf = Number(confidence);
    if (!Number.isFinite(conf) || conf < VERDICT_MIN_CONFIDENCE) return VERDICT_NO_DATA;
    switch (String(status || '')) {
        case 'green':
            return recommendedCount > 0 ? VERDICT_GOOD : VERDICT_FAIR;
        case 'yellow':
            return VERDICT_FAIR;
        case 'red':
            return VERDICT_POOR;
        default:
            return VERDICT_NO_DATA;
    }
}

function quantileSorted(sorted, q) {
    if (!Array.isArray(sorted) || sorted.length === 0) return Number.NaN;
    if (sorted.length === 1) return sorted[0];
    const clampedQ = Math.max(0, Math.min(1, Number(q) || 0));
    const idx = (sorted.length - 1) * clampedQ;
    const lo = Math.floor(idx);
    const hi = Math.ceil(idx);
    if (lo === hi) return sorted[lo];
    const t = idx - lo;
    return sorted[lo] + ((sorted[hi] - sorted[lo]) * t);
}

function prepareCanvas(canvas, fallbackW, fallbackH) {
    if (!canvas) return null;
    const ctx = canvas.getContext('2d');
    if (!ctx) return null;

    const cssW = Math.max(1, Math.round(canvas.clientWidth || fallbackW || 230));
    const cssH = Math.max(1, Math.round(canvas.clientHeight || fallbackH || 120));
    const dpr = Math.max(1, Math.min(3, window.devicePixelRatio || 1));

    const pixelW = Math.round(cssW * dpr);
    const pixelH = Math.round(cssH * dpr);
    if (canvas.width !== pixelW || canvas.height !== pixelH) {
        canvas.width = pixelW;
        canvas.height = pixelH;
    }

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    return { ctx, w: cssW, h: cssH };
}

function setLegendHelpVisible(helpToggle, helpPanel, visible) {
    if (!helpToggle || !helpPanel) return;
    helpPanel.style.display = visible ? 'block' : 'none';
    helpToggle.setAttribute('aria-expanded', visible ? 'true' : 'false');
}

function escapeHtml(value) {
    return String(value ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

async function ensureDxConditions(qth, minutes, surroundings) {
    const key = `${qth}|${minutes}|${surroundings ? 1 : 0}`;
    const now = Date.now();

    if (runtime.dxCache && runtime.dxCacheKey === key && (now - runtime.lastDxFetchAt) < DX_FETCH_INTERVAL_MS) {
        return runtime.dxCache;
    }

    if (runtime.dxInFlight) {
        if (runtime.dxInFlightKey === key) {
            return runtime.dxInFlight;
        }
        runtime.dxAbortController?.abort();
    }

    const controller = new AbortController();
    runtime.dxAbortController = controller;
    runtime.dxAttemptKey = key;
    runtime.dxAttemptAt = now;
    runtime.dxInFlightKey = key;
    runtime.dxInFlight = (async () => {
        try {
            const params = new URLSearchParams();
            params.set('qth', qth);
            params.set('minutes', String(minutes));
            if (surroundings) params.set('surroundings', 'true');
            params.set('rings', 'auto');

            const response = await fetch(`/api/dx_conditions?${params.toString()}`, { signal: controller.signal });
            if (!response.ok) throw new Error(`dx_conditions HTTP ${response.status}`);
            const payload = await response.json();
            runtime.dxCache = payload;
            runtime.dxCacheKey = key;
            runtime.lastDxFetchAt = Date.now();
            return payload;
        } catch (err) {
            if (err?.name === 'AbortError') {
                return runtime.dxCache;
            }
            if (!runtime.dxCache) {
                console.warn('Band stats dx_conditions fetch failed:', err);
            }
            return runtime.dxCache;
        } finally {
            // Only clear the in-flight tracking when THIS request is still the
            // current one. If a newer request (different key) took over while
            // this one was aborted, its finally must not clobber the newer
            // request's tracking — otherwise a subsequent call with the newer
            // key would fail to dedupe and start a duplicate fetch.
            if (runtime.dxAbortController === controller) {
                runtime.dxAbortController = null;
                runtime.dxInFlight = null;
                runtime.dxInFlightKey = '';
            }
        }
    })();

    return runtime.dxInFlight;
}
