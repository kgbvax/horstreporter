import { describe, expect, it, vi } from 'vitest';

vi.mock('../static/map.js', () => ({ map: { removeLayer: vi.fn(), fitBounds: vi.fn(), hasLayer: vi.fn(() => false) } }));

import { computeAutoZoomBox } from '../static/renderers.js';

const ctx = {
    minSnrMode: 'none',
    ssbMinDb: 0,
    cwMinDb: -15,
    selectedBand: 'all',
    enabledBands: new Set(['20m', '40m'])
};

function europe(count) {
    const spots = [];
    for (let i = 0; i < count; i += 1) {
        spots.push({ band: '20m', snr: 0, lat: 40 + (i % 20), lng: -5 + (i % 30) });
    }
    return spots;
}

describe('computeAutoZoomBox', () => {
    it('ignores a few long-haul outliers once there are enough spots', () => {
        const spots = [
            ...europe(100),
            { band: '20m', snr: 0, lat: -41, lng: 173 }, // ZL
            { band: '20m', snr: 0, lat: -37, lng: 145 }, // VK
            { band: '20m', snr: 0, lat: 33, lng: -93 } // W5
        ];
        const box = computeAutoZoomBox(spots, ctx);
        expect(box.minLat).toBeGreaterThanOrEqual(40);
        expect(box.maxLng).toBeLessThanOrEqual(25);
        expect(box.minLng).toBeGreaterThanOrEqual(-5);
    });

    it('keeps every spot in small sets', () => {
        const spots = [...europe(5), { band: '20m', snr: 0, lat: -41, lng: 173 }];
        expect(computeAutoZoomBox(spots, ctx)).toEqual({ minLat: -41, maxLat: 44, minLng: -5, maxLng: 173 });
    });

    it('applies band and SNR filters and skips spots without coordinates', () => {
        const spots = [
            { band: '20m', snr: 5, lat: 50, lng: 7 },
            { band: '80m', snr: 5, lat: -41, lng: 173 },
            { band: '20m', snr: -20, lat: 35, lng: 139 },
            { band: '20m', snr: 5, lat: undefined, lng: 0 }
        ];
        const box = computeAutoZoomBox(spots, { ...ctx, minSnrMode: 'ssb' });
        expect(box).toEqual({ minLat: 50, maxLat: 50, minLng: 7, maxLng: 7 });
    });

    it('returns null when nothing passes the filters', () => {
        expect(computeAutoZoomBox([{ band: '80m', snr: 0, lat: 1, lng: 1 }], ctx)).toBeNull();
    });
});
