import { setRowExtras, refreshMatrix } from './wspr-matrix.js';
import { getBandRow, getBandNormalRate, subscribeBandRows, drawBandMiniPlot, getLiveArea } from './band-lab.js';
import { areaSummary, bandAreaTag } from './live-area.js';
import { escapeHtml } from './ui-helpers.js';

// cond-now.js — the "Now" view of the Conditions dock. One table row per
// enabled band: the band with its verdict and reports count · factor stacked in the
// row header, the mini distance-vs-SNR plot and the Propagation matrix cells.
// The matrix table is owned by wspr-matrix.js; this module adds the header
// lines and the plot column through its row-extras hook and
// takes the numbers from band-lab.js (dx_conditions + live spots).

// Backend activity_level → the word shown in the row. "normal" is blank on
// purpose: a row says something only when there is something to say.
const LEVEL_WORDS = {
    above: 'lively',
    below: 'quiet',
    low_sample: 'low sample',
    no_baseline: 'no baseline',
};

// Verdict text for a row model (see band-lab.js getBandRow). '' while dx
// metrics have not arrived, so a row never flashes a verdict computed from
// missing data. A band with live reports but no dx entry (only RBN / WSPR, or
// nothing at or above the conditions SNR floor) is "not scored", not "no reports".
export function verdictText(row) {
    if (!row) return '';
    const m = row.metrics;
    if (!m) return row.dxReady ? (row.reports > 0 ? 'not scored' : 'no reports') : '';
    const parts = [];
    const word = LEVEL_WORDS[String(m.activity_level || '')];
    if (word) parts.push(word);
    const reach = String(m.reach_level || '');
    if (reach === 'longer' || reach === 'shorter') parts.push(`${reach} reach`);
    const tag = bandAreaTag(m);
    if (tag) parts.push(tag);
    return parts.join(', ');
}

// Spot count bucketed for a glance: "none", exact up to 999, then 1.2k / 40k /
// 1.2M. Never shows "1000" or "10.0k" (rounding carries into the next unit).
export function formatCount(n) {
    const v = Number(n);
    if (!Number.isFinite(v) || v < 0) return '';
    const r = Math.round(v);
    if (r === 0) return 'none';
    if (r < 1000) return String(r);
    if (r < 10000) {
        const s = (r / 1000).toFixed(1);
        return s === '10.0' ? '10k' : `${s}k`;
    }
    if (r < 1e6) {
        const k = Math.round(r / 1000);
        return k >= 1000 ? '1.0M' : `${k}k`;
    }
    const s = (r / 1e6).toFixed(1);
    return Number(s) >= 10 ? `${Math.round(r / 1e6)}M` : `${s}M`;
}

// Reports against normal as a factor: ×0.4, ×1 (within ±10% of normal),
// ×2.4, ×13, capped at >×99; ×0 below 0.05.
export function formatFactor(ratio) {
    const r = Number(ratio);
    if (!Number.isFinite(r) || r < 0) return '';
    if (r < 0.05) return '×0';
    if (r >= 0.9 && r <= 1.1) return '×1';
    if (r > 99) return '>×99';
    if (r >= 10) return `×${Math.round(r)}`;
    const s = r.toFixed(1);
    return s === '10.0' ? '×10' : `×${s}`;
}

// The numbers behind a row's reports line, or null without a baseline
// comparison. The ratio is the backend's like-for-like activity_ratio (what the
// verdict is derived from), else reports / normal.
function reportsFigures(row) {
    const m = row?.metrics;
    if (!m || m.activity_level === 'no_baseline') return null;
    const spots = Number(m.regional_spots);
    const expected = Number(m.regional_expected);
    if (!Number.isFinite(spots) || !Number.isFinite(expected)) return null;
    let ratio = Number(m.activity_ratio);
    if (m.activity_ratio == null || !Number.isFinite(ratio)) ratio = expected > 0 ? spots / expected : NaN;
    return { spots, expected, ratio };
}

// "1.2k · ×2.4": the region's reports in the window, bucketed, and their
// factor against the normal for this time of day. Blank without a baseline
// comparison; count only when no factor can be computed.
export function reportsPair(row) {
    const f = reportsFigures(row);
    if (!f) return '';
    return [formatCount(f.spots), formatFactor(f.ratio)].filter(Boolean).join(' · ');
}

// Exact numbers for the line's tooltip: "1,234 reports · normal 812 · ×1.52".
export function reportsTitle(row) {
    const f = reportsFigures(row);
    if (!f) return '';
    const parts = [`${Math.round(f.spots).toLocaleString('en-US')} reports`, `normal ${Math.round(f.expected).toLocaleString('en-US')}`];
    if (Number.isFinite(f.ratio) && f.ratio >= 0) parts.push(`×${f.ratio.toFixed(2)}`);
    return parts.join(' · ');
}

// Bands the rail can show; matches the band rail in index.html.
const RAIL_BANDS = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

const COLUMNS = [
    { label: 'Distance / SNR', className: 'cond-col-plot' },
];

function bandTexts(band) {
    const row = getBandRow(band);
    return { verdict: verdictText(row), pair: reportsPair(row), title: reportsTitle(row) };
}

function drawPlots(body = document.getElementById('wspr-matrix-body')) {
    if (!body) return;
    for (const canvas of body.querySelectorAll('canvas.cond-mini')) {
        drawBandMiniPlot(canvas, canvas.getAttribute('data-band'));
    }
}

const extras = {
    columns: COLUMNS,
    // Antarctica adds a mostly empty column; the Now table leaves it out.
    hiddenRegions: ['AN'],
    // Band \n verdict \n count · factor, stacked in the row header so the
    // table stays narrow. Empty lines are left out; the exact numbers are in the
    // reports line's tooltip.
    rowHeader(band) {
        const { verdict, pair, title } = bandTexts(band);
        return (verdict ? `<span class="cond-band-verdict">${escapeHtml(verdict)}</span>` : '') +
            (pair ? `<span class="cond-band-pair" title="${escapeHtml(title)}">${escapeHtml(pair)}</span>` : '');
    },
    cells(band) {
        return `<td class="cond-cell-plot"><canvas class="cond-mini" data-band="${band}" width="96" height="44" aria-hidden="true"></canvas></td>`;
    },
    // Changes when the text cells would; the plots redraw without a rebuild.
    key(bands) {
        return bands.map((b) => {
            const { verdict, pair, title } = bandTexts(b);
            return `${b}:${verdict}:${pair}:${title}`;
        }).join('|');
    },
    after: drawPlots,
};

let unsubscribe = null;

// The one-line note above the table when the area was widened around a sparse
// home square. Hidden otherwise.
function renderAreaLine() {
    const el = document.getElementById('cond-area');
    if (!el) return;
    const text = areaSummary(getLiveArea());
    el.textContent = text;
    el.hidden = !text;
}

// onRailChange: called when a band's normal rate (the rail sparkline's dashed
// line) changed, so the rail can redraw.
export function initCondNow({ onRailChange } = {}) {
    setRowExtras(extras);
    unsubscribe?.();
    const railKeyNow = () => RAIL_BANDS.map((b) => getBandNormalRate(b) ?? '').join('|');
    let railKey = railKeyNow();
    renderAreaLine();
    unsubscribe = subscribeBandRows(() => {
        renderAreaLine();
        refreshMatrix();
        drawPlots();
        const key = railKeyNow();
        if (key !== railKey) {
            railKey = key;
            onRailChange?.();
        }
    });
}

export const __test = { extras, LEVEL_WORDS, COLUMNS };
