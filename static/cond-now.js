import { setRowExtras, refreshMatrix } from './wspr-matrix.js';
import { getBandRow, getBandNormalRate, subscribeBandRows, drawBandMiniPlot } from './band-lab.js';
import { escapeHtml } from './ui-helpers.js';

// cond-now.js — the "Now" view of the Conditions dock. One table row per
// enabled band: the band with its verdict and reports / normal stacked in the
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
    return parts.join(', ');
}

// "412 / 380": the region's reports in the window against its normal for
// this time of day. Blank without a baseline comparison.
export function reportsPair(row) {
    const m = row?.metrics;
    if (!m || m.activity_level === 'no_baseline') return '';
    const spots = Number(m.regional_spots);
    const expected = Number(m.regional_expected);
    if (!Number.isFinite(spots) || !Number.isFinite(expected)) return '';
    return `${Math.round(spots).toLocaleString('en-US')} / ${Math.round(expected).toLocaleString('en-US')}`;
}

// Bands the rail can show; matches the band rail in index.html.
const RAIL_BANDS = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

const COLUMNS = [
    { label: 'Distance / SNR', className: 'cond-col-plot' },
];

function bandTexts(band) {
    const row = getBandRow(band);
    return { verdict: verdictText(row), pair: reportsPair(row) };
}

function drawPlots(body = document.getElementById('wspr-matrix-body')) {
    if (!body) return;
    for (const canvas of body.querySelectorAll('canvas.cond-mini')) {
        drawBandMiniPlot(canvas, canvas.getAttribute('data-band'));
    }
}

const extras = {
    columns: COLUMNS,
    // Band \n verdict \n reports / normal, stacked in the row header so the
    // table stays narrow. Empty lines are left out.
    rowHeader(band) {
        const { verdict, pair } = bandTexts(band);
        return (verdict ? `<span class="cond-band-verdict">${escapeHtml(verdict)}</span>` : '') +
            (pair ? `<span class="cond-band-pair">${escapeHtml(pair)}</span>` : '');
    },
    cells(band) {
        return `<td class="cond-cell-plot"><canvas class="cond-mini" data-band="${band}" width="96" height="44" aria-hidden="true"></canvas></td>`;
    },
    // Changes when the text cells would; the plots redraw without a rebuild.
    key(bands) {
        return bands.map((b) => {
            const { verdict, pair } = bandTexts(b);
            return `${b}:${verdict}:${pair}`;
        }).join('|');
    },
    after: drawPlots,
};

let unsubscribe = null;

// onRailChange: called when a band's normal rate (the rail sparkline's dashed
// line) changed, so the rail can redraw.
export function initCondNow({ onRailChange } = {}) {
    setRowExtras(extras);
    unsubscribe?.();
    const railKeyNow = () => RAIL_BANDS.map((b) => getBandNormalRate(b) ?? '').join('|');
    let railKey = railKeyNow();
    unsubscribe = subscribeBandRows(() => {
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
