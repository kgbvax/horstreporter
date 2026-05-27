import { bandColors, getEnabledBands, getGraylineEnabled, getGraylineOverlayOpacities, getSubsolarPoint, getMinSnrMode, getSelectedBand, locatorToBounds, getGridResolution } from './utils.js';

const EARTH_RADIUS_KM = 6371;
const ANTIPODE_KM = Math.PI * EARTH_RADIUS_KM;
const MAX_VISIBLE_C = Math.PI - 0.02;
const DXCC_SHOW_ALL_ZOOM_THRESHOLD = 5.0;

const PALETTE_LIGHT = [
    '#FBEFF0', '#FBD3D1', '#FEE5DA', '#FFE2B7', '#FFFBD4', '#E8EDAD', '#E4F0DB',
    '#C3E6E5', '#E1F3FC', '#BFD3ED', '#E0DEEF', '#DFCBE3', '#E3D9C6', '#FFFFFF'
];
const PALETTE_DARK = [
    '#4c383c', '#5a3c3b', '#5b4b44', '#615640', '#5a5a3c', '#4b5637', '#3f5441',
    '#355453', '#375360', '#334967', '#45425f', '#55445d', '#544d43', '#2f343a'
];

const DXCC_PREFIX_BY_ISO_A2 = {
    US: 'K', CA: 'VE', RU: 'UA', CN: 'BY', IN: 'VU', AU: 'VK', BR: 'PY', AR: 'LU', ZA: 'ZS', CL: 'CE',
    JP: 'JA', ID: 'YB', MX: 'XE', ES: 'EA', FR: 'F', DE: 'DL', IT: 'I', GB: 'G', NO: 'LA', SE: 'SM',
    FI: 'OH', NZ: 'ZL', EG: 'SU', ET: 'ET', SD: 'ST', DJ: 'J2', ER: 'E3', YE: '7O', SA: 'HZ',
    IR: 'EP', PK: 'AP', CO: 'HK', PE: 'OA', NG: '5N', CD: '9Q', KZ: 'UN', TH: 'HS', TZ: '5H',
    UA: 'UR', UZ: 'UK', MY: '9M2', VN: 'XV', KR: 'HL', DZ: '7X', MA: 'CN', PT: 'CT', NL: 'PA',
    CH: 'HB', BE: 'ON', AT: 'OE', PL: 'SP', RO: 'YO', GR: 'SV', TR: 'TA', IS: 'TF',
    GL: 'OX', PF: 'FO', FJ: 'FW', MP: 'KH0', GU: 'KH2', PW: 'PY', RE: 'FR', AX: 'OH0',
    KE: '5Z', UG: '5X', GH: '9G', CM: 'TJ', AO: 'D4', NA: 'V5', BW: 'A2', ZW: 'Z2', MW: '7J', 
    ZM: '9J', MU: '3B8', RW: '9XR', BI: '9U', SO: '5J', SC: 'S9', MG: '5R', SN: '6W', CI: 'TU',
    BF: 'XT', ML: 'TN', NE: '5U', MR: '5T', LS: '5M', SZ: 'ZS3', KM: 'D3', MZ: 'C9', LY: '5A',
    IE: 'EI', GI: 'ZB', MT: 'ZB2', CY: 'ZC4', LB: 'OD', IL: '4X',
    // Additional entities
    AD: 'C3', AE: 'A6', AF: 'T4', AG: 'V2', AI: 'VP2', AL: 'ZA1', AM: 'EK', AQ: 'AQ',
    AS: 'KH8', AW: 'P4', AZ: '4K', BA: 'T9', BB: '8P', BD: 'S2', BG: 'LZ', BH: 'A9',
    BJ: 'TY', BL: 'FJ2', BM: 'VP9', BN: 'V8', BO: 'CP', BS: 'C6', BT: 'A5', BY: 'EU',
    BZ: 'V3', CF: 'TL', CG: 'TN2', CK: 'E51', 'CN-TW': 'BX', CR: 'TI', CU: 'CO', CV: 'D4',
    CW: 'PJ', CZ: 'OK', DK: 'OZ', DM: 'J7', DO: 'HI', EC: 'HC', EE: 'ES', EH: 'EH',
    FK: 'VP8', FM: 'V6', FO: 'OY', GA: 'TR', GD: 'J3', GE: '4L', GG: 'GU', GM: 'C5',
    GN: '3G', GQ: '3C', GS: 'VP8', GT: 'TG', GW: '3G2', GY: '8R', HK: 'VR2', HM: 'VK0',
    HN: 'HR', HR: '9A', HT: 'HH', HU: 'HA', IM: 'MD', IO: 'VQ9', IQ: 'YI', JE: 'MJ',
    JM: '6Y', JO: 'JY', KG: 'EX', KH: 'XU', KI: 'T31', KN: 'V4', KP: 'P5', KW: '9K',
    KY: 'ZF', LA: 'XW', LC: 'J6', LI: 'HB3', LK: '4S', LR: 'EL', LT: 'LY', LU: 'LX',
    LV: 'YL', MC: '3A', MD: 'ER2', ME: '4O', MF: 'PJ2', MH: 'V7', MK: 'Z3', MM: 'XZ',
    MN: 'JT', MO: 'BX2', MS: 'J4', MV: '8Q', NC: 'FK', NF: 'VK0', NI: 'YN', NP: '9N',
    NR: 'C2', NU: 'E2', OM: 'A4', PA: 'HP', PG: 'P2', PH: 'DU', PM: 'FP', PN: 'VP6',
    PR: 'K4', PS: 'E4', PY: 'ZP', QA: 'A7', RS: 'YU', SB: 'H44', SG: '9V', SH: 'ZD7',
    SI: 'S5', SK: 'OM2', SL: '9L', SM: 'T7', SR: 'PZ', SS: 'Z8', ST: 'S9', SV: 'YS',
    SX: 'PJ3', SY: 'YK', TC: 'VP5', TD: 'TT', TF: 'FT', TG: '5V', TJ: 'EY', TL: '4W',
    TM: 'EZ', TN: '3V', TO: 'A3', TT: '9Y', TV: 'T2', UY: 'CX', VA: 'I', VC: 'J8',
    VE: 'YV', VG: 'VP2', VI: 'K4', VU: 'YJ', WF: 'FW', WS: '5W'
};

const DXCC_PREFIX_BY_A3 = {
    FRA: 'F',
    NOR: 'LA',
    GBR: 'G',
    DEU: 'DL',
    ESP: 'EA',
    PRT: 'CT',
    ITA: 'I',
    RUS: 'UA'
};

// Entities that are not represented as separate countries in world.geojson but are distinct DXCC entities.
const SUPPLEMENTAL_DXCC_ENTITIES = [
    { key: 'DXCC_ASIATIC_RUSSIA', name: 'Asiatic Russia', prefix: 'UA9', lat: 60.0, lng: 90.0, scoreBoost: 3.2 },
    { key: 'DXCC_KALININGRAD', name: 'Kaliningrad', prefix: 'UA2', lat: 54.7, lng: 20.5, scoreBoost: 2.1 },
    { key: 'DXCC_ALASKA', name: 'Alaska', prefix: 'KL7', lat: 64.8, lng: -149.5, scoreBoost: 2.2 },
    { key: 'DXCC_HAWAII', name: 'Hawaii', prefix: 'KH6', lat: 20.8, lng: -157.5, scoreBoost: 2.1 },
    { key: 'DXCC_AZORES', name: 'Azores', prefix: 'CU', lat: 38.6, lng: -28.0, scoreBoost: 1.9 },
    { key: 'DXCC_MADEIRA', name: 'Madeira', prefix: 'CT3', lat: 32.7, lng: -16.9, scoreBoost: 1.7 },
    { key: 'DXCC_CANARY_ISLANDS', name: 'Canary Islands', prefix: 'EA8', lat: 28.3, lng: -16.5, scoreBoost: 1.9 },
    { key: 'DXCC_BALEARIC', name: 'Balearic Islands', prefix: 'EA6', lat: 39.6, lng: 2.9, scoreBoost: 1.6 },
    { key: 'DXCC_CEUTA_MELILLA', name: 'Ceuta and Melilla', prefix: 'EA9', lat: 35.3, lng: -2.9, scoreBoost: 1.6 },
    { key: 'DXCC_SARDINIA', name: 'Sardinia', prefix: 'IS0', lat: 40.1, lng: 9.0, scoreBoost: 1.4 }
];

const state = {
    enabled: false,
    canvas: null,
    ctx: null,
    worldGeoJson: null,
    center: [20, 0],
    theme: 'light',
    zoom: 1.5,
    horizonKm: 16000,
    ns6tIndicatorEnabled: true,
    dxccLabelDensity: 1.0,
    dxccLabelsEnabled: true,
    lastSpots: [],
    lastStyle: 'grid-snr',
    resizeAttached: false,
    canvasSizeCache: { width: 0, height: 0, dpr: 0 },
    dxccLabelCache: new Map(),
    countryFillCache: new Map(),
    graylineOverlayCache: {
        key: '',
        canvas: null
    },
    worldLayerCache: {
        key: '',
        canvas: null
    }
};

export function isAzimuthEnabled() {
    return state.enabled;
}

export function setAzimuthEnabled(enabled) {
    state.enabled = Boolean(enabled);
    const mapEl = typeof document !== 'undefined' ? document.getElementById('map') : null;
    const canvas = ensureCanvas();
    if (mapEl) mapEl.style.display = state.enabled ? 'none' : 'block';
    if (canvas) canvas.style.display = state.enabled ? 'block' : 'none';
}

export function setAzimuthTheme(theme) {
    state.theme = theme === 'dark' ? 'dark' : 'light';
}

export function setAzimuthCenter(center) {
    if (Array.isArray(center) && center.length === 2) {
        state.center = [Number(center[0]) || 0, normalizeLng(Number(center[1]) || 0)];
    }
}

export function setAzimuthZoom(z) {
    state.zoom = clampAzimuthZoom(z);
}

export function setAzimuthHorizonKm(km) {
    state.horizonKm = clampAzimuthHorizonKm(km);
}

export function setAzimuthNs6tIndicatorEnabled(enabled) {
    state.ns6tIndicatorEnabled = Boolean(enabled);
}

export function setAzimuthDxccLabelDensity(density) {
    state.dxccLabelDensity = Math.max(0, Math.min(5.0, Number(density) || 1.0));
}

export function setAzimuthDxccLabelsEnabled(enabled) {
    state.dxccLabelsEnabled = Boolean(enabled);
}

export function clampAzimuthZoom(z) {
    return Math.max(1.0, Math.min(5.0, Number(z) || 1.5));
}

export function clampAzimuthHorizonKm(km) {
    return Math.max(1000, Math.min(20015, Number(km) || 16000));
}

export function initAzimuthCanvas() {
    const canvas = ensureCanvas();
    if (!canvas) return;
    resizeCanvasToElement(canvas);
    if (!state.resizeAttached && typeof window !== 'undefined') {
        window.addEventListener('resize', () => {
            const c = ensureCanvas();
            if (c) resizeCanvasToElement(c);
            if (state.enabled) renderAzimuthScene({ spots: state.lastSpots, style: state.lastStyle });
        });
        state.resizeAttached = true;
    }
}

export async function loadAzimuthWorldGeoJson() {
    if (state.worldGeoJson) return state.worldGeoJson;
    if (typeof fetch !== 'function') return null;
    const resp = await fetch('vendor/world.geojson');
    if (!resp.ok) {
        throw new Error(`Failed to load world.geojson: ${resp.status}`);
    }
    state.worldGeoJson = await resp.json();
    return state.worldGeoJson;
}

function ensureCanvas() {
    if (state.canvas) return state.canvas;
    if (typeof document === 'undefined') return null;
    state.canvas = document.getElementById('azimuth-canvas');
    if (!state.canvas) return null;
    state.ctx = state.canvas.getContext('2d');
    state.canvas.style.pointerEvents = 'none';
    if (!state.enabled) state.canvas.style.display = 'none';
    return state.canvas;
}

function resizeCanvasToElement(canvas) {
    const dpr = (typeof window !== 'undefined' && window.devicePixelRatio) ? window.devicePixelRatio : 1;
    const width = canvas.clientWidth || canvas.offsetWidth || 1;
    const height = canvas.clientHeight || canvas.offsetHeight || 1;

    if (state.canvasSizeCache.width === width && state.canvasSizeCache.height === height && state.canvasSizeCache.dpr === dpr) {
        return;
    }

    state.canvasSizeCache = { width, height, dpr };
    canvas.width = Math.max(1, Math.round(width * dpr));
    canvas.height = Math.max(1, Math.round(height * dpr));
    if (state.ctx) {
        state.ctx.setTransform(1, 0, 0, 1, 0, 0);
        state.ctx.scale(dpr, dpr);
    }
}

function degToRad(v) {
    return (v * Math.PI) / 180;
}

function radToDeg(v) {
    return (v * 180) / Math.PI;
}

function normalizeLng(lng) {
    let out = lng;
    while (out > 180) out -= 360;
    while (out < -180) out += 360;
    return out;
}

function haversineKm(aLat, aLng, bLat, bLng) {
    const dLat = degToRad(bLat - aLat);
    const dLng = degToRad(bLng - aLng);
    const a = Math.sin(dLat / 2) ** 2 + Math.cos(degToRad(aLat)) * Math.cos(degToRad(bLat)) * Math.sin(dLng / 2) ** 2;
    return 2 * EARTH_RADIUS_KM * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

function destinationPoint(lat, lng, bearingDeg, distanceKm) {
    const angularDistance = distanceKm / EARTH_RADIUS_KM;
    const bearing = degToRad(bearingDeg);
    const lat1 = degToRad(lat);
    const lng1 = degToRad(lng);

    const sinLat1 = Math.sin(lat1);
    const cosLat1 = Math.cos(lat1);
    const sinAd = Math.sin(angularDistance);
    const cosAd = Math.cos(angularDistance);

    const lat2 = Math.asin(sinLat1 * cosAd + cosLat1 * sinAd * Math.cos(bearing));
    const lng2 = lng1 + Math.atan2(Math.sin(bearing) * sinAd * cosLat1, cosAd - sinLat1 * Math.sin(lat2));

    return [radToDeg(lat2), normalizeLng(radToDeg(lng2))];
}

export function projectAeqdNormalized(center, point) {
    const lat0 = degToRad(center[0]);
    const lon0 = degToRad(center[1]);
    const lat = degToRad(point[0]);
    const lon = degToRad(point[1]);

    let dLon = lon - lon0;
    while (dLon > Math.PI) dLon -= 2 * Math.PI;
    while (dLon < -Math.PI) dLon += 2 * Math.PI;

    const sinLat0 = Math.sin(lat0);
    const cosLat0 = Math.cos(lat0);
    const sinLat = Math.sin(lat);
    const cosLat = Math.cos(lat);
    const cosC = Math.max(-1, Math.min(1, sinLat0 * sinLat + cosLat0 * cosLat * Math.cos(dLon)));
    const c = Math.acos(cosC);
    if (!Number.isFinite(c) || c > MAX_VISIBLE_C) {
        return { visible: false, x: 0, y: 0, c };
    }

    const k = c === 0 ? 1 : c / Math.sin(c);
    const x = k * cosLat * Math.sin(dLon);
    const y = k * (cosLat0 * sinLat - sinLat0 * cosLat * Math.cos(dLon));

    return { visible: true, x, y, c };
}

export function computeAzimuthLabelSpecs(center, increment = 30, distanceKm = ANTIPODE_KM - 900) {
    const labels = [];
    for (let bearing = 0; bearing < 360; bearing += increment) {
        const [lat, lng] = destinationPoint(center[0], center[1], bearing, distanceKm);
        const cardinal = bearing === 0 ? 'N' : bearing === 90 ? 'E' : bearing === 180 ? 'S' : bearing === 270 ? 'W' : '';
        labels.push({ bearing, label: cardinal ? `${bearing}° ${cardinal}` : `${bearing}°`, lat, lng });
    }
    return labels;
}

function featureName(feature) {
    const p = feature?.properties || {};
    return p.ADMIN || p.NAME_LONG || p.NAME || 'Unknown';
}

function featureKey(feature) {
    const p = feature?.properties || {};
    return p.ADM0_A3 || p.ISO_A2 || p.SOV_A3 || p.BRK_A3 || p.NAME || p.ADMIN || 'UNKNOWN';
}

function featurePrefix(feature) {
    const p = feature?.properties || {};
    const explicit = p.DXCC_PREFIX || p.DXCC || p.dxccPrefix;
    if (explicit) return String(explicit).toUpperCase();
    const isoCandidates = [p.ISO_A2, p.ISO_A2_EH, p.POSTAL]
        .map(v => String(v || '').toUpperCase())
        .filter(v => !!v && v !== '-99');

    for (const iso of isoCandidates) {
        const prefix = DXCC_PREFIX_BY_ISO_A2[iso];
        if (prefix) return prefix;
    }

    const a3Candidates = [p.ADM0_A3, p.BRK_A3, p.SOV_A3, p.ISO_A3, p.ISO_A3_EH]
        .map(v => String(v || '').toUpperCase())
        .filter(v => !!v && v !== '-99');

    for (const a3 of a3Candidates) {
        const prefix = DXCC_PREFIX_BY_A3[a3];
        if (prefix) return prefix;
    }

    return null;
}

function featureAnchor(feature) {
    const p = feature?.properties || {};
    const x = Number(p.LABEL_X);
    const y = Number(p.LABEL_Y);
    if (Number.isFinite(x) && Number.isFinite(y) && Math.abs(x) <= 180 && Math.abs(y) <= 90) {
        return [y, normalizeLng(x)];
    }

    const geom = feature?.geometry;
    if (!geom || !Array.isArray(geom.coordinates)) return null;

    const coords = [];
    const walk = (node) => {
        if (!Array.isArray(node)) return;
        if (typeof node[0] === 'number' && typeof node[1] === 'number') {
            coords.push([node[1], node[0]]);
            return;
        }
        node.forEach(walk);
    };
    walk(geom.coordinates);
    if (!coords.length) return null;

    const lat = coords.reduce((s, c) => s + c[0], 0) / coords.length;
    const lng = normalizeLng(coords.reduce((s, c) => s + c[1], 0) / coords.length);
    return [lat, lng];
}

function roughFeatureAreaScore(feature) {
    const geom = feature?.geometry;
    if (!geom || !Array.isArray(geom.coordinates)) return 0;
    let minLat = 90;
    let maxLat = -90;
    let minLng = 180;
    let maxLng = -180;

    const walk = (node) => {
        if (!Array.isArray(node)) return;
        if (typeof node[0] === 'number' && typeof node[1] === 'number') {
            const lng = normalizeLng(node[0]);
            const lat = node[1];
            if (lat < minLat) minLat = lat;
            if (lat > maxLat) maxLat = lat;
            if (lng < minLng) minLng = lng;
            if (lng > maxLng) maxLng = lng;
            return;
        }
        node.forEach(walk);
    };
    walk(geom.coordinates);

    const dLat = Math.max(0.001, maxLat - minLat);
    const dLngRaw = Math.max(0.001, maxLng - minLng);
    const dLng = Math.min(dLngRaw, 360 - dLngRaw);
    return Math.log10(dLat * dLng + 1);
}

function countryFillForKey(key, theme) {
    const palette = theme === 'dark' ? PALETTE_DARK : PALETTE_LIGHT;
    let hash = 0;
    const text = String(key || 'UNKNOWN');
    for (let i = 0; i < text.length; i++) {
        hash = ((hash << 5) - hash) + text.charCodeAt(i);
        hash |= 0;
    }
    return palette[Math.abs(hash) % palette.length];
}

function countryFillForFeature(feature, theme) {
    const p = feature?.properties || {};
    const rawIndex = Number(p.MAPCOLOR13 ?? p.mapcolor13 ?? p.MAPCOLOR9 ?? p.mapcolor9);
    if (Number.isFinite(rawIndex) && rawIndex > 0) {
        const palette = theme === 'dark' ? PALETTE_DARK : PALETTE_LIGHT;
        return palette[(Math.floor(rawIndex) - 1) % palette.length];
    }
    return countryFillForKey(featureKey(feature), theme);
}

function collectGridSquares(spots, resolution) {
    const squareData = {};
    spots.forEach(spot => {
        let loc = (spot.locator || '').substring(0, resolution);
        if (loc.length < resolution) loc = (spot.locator || '').substring(0, 4);
        if (loc.length < 4) return;
        if (!squareData[loc]) squareData[loc] = { snrSum: 0, count: 0, bands: {} };
        squareData[loc].snrSum += spot.snr;
        squareData[loc].count += 1;
        squareData[loc].bands[spot.band] = (squareData[loc].bands[spot.band] || 0) + 1;
    });
    return squareData;
}

function computeConvexHullRing(coords) {
    if (!Array.isArray(coords) || coords.length < 3) return [];

    const normalized = coords
        .map(c => [Number(c[0]), Number(c[1])])
        .filter(c => Number.isFinite(c[0]) && Number.isFinite(c[1]));
    if (normalized.length < 3) return [];

    const unique = Array.from(new Set(normalized.map(c => `${c[0].toFixed(6)},${c[1].toFixed(6)}`)))
        .map(s => s.split(',').map(Number));
    if (unique.length < 3) return [];

    unique.sort((a, b) => (a[0] - b[0]) || (a[1] - b[1]));

    const cross = (o, a, b) => ((a[0] - o[0]) * (b[1] - o[1])) - ((a[1] - o[1]) * (b[0] - o[0]));

    const lower = [];
    for (const p of unique) {
        while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], p) <= 0) {
            lower.pop();
        }
        lower.push(p);
    }

    const upper = [];
    for (let i = unique.length - 1; i >= 0; i -= 1) {
        const p = unique[i];
        while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], p) <= 0) {
            upper.pop();
        }
        upper.push(p);
    }

    const hull = lower.slice(0, -1).concat(upper.slice(0, -1));
    if (hull.length < 3) return [];

    hull.push(hull[0]);
    return hull;
}

export function selectProminentDxccLabels(featureCollection, center, options = {}) {
    const features = featureCollection?.features || [];
    const maxLabels = Number(options.maxLabels ?? 28);
    const minDistanceKm = Number(options.minDistanceKm ?? 900);
    const includeSupplemental = options.includeSupplemental ?? (features.length > 50);

    const candidates = [];
    for (const feature of features) {
        const prefix = featurePrefix(feature);
        if (!prefix) continue;
        const anchor = featureAnchor(feature);
        if (!anchor) continue;

        const [lat, lng] = anchor;
        const distanceKm = haversineKm(center[0], center[1], lat, lng);
        if (distanceKm > ANTIPODE_KM - 300) continue;

        const p = feature.properties || {};
        const labelRank = Number(p.LABELRANK ?? 4);
        const pop = Number(p.POP_EST ?? 0);
        const popScore = Math.log10(Math.max(pop, 1));
        const areaScore = roughFeatureAreaScore(feature);
        const score = areaScore + (0.7 * popScore) - (0.45 * labelRank) - (distanceKm / 100000);

        candidates.push({ key: featureKey(feature), name: featureName(feature), prefix, lat, lng, distanceKm, score });
    }

    if (includeSupplemental) {
        for (const entity of SUPPLEMENTAL_DXCC_ENTITIES) {
            const distanceKm = haversineKm(center[0], center[1], entity.lat, entity.lng);
            if (distanceKm > ANTIPODE_KM - 300) continue;

            const score = 4.2 + Number(entity.scoreBoost || 0) - (distanceKm / 90000);
            candidates.push({
                key: entity.key,
                name: entity.name,
                prefix: entity.prefix,
                lat: entity.lat,
                lng: entity.lng,
                distanceKm,
                score
            });
        }
    }

    candidates.sort((a, b) => (b.score - a.score) || (a.distanceKm - b.distanceKm));

    const out = [];
    const usedPrefix = new Set();
    for (const c of candidates) {
        if (out.length >= maxLabels) break;
        if (usedPrefix.has(c.prefix)) continue;
        if (out.some(p => haversineKm(p.lat, p.lng, c.lat, c.lng) < minDistanceKm)) continue;
        out.push(c);
        usedPrefix.add(c.prefix);
    }
    return out;
}

function getFilteredSpots(spots, renderCtx) {
    return spots.filter(spot => {
        if (renderCtx.minSnrMode === 'ssb' && spot.snr < renderCtx.ssbMinDb) return false;
        if (renderCtx.minSnrMode === 'cw' && spot.snr < renderCtx.cwMinDb) return false;
        if (!renderCtx.enabledBands.has(spot.band)) return false;
        if (renderCtx.selectedBand !== 'all' && spot.band !== renderCtx.selectedBand) return false;
        return true;
    });
}

export function createAzimuthRenderPlan({ featureCollection, center, spots = [], style = 'grid-snr', theme = 'light', zoomLevel } = {}) {
    const z = typeof zoomLevel === 'number' ? zoomLevel : state.zoom;
    const countryFillCacheKey = `${theme}:${featureCollection?.features?.length || 0}`;
    let countryFillMap = state.countryFillCache.get(countryFillCacheKey);
    if (!countryFillMap) {
        countryFillMap = new Map((featureCollection?.features || []).map(f => [featureKey(f), countryFillForFeature(f, theme)]));
        state.countryFillCache.set(countryFillCacheKey, countryFillMap);
        if (state.countryFillCache.size > 6) {
            const firstKey = state.countryFillCache.keys().next().value;
            state.countryFillCache.delete(firstKey);
        }
    }

    const labelCacheKey = `${center[0].toFixed(3)}:${center[1].toFixed(3)}:${z.toFixed(2)}:${theme}:${state.horizonKm}:${state.dxccLabelDensity.toFixed(1)}`;
    let labels = state.dxccLabelCache.get(labelCacheKey);
    if (!labels) {
        const baseLodTable = {
            maxLabels: z <= 1.1 ? 18 : z <= 1.4 ? 28 : z <= 1.8 ? 42 : z <= 2.2 ? 62 : 90,
            minDistanceKm: z <= 1.1 ? 1400 : z <= 1.4 ? 1150 : z <= 1.8 ? 900 : z <= 2.2 ? 700 : 520
        };
        const adjustedLod = state.dxccLabelDensity === 0
            ? { maxLabels: 0, minDistanceKm: 20015 }
            : z >= DXCC_SHOW_ALL_ZOOM_THRESHOLD
                ? { maxLabels: 2000, minDistanceKm: 0 }
                : {
                    maxLabels: Math.max(3, Math.round(baseLodTable.maxLabels * state.dxccLabelDensity)),
                    minDistanceKm: Math.max(100, Math.round(baseLodTable.minDistanceKm / state.dxccLabelDensity))
                };
        labels = selectProminentDxccLabels(featureCollection, center, adjustedLod).map(l => ({ key: l.key, prefix: l.prefix, lat: Number(l.lat.toFixed(3)), lng: Number(l.lng.toFixed(3)) }));

        state.dxccLabelCache.set(labelCacheKey, labels);
        if (state.dxccLabelCache.size > 12) {
            const firstKey = state.dxccLabelCache.keys().next().value;
            state.dxccLabelCache.delete(firstKey);
        }
    }

    const azimuthLabels = computeAzimuthLabelSpecs(center, 30).map(l => ({
        bearing: l.bearing,
        label: l.label,
        lat: Number(l.lat.toFixed(3)),
        lng: Number(l.lng.toFixed(3))
    }));

    const overlaySummary = { style: 'grid-snr', itemCount: spots.length };

    return {
        center: [Number(center[0].toFixed(3)), Number(center[1].toFixed(3))],
        theme,
        countryFillMap,
        dxccLabels: labels,
        azimuthLabels,
        overlaySummary,
        zoom: z
    };
}

function projectToCanvas(lat, lng, width, height, options = {}) {
    const applyZoom = options.applyZoom !== false;
    const p = projectAeqdNormalized(state.center, [lat, lng]);
    if (!p.visible) return null;

    const distanceKm = p.c * EARTH_RADIUS_KM;
    if (distanceKm > state.horizonKm) return null;

    const radius = Math.min(width, height) * 0.47;
    const scale = (radius * (applyZoom ? state.zoom : 1)) / Math.PI;
    return { x: width / 2 + p.x * scale, y: height / 2 - p.y * scale };
}

function drawBackground(ctx, width, height) {
    ctx.clearRect(0, 0, width, height);
    ctx.fillStyle = state.theme === 'dark' ? '#13212c' : '#87D1EC';
    ctx.fillRect(0, 0, width, height);
}

function drawWorld(ctx, width, height, plan) {
    if (!state.worldGeoJson?.features?.length) return;
    const featureFill = plan.countryFillMap || new Map();
    for (const feature of state.worldGeoJson.features) {
        const key = featureKey(feature);
        const fill = featureFill.get(key);
        if (!fill) continue;
        const geom = feature.geometry;
        if (!geom) continue;
        ctx.fillStyle = fill;
        ctx.globalAlpha = state.theme === 'dark' ? 0.52 : 0.64;
        ctx.strokeStyle = state.theme === 'dark' ? '#263340' : '#30353a';
        ctx.lineWidth = 0.5;

        const drawRing = (ring) => {
            let segment = [];
            const flush = () => {
                if (segment.length < 3) {
                    segment = [];
                    return;
                }
                ctx.beginPath();
                ctx.moveTo(segment[0].x, segment[0].y);
                for (let i = 1; i < segment.length; i += 1) {
                    ctx.lineTo(segment[i].x, segment[i].y);
                }
                ctx.closePath();
                ctx.fill();
                ctx.stroke();
                segment = [];
            };

            for (const [lng, lat] of ring) {
                const p = projectToCanvas(lat, lng, width, height);
                if (!p) {
                    flush();
                    continue;
                }
                segment.push(p);
            }
            flush();
        };

        if (geom.type === 'Polygon') {
            for (const ring of geom.coordinates || []) drawRing(ring);
        } else if (geom.type === 'MultiPolygon') {
            for (const poly of geom.coordinates || []) for (const ring of poly || []) drawRing(ring);
        }
    }
    ctx.globalAlpha = 1;
}

function getWorldLayerCacheKey(width, height) {
    const cLat = Number(state.center[0]).toFixed(3);
    const cLng = Number(state.center[1]).toFixed(3);
    return `${width}x${height}:${state.theme}:${state.zoom.toFixed(2)}:${state.horizonKm}:${cLat}:${cLng}`;
}

function drawWorldCached(ctx, width, height, plan) {
    const cacheKey = getWorldLayerCacheKey(width, height);
    if (state.worldLayerCache.key === cacheKey && state.worldLayerCache.canvas) {
        ctx.drawImage(state.worldLayerCache.canvas, 0, 0, width, height);
        return;
    }

    let layerCanvas = state.worldLayerCache.canvas;
    if (!layerCanvas) {
        if (typeof OffscreenCanvas !== 'undefined') {
            layerCanvas = new OffscreenCanvas(Math.max(1, width), Math.max(1, height));
        } else if (typeof document !== 'undefined') {
            layerCanvas = document.createElement('canvas');
        }
    }

    if (!layerCanvas) {
        drawWorld(ctx, width, height, plan);
        return;
    }

    layerCanvas.width = Math.max(1, width);
    layerCanvas.height = Math.max(1, height);

    const lctx = layerCanvas.getContext('2d');
    if (!lctx) {
        drawWorld(ctx, width, height, plan);
        return;
    }

    lctx.clearRect(0, 0, width, height);
    drawWorld(lctx, width, height, plan);

    state.worldLayerCache.key = cacheKey;
    state.worldLayerCache.canvas = layerCanvas;
    ctx.drawImage(layerCanvas, 0, 0, width, height);
}

function drawAzimuthLabels(ctx, width, height, plan) {
    ctx.fillStyle = state.theme === 'dark' ? '#d7e4ee' : '#304252';
    ctx.font = '400 10px sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    for (const label of plan.azimuthLabels) {
        const p = projectToCanvas(label.lat, label.lng, width, height, { applyZoom: false });
        if (p) ctx.fillText(label.label, p.x, p.y);
    }
}

function unprojectFromCanvas(x, y, width, height, options = {}) {
    const applyZoom = options.applyZoom !== false;
    const radius = Math.min(width, height) * 0.47;
    const scale = (radius * (applyZoom ? state.zoom : 1)) / Math.PI;
    if (!scale) return null;

    const nx = (x - (width / 2)) / scale;
    const ny = -((y - (height / 2)) / scale);
    const rho = Math.hypot(nx, ny);
    if (!Number.isFinite(rho) || rho > MAX_VISIBLE_C) return null;

    const lat0 = degToRad(state.center[0]);
    const lon0 = degToRad(state.center[1]);

    if (rho === 0) {
        return { lat: state.center[0], lng: state.center[1], c: 0 };
    }

    const sinC = Math.sin(rho);
    const cosC = Math.cos(rho);
    const lat = Math.asin((cosC * Math.sin(lat0)) + ((ny * sinC * Math.cos(lat0)) / rho));
    const lon = lon0 + Math.atan2(nx * sinC, (rho * Math.cos(lat0) * cosC) - (ny * Math.sin(lat0) * sinC));

    return {
        lat: radToDeg(lat),
        lng: normalizeLng(radToDeg(lon)),
        c: rho
    };
}

function hexToRgb(hex) {
    const value = String(hex || '').replace('#', '');
    if (value.length !== 6) return [0, 0, 0];
    return [
        Number.parseInt(value.slice(0, 2), 16),
        Number.parseInt(value.slice(2, 4), 16),
        Number.parseInt(value.slice(4, 6), 16)
    ];
}

function blendOverlayColors(base, color, alpha) {
    if (alpha <= 0) return base;
    const nextAlpha = base.a + (alpha * (1 - base.a));
    if (nextAlpha <= 0) return base;
    return {
        r: ((base.r * base.a) + (color[0] * alpha * (1 - base.a))) / nextAlpha,
        g: ((base.g * base.a) + (color[1] * alpha * (1 - base.a))) / nextAlpha,
        b: ((base.b * base.a) + (color[2] * alpha * (1 - base.a))) / nextAlpha,
        a: nextAlpha
    };
}

function drawGrayline(ctx, width, height) {
    const bucket = Math.floor(Date.now() / (5 * 60 * 1000));
    const key = `${width}x${height}:${state.theme}:${state.zoom.toFixed(2)}:${state.horizonKm}:${state.center[0].toFixed(3)}:${state.center[1].toFixed(3)}:${bucket}`;

    if (state.graylineOverlayCache.key === key && state.graylineOverlayCache.canvas) {
        ctx.drawImage(state.graylineOverlayCache.canvas, 0, 0, width, height);
        return;
    }

    const overlayCanvas = typeof OffscreenCanvas !== 'undefined'
        ? new OffscreenCanvas(Math.max(1, width), Math.max(1, height))
        : document.createElement('canvas');
    overlayCanvas.width = Math.max(1, width);
    overlayCanvas.height = Math.max(1, height);

    const overlayCtx = overlayCanvas.getContext('2d');
    if (!overlayCtx) return;

    const subsolarPoint = getSubsolarPoint(new Date(bucket * 5 * 60 * 1000));
    const twilightFill = hexToRgb(state.theme === 'dark' ? '#9a8371' : '#b08b72');
    const nightFill = hexToRgb(state.theme === 'dark' ? '#01050a' : '#182534');
    const imageData = overlayCtx.createImageData(width, height);
    const data = imageData.data;

    for (let y = 0; y < height; y += 1) {
        for (let x = 0; x < width; x += 1) {
            const geo = unprojectFromCanvas(x + 0.5, y + 0.5, width, height);
            if (!geo) continue;

            const distanceKm = geo.c * EARTH_RADIUS_KM;
            if (distanceKm > state.horizonKm) continue;

            const { graylineOpacity, nightOpacity } = getGraylineOverlayOpacities(geo.lat, geo.lng, subsolarPoint);
            if (graylineOpacity <= 0 && nightOpacity <= 0) continue;

            let pixel = { r: 0, g: 0, b: 0, a: 0 };
            pixel = blendOverlayColors(pixel, twilightFill, graylineOpacity);
            pixel = blendOverlayColors(pixel, nightFill, nightOpacity);

            const offset = (y * width * 4) + (x * 4);
            data[offset] = Math.round(pixel.r);
            data[offset + 1] = Math.round(pixel.g);
            data[offset + 2] = Math.round(pixel.b);
            data[offset + 3] = Math.round(pixel.a * 255);
        }
    }

    overlayCtx.putImageData(imageData, 0, 0);
    state.graylineOverlayCache = {
        key,
        canvas: overlayCanvas
    };
    ctx.drawImage(overlayCanvas, 0, 0, width, height);
}

function drawAzimuthIndicator(ctx, width, height) {
    const centerX = width / 2;
    const centerY = height / 2;
    const maxDist = Math.max(1200, Math.min(state.horizonKm - 12, ANTIPODE_KM - 12));
    const guideInnerDist = Math.max(500, maxDist - 2200);
    const tickInnerDist = Math.max(600, maxDist - 420);
    const labelDist = Math.max(500, maxDist - 900);

    ctx.strokeStyle = state.theme === 'dark' ? 'rgba(220,230,240,0.45)' : 'rgba(30,42,55,0.45)';
    ctx.fillStyle = state.theme === 'dark' ? 'rgba(225,236,245,0.8)' : 'rgba(23,35,46,0.8)';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.font = '500 9px Verdana, Arial, sans-serif';

    // Thin guide rays every 30 degrees, stretched almost from the target to the border.
    ctx.save();
    ctx.lineWidth = 0.7;
    ctx.strokeStyle = state.theme === 'dark' ? 'rgba(220,230,240,0.28)' : 'rgba(30,42,55,0.28)';
    for (let b = 0; b < 360; b += 30) {
        const innerGeo = destinationPoint(state.center[0], state.center[1], b, guideInnerDist);
        const outerGeo = destinationPoint(state.center[0], state.center[1], b, maxDist);
        const pInner = projectToCanvas(innerGeo[0], innerGeo[1], width, height, { applyZoom: false });
        const pOuter = projectToCanvas(outerGeo[0], outerGeo[1], width, height, { applyZoom: false });
        if (!pInner || !pOuter) continue;

        ctx.beginPath();
        ctx.moveTo(pInner.x, pInner.y);
        ctx.lineTo(pOuter.x, pOuter.y);
        ctx.stroke();
    }
    ctx.restore();

    // Fine border ticks every 5 degrees around the visible edge.
    for (let b = 0; b < 360; b += 5) {
        const outerGeo = destinationPoint(state.center[0], state.center[1], b, maxDist);
        const innerGeo = destinationPoint(state.center[0], state.center[1], b, tickInnerDist);
        const pOuter = projectToCanvas(outerGeo[0], outerGeo[1], width, height, { applyZoom: false });
        const pInner = projectToCanvas(innerGeo[0], innerGeo[1], width, height, { applyZoom: false });
        if (!pOuter || !pInner) continue;

        const isMajor = (b % 30) === 0;
        ctx.lineWidth = isMajor ? 1.0 : 0.45;
        ctx.beginPath();
        ctx.moveTo(pInner.x, pInner.y);
        ctx.lineTo(pOuter.x, pOuter.y);
        ctx.stroke();

        if (isMajor) {
            const labelGeo = destinationPoint(state.center[0], state.center[1], b, labelDist);
            const pLabel = projectToCanvas(labelGeo[0], labelGeo[1], width, height, { applyZoom: false });
            if (pLabel) {
                const text = b === 0 ? 'N' : b === 90 ? 'E' : b === 180 ? 'S' : b === 270 ? 'W' : `${b}°`;
                ctx.fillText(text, pLabel.x, pLabel.y);
            }
        }
    }

    const ringDist = Math.max(1000, maxDist - 1800);
    for (let r = 1; r <= 3; r++) {
        const dist = (ringDist * r) / 3;
        ctx.beginPath();
        let started = false;
        for (let b = 0; b <= 360; b += 2) {
            const geo = destinationPoint(state.center[0], state.center[1], b, dist);
            const p = projectToCanvas(geo[0], geo[1], width, height, { applyZoom: false });
            if (!p) {
                started = false;
                continue;
            }
            if (!started) {
                ctx.moveTo(p.x, p.y);
                started = true;
            } else {
                ctx.lineTo(p.x, p.y);
            }
        }
        ctx.strokeStyle = state.theme === 'dark' ? 'rgba(220,230,240,0.14)' : 'rgba(20,30,40,0.12)';
        ctx.lineWidth = 0.6;
        ctx.stroke();
    }
}

function drawDxccLabels(ctx, width, height, plan) {
    const centerX = width / 2;
    const centerY = height / 2;
    const occupied = [];
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    const textColor = state.theme === 'dark' ? '#f5e6b2' : '#4a3218';

    for (const label of plan.dxccLabels) {
        const p = projectToCanvas(label.lat, label.lng, width, height);
        if (!p) continue;

        const radial = Math.hypot(p.x - centerX, p.y - centerY) / Math.max(1, Math.min(width, height) * 0.47 * state.zoom);
        const fontSize = Math.max(8, Math.min(11.5, 10.8 + (state.zoom - 1.2) * 1.1 - radial * 2.1));
        ctx.font = `400 ${fontSize.toFixed(1)}px Verdana, Arial, sans-serif`;

        const labelW = ctx.measureText(label.prefix).width + 4;
        const labelH = fontSize + 2;
        const box = {
            x0: p.x - labelW / 2,
            y0: p.y - labelH / 2,
            x1: p.x + labelW / 2,
            y1: p.y + labelH / 2
        };

        const collides = occupied.some(o => !(box.x1 < o.x0 || box.x0 > o.x1 || box.y1 < o.y0 || box.y0 > o.y1));
        if (collides) continue;
        occupied.push(box);

        ctx.fillStyle = state.theme === 'dark' ? 'rgba(0,0,0,0.45)' : 'rgba(255,255,255,0.72)';
        ctx.beginPath();
        if (typeof ctx.roundRect === 'function') {
            ctx.roundRect(box.x0 - 1, box.y0 - 0.5, labelW + 2, labelH + 1, 3);
        } else {
            // Fallback for browsers that don't implement roundRect on CanvasRenderingContext2D.
            ctx.rect(box.x0 - 1, box.y0 - 0.5, labelW + 2, labelH + 1);
        }
        ctx.fill();

        ctx.fillStyle = textColor;
        ctx.fillText(label.prefix, p.x, p.y);
    }
}

function drawActiveAreaOverlay(ctx, width, height, filteredSpots, maxClusterDist) {
    const hasTurf = typeof turf !== 'undefined';

    if (!hasTurf) {
        filteredSpots.forEach(spot => {
            const c = projectToCanvas(spot.lat, spot.lng, width, height);
            if (!c) return;
            const color = bandColors[spot.band] || bandColors.all;
            ctx.beginPath();
            ctx.arc(c.x, c.y, 4.5, 0, Math.PI * 2);
            ctx.strokeStyle = color;
            ctx.lineWidth = 2;
            ctx.globalAlpha = 0.65;
            ctx.stroke();
            ctx.globalAlpha = 0.5;
            ctx.fillStyle = color;
            ctx.fill();
        });
        ctx.globalAlpha = 1;
        return;
    }

    const pointsByBand = {};
    const seenCoordsByBand = {};
    const debugBands = [];

    filteredSpots.forEach(spot => {
        if (!pointsByBand[spot.band]) {
            pointsByBand[spot.band] = [];
            seenCoordsByBand[spot.band] = new Set();
        }

        const coordKey = `${spot.lng},${spot.lat}`;
        if (seenCoordsByBand[spot.band].has(coordKey)) return;
        seenCoordsByBand[spot.band].add(coordKey);
        pointsByBand[spot.band].push(turf.point([spot.lng, spot.lat]));
    });

    Object.keys(pointsByBand).forEach(band => {
        const pts = pointsByBand[band];
        const color = bandColors[band] || bandColors.all;

        const isolatedPts = [];
        const hullRings = [];

        const coords = pts.map(p => p.geometry.coordinates);
        if (coords.length >= 3) {
            let ring = [];

            if (coords.length >= 6) {
                const centroid = coords.reduce((acc, c) => {
                    acc.lat += Number(c[1]);
                    acc.lng += Number(c[0]);
                    return acc;
                }, { lat: 0, lng: 0 });
                centroid.lat /= coords.length;
                centroid.lng /= coords.length;

                const ranked = coords
                    .map(c => ({
                        coord: c,
                        dist: haversineKm(centroid.lat, centroid.lng, Number(c[1]), Number(c[0]))
                    }))
                    .sort((a, b) => a.dist - b.dist);

                const keepCount = Math.max(6, Math.ceil(ranked.length * 0.75));
                const coreCoords = ranked.slice(0, keepCount).map(r => r.coord);
                ring = computeConvexHullRing(coreCoords);
            }

            if (ring.length < 4) {
                ring = computeConvexHullRing(coords);
            }

            if (ring.length >= 4) {
                hullRings.push(ring);
                if (hasTurf) {
                    try {
                        const poly = turf.polygon([ring]);
                        pts.forEach(p => {
                            try {
                                if (!turf.booleanPointInPolygon(p, poly)) isolatedPts.push(p);
                            } catch (_) {
                                isolatedPts.push(p);
                            }
                        });
                    } catch (_) {
                        pts.forEach(p => isolatedPts.push(p));
                    }
                } else {
                    pts.forEach(p => isolatedPts.push(p));
                }
            } else {
                pts.forEach(p => isolatedPts.push(p));
            }
        } else {
            pts.forEach(p => isolatedPts.push(p));
        }

        hullRings.forEach(ring => {
            ctx.beginPath();
            let started = false;
            let visibleCount = 0;
            ring.forEach(([lng, lat]) => {
                const p = projectToCanvas(lat, lng, width, height);
                if (!p) {
                    started = false;
                    return;
                }
                if (!started) {
                    ctx.moveTo(p.x, p.y);
                    started = true;
                } else {
                    ctx.lineTo(p.x, p.y);
                }
                visibleCount += 1;
            });
            if (visibleCount < 3) return;
            ctx.closePath();
            ctx.fillStyle = color;
            ctx.globalAlpha = 0.34;
            ctx.fill();
            ctx.globalAlpha = 1;
            ctx.strokeStyle = color;
            ctx.lineWidth = 1.8;
            ctx.stroke();
        });

        isolatedPts.forEach(p => {
            const [lng, lat] = p.geometry.coordinates;
            const c = projectToCanvas(lat, lng, width, height);
            if (!c) return;
            ctx.beginPath();
            ctx.arc(c.x, c.y, 4.5, 0, Math.PI * 2);
            ctx.strokeStyle = color;
            ctx.lineWidth = 2;
            ctx.globalAlpha = 0.65;
            ctx.stroke();
            ctx.globalAlpha = 0.5;
            ctx.fillStyle = color;
            ctx.fill();
        });

        debugBands.push({
            band,
            points: pts.length,
            hullCount: hullRings.length,
            isolated: isolatedPts.length
        });
    });

    ctx.globalAlpha = 1;
    if (typeof window !== 'undefined') {
        window.__azimuthActiveAreaDebug = {
            hasTurf,
            debugBands,
            maxClusterDist,
            ts: Date.now()
        };
    }
}

function drawSpots(ctx, width, height, filteredSpots, style, gridSquares, maxClusterDist) {
    if (style === 'grid-snr') {
        const squares = gridSquares || collectGridSquares(filteredSpots, getGridResolution());
        for (const loc of Object.keys(squares)) {
            const bounds = locatorToBounds(loc);
            if (!bounds) continue;
            const lat = (bounds[0][0] + bounds[1][0]) / 2;
            const lng = (bounds[0][1] + bounds[1][1]) / 2;
            const p = projectToCanvas(lat, lng, width, height);
            if (!p) continue;
            const entry = squares[loc];
            const avgSnr = entry.snrSum / Math.max(1, entry.count);
            let dominantBand = 'all';
            let maxCount = 0;
            for (const band of Object.keys(entry.bands)) {
                if (entry.bands[band] > maxCount) {
                    maxCount = entry.bands[band];
                    dominantBand = band;
                }
            }
            ctx.fillStyle = bandColors[dominantBand] || bandColors.all;
            ctx.globalAlpha = avgSnr >= 10 ? 0.9 : avgSnr >= 0 ? 0.65 : 0.35;
            ctx.fillRect(p.x - 4, p.y - 4, 8, 8);
        }
        ctx.globalAlpha = 1;
        return;
    }

    for (const spot of filteredSpots) {
        const p = projectToCanvas(spot.lat, spot.lng, width, height);
        if (!p) continue;
        const color = bandColors[spot.band] || bandColors.all;
        ctx.beginPath();
        ctx.arc(p.x, p.y, 4, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.8;
        ctx.fill();
    }
    ctx.globalAlpha = 1;
}

function withHorizonClip(ctx, width, height, drawFn) {
    const radiusBase = Math.min(width, height) * 0.47;
    const scale = (radiusBase * state.zoom) / Math.PI;
    const horizonAngular = Math.min(MAX_VISIBLE_C, state.horizonKm / EARTH_RADIUS_KM);
    const clipRadius = Math.max(1, scale * horizonAngular);

    ctx.save();
    ctx.beginPath();
    ctx.arc(width / 2, height / 2, clipRadius, 0, Math.PI * 2);
    ctx.clip();
    drawFn();
    ctx.restore();
}

export function renderAzimuthScene({ spots = [], style } = {}) {
    if (!state.enabled) return;
    const perfEnabled = typeof window !== 'undefined' && window.localStorage?.getItem('azimuthProfile') === 'true';
    const nowMs = () => (typeof performance !== 'undefined' && typeof performance.now === 'function') ? performance.now() : Date.now();
    const profile = perfEnabled ? { start: nowMs() } : null;

    const canvas = ensureCanvas();
    if (!canvas || !state.ctx || !state.worldGeoJson) return;
    resizeCanvasToElement(canvas);

    state.lastSpots = spots;
    const requestedStyle = style || (document.querySelector('input[name="style-select"]:checked')?.value || 'grid-snr');
    const resolvedStyle = requestedStyle === 'grid-snr' ? 'grid-snr' : 'grid-snr';
    state.lastStyle = resolvedStyle;

    const renderCtx = {
        minSnrMode: getMinSnrMode(),
        ssbMinDb: parseInt(document.getElementById('ssb-min-db')?.value || '0', 10),
        cwMinDb: parseInt(document.getElementById('cw-min-db')?.value || '-15', 10),
        selectedBand: getSelectedBand(),
        enabledBands: getEnabledBands(),
        maxClusterDist: Math.max(100, parseInt(document.getElementById('cluster-distance')?.value || '500', 10) || 500)
    };

    const filteredSpots = getFilteredSpots(spots, renderCtx);
    const gridSquares = resolvedStyle === 'grid-snr' ? collectGridSquares(filteredSpots, getGridResolution()) : null;

    const width = canvas.clientWidth || 1;
    const height = canvas.clientHeight || 1;
    if (profile) profile.planStart = nowMs();
    const plan = createAzimuthRenderPlan({
        featureCollection: state.worldGeoJson,
        center: state.center,
        spots: filteredSpots,
        style: resolvedStyle,
        theme: state.theme,
        zoomLevel: state.zoom
    });
    if (profile) profile.planEnd = nowMs();

    if (profile) profile.drawStart = nowMs();
    drawBackground(state.ctx, width, height);
    withHorizonClip(state.ctx, width, height, () => {
        if (profile) profile.worldStart = nowMs();
        drawWorldCached(state.ctx, width, height, plan);
        if (profile) profile.worldEnd = nowMs();

        if (getGraylineEnabled()) {
            drawGrayline(state.ctx, width, height);
        }

        if (profile) profile.spotsStart = nowMs();
        drawSpots(state.ctx, width, height, filteredSpots, resolvedStyle, gridSquares, renderCtx.maxClusterDist);
        if (profile) profile.spotsEnd = nowMs();

        if (state.dxccLabelsEnabled) {
            if (profile) profile.dxccStart = nowMs();
            drawDxccLabels(state.ctx, width, height, plan);
            if (profile) profile.dxccEnd = nowMs();
        }
    });

    if (profile) profile.scaleStart = nowMs();
    if (state.ns6tIndicatorEnabled) {
        drawAzimuthIndicator(state.ctx, width, height);
    } else {
        drawAzimuthLabels(state.ctx, width, height, plan);
    }
    if (profile) profile.scaleEnd = nowMs();

    if (profile) {
        const end = nowMs();
        // eslint-disable-next-line no-console
        console.debug('[azimuthProfile]', {
            totalMs: Number((end - profile.start).toFixed(2)),
            planMs: Number(((profile.planEnd || end) - (profile.planStart || profile.start)).toFixed(2)),
            worldMs: Number(((profile.worldEnd || end) - (profile.worldStart || profile.start)).toFixed(2)),
            spotsMs: Number(((profile.spotsEnd || end) - (profile.spotsStart || profile.start)).toFixed(2)),
            scaleMs: Number(((profile.scaleEnd || end) - (profile.scaleStart || profile.start)).toFixed(2)),
            dxccMs: state.dxccLabelsEnabled
                ? Number(((profile.dxccEnd || end) - (profile.dxccStart || profile.start)).toFixed(2))
                : 0,
            style: resolvedStyle,
            zoom: state.zoom
        });
    }
}
