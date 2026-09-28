import { setRowExtras, refreshMatrix } from './wspr-matrix.js';
import { getBandRow, subscribeBandRows, drawBandMiniPlot } from './band-lab.js';
import { escapeHtml } from './ui-helpers.js';

// cond-now.js — the "Now" view of the Conditions dock. One table row per
// enabled band: verdict, reports / normal, the mini distance-vs-SNR plot and
// the Propagation matrix cells. The matrix table is owned by wspr-matrix.js;
// this module adds the three leading columns through its row-extras hook and
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
// missing data.
export function verdictText(row) {
    if (!row) return '';
    const m = row.metrics;
    if (!m) return row.dxReady ? 'no reports' : '';
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

const COLUMNS = [
    { label: 'Activity', className: 'cond-col-verdict' },
    { label: 'Reports / normal', className: 'cond-col-pair' },
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
    cells(band) {
        const { verdict, pair } = bandTexts(band);
        return `<td class="cond-cell-verdict" data-band="${band}">${escapeHtml(verdict)}</td>` +
            `<td class="cond-cell-pair" data-band="${band}">${escapeHtml(pair)}</td>` +
            `<td class="cond-cell-plot"><canvas class="cond-mini" data-band="${band}" width="96" height="44" aria-hidden="true"></canvas></td>`;
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

export function initCondNow() {
    setRowExtras(extras);
    unsubscribe?.();
    unsubscribe = subscribeBandRows(() => {
        refreshMatrix();
        drawPlots();
    });
}

export const __test = { extras, LEVEL_WORDS, COLUMNS };
