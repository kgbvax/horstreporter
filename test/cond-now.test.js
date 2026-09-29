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

import { verdictText, reportsPair, initCondNow, __test } from '../static/cond-now.js';
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

describe('reportsPair', () => {
    it('shows the region reports against its normal', () => {
        expect(reportsPair(row({ activity_level: 'above', regional_spots: 1234, regional_expected: 812.4 }))).toBe('1,234 / 812');
    });

    it('is blank without a baseline comparison', () => {
        expect(reportsPair(row({ activity_level: 'no_baseline', regional_spots: 10, regional_expected: 0 }))).toBe('');
        expect(reportsPair(row(null))).toBe('');
        expect(reportsPair(row({ activity_level: 'normal' }))).toBe('');
    });
});

describe('row extras', () => {
    it('emits verdict, pair and plot cells for a band', () => {
        getBandRow.mockReturnValueOnce(row({ activity_level: 'below', regional_spots: 3, regional_expected: 9 }));
        const html = __test.extras.cells('20m');
        expect(html).toContain('class="cond-cell-verdict" data-band="20m">quiet</td>');
        expect(html).toContain('class="cond-cell-pair" data-band="20m">3 / 9</td>');
        expect(html).toContain('<canvas class="cond-mini" data-band="20m"');
        expect(__test.extras.columns).toHaveLength(3);
    });

    it('keys on the text cells only, so dots redraw without a rebuild', () => {
        getBandRow.mockReturnValue(row({ activity_level: 'above' }));
        const a = __test.extras.key(['20m', '40m']);
        expect(a).toBe('20m:lively:|40m:lively:');
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
