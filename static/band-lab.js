import { state } from './state.js';
import { bandColors, formatNumber, getEnabledBands, getMinSnrMode, getSelectedBand, locatorToBounds, haversineKm, hexToRgba } from './utils.js';
import { dashedLine, fillCircle } from './canvas-draw.js';

const ENABLE_KEY = 'bandLabEnabled';
const UPDATE_THROTTLE_MS = 300;
const DX_FETCH_INTERVAL_MS = 15000;
const ACTIVITY_BINS = 12;
const WINDOW_SIZE_KEY = 'bandLabWindowSize';
const TIME_RANGE_KEY = 'bandLabTimeRangeMinutes';
const BAND_LAB_TIME_RANGE_MINUTES = [15, 30, 60, 120];

const runtime = {
    enabled: false,
    initialized: false,
    lastUpdateAt: 0,
    updateSeq: 0,
    lastDxFetchAt: 0,
    dxCache: null,
    dxCacheKey: '',
    dxInFlight: null,
    dxInFlightKey: '',
    dxAbortController: null,
    onLayoutChange: null
};

export function initBandLab(options = {}) {
    const toggleButton = document.getElementById('band-stats-toggle');
    const content = document.getElementById('band-lab-content');
    const windowEl = document.getElementById('band-lab-window');
    const closeBtn = document.getElementById('band-lab-window-close');
    const helpToggle = document.getElementById('band-lab-legend-help-toggle');
    const helpPanel = document.getElementById('band-lab-legend-help');
    const timeRangeSelect = document.getElementById('band-lab-time-range');
    if (!content || !windowEl) return;

    if (typeof options.onLayoutChange === 'function') {
        runtime.onLayoutChange = options.onLayoutChange;
    }

    runtime.enabled = localStorage.getItem(ENABLE_KEY) === 'true';
    setBandStatsVisible(windowEl, toggleButton, runtime.enabled);

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

    restoreWindowWidth(windowEl);
    if (!runtime.initialized) {
        setupWindowResize(windowEl);
    }

    if (!runtime.initialized) {
        toggleButton?.addEventListener('click', () => {
            runtime.enabled = !runtime.enabled;
            localStorage.setItem(ENABLE_KEY, runtime.enabled ? 'true' : 'false');
            setBandStatsVisible(windowEl, toggleButton, runtime.enabled);
            updateBandLab({ force: true });
        });

        closeBtn?.addEventListener('click', () => {
            runtime.enabled = false;
            localStorage.setItem(ENABLE_KEY, 'false');
            setBandStatsVisible(windowEl, toggleButton, runtime.enabled);
            setLegendHelpVisible(helpToggle, helpPanel, false);
        });

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

        runtime.initialized = true;
    }

    updateBandLab({ force: true });
}

function setBandStatsVisible(windowEl, toggleButton, visible) {
    windowEl.classList.toggle('is-hidden', !visible);
    runtime.onLayoutChange?.();
    if (!toggleButton) return;
    // Mirror the Chase Queue toggle: the button hides while the panel is open
    // (the panel carries its own close), and reappears when it closes.
    toggleButton.style.display = visible ? 'none' : '';
    toggleButton.setAttribute('aria-pressed', visible ? 'true' : 'false');
}

export function updateBandLab(options = {}) {
    if (!runtime.enabled) return;

    const now = Date.now();
    const force = options.force === true;
    if (!force && (now - runtime.lastUpdateAt) < UPDATE_THROTTLE_MS) {
        return;
    }
    runtime.lastUpdateAt = now;

    const summaryEl = document.getElementById('band-lab-summary');
    const cardsEl = document.getElementById('band-lab-cards');
    if (!summaryEl || !cardsEl) return;

    const target = String(document.getElementById('target')?.value || '').trim().toUpperCase();
    const minutes = getBandLabLookbackMinutes();
    const surroundings = document.getElementById('surroundings')?.checked === true;

    if (!target) {
        summaryEl.innerHTML = '<div class="text-muted">Enter a target to inspect band conditions.</div>';
        cardsEl.innerHTML = '';
        return;
    }

    const filtered = filterSpots(state.liveSpots, minutes);
    const grouped = groupSpotsByBand(filtered);
    const requestSeq = ++runtime.updateSeq;
    const requestKey = `${target}|${minutes}|${surroundings ? 1 : 0}`;
    const hasFreshDx = runtime.dxCache && runtime.dxCacheKey === requestKey;

    // Render immediately from live spots to avoid a blank panel while dx_conditions loads.
    renderSummary(summaryEl, { loading: !hasFreshDx });
    renderBandCards(cardsEl, grouped, target, minutes);

    void ensureDxConditions(target, minutes, surroundings).then(() => {
        // Ignore stale async responses after newer updates were scheduled.
        if (!runtime.enabled || requestSeq !== runtime.updateSeq) return;
        renderSummary(summaryEl);
        renderBandCards(cardsEl, grouped, target, minutes);
    });
}

function filterSpots(spots, minutes) {
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    const maxAgeSeconds = minutes * 60;

    return spots.filter((spot) => {
        if (!spot) return false;
        if (spot.ageSeconds > maxAgeSeconds) return false;
        if (!enabledBands.has(spot.band)) return false;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
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

function sanitizeBandId(band) {
    return String(band || '').replace(/[^a-zA-Z0-9_-]/g, '_');
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
    const condition = String(resp.condition || 'Unknown');
    const bestBands = Array.isArray(resp.best_bands) ? resp.best_bands : [];
    const recBands = Array.isArray(resp.recommended_bands) ? resp.recommended_bands : [];

    const top = recBands.length > 0 ? recBands : bestBands;
    const recommendation = top.length > 0
        ? `Best now: ${top.slice(0, 3).join(', ')}`
        : 'No clear best band yet';

    const decision = buildGlobalDecision(score, confidence, top.length);
    const confidencePct = Math.round(confidence * 100);

    summaryEl.innerHTML = `
        <div class="band-lab-summary-grid">
            <div class="band-lab-decision-row">
                <span class="band-lab-decision-badge ${decision.className}">${escapeHtml(decision.label)}</span>
                <span class="band-lab-confidence">confidence ${confidencePct}%</span>
            </div>
            <div><strong>Condition:</strong> ${escapeHtml(condition)}</div>
            <div><strong>Score:</strong> ${Number.isFinite(score) ? score.toFixed(1) : 'n/a'}</div>
            <div class="band-lab-summary-reco"><strong>Decision:</strong> ${escapeHtml(recommendation)}</div>
        </div>
    `;
}

function renderBandCards(cardsEl, grouped, target, minutes) {
    const bands = Array.from(grouped.keys()).sort((a, b) => compareBand(a, b));
    if (bands.length === 0) {
        cardsEl.innerHTML = '<div class="text-muted small">No reports match current filters.</div>';
        return;
    }

    const targetCenter = getTargetCenter(target);
    const dxBands = toBandMetricMap(runtime.dxCache);
    const globalMaxDistanceKm = targetCenter ? getGlobalMaxDistanceKm(grouped, targetCenter) : null;
    const allBandCounts = bands.map((band) => (grouped.get(band) || []).length);
    const totalReportsAllBands = allBandCounts.reduce((sum, n) => sum + n, 0);
    const maxReportsSingleBand = Math.max(0, ...allBandCounts);

    cardsEl.innerHTML = bands.map((band) => {
        const points = grouped.get(band) || [];
        const safeBand = sanitizeBandId(band);
        const count = points.length;
        const rec = buildBandRecommendation(band, points, dxBands.get(band), {
            totalReportsAllBands,
            maxReportsSingleBand
        });

        return `
            <div class="band-lab-card" style="border-left-color: ${bandColors[band] || '#999'};">
                <div class="band-lab-card-head">
                    <span class="band-lab-band">${escapeHtml(`${band} - ${rec}`)}</span>
                    <span class="band-lab-meta">${formatNumber(count)} reports</span>
                </div>
                <div class="band-lab-card-charts">
                    <div class="band-lab-chart-block">
                        <div class="band-lab-chart-title">Distance vs SNR</div>
                        <canvas id="band-lab-scatter-${safeBand}" width="230" height="120"></canvas>
                        ${targetCenter ? '' : '<div class="band-lab-chart-note">Distance plot needs locator target (e.g. JO32).</div>'}
                    </div>
                    <div class="band-lab-chart-block">
                        <div class="band-lab-chart-title">Reports over time + baseline</div>
                        <canvas id="band-lab-activity-${safeBand}" width="230" height="120"></canvas>
                    </div>
                </div>
            </div>
        `;
    }).join('');

    for (const band of bands) {
        const safeBand = sanitizeBandId(band);
        const points = grouped.get(band) || [];
        drawActivityChart(document.getElementById(`band-lab-activity-${safeBand}`), points, dxBands.get(band), minutes);
        drawScatterChart(document.getElementById(`band-lab-scatter-${safeBand}`), points, targetCenter, band, globalMaxDistanceKm);
    }
}

function getGlobalMaxDistanceKm(grouped, targetCenter) {
    let maxDistance = 500;
    for (const points of grouped.values()) {
        for (const point of points) {
            const d = haversineKm(targetCenter.lat, targetCenter.lng, Number(point.lat), Number(point.lng));
            if (Number.isFinite(d)) {
                maxDistance = Math.max(maxDistance, d);
            }
        }
    }
    return maxDistance;
}

function getTargetCenter(target) {
    if (!/^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(target)) return null;
    const bounds = locatorToBounds(target);
    if (!bounds) return null;
    return {
        lat: (bounds[0][0] + bounds[1][0]) / 2,
        lng: (bounds[0][1] + bounds[1][1]) / 2
    };
}

// Pure: turn spots + target center into scatter samples + axis ranges, or null
// when there is no usable distance data. Extracted from drawScatterChart so the
// math is unit-testable without a canvas.
export function computeScatterData(points, targetCenter, globalMaxDistanceKm) {
    if (!targetCenter || !points || points.length === 0) return null;
    const samples = points
        .map((p) => ({
            d: haversineKm(targetCenter.lat, targetCenter.lng, Number(p.lat), Number(p.lng)),
            s: Number(p.snr || 0)
        }))
        .filter((v) => Number.isFinite(v.d) && Number.isFinite(v.s));
    if (samples.length === 0) return null;
    const maxDist = Math.max(500, Number(globalMaxDistanceKm) || 0, ...samples.map((s) => s.d));
    const minSnr = Math.min(-20, -15, ...samples.map((s) => s.s));
    const maxSnr = Math.max(20, 0, ...samples.map((s) => s.s));
    const snrRange = Math.max(10, maxSnr - minSnr);
    return { samples, maxDist, minSnr, maxSnr, snrRange };
}

function drawScatterChart(canvas, points, targetCenter, band, globalMaxDistanceKm) {
    const prepared = prepareCanvas(canvas, 230, 120);
    if (!prepared) return;
    const { ctx, w, h } = prepared;

    const pad = { l: 30, r: 10, t: 10, b: 20 };
    const pw = w - pad.l - pad.r;
    const ph = h - pad.t - pad.b;

    ctx.clearRect(0, 0, w, h);
    drawChartFrame(ctx, pad, pw, ph);

    const data = computeScatterData(points, targetCenter, globalMaxDistanceKm);
    if (!data) {
        drawNoData(ctx, w, h, 'no distance data');
        return;
    }
    const { samples, maxDist, minSnr, maxSnr, snrRange } = data;

    const drawSnrGuide = (snr, color, label) => {
        const y = pad.t + ph - ((snr - minSnr) / snrRange) * ph;
        if (!Number.isFinite(y) || y < pad.t || y > (pad.t + ph)) return;
        ctx.strokeStyle = hexToRgba(color, 0.85);
        dashedLine(ctx, pad.l, y, pad.l + pw, y, [4, 3], 1);

        ctx.fillStyle = hexToRgba(color, 0.95);
        ctx.font = '10px sans-serif';
        ctx.fillText(label, pad.l + 3, Math.max(pad.t + 10, y - 2));
    };

    drawSnrGuide(0, '#f97316', 'phone 0 dB');
    drawSnrGuide(-15, '#22c55e', 'cw -15 dB');

    const sortedDistances = samples.map((s) => s.d).sort((a, b) => a - b);
    const p50Dist = quantileSorted(sortedDistances, 0.5);
    const p90Dist = quantileSorted(sortedDistances, 0.9);

    if (Number.isFinite(p50Dist)) {
        const x = pad.l + (p50Dist / maxDist) * pw;
        ctx.strokeStyle = hexToRgba('#64748b', 0.38);
        dashedLine(ctx, x, pad.t, x, pad.t + ph, [2, 4], 0.9);
    }

    if (Number.isFinite(p90Dist)) {
        const x = pad.l + (p90Dist / maxDist) * pw;
        ctx.strokeStyle = hexToRgba('#64748b', 0.3);
        dashedLine(ctx, x, pad.t, x, pad.t + ph, [2, 5], 0.9);
    }

    ctx.fillStyle = hexToRgba(bandColors[band] || '#4f46e5', 0.5);
    for (const sample of samples) {
        const x = pad.l + (sample.d / maxDist) * pw;
        const y = pad.t + ph - ((sample.s - minSnr) / snrRange) * ph;
        fillCircle(ctx, x, y, 2.2);
    }

    const yTicks = [maxSnr, (maxSnr + minSnr) / 2, minSnr];
    ctx.fillStyle = hexToRgba('#334155', 0.92);
    ctx.font = '10px sans-serif';
    ctx.textAlign = 'right';
    yTicks.forEach((tick) => {
        const y = pad.t + ph - ((tick - minSnr) / snrRange) * ph;
        ctx.fillText(`${Math.round(tick)}`, pad.l - 4, y + 3);
    });
    ctx.textAlign = 'left';

    const xTicks = buildDistanceTicks(maxDist);
    ctx.textAlign = 'center';
    xTicks.forEach((tickKm) => {
        const x = pad.l + (tickKm / maxDist) * pw;
        ctx.strokeStyle = hexToRgba('#64748b', 0.35);
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(x, pad.t + ph);
        ctx.lineTo(x, pad.t + ph + 4);
        ctx.stroke();
        ctx.fillStyle = hexToRgba('#334155', 0.9);
        ctx.fillText(formatKmLabel(tickKm), x, pad.t + ph + 12);
    });
    ctx.textAlign = 'left';
    ctx.fillText('SNR dB', 2, pad.t + 8);
}

function buildDistanceTicks(maxDistKm) {
    const targetTickCount = 4;
    const rawStep = Math.max(250, maxDistKm / targetTickCount);
    const magnitude = 10 ** Math.floor(Math.log10(rawStep));
    const normalized = rawStep / magnitude;
    let step;
    if (normalized <= 1) step = 1 * magnitude;
    else if (normalized <= 2) step = 2 * magnitude;
    else if (normalized <= 5) step = 5 * magnitude;
    else step = 10 * magnitude;

    const ticks = [];
    for (let v = 0; v <= maxDistKm + 1e-6; v += step) {
        ticks.push(v);
    }
    const lastTick = ticks[ticks.length - 1] || 0;
    if (Math.abs(lastTick - maxDistKm) > step * 0.2) {
        ticks.push(maxDistKm);
    } else {
        ticks[ticks.length - 1] = maxDistKm;
    }
    return ticks;
}

function formatKmLabel(km) {
    const rounded = Math.round(km);
    if (rounded >= 1000) {
        return `${Math.round(rounded / 100) / 10}k`;
    }
    return String(rounded);
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
//   baselineRatesPerBin[i]    — historical spots/min for the slot containing bin i's centre
//   baselineTargetUsedPerBin[i] — true when the per-slot target baseline was used
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
    const binRates = counts.map((c) => c / binMinutes);

    const baselineBySlot = Array.isArray(bandMetrics?.baseline_activity_by_slot) ? bandMetrics.baseline_activity_by_slot : [];
    const slotUsedByTarget = Array.isArray(bandMetrics?.baseline_slot_used_by_target) ? bandMetrics.baseline_slot_used_by_target : [];
    const currentSlotBaselineRate = Math.max(0, Number(bandMetrics?.baseline_activity || 0));
    const currentSlotTargetUsed = bandMetrics?.target_baseline_used === true;

    const baselineRatesPerBin = new Array(ACTIVITY_BINS).fill(0);
    const baselineTargetUsedPerBin = new Array(ACTIVITY_BINS).fill(false);
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
                used = Boolean(slotUsedByTarget[slot]);
            }
        }
        // Fallback: if the per-slot array didn't carry data, fall back to the
        // current-slot single value (preserves the v1 behaviour for backends
        // that haven't returned the new field yet).
        if (rate === 0 && baselineBySlot.length === 0 && currentSlotBaselineRate > 0) {
            rate = currentSlotBaselineRate;
            used = currentSlotTargetUsed;
        }
        baselineRatesPerBin[i] = rate;
        baselineTargetUsedPerBin[i] = used;
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
        baselineTargetUsedPerBin,
        yMax,
        binMinutes,
        slotChanges,
    };
}

function formatRate(rate) {
    if (!Number.isFinite(rate) || rate <= 0) return '0';
    if (rate >= 10) return `${rate.toFixed(0)}/min`;
    if (rate >= 1) return `${rate.toFixed(1)}/min`;
    return `${rate.toFixed(2)}/min`;
}

function drawActivityChart(canvas, points, bandMetrics, minutes) {
    const prepared = prepareCanvas(canvas, 230, 120);
    if (!prepared) return;
    const { ctx, w, h } = prepared;

    const pad = { l: 38, r: 10, t: 10, b: 20 };
    const pw = w - pad.l - pad.r;
    const ph = h - pad.t - pad.b;

    ctx.clearRect(0, 0, w, h);
    drawChartFrame(ctx, pad, pw, ph);

    const data = computeActivityChartData(points, bandMetrics, minutes, Date.now());
    const { binRates, baselineRatesPerBin, baselineTargetUsedPerBin, yMax } = data;

    // Faint horizontal gridlines at 0, half, full.
    ctx.strokeStyle = hexToRgba('#64748b', 0.2);
    ctx.lineWidth = 1;
    for (const tick of [0, yMax / 2, yMax]) {
        const y = pad.t + ph - (tick / yMax) * ph;
        ctx.beginPath();
        ctx.moveTo(pad.l, y);
        ctx.lineTo(pad.l + pw, y);
        ctx.stroke();
    }

    // Bars: spots/min per bin.
    const barWidth = pw / ACTIVITY_BINS;
    ctx.fillStyle = hexToRgba('#3b82f6', 0.6);
    binRates.forEach((rate, i) => {
        if (!Number.isFinite(rate) || rate <= 0) return;
        const bh = (rate / yMax) * ph;
        const x = pad.l + i * barWidth + 0.7;
        const y = pad.t + ph - bh;
        ctx.fillRect(x, y, Math.max(1, barWidth - 1.4), bh);
    });

    // Stepped baseline. Walk runs of contiguous bins that share the same slot
    // (and thus the same baseline rate + per-bin used flag), draw each run as
    // one horizontal segment; vertical connectors only at slot boundaries.
    const renderBaselineSegment = (startIdx, endIdx) => {
        const rate = baselineRatesPerBin[startIdx];
        if (!(rate > 0)) return null;
        const used = baselineTargetUsedPerBin[startIdx];
        const x0 = pad.l + startIdx * barWidth;
        const x1 = pad.l + (endIdx + 1) * barWidth;
        const yRaw = pad.t + ph - (rate / yMax) * ph;
        const y = Math.max(pad.t + 1, Math.min(pad.t + ph - 1, yRaw));
        const color = used ? '#ef4444' : '#94a3b8';
        ctx.strokeStyle = hexToRgba(color, used ? 0.95 : 0.85);
        dashedLine(ctx, x0, y, x1, y, [4, 3], 1.2);
        return { startIdx, endIdx, y, color, used };
    };

    let runStart = 0;
    let lastSegment = null;
    for (let i = 1; i <= ACTIVITY_BINS; i++) {
        const slotChange = i === ACTIVITY_BINS
            || baselineRatesPerBin[i] !== baselineRatesPerBin[i - 1]
            || baselineTargetUsedPerBin[i] !== baselineTargetUsedPerBin[i - 1];
        if (!slotChange) continue;
        const seg = renderBaselineSegment(runStart, i - 1);
        // Vertical connector between adjacent segments at a slot boundary.
        if (lastSegment && seg) {
            const xJoin = pad.l + i * barWidth;
            ctx.strokeStyle = hexToRgba('#94a3b8', 0.55);
            dashedLine(ctx, xJoin, lastSegment.y, xJoin, seg.y, [2, 2], 1);
        }
        if (seg) lastSegment = seg;
        runStart = i;
    }

    // Single 'baseline' label, placed near the rightmost segment's y so it
    // doesn't drift when there are slot steps.
    if (lastSegment) {
        ctx.fillStyle = hexToRgba(lastSegment.color, 0.95);
        ctx.font = '10px sans-serif';
        ctx.textAlign = 'right';
        ctx.fillText(
            lastSegment.used ? 'baseline' : 'baseline·global',
            pad.l + pw - 4,
            Math.max(pad.t + 10, lastSegment.y - 3)
        );
        ctx.textAlign = 'left';
    }

    // Y-axis labels (spots/min).
    ctx.fillStyle = hexToRgba('#334155', 0.95);
    ctx.font = '10px sans-serif';
    ctx.textAlign = 'right';
    ctx.fillText(formatRate(yMax), pad.l - 4, pad.t + 8);
    ctx.fillText(formatRate(yMax / 2), pad.l - 4, pad.t + (ph / 2) + 3);
    ctx.fillText('0', pad.l - 4, pad.t + ph + 3);
    ctx.textAlign = 'left';
    ctx.fillText(`last ${minutes}m`, pad.l, pad.t + ph + 12);
    ctx.textAlign = 'right';
    ctx.fillText('spots/min', pad.l + pw, pad.t + ph + 12);
    ctx.textAlign = 'left';
}

function drawChartFrame(ctx, pad, pw, ph) {
    ctx.strokeStyle = hexToRgba('#64748b', 0.45);
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad.l, pad.t + ph);
    ctx.lineTo(pad.l + pw, pad.t + ph);
    ctx.lineTo(pad.l + pw, pad.t);
    ctx.stroke();
}

function drawNoData(ctx, w, h, text) {
    ctx.fillStyle = hexToRgba('#64748b', 0.9);
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, w / 2, h / 2);
    ctx.textAlign = 'left';
    ctx.textBaseline = 'alphabetic';
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

function buildBandRecommendation(band, points, bandMetrics, sampleContext = {}) {
    const score = Number(bandMetrics?.score || 0);
    const confidence = confidence01(bandMetrics?.confidence);
    const trend = String(bandMetrics?.trend || '').toLowerCase();
    const count = points.length;
    const totalReportsAllBands = Math.max(0, Number(sampleContext.totalReportsAllBands) || 0);
    const maxReportsSingleBand = Math.max(0, Number(sampleContext.maxReportsSingleBand) || 0);

    if (count < 2) {
        return 'low sample';
    }

    let tier = 0; // 0=weak, 1=moderate, 2=strong
    if (score >= 70 && confidence >= 0.55) {
        tier = 2;
    } else if (score >= 50 || (trend === 'improving' && confidence >= 0.35)) {
        tier = 1;
    }

    const reportShare = totalReportsAllBands > 0 ? count / totalReportsAllBands : 0;
    const relativeToLeader = maxReportsSingleBand > 0 ? count / maxReportsSingleBand : 0;

    // Evidence-based downgrades: require enough volume and relative presence.
    if (count < 8) {
        tier = Math.max(0, tier - 1);
    }
    if (count < 4) {
        tier = Math.max(0, tier - 1);
    }
    if (maxReportsSingleBand >= 40 && relativeToLeader < 0.15) {
        tier = Math.max(0, tier - 1);
    }
    if (totalReportsAllBands >= 120 && reportShare < 0.05) {
        tier = Math.max(0, tier - 1);
    }

    const label = tier >= 2 ? 'strong' : tier === 1 ? 'moderate' : 'weak';
    return label;
}

// The backend reports confidence on a 0-99 scale, but the decision/tier
// thresholds and the summary display below expect a 0-1 fraction. Normalise at
// the read sites so a ~98.7 value doesn't clamp to a permanent "100%".
function confidence01(raw) {
    const v = Number(raw);
    if (!Number.isFinite(v)) return 0;
    return Math.max(0, Math.min(1, v / 100));
}

function buildGlobalDecision(score, confidence, recommendedCount) {
    if (score >= 70 && confidence >= 0.55 && recommendedCount > 0) {
        return { label: 'Worth turning radio on', className: 'is-go' };
    }
    if (score >= 50 || confidence >= 0.4) {
        return { label: 'Maybe — monitor a few minutes', className: 'is-watch' };
    }
    return { label: 'Likely low payoff now', className: 'is-wait' };
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

// Right-edge splitter: drag to resize the docked panel's width only. The width
// lives in the --bandlab-w CSS var so the flex column re-bases instantly, and we
// re-fit the map live via the layout-change callback.
function setupWindowResize(windowEl) {
    const handle = document.getElementById('band-lab-window-resize');
    if (!handle) return;

    const MIN_W = 300;
    const maxWidth = () => Math.max(MIN_W, Math.round(window.innerWidth * 0.7));

    let resizing = false;
    let startMouseX = 0;
    let startWidth = 0;
    let rafPending = false;

    const onMove = (e) => {
        if (!resizing) return;
        const next = Math.max(MIN_W, Math.min(maxWidth(), startWidth + (e.clientX - startMouseX)));
        windowEl.style.setProperty('--bandlab-w', `${Math.round(next)}px`);
        if (!rafPending) {
            rafPending = true;
            requestAnimationFrame(() => {
                rafPending = false;
                runtime.onLayoutChange?.();
            });
        }
    };

    const stopResize = () => {
        if (!resizing) return;
        resizing = false;
        document.body.style.cursor = '';
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', stopResize);
        persistWindowWidth(windowEl);
        runtime.onLayoutChange?.();
    };

    handle.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        resizing = true;
        startMouseX = e.clientX;
        startWidth = windowEl.offsetWidth;
        document.body.style.cursor = 'col-resize';
        document.addEventListener('mousemove', onMove);
        document.addEventListener('mouseup', stopResize);
        e.preventDefault();
        e.stopPropagation();
    });
}

function persistWindowWidth(windowEl) {
    localStorage.setItem(WINDOW_SIZE_KEY, JSON.stringify({ width: windowEl.offsetWidth }));
}

function restoreWindowWidth(windowEl) {
    const raw = localStorage.getItem(WINDOW_SIZE_KEY);
    if (!raw) return;
    try {
        const parsed = JSON.parse(raw);
        const width = Number(parsed?.width);
        if (Number.isFinite(width) && width >= 300) {
            windowEl.style.setProperty('--bandlab-w', `${Math.round(width)}px`);
        }
    } catch {
        // ignore invalid persisted values
    }
}

function escapeHtml(value) {
    return String(value ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

async function ensureDxConditions(target, minutes, surroundings) {
    const key = `${target}|${minutes}|${surroundings ? 1 : 0}`;
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
    runtime.dxInFlightKey = key;
    runtime.dxInFlight = (async () => {
        try {
            const params = new URLSearchParams();
            params.set('target', target);
            params.set('minutes', String(minutes));
            if (surroundings) params.set('surroundings', 'true');

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
                console.warn('Band Lab dx_conditions fetch failed:', err);
            }
            return runtime.dxCache;
        } finally {
            if (runtime.dxAbortController === controller) {
                runtime.dxAbortController = null;
            }
            runtime.dxInFlight = null;
            runtime.dxInFlightKey = '';
        }
    })();

    return runtime.dxInFlight;
}
