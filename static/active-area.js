// The "active area" map style: band territories.
//
// Every place on the map gets at most ONE band color, the band with the most
// local activity, so a region reads as "20m here, 40m there" instead of a
// stack of translucent overlapping blobs. Neighbouring territories meet along
// the line where both bands are equally strong. A second band that comes
// close to the leader (FIELD.coRatio) is drawn as diagonal stripes in its own
// color on top, and the dense core of a territory gets a second, stronger fill.
//
// Pipeline (shared by Mercator and Azimuthal):
//   aggregateSpots      one weighted point per (location, band)
//   buildGeoBandField   per-band Gaussian density on a lat/lng grid, kernel in km
//   buildTerritoryShapes  contour rings per band (territory, core, stripes), in lat/lng
//   drawTerritoryShapes   project the rings and fill them on a canvas
// The first three steps depend only on the spots, so a pan, zoom or azimuthal
// drag re-runs only the last one. Lone spots that never grow a region are
// drawn as dots (loneDotPoints / drawDots).
import { bandColors } from './utils.js';

// Thresholds on the density field.
export const FIELD = {
    presence: 1.5,   // outer region threshold (a lone square with one report peaks at 1.0)
    core: 5,         // dense core threshold
    coRatio: 0.6,    // a second band at >= 60% of the leader counts as co-dominant
};

// --- aggregation ------------------------------------------------------------

// One weighted point per (location, band). Weight = sqrt(reports) so one loud
// station repeating itself does not outweigh several distinct squares.
export function aggregateSpots(spots) {
    const agg = new Map();
    for (const s of spots) {
        const lat = Number(s.lat), lng = Number(s.lng);
        if (!Number.isFinite(lat) || !Number.isFinite(lng)) continue;
        const band = s.band || 'all';
        const k = `${band}|${lat.toFixed(3)}|${lng.toFixed(3)}`;
        const a = agg.get(k);
        if (a) a.n += 1;
        else agg.set(k, { lat, lng, band, n: 1 });
    }
    const pts = [...agg.values()];
    for (const p of pts) p.w = Math.sqrt(p.n);
    return pts;
}

// --- geographic density field ----------------------------------------------

// Double cellDeg until the field over the padded extent of pts has at most
// maxCells vertices.
export function coarsenForExtent(pts, cellDeg, radiusKm, maxCells) {
    if (!pts.length) return cellDeg;
    let s = 90, n = -90, w = 180, e = -180;
    for (const p of pts) {
        if (p.lat < s) s = p.lat;
        if (p.lat > n) n = p.lat;
        if (p.lng < w) w = p.lng;
        if (p.lng > e) e = p.lng;
    }
    const pad = radiusKm / KM_PER_DEG_LAT;
    const latSpan = Math.min(178, n - s + 2 * pad);
    const lngSpan = Math.min(360, e - w + 4 * pad);
    while ((latSpan / cellDeg) * (lngSpan / cellDeg) > maxCells && cellDeg < 4) cellDeg *= 2;
    return cellDeg;
}

const KM_PER_DEG_LAT = 110.57;
const KM_PER_DEG_LNG = 111.32;

// exp(-t * x) for x in [0, 1], sampled on KERNEL_LUT; t = r^2 / (2 sigma^2) is
// the same for every radius because sigma is a fixed fraction of the radius.
const KERNEL_LUT = new Float32Array(2049);
let kernelLutT = NaN;
function kernelLut(t) {
    if (t !== kernelLutT) {
        for (let n = 0; n < KERNEL_LUT.length; n++) KERNEL_LUT[n] = Math.exp(-t * n / (KERNEL_LUT.length - 1));
        kernelLutT = t;
    }
    return KERNEL_LUT;
}

// Scatter a truncated Gaussian (sigma = radius / 2.2, as in the azimuth KDE)
// from every point into a lat/lng grid. Returns null when there is no data.
export function buildGeoBandField(pts, { cellDeg = 1, radiusKm = 400 } = {}) {
    if (!pts.length) return null;
    let minLat = 90, maxLat = -90, minLng = 180, maxLng = -180;
    for (const p of pts) {
        if (p.lat < minLat) minLat = p.lat;
        if (p.lat > maxLat) maxLat = p.lat;
        if (p.lng < minLng) minLng = p.lng;
        if (p.lng > maxLng) maxLng = p.lng;
    }
    const padLat = radiusKm / KM_PER_DEG_LAT;
    const maxAbs = Math.min(80, Math.max(Math.abs(minLat), Math.abs(maxLat)) + padLat);
    const padLng = radiusKm / (KM_PER_DEG_LNG * Math.cos(maxAbs * Math.PI / 180));
    const lat0 = Math.max(-89, minLat - padLat);
    const lat1 = Math.min(89, maxLat + padLat);
    const lng0 = Math.max(-180, minLng - padLng);
    const lng1 = Math.min(180, maxLng + padLng);
    const cols = Math.max(2, Math.ceil((lng1 - lng0) / cellDeg) + 1);
    const rows = Math.max(2, Math.ceil((lat1 - lat0) / cellDeg) + 1);

    const bands = [...new Set(pts.map((p) => p.band))].sort();
    const densityByBand = {};
    for (const b of bands) densityByBand[b] = new Float32Array(cols * rows);

    // Gaussian lookup over the squared distance (exp() was the inner-loop cost).
    const sigma = radiusKm / 2.2;
    const r2 = radiusKm * radiusKm;
    const lutScale = (KERNEL_LUT.length - 1) / r2;
    const kernel = kernelLut(r2 / (2 * sigma * sigma));
    for (const p of pts) {
        const d = densityByBand[p.band];
        const jLo = Math.max(0, Math.floor((p.lat - padLat - lat0) / cellDeg));
        const jHi = Math.min(rows - 1, Math.ceil((p.lat + padLat - lat0) / cellDeg));
        for (let j = jLo; j <= jHi; j++) {
            const lat = lat0 + j * cellDeg;
            const dy = (lat - p.lat) * KM_PER_DEG_LAT;
            const kmPerLng = KM_PER_DEG_LNG * Math.cos(((lat + p.lat) / 2) * Math.PI / 180);
            const spanLng = radiusKm / Math.max(1, kmPerLng);
            const iLo = Math.max(0, Math.floor((p.lng - spanLng - lng0) / cellDeg));
            const iHi = Math.min(cols - 1, Math.ceil((p.lng + spanLng - lng0) / cellDeg));
            for (let i = iLo; i <= iHi; i++) {
                const dx = (lng0 + i * cellDeg - p.lng) * kmPerLng;
                const dd = dx * dx + dy * dy;
                if (dd > r2) continue;
                d[j * cols + i] += kernel[(dd * lutScale) | 0] * p.w;
            }
        }
    }
    return { cols, rows, lat0, lng0, cellDeg, bands, densityByBand };
}

// Per-vertex max over all bands and max over "all bands except b".
function maxFields(field) {
    const { cols, rows, bands, densityByBand } = field;
    const n = cols * rows;
    const top = new Float32Array(n);
    const second = new Float32Array(n);
    const topBand = new Int16Array(n).fill(-1);
    bands.forEach((b, bi) => {
        const d = densityByBand[b];
        for (let k = 0; k < n; k++) {
            const v = d[k];
            if (v > top[k]) { second[k] = top[k]; top[k] = v; topBand[k] = bi; } else if (v > second[k]) second[k] = v;
        }
    });
    return { top, second, topBand };
}

// --- contour rings (marching squares with segment linking) ------------------
//
// Cell segments are linked into closed rings and each region is filled once
// with the even-odd rule (holes work). Do not fill one polygon per cell: the
// canvas path then grows with the AREA of every region (tens of thousands of
// sub-paths, about 0.5 s per draw on a contest-sized feed), while rings grow
// only with the length of the borders. Rings are in lat/lng, so a redraw
// only projects their vertices.

// Segment table per marching-squares case. Cell corners: c0 (i,j), c1 (i+1,j),
// c2 (i+1,j+1), c3 (i,j+1); case bit k is set when corner k is inside.
// Edges: 0 = c0-c1, 1 = c1-c2, 2 = c2-c3, 3 = c3-c0. Saddles (5, 10) are
// resolved with the cell-centre average.
const CASE_SEGMENTS = [
    [], [3, 0], [0, 1], [3, 1], [1, 2], null, [0, 2], [3, 2],
    [2, 3], [0, 2], null, [1, 2], [1, 3], [0, 1], [3, 0], [],
];

// Trace the region values[k] >= 0 on a (pc x pr) grid whose border vertices
// are all negative, so every ring closes. Returns rings of grid coordinates
// [gx0, gy0, gx1, gy1, ...].
function traceRings(values, pc, pr) {
    const edgeA = [];
    const edgeB = [];
    const adj = new Map(); // edge key -> first segment, or [s1, s2]
    const pt = new Map(); // edge key -> [gx, gy]
    const edgeKey = (i, j, e) => {
        switch (e) {
            case 0: return (j * pc + i) * 2;
            case 1: return (j * pc + i + 1) * 2 + 1;
            case 2: return ((j + 1) * pc + i) * 2;
            default: return (j * pc + i) * 2 + 1;
        }
    };
    const crossing = (key) => {
        if (pt.has(key)) return;
        const horizontal = (key & 1) === 0;
        const base = key >> 1;
        const i = base % pc;
        const j = (base - i) / pc;
        const a = values[base];
        const b = values[horizontal ? base + 1 : base + pc];
        const f = a / (a - b);
        pt.set(key, horizontal ? [i + f, j] : [i, j + f]);
    };
    const addSeg = (ka, kb) => {
        const s = edgeA.length;
        edgeA.push(ka);
        edgeB.push(kb);
        for (const k of [ka, kb]) {
            const cur = adj.get(k);
            if (cur === undefined) adj.set(k, s);
            else adj.set(k, [cur, s]);
            crossing(k);
        }
    };
    for (let j = 0; j < pr - 1; j++) {
        let row = j * pc;
        for (let i = 0; i < pc - 1; i++, row++) {
            const v0 = values[row], v1 = values[row + 1], v2 = values[row + pc + 1], v3 = values[row + pc];
            const c = (v0 >= 0 ? 1 : 0) | (v1 >= 0 ? 2 : 0) | (v2 >= 0 ? 4 : 0) | (v3 >= 0 ? 8 : 0);
            if (c === 0 || c === 15) continue;
            const segs = CASE_SEGMENTS[c];
            if (segs) {
                addSeg(edgeKey(i, j, segs[0]), edgeKey(i, j, segs[1]));
                continue;
            }
            const centre = (v0 + v1 + v2 + v3) / 4 >= 0;
            // Case 5: c0, c2 inside. Case 10: c1, c3 inside.
            const cutCorners13 = (c === 5) === centre;
            if (cutCorners13) {
                addSeg(edgeKey(i, j, 0), edgeKey(i, j, 1));
                addSeg(edgeKey(i, j, 2), edgeKey(i, j, 3));
            } else {
                addSeg(edgeKey(i, j, 3), edgeKey(i, j, 0));
                addSeg(edgeKey(i, j, 1), edgeKey(i, j, 2));
            }
        }
    }
    const used = new Uint8Array(edgeA.length);
    const rings = [];
    for (let s0 = 0; s0 < edgeA.length; s0++) {
        if (used[s0]) continue;
        used[s0] = 1;
        const start = edgeA[s0];
        const ring = [...pt.get(start)];
        let cur = edgeB[s0];
        let s = s0;
        for (let guard = 0; guard < edgeA.length && cur !== start; guard++) {
            const p = pt.get(cur);
            ring.push(p[0], p[1]);
            const a = adj.get(cur);
            const t = Array.isArray(a) ? (a[0] === s ? a[1] : a[0]) : -1;
            if (t < 0 || used[t]) break;
            used[t] = 1;
            cur = edgeA[t] === cur ? edgeB[t] : edgeA[t];
            s = t;
        }
        if (ring.length >= 6) rings.push(ring);
    }
    return rings;
}

// Cell box where d >= threshold, grown by one vertex; null if empty. Every
// region of a band lies inside its own presence box, so the ring passes only
// need to scan that box instead of the whole field.
function bandBox(field, d, threshold) {
    const { cols, rows } = field;
    let i0 = cols, i1 = -1, j0 = rows, j1 = -1;
    for (let j = 0; j < rows; j++) {
        const row = j * cols;
        for (let i = 0; i < cols; i++) {
            if (d[row + i] < threshold) continue;
            if (i < i0) i0 = i;
            if (i > i1) i1 = i;
            if (j < j0) j0 = j;
            if (j > j1) j1 = j;
        }
    }
    if (i1 < 0) return null;
    return { i0: Math.max(0, i0 - 1), i1: Math.min(cols - 1, i1 + 1), j0: Math.max(0, j0 - 1), j1: Math.min(rows - 1, j1 + 1) };
}

// Padded value grids for one box: (w + 2) x (h + 2) vertices, border -1.
function paddedGrids(box, count) {
    const w = box.i1 - box.i0 + 1, h = box.j1 - box.j0 + 1;
    const pc = w + 2, pr = h + 2;
    const grids = [];
    for (let n = 0; n < count; n++) grids.push(new Float32Array(pc * pr).fill(-1));
    return { w, h, pc, pr, grids };
}

// Trace one padded grid and convert the rings to [lat, lng, ...].
function traceToLatLng(field, box, values, pc, pr) {
    const rings = traceRings(values, pc, pr);
    const latBase = field.lat0 + (box.j0 - 1) * field.cellDeg;
    const lngBase = field.lng0 + (box.i0 - 1) * field.cellDeg;
    for (const r of rings) {
        for (let n = 0; n < r.length; n += 2) {
            const lng = lngBase + r[n] * field.cellDeg;
            r[n] = latBase + r[n + 1] * field.cellDeg;
            r[n + 1] = lng;
        }
    }
    return rings;
}

// Add projected rings to the current path. A vertex that does not project
// (beyond the azimuthal horizon) is skipped.
function pathRings(ctx, rings, project) {
    let n = 0;
    for (const r of rings) {
        let started = false;
        for (let k = 0; k < r.length; k += 2) {
            const p = project(r[k], r[k + 1]);
            if (!p) continue;
            if (started) ctx.lineTo(p.x, p.y);
            else { ctx.moveTo(p.x, p.y); started = true; }
        }
        if (started) { ctx.closePath(); n++; }
    }
    return n;
}

// Diagonal stripe pattern in one band color (cached per color + scale).
const patternCache = new Map();
function stripePattern(ctx, color, scale = 1) {
    const key = `${color}|${scale}`;
    const hit = patternCache.get(key);
    if (hit) return hit;
    const size = Math.round(10 * scale);
    const c = (typeof OffscreenCanvas !== 'undefined') ? new OffscreenCanvas(size, size) : Object.assign(document.createElement('canvas'), { width: size, height: size });
    const g = c.getContext('2d');
    g.strokeStyle = color;
    g.lineWidth = 3 * scale;
    g.lineCap = 'square';
    g.beginPath();
    g.moveTo(-size, size * 2); g.lineTo(size * 2, -size);
    g.moveTo(-size, size); g.lineTo(size, -size);
    g.moveTo(0, size * 2); g.lineTo(size * 2, 0);
    g.stroke();
    const pat = ctx.createPattern(c, 'repeat');
    patternCache.set(key, pat);
    return pat;
}

// --- territories --------------------------------------------------------------

// Every place gets at most ONE fill color: the band with the highest local
// density. Boundaries between bands are the iso-line d_a == d_b, so neighbouring
// territories tile without overlap. A second band at >= coRatio of the leader
// is drawn as diagonal stripes in its own color on top ("both"). The dense core
// of a territory is a second, stronger fill of the same color.
//
// buildTerritoryShapes does the expensive part once per field; the result is
// projection-independent and can be redrawn with drawTerritoryShapes.
export function buildTerritoryShapes(field) {
    if (!field) return null;
    const mx = maxFields(field);
    const { presence, core, coRatio } = FIELD;
    const { cols } = field;
    const { top, second, topBand } = mx;
    return field.bands.map((band, bi) => {
        const d = field.densityByBand[band];
        const box = bandBox(field, d, presence);
        if (!box) return { band, territory: [], core: [], hatch: [] };
        // One pass fills all three value grids: territory (leader and above
        // presence), core (leader and above core) and hatch (not the leader
        // but within coRatio of it, and above presence).
        const { w, h, pc, pr, grids: [tv, cv, hv] } = paddedGrids(box, 3);
        let anyT = false, anyC = false, anyH = false;
        for (let j = 0; j < h; j++) {
            let k = (box.j0 + j) * cols + box.i0;
            let o = (j + 1) * pc + 1;
            for (let i = 0; i < w; i++, k++, o++) {
                const v = d[k];
                const others = topBand[k] === bi ? second[k] : top[k];
                const lead = v - others;
                const t = v - presence < lead ? v - presence : lead;
                const c = v - core < lead ? v - core : lead;
                let hh = v - presence;
                const co = v - coRatio * others;
                if (co < hh) hh = co;
                if (-lead < hh) hh = -lead;
                tv[o] = t; cv[o] = c; hv[o] = hh;
                if (t >= 0) anyT = true;
                if (c >= 0) anyC = true;
                if (hh >= 0) anyH = true;
            }
        }
        return {
            band,
            territory: anyT ? traceToLatLng(field, box, tv, pc, pr) : [],
            core: anyC ? traceToLatLng(field, box, cv, pc, pr) : [],
            hatch: anyH ? traceToLatLng(field, box, hv, pc, pr) : [],
        };
    });
}

export function drawTerritoryShapes(ctx, shapes, project, { pixelRatio = 1 } = {}) {
    if (!shapes) return;
    ctx.save();
    ctx.lineJoin = 'round';
    ctx.lineWidth = 1.25;
    for (const s of shapes) {
        if (!s.territory.length) continue;
        const color = bandColors[s.band] || bandColors.all;
        ctx.fillStyle = color;
        ctx.strokeStyle = color;
        ctx.beginPath();
        pathRings(ctx, s.territory, project);
        ctx.globalAlpha = 0.30;
        ctx.fill('evenodd');
        ctx.globalAlpha = 0.85;
        ctx.stroke();
        if (s.core.length) {
            ctx.beginPath();
            pathRings(ctx, s.core, project);
            ctx.globalAlpha = 0.22;
            ctx.fill('evenodd');
        }
    }
    ctx.globalAlpha = 0.75;
    for (const s of shapes) {
        if (!s.hatch.length) continue;
        ctx.beginPath();
        pathRings(ctx, s.hatch, project);
        ctx.fillStyle = stripePattern(ctx, bandColors[s.band] || bandColors.all, pixelRatio);
        ctx.fill('evenodd');
    }
    ctx.restore();
}

// Lone-spot dots: points whose own band density stays
// below presence never grow a region, so they are drawn as small dots. The
// list is computed once per field; drawDots only projects it.
export function loneDotPoints(field, pts) {
    if (!field) return [];
    const { cols, rows, lat0, lng0, cellDeg } = field;
    return pts.filter((p) => {
        const i = Math.round((p.lng - lng0) / cellDeg);
        const j = Math.round((p.lat - lat0) / cellDeg);
        const d = field.densityByBand[p.band];
        const v = (d && i >= 0 && j >= 0 && i < cols && j < rows) ? d[j * cols + i] : 0;
        return v < FIELD.presence;
    });
}

export function drawDots(ctx, pts, project, radius = 3.5) {
    ctx.save();
    ctx.globalAlpha = 0.8;
    for (const p of pts) {
        const q = project(p.lat, p.lng);
        if (!q) continue;
        ctx.beginPath();
        ctx.arc(q.x, q.y, radius, 0, Math.PI * 2);
        ctx.fillStyle = bandColors[p.band] || bandColors.all;
        ctx.fill();
    }
    ctx.restore();
}
