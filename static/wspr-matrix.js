import { state } from './state.js';
import { WSPR_REGIONS, bandColors, getEnabledBands, getMinSnrMode } from './utils.js';
import { escapeHtml } from './ui-helpers.js';
import {
    LOOKS, payloadHas, stripAxisHtml, stripRowHtml, layoutStripLabels,
    dayStripSvg, sparklineSvg, lookLegendHtml,
} from './wspr-matrix-looks.js';

// wspr-matrix.js — the unified Propagation panel: band × region propagation-
// intelligence matrix over ALL ingest sources (WSPR, PSKReporter FT8/FT4,
// RBN, DX cluster). Polls /api/prop_intel/v2 (the multi-source contract;
// v1 stays frozen for the horstapp widgets). Every look shows a path against
// its normal for this hour, not its raw count (wspr-matrix-looks.js, chip
// row): dot strip (default), day strip, sparkline. They replaced the viridis /
// inferno heatmaps (docs/ideation/2026-10-05-now-matrix-ten-more-ways.html)
// and ask the backend for silent cells, day curves and the trend (silent=1,
// normal_day=1, trend=1).
// The SSB/CW open floors follow the global "Min SNR" control: the panel
// passes ssb_min_db/cw_min_db from #ssb-min-db/#cw-min-db so a stricter
// min-SNR raises the open thresholds here too (backend v2 overrides).
// From-here-only by design: cells are paths with the operator's QTH at one
// end — an unfiltered global window is noise (never re-add one).
//
// Fetch discipline adopted from the removed prop-matrix.js: 30s poll with
// visibility gating, 15s cache TTL, AbortController in-flight dedup, QTH-
// change listener. Clicking a cell drills the grid-square plot down to that
// band × region (state.drillDownBand/Region + #drill-down-clear).
//
// Keyboard: the table is an ARIA grid with a roving tabindex. Exactly one data
// cell is in the tab order (the drill-down cell, else the first data cell);
// arrow keys move between data cells (empty cells are skipped and inert),
// Home/End jump within the row (with Ctrl: within the grid), Enter/Space
// toggle the drill-down like a click. Re-renders put focus back on the same
// band × region cell when it was inside the panel.

const PANEL_ID = 'wspr-matrix-window';
const BODY_ID = 'wspr-matrix-body';
const SOURCES_KEY = 'wsprMatrixSources';
const STYLE_KEY = 'wsprMatrixStyle';
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
    { key: 'wspr', label: 'WSPR', title: 'WSPR beacon reports' },
    { key: 'pskr', label: 'PSKR', title: 'PSKReporter FT8 and FT4 reports' },
    { key: 'rbn', label: 'RBN', title: 'Reverse Beacon Network CW and RTTY spots' },
    { key: 'dxcluster', label: 'DXC', title: 'DX cluster spots' },
];
const SOURCE_LABELS = Object.fromEntries(SOURCES.map((s) => [s.key, s.label]));
// Per-source "open" basis (backend v2 open_basis) in the cell tooltip.
const OPEN_BASIS_LABELS = { budget: 'link budget', snr_floor: 'SNR floor' };
const ALL_SOURCES = SOURCES.map((s) => s.key);
// Default selection excludes dxcluster: prod has no DX-cluster ingest wired
// (-dxcluster-enable absent), so DXC would sit there contributing nothing.
// The chip stays available for local/dev instances that run the ingest.
const DEFAULT_SOURCES = ['wspr', 'pskr', 'rbn'];

// The looks (wspr-matrix-looks.js). A stored look that no longer exists
// (viridis / inferno) falls back to the default.
const STYLES = LOOKS;
const ALL_STYLES = STYLES.map((s) => s.key);
const DEFAULT_STYLE = 'dots';
// One retry when the data a look needs was not ready yet (the area normal
// and day curves load in the background on the server).
const DETAIL_RETRY_MS = 6_000;

// Surge strength for the accessible name: z >= 4 or >= 50% source agreement
// is a strong surge.
function surgeStrength(cell) {
    if (!cell.atypical) return 0;
    const multi = (cell.atypical_agreement ?? 0) >= 0.5;
    const z = Number(cell.atypical.z_score) || 0;
    return (z >= 4 || multi) ? 2 : 1;
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
    sources: [...DEFAULT_SOURCES],
    style: DEFAULT_STYLE,
    detailRetryTimer: null,
    detailRetryKey: '',
    stripObserver: null,
    stripWidth: 0,
    // Optional extra per-band columns (see setRowExtras); the Conditions dock
    // uses them to put verdict / count / plot in the same row as the cells.
    rowExtras: null,
};

export function initWsprMatrix() {
    const panel = document.getElementById(PANEL_ID);
    if (!panel) return;

    localStorage.removeItem(LEGACY_FROM_HERE_KEY);
    localStorage.removeItem(LEGACY_PROP_MATRIX_KEY);

    const storedSources = localStorage.getItem(SOURCES_KEY);
    if (storedSources) {
        const parsed = storedSources.split(',').map((s) => s.trim()).filter((s) => ALL_SOURCES.includes(s));
        // Canonical order (matches the backend's parse + the chip row).
        parsed.sort((a, b) => ALL_SOURCES.indexOf(a) - ALL_SOURCES.indexOf(b));
        if (parsed.length > 0) runtime.sources = parsed;
    }

    const storedStyle = localStorage.getItem(STYLE_KEY);
    if (storedStyle && ALL_STYLES.includes(storedStyle)) {
        runtime.style = storedStyle;
    }

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

    // Re-poll when the global Min SNR control changes — the panel's SSB/CW
    // open floors are the same filter as everything else on the page.
    const onMinSnrChange = () => {
        if (!runtime.enabled) return;
        invalidateCache();
        pollMatrix(true);
    };
    document.getElementById('min-snr-group')?.addEventListener('change', (e) => {
        if (e.target?.name === 'min-snr') onMinSnrChange();
    });
    // Delegated: the threshold sliders are re-created on every Min SNR mode
    // switch (Svelte), so element-bound listeners would be lost.
    // Replace, don't stack, if init runs again.
    if (runtime.snrThresholdListener) document.removeEventListener('change', runtime.snrThresholdListener);
    runtime.snrThresholdListener = (e) => {
        const id = e.target?.id;
        if (id === 'ssb-min-db' || id === 'cw-min-db') onMinSnrChange();
    };
    document.addEventListener('change', runtime.snrThresholdListener);
}

// Dock entry point: the Conditions dock owns visibility (cond-dock.js).
export function setWsprMatrixVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    if (!panel) return;
    if (runtime.enabled === visible && (panel.classList.contains('is-hidden') === !visible)) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
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
    clearTimeout(runtime.detailRetryTimer);
    runtime.detailRetryTimer = null;
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
        if (body) body.innerHTML = '<div class="text-muted small">Enter your locator to see propagation.</div>';
        return;
    }

    // Global Min SNR state: only the active threshold is meaningful, but
    // both ride in the key so toggling none/cw/ssb always re-fetches.
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = document.getElementById('ssb-min-db')?.value || '0';
    const cwMinDb = document.getElementById('cw-min-db')?.value || '-15';

    const now = Date.now();
    const key = `${qth}|15|fh|${runtime.sources.join(',')}|${minSnrMode}|${ssbMinDb}|${cwMinDb}`;
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
    // The server widens a sparse home block (live_area.go) so the matrix has cells.
    params.set('rings', 'auto');
    params.set('sources', runtime.sources.join(','));
    if (minSnrMode === 'ssb') params.set('ssb_min_db', ssbMinDb);
    if (minSnrMode === 'cw') params.set('cw_min_db', cwMinDb);
    // What the looks draw besides the counts: silent cells ("usually open,
    // nothing now"), the usual day and the last hour per cell.
    params.set('silent', '1');
    params.set('normal_day', '1');
    params.set('trend', '1');

    try {
        const resp = await fetch(`/api/prop_intel/v2?${params.toString()}`, { signal: controller.signal });
        if (!resp.ok) throw new Error(`prop_intel v2 HTTP ${resp.status}`);
        const payload = await resp.json();
        runtime.cache = payload;
        runtime.cacheKey = key;
        runtime.lastFetchedAt = Date.now();
        runtime.lastQth = qth;
        renderMatrix();
        scheduleDetailRetry(payload, key);
    } catch (err) {
        if (err?.name === 'AbortError') return;
        console.warn('prop_intel v2 fetch failed:', err);
        if (body && runtime.cache == null) {
            // With row extras the rows still carry verdict / count / plot from
            // other sources, so draw them (cells empty) instead of a bare message.
            if (runtime.rowExtras) renderMatrix();
            else body.innerHTML = '<div class="text-muted small">Propagation data unavailable.</div>';
        }
    } finally {
        if (runtime.abortController === controller) {
            runtime.abortController = null;
        }
    }
}

// Switch the look. Render-only (same payload): reset the render fingerprint
// and repaint from cache, no refetch.
function setStyle(key) {
    if (!ALL_STYLES.includes(key) || runtime.style === key) return;
    runtime.style = key;
    localStorage.setItem(STYLE_KEY, key);
    runtime.lastRenderKey = '';
    renderMatrix();
}

// When the data the looks need was not ready (no normal yet, or no day
// curves) the panel refetches once a few seconds later instead of waiting
// for the next 30 s poll.
function scheduleDetailRetry(payload, key) {
    const has = payloadHas(payload);
    const missing = runtime.sources.includes('pskr') && (!has.normal || !has.day);
    if (!missing || runtime.detailRetryKey === key) return;
    runtime.detailRetryKey = key;
    clearTimeout(runtime.detailRetryTimer);
    runtime.detailRetryTimer = setTimeout(() => {
        runtime.detailRetryTimer = null;
        if (runtime.enabled && runtime.cacheKey === key) {
            invalidateCache();
            pollMatrix(true);
        }
    }, DETAIL_RETRY_MS);
}

function renderSourceChips() {
    const chips = SOURCES.map((s) => {
        const on = runtime.sources.includes(s.key);
        return `<button type="button" class="wspr-src-chip${on ? ' is-on' : ''}" data-source="${s.key}" aria-pressed="${on}" title="${s.title}">${s.label}</button>`;
    }).join('');
    const styleChips = STYLES.map((s) => {
        const on = runtime.style === s.key;
        return `<button type="button" class="wspr-src-chip${on ? ' is-on' : ''}" data-style="${s.key}" aria-pressed="${on}" title="${s.title}">${s.label}</button>`;
    }).join('');
    return '<div class="wspr-src-chips">' +
        `<div class="wspr-chip-group" role="group" aria-label="Sources">${chips}</div>` +
        '<span class="wspr-chip-sep" aria-hidden="true"></span>' +
        `<div class="wspr-chip-group" role="group" aria-label="Look">${styleChips}</div>` +
        '</div>';
}

// --- Focus across re-renders ---------------------------------------------------
// The body is rebuilt via innerHTML (poll results, chip toggles, drill-down),
// which drops focus to <body>. Remember what had focus inside the panel and
// put it back on the equivalent element of the new markup.
function captureFocus(body) {
    const el = document.activeElement;
    if (!el || el === document.body || !body.contains(el)) return null;
    if (el.classList.contains('wspr-matrix-cell')) {
        return { band: el.getAttribute('data-band'), region: el.getAttribute('data-region') };
    }
    if (el.classList.contains('wspr-src-chip')) {
        return { source: el.getAttribute('data-source'), style: el.getAttribute('data-style') };
    }
    return {};
}

function findCell(root, band, region) {
    for (const td of root.querySelectorAll('.wspr-matrix-cell')) {
        if (td.getAttribute('data-band') === band && td.getAttribute('data-region') === region) return td;
    }
    return null;
}

function restoreFocus(body, focus) {
    if (!focus) return;
    let target = null;
    if (focus.band) {
        target = findCell(body, focus.band, focus.region);
    } else if (focus.source || focus.style) {
        target = Array.from(body.querySelectorAll('.wspr-src-chip')).find((chip) =>
            chip.getAttribute('data-source') === focus.source && chip.getAttribute('data-style') === focus.style) || null;
    }
    // The cell may be gone (band disabled, path closed): keep focus in the
    // grid on its tab stop rather than losing it to <body>.
    if (!target) target = body.querySelector('.wspr-matrix-cell[tabindex="0"]');
    if (!target) return;
    if (target.classList.contains('wspr-matrix-cell')) setRovingCell(target);
    target.focus();
}

function renderMatrix() {
    const body = document.getElementById(BODY_ID);
    if (!body) return;
    const data = runtime.cache;
    const allCells = (data && Array.isArray(data.cells)) ? data.cells : [];
    // Rows follow the band rail's enabled set (when the rail is present).
    const enabled = document.querySelector('.band-enable') ? getEnabledBands() : null;
    const cells = enabled ? allCells.filter((c) => enabled.has(c.band)) : allCells;
    if (cells.length === 0 && !(runtime.rowExtras && enabled && enabled.size > 0)) {
        runtime.lastRenderKey = '';
        const empty = allCells.length > 0
            ? 'No paths open on the enabled bands.'
            : 'No paths open in the current window.';
        const focus = captureFocus(body);
        body.innerHTML = renderSourceChips() +
            `<div class="text-muted small">${empty}</div>` +
            lookLegendHtml(runtime.style, payloadHas(data), runtime.sources.includes('pskr'));
        attachSourceChipHandlers(body);
        restoreFocus(body, focus);
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
    // With row extras (Conditions dock) every enabled band gets a row, even
    // without a path, so the verdict / count / plot columns stay glanceable.
    if (runtime.rowExtras && enabled) {
        for (const band of enabled) {
            if (!matrix.has(band) && BAND_ORDER.includes(band)) {
                matrix.set(band, new Map());
                activeBands.push(band);
            }
        }
    }
    activeBands.sort((a, b) => BAND_ORDER.indexOf(a) - BAND_ORDER.indexOf(b));
    const extras = runtime.rowExtras;
    const hidden = new Set(extras?.hiddenRegions || []);
    const regions = WSPR_REGIONS.filter((r) => !hidden.has(r));

    // Fingerprint for skip-rebuild (drill-down included: it sets
    // aria-selected and the tab stop; region names included: they are in the
    // cell names; the payload time included: the looks draw normals, day
    // curves and trends, so every new payload repaints). The marks draw in
    // currentColor, so a theme toggle needs no rebuild.
    const regionNames = (data && data.region_names) || {};
    const renderKey = `${runtime.style}|${runtime.sources.join(',')}|t${data?.now ?? ''}` +
        `|x:${extras ? extras.key(activeBands) : ''}` +
        `|${state.drillDownBand}×${state.drillDownRegion}` +
        `|${WSPR_REGIONS.map((r) => regionNames[r] || '').join(',')}|${activeBands
        .map((b) => `${b}:${Array.from(matrix.get(b).entries()).sort().map(([r, c]) =>
            `${r}${c.spot_count}${c.ssb_open ? 'S' : ''}${c.cw_open ? 'C' : ''}${c.rising ? 'R' : ''}${c.silent ? 'Q' : ''}` +
            `${c.atypical ? `${c.atypical.z_score}~${c.atypical.confidence}` : ''}` +
            `${(c.active_sources || []).join('+')}@${c.open_agreement ?? ''}~${c.atypical_agreement ?? ''}`
        ).join('')}`)
        .join('|')}`;
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    const extraHead = extras ? extras.columns.map((c) => `<th scope="col" role="columnheader" class="${c.className || ''}">${c.label}</th>`).join('') : '';
    const strip = runtime.style === 'dots';

    let html = renderSourceChips();
    html += `<table class="wspr-matrix-table wspr-look-${runtime.style}" role="grid" aria-label="Propagation by band and region">` +
        '<thead><tr role="row"><th scope="col" role="columnheader"><span class="wspr-sr-only">Band</span></th>' + extraHead;
    if (strip) {
        html += `<th scope="col" role="columnheader" class="wspr-strip-head"><span class="wspr-sr-only">Regions against their normal</span>${stripAxisHtml()}</th>`;
    } else {
        for (const region of regions) {
            html += `<th scope="col" role="columnheader" title="${escapeHtml(regionNames[region] || region)}">${region}</th>`;
        }
    }
    html += '</tr></thead><tbody>';

    for (const band of activeBands) {
        const bandMap = matrix.get(band);
        const color = bandColors[band] || bandColors.all || '#555';
        html += `<tr role="row"><th scope="row" role="rowheader" class="wspr-matrix-band" style="border-left: 3px solid ${color}">${band}${extras?.rowHeader ? extras.rowHeader(band) : ''}</th>`;
        if (extras) html += extras.cells(band);
        if (strip) {
            const dots = regions.filter((r) => bandMap.has(r)).map((region) => {
                const cell = bandMap.get(region);
                return { region, cell, attrs: cellAttrs(band, region, cell, regionNames) };
            });
            html += `<td class="wspr-strip-td" role="presentation">${stripRowHtml(dots)}</td>`;
        } else {
            for (const region of regions) {
                const cell = bandMap.get(region);
                html += renderLookCell(band, region, cell, data, regionNames);
            }
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    html += lookLegendHtml(runtime.style, payloadHas(data), runtime.sources.includes('pskr'));

    const focus = captureFocus(body);
    body.innerHTML = html;
    if (strip) {
        layoutStripLabels(body);
        watchStripWidth(body);
    }

    attachSourceChipHandlers(body);
    const table = body.querySelector('.wspr-matrix-table');
    attachGridHandlers(table);
    const drill = findCell(table, state.drillDownBand, state.drillDownRegion);
    const first = table.querySelector('.wspr-matrix-cell');
    if (drill || first) setRovingCell(drill || first);
    restoreFocus(body, focus);
    extras?.after(body);
}

// The strip's dot and label placement depends on its width, which changes
// with the window, the dock splitter and the row headers (verdict text
// arrives separately): lay it out again when the first strip resizes.
function watchStripWidth(body) {
    if (typeof ResizeObserver !== 'function') return;
    const strip = body.querySelector('.wspr-strip');
    if (!runtime.stripObserver) {
        runtime.stripObserver = new ResizeObserver((entries) => {
            const w = Math.round(entries[entries.length - 1]?.contentRect?.width || 0);
            if (w === runtime.stripWidth) return;
            runtime.stripWidth = w;
            const root = document.getElementById(BODY_ID);
            if (root && runtime.style === 'dots') layoutStripLabels(root);
        });
    }
    runtime.stripObserver.disconnect();
    runtime.stripWidth = strip ? Math.round(strip.getBoundingClientRect().width) : 0;
    if (strip) runtime.stripObserver.observe(strip);
}

// "20m to Japan: 1,234 spots" (full region name from the payload's
// region_names, code as fallback).
function pathSummary(band, region, cell, regionNames = {}) {
    const count = Number(cell.spot_count) || 0;
    return `${band} to ${regionNames[region] || region}: ${count.toLocaleString('en-US')} ${count === 1 ? 'spot' : 'spots'}`;
}

// The cell's from-here normal (backend expected / expected_spots, PSKReporter
// reports from the operator's area), when present.
function hasNormal(cell) {
    return cell?.expected != null && cell?.expected_spots != null &&
        Number.isFinite(Number(cell.expected)) && Number.isFinite(Number(cell.expected_spots));
}

// "PSKReporter: 771 now, normal about 4,515 at this hour (×0.17)" for the
// tooltip; a normal under one report reads "normally under 1".
function expectedLine(cell) {
    const now = Number(cell.expected_spots) || 0;
    const e = Number(cell.expected);
    if (e < 1) return `PSKReporter: ${now.toLocaleString('en-US')} now, normally under 1 at this hour`;
    const r = now / e;
    const factor = r < 1 ? r.toFixed(2) : r < 10 ? r.toFixed(1) : String(Math.round(r));
    return `PSKReporter: ${now.toLocaleString('en-US')} now, normal about ${Math.round(e).toLocaleString('en-US')} at this hour (\u00d7${factor})`;
}

// Accessible name for a data cell: path, spot count, top mode, rising and
// surge.
function cellLabel(band, region, cell, regionNames = {}) {
    if (isSilent(cell)) {
        return `${band} to ${regionNames[region] || region}: no reports now, usually about ${Math.round(Number(cell.expected)).toLocaleString('en-US')} at this hour`;
    }
    const parts = [pathSummary(band, region, cell, regionNames)];
    if (cell.ssb_open) parts.push('SSB open');
    else if (cell.cw_open) parts.push('CW open');
    if (cell.rising) parts.push('rising');
    const surge = surgeStrength(cell);
    if (surge === 2) parts.push('strong surge');
    else if (surge === 1) parts.push('surge');
    return parts.join(', ');
}

// A cell with no reports now that usually has some at this hour (backend
// silent=1).
function isSilent(cell) {
    return Boolean(cell?.silent) && !(Number(cell.spot_count) > 0);
}

// Tooltip lines for a data cell: path and count, modes, rising, surge, the
// from-here normal and one line per source.
function cellTitle(band, region, cell, regionNames = {}) {
    if (isSilent(cell)) return cellLabel(band, region, cell, regionNames);
    const titleParts = [pathSummary(band, region, cell, regionNames)];
    if (cell.ssb_open) titleParts.push('SSB open');
    if (cell.cw_open) titleParts.push('CW open');
    if (cell.rising) titleParts.push('Rising');
    if (cell.atypical) {
        const conf = Math.round(Math.max(0, Math.min(1, Number(cell.atypical.confidence ?? 0))) * 100);
        titleParts.push(`Surge: z=${cell.atypical.z_score}, confidence ${conf}%`);
    }
    if (hasNormal(cell)) titleParts.push(expectedLine(cell));
    if (Array.isArray(cell.sources) && cell.sources.length) {
        for (const s of cell.sources) {
            let line = `${SOURCE_LABELS[s.source] || s.source}: ${s.spot_count} spots`;
            if (s.open) {
                const basis = OPEN_BASIS_LABELS[s.open_basis] || s.open_basis;
                line += s.open_basis === 'presence' ? ' (presence)' : `, open (${basis}${s.unknown_power ? ', TX power unknown' : ''})`;
            }
            if (s.atypical) line += `, z=${s.atypical.z_score}`;
            titleParts.push(line);
        }
    }
    return titleParts.join('\n');
}

// Shared gridcell attributes (role, name, tooltip, selection, roving tab
// stop, band × region) for a look's mark.
function cellAttrs(band, region, cell, regionNames) {
    const selected = state.drillDownBand === band && state.drillDownRegion === region;
    return `role="gridcell" title="${escapeHtml(cellTitle(band, region, cell, regionNames))}" ` +
        `aria-label="${escapeHtml(cellLabel(band, region, cell, regionNames))}" aria-selected="${selected}" tabindex="-1" ` +
        `data-band="${band}" data-region="${region}"`;
}

// Day strip / sparkline cell (wspr-matrix-looks.js draws the SVG).
function renderLookCell(band, region, cell, data, regionNames) {
    if (!cell || (!(cell.spot_count > 0) && !isSilent(cell))) {
        return `<td role="gridcell" class="wspr-matrix-cell-empty" data-band="${band}" data-region="${region}"></td>`;
    }
    const label = cellLabel(band, region, cell, regionNames);
    const now = Number(data?.now) || Math.floor(Date.now() / 1000);
    const minutes = Number(data?.minutes) || 15;
    const svg = runtime.style === 'day'
        ? dayStripSvg(cell, now, minutes, label)
        : sparklineSvg(cell, now, minutes, Number(data?.trend_bin_minutes) || 5, label);
    return `<td class="wspr-matrix-cell wspr-look-cell${isSilent(cell) ? ' is-silent' : ''}" ${cellAttrs(band, region, cell, regionNames)}>${svg}</td>`;
}

function attachSourceChipHandlers(body) {
    body.querySelectorAll('.wspr-src-chip').forEach((chip) => {
        chip.addEventListener('click', () => {
            const source = chip.getAttribute('data-source');
            if (source) toggleSource(source);
            const styleKey = chip.getAttribute('data-style');
            if (styleKey) setStyle(styleKey);
        });
    });
}

// --- Grid keyboard model ----------------------------------------------------
// Roving tabindex over the data cells (.wspr-matrix-cell); empty cells are
// never focusable, so every move skips them. A data cell is a <td>, or in the
// dot strip a dot inside the row's one strip cell: its row is the enclosing
// <tr> and its column the dot's position (data-col).
function setRovingCell(td) {
    const table = td.closest('table');
    if (!table) return;
    table.querySelectorAll('.wspr-matrix-cell[tabindex="0"]').forEach((c) => {
        if (c !== td) c.setAttribute('tabindex', '-1');
    });
    td.setAttribute('tabindex', '0');
}

function dataCells(row) {
    return Array.from(row.querySelectorAll('.wspr-matrix-cell'));
}

function rowOf(el) {
    return el.closest('tr');
}

function colOf(el) {
    return el.tagName === 'TD' ? el.cellIndex : Number(el.getAttribute('data-col')) || 0;
}

// Data cell in `row` whose column is closest to `col` (ties go left).
function closestInRow(row, col) {
    let best = null;
    for (const td of dataCells(row)) {
        if (!best || Math.abs(colOf(td) - col) < Math.abs(colOf(best) - col)) best = td;
    }
    return best;
}

function cellInRowStep(td, dir) {
    const cells = dataCells(rowOf(td));
    return cells[cells.indexOf(td) + dir] || null;
}

// Up/Down keep the column when a later row has a data cell there; otherwise
// they land on the closest data cell of the next row that has any.
function cellInColumnStep(table, td, dir) {
    const rows = Array.from(table.tBodies[0]?.rows || []);
    const start = rows.indexOf(rowOf(td));
    const col = colOf(td);
    for (let i = start + dir; td.tagName === 'TD' && i >= 0 && i < rows.length; i += dir) {
        const c = rows[i].cells[col];
        if (c && c.classList.contains('wspr-matrix-cell')) return c;
    }
    for (let i = start + dir; i >= 0 && i < rows.length; i += dir) {
        const c = closestInRow(rows[i], col);
        if (c) return c;
    }
    return null;
}

function onGridKeydown(e) {
    const td = e.target.closest?.('.wspr-matrix-cell');
    const table = e.currentTarget;
    if (!td || !table.contains(td)) return;
    let next = null;
    switch (e.key) {
        case 'ArrowRight': next = cellInRowStep(td, 1); break;
        case 'ArrowLeft': next = cellInRowStep(td, -1); break;
        case 'ArrowDown': next = cellInColumnStep(table, td, 1); break;
        case 'ArrowUp': next = cellInColumnStep(table, td, -1); break;
        case 'Home': {
            const cells = e.ctrlKey ? Array.from(table.querySelectorAll('.wspr-matrix-cell')) : dataCells(rowOf(td));
            next = cells[0] || null;
            break;
        }
        case 'End': {
            const cells = e.ctrlKey ? Array.from(table.querySelectorAll('.wspr-matrix-cell')) : dataCells(rowOf(td));
            next = cells[cells.length - 1] || null;
            break;
        }
        case 'Enter':
        case ' ':
            e.preventDefault();
            toggleDrillDown(td.getAttribute('data-band'), td.getAttribute('data-region'));
            return;
        default:
            return;
    }
    // Arrows/Home/End never scroll the panel, even at the grid edge.
    e.preventDefault();
    if (next && next !== td) {
        setRovingCell(next);
        next.focus();
    }
}

function attachGridHandlers(table) {
    if (!table) return;
    table.addEventListener('click', (e) => {
        const td = e.target.closest?.('.wspr-matrix-cell');
        if (!td || !table.contains(td)) return;
        toggleDrillDown(td.getAttribute('data-band'), td.getAttribute('data-region'));
    });
    table.addEventListener('keydown', onGridKeydown);
    // A clicked (or otherwise focused) cell becomes the tab stop.
    table.addEventListener('focusin', (e) => {
        const td = e.target.closest?.('.wspr-matrix-cell');
        if (td) setRovingCell(td);
    });
}

// --- Drill-down -------------------------------------------------------------
// Activating a matrix cell (click, Enter, Space) filters the grid-square plot
// to that band × region; activating the active cell again clears it. Both are
// stashed in the shared state singleton and a map re-render is scheduled via
// the global hook exported by app.js (window.__horstScheduleRender). The
// matrix re-renders too (aria-selected and the tab stop follow the filter).
function toggleDrillDown(band, region) {
    if (!band || !region) return;
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
    if (runtime.enabled && runtime.cache) renderMatrix();
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
    if (runtime.enabled && runtime.cache) renderMatrix();
}

// Conditions dock hooks. `extras` = { columns: [{label, className}],
// cells(band) -> '<td>…</td>' per column, optional rowHeader(band) -> HTML
// stacked under the band name in the row header, key(bands) -> string that
// changes when the extra markup would, after(body) -> draw into the fresh DOM,
// optional hiddenRegions -> region codes left out of the table }.
export function setRowExtras(extras) {
    runtime.rowExtras = extras || null;
    runtime.lastRenderKey = '';
}

// Repaint from cache (no refetch). Without a payload only the extra rows can
// be drawn, so this is a no-op unless row extras are set.
export function refreshMatrix() {
    if (runtime.enabled && currentQth() && (runtime.cache || runtime.rowExtras)) renderMatrix();
}

// Test hooks.
export const __test = {
    cellLabel,
    cellTitle,
    toggleDrillDown,
    surgeStrength,
    hasNormal,
    expectedLine,
    setStyle,
    renderLookCell,
    scheduleDetailRetry,
    DETAIL_RETRY_MS,
    DEFAULT_STYLE,
    STYLES,
    runtime,
    reset() {
        stopPolling();
        runtime.enabled = false;
        runtime.cache = null;
        runtime.cacheKey = '';
        runtime.lastFetchedAt = 0;
        runtime.lastQth = '';
        runtime.lastRenderKey = '';
        runtime.sources = [...DEFAULT_SOURCES];
        runtime.style = DEFAULT_STYLE;
        runtime.rowExtras = null;
        runtime.detailRetryKey = '';
    },
    invalidateCache,
    toggleSource,
    updateDrillDownButton,
    clearDrillDown,
    PANEL_ID,
    BODY_ID,
    SOURCES_KEY,
    STYLE_KEY,
};
