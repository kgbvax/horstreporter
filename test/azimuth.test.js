import { beforeAll, beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { haversineKm } from '../static/utils.js';

// Wrap (not replace) the solar helpers so recompute counts and the subsolar
// time basis are observable while every other utils export stays real.
vi.mock('../static/utils.js', async (importOriginal) => {
    const actual = await importOriginal();
    return {
        ...actual,
        getSubsolarPoint: vi.fn((date) => actual.getSubsolarPoint(date))
    };
});

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

// --- U6: render-plan composition, projection math, label selection ---

// Two adjacent European entities whose label anchors are ~320km apart — close
// enough to collide at the LOD minimum distances used at low zoom.
function makeDenseEuropeFeatureCollection() {
    const mk = (iso, name, pop, labelX, labelY, ring) => ({
        type: 'Feature',
        properties: { ISO_A2: iso, POP_EST: pop, LABELRANK: 2, LABEL_X: labelX, LABEL_Y: labelY, NAME: name, ADM0_A3: name === 'Germany' ? 'DEU' : 'NLD' },
        geometry: { type: 'Polygon', coordinates: [ring] }
    });
    return {
        type: 'FeatureCollection',
        features: [
            mk('DE', 'Germany', 84000000, 10.4, 51.1, [[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]),
            mk('NL', 'Netherlands', 17000000, 5.3, 52.3, [[4, 50.8], [7, 50.8], [7, 53.6], [4, 53.6], [4, 50.8]])
        ]
    };
}

function makeFeature(overrides = {}) {
    return {
        type: 'Feature',
        properties: {
            ISO_A2: 'DE', POP_EST: 84000000, LABELRANK: 2,
            LABEL_X: 10.4, LABEL_Y: 51.1, NAME: 'Germany', ADM0_A3: 'DEU',
            ...overrides
        },
        geometry: { type: 'Polygon', coordinates: [[[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]] }
    };
}

describe('clampAzimuthZoom boundaries', () => {
    it('is identity inside the supported range, including both endpoints', () => {
        expect(az.clampAzimuthZoom(1)).toBe(1);
        expect(az.clampAzimuthZoom(5)).toBe(5);
        expect(az.clampAzimuthZoom(2.7)).toBe(2.7);
        expect(az.clampAzimuthZoom(1.0 + 1e-9)).toBeCloseTo(1.0, 8);
    });

    it('clamps out-of-range values to the nearest bound', () => {
        expect(az.clampAzimuthZoom(-3)).toBe(1);
        expect(az.clampAzimuthZoom(5.5)).toBe(5);
        expect(az.clampAzimuthZoom(Infinity)).toBe(5);
    });

    it('treats 0 as missing input and falls back to the 1.5 default', () => {
        // Number(0) is falsy, so the `Number(z) || 1.5` fallback kicks in
        // before the clamps: 0 becomes the default, not the lower bound.
        expect(az.clampAzimuthZoom(0)).toBe(1.5);
    });

    it('falls back to the 1.5 default for non-numeric input', () => {
        expect(az.clampAzimuthZoom(null)).toBe(1.5);
        expect(az.clampAzimuthZoom(NaN)).toBe(1.5);
        expect(az.clampAzimuthZoom(undefined)).toBe(1.5);
    });
});

describe('projectAeqdNormalized', () => {
    it('round-trips a known QTH-to-destination projection within tolerance', () => {
        const center = [0, 0];
        // ~1000 km due east along the equator: on the equator the great-circle
        // initial bearing east is exactly 90 degrees.
        const point = [0, 8.9832];

        const p = az.projectAeqdNormalized(center, point);
        expect(p.visible).toBe(true);

        // |(x, y)| equals the angular distance c by construction of AEQD.
        const radius = Math.hypot(p.x, p.y);
        expect(radius).toBeCloseTo(p.c, 6);

        // c in radians converts to ~the haversine great-circle distance.
        expect(p.c * 6371).toBeCloseTo(haversineKm(center[0], center[1], point[0], point[1]), -1);
        expect(p.c * 6371).toBeCloseTo(1000, -1);

        // Initial bearing of the projected direction (x = east, y = north) is due east.
        const bearingDeg = (Math.atan2(p.x, p.y) * 180) / Math.PI;
        expect(bearingDeg).toBeCloseTo(90, 3);
    });

    it('keeps the AEQD radius invariant away from the equator too', () => {
        const center = [52.52, 13.4];
        // A point ~1000 km east on the same parallel: the great-circle bearing
        // tilts north of east (great circles bow poleward), so only the
        // radius/distance invariants are asserted here.
        const point = [52.52, 28.19];
        const p = az.projectAeqdNormalized(center, point);
        expect(p.visible).toBe(true);
        expect(Math.hypot(p.x, p.y)).toBeCloseTo(p.c, 6);
        expect(p.c * 6371).toBeCloseTo(haversineKm(center[0], center[1], point[0], point[1]), -1);
        const bearingDeg = (Math.atan2(p.x, p.y) * 180) / Math.PI;
        expect(bearingDeg).toBeGreaterThan(80);
        expect(bearingDeg).toBeLessThan(90);
    });

    it('keeps visible=true and small positive x across the antimeridian wrap', () => {
        const p = az.projectAeqdNormalized([0, 170], [0, -170]);
        expect(p.visible).toBe(true);
        // 20 degrees of longitude east across the wrap.
        expect(p.c).toBeCloseTo((20 * Math.PI) / 180, 5);
        expect(p.x).toBeGreaterThan(0);
        expect(Math.abs(p.y)).toBeLessThan(1e-8);
    });

    it('marks the antipode as not visible', () => {
        const p = az.projectAeqdNormalized([0, 0], [0, 180]);
        expect(p.visible).toBe(false);
        // c is still returned so callers can distinguish "behind the horizon".
        expect(p.c).toBeCloseTo(Math.PI, 6);
        expect(p.x).toBe(0);
        expect(p.y).toBe(0);
    });

    it('projects the north pole straight up with the exact AEQD factor', () => {
        const p = az.projectAeqdNormalized([0, 0], [90, 0]);
        expect(p.visible).toBe(true);
        expect(p.c).toBeCloseTo(Math.PI / 2, 6);
        // k = c / sin(c) = (π/2) / 1 at c = π/2; x is 0 on the meridian.
        expect(p.x).toBeCloseTo(0, 8);
        expect(p.y).toBeCloseTo(Math.PI / 2, 6);
    });
});

describe('computeAzimuthLabelSpecs', () => {
    it('generates 90-degree increments with cardinals and no in-between labels', () => {
        const labels = az.computeAzimuthLabelSpecs([52, 7], 90);
        expect(labels.map(l => l.bearing)).toEqual([0, 90, 180, 270]);
        expect(labels.map(l => l.label)).toEqual(['0° N', '90° E', '180° S', '270° W']);
    });

    it('a 360-degree increment degenerates to the single north label', () => {
        const labels = az.computeAzimuthLabelSpecs([52, 7], 360);
        expect(labels).toHaveLength(1);
        expect(labels[0].bearing).toBe(0);
        expect(labels[0].label).toBe('0° N');
    });

    it('every label anchor sits ~ANTIPODE-900 km from the center and stays in range', () => {
        const center = [52, 7];
        const expectedDistance = Math.PI * 6371 - 900;
        for (const label of az.computeAzimuthLabelSpecs(center, 45)) {
            expect(label.lat).toBeGreaterThanOrEqual(-90);
            expect(label.lat).toBeLessThanOrEqual(90);
            expect(label.lng).toBeGreaterThanOrEqual(-180);
            expect(label.lng).toBeLessThanOrEqual(180);
            expect(haversineKm(center[0], center[1], label.lat, label.lng)).toBeCloseTo(expectedDistance, 0);
        }
    });
});

describe('selectProminentDxccLabels selection rules', () => {
    it('returns an empty selection for an empty feature collection', () => {
        expect(az.selectProminentDxccLabels({ type: 'FeatureCollection', features: [] }, [52, 7], {})).toEqual([]);
        expect(az.selectProminentDxccLabels(null, [52, 7], {})).toEqual([]);
    });

    it('skips features with an unresolvable DXCC prefix or no anchor', () => {
        const noPrefix = makeFeature({ ISO_A2: 'ZZ', ADM0_A3: 'ZZZ', NAME: 'Nowhere' });
        const noAnchor = {
            type: 'Feature',
            properties: { ISO_A2: 'DE', NAME: 'Germany' },
            geometry: null
        };

        const labels = az.selectProminentDxccLabels(
            { type: 'FeatureCollection', features: [noPrefix, noAnchor] },
            [52, 7],
            { maxLabels: 10, minDistanceKm: 100 }
        );
        expect(labels).toEqual([]);
    });

    it('prefers an explicit DXCC_PREFIX property over the ISO/A3 tables', () => {
        const feature = makeFeature({ DXCC_PREFIX: 'dp0special' });
        const labels = az.selectProminentDxccLabels(
            { type: 'FeatureCollection', features: [feature] },
            [52, 7],
            { maxLabels: 10, minDistanceKm: 100 }
        );
        expect(labels).toHaveLength(1);
        expect(labels[0].prefix).toBe('DP0SPECIAL');
    });

    it('suppresses overlapping candidates: the higher-scored label survives', () => {
        const fc = makeDenseEuropeFeatureCollection();
        const labels = az.selectProminentDxccLabels(fc, [52, 7], {
            maxLabels: 10,
            minDistanceKm: 500,
            includeSupplemental: false
        });

        // Germany and the Netherlands are ~320km apart; only one slot survives.
        expect(labels).toHaveLength(1);
        expect(labels[0].prefix).toBe('DL');
        expect(labels[0].name).toBe('Germany');
    });

    it('maxLabels 0 yields no labels', () => {
        const fc = { type: 'FeatureCollection', features: [makeFeature()] };
        expect(az.selectProminentDxccLabels(fc, [52, 7], { maxLabels: 0, minDistanceKm: 100 })).toEqual([]);
    });

    it('excludes anchors on the far side of the antipode guard radius', () => {
        // Exactly antipodal to center [52, 7]: haversine distance ~20015km,
        // beyond ANTIPODE_KM - 300.
        const feature = makeFeature({ LABEL_X: -173, LABEL_Y: -52 });
        const labels = az.selectProminentDxccLabels(
            { type: 'FeatureCollection', features: [feature] },
            [52, 7],
            { maxLabels: 10, minDistanceKm: 100 }
        );
        expect(labels).toEqual([]);
    });

    it('omits supplemental entities by default for small feature sets', () => {
        const fc = { type: 'FeatureCollection', features: [makeFeature()] };
        const without = az.selectProminentDxccLabels(fc, [52, 7], { maxLabels: 20, minDistanceKm: 100 });
        expect(without.some(l => l.prefix === 'KL7')).toBe(false);

        const withSupplemental = az.selectProminentDxccLabels(fc, [52, 7], { maxLabels: 20, minDistanceKm: 100, includeSupplemental: true });
        expect(withSupplemental.some(l => l.prefix === 'KL7' && l.name === 'Alaska')).toBe(true);
    });
});

describe('createAzimuthRenderPlan scene composition', () => {
    it('builds an empty plan for a spotless scene', () => {
        // A distinct center so the plan's DXCC label cache (keyed by
        // center/zoom/theme, not by the featureCollection) doesn't return a
        // cached label set from another fixture.
        const plan = az.createAzimuthRenderPlan({
            featureCollection: { type: 'FeatureCollection', features: [] },
            center: [40, -100],
            spots: [],
            theme: 'light',
            zoomLevel: 2
        });

        expect(plan).toEqual({
            center: [40, -100],
            theme: 'light',
            countryFillMap: expect.any(Map),
            dxccLabels: [],
            azimuthLabels: expect.any(Array),
            overlaySummary: { style: 'grid-snr', itemCount: 0 },
            zoom: 2
        });
        expect(plan.countryFillMap.size).toBe(0);
        expect(plan.azimuthLabels).toHaveLength(12);
    });

    it('counts a single spot in the overlay summary', () => {
        const plan = az.createAzimuthRenderPlan({
            featureCollection: makeDenseEuropeFeatureCollection(),
            center: [52, 7],
            spots: [{ locator: 'JO32', snr: 8, band: '20m', lat: 52.2, lng: 7.3 }],
            theme: 'light',
            zoomLevel: 2
        });
        expect(plan.overlaySummary).toEqual({ style: 'grid-snr', itemCount: 1 });
    });

    it('counts every spot unfiltered, including dxcluster-sourced and off-band ones', () => {
        const spots = [];
        for (let i = 0; i < 20; i++) spots.push({ locator: 'JO32', snr: 8, band: '20m', lat: 52 + i * 0.1, lng: 7 + i * 0.1 });
        spots.push({ locator: 'JO42', snr: -30, band: '70cm', lat: 53, lng: 8 });         // weak + off-band
        spots.push({ locator: 'JO52', snr: 0, band: '40m', sourceType: 'DXCLUSTER', lat: 54, lng: 9 }); // dxcluster ingest
        spots.push({ locator: null, snr: 5, band: '20m', lat: 55, lng: 10 });             // no locator

        const plan = az.createAzimuthRenderPlan({
            featureCollection: { type: 'FeatureCollection', features: [] },
            center: [52, 7],
            spots,
            theme: 'light',
            zoomLevel: 2
        });
        // The plan is the raw scene contract: filtering happens downstream at
        // draw time, so itemCount is the unfiltered spot count.
        expect(plan.overlaySummary).toEqual({ style: 'grid-snr', itemCount: 23 });
    });

    it('rounds the center to 3 decimals and passes zoom/theme through', () => {
        const plan = az.createAzimuthRenderPlan({
            featureCollection: { type: 'FeatureCollection', features: [] },
            center: [52.520567, 13.401234],
            spots: [],
            theme: 'dark',
            zoomLevel: 3.14159
        });
        expect(plan.center).toEqual([52.521, 13.401]);
        expect(plan.theme).toBe('dark');
        expect(plan.zoom).toBeCloseTo(3.14159, 10);
    });

    it('assigns palette fills keyed by feature key, MAPCOLOR13 first, deterministically', () => {
        const mapcolorFeature = {
            type: 'Feature',
            properties: { ISO_A2: 'BR', POP_EST: 1, LABELRANK: 2, LABEL_X: -50, LABEL_Y: -10, NAME: 'Brazil', ADM0_A3: 'BRA', MAPCOLOR13: 3 },
            geometry: { type: 'Polygon', coordinates: [[[-70, -30], [-40, -30], [-40, 0], [-70, -30]]] }
        };
        const plan = az.createAzimuthRenderPlan({
            featureCollection: { type: 'FeatureCollection', features: [mapcolorFeature] },
            center: [-20, -50],
            spots: [],
            theme: 'light',
            zoomLevel: 2
        });

        // MAPCOLOR13 3 -> palette index (3-1) % 14 = 2 of the light palette.
        expect(plan.countryFillMap.get('BRA')).toBe('#FEE5DA');

        // Repeat with the same fixture: cached fill map is identical.
        const again = az.createAzimuthRenderPlan({
            featureCollection: { type: 'FeatureCollection', features: [mapcolorFeature] },
            center: [-20, -50],
            spots: [],
            theme: 'light',
            zoomLevel: 2
        });
        expect(again.countryFillMap.get('BRA')).toBe('#FEE5DA');
    });

    it('applies the DXCC label LOD: low zoom drops near neighbours, zoom >= 5 shows all', () => {
        const fc = makeDenseEuropeFeatureCollection();
        // Center distinct from other tests at zoom 2: the label cache is keyed
        // by center/zoom/theme and would otherwise serve a foreign fixture.
        const common = { featureCollection: fc, center: [52.4, 7.5], spots: [], theme: 'light' };

        const lowZoom = az.createAzimuthRenderPlan({ ...common, zoomLevel: 2 });
        // LOD at zoom 2: minDistanceKm 700 drops the ~320km-away NL anchor.
        expect(lowZoom.dxccLabels.map(l => l.prefix)).toEqual(['DL']);

        const showAll = az.createAzimuthRenderPlan({ ...common, zoomLevel: 5.5 });
        expect(showAll.dxccLabels.map(l => l.prefix).sort()).toEqual(['DL', 'PA']);
    });

    it('treats a label density of 0 as invalid and keeps the fallback of 1.0', () => {
        // The setter's `Number(density) || 1.0` fallback makes 0 (and any
        // non-numeric value) a no-op: the "density 0 -> no labels" LOD table
        // entry is unreachable through the public setter. Pin the actual
        // behavior: labels still render.
        az.setAzimuthDxccLabelDensity(0);
        try {
            const plan = az.createAzimuthRenderPlan({
                featureCollection: makeDenseEuropeFeatureCollection(),
                center: [52, 7],
                spots: [],
                theme: 'light',
                zoomLevel: 2
            });
            expect(plan.dxccLabels.length).toBeGreaterThan(0);
            az.setAzimuthDxccLabelDensity(-2);
            const clamped = az.createAzimuthRenderPlan({
                featureCollection: makeDenseEuropeFeatureCollection(),
                center: [53, 7],
                spots: [],
                theme: 'light',
                zoomLevel: 2
            });
            // Negative densities clamp to the 0 lower bound — density 0 is
            // therefore reachable only via the clamp, not the falsy fallback.
            expect(clamped.dxccLabels).toEqual([]);
        } finally {
            az.setAzimuthDxccLabelDensity(1.0);
        }
    });
});

// --- U6 (plan 2026-09-11-002): azimuth grayline keys on the dataNow clock ---
// drawGrayline is reached through the module's __internals export; a recompute
// is observable as one getSubsolarPoint call (cache-hit and throttle paths
// never reach the subsolar computation). The 320ms recompute throttle stays
// keyed on wall-clock Date.now() (KTD-9) — verified below with an
// intentionally large data-time offset.
const BUCKET_MS = 5 * 60 * 1000;
const WALL_MS = 1_730_000_000_000;
const PLAYHEAD_MS = 1_700_000_123_000; // 113_000ms into its bucket

describe('azimuth grayline playhead clock (plan 2026-09-11-002 U6, KTD-8/9)', () => {
    let getSubsolarPoint;

    function installCanvasStub() {
        // jsdom has no canvas 2D implementation; a minimal stub lets the
        // recompute's sample pass run and the overlay cache be written.
        const fakeCtx = {
            createImageData: (w, h) => ({ data: new Uint8ClampedArray(w * h * 4) }),
            putImageData: vi.fn(),
            clearRect: vi.fn(),
            drawImage: vi.fn(),
            imageSmoothingEnabled: true,
            imageSmoothingQuality: 'low'
        };
        vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(fakeCtx);
    }

    function resetGraylineCache() {
        az.__internals.state.graylineOverlayCache = { key: '', canvas: null, computedAtMs: 0 };
    }

    beforeEach(async () => {
        vi.useFakeTimers({ now: WALL_MS });
        installCanvasStub();
        resetGraylineCache();
        az.__internals.state.center = [20, 0];
        az.__internals.state.theme = 'light';
        az.__internals.state.zoom = 1.5;
        az.__internals.state.horizonKm = 16000;
        ({ getSubsolarPoint } = await import('../static/utils.js'));
        vi.mocked(getSubsolarPoint).mockClear();
    });

    afterEach(async () => {
        const { clearDataNowOverride } = await import('../static/data-now.js');
        clearDataNowOverride();
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    function draw(dataNowMs) {
        const ctx = { drawImage: vi.fn() };
        az.__internals.drawGrayline(ctx, 300, 300, dataNowMs);
        return ctx;
    }

    it('live mode (no dataNowMs): the bucket derives from the wall clock', () => {
        const bucket = Math.floor(WALL_MS / BUCKET_MS);
        draw(undefined);

        expect(getSubsolarPoint).toHaveBeenCalledTimes(1);
        expect(getSubsolarPoint).toHaveBeenCalledWith(new Date(bucket * BUCKET_MS));
        expect(az.__internals.state.graylineOverlayCache.key.endsWith(`:${bucket}`)).toBe(true);
    });

    it('scrub: two playheads inside one 5-minute bucket yield a single recompute', () => {
        draw(PLAYHEAD_MS);
        vi.advanceTimersByTime(400); // wall gap proves this is the KEY hit, not the throttle
        draw(PLAYHEAD_MS + 60_000);

        expect(getSubsolarPoint).toHaveBeenCalledTimes(1);
        const key = az.__internals.state.graylineOverlayCache.key;
        expect(key.endsWith(`:${Math.floor(PLAYHEAD_MS / BUCKET_MS)}`)).toBe(true);
    });

    it('scrub: a playhead in a different bucket recomputes with the new bucket', () => {
        draw(PLAYHEAD_MS);
        vi.advanceTimersByTime(400); // past the 320ms throttle so the new bucket can rebuild
        draw(PLAYHEAD_MS + BUCKET_MS + 60_000);

        expect(getSubsolarPoint).toHaveBeenCalledTimes(2);
        expect(getSubsolarPoint).toHaveBeenLastCalledWith(
            new Date(Math.floor((PLAYHEAD_MS + BUCKET_MS + 60_000) / BUCKET_MS) * BUCKET_MS)
        );
    });

    it('playback 600x: recomputes track bucket crossings, not frames (no per-frame storm)', () => {
        // 600x of a 50ms wall frame = 30s of playhead per frame; 40 frames span
        // 20min of playhead (4 bucket crossings) in 2s of wall time.
        const frames = 40;
        for (let i = 0; i < frames; i++) {
            draw(PLAYHEAD_MS + i * 30_000);
            if (i < frames - 1) vi.advanceTimersByTime(50);
        }

        const buckets = new Set();
        for (let i = 0; i < frames; i++) {
            buckets.add(Math.floor((PLAYHEAD_MS + i * 30_000) / BUCKET_MS));
        }
        expect(getSubsolarPoint).toHaveBeenCalledTimes(buckets.size);
        expect(buckets.size).toBe(4); // pinned: well below 40 frames
        expect(getSubsolarPoint).toHaveBeenCalledTimes(4);
    });

    it('replay determinism: the same playhead bucket reuses the cached overlay', () => {
        const first = draw(PLAYHEAD_MS);
        const cached = az.__internals.state.graylineOverlayCache.canvas;
        vi.advanceTimersByTime(1000); // replay re-entry arrives later on the wall clock too
        const second = draw(PLAYHEAD_MS);

        expect(getSubsolarPoint).toHaveBeenCalledTimes(1);
        expect(second.drawImage).toHaveBeenCalledWith(cached, 0, 0, 300, 300);
        expect(first.drawImage).toHaveBeenCalledWith(cached, 0, 0, 300, 300);
    });

    it('the 320ms recompute throttle stays keyed on wall-clock Date.now()', () => {
        // Prime the cache with a mismatched key (so the key-hit path cannot
        // serve it) computed 100ms ago on the wall clock; the data clock sits
        // in a fresh bucket, far from the wall clock.
        const cachedCanvas = { width: 300, height: 300 };
        az.__internals.state.graylineOverlayCache = {
            key: 'foreign-key',
            canvas: cachedCanvas,
            computedAtMs: Date.now() - 100
        };

        const throttled = draw(PLAYHEAD_MS + BUCKET_MS);
        expect(getSubsolarPoint).not.toHaveBeenCalled();
        expect(throttled.drawImage).toHaveBeenCalledWith(cachedCanvas, 0, 0, 300, 300);

        // Past the wall-clock throttle the same data clock recomputes.
        vi.advanceTimersByTime(400);
        draw(PLAYHEAD_MS + BUCKET_MS);
        expect(getSubsolarPoint).toHaveBeenCalledTimes(1);
    });
});

describe('azimuth basemap tokens (shared with Mercator via map-tokens.js)', () => {
    let MAP_TOKEN_FALLBACKS;
    let clearMapTokenCache;

    // Records every draw op with the fill/stroke style active at that moment.
    function makeStyleRecordingCtx() {
        const ops = [];
        const ctx = {
            ops,
            globalAlpha: 1, strokeStyle: '', fillStyle: '', lineWidth: 1, lineCap: '', lineJoin: '', font: '',
            textAlign: '', textBaseline: '',
            beginPath() {}, closePath() {}, moveTo() {}, lineTo() {}, rect() {},
            // A real 2D context restores these on restore(); the halo text
            // relies on that to hand the graticule stroke back.
            _stack: [],
            save() { ctx._stack.push({ fillStyle: ctx.fillStyle, strokeStyle: ctx.strokeStyle, globalAlpha: ctx.globalAlpha, lineWidth: ctx.lineWidth, lineJoin: ctx.lineJoin }); },
            restore() { Object.assign(ctx, ctx._stack.pop() || {}); },
            clearRect(...args) { ops.push({ op: 'clearRect', args }); },
            fillRect(...args) { ops.push({ op: 'fillRect', fillStyle: ctx.fillStyle, args }); },
            arc(...args) { ops.push({ op: 'arc', args }); },
            roundRect(...args) { ops.push({ op: 'roundRect', args }); },
            fill() { ops.push({ op: 'fill', fillStyle: ctx.fillStyle, alpha: ctx.globalAlpha }); },
            stroke() { ops.push({ op: 'stroke', strokeStyle: ctx.strokeStyle, alpha: ctx.globalAlpha }); },
            fillText(text) { ops.push({ op: 'fillText', text, fillStyle: ctx.fillStyle }); },
            strokeText(text) { ops.push({ op: 'strokeText', text, strokeStyle: ctx.strokeStyle }); },
            measureText(text) { return { width: String(text).length * 6 }; }
        };
        return ctx;
    }

    beforeAll(async () => {
        ({ MAP_TOKEN_FALLBACKS, clearMapTokenCache } = await import('../static/map-tokens.js'));
    });

    beforeEach(() => {
        clearMapTokenCache();
        document.body.innerHTML = '';
        document.body.removeAttribute('style');
        document.body.setAttribute('data-theme', 'light');
        const s = az.__internals.state;
        s.theme = 'light';
        s.center = [0, 0];
        s.zoom = 1.5;
        s.horizonKm = 16000;
    });

    afterEach(() => {
        clearMapTokenCache();
        document.body.innerHTML = '';
        document.body.removeAttribute('style');
        document.body.removeAttribute('data-theme');
        az.__internals.state.theme = 'light';
        az.__internals.state.worldGeoJson = null;
    });

    it('paints the page surface around the disc and the water inside it', () => {
        document.body.style.setProperty('--bg-color', '#fefefe');
        document.body.style.setProperty('--map-water', '#123456');
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawBackground(ctx, 400, 300);

        const fillRect = ctx.ops.find(o => o.op === 'fillRect');
        expect(fillRect.fillStyle).toBe('#fefefe');
        expect(fillRect.args).toEqual([0, 0, 400, 300]);

        const disc = ctx.ops.find(o => o.op === 'arc');
        // At the default zoom/horizon the disc stops just inside the bearing
        // scale ring (0.47 * min side minus the 12px clearance).
        expect(disc.args.slice(0, 3)).toEqual([200, 150, (300 * 0.47) - 12]);
        expect(ctx.ops.find(o => o.op === 'fill').fillStyle).toBe('#123456');
    });

    it('shrinks the water disc to the horizon when the horizon is short', () => {
        az.__internals.state.zoom = 1;
        az.__internals.state.horizonKm = 2000;
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawBackground(ctx, 400, 400);
        const radius = ctx.ops.find(o => o.op === 'arc').args[2];
        expect(radius).toBeCloseTo((400 * 0.47 / Math.PI) * (2000 / 6371), 6);
        expect(ctx.ops.find(o => o.op === 'fill').fillStyle).toBe(MAP_TOKEN_FALLBACKS.light.water);
    });

    it('re-reads the tokens when the theme changes', () => {
        document.body.style.setProperty('--map-water', '#111111');
        const light = makeStyleRecordingCtx();
        az.__internals.drawBackground(light, 300, 300);
        expect(light.ops.find(o => o.op === 'fill').fillStyle).toBe('#111111');

        document.body.setAttribute('data-theme', 'dark');
        document.body.style.setProperty('--map-water', '#222222');
        az.setAzimuthTheme('dark');
        const dark = makeStyleRecordingCtx();
        az.__internals.drawBackground(dark, 300, 300);
        expect(dark.ops.find(o => o.op === 'fill').fillStyle).toBe('#222222');

        // Back to light: the light values are read again, not the stale cache.
        document.body.setAttribute('data-theme', 'light');
        document.body.style.setProperty('--map-water', '#333333');
        az.setAzimuthTheme('light');
        const again = makeStyleRecordingCtx();
        az.__internals.drawBackground(again, 300, 300);
        expect(again.ops.find(o => o.op === 'fill').fillStyle).toBe('#333333');
    });

    function worldFixture() {
        return {
            type: 'FeatureCollection',
            features: [{
                type: 'Feature',
                properties: { ADM0_A3: 'DEU', ISO_A2: 'DE' },
                geometry: { type: 'Polygon', coordinates: [[[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]] }
            }]
        };
    }

    function setCountryColoring(on) {
        const toggle = document.createElement('input');
        toggle.type = 'checkbox';
        toggle.id = 'show-country-coloring';
        toggle.checked = on;
        document.body.appendChild(toggle);
    }

    it('fills land with --map-land and strokes borders with --map-border when country coloring is off', () => {
        setCountryColoring(false);
        az.__internals.state.center = [51, 10];
        az.__internals.state.worldGeoJson = worldFixture();
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawWorld(ctx, 400, 400, { countryFillMap: new Map([['DEU', '#FEE5DA']]) });

        const fills = ctx.ops.filter(o => o.op === 'fill');
        const strokes = ctx.ops.filter(o => o.op === 'stroke');
        expect(fills.length).toBeGreaterThan(0);
        expect(new Set(fills.map(o => o.fillStyle))).toEqual(new Set([MAP_TOKEN_FALLBACKS.light.land]));
        expect(new Set(strokes.map(o => o.strokeStyle))).toEqual(new Set([MAP_TOKEN_FALLBACKS.light.border]));
        expect(fills.every(o => o.alpha === 1)).toBe(true);
    });

    it('tints country fills over --map-land at the Mercator overlay opacity', () => {
        setCountryColoring(true);
        az.__internals.state.center = [51, 10];
        az.__internals.state.worldGeoJson = worldFixture();
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawWorld(ctx, 400, 400, { countryFillMap: new Map([['DEU', '#FEE5DA']]) });

        // #FEE5DA at 30% over #fafaf8, as Mercator shows it over the CARTO land.
        const fills = ctx.ops.filter(o => o.op === 'fill');
        expect(fills.length).toBeGreaterThan(0);
        expect(new Set(fills.map(o => o.fillStyle))).toEqual(new Set(['#fbf4ef']));
    });

    it('uses the same country palette as the Mercator overlay (utils.js)', async () => {
        const { getCountryFillForFeature } = await import('../static/utils.js');
        const feature = { type: 'Feature', properties: { ADM0_A3: 'ZZQ', MAPCOLOR13: 7 }, geometry: null };
        // The fill map is cached by theme + feature count; start clean.
        az.__internals.state.countryFillCache.clear();
        for (const theme of ['light', 'dark']) {
            const plan = az.createAzimuthRenderPlan({
                featureCollection: { type: 'FeatureCollection', features: [feature] },
                center: [0, 0],
                theme,
                zoomLevel: 1.5
            });
            expect(plan.countryFillMap.get('ZZQ')).toBe(getCountryFillForFeature(feature, theme));
        }
    });

    it('draws bearing labels in --map-label over a --map-land halo and the ring in --map-graticule', () => {
        document.body.style.setProperty('--map-label', '#0a0b0c');
        document.body.style.setProperty('--map-land', '#f0f0f0');
        document.body.style.setProperty('--map-graticule', '#445566');
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawAzimuthIndicator(ctx, 400, 400);

        const texts = ctx.ops.filter(o => o.op === 'fillText');
        expect(texts.map(o => o.text)).toEqual(expect.arrayContaining(['N', 'E', 'S', 'W', '30°', '330°']));
        expect(new Set(texts.map(o => o.fillStyle))).toEqual(new Set(['#0a0b0c']));
        const halos = ctx.ops.filter(o => o.op === 'strokeText');
        expect(halos).toHaveLength(texts.length);
        expect(new Set(halos.map(o => o.strokeStyle))).toEqual(new Set(['#f0f0f0']));
        expect(new Set(ctx.ops.filter(o => o.op === 'stroke').map(o => o.strokeStyle))).toEqual(new Set(['#445566']));
    });

    it('draws DXCC labels as the Mercator pill (--dxcc-label-* tokens)', () => {
        document.body.setAttribute('data-theme', 'dark');
        az.setAzimuthTheme('dark');
        const ctx = makeStyleRecordingCtx();
        az.__internals.drawDxccLabels(ctx, 400, 400, { dxccLabels: [{ prefix: 'DL', lat: 10, lng: 10 }] });

        const dark = MAP_TOKEN_FALLBACKS.dark;
        expect(ctx.ops.find(o => o.op === 'roundRect')).toBeTruthy();
        expect(ctx.ops.find(o => o.op === 'fill').fillStyle).toBe(dark.labelBg);
        expect(ctx.ops.find(o => o.op === 'stroke').strokeStyle).toBe(dark.labelBorder);
        expect(ctx.ops.find(o => o.op === 'fillText')).toMatchObject({ text: 'DL', fillStyle: dark.labelText });
    });
});
