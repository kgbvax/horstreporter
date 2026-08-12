import { state } from './state.js';
import { regionForLocatorCached, WSPR_REGIONS, bandColors } from './utils.js';
import { makeDraggable } from './panel-drag.js';

// wspr-matrix.js — band × region matrix panel showing which bands have WSPR
// paths open to which world regions right now. Aggregates state.liveSpots
// client-side (≤60-min SSE window). No backend endpoint needed.

const PANEL_ID = 'wspr-matrix-window';
const TOGGLE_ID = 'wspr-matrix-toggle';
const BODY_ID = 'wspr-matrix-body';
const ENABLE_KEY = 'wsprMatrixEnabled';
const UPDATE_THROTTLE_MS = 300;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

const runtime = {
    enabled: false,
    lastUpdateAt: 0,
    onLayoutChange: null,
    // Fingerprint of the last rendered matrix; the table DOM is only rebuilt
    // when the band × region counts actually change.
    lastMatrixKey: '',
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

    toggle.addEventListener('click', () => {
        setWsprMatrixVisible(!runtime.enabled);
    });

    const closeBtn = panel.querySelector('.wspr-matrix-close');
    if (closeBtn) {
        closeBtn.addEventListener('click', () => setWsprMatrixVisible(false));
    }

    // Make the floating card draggable by its header (position persisted).
    makeDraggable(panel, panel.querySelector('.wspr-matrix-window-header'), 'wsprMatrixPos');
}

function setWsprMatrixVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    if (toggle) toggle.style.display = visible ? 'none' : '';
    localStorage.setItem(ENABLE_KEY, visible ? 'true' : 'false');
    if (runtime.onLayoutChange) runtime.onLayoutChange();
    if (visible) updateWsprMatrix();
}

export function updateWsprMatrix() {
    if (!runtime.enabled) return;
    const now = Date.now();
    if (now - runtime.lastUpdateAt < UPDATE_THROTTLE_MS) return;
    runtime.lastUpdateAt = now;

    const body = document.getElementById(BODY_ID);
    if (!body) return;

    // Aggregate WSPR spots by band × region.
    // Each WSPR path contributes to both the transmitter's region and the
    // receiver's region — a path between EU and NA lights both the EU and NA
    // cells for that band. regionForLocatorCached memoizes per locator.
    const matrix = new Map(); // band -> Map<region, count>
    for (const spot of state.liveSpots) {
        if (String(spot?.sourceType || '').toLowerCase() !== 'wspr') continue;
        const band = spot.band;
        if (!band || !BAND_ORDER.includes(band)) continue;
        if (!matrix.has(band)) matrix.set(band, new Map());
        const bandMap = matrix.get(band);
        const txRegion = regionForLocatorCached(spot.locator);
        const rxRegion = regionForLocatorCached(spot.reporterLocator);
        if (txRegion) bandMap.set(txRegion, (bandMap.get(txRegion) || 0) + 1);
        if (rxRegion && rxRegion !== txRegion) bandMap.set(rxRegion, (bandMap.get(rxRegion) || 0) + 1);
    }

    // Render the grid: rows = bands, columns = 11 regions.
    const activeBands = BAND_ORDER.filter((b) => matrix.has(b));
    if (activeBands.length === 0) {
        runtime.lastMatrixKey = '';
        body.innerHTML = '<div class="text-muted small">No WSPR paths open in the current window.</div>';
        return;
    }

    // Skip the DOM rebuild when the band × region counts are unchanged.
    const matrixKey = activeBands
        .map((b) => `${b}:${Array.from(matrix.get(b).entries()).sort().map(([r, c]) => `${r}${c}`).join('')}`)
        .join('|');
    if (matrixKey === runtime.lastMatrixKey) return;
    runtime.lastMatrixKey = matrixKey;

    const maxCount = Math.max(...Array.from(matrix.values()).flatMap((m) => Array.from(m.values())), 1);

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
            const count = bandMap.get(region) || 0;
            if (count === 0) {
                html += '<td class="wspr-matrix-cell-empty"></td>';
            } else {
                const intensity = Math.min(1, count / maxCount);
                const alpha = 0.2 + intensity * 0.7;
                html += `<td class="wspr-matrix-cell" style="background: rgba(23, 162, 184, ${alpha})" title="${band} → ${region}: ${count} paths">${count}</td>`;
            }
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    body.innerHTML = html;
}