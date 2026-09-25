import { describe, it, expect } from 'vitest';
import { bandActivityLabel, bandHeadText, dxNeedsRefresh } from '../static/band-lab.js';

describe('bandActivityLabel (Band stats card label)', () => {
    it('reads a low-volume band far above its own normal as above normal', () => {
        // The old label demoted such a band for carrying under 15% of the
        // busiest band's reports; the new one never compares bands.
        expect(bandActivityLabel({ activity_level: 'above', activity_ratio: 2.27, current_links: 60 }))
            .toBe('above normal 2.27×');
    });

    it('formats normal and below with the ratio', () => {
        expect(bandActivityLabel({ activity_level: 'normal', activity_ratio: 0.98 })).toBe('normal 0.98×');
        expect(bandActivityLabel({ activity_level: 'below', activity_ratio: 0.41 })).toBe('below normal 0.41×');
        // Just under the thresholds: the shown ratio must agree with the level.
        expect(bandActivityLabel({ activity_level: 'normal', activity_ratio: 1.49 })).toBe('normal 1.49×');
        expect(bandActivityLabel({ activity_level: 'normal', activity_ratio: 0.67 })).toBe('normal 0.67×');
        expect(bandActivityLabel({ activity_level: 'above', activity_ratio: 12.6 })).toBe('above normal 13×');
    });

    it('shows sample and baseline guards without a ratio', () => {
        expect(bandActivityLabel({ activity_level: 'low_sample', activity_ratio: 4 })).toBe('low sample');
        expect(bandActivityLabel({ activity_level: 'no_baseline' })).toBe('no baseline');
    });

    it('appends the reach qualifier only when reach is off normal', () => {
        expect(bandActivityLabel({ activity_level: 'above', activity_ratio: 2, reach_level: 'longer' }))
            .toBe('above normal 2.00×, longer reach');
        expect(bandActivityLabel({ activity_level: 'normal', activity_ratio: 1, reach_level: 'shorter' }))
            .toBe('normal 1.00×, shorter reach');
        expect(bandActivityLabel({ activity_level: 'normal', activity_ratio: 1, reach_level: 'typical' }))
            .toBe('normal 1.00×');
    });

    it('is empty until dx metrics arrive or for an unknown level', () => {
        expect(bandActivityLabel(undefined)).toBe('');
        expect(bandActivityLabel({ score: 80 })).toBe('');
        expect(bandActivityLabel({ activity_level: 'weird' })).toBe('');
    });
});

describe('bandHeadText', () => {
    it('shows the bare band while dx metrics are loading', () => {
        expect(bandHeadText('10m', undefined, false)).toBe('10m');
    });
    it('marks a band missing from a loaded dx response as not scored', () => {
        expect(bandHeadText('10m', undefined, true)).toBe('10m: not scored');
    });
    it('prefixes the band to the label with a colon separator', () => {
        expect(bandHeadText('10m', { activity_level: 'above', activity_ratio: 3 }, true)).toBe('10m: above normal 3.00×');
    });
});

describe('dxNeedsRefresh', () => {
    const key = 'JO62|60|0';
    it('refetches when nothing or another key is cached', () => {
        expect(dxNeedsRefresh({ dxCache: null, dxCacheKey: '', lastDxFetchAt: 0 }, key, 1000)).toBe(true);
        expect(dxNeedsRefresh({ dxCache: {}, dxCacheKey: 'JO62|15|0', lastDxFetchAt: 1000 }, key, 1000)).toBe(true);
    });
    it('retries a failed fetch once per interval, not on every update', () => {
        const rt = { dxCache: null, dxCacheKey: '', lastDxFetchAt: 0, dxAttemptKey: key, dxAttemptAt: 1000 };
        expect(dxNeedsRefresh(rt, key, 1500)).toBe(false);
        expect(dxNeedsRefresh(rt, key, 1000 + 15000)).toBe(true);
        // A different key is fetched immediately.
        expect(dxNeedsRefresh(rt, 'JO62|15|0', 1500)).toBe(true);
    });
    it('keeps a same-key response only for the fetch interval', () => {
        const rt = { dxCache: {}, dxCacheKey: key, lastDxFetchAt: 1000 };
        expect(dxNeedsRefresh(rt, key, 1000 + 14999)).toBe(false);
        expect(dxNeedsRefresh(rt, key, 1000 + 15000)).toBe(true);
    });
});
