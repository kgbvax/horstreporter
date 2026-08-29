import { state } from './state.js';
import { WSPR_REGIONS, bandColors } from './utils.js';
import { makeDraggable } from './panel-drag.js';

// wspr-matrix.js — band × region matrix panel showing which bands have WSPR
// paths open to which world regions, with SSB/CW flags, rising slope, and
// atypical surge detection. Polls /api/prop_intel for the backend nowcast.

const PANEL_ID = 'wspr-matrix-window';
const TOGGLE_ID = 'wspr-matrix-toggle';
const BODY_ID = 'wspr-matrix-body';
const ENABLE_KEY = 'wsprMatrixEnabled';
const FROM_HERE_KEY = 'wsprMatrixFromHere';
const UPDATE_THROTTLE_MS = 300;
const POLL_INTERVAL_MS = 45000;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

// Cell heat ramp: deep teal (low activity) → brighter teal (high activity).
// Both endpoints hold white text at WCAG AA (5.1:1 at the bright end) — the
// old low-alpha rgba() washed out against light panel backgrounds.
const CELL_COLOR_LOW = [10, 61, 71];    // #0a3d47
const CELL_COLOR_HIGH = [17, 121, 138];  // #11798a

// Map a 0..1 activity intensity to a solid background color. sqrt() spreads
// the low end so 1-2 spots don't collapse onto the darkest shade.
function cellColor(intensity) {
    const t = Math.sqrt(Math.max(0, Math.min(1, intensity)));
    const rgb = CELL_COLOR_LOW.map((c, i) => Math.round(c + (CELL_COLOR_HIGH[i] - c) * t));
    return `rgb(${rgb.join(', ')})`;
}

// Mode badge shows only the top mode: SSB beats CW (if SSB is open, phone
// wins the band, so the CW badge is dropped for glanceability). Full detail
// stays in the cell tooltip.
function topModeBadges(cell) {
    if (cell.ssb_open) return '<span class="wspr-badge wspr-badge-ssb">S</span>';
    if (cell.cw_open) return '<span class="wspr-badge wspr-badge-cw">C</span>';
    return '';
}

const runtime = {
    enabled: false,
    fromHere: false,
    lastUpdateAt: 0,
    onLayoutChange: null,
    lastMatrixKey: '',
    pollTimer: null,
    cachedResponse: null,
};

export function initWsprMatrix({ onLayoutChange } = {}) {
    runtime.onLayoutChange = onLayoutChange || null;
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel || !toggle) return;

    const stored = localStorage.getItem(ENABLE_KEY);
    if (stored === 'true') {
        setWsprMatrixVisible(true);
    }

    runtime.fromHere = localStorage.getItem(FROM_HERE_KEY) === 'true';

    toggle.addEventListener('click', () => {
        setWsprMatrixVisible(!runtime.enabled);
    });

    // From-here toggle button.
    const fromHereBtn = panel.querySelector('.wspr-matrix-from-here-toggle');
    if (fromHereBtn) {
        fromHereBtn.addEventListener('click', () => {
            runtime.fromHere = !runtime.fromHere;
            localStorage.setItem(FROM_HERE_KEY, runtime.fromHere ? 'true' : 'false');
            fromHereBtn.classList.toggle('is-active', runtime.fromHere);
            runtime.lastMatrixKey = '';
            updateWsprMatrix();
        });
        fromHereBtn.classList.toggle('is-active', runtime.fromHere);
    }

    makeDraggable(panel, panel.querySelector('.wspr-matrix-window-header'), 'wsprMatrixPos');
}

function setWsprMatrixVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    if (toggle) toggle.classList.toggle('is-active', visible);
    localStorage.setItem(ENABLE_KEY, visible ? 'true' : 'false');
    if (runtime.onLayoutChange) runtime.onLayoutChange();
    if (visible) {
        updateWsprMatrix();
        startPolling();
    } else {
        stopPolling();
    }
}

function startPolling() {
    if (runtime.pollTimer) clearInterval(runtime.pollTimer);
    runtime.pollTimer = setInterval(updateWsprMatrix, POLL_INTERVAL_MS);
}

function stopPolling() {
    if (runtime.pollTimer) {
        clearInterval(runtime.pollTimer);
        runtime.pollTimer = null;
    }
}

export async function updateWsprMatrix() {
    if (!runtime.enabled) return;
    const now = Date.now();
    if (now - runtime.lastUpdateAt < UPDATE_THROTTLE_MS) return;
    runtime.lastUpdateAt = now;

    const body = document.getElementById(BODY_ID);
    if (!body) return;

    // Fetch the WSPR propagation-intelligence payload from /api/prop_intel.
    const qth = state.qth || '';
    if (!qth) {
        body.innerHTML = '<div class="text-muted small">Set a QTH to see the WSPR matrix.</div>';
        return;
    }

    const fromHereParam = runtime.fromHere ? '&from_here=true' : '';
    const url = `/api/prop_intel?qth=${encodeURIComponent(qth)}&minutes=15&surroundings=true${fromHereParam}`;

    try {
        const resp = await fetch(url);
        if (!resp.ok) {
            body.innerHTML = '<div class="text-muted small">WSPR matrix unavailable.</div>';
            return;
        }
        const data = await resp.json();
        runtime.cachedResponse = data;
        renderMatrix(body, data);
    } catch (e) {
        body.innerHTML = '<div class="text-muted small">WSPR matrix fetch failed.</div>';
    }
}

function renderMatrix(body, data) {
    const cells = data.cells || [];
    if (cells.length === 0) {
        runtime.lastMatrixKey = '';
        body.innerHTML = '<div class="text-muted small">No WSPR paths open in the current window.</div>';
        return;
    }

    // Build band → region → cell map.
    const matrix = new Map();
    const activeBands = [];
    for (const cell of cells) {
        if (!matrix.has(cell.band)) {
            matrix.set(cell.band, new Map());
            activeBands.push(cell.band);
        }
        matrix.get(cell.band).set(cell.region, cell);
    }
    activeBands.sort((a, b) => BAND_ORDER.indexOf(a) - BAND_ORDER.indexOf(b));

    // Fingerprint for skip-rebuild.
    const matrixKey = activeBands
        .map((b) => `${b}:${Array.from(matrix.get(b).entries()).sort().map(([r, c]) => `${r}${c.spot_count}${c.ssb_open?'S':''}${c.cw_open?'C':''}${c.rising?'R':''}${c.atypical?c.atypical.flavor:''}`).join('')}`)
        .join('|');
    if (matrixKey === runtime.lastMatrixKey) return;
    runtime.lastMatrixKey = matrixKey;

    const maxCount = Math.max(...cells.map((c) => c.spot_count || 0), 1);

    let html = '<table class="wspr-matrix-table"><thead><tr><th></th>';
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
            if (!cell || cell.spot_count === 0) {
                html += '<td class="wspr-matrix-cell-empty"></td>';
            } else {
                const intensity = Math.min(1, cell.spot_count / maxCount);
                let badges = topModeBadges(cell);
                if (cell.rising) badges += '<span class="wspr-badge wspr-badge-rising">&uarr;</span>';
                let atypicalBadge = '';
                if (cell.atypical) {
                    const flavorClass = `wspr-badge-atypical-${cell.atypical.flavor}`;
                    const flavorChar = cell.atypical.flavor === 'atypical-both' ? '!!' :
                        cell.atypical.flavor === 'atypical-wspr-silent-ft8' ? '!w' : '!';
                    atypicalBadge = `<span class="wspr-badge wspr-badge-atypical ${flavorClass}" title="Atypical z=${cell.atypical.z_score} (${cell.atypical.flavor})">${flavorChar}</span>`;
                }
                const fromHereMark = cell.from_here ? '<span class="wspr-badge wspr-badge-from-here">*</span>' : '';
                const titleParts = [`${band} → ${region}: ${cell.spot_count} spots`];
                if (cell.ssb_open) titleParts.push('SSB open');
                if (cell.cw_open) titleParts.push('CW open');
                if (cell.rising) titleParts.push('rising');
                if (cell.atypical) titleParts.push(`atypical z=${cell.atypical.z_score} (${cell.atypical.flavor})`);
                if (cell.from_here) titleParts.push('from-here');
                html += `<td class="wspr-matrix-cell" style="background: ${cellColor(intensity)}" title="${titleParts.join(', ')}">${cell.spot_count}${badges}${atypicalBadge}${fromHereMark}</td>`;
            }
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    html += '<div class="wspr-matrix-legend small text-muted"><span class="wspr-badge wspr-badge-ssb">S</span>=SSB <span class="wspr-badge wspr-badge-cw">C</span>=CW (top mode) <span class="wspr-badge wspr-badge-rising">&uarr;</span>=rising <span class="wspr-badge wspr-badge-atypical">!</span>=atypical *=from-here</div>';
    body.innerHTML = html;
}

// Test hooks (mirrors the prop-matrix.js __test convention).
export const __test = {
    cellColor,
    topModeBadges,
};