import { describe, expect, it } from 'vitest';
import {
    gridSnrOpacity,
    topQuartileMean
} from '../static/utils.js';

describe('topQuartileMean', () => {
    it('uses the single best spot for tiny squares (k = ceil(n/4))', () => {
        expect(topQuartileMean([12])).toBe(12);
        expect(topQuartileMean([10, 4])).toBe(10);
    });

    it('takes the mean of the strongest quarter once there are 8+ spots', () => {
        // best 1 of 4
        expect(topQuartileMean([10, 0, 0, 0])).toBe(10);
        // best 2 of 8: 12, 8 -> 10
        expect(topQuartileMean([12, 8, -5, -5, -5, -5, -5, -5])).toBe(10);
    });

    it('keeps a single strong outlier from dominating a weak square', () => {
        const snrs = [12, ...Array(20).fill(-8)];
        // best 6: 12, -8 x5 -> ~-4.67, still below 0 dB
        const score = topQuartileMean(snrs);
        expect(score).toBeCloseTo(-28 / 6, 5);
        expect(score).toBeLessThan(0);
    });

    it('ignores non-finite values and returns NaN for empty input', () => {
        expect(topQuartileMean([])).toBeNaN();
        expect(topQuartileMean(undefined)).toBeNaN();
        expect(topQuartileMean([NaN, Infinity, 5])).toBe(5);
    });
});

describe('gridSnrOpacity', () => {
    it('hits the calibrated anchors', () => {
        expect(gridSnrOpacity(0)).toBeCloseTo(0.45, 5); // old "medium" tier
        expect(gridSnrOpacity(-10)).toBeCloseTo(0.15, 5);
        expect(gridSnrOpacity(10)).toBeCloseTo(0.60, 5);
        expect(gridSnrOpacity(20)).toBeCloseTo(0.75, 5);
    });

    it('clamps at the floor and cap', () => {
        expect(gridSnrOpacity(-30)).toBeCloseTo(0.10, 5);
        expect(gridSnrOpacity(50)).toBeCloseTo(0.75, 5);
    });

    it('is continuous across the 0 dB anchor', () => {
        expect(gridSnrOpacity(-0.1)).toBeCloseTo(0.447, 3);
        expect(gridSnrOpacity(0.1)).toBeGreaterThan(0.45);
        expect(gridSnrOpacity(0.1)).toBeLessThan(0.46);
    });

    it('maps non-finite scores to the floor', () => {
        expect(gridSnrOpacity(NaN)).toBeCloseTo(0.10, 5);
    });
});
