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

    it('caps the axis at the global p95 cap, not the raw max', () => {
        // Two near points (~111 km) and one far point (~854 km). With the cap
        // floored at 500 km the far point must be flagged clipped and the axis
        // must NOT stretch to the raw max.
        const out = computeScatterData([
            { lat: 51, lng: 1, snr: 5 },
            { lat: 51, lng: 1, snr: 5 },
            { lat: 60, lng: 10, snr: 25 },
        ], center, 0);
        expect(out.maxDist).toBe(500);
        const clipped = out.samples.filter((s) => s.clipped);
        expect(clipped).toHaveLength(1);
        expect(clipped[0].d).toBeGreaterThan(500);
    });

    it('does not flag in-range points as clipped', () => {
        const out = computeScatterData([
            { lat: 51, lng: 1, snr: 5 },
        ], center, 500);
        expect(out.maxDist).toBe(500);
        expect(out.samples.every((s) => !s.clipped)).toBe(true);
    });
});
