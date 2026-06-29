import { beforeAll, describe, expect, it } from 'vitest';

let az;

beforeAll(async () => {
    az = await import('./azimuth.js');
});

// Minimal canvas 2D context that records draw calls, for asserting which
// primitives a render function emits without a real canvas.
function makeRecordingCtx(calls) {
    const inc = (k) => { calls[k] = (calls[k] || 0) + 1; };
    return {
        save() {}, restore() {}, beginPath() {}, closePath() {},
        moveTo() { inc('moveTo'); }, lineTo() { inc('lineTo'); },
        arc() { inc('arc'); }, stroke() { inc('stroke'); }, fill() { inc('fill'); },
        setLineDash() { inc('dash'); }, clearRect() {}, fillRect() {},
        globalAlpha: 1, strokeStyle: '', fillStyle: '', lineWidth: 1, lineCap: '', lineJoin: ''
    };
}

describe('azimuth.js', () => {
    it('clamps azimuth zoom to supported range and handles invalid input', () => {
        expect(az.clampAzimuthZoom(0.1)).toBe(1);
        expect(az.clampAzimuthZoom(10)).toBe(5);
        expect(az.clampAzimuthZoom('foo')).toBe(1.5);
    });

    it('clamps azimuth horizon to supported range and handles invalid input', () => {
        expect(az.clampAzimuthHorizonKm(100)).toBe(1000);
        expect(az.clampAzimuthHorizonKm(50000)).toBe(20015);
        expect(az.clampAzimuthHorizonKm('foo')).toBe(16000);
    });

    it('projects center to origin in normalized AEQD coordinates', () => {
        const p = az.projectAeqdNormalized([52, 7], [52, 7]);
        expect(p.visible).toBe(true);
        expect(p.x).toBeCloseTo(0, 8);
        expect(p.y).toBeCloseTo(0, 8);
    });

    it('computes initial great-circle bearing from the station center', () => {
        az.setAzimuthCenter([0, 0]);
        expect(az.bearingFromCenter(10, 0)).toBeCloseTo(0, 3);   // due north
        expect(az.bearingFromCenter(0, 10)).toBeCloseTo(90, 3);  // due east
        expect(az.bearingFromCenter(-10, 0)).toBeCloseTo(180, 3); // due south
        expect(az.bearingFromCenter(0, -10)).toBeCloseTo(270, 3); // due west
    });

    it('draws a rim halo arc for azimuth sectors with rising activity', () => {
        az.setAzimuthCenter([52, 7]);
        const spots = [];
        for (let i = 0; i < 4; i++) spots.push({ lat: 52, lng: 30, ageSeconds: 200 }); // older
        for (let i = 0; i < 12; i++) spots.push({ lat: 52, lng: 30, ageSeconds: 10 }); // newer -> rising
        const calls = { stroke: 0, arc: 0, fill: 0 };
        const ctx = makeRecordingCtx(calls);
        az.drawTrendHalo(ctx, 800, 800, spots);
        expect(calls.arc).toBeGreaterThanOrEqual(1);
        expect(calls.stroke).toBeGreaterThanOrEqual(1);
        expect(calls.fill).toBe(0);
    });

    it('computes 30 degree azimuth labels including cardinals', () => {
        const labels = az.computeAzimuthLabelSpecs([52, 7], 30, 15000);
        expect(labels).toHaveLength(12);
        expect(labels[0].label).toContain('N');
        expect(labels[3].label).toContain('E');
        expect(labels[6].label).toContain('S');
        expect(labels[9].label).toContain('W');
    });

    it('resolves France DXCC prefix even when ISO_A2 is -99', () => {
        const featureCollection = {
            type: 'FeatureCollection',
            features: [
                {
                    type: 'Feature',
                    properties: { ISO_A2: '-99', ISO_A2_EH: 'FR', ADM0_A3: 'FRA', POP_EST: 65000000, LABELRANK: 2, LABEL_X: 2.55, LABEL_Y: 46.69, NAME: 'France' },
                    geometry: { type: 'Polygon', coordinates: [[[-5, 42], [8, 42], [8, 51], [-5, 51], [-5, 42]]] }
                }
            ]
        };

        const labels = az.selectProminentDxccLabels(featureCollection, [46, 2], {
            maxLabels: 5,
            minDistanceKm: 100
        });

        expect(labels.some(l => l.prefix === 'F')).toBe(true);
    });

    it('selects prominent DXCC labels with LOD distance spacing', () => {
        const featureCollection = {
            type: 'FeatureCollection',
            features: [
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'DE', POP_EST: 84000000, LABELRANK: 2, LABEL_X: 10.4, LABEL_Y: 51.1, NAME: 'Germany' },
                    geometry: { type: 'Polygon', coordinates: [[[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]] }
                },
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'AU', POP_EST: 25364307, LABELRANK: 2, LABEL_X: 134.05, LABEL_Y: -24.12, NAME: 'Australia' },
                    geometry: { type: 'Polygon', coordinates: [[[113, -44], [154, -44], [154, -10], [113, -10], [113, -44]]] }
                },
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'IN', POP_EST: 1400000000, LABELRANK: 2, LABEL_X: 79, LABEL_Y: 22, NAME: 'India' },
                    geometry: { type: 'Polygon', coordinates: [[[68, 8], [97, 8], [97, 37], [68, 37], [68, 8]]] }
                },
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'RU', POP_EST: 144000000, LABELRANK: 2, LABEL_X: 37.6, LABEL_Y: 55.7, NAME: 'Russia' },
                    geometry: { type: 'Polygon', coordinates: [[[27, 41], [180, 41], [180, 82], [27, 82], [27, 41]]] }
                }
            ]
        };

        const labels = az.selectProminentDxccLabels(featureCollection, [52, 7], {
            maxLabels: 10,
            minDistanceKm: 100,
            includeSupplemental: true
        });

        expect(labels.some(l => l.prefix === 'DL')).toBe(true);
        expect(labels.some(l => l.prefix === 'VK')).toBe(true);
        expect(labels.some(l => l.prefix === 'VU')).toBe(true);
        expect(labels.some(l => l.prefix === 'UA9')).toBe(true);
    });

    it('creates deterministic azimuth render plan snapshot', () => {
        const featureCollection = {
            type: 'FeatureCollection',
            features: [
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'DE', POP_EST: 84000000, LABELRANK: 2, LABEL_X: 10.4, LABEL_Y: 51.1, NAME: 'Germany', ADM0_A3: 'DEU' },
                    geometry: { type: 'Polygon', coordinates: [[[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]] }
                },
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'IN', POP_EST: 1400000000, LABELRANK: 2, LABEL_X: 79, LABEL_Y: 22, NAME: 'India', ADM0_A3: 'IND' },
                    geometry: { type: 'Polygon', coordinates: [[[68, 8], [97, 8], [97, 37], [68, 37], [68, 8]]] }
                },
                {
                    type: 'Feature',
                    properties: { ISO_A2: 'AU', POP_EST: 25364307, LABELRANK: 2, LABEL_X: 134.05, LABEL_Y: -24.12, NAME: 'Australia', ADM0_A3: 'AUS' },
                    geometry: { type: 'Polygon', coordinates: [[[113, -44], [154, -44], [154, -10], [113, -10], [113, -44]]] }
                }
            ]
        };

        const spots = [
            { locator: 'JO32', snr: 8, band: '20m', lat: 52.2, lng: 7.3 },
            { locator: 'MK82', snr: 12, band: '20m', lat: 22.2, lng: 79.1 },
            { locator: 'QF56', snr: 5, band: '20m', lat: -24.8, lng: 134.1 }
        ];

        const plan = az.createAzimuthRenderPlan({
            featureCollection,
            center: [52, 7],
            spots,
            style: 'grid-snr',
            theme: 'light',
            zoomLevel: 2
        });

        expect(plan.center).toEqual([52, 7]);
        expect(plan.overlaySummary).toEqual({ style: 'grid-snr', itemCount: 3 });
        expect(plan.dxccLabels.map(l => l.prefix).sort()).toEqual(['DL', 'VK', 'VU']);
        expect(plan.azimuthLabels).toHaveLength(12);
    });

    it('normalizes non-grid azimuth styles to grid-snr overlay summary', () => {
        const featureCollection = {
            type: 'FeatureCollection',
            features: []
        };

        const spots = [
            { locator: 'JO32', snr: 8, band: '20m', lat: 52.2, lng: 7.3 },
            { locator: 'JO42', snr: 3, band: '40m', lat: 53.0, lng: 8.0 },
            { locator: 'JO52', snr: 6, band: '20m', lat: 54.0, lng: 9.0 }
        ];

        const areaPlan = az.createAzimuthRenderPlan({
            featureCollection,
            center: [52, 7],
            spots,
            style: 'active-area',
            theme: 'dark',
            zoomLevel: 1.8
        });

        expect(areaPlan.overlaySummary).toEqual({ style: 'grid-snr', itemCount: 3 });
    });
});
