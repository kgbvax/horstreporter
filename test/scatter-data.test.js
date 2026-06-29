import { describe, it, expect } from 'vitest';
import { computeScatterData } from '../static/band-lab.js';

const center = { lat: 50, lng: 0 };

describe('computeScatterData', () => {
    it('returns null for no center or no points', () => {
        expect(computeScatterData(null, center, 0)).toBeNull();
        expect(computeScatterData([], center, 0)).toBeNull();
        expect(computeScatterData([{ lat: 51, lng: 1, snr: 5 }], null, 0)).toBeNull();
    });

    it('filters non-finite samples and returns null when none usable', () => {
        expect(computeScatterData([{ lat: NaN, lng: 1, snr: 5 }], center, 0)).toBeNull();
    });

    it('builds samples and axis ranges with floors', () => {
        const out = computeScatterData([
            { lat: 51, lng: 1, snr: 5 },
            { lat: 60, lng: 10, snr: 25 },
        ], center, 0);
        expect(out.samples).toHaveLength(2);
        expect(out.maxDist).toBeGreaterThanOrEqual(500);
        expect(out.minSnr).toBe(-20);
        expect(out.maxSnr).toBe(25);
        expect(out.snrRange).toBe(45);
    });
});
