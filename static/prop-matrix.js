import { WSPR_REGIONS, bandColors, getEnabledBands } from './utils.js';

// prop-matrix.js — band × region propagation-intelligence matrix panel.
// Polls the backend `/api/prop_intel` endpoint (built in U1/U2) and renders
// per-cell P(open) as background intensity, expected count as a numeric
// badge, surge highlight, confidence border, and a nowcast/forecast toggle.
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
const VIEW_TOGGLE_ID = 'prop-matrix-view-toggle';
const ENABLE_KEY = 'propMatrixEnabled';
const VIEW_KEY = 'propMatrixView'; // 'nowcast' | 'forecast'

const POLL_INTERVAL_MS = 30_000;
const CACHE_TTL_MS = 15_000;
const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

// Green scale for P(open): 0 = transparent, 1 = solid green. The RGB tuple is
// the same green used by the success/positive affordances elsewhere in the app.
const OPEN_GREEN_RGB = '40, 167, 69';
const SURGE_RGB = '255, 138, 30';
const LOW_CONFIDENCE_THRESHOLD = 0.4;

const runtime = {
    enabled: false,
    initialized: false,
    view: 'nowcast',
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
    const storedView = localStorage.getItem(VIEW_KEY);
    if (storedView === 'forecast' || storedView === 'nowcast') {
        runtime.view = storedView;
        syncViewToggle();
    }

    if (stored === 'true') {
        setPropMatrixVisible(true);
    }

    if (!runtime.initialized) {
        toggle.addEventListener('click', () => {
            setPropMatrixVisible(!runtime.enabled);
        });

        const closeBtn = panel.querySelector('.prop-matrix-close');
        if (closeBtn) {
            closeBtn.addEventListener('click', () => setPropMatrixVisible(false));
        }

        const viewToggle = document.getElementById(VIEW_TOGGLE_ID);
        if (viewToggle) {
            viewToggle.addEventListener('click', () => {
                runtime.view = runtime.view === 'nowcast' ? 'forecast' : 'nowcast';
                localStorage.setItem(VIEW_KEY, runtime.view);
                syncViewToggle();
                // Force a re-render from cache (no re-fetch needed; both views
                // are present in the same response payload).
                runtime.lastRenderKey = '';
                renderPropMatrix();
            });
        }

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

        runtime.initialized = true;
    }
}

function syncViewToggle() {
    const viewToggle = document.getElementById(VIEW_TOGGLE_ID);
    if (!viewToggle) return;
    viewToggle.textContent = runtime.view === 'nowcast' ? 'Nowcast ▸' : 'Forecast ▸';
    viewToggle.setAttribute('aria-pressed', runtime.view === 'forecast' ? 'true' : 'false');
    viewToggle.title = runtime.view === 'nowcast'
        ? 'Showing nowcast. Click for 1-hour forecast.'
        : 'Showing 1-hour forecast. Click for nowcast.';
}

export function setPropMatrixVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    if (toggle) toggle.style.display = visible ? 'none' : '';
    localStorage.setItem(ENABLE_KEY, visible ? 'true' : 'false');
    if (runtime.onLayoutChange) runtime.onLayoutChange();
    if (visible) {
        syncViewToggle();
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
    const key = `${qth}|15`;
    if (!force && runtime.cache && runtime.cacheKey === key && (now - runtime.lastFetchedAt) < CACHE_TTL_MS) {
        renderPropMatrix();
        return;
    }

    if (runtime.abortController) runtime.abortController.abort();
    const controller = new AbortController();
    runtime.abortController = controller;

    const params = new URLSearchParams();
    params.set('qth', qth);
    params.set('minutes', '15');

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

    const enabledBands = getEnabledBands();
    const bandList = BAND_ORDER.filter((b) => enabledBands.has(b));
    const regions = Array.isArray(resp.regions) && resp.regions.length > 0
        ? resp.regions
        : WSPR_REGIONS;

    const cellsByBandRegion = indexCells(resp);

    // Rebuild fingerprint: skip DOM rebuild when nothing changed.
    const renderKey = `${runtime.view}|${bandList.join(',')}|${regions.join(',')}|${fingerprintCells(cellsByBandRegion)}`;
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    if (bandList.length === 0) {
        body.innerHTML = '<div class="text-muted small">No bands enabled.</div>';
        return;
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
            html += renderCell(band, region, cell);
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    body.innerHTML = html;
}

function renderCell(band, region, cell) {
    if (!cell) {
        return '<td class="prop-matrix-cell-empty"></td>';
    }
    const view = cell[runtime.view] || cell.nowcast || cell;
    const pOpen = clamp01(Number(view.pOpen ?? 0));
    const expectedCount = Number(view.expectedCount ?? 0);
    const confidence = clamp01(Number(view.confidence ?? (cell.confidence ?? 0)));
    const surge = Boolean(cell.surge);

    const alpha = 0.15 + pOpen * 0.85;
    const background = `rgba(${OPEN_GREEN_RGB}, ${alpha})`;
    const roundedCount = Math.round(expectedCount);

    const classes = ['prop-matrix-cell'];
    if (surge) classes.push('prop-matrix-surge');
    if (confidence < LOW_CONFIDENCE_THRESHOLD) classes.push('prop-matrix-low-confidence');

    const title = buildCellTitle(band, region, pOpen, expectedCount, confidence, surge);
    const badge = expectedCount > 0 ? `<span class="prop-matrix-badge">${roundedCount}</span>` : '';
    const surgeIcon = surge ? '<span class="prop-matrix-surge-icon" title="surge">⚡</span>' : '';

    return `<td class="${classes.join(' ')}" style="background: ${background};" title="${escapeHtml(title)}">${surgeIcon}${badge}</td>`;
}

function buildCellTitle(band, region, pOpen, expectedCount, confidence, surge) {
    const lines = [
        `${band} → ${region}`,
        `${runtime.view === 'forecast' ? 'Forecast' : 'Nowcast'}: P(open) ${(pOpen * 100).toFixed(0)}%, exp ${Math.round(expectedCount)}/h`,
        `confidence ${(confidence * 100).toFixed(0)}%`,
    ];
    if (surge) lines.push('SURGE');
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
        const v = c[runtime.view] || c.nowcast || c;
        out.push(`${k}:${Number(v?.pOpen ?? 0).toFixed(3)}:${Number(v?.expectedCount ?? 0).toFixed(1)}:${c.surge ? 1 : 0}`);
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

// --- Test hooks -----------------------------------------------------------
// Exported for vitest. These are not part of the public UI API.
export const __test = {
    runtime,
    reset() {
        stopPolling();
        runtime.enabled = false;
        runtime.initialized = false;
        runtime.view = 'nowcast';
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
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    VIEW_TOGGLE_ID,
    ENABLE_KEY,
    VIEW_KEY,
};