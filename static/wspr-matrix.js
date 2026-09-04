import { state } from './state.js';
import { WSPR_REGIONS, bandColors } from './utils.js';
import { makeDraggable } from './panel-drag.js';

// wspr-matrix.js — the unified Prop panel: band × region propagation-
// intelligence matrix over ALL ingest sources (WSPR, PSKReporter FT8/FT4,
// RBN, DX cluster). Polls /api/prop_intel/v2 (the multi-source contract;
// v1 stays frozen for the horstapp widgets). Renders per-cell activity on the
// canonical teal heat ramp with SSB/CW flags, rising slope, atypical-surge
// badges (!! when multiple sources agree), and a ×n source-count corner.
// From-here-only by design: cells are paths with the operator's QTH at one
// end — an unfiltered global window is noise (never re-add one).
//
// Fetch discipline adopted from the removed prop-matrix.js: 30s poll with
// visibility gating, 15s cache TTL, AbortController in-flight dedup, QTH-
// change listener. Clicking a cell drills the grid-square plot down to that
// band × region (state.drillDownBand/Region + #drill-down-clear).

const PANEL_ID = 'wspr-matrix-window';
const TOGGLE_ID = 'wspr-matrix-toggle';
const BODY_ID = 'wspr-matrix-body';
const ENABLE_KEY = 'wsprMatrixEnabled';
const SOURCES_KEY = 'wsprMatrixSources';
// Legacy localStorage key from the removed from-here/unfiltered toggle —
// the matrix is from-here-only now; clean up the stale pref once.
const LEGACY_FROM_HERE_KEY = 'wsprMatrixFromHere';
// Legacy key from the deleted prop-matrix.js panel — merged away.
const LEGACY_PROP_MATRIX_KEY = 'propMatrixEnabled';

const POLL_INTERVAL_MS = 30_000;
const CACHE_TTL_MS = 15_000;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

// Selectable sources, canonical order (matches the backend's parseSources
// contract). Public chip label → API source name.
const SOURCES = [
    { key: 'wspr', label: 'WSPR' },
    { key: 'pskr', label: 'PSKR' },
    { key: 'rbn', label: 'RBN' },
    { key: 'dxcluster', label: 'DXC' },
];
const ALL_SOURCES = SOURCES.map((s) => s.key);

// Cell heat ramps. Activity maps to *distance from the panel background* in
// both themes — the perceptual trick that makes the gradient read at a glance:
//   light theme: pale aqua (sparse) → deep teal (peak), chips darken
//   dark theme:  deep teal (sparse) → luminous aqua (peak), chips brighten
// Each ramp spans ~50 L* (the old single-direction teal ramp capped at ~23 L*
// by white text, so everything above ~5 spots looked identical). sqrt() still
// spreads the low end so 1-2 spots don't collapse onto the sparse shade.
const CELL_COLOR_LIGHT = { low: [159, 217, 226], high: [8, 55, 67] }; // #9fd9e2 → #083743
const CELL_COLOR_DARK = { low: [13, 71, 83], high: [127, 220, 234] }; // #0d4753 → #7fdcea

// Numerals flip ink at the luminance where white/black cross (~0.179); pure
// black/white inks keep >= 4.58:1 on every shade either side of the switch —
// a mid-luminance teal would fail AA with both inks, which is why the switch
// uses #000 rather than the softer #1a1a1a used by the flag badges.
const WHITE_INK = 'rgb(255, 255, 255)';
const BLACK_INK = 'rgb(0, 0, 0)';

function rgbLuminance([r, g, b]) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

// Map a 0..1 activity intensity to a solid background color for the theme.
function cellColor(intensity, theme = 'light') {
    const { low, high } = theme === 'dark' ? CELL_COLOR_DARK : CELL_COLOR_LIGHT;
    const t = Math.sqrt(Math.max(0, Math.min(1, intensity)));
    const rgb = low.map((c, i) => Math.round(c + (high[i] - c) * t));
    return `rgb(${rgb.join(', ')})`;
}

// Pick the numeral ink for a chip color: whichever of white/black is AA-safe.
function cellInk(rgb) {
    const l = typeof rgb === 'string'
        ? rgbLuminance(rgb.match(/\d+/g).map(Number))
        : rgbLuminance(rgb);
    return l <= 0.179 ? WHITE_INK : BLACK_INK;
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
    pollTimer: null,
    abortController: null,
    cache: null,         // last /api/prop_intel/v2 payload
    cacheKey: '',
    lastFetchedAt: 0,
    lastQth: '',
    lastRenderKey: '',
    sources: [...ALL_SOURCES],
    onLayoutChange: null,
};

export function initWsprMatrix({ onLayoutChange } = {}) {
    runtime.onLayoutChange = onLayoutChange || null;
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel || !toggle) return;

    localStorage.removeItem(LEGACY_FROM_HERE_KEY);
    localStorage.removeItem(LEGACY_PROP_MATRIX_KEY);

    const storedSources = localStorage.getItem(SOURCES_KEY);
    if (storedSources) {
        const parsed = storedSources.split(',').map((s) => s.trim()).filter((s) => ALL_SOURCES.includes(s));
        // Canonical order (matches the backend's parse + the chip row).
        parsed.sort((a, b) => ALL_SOURCES.indexOf(a) - ALL_SOURCES.indexOf(b));
        if (parsed.length > 0) runtime.sources = parsed;
    }

    const stored = localStorage.getItem(ENABLE_KEY);
    if (stored === 'true') {
        setWsprMatrixVisible(true);
    }

    toggle.addEventListener('click', () => {
        setWsprMatrixVisible(!runtime.enabled);
    });

    // Re-poll when QTH changes (band-lab.js pattern).
    const qthInput = document.getElementById('qth');
    if (qthInput) {
        qthInput.addEventListener('change', () => {
            if (!runtime.enabled) return;
            const next = currentQth();
            if (next !== runtime.lastQth) {
                invalidateCache();
                pollMatrix(true);
            }
        });
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
        startPolling();
    } else {
        stopPolling();
    }
}

function startPolling() {
    stopPolling();
    pollMatrix(true);
    runtime.pollTimer = setInterval(() => {
        if (document.visibilityState !== 'visible') return;
        pollMatrix(false);
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

function invalidateCache() {
    runtime.cache = null;
    runtime.cacheKey = '';
    runtime.lastFetchedAt = 0;
}

function currentQth() {
    return String(document.getElementById('qth')?.value || state.qth || '').trim().toUpperCase();
}

// Toggle a source chip. At least one source stays selected — an empty
// selection would render the whole panel meaningless.
function toggleSource(key) {
    const idx = runtime.sources.indexOf(key);
    if (idx >= 0) {
        if (runtime.sources.length === 1) return;
        runtime.sources.splice(idx, 1);
    } else {
        runtime.sources.push(key);
        runtime.sources.sort((a, b) => ALL_SOURCES.indexOf(a) - ALL_SOURCES.indexOf(b));
    }
    localStorage.setItem(SOURCES_KEY, runtime.sources.join(','));
    invalidateCache();
    pollMatrix(true);
}

export async function updateWsprMatrix() {
    if (!runtime.enabled) return;
    pollMatrix(false);
}

async function pollMatrix(force) {
    if (!runtime.enabled) return;
    const qth = currentQth();
    const body = document.getElementById(BODY_ID);
    if (!qth) {
        if (body) body.innerHTML = '<div class="text-muted small">Set a QTH to see the Prop matrix.</div>';
        return;
    }

    const now = Date.now();
    const key = `${qth}|15|fh|${runtime.sources.join(',')}`;
    if (!force && runtime.cache && runtime.cacheKey === key && (now - runtime.lastFetchedAt) < CACHE_TTL_MS) {
        renderMatrix();
        return;
    }

    if (runtime.abortController) runtime.abortController.abort();
    const controller = new AbortController();
    runtime.abortController = controller;

    // From-here-only by design, anchored at the QTH with its surroundings
    // block — same request shape v1 used, now multi-source with the chip
    // selection passed as CSV.
    const params = new URLSearchParams();
    params.set('qth', qth);
    params.set('minutes', '15');
    params.set('surroundings', 'true');
    params.set('from_here', 'true');
    params.set('sources', runtime.sources.join(','));

    try {
        const resp = await fetch(`/api/prop_intel/v2?${params.toString()}`, { signal: controller.signal });
        if (!resp.ok) throw new Error(`prop_intel v2 HTTP ${resp.status}`);
        const payload = await resp.json();
        runtime.cache = payload;
        runtime.cacheKey = key;
        runtime.lastFetchedAt = Date.now();
        runtime.lastQth = qth;
        renderMatrix();
    } catch (err) {
        if (err?.name === 'AbortError') return;
        console.warn('prop_intel v2 fetch failed:', err);
        if (body && runtime.cache == null) {
            body.innerHTML = '<div class="text-muted small">Propagation intelligence unavailable.</div>';
        }
    } finally {
        if (runtime.abortController === controller) {
            runtime.abortController = null;
        }
    }
}

function renderSourceChips() {
    const chips = SOURCES.map((s) => {
        const on = runtime.sources.includes(s.key);
        return `<button type="button" class="wspr-src-chip${on ? ' is-on' : ''}" data-source="${s.key}" aria-pressed="${on}">${s.label}</button>`;
    }).join('');
    return `<div class="wspr-src-chips" role="group" aria-label="Sources">${chips}</div>`;
}

function renderMatrix() {
    const body = document.getElementById(BODY_ID);
    if (!body) return;
    const data = runtime.cache;
    const cells = (data && Array.isArray(data.cells)) ? data.cells : [];
    if (cells.length === 0) {
        runtime.lastRenderKey = '';
        body.innerHTML = renderSourceChips() +
            '<div class="text-muted small">No paths open in the current window.</div>' +
            legendHtml();
        attachSourceChipHandlers(body);
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

    // Fingerprint for skip-rebuild (theme included: a toggle re-shades chips).
    const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    const renderKey = `${theme}|${runtime.sources.join(',')}|${activeBands
        .map((b) => `${b}:${Array.from(matrix.get(b).entries()).sort().map(([r, c]) =>
            `${r}${c.spot_count}${c.ssb_open ? 'S' : ''}${c.cw_open ? 'C' : ''}${c.rising ? 'R' : ''}` +
            `${c.atypical ? `${c.atypical.z_score}~${c.atypical.confidence}` : ''}` +
            `${(c.active_sources || []).join('+')}@${c.open_agreement ?? ''}~${c.atypical_agreement ?? ''}`
        ).join('')}`)
        .join('|')}`;
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    const maxCount = Math.max(...cells.map((c) => c.spot_count || 0), 1);

    let html = renderSourceChips();
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
            html += renderCell(band, region, cell, maxCount, theme);
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    html += legendHtml();
    body.innerHTML = html;

    attachSourceChipHandlers(body);
    attachCellClickHandler(body);
}

function renderCell(band, region, cell, maxCount, theme) {
    if (!cell || cell.spot_count === 0) {
        return `<td class="wspr-matrix-cell-empty" data-band="${band}" data-region="${region}" role="button" tabindex="0"></td>`;
    }
    const intensity = Math.min(1, cell.spot_count / maxCount);
    let badges = topModeBadges(cell);
    if (cell.rising) badges += '<span class="wspr-badge wspr-badge-rising">&uarr;</span>';
    let atypicalBadge = '';
    let surgeClass = '';
    if (cell.atypical) {
        // !! when independent sources agree on the surge; ! for a single
        // source's atypical call.
        const multi = (cell.atypical_agreement ?? 0) >= 0.5;
        surgeClass = ' wspr-matrix-surge';
        const cls = multi ? ' wspr-badge-atypical-multi' : '';
        const char = multi ? '!!' : '!';
        const conf = Math.round(Math.max(0, Math.min(1, Number(cell.atypical.confidence ?? 0))) * 100);
        atypicalBadge = `<span class="wspr-badge wspr-badge-atypical${cls}" title="atypical z=${cell.atypical.z_score}, confidence ${conf}%${multi ? `, ${Math.round((cell.atypical_agreement ?? 0) * 100)}% of sources agree` : ''}">${char}</span>`;
    }
    // Source-count corner: only meaningful with ≥2 active sources (with one,
    // open agreement is vacuously 1.0). Faded when sources disagree on "open".
    const activeSources = Array.isArray(cell.active_sources) ? cell.active_sources : [];
    let srcMark = '';
    if (activeSources.length >= 2) {
        const disagree = (cell.open_agreement ?? 1) < 1;
        srcMark = `<span class="wspr-matrix-src${disagree ? ' is-mixed' : ''}">×${activeSources.length}</span>`;
    }
    const titleParts = [`${band} → ${region}: ${cell.spot_count} spots`];
    if (cell.ssb_open) titleParts.push('SSB open');
    if (cell.cw_open) titleParts.push('CW open');
    if (cell.rising) titleParts.push('rising');
    if (cell.atypical) {
        const conf = Math.round(Math.max(0, Math.min(1, Number(cell.atypical.confidence ?? 0))) * 100);
        titleParts.push(`atypical z=${cell.atypical.z_score}, confidence ${conf}%`);
    }
    if (Array.isArray(cell.sources) && cell.sources.length) {
        for (const s of cell.sources) {
            let line = `${s.source}: ${s.spot_count} spots`;
            if (s.open) {
                line += s.open_basis === 'presence' ? ' (presence)' : ` open (${s.open_basis}${s.unknown_power ? ', tx power unknown' : ''})`;
            }
            if (s.atypical) line += `, z=${s.atypical.z_score}`;
            titleParts.push(line);
        }
    }
    const bg = cellColor(intensity, theme);
    return `<td class="wspr-matrix-cell${surgeClass}" style="background: ${bg}; color: ${cellInk(bg)}" title="${titleParts.join('\n')}" data-band="${band}" data-region="${region}" role="button" tabindex="0">${cell.spot_count}${badges}${atypicalBadge}${srcMark}</td>`;
}

function legendHtml() {
    return '<div class="wspr-matrix-legend small text-muted"><span class="wspr-heat-scale" aria-hidden="true"></span>=spots: few &rarr; many <span class="wspr-badge wspr-badge-ssb">S</span>=SSB <span class="wspr-badge wspr-badge-cw">C</span>=CW (top mode) <span class="wspr-badge wspr-badge-rising">&uarr;</span>=rising <span class="wspr-badge wspr-badge-atypical">!</span>=atypical <span class="wspr-badge wspr-badge-atypical wspr-badge-atypical-multi">!!</span>=atypical, multi-source <span class="wspr-matrix-src">×n</span>=sources</div>';
}

function attachSourceChipHandlers(body) {
    body.querySelectorAll('.wspr-src-chip').forEach((chip) => {
        chip.addEventListener('click', () => {
            const key = chip.getAttribute('data-source');
            if (key) toggleSource(key);
        });
    });
}

// --- Drill-down -------------------------------------------------------------
// Clicking a matrix cell filters the grid-square plot to that band × region.
// The cell's data attrs carry band/region; both are stashed in the shared
// state singleton and a re-render is scheduled via the global hook exported
// by app.js (window.__horstScheduleRender).
function attachCellClickHandler(body) {
    body.querySelectorAll('.wspr-matrix-cell, .wspr-matrix-cell-empty').forEach((td) => {
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

// Test hooks.
export const __test = {
    cellColor,
    cellInk,
    rgbLuminance,
    topModeBadges,
    renderCell,
    runtime,
    reset() {
        stopPolling();
        runtime.enabled = false;
        runtime.cache = null;
        runtime.cacheKey = '';
        runtime.lastFetchedAt = 0;
        runtime.lastQth = '';
        runtime.lastRenderKey = '';
        runtime.sources = [...ALL_SOURCES];
        runtime.onLayoutChange = null;
    },
    invalidateCache,
    toggleSource,
    updateDrillDownButton,
    clearDrillDown,
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
    SOURCES_KEY,
};
