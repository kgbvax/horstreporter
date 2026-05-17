import { describe, expect, it, beforeAll } from 'vitest';

let az;

function nowMs() {
    return (typeof performance !== 'undefined' && typeof performance.now === 'function')
        ? performance.now()
        : Date.now();
}

beforeAll(async () => {
    az = await import('../static/azimuth.js');
});

describe('azimuth perf harness', () => {
    it('builds a render plan for medium dataset within a practical budget', () => {
        const featureCollection = {
            type: 'FeatureCollection',
            features: []
        };

        for (let i = 0; i < 250; i++) {
            const lng = -170 + (i % 34) * 10;
            const lat = -60 + Math.floor(i / 34) * 12;
            featureCollection.features.push({
                type: 'Feature',
                properties: {
                    ISO_A2: i % 3 === 0 ? 'DE' : i % 3 === 1 ? 'IN' : 'AU',
                    POP_EST: 1_000_000 + i * 1000,
                    LABELRANK: 2,
                    LABEL_X: lng,
                    LABEL_Y: lat,
                    NAME: `F-${i}`,
                    ADM0_A3: `A${i}`
                },
                geometry: {
                    type: 'Polygon',
                    coordinates: [[[lng, lat], [lng + 3, lat], [lng + 3, lat + 2], [lng, lat + 2], [lng, lat]]]
                }
            });
        }

        const spots = [];
        for (let i = 0; i < 5000; i++) {
            spots.push({
                locator: 'JO32',
                snr: (i % 30) - 10,
                band: i % 2 === 0 ? '20m' : '40m',
                lat: -55 + (i % 110),
                lng: -170 + (i % 340)
            });
        }

        const start = nowMs();
        const plan = az.createAzimuthRenderPlan({
            featureCollection,
            center: [52, 7],
            spots,
            style: 'grid-snr',
            theme: 'light',
            zoomLevel: 2.5
        });
        const elapsed = nowMs() - start;

        expect(plan.dxccLabels.length).toBeGreaterThan(0);
        expect(plan.azimuthLabels.length).toBe(12);
        expect(elapsed).toBeLessThan(2500);
    });
});
