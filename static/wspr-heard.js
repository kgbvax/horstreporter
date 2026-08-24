import { state } from './state.js';
import {
    bandColors,
    WSPR_REGIONS,
    wsprSnrToMode,
    wsprHeardConfidence,
    isMyWsprSpot,
    deviationColor,
} from './utils.js';
import { makeDraggable } from './panel-drag.js';

// wspr-heard.js — the WSPR reverse-beacon panel: "who heard me". The operator's
// own WSPR transmitter runs continuously and other stations report its signal;
// those reception reports are the most direct measure of what is reachable from
// the operator's location. This panel shows:
//   - a list of hearing stations (band, grid, distance, median SNR, translated
//     mode, report count/confidence) from the 24 h backend aggregate, and
//   - a band × region matrix scoped to the operator's own transmissions, with a
//     "raw / deviation" toggle (deviation = above/below the time-of-day-aware
//     historical average).
//
// Data sources:
//   - Live (Tier 1): state.liveSpots filtered by isMyWsprSpot, for the live
//     "hearing me now" count and the map layer (renderers.js).
//   - Backend (Tier 2): GET /api/wspr_heard (24 h aggregate + region_baseline),
//     polled on a 60 s interval with an AbortController in-flight dedup.

const PANEL_ID = 'wspr-heard-window';
const TOGGLE_ID = 'wspr-heard-toggle';
const BODY_ID = 'wspr-heard-body';
const ENABLE_KEY = 'wsprHeardEnabled';
const MATRIX_MODE_KEY = 'wsprHeardMatrixMode';

const POLL_INTERVAL_MS = 60_000;
const CACHE_TTL_MS = 30_000;
const UPDATE_THROTTLE_MS = 300;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

// Power offset for mode translation: 10·log10(P_qso / P_wspr). Defaults assume
// WSPR at 5 W and the QSO mode at 100 W (≈ +13 dB). The received SNR scales
// with TX power, so a station heard at −18 dB on 5 W WSPR would be heard at
// −5 dB on 100 W — enough to flip the viable mode.
const POWER_OFFSET_DB = 13;

const MODE_LABELS = { ssb: 'SSB', cw: 'CW', ft8: 'FT8', 'wspr-only': 'WSPR' };

const runtime = {
    enabled: false,
    initialized: false,
    pollTimer: null,
    abortController: null,
    cache: null,
    cacheKey: '',
    lastFetchedAt: 0,
    lastQth: '',
    lastRenderKey: '',
    lastUpdateAt: 0,
    liveCount: 0,
    matrixMode: 'raw',
    onLayoutChange: null,
};

export function initWsprHeard({ onLayoutChange } = {}) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel || !toggle) return;

    runtime.onLayoutChange = onLayoutChange || null;

    const stored = localStorage.getItem(ENABLE_KEY);
    if (stored === 'true') {
        setWsprHeardVisible(true);
    }

    if (!runtime.initialized) {
        toggle.addEventListener('click', () => {
            setWsprHeardVisible(!runtime.enabled);
        });

        // Re-poll when QTH changes (mirrors prop-matrix.js).
        const qthInput = document.getElementById('qth');
        if (qthInput) {
            qthInput.addEventListener('change', () => {
                if (!runtime.enabled) return;
                const next = currentQth();
                if (next !== runtime.lastQth) {
                    runtime.cache = null;
                    runtime.cacheKey = '';
                    runtime.lastFetchedAt = 0;
                    pollWsprHeard(true);
                }
            });
        }

        // Raw / deviation matrix toggle.
        const modeBtn = document.getElementById('wspr-heard-matrix-mode');
        if (modeBtn) {
            const saved = localStorage.getItem(MATRIX_MODE_KEY);
            if (saved === 'deviation') runtime.matrixMode = 'deviation';
            modeBtn.addEventListener('click', () => {
                runtime.matrixMode = runtime.matrixMode === 'raw' ? 'deviation' : 'raw';
                localStorage.setItem(MATRIX_MODE_KEY, runtime.matrixMode);
                renderWsprHeard();
            });
        }

        makeDraggable(panel, panel.querySelector('.wspr-heard-window-header'), 'wsprHeardPos');

        runtime.initialized = true;
    }
}

export function setWsprHeardVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    if (toggle) toggle.classList.toggle('is-active', visible);
    localStorage.setItem(ENABLE_KEY, visible ? 'true' : 'false');
    if (runtime.onLayoutChange) runtime.onLayoutChange();
    if (visible) {
        startPolling();
    } else {
        stopPolling();
    }
}

// updateWsprHeard is called on stream changes (alongside updateWsprMatrix). It
// recomputes the live "hearing me now" count and nudges the backend poll.
export function updateWsprHeard() {
    if (!runtime.enabled) return;
    const now = Date.now();
    if (now - runtime.lastUpdateAt < UPDATE_THROTTLE_MS) return;
    runtime.lastUpdateAt = now;

    const qth = currentQth();
    let liveCount = 0;
    if (qth) {
        for (const spot of state.liveSpots) {
            if (isMyWsprSpot(spot, qth)) liveCount++;
        }
    }
    if (liveCount !== runtime.liveCount) {
        runtime.liveCount = liveCount;
        renderWsprHeard();
    }

    pollWsprHeard(false);
}

function startPolling() {
    stopPolling();
    pollWsprHeard(true);
    runtime.pollTimer = setInterval(() => {
        if (document.visibilityState !== 'visible') return;
        pollWsprHeard(false);
    }, POLL_INTERVAL_MS);
}

function stopPolling() {
    if (runtime.pollTimer) {
        clearInterval(runtime.pollTimer);
        runtime.pollTimer = null;
    }
    if (runtime.abortController) {
        runtime.abortController.abort();
        runtime.abortController = null;
    }
}

function currentQth() {
    return String(document.getElementById('qth')?.value || '').trim().toUpperCase();
}

async function pollWsprHeard(force) {
    const qth = currentQth();
    if (!qth) {
        const body = document.getElementById(BODY_ID);
        if (body) body.innerHTML = '<div class="text-muted small">Enter a QTH to see who heard you.</div>';
        return;
    }

    const now = Date.now();
    const key = `${qth}|24`;
    if (!force && runtime.cache && runtime.cacheKey === key && (now - runtime.lastFetchedAt) < CACHE_TTL_MS) {
        renderWsprHeard();
        return;
    }

    if (runtime.abortController) runtime.abortController.abort();
    const controller = new AbortController();
    runtime.abortController = controller;

    const params = new URLSearchParams();
    params.set('qth', qth);
    params.set('hours', '24');

    try {
        const resp = await fetch(`/api/wspr_heard?${params.toString()}`, { signal: controller.signal });
        if (!resp.ok) throw new Error(`wspr_heard HTTP ${resp.status}`);
        const payload = await resp.json();
        runtime.cache = payload;
        runtime.cacheKey = key;
        runtime.lastFetchedAt = Date.now();
        runtime.lastQth = qth;
        renderWsprHeard();
    } catch (err) {
        if (err?.name === 'AbortError') return;
        console.warn('wspr_heard fetch failed:', err);
        const body = document.getElementById(BODY_ID);
        if (body && runtime.cache == null) {
            body.innerHTML = '<div class="text-muted small">Who-heard-me data unavailable.</div>';
        }
    } finally {
        if (runtime.abortController === controller) {
            runtime.abortController = null;
        }
    }
}

function renderWsprHeard() {
    const body = document.getElementById(BODY_ID);
    if (!body) return;
    const resp = runtime.cache;
    if (!resp) {
        body.innerHTML = '<div class="text-muted small">Collecting who-heard-me data…</div>';
        return;
    }

    const reports = Array.isArray(resp.reports) ? resp.reports : [];
    const baseline = Array.isArray(resp.region_baseline) ? resp.region_baseline : [];

    // Skip the DOM rebuild when nothing changed (reports + baseline + mode +
    // live count all feed the fingerprint).
    const renderKey = JSON.stringify({
        reports: reports.map((r) => [r.band, r.hearing_callsign, r.count, r.snr_median]),
        baseline: baseline.map((b) => [b.band, b.region, b.current_count, b.ratio]),
        mode: runtime.matrixMode,
        live: runtime.liveCount,
    });
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    let html = '';

    // Live summary line.
    html += `<div class="wspr-heard-summary">`;
    html += `<span>${reports.length} hearing station${reports.length === 1 ? '' : 's'} (24 h)</span>`;
    if (runtime.liveCount > 0) {
        html += `<span class="wspr-heard-live">${runtime.liveCount} hearing me now</span>`;
    }
    html += `</div>`;

    // Reports list.
    if (reports.length === 0) {
        html += '<div class="text-muted small">No one has reported your WSPR signal in the last 24 h.</div>';
    } else {
        html += '<table class="wspr-heard-table"><thead><tr>';
        html += '<th>Band</th><th>Station</th><th>Dist</th><th>SNR</th><th>Mode</th><th>N</th>';
        html += '</tr></thead><tbody>';
        for (const r of reports) {
            const color = bandColors[r.band] || bandColors.all || '#555';
            const mode = wsprSnrToMode(r.snr_median, POWER_OFFSET_DB);
            const conf = wsprHeardConfidence(r.count);
            const dist = Number(r.distance_km) > 0 ? `${Math.round(r.distance_km)} km` : '—';
            const grid = r.hearing_locator ? ` <span class="wspr-heard-grid">${r.hearing_locator}</span>` : '';
            html += `<tr>`;
            html += `<td class="wspr-heard-band" style="border-left: 3px solid ${color}">${r.band}</td>`;
            html += `<td>${escapeHtml(r.hearing_callsign)}${grid}</td>`;
            html += `<td>${dist}</td>`;
            html += `<td>${r.snr_median} dB</td>`;
            html += `<td class="wspr-heard-mode wspr-heard-mode-${mode}">${MODE_LABELS[mode] || mode}</td>`;
            html += `<td title="${conf} confidence">${r.count}</td>`;
            html += `</tr>`;
        }
        html += '</tbody></table>';
    }

    // Matrix (band × region), raw or deviation.
    html += renderMatrix(baseline);

    body.innerHTML = html;
}

function renderMatrix(baseline) {
    const deviation = runtime.matrixMode === 'deviation';

    // Build band -> region -> cell map.
    const matrix = new Map();
    for (const b of baseline) {
        if (!BAND_ORDER.includes(b.band)) continue;
        if (!matrix.has(b.band)) matrix.set(b.band, new Map());
        matrix.get(b.band).set(b.region, b);
    }

    const activeBands = BAND_ORDER.filter((b) => matrix.has(b));
    if (activeBands.length === 0) {
        return '<div class="text-muted small">No region data for your transmissions yet.</div>';
    }

    const modeLabel = deviation ? 'deviation' : 'raw';
    let html = `<div class="wspr-heard-matrix-header">`;
    html += `<span>Band × Region</span>`;
    html += `<button type="button" id="wspr-heard-matrix-mode" class="wspr-heard-mode-btn" title="Toggle raw counts vs above/below-average">${modeLabel}</button>`;
    html += `</div>`;

    html += '<table class="wspr-matrix-table"><thead><tr><th></th>';
    for (const region of WSPR_REGIONS) {
        html += `<th title="${region}">${region}</th>`;
    }
    html += '</tr></thead><tbody>';

    for (const band of activeBands) {
        const bandMap = matrix.get(band);
        const color = bandColors[band] || bandColors.all || '#555';
        html += `<tr><td class="wspr-matrix-band" style="border-left: 3px solid ${color}">${band}</td>`;
        for (const region of WSPR_REGIONS) {
            const cell = bandMap.get(region);
            if (!cell) {
                html += '<td class="wspr-matrix-cell-empty"></td>';
                continue;
            }
            if (deviation) {
                html += renderDeviationCell(cell);
            } else {
                html += renderRawCell(cell);
            }
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    return html;
}

function renderRawCell(cell) {
    const count = cell.current_count || 0;
    if (count === 0) return '<td class="wspr-matrix-cell-empty"></td>';
    // Single-hue intensity (mirrors wspr-matrix.js), scoped to my own spots.
    const intensity = Math.min(1, count / 10);
    const alpha = 0.2 + intensity * 0.7;
    return `<td class="wspr-matrix-cell" style="background: rgba(23, 162, 184, ${alpha})" title="${cell.band} → ${cell.region}: ${count} reports">${count}</td>`;
}

function renderDeviationCell(cell) {
    const count = cell.current_count || 0;
    const baseline = Number(cell.baseline_count) || 0;
    if (baseline < 1) {
        // Too little history for a meaningful ratio.
        return `<td class="wspr-matrix-cell wspr-heard-cell-nodata" title="${cell.band} → ${cell.region}: insufficient history">—</td>`;
    }
    const ratio = Number(cell.ratio) || 0;
    const color = deviationColor(ratio);
    const label = ratio >= 1 ? `+${ratio.toFixed(1)}×` : `−${ratio.toFixed(1)}×`;
    return `<td class="wspr-matrix-cell wspr-heard-cell" style="background: ${color}" title="${cell.band} → ${cell.region}: ${count} now vs ${baseline.toFixed(1)} avg (${label})">${count}<span class="wspr-heard-ratio">${label}</span></td>`;
}

function escapeHtml(s) {
    return String(s ?? '').replace(/[&<>"']/g, (c) => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
}
