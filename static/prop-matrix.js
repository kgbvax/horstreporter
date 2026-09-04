import { WSPR_REGIONS, bandColors, getEnabledBands } from './utils.js';
import { state } from './state.js';
import { makeDraggable } from './panel-drag.js';

// prop-matrix.js — band × region propagation-intelligence matrix panel.
// Polls the backend `/api/prop_intel` WSPR nowcast and renders per-cell
// activity as background intensity (spots relative to the busiest cell),
// the spot count as a numeric badge, an atypical-surge highlight, and a
// low-confidence border. From-here-only by design: cells are paths with the
// operator's QTH at one end (same stance as wspr-matrix.js).
//
// Patterns mirrored from:
//   - wspr-matrix.js: toggle button + localStorage enable + DOM-rebuild
//     fingerprint + panel container.
//   - band-lab.js: backend polling with AbortController in-flight dedup and
//     a 15s cache TTL; runtime object shape.
//   - hot-band-indicator.js: 30s polling interval with visibility gating.

const PANEL_ID = 'prop-matrix-window';
const TOGGLE_ID = 'prop-matrix-toggle';
const BODY_ID = 'prop-matrix-body';
const ENABLE_KEY = 'propMatrixEnabled';

const POLL_INTERVAL_MS = 30_000;
const CACHE_TTL_MS = 15_000;
const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

// Green scale for cell activity: 0 = transparent, 1 = solid green. The RGB
// tuple is the same green used by the success/positive affordances elsewhere
// in the app.
const OPEN_GREEN_RGB = '40, 167, 69';
const LOW_CONFIDENCE_THRESHOLD = 0.4;

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
    onLayoutChange: null,
};

export function initPropMatrix({ onLayoutChange } = {}) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel || !toggle) return;

    runtime.onLayoutChange = onLayoutChange || null;

    const stored = localStorage.getItem(ENABLE_KEY);

    if (stored === 'true') {
        setPropMatrixVisible(true);
    }

    if (!runtime.initialized) {
        toggle.addEventListener('click', () => {
            setPropMatrixVisible(!runtime.enabled);
        });

        // Re-poll when QTH changes (band-lab.js pattern at line 138).
        const qthInput = document.getElementById('qth');
        if (qthInput) {
            qthInput.addEventListener('change', () => {
                if (!runtime.enabled) return;
                const next = currentQth();
                if (next !== runtime.lastQth) {
                    // New QTH invalidates the cache so the next tick re-fetches.
                    runtime.cache = null;
                    runtime.cacheKey = '';
                    runtime.lastFetchedAt = 0;
                    pollPropMatrix(true);
                }
            });
        }

        // Make the floating card draggable by its header (position persisted).
        makeDraggable(panel, panel.querySelector('.prop-matrix-window-header'), 'propMatrixPos');

        runtime.initialized = true;
    }
}

export function setPropMatrixVisible(visible) {
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

export function updatePropMatrix() {
    if (!runtime.enabled) return;
    pollPropMatrix(false);
}

function startPolling() {
    stopPolling();
    pollPropMatrix(true);
    runtime.pollTimer = setInterval(() => {
        if (document.visibilityState !== 'visible') return;
        pollPropMatrix(false);
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

async function pollPropMatrix(force) {
    const qth = currentQth();
    if (!qth) {
        const body = document.getElementById(BODY_ID);
        if (body) body.innerHTML = '<div class="text-muted small">Enter a QTH to see propagation intelligence.</div>';
        return;
    }

    const now = Date.now();
    const key = `${qth}|15|fh`;
    if (!force && runtime.cache && runtime.cacheKey === key && (now - runtime.lastFetchedAt) < CACHE_TTL_MS) {
        renderPropMatrix();
        return;
    }

    if (runtime.abortController) runtime.abortController.abort();
    const controller = new AbortController();
    runtime.abortController = controller;

    // From-here-only by design (never an unfiltered global window), anchored
    // at the QTH with its surroundings block — same request shape the WSPR
    // matrix panel and the mobile app use.
    const params = new URLSearchParams();
    params.set('qth', qth);
    params.set('minutes', '15');
    params.set('surroundings', 'true');
    params.set('from_here', 'true');

    try {
        const resp = await fetch(`/api/prop_intel?${params.toString()}`, { signal: controller.signal });
        if (!resp.ok) throw new Error(`prop_intel HTTP ${resp.status}`);
        const payload = await resp.json();
        runtime.cache = payload;
        runtime.cacheKey = key;
        runtime.lastFetchedAt = Date.now();
        runtime.lastQth = qth;
        renderPropMatrix();
    } catch (err) {
        if (err?.name === 'AbortError') return;
        console.warn('prop_intel fetch failed:', err);
        const body = document.getElementById(BODY_ID);
        if (body && runtime.cache == null) {
            body.innerHTML = '<div class="text-muted small">Propagation intelligence unavailable.</div>';
        }
    } finally {
        if (runtime.abortController === controller) {
            runtime.abortController = null;
        }
    }
}

function renderPropMatrix() {
    const body = document.getElementById(BODY_ID);
    if (!body) return;
    const resp = runtime.cache;
    if (!resp) {
        body.innerHTML = '<div class="text-muted small">Collecting propagation intelligence…</div>';
        return;
    }

    const cells = Array.isArray(resp.cells) ? resp.cells : [];
    if (cells.length === 0) {
        runtime.lastRenderKey = '';
        body.innerHTML = '<div class="text-muted small">No from-here WSPR paths in the current window.</div>';
        return;
    }

    const enabledBands = getEnabledBands();
    const bandList = BAND_ORDER.filter((b) => enabledBands.has(b));
    const regions = Array.isArray(resp.regions) && resp.regions.length > 0
        ? resp.regions
        : WSPR_REGIONS;

    const cellsByBandRegion = indexCells(resp);

    // Rebuild fingerprint: skip DOM rebuild when nothing changed.
    const renderKey = `${bandList.join(',')}|${regions.join(',')}|${fingerprintCells(cellsByBandRegion)}`;
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    if (bandList.length === 0) {
        body.innerHTML = '<div class="text-muted small">No bands enabled.</div>';
        return;
    }

    // Intensity is relative to the busiest visible cell, like the WSPR
    // matrix (there spot_count/maxCount drives the teal ramp).
    let maxCount = 1;
    for (const c of cells) {
        const n = Number(c?.spot_count ?? 0);
        if (n > maxCount) maxCount = n;
    }

    let html = '<table class="prop-matrix-table"><thead><tr><th></th>';
    for (const region of regions) {
        html += `<th title="${escapeHtml(region)}">${escapeHtml(region)}</th>`;
    }
    html += '</tr></thead><tbody>';

    for (const band of bandList) {
        const color = bandColors[band] || bandColors.all || '#555';
        html += `<tr><td class="prop-matrix-band" style="border-left: 3px solid ${color}">${escapeHtml(band)}</td>`;
        for (const region of regions) {
            const cell = cellsByBandRegion.get(cellKey(band, region));
            html += renderCell(band, region, cell, maxCount);
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    body.innerHTML = html;

    attachCellClickHandler(body, bandList, regions, cellsByBandRegion);
}

function renderCell(band, region, cell, maxCount = 1) {
    if (!cell) {
        return `<td class="prop-matrix-cell-empty" data-band="${escapeHtml(band)}" data-region="${escapeHtml(region)}" role="button" tabindex="0"></td>`;
    }
    const spotCount = Math.max(0, Number(cell.spot_count ?? 0));
    // sqrt() spreads the low end so 1-2-spot cells don't collapse onto the
    // sparse shade (same ramp trick as wspr-matrix.js).
    const intensity = clamp01(spotCount / Math.max(1, maxCount));
    const alpha = 0.15 + Math.sqrt(intensity) * 0.85;
    const background = `rgba(${OPEN_GREEN_RGB}, ${alpha})`;

    // The WSPR engine's "surge" is the atypical z-score flag; its confidence
    // is the reliability of that call. No atypical data → no confidence call.
    const atypical = cell.atypical || null;
    const confidence = atypical ? clamp01(Number(atypical.confidence ?? 0)) : 1;

    const classes = ['prop-matrix-cell'];
    if (atypical) classes.push('prop-matrix-surge');
    if (atypical && confidence < LOW_CONFIDENCE_THRESHOLD) classes.push('prop-matrix-low-confidence');

    const title = buildCellTitle(band, region, cell, atypical);
    const badge = spotCount > 0 ? `<span class="prop-matrix-badge">${spotCount}</span>` : '';
    const surgeMark = atypical
        ? `<span class="prop-matrix-surge-icon" title="atypical z=${escapeHtml(String(atypical.z_score))} (${escapeHtml(atypical.flavor || '')})">!</span>`
        : '';

    return `<td class="${classes.join(' ')}" style="background: ${background};" title="${escapeHtml(title)}" data-band="${escapeHtml(band)}" data-region="${escapeHtml(region)}" role="button" tabindex="0">${surgeMark}${badge}</td>`;
}

function buildCellTitle(band, region, cell, atypical) {
    const lines = [`${band} → ${region}: ${Number(cell.spot_count ?? 0)} spots`];
    const modes = [];
    if (cell.ssb_open) modes.push('SSB open');
    if (cell.cw_open) modes.push('CW open');
    if (modes.length) lines.push(modes.join(', '));
    if (cell.rising) lines.push('rising');
    if (atypical) lines.push(`atypical z=${atypical.z_score} (${atypical.flavor}), confidence ${Math.round(clamp01(Number(atypical.confidence ?? 0)) * 100)}%`);
    if (Array.isArray(cell.sources) && cell.sources.length) lines.push(`sources: ${cell.sources.join(', ')}`);
    return lines.join('\n');
}

function indexCells(resp) {
    const map = new Map();
    const cells = Array.isArray(resp.cells) ? resp.cells : [];
    for (const c of cells) {
        if (!c || !c.band || !c.region) continue;
        map.set(cellKey(c.band, c.region), c);
    }
    return map;
}

function cellKey(band, region) {
    return `${band}|${region}`;
}

function fingerprintCells(map) {
    const out = [];
    for (const [k, c] of map.entries()) {
        out.push(`${k}:${Number(c?.spot_count ?? 0)}:${c?.ssb_open ? 1 : 0}${c?.cw_open ? 1 : 0}${c?.rising ? 1 : 0}:${c?.atypical ? c.atypical.flavor : ''}`);
    }
    return out.sort().join('|');
}

function clamp01(v) {
    if (!Number.isFinite(v)) return 0;
    return Math.max(0, Math.min(1, v));
}

function escapeHtml(s) {
    return String(s ?? '').replace(/[&<>"']/g, (ch) => {
        switch (ch) {
            case '&': return '&amp;';
            case '<': return '&lt;';
            case '>': return '&gt;';
            case '"': return '&quot;';
            case "'": return '&#39;';
            default: return ch;
        }
    });
}

// --- Drill-down (U4) ------------------------------------------------------
// Clicking a matrix cell filters the grid-square plot to that band × region.
// The cell's row index → band, column index → region. Both are stashed in the
// shared state singleton and a re-render is scheduled via the global hook
// exported by app.js (window.__horstScheduleRender).
function attachCellClickHandler(body, bandList, regions, cellsByBandRegion) {
    body.querySelectorAll('.prop-matrix-cell, .prop-matrix-cell-empty').forEach((td) => {
        td.addEventListener('click', () => {
            const band = td.getAttribute('data-band');
            const region = td.getAttribute('data-region');
            if (!band || !region) return;
            // Toggle: clicking the already-active cell clears the drill-down.
            if (state.drillDownBand === band && state.drillDownRegion === region) {
                state.drillDownBand = '';
                state.drillDownRegion = '';
            } else {
                state.drillDownBand = band;
                state.drillDownRegion = region;
            }
            updateDrillDownButton();
            if (typeof window.__horstScheduleRender === 'function') {
                window.__horstScheduleRender();
            }
        });
    });
}

// Show/hide the "clear filter" overlay button based on drill-down state.
export function updateDrillDownButton() {
    const btn = document.getElementById('drill-down-clear');
    if (!btn) return;
    const active = state.drillDownBand !== '' || state.drillDownRegion !== '';
    btn.style.display = active ? '' : 'none';
    if (active) {
        btn.textContent = `Clear filter: ${state.drillDownBand} × ${state.drillDownRegion}`;
        btn.title = `Grid-square plot filtered to ${state.drillDownBand} × ${state.drillDownRegion}. Click to clear.`;
    }
}

// Clear the drill-down filter (called from the overlay button).
export function clearDrillDown() {
    state.drillDownBand = '';
    state.drillDownRegion = '';
    updateDrillDownButton();
    if (typeof window.__horstScheduleRender === 'function') {
        window.__horstScheduleRender();
    }
}

// --- Test hooks -----------------------------------------------------------
// Exported for vitest. These are not part of the public UI API.
export const __test = {
    runtime,
    reset() {
        stopPolling();
        runtime.enabled = false;
        runtime.initialized = false;
        runtime.cache = null;
        runtime.cacheKey = '';
        runtime.lastFetchedAt = 0;
        runtime.lastQth = '';
        runtime.lastRenderKey = '';
        runtime.onLayoutChange = null;
    },
    renderPropMatrix,
    renderCell,
    setPropMatrixVisible,
    attachCellClickHandler,
    updateDrillDownButton,
    clearDrillDown,
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
};