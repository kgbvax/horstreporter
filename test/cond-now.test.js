import { describe, expect, it, vi } from 'vitest';

// cond-now.js wires wspr-matrix.js and band-lab.js; only its pure formatting
// and the extras contract are exercised here.
vi.mock('../static/wspr-matrix.js', () => ({ setRowExtras: vi.fn(), refreshMatrix: vi.fn() }));
vi.mock('../static/band-lab.js', () => ({
    getBandRow: vi.fn(() => null),
    getBandNormalRate: vi.fn(() => null),
    subscribeBandRows: vi.fn(() => () => {}),
    drawBandMiniPlot: vi.fn(),
    getLiveArea: vi.fn(() => null),
}));

import { verdictText, reportsPair, reportsTitle, formatCount, formatFactor, initCondNow, __test } from '../static/cond-now.js';
import { setRowExtras, refreshMatrix } from '../static/wspr-matrix.js';
import { getBandRow, getBandNormalRate, subscribeBandRows, drawBandMiniPlot, getLiveArea } from '../static/band-lab.js';
import { parseAreaPayload } from '../static/live-area.js';

const row = (metrics, dxReady = true) => ({ band: '20m', metrics, dxReady, reports: 5, minutes: 60 });

describe('verdictText', () => {
    it('relabels above / below normal as lively / quiet', () => {
        expect(verdictText(row({ activity_level: 'above' }))).toBe('lively');
        expect(verdictText(row({ activity_level: 'below' }))).toBe('quiet');
    });

    it('is blank for normal and before dx metrics arrive', () => {
        expect(verdictText(row({ activity_level: 'normal' }))).toBe('');
        expect(verdictText(row(null, false))).toBe('');
        expect(verdictText(null)).toBe('');
    });

    it('says no reports for a silent band, not scored for one with live reports but no dx entry', () => {
        expect(verdictText({ ...row(null, true), reports: 0 })).toBe('no reports');
        expect(verdictText(row(null, true))).toBe('not scored');
    });

    it('adds the reach qualifier, also to an otherwise blank verdict', () => {
        expect(verdictText(row({ activity_level: 'above', reach_level: 'longer' }))).toBe('lively, longer reach');
        expect(verdictText(row({ activity_level: 'normal', reach_level: 'shorter' }))).toBe('shorter reach');
        expect(verdictText(row({ activity_level: 'normal', reach_level: 'typical' }))).toBe('');
    });

    it('marks a band that only has a sample through the widened area', () => {
        expect(verdictText(row({ activity_level: 'normal', area_widened: true }))).toBe('wide area');
        expect(verdictText(row({ activity_level: 'above', reach_level: 'longer', area_widened: true }))).toBe('lively, longer reach, wide area');
        expect(verdictText(row({ activity_level: 'normal', area_widened: false }))).toBe('');
    });

    it('keeps the low sample and no baseline qualifiers', () => {
        expect(verdictText(row({ activity_level: 'low_sample' }))).toBe('low sample');
        expect(verdictText(row({ activity_level: 'no_baseline' }))).toBe('no baseline');
    });
});

describe('formatCount', () => {
    it.each([
        [0, 'none'], [7, '7'], [412, '412'], [999, '999'],
        [1000, '1.0k'], [1034, '1.0k'], [1234, '1.2k'], [9949, '9.9k'], [9960, '10k'],
        [40213, '40k'], [99500, '100k'], [999499, '999k'], [999500, '1.0M'],
        [1200000, '1.2M'], [9960000, '10M'], [25000000, '25M'],
    ])('%s → %s', (n, text) => {
        expect(formatCount(n)).toBe(text);
    });

    it('is blank for missing or negative input', () => {
        expect(formatCount(NaN)).toBe('');
        expect(formatCount(undefined)).toBe('');
        expect(formatCount(-3)).toBe('');
    });
});

describe('formatFactor', () => {
    it.each([
        [0, '×0'], [0.04, '×0'], [0.05, '×0.1'], [0.12, '×0.1'], [0.41, '×0.4'], [0.89, '×0.9'],
        [0.9, '×1'], [0.95, '×1'], [1.1, '×1'], [1.11, '×1.1'], [2.37, '×2.4'],
        [9.96, '×10'], [12.6, '×13'], [99, '×99'], [150, '>×99'],
    ])('%s → %s', (ratio, text) => {
        expect(formatFactor(ratio)).toBe(text);
    });

    it('is blank for missing or negative input', () => {
        expect(formatFactor(NaN)).toBe('');
        expect(formatFactor(-1)).toBe('');
    });
});

describe('reportsPair', () => {
    it('shows the bucketed region reports and their factor against normal', () => {
        expect(reportsPair(row({ activity_level: 'above', regional_spots: 1234, regional_expected: 812.4, activity_ratio: 2.37 }))).toBe('1.2k · ×2.4');
        expect(reportsPair(row({ activity_level: 'below', regional_spots: 0, regional_expected: 9, activity_ratio: 0 }))).toBe('none · ×0');
    });

    it('falls back to reports / normal without an activity ratio', () => {
        expect(reportsPair(row({ activity_level: 'above', regional_spots: 30, regional_expected: 12 }))).toBe('30 · ×2.5');
    });

    it('shows the count alone when no factor can be computed', () => {
        expect(reportsPair(row({ activity_level: 'low_sample', regional_spots: 5, regional_expected: 0 }))).toBe('5');
    });

    it('is blank without a baseline comparison', () => {
        expect(reportsPair(row({ activity_level: 'no_baseline', regional_spots: 10, regional_expected: 0 }))).toBe('');
        expect(reportsPair(row(null))).toBe('');
        expect(reportsPair(row({ activity_level: 'normal' }))).toBe('');
    });
});

describe('reportsTitle', () => {
    it('keeps the exact numbers for the tooltip', () => {
        expect(reportsTitle(row({ activity_level: 'above', regional_spots: 1234, regional_expected: 812.4, activity_ratio: 1.5191 })))
            .toBe('1,234 reports · normal 812 · ×1.52');
        expect(reportsTitle(row(null))).toBe('');
    });
});

describe('row extras', () => {
    it('stacks verdict and pair under the band name, and adds the plot cell', () => {
        getBandRow.mockReturnValueOnce(row({ activity_level: 'below', regional_spots: 3, regional_expected: 9, activity_ratio: 0.33 }));
        const header = __test.extras.rowHeader('20m');
        expect(header).toBe('<span class="cond-band-verdict">quiet</span><span class="cond-band-pair" title="3 reports · normal 9 · ×0.33">3 · ×0.3</span>');
        expect(__test.extras.cells('20m')).toContain('<canvas class="cond-mini" data-band="20m"');
        expect(__test.extras.columns).toHaveLength(1);
    });

    it('leaves out empty header lines', () => {
        getBandRow.mockReturnValueOnce(row({ activity_level: 'normal' }));
        expect(__test.extras.rowHeader('20m')).toBe('');
    });

    it('keys on the text cells only, so dots redraw without a rebuild', () => {
        getBandRow.mockReturnValue(row({ activity_level: 'above' }));
        const a = __test.extras.key(['20m', '40m']);
        expect(a).toBe('20m:lively::|40m:lively::');
        getBandRow.mockReturnValue(row({ activity_level: 'below' }));
        expect(__test.extras.key(['20m', '40m'])).not.toBe(a);
        getBandRow.mockReturnValue(null);
    });

    it('registers with the matrix and repaints on every row update', () => {
        document.body.innerHTML = '<div id="wspr-matrix-body"><canvas class="cond-mini" data-band="17m"></canvas></div>';
        initCondNow();
        expect(setRowExtras).toHaveBeenCalledWith(__test.extras);
        const onRows = subscribeBandRows.mock.calls.at(-1)[0];
        onRows();
        expect(refreshMatrix).toHaveBeenCalled();
        expect(drawBandMiniPlot).toHaveBeenCalledWith(expect.any(HTMLCanvasElement), '17m');
    });

    it('shows the widened area above the table and hides it otherwise', () => {
        document.body.innerHTML = '<div id="cond-area" hidden></div><div id="wspr-matrix-body"></div>';
        const line = document.getElementById('cond-area');

        getLiveArea.mockReturnValue(parseAreaPayload({ centre: 'FN76', base_radius: 0, radius: 2, widened: true }));
        initCondNow();
        expect(line.hidden).toBe(false);
        expect(line.textContent).toBe('Area: 5×5 squares around FN76, widened for a fuller sample.');

        // A dense area: nothing to say, so the line goes away on the next update.
        getLiveArea.mockReturnValue(parseAreaPayload({ centre: 'JO32', base_radius: 0, radius: 0, widened: false }));
        subscribeBandRows.mock.calls.at(-1)[0]();
        expect(line.hidden).toBe(true);
        expect(line.textContent).toBe('');

        getLiveArea.mockReturnValue(null);
        subscribeBandRows.mock.calls.at(-1)[0]();
        expect(line.hidden).toBe(true);
    });

    it('tells the rail only when a normal rate changed', () => {
        const onRailChange = vi.fn();
        initCondNow({ onRailChange });
        const onRows = subscribeBandRows.mock.calls.at(-1)[0];
        onRows();
        expect(onRailChange).toHaveBeenCalledTimes(0); // nothing known yet, nothing changed
        getBandNormalRate.mockImplementation((b) => (b === '20m' ? 2.5 : null));
        onRows();
        onRows();
        expect(onRailChange).toHaveBeenCalledTimes(1);
        getBandNormalRate.mockReturnValue(null);
    });
});

describe('cond-now hidden regions', () => {
    it('leaves Antarctica and Hawaii out of the Now table', async () => {
        const { __test } = await import('../static/cond-now.js');
        expect(__test.extras.hiddenRegions).toEqual(['AN', 'KH6']);
    });
});
