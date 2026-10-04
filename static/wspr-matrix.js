import { state } from './state.js';
import { WSPR_REGIONS, bandColors, getEnabledBands, getMinSnrMode } from './utils.js';
import { escapeHtml } from './ui-helpers.js';

// wspr-matrix.js — the unified Propagation panel: band × region propagation-
// intelligence matrix over ALL ingest sources (WSPR, PSKReporter FT8/FT4,
// RBN, DX cluster). Polls /api/prop_intel/v2 (the multi-source contract;
// v1 stays frozen for the horstapp widgets). Renders per-cell activity as a
// heatmap colormap — switchable between viridis and inferno (chip row) —
// where the anomaly is a glyph: up-chevrons on viridis, a warning ring on
// inferno (tmp/prop-vis-round3.html, decision 02: one-channel fill + glyph).
// A third look, Disc (docs/ideation/2026-10-04-now-matrix-visualization-
// ideation.html option 4), drops the fill: a fixed ring for the normal at this
// hour from the operator's area and a disc whose area is the PSKReporter reports
// now as a share of it (see discSvg).
// Both looks keep SSB/CW flags and the rising slope. Each look has a ramp per
// theme: the reference colormaps on the dark theme (chips brighten with
// activity), page-anchored reversals on the light theme (chips darken with
// activity) — see rampStops().
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

// Switchable fill "look" (tmp/prop-vis-round3.html, decision 02: one-channel
// fill + glyph). Both styles are data-colored heatmaps; the ramp direction
// follows the theme (see rampStops) and the numeral ink flips per chip.
const STYLES = [
    { key: 'viridis', label: 'Viridis', title: 'Viridis colors, surges shown as chevrons' },
    { key: 'inferno', label: 'Inferno', title: 'Inferno colors, surges shown as rings' },
    { key: 'disc', label: 'Disc', title: 'Discs: reports now over a ring for the normal at this hour' },
];
const ALL_STYLES = STYLES.map((s) => s.key);
// Dark theme: 9-stop perceptually-uniform maps (matplotlib reference
// samples), sparse → peak. Their dark low end sinks into the dark panel and
// the bright high end stands off it, so visual weight tracks activity.
const VIRIDIS_STOPS = ['#440154', '#482677', '#3f4788', '#31688e', '#26828e', '#1f9e89', '#35b779', '#6ece58', '#fde725'];
const INFERNO_STOPS = ['#000004', '#1b0c41', '#4a0c6b', '#781c6d', '#a52c60', '#cf4446', '#ed6925', '#fb9b06', '#fcffa4'];
// Light theme: the same colormaps reversed so lightness runs page-white →
// dark (a sequential ramp on a light surface must darken with magnitude; the
// reference direction puts the heaviest, near-black chip on the *least*
// active path). The three sparse stops are the reversed map's bright end
// blended toward the page (30/55/78% and 65/62/85% of the reference color)
// so 1-2 spots read as a faint tint instead of saturated yellow / cream, and
// inferno drops its #000004 tail so the peak is deep purple, not black. Hue
// order is unchanged, so both looks stay recognisable across themes.
const VIRIDIS_LIGHT_STOPS = ['#fef8be', '#afe4a3', '#61c796', '#1f9e89', '#26828e', '#31688e', '#3f4788', '#482677', '#440154'];
const INFERNO_LIGHT_STOPS = ['#fdffc4', '#fdc165', '#f08046', '#cf4446', '#a52c60', '#781c6d', '#4a0c6b', '#1b0c41'];

// Current theme as the render sees it (body[data-theme], 'light' unless dark).
function currentTheme() {
    return document.body?.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

// Stop table for a look in a theme (unknown/legacy keys — e.g. a stored
// 'aqua' — fall back to the viridis default).
function rampStops(style, theme) {
    if (theme === 'dark') return style === 'inferno' ? INFERNO_STOPS : VIRIDIS_STOPS;
    return style === 'inferno' ? INFERNO_LIGHT_STOPS : VIRIDIS_LIGHT_STOPS;
}

function hexToRgb(h) {
    return [parseInt(h.slice(1, 3), 16), parseInt(h.slice(3, 5), 16), parseInt(h.slice(5, 7), 16)];
}

// Interpolate a stop table at t in 0..1 → [r,g,b]. sqrt() spreads the low end
// so 1-2 spots don't collapse onto the dead shade.
function rampAt(stops, t) {
    t = Math.sqrt(Math.max(0, Math.min(1, t)));
    const n = stops.length - 1;
    const i = Math.min(n - 1, Math.floor(t * n));
    const a = hexToRgb(stops[i]);
    const b = hexToRgb(stops[i + 1]);
    const f = t * n - i;
    return a.map((v, k) => Math.round(v + (b[k] - v) * f));
}

// Per-style, per-theme chip fill.
function styleFill(style, intensity, theme = currentTheme()) {
    return rampAt(rampStops(style, theme), intensity);
}

// Numerals flip ink at the luminance where white/black cross (~0.179); pure
// black/white inks keep >= 4.58:1 on every shade either side of the switch —
// a mid-luminance ink would fail AA with both, which is why the switch uses
// #000 rather than the softer #1a1a1a used by the flag badges.
const WHITE_INK = 'rgb(255, 255, 255)';
const BLACK_INK = 'rgb(0, 0, 0)';

function rgbLuminance([r, g, b]) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

// Pick the numeral ink for a chip color: whichever of white/black is AA-safe.
function cellInk(rgb) {
    const l = typeof rgb === 'string'
        ? rgbLuminance(rgb.match(/\d+/g).map(Number))
        : rgbLuminance(rgb);
    return l <= 0.179 ? WHITE_INK : BLACK_INK;
}

// Heat-style anomaly glyphs (round-3 decision: shape carries the anomaly).
// v2 atypical only fires on surges (z >= threshold), so glyphs only ever go
// up / amber; "strong" doubles the mark for z >= 4 or multi-source agreement.
function surgeStrength(cell) {
    if (!cell.atypical) return 0;
    const multi = (cell.atypical_agreement ?? 0) >= 0.5;
    const z = Number(cell.atypical.z_score) || 0;
    return (z >= 4 || multi) ? 2 : 1;
}

function chevSvg(ink) {
    return `<svg width="9" height="5" viewBox="0 0 9 5" style="display:block" aria-hidden="true"><polygon points="0,4.5 4.5,0.5 9,4.5" fill="${ink}"/></svg>`;
}

function chevronGlyph(strong, ink) {
    const svgs = chevSvg(ink) + (strong ? chevSvg(ink) : '');
    return `<span class="wspr-chev">${svgs}</span>`;
}

// Inferno surge ring. Dark theme: amber, which stands off the dark and red
// shades that dominate that ramp. Light theme: the chip's own numeral ink —
// the light ramp runs cream → orange → red, where amber vanishes, and the
// ink is AA-safe on every shade by construction (see cellInk).
const RING_COLOR = '#f5b83d';

function ringColor(theme, ink) {
    return theme === 'dark' ? RING_COLOR : ink;
}

function ringShadow(strong, color = RING_COLOR) {
    const w = strong ? 3 : 2;
    return `inset 0 0 0 ${w}px ${color}`;
}

// Mode badge shows only the top mode: SSB beats CW (if SSB is open, phone
// wins the band, so the CW badge is dropped for glanceability). Full detail
// stays in the cell tooltip.
function topModeBadges(cell) {
    if (cell.ssb_open) return '<span class="wspr-badge wspr-badge-ssb">S</span>';
    if (cell.cw_open) return '<span class="wspr-badge wspr-badge-cw">C</span>';
    return '';
}

// --- Disc look ---------------------------------------------------------------
// One 40x40 SVG per cell, comparing the cell with its own normal. The ring is
// the normal for this hour from the operator's area (the same size in every
// cell); the disc is the PSKReporter reports now, its AREA proportional to
// their share of that normal (expected_spots / expected, clamped to 1/16..4).
// Disc inside the ring = quieter than usual, outside = livelier. Filled = SSB
// open, hollow = CW only, faint = under both floors; the band's rail colour.
// A cell with reports but nothing to compare (no PSKReporter normal, or only
// WSPR / RBN reports) is a small dashed dot. The ring alone is "usually open
// at this hour, nothing now" (a silent cell). A surge (z-score against the
// same normal, backend) adds an orange outline; rising a small triangle.
const DISC_BOX = 40;
const DISC_RING_R = 8.5;
const DISC_R_MIN = 2.5;
const DISC_RATIO_MIN = 1 / 16;
const DISC_RATIO_MAX = 4;
const DISC_DOT_R = 3.5;
const DISC_SURGE_LIGHT = '#c2410c';
const DISC_SURGE_DARK = RING_COLOR;

// The cell carries a from-here normal (expected and the live count it compares).
function hasNormal(cell) {
    return cell?.expected != null && cell?.expected_spots != null &&
        Number.isFinite(Number(cell.expected)) && Number.isFinite(Number(cell.expected_spots));
}

// Live PSKReporter reports over the normal, for labels; a normal under one
// report counts as one.
function normalRatio(cell) {
    return Number(cell.expected_spots) / Math.max(1, Number(cell.expected));
}

// The share the disc draws: both counts padded by DISC_PRIOR reports, so one
// or two stray reports on a path that is normally empty do not fill the cell,
// while busy cells are unchanged (771 / 4,515 stays x0.17).
const DISC_PRIOR = 2;
function drawRatio(cell) {
    return (Number(cell.expected_spots) + DISC_PRIOR) / (Number(cell.expected) + DISC_PRIOR);
}

// Disc radius for a share of normal: ratio 1 fills the ring exactly.
function discRadiusForRatio(ratio) {
    const v = Number(ratio);
    if (!Number.isFinite(v) || v <= 0) return 0;
    const t = Math.min(DISC_RATIO_MAX, Math.max(DISC_RATIO_MIN, v));
    return Math.max(DISC_R_MIN, DISC_RING_R * Math.sqrt(t));
}

// "×0.17", "×1.5", "×12" for labels.
function formatRatio(ratio) {
    const v = Number(ratio);
    if (!Number.isFinite(v) || v < 0) return '';
    if (v < 1) return `×${v.toFixed(2)}`;
    if (v < 10) return `×${v.toFixed(1)}`;
    return `×${Math.round(v)}`;
}

// The band's rail colour; on the dark theme blended 35% toward white so the
// dark blues, purples and reds of 40m / 80m / 160m stay visible on the panel.
function discInk(band, theme) {
    let hex = bandColors[band] || bandColors.all || '#555555';
    if (/^#[0-9a-f]{3}$/i.test(hex)) hex = `#${hex[1]}${hex[1]}${hex[2]}${hex[2]}${hex[3]}${hex[3]}`;
    const rgb = hexToRgb(hex);
    if (theme !== 'dark') return `rgb(${rgb.join(', ')})`;
    return `rgb(${rgb.map((v) => Math.round(v + (255 - v) * 0.35)).join(', ')})`;
}

// "about 640" for labels: whole numbers, "<1" under one.
function formatExpected(e) {
    const v = Number(e);
    if (!Number.isFinite(v) || v < 0) return '';
    return v < 1 ? '<1' : Math.round(v).toLocaleString('en-US');
}

// One circle in the cell's mode style: filled = SSB, hollow = CW, faint = neither.
function modeCircle(c, r, cell, ink, dashed) {
    const dash = dashed ? ' stroke-dasharray="2 1.5"' : '';
    if (cell.ssb_open) {
        return `<circle cx="${c}" cy="${c}" r="${r.toFixed(1)}" fill="${ink}" fill-opacity="0.8" stroke="var(--text-color)" stroke-opacity="${dashed ? 0.7 : 0.3}" stroke-width="1"${dash}/>`;
    }
    if (cell.cw_open) {
        return `<circle cx="${c}" cy="${c}" r="${r.toFixed(1)}" fill="none" stroke="${ink}" stroke-width="2.2"${dash}/>`;
    }
    return `<circle cx="${c}" cy="${c}" r="${r.toFixed(1)}" fill="${ink}" fill-opacity="0.28" stroke="${ink}" stroke-opacity="0.6" stroke-width="1"${dash}/>`;
}

function discSvg(band, cell, theme) {
    const c = DISC_BOX / 2;
    let svg = `<svg class="wspr-disc" viewBox="0 0 ${DISC_BOX} ${DISC_BOX}" aria-hidden="true" focusable="false">`;
    const normal = hasNormal(cell);
    const n = Number(cell?.spot_count) || 0;
    let r = 0;
    if (n > 0) {
        const ink = discInk(band, theme);
        const compared = normal && Number(cell.expected_spots) > 0;
        r = compared ? discRadiusForRatio(drawRatio(cell)) : DISC_DOT_R;
        svg += modeCircle(c, r, cell, ink, !compared);
        const strength = surgeStrength(cell);
        if (strength > 0) {
            const surge = theme === 'dark' ? DISC_SURGE_DARK : DISC_SURGE_LIGHT;
            for (let i = 0; i < strength; i++) {
                svg += `<circle cx="${c}" cy="${c}" r="${(r + 2.5 + i * 2.2).toFixed(1)}" fill="none" stroke="${surge}" stroke-width="1.6"/>`;
            }
        }
    }
    if (normal) {
        // Last, so the normal stays readable on top of a bigger disc: a page-
        // coloured halo under a text-coloured line holds up on any band colour.
        svg += `<circle cx="${c}" cy="${c}" r="${DISC_RING_R}" fill="none" stroke="var(--bg-color)" stroke-opacity="0.85" stroke-width="3.6"/>` +
            `<circle cx="${c}" cy="${c}" r="${DISC_RING_R}" fill="none" stroke="var(--text-color)" stroke-opacity="0.9" stroke-width="1.3"/>`;
    }
    if (n > 0 && cell.rising) {
        const y = c - Math.max(r, normal ? DISC_RING_R : 0) - 2.5;
        svg += `<path d="M${c - 3.5} ${y.toFixed(1)} l3.5 -5 l3.5 5z" fill="var(--text-color)" fill-opacity="0.9"/>`;
    }
    return svg + '</svg>';
}

// Inline key for the Disc look's legend: quieter, livelier, ring alone, dot.
function discKeySvg() {
    const ring = 'fill="none" stroke="currentColor" stroke-opacity="0.9" stroke-width="1.2"';
    return '<svg class="wspr-disc-key" width="96" height="22" viewBox="0 0 96 22" aria-hidden="true" focusable="false">' +
        `<circle cx="11" cy="11" r="4" fill="currentColor" fill-opacity="0.6"/><circle cx="11" cy="11" r="7" ${ring}/>` +
        `<circle cx="36" cy="11" r="10" fill="currentColor" fill-opacity="0.6"/><circle cx="36" cy="11" r="7" ${ring}/>` +
        `<circle cx="61" cy="11" r="7" ${ring}/>` +
        '<circle cx="85" cy="11" r="3.5" fill="currentColor" fill-opacity="0.6" stroke="currentColor" stroke-dasharray="2 1.5"/></svg>';
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
    style: 'viridis',
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
    // Per-cell normals and "usually busy, none now" cells, for the Disc look.
    // The heat looks drop the silent cells, so every look shares one payload.
    params.set('silent', '1');
    if (minSnrMode === 'ssb') params.set('ssb_min_db', ssbMinDb);
    if (minSnrMode === 'cw') params.set('cw_min_db', cwMinDb);

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

// Switch the fill look. Style is render-only (same payload), so this resets
// the render fingerprint and repaints from cache — no refetch.
function setStyle(key) {
    if (!ALL_STYLES.includes(key) || runtime.style === key) return;
    runtime.style = key;
    localStorage.setItem(STYLE_KEY, key);
    runtime.lastRenderKey = '';
    renderMatrix();
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
        `<div class="wspr-chip-group" role="group" aria-label="Color scale">${styleChips}</div>` +
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
    const disc = runtime.style === 'disc';
    const cells = allCells.filter((c) => (!enabled || enabled.has(c.band)) && (disc || !c.silent));
    if (cells.length === 0 && !(runtime.rowExtras && enabled && enabled.size > 0)) {
        runtime.lastRenderKey = '';
        const empty = allCells.length > 0
            ? 'No paths open on the enabled bands.'
            : 'No paths open in the current window.';
        const focus = captureFocus(body);
        body.innerHTML = renderSourceChips() +
            `<div class="text-muted small">${empty}</div>` +
            legendHtml();
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

    // Fingerprint for skip-rebuild (theme included: a toggle re-shades chips;
    // drill-down included: it sets aria-selected and the tab stop; region
    // names included: they are in the cell names).
    const theme = currentTheme();
    const regionNames = (data && data.region_names) || {};
    const renderKey = `${theme}|${runtime.style}|${runtime.sources.join(',')}` +
        `|x:${extras ? extras.key(activeBands) : ''}` +
        `|${state.drillDownBand}×${state.drillDownRegion}` +
        `|${WSPR_REGIONS.map((r) => regionNames[r] || '').join(',')}|${activeBands
        .map((b) => `${b}:${Array.from(matrix.get(b).entries()).sort().map(([r, c]) =>
            `${r}${c.spot_count}${c.ssb_open ? 'S' : ''}${c.cw_open ? 'C' : ''}${c.rising ? 'R' : ''}` +
            `${c.atypical ? `${c.atypical.z_score}~${c.atypical.confidence}` : ''}` +
            `${runtime.style === 'disc' ? `E${c.expected ?? ''}/${c.expected_spots ?? ''}${c.silent ? 'Z' : ''}` : ''}` +
            `${(c.active_sources || []).join('+')}@${c.open_agreement ?? ''}~${c.atypical_agreement ?? ''}`
        ).join('')}`)
        .join('|')}`;
    if (renderKey === runtime.lastRenderKey) return;
    runtime.lastRenderKey = renderKey;

    const maxCount = Math.max(...cells.map((c) => c.spot_count || 0), 1);
    const extraHead = extras ? extras.columns.map((c) => `<th scope="col" role="columnheader" class="${c.className || ''}">${c.label}</th>`).join('') : '';

    let html = renderSourceChips();
    html += '<table class="wspr-matrix-table" role="grid" aria-label="Propagation by band and region">' +
        '<thead><tr role="row"><th scope="col" role="columnheader"><span class="wspr-sr-only">Band</span></th>' + extraHead;
    for (const region of regions) {
        html += `<th scope="col" role="columnheader" title="${escapeHtml(regionNames[region] || region)}">${region}</th>`;
    }
    html += '</tr></thead><tbody>';

    for (const band of activeBands) {
        const bandMap = matrix.get(band);
        const color = bandColors[band] || bandColors.all || '#555';
        html += `<tr role="row"><th scope="row" role="rowheader" class="wspr-matrix-band" style="border-left: 3px solid ${color}">${band}${extras?.rowHeader ? extras.rowHeader(band) : ''}</th>`;
        if (extras) html += extras.cells(band);
        for (const region of regions) {
            const cell = bandMap.get(region);
            html += renderCell(band, region, cell, maxCount, theme, regionNames);
        }
        html += '</tr>';
    }
    html += '</tbody></table>';
    html += legendHtml(theme);

    const focus = captureFocus(body);
    body.innerHTML = html;

    attachSourceChipHandlers(body);
    const table = body.querySelector('.wspr-matrix-table');
    attachGridHandlers(table);
    const drill = findCell(table, state.drillDownBand, state.drillDownRegion);
    const first = table.querySelector('.wspr-matrix-cell');
    if (drill || first) setRovingCell(drill || first);
    restoreFocus(body, focus);
    extras?.after(body);
}

// "20m to Japan: 1,234 spots" (full region name from the payload's
// region_names, code as fallback).
function pathSummary(band, region, cell, regionNames = {}) {
    const count = Number(cell.spot_count) || 0;
    return `${band} to ${regionNames[region] || region}: ${count.toLocaleString('en-US')} ${count === 1 ? 'spot' : 'spots'}`;
}

// "PSKReporter: 771 now, normal about 4,515 at this hour (×0.17)" from the
// cell's from-here normal.
function expectedLine(cell) {
    const now = Number(cell.expected_spots) || 0;
    if (Number(cell.expected) < 1) return `PSKReporter: ${now.toLocaleString('en-US')} now, normally under 1 at this hour`;
    return `PSKReporter: ${now.toLocaleString('en-US')} now, normal about ${formatExpected(cell.expected)} at this hour (${formatRatio(normalRatio(cell))})`;
}

// Accessible name for a data cell: path, spot count and the marks it shows
// (top mode badge, rising arrow, surge glyph or ring).
function cellLabel(band, region, cell, regionNames = {}) {
    const parts = [pathSummary(band, region, cell, regionNames)];
    if (cell.ssb_open) parts.push('SSB open');
    else if (cell.cw_open) parts.push('CW open');
    if (cell.rising) parts.push('rising');
    if (hasNormal(cell)) parts.push(Number(cell.expected) < 1 ? 'not normally open at this hour' : `${formatRatio(normalRatio(cell))} normal`);
    const surge = surgeStrength(cell);
    if (surge === 2) parts.push('strong surge');
    else if (surge === 1) parts.push('surge');
    return parts.join(', ');
}

// Disc look, no live spots: the ring alone ("usually busy at this hour, none
// now") when the cell has a normal; inert like any empty cell.
function renderSilentDiscCell(band, region, cell, regionNames) {
    const name = regionNames[region] || region;
    const text = `${band} to ${name}: none now, usually about ${formatExpected(cell.expected)} PSKReporter reports at this hour`;
    return `<td role="gridcell" class="wspr-matrix-cell-empty wspr-disc-silent" title="${escapeHtml(text)}" aria-label="${escapeHtml(text)}" data-band="${band}" data-region="${region}">${discSvg(band, cell, currentTheme())}</td>`;
}

function renderCell(band, region, cell, maxCount, theme, regionNames = runtime.cache?.region_names || {}) {
    if (runtime.style === 'disc' && cell?.silent && Number(cell.expected) > 0) {
        return renderSilentDiscCell(band, region, cell, regionNames);
    }
    if (!cell || cell.spot_count === 0) {
        // Nothing to drill into: a plain grid cell, outside the tab order.
        return `<td role="gridcell" class="wspr-matrix-cell-empty" data-band="${band}" data-region="${region}"></td>`;
    }
    const style = runtime.style;
    const intensity = Math.min(1, cell.spot_count / maxCount);
    const bgRgb = styleFill(style, intensity, theme);
    const bg = `rgb(${bgRgb.join(', ')})`;
    const ink = cellInk(bgRgb);
    let badges = topModeBadges(cell);
    if (cell.rising) badges += '<span class="wspr-badge wspr-badge-rising">&uarr;</span>';
    let atypicalMark = '';
    let ring = '';
    let surgeLine = '';
    if (cell.atypical) {
        const conf = Math.round(Math.max(0, Math.min(1, Number(cell.atypical.confidence ?? 0))) * 100);
        const multi = (cell.atypical_agreement ?? 0) >= 0.5;
        surgeLine = `Surge: z=${cell.atypical.z_score}, confidence ${conf}%`;
        const tip = `${surgeLine}${multi ? `, ${Math.round((cell.atypical_agreement ?? 0) * 100)}% of sources agree` : ''}`;
        // Heat styles speak in geometry, not badges (round-3 decision 02).
        // The v2 backend only flags surges, so glyphs only point up.
        const strong = surgeStrength(cell) === 2;
        if (style === 'inferno') {
            ring = ringShadow(strong, ringColor(theme, ink));
        } else if (style !== 'disc') {
            atypicalMark = `<span title="${tip}">${chevronGlyph(strong, ink)}</span>`;
        }
    }
    const titleParts = [pathSummary(band, region, cell, regionNames)];
    if (cell.ssb_open) titleParts.push('SSB open');
    if (cell.cw_open) titleParts.push('CW open');
    if (cell.rising) titleParts.push('Rising');
    if (surgeLine) titleParts.push(surgeLine);
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
    const selected = state.drillDownBand === band && state.drillDownRegion === region;
    const label = escapeHtml(cellLabel(band, region, cell, regionNames));
    const title = escapeHtml(titleParts.join('\n'));
    if (style === 'disc') {
        return `<td role="gridcell" class="wspr-matrix-cell wspr-disc-cell" title="${title}" aria-label="${label}" aria-selected="${selected}" tabindex="-1" data-band="${band}" data-region="${region}">${discSvg(band, cell, theme)}</td>`;
    }
    const styleAttr = `background: ${bg}; color: ${ink}${ring ? `; box-shadow: ${ring}` : ''}`;
    return `<td role="gridcell" class="wspr-matrix-cell" style="${styleAttr}" title="${title}" aria-label="${label}" aria-selected="${selected}" tabindex="-1" data-band="${band}" data-region="${region}">${runtime.rowExtras?.hideCounts ? '' : cell.spot_count}${badges}${atypicalMark}</td>`;
}

function legendHtml(theme = currentTheme()) {
    const style = runtime.style;
    if (style === 'disc') {
        return `<div class="wspr-matrix-legend small text-muted">${discKeySvg()} ring = normal for this hour from your area; disc = PSKReporter reports now against it (inside = quieter, outside = livelier, up to 4&times;); ring alone = usually open, nothing now; dashed dot = reports but no normal to compare. Hollow = CW only, faint = below both thresholds, &#9650; rising, orange outline = surge.</div>`;
    }
    const stops = rampStops(style, theme);
    // The ring swatch's border follows the theme in style.css (amber on dark,
    // currentColor on light — the same ink rule the chips use).
    const anomaly = style === 'inferno'
        ? '<span class="wspr-ring-swatch"></span>=surge ring (thicker = strong / multi-source)'
        : `<span class="wspr-chev">${chevSvg('currentColor')}</span>=surge <span class="wspr-chev">${chevSvg('currentColor')}${chevSvg('currentColor')}</span>=strong / multi-source`;
    return `<div class="wspr-matrix-legend small text-muted"><span class="wspr-heat-scale" style="background: linear-gradient(90deg, ${stops.join(', ')})" aria-hidden="true"></span>=spots: few &rarr; many <span class="wspr-badge wspr-badge-ssb">S</span>=SSB <span class="wspr-badge wspr-badge-cw">C</span>=CW (top mode) <span class="wspr-badge wspr-badge-rising">&uarr;</span>=rising ${anomaly}</div>`;
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
// never focusable, so every move skips them.
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

// Data cell in `row` whose column is closest to `col` (ties go left).
function closestInRow(row, col) {
    let best = null;
    for (const td of dataCells(row)) {
        if (!best || Math.abs(td.cellIndex - col) < Math.abs(best.cellIndex - col)) best = td;
    }
    return best;
}

function cellInRowStep(td, dir) {
    const cells = dataCells(td.parentElement);
    return cells[cells.indexOf(td) + dir] || null;
}

// Up/Down keep the column when a later row has a data cell there; otherwise
// they land on the closest data cell of the next row that has any.
function cellInColumnStep(table, td, dir) {
    const rows = Array.from(table.tBodies[0]?.rows || []);
    const start = rows.indexOf(td.parentElement);
    const col = td.cellIndex;
    for (let i = start + dir; i >= 0 && i < rows.length; i += dir) {
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
            const cells = e.ctrlKey ? Array.from(table.querySelectorAll('.wspr-matrix-cell')) : dataCells(td.parentElement);
            next = cells[0] || null;
            break;
        }
        case 'End': {
            const cells = e.ctrlKey ? Array.from(table.querySelectorAll('.wspr-matrix-cell')) : dataCells(td.parentElement);
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
// optional hiddenRegions -> region codes left out of the table, optional
// hideCounts -> cells show colour and badges only (the count stays in the
// tooltip and the accessible name) }.
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
    cellInk,
    rgbLuminance,
    topModeBadges,
    renderCell,
    cellLabel,
    toggleDrillDown,
    styleFill,
    rampAt,
    rampStops,
    legendHtml,
    surgeStrength,
    chevronGlyph,
    ringShadow,
    ringColor,
    setStyle,
    discRadiusForRatio,
    normalRatio,
    drawRatio,
    hasNormal,
    formatRatio,
    discInk,
    discSvg,
    formatExpected,
    expectedLine,
    VIRIDIS_STOPS,
    INFERNO_STOPS,
    VIRIDIS_LIGHT_STOPS,
    INFERNO_LIGHT_STOPS,
    RING_COLOR,
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
        runtime.style = 'viridis';
        runtime.rowExtras = null;
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
