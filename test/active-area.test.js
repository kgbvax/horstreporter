import { describe, expect, it } from 'vitest';
import {
    FIELD,
    aggregateSpots,
    buildGeoBandField,
    buildTerritoryShapes,
    coarsenForExtent,
    drawTerritoryShapes,
    loneDotPoints,
} from '../static/active-area.js';

// A square block of spots (one per grid point, `reports` each) centred on
// (lat, lng), for one band.
function block(band, lat, lng, { half = 2, step = 1, reports = 1 } = {}) {
    const spots = [];
    for (let dy = -half; dy <= half; dy += step) {
        for (let dx = -half; dx <= half; dx += step) {
            for (let r = 0; r < reports; r++) spots.push({ band, lat: lat + dy, lng: lng + dx });
        }
    }
    return spots;
}

function shapesFor(spots, { cellDeg = 0.5, radiusKm = 400 } = {}) {
    const pts = aggregateSpots(spots);
    const field = buildGeoBandField(pts, { cellDeg, radiusKm });
    return { pts, field, shapes: buildTerritoryShapes(field) };
}

// Even-odd point-in-rings test on [lat, lng, ...] rings: this is how the
// canvas fills them, so holes count as outside.
function inside(rings, lat, lng) {
    let odd = false;
    for (const r of rings) {
        for (let i = 0, j = r.length - 2; i < r.length; j = i, i += 2) {
            const yi = r[i], xi = r[i + 1], yj = r[j], xj = r[j + 1];
            if ((yi > lat) !== (yj > lat) && lng < ((xj - xi) * (lat - yi)) / (yj - yi) + xi) odd = !odd;
        }
    }
    return odd;
}

const byBand = (shapes, band) => shapes.find((s) => s.band === band);

describe('active-area territories', () => {
    it('weights a repeated location by sqrt(reports)', () => {
        const pts = aggregateSpots([
            { band: '20m', lat: 50, lng: 10 }, { band: '20m', lat: 50, lng: 10 },
            { band: '20m', lat: 50, lng: 10 }, { band: '20m', lat: 50, lng: 10 },
            { band: '40m', lat: 50, lng: 10 },
        ]);
        expect(pts).toHaveLength(2);
        expect(pts.find((p) => p.band === '20m').w).toBe(2);
        expect(pts.find((p) => p.band === '40m').w).toBe(1);
    });

    it('traces one closed territory around a dense block, and none for a lone spot', () => {
        const { pts, field, shapes } = shapesFor([...block('20m', 50, 10), { band: '20m', lat: 20, lng: 60 }]);
        const s = byBand(shapes, '20m');
        expect(s.territory).toHaveLength(1);
        expect(inside(s.territory, 50, 10)).toBe(true);
        expect(inside(s.territory, 20, 60)).toBe(false);
        // The lone spot is drawn as a dot; spots inside the territory are not.
        const lone = loneDotPoints(field, pts);
        expect(lone).toHaveLength(1);
        expect(lone[0]).toMatchObject({ lat: 20, lng: 60 });
    });

    it('gives each place one band: the stronger band owns the overlap, without stripes', () => {
        const { shapes } = shapesFor([
            ...block('20m', 50, 10, { reports: 16 }),
            ...block('40m', 50, 10),
        ]);
        expect(inside(byBand(shapes, '20m').territory, 50, 10)).toBe(true);
        expect(inside(byBand(shapes, '40m').territory, 50, 10)).toBe(false);
        expect(byBand(shapes, '40m').hatch).toHaveLength(0);
    });

    it('splits neighbouring bands along the line where both are equally strong', () => {
        const { shapes } = shapesFor([...block('20m', 50, 5), ...block('40m', 50, 13)]);
        const t20 = byBand(shapes, '20m').territory;
        const t40 = byBand(shapes, '40m').territory;
        expect(inside(t20, 50, 5)).toBe(true);
        expect(inside(t40, 50, 13)).toBe(true);
        // No place belongs to both.
        for (let lng = 0; lng <= 18; lng += 0.25) {
            expect(inside(t20, 50, lng) && inside(t40, 50, lng)).toBe(false);
        }
    });

    it('stripes a second band that comes within coRatio of the leader', () => {
        // 20m slightly stronger than 40m in the same place.
        const { field, shapes } = shapesFor([
            ...block('20m', 50, 10, { reports: 4 }),
            ...block('40m', 50, 10, { reports: 3 }),
        ]);
        const k = field.cols * Math.round((50 - field.lat0) / field.cellDeg) + Math.round((10 - field.lng0) / field.cellDeg);
        const ratio = field.densityByBand['40m'][k] / field.densityByBand['20m'][k];
        expect(ratio).toBeGreaterThan(FIELD.coRatio);
        expect(ratio).toBeLessThan(1);
        expect(inside(byBand(shapes, '20m').territory, 50, 10)).toBe(true);
        expect(inside(byBand(shapes, '40m').hatch, 50, 10)).toBe(true);
        expect(byBand(shapes, '20m').hatch).toHaveLength(0);
    });

    it('keeps a hole in a ring-shaped region (even-odd fill)', () => {
        // A ring of spots 6 deg from the centre, nothing inside.
        const spots = [];
        for (let a = 0; a < 360; a += 4) {
            for (let r = 0; r < 4; r++) {
                spots.push({ band: '20m', lat: 50 + 6 * Math.sin((a * Math.PI) / 180), lng: 10 + 9 * Math.cos((a * Math.PI) / 180) });
            }
        }
        const { shapes } = shapesFor(spots, { radiusKm: 250 });
        const t = byBand(shapes, '20m').territory;
        expect(t.length).toBe(2);
        expect(inside(t, 56, 10)).toBe(true);
        expect(inside(t, 50, 10)).toBe(false);
    });

    it('draws each band with one even-odd fill per layer, one sub-path per ring', () => {
        const { shapes } = shapesFor([...block('20m', 50, 5), ...block('40m', 50, 13)]);
        const calls = [];
        const ctx = {
            save() {}, restore() {}, beginPath() {}, closePath() {}, lineTo() {}, stroke() {},
            moveTo() { calls.push('moveTo'); },
            fill(rule) { calls.push(`fill:${rule}`); },
            createPattern: () => 'pattern',
        };
        // drawTerritoryShapes builds its stripe pattern on a canvas; jsdom has
        // no 2D context, so only draw shapes without stripes here.
        const noHatch = shapes.map((s) => ({ ...s, hatch: [] }));
        drawTerritoryShapes(ctx, noHatch, (lat, lng) => ({ x: lng, y: -lat }));
        const rings = noHatch.reduce((n, s) => n + s.territory.length + s.core.length, 0);
        expect(calls.filter((c) => c === 'moveTo')).toHaveLength(rings);
        expect(calls.filter((c) => c.startsWith('fill:')).every((c) => c === 'fill:evenodd')).toBe(true);
    });

    it('coarsens the grid for a world-wide extent and keeps it for a small one', () => {
        const world = aggregateSpots([{ band: '20m', lat: -60, lng: -170 }, { band: '20m', lat: 70, lng: 170 }]);
        const local = aggregateSpots([{ band: '20m', lat: 50, lng: 5 }, { band: '20m', lat: 52, lng: 9 }]);
        expect(coarsenForExtent(world, 0.25, 400, 250000)).toBeGreaterThanOrEqual(0.5);
        expect(coarsenForExtent(local, 0.25, 400, 250000)).toBe(0.25);
    });
});
