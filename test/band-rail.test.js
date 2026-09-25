import { describe, expect, it, vi } from 'vitest';

vi.mock('../static/perf.js', () => ({
    startPerfTimer: () => 0,
    endPerfTimer: () => {},
    incrementPerfCounter: () => {},
    isPerfProfilingEnabled: () => false,
}));
vi.mock('../static/map.js', () => ({ map: {} }));

import { bandActivity } from '../static/renderers.js';

const ctx = { minSnrMode: 'all', ssbMinDb: 0, cwMinDb: -15 };

describe('bandActivity (band rail counts + sparkline bins)', () => {
    it('bins live stream spots by receive-stamped age (they carry no `t`)', () => {
        const recvMs = 1_800_000_000_000;
        const live = (band, ageSeconds) => ({ band, snr: 0, ageSeconds, __recvAge: ageSeconds, __recvMs: recvMs });
        const spots = [live('20m', 0), live('20m', 10), live('20m', 890), live('40m', 450)];

        const act = bandActivity(spots, ctx);

        expect(act.get('20m').count).toBe(3);
        // 15-minute window, 15 bins: newest two land in the last bin, the
        // ~15-min-old one in the first; 40m's mid-window spot in the middle.
        expect(act.get('20m').bins[14]).toBe(2);
        expect(act.get('20m').bins[0]).toBe(1);
        expect(act.get('40m').bins[7]).toBe(1);
    });

    it('uses the absolute `t` of timeline spots', () => {
        const spots = [{ band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 - 60 * 7.5 }];
        const act = bandActivity(spots, ctx);
        expect(act.get('20m').bins[14]).toBe(1);
        expect(act.get('20m').bins[7]).toBe(1);
    });

    it('smooths the drawn line with a 3-bin moving average', () => {
        const spots = [{ band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 - 150 }];
        const { bins, line } = bandActivity(spots, ctx).get('20m');
        expect(bins.slice(12)).toEqual([1, 0, 2]);
        expect(line[13]).toBe(1);
        expect(line[14]).toBe(1);
    });

    it('applies the SNR floor', () => {
        const spots = [{ band: '20m', snr: -20, t: 1000 }, { band: '20m', snr: 5, t: 1000 }];
        expect(bandActivity(spots, { ...ctx, minSnrMode: 'ssb' }).get('20m').count).toBe(1);
    });
});
