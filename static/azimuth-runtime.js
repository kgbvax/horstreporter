import { bandColors, getCountryColoringEnabled, getEnabledBands, getForecastEnabled, getGraylineEnabled, getGraylineOverlayOpacities, getSubsolarPoint, getMinSnrMode, getSelectedBand, locatorToBounds, getGridResolution, greatCirclePoints, degToRad, radToDeg, haversineKm, hexToRgb, blendOverlayColors, normalizeLongitude as normalizeLng } from './utils.js';

import { radialLine, strokeCircle } from './canvas-draw.js';

const EARTH_RADIUS_KM = 6371;
const ANTIPODE_KM = Math.PI * EARTH_RADIUS_KM;
const MAX_VISIBLE_C = Math.PI - 0.02;
const DXCC_SHOW_ALL_ZOOM_THRESHOLD = 5.0;
const AZIMUTH_SCALE_CLEARANCE_PX = 12;
const GRAYLINE_RECOMPUTE_MIN_INTERVAL_MS = 320;
// Active-area SNR field: value assigned to grid vertices with no nearby data,
// and the "data presence" contour just above it that forms the smooth outer
// boundary. NO_DATA sits far below any real SNR so the presence iso-line
// interpolates a sub-cell falloff between data and empty space.
const AZIMUTH_FIELD_NO_DATA = -100;
const AZIMUTH_FIELD_PRESENCE = -40;

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
        canvas: null,
        computedAtMs: 0
    },
    gridCellProjectionCache: {
        key: '',
        cells: new Map()
    },
    worldLayerCache: {
        key: '',
        canvas: null
    },
    hiddenGridSquaresCount: 0,
    isDragging: false,
    antennaOverlay: {
        enabled: false,
        stationLat: null,
        stationLng: null,
        stationLocator: '',
        stationName: '',
        azimuthDeg: 0,
        beamwidth3dBDeg: 60,
        mode: 'forward',
        pendingTargetBearingDeg: null,
        pendingTargetLabel: ''
    },
    dxSpotHighlight: {
        enabled: false,
        originLat: null,
        originLng: null,
        spotLat: null,
        spotLng: null,
        label: '',
        pinned: false
    }
};

export function isAzimuthEnabled() {
    return state.enabled;
}

export function getAzimuthHiddenGridSquaresCount() {
    return Math.max(0, Number(state.hiddenGridSquaresCount) || 0);
}

export function setAzimuthEnabled(enabled) {
    state.enabled = Boolean(enabled);
    const mapEl = typeof document !== 'undefined' ? document.getElementById('map') : null;
    const canvas = ensureCanvas();
    if (mapEl) mapEl.style.display = state.enabled ? 'none' : 'block';
    if (canvas) {
        canvas.style.display = state.enabled ? 'block' : 'none';
        canvas.style.pointerEvents = state.enabled ? 'auto' : 'none';
    }
}

export function setAzimuthDragging(dragging) {
    state.isDragging = Boolean(dragging);
}

export function setAzimuthTheme(theme) {
    state.theme = theme === 'dark' ? 'dark' : 'light';
}

export function setAzimuthCenter(center) {
    if (Array.isArray(center) && center.length === 2) {
        state.center = [Number(center[0]) || 0, normalizeLng(Number(center[1]) || 0)];
    }
}

export function getAzimuthCenter() {
    return [state.center[0], state.center[1]];
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

export function setAzimuthAntennaOverlay(overlay = {}) {
    const toFiniteOrNull = (value) => {
        if (value === null || value === undefined || value === '') return null;
        const parsed = Number(value);
        return Number.isFinite(parsed) ? parsed : null;
    };

    const enabled = Boolean(overlay.enabled);
    const stationLat = Number(overlay.stationLat);
    const stationLng = Number(overlay.stationLng);
    const azimuthDeg = Number(overlay.azimuthDeg);
    const beamwidth3dBDeg = Number(overlay.beamwidth3dBDeg);
    const pendingTargetBearingDeg = toFiniteOrNull(overlay.pendingTargetBearingDeg);
    const modeRaw = String(overlay.mode || 'forward').toLowerCase();
    const mode = modeRaw === 'backward' || modeRaw === 'bidirectional' ? modeRaw : 'forward';

    state.antennaOverlay = {
        enabled: enabled && Number.isFinite(stationLat) && Number.isFinite(stationLng) && Number.isFinite(azimuthDeg),
        stationLat: Number.isFinite(stationLat) ? stationLat : null,
        stationLng: Number.isFinite(stationLng) ? normalizeLng(stationLng) : null,
        stationLocator: String(overlay.stationLocator || ''),
        stationName: String(overlay.stationName || ''),
        azimuthDeg: Number.isFinite(azimuthDeg) ? ((azimuthDeg % 360) + 360) % 360 : 0,
        beamwidth3dBDeg: Number.isFinite(beamwidth3dBDeg) ? Math.max(5, Math.min(180, beamwidth3dBDeg)) : 60,
        mode,
        pendingTargetBearingDeg: Number.isFinite(pendingTargetBearingDeg)
            ? ((pendingTargetBearingDeg % 360) + 360) % 360
            : null,
        pendingTargetLabel: String(overlay.pendingTargetLabel || '')
    };
}

// setAzimuthDxSpotHighlight stores the Chase Queue spot highlight (origin →
// spot, plus callsign label). Pass { enabled:false } to clear.
export function setAzimuthDxSpotHighlight(overlay = {}) {
    const spotLat = Number(overlay.spotLat);
    const spotLng = Number(overlay.spotLng);
    const originLat = Number(overlay.originLat);
    const originLng = Number(overlay.originLng);
    state.dxSpotHighlight = {
        enabled: Boolean(overlay.enabled) && Number.isFinite(spotLat) && Number.isFinite(spotLng),
        spotLat: Number.isFinite(spotLat) ? spotLat : null,
        spotLng: Number.isFinite(spotLng) ? normalizeLng(spotLng) : null,
        originLat: Number.isFinite(originLat) ? originLat : null,
        originLng: Number.isFinite(originLng) ? normalizeLng(originLng) : null,
        label: String(overlay.label || ''),
        pinned: Boolean(overlay.pinned)
    };
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
    state.canvas.style.pointerEvents = state.enabled ? 'auto' : 'none';
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

function collectGridSquares(spots, resolution, visibleSpots = spots) {
    const squareData = {};
    spots.forEach(spot => {
        if (String(spot?.sourceType || '').toLowerCase() === 'dxcluster') return;
        let loc = (spot.locator || '').substring(0, resolution);
        if (loc.length < resolution) loc = (spot.locator || '').substring(0, 4);
        if (loc.length < 4) return;
        if (!squareData[loc]) squareData[loc] = { snrSum: 0, count: 0, maxSnr: -Infinity, visibleCount: 0, bands: {} };
        squareData[loc].snrSum += spot.snr;
        squareData[loc].count += 1;
        squareData[loc].maxSnr = Math.max(squareData[loc].maxSnr, Number(spot.snr));
    });

    visibleSpots.forEach(spot => {
        if (String(spot?.sourceType || '').toLowerCase() === 'dxcluster') return;
        let loc = (spot.locator || '').substring(0, resolution);
        if (loc.length < resolution) loc = (spot.locator || '').substring(0, 4);
        if (loc.length < 4 || !squareData[loc]) return;
        squareData[loc].visibleCount += 1;
        squareData[loc].bands[spot.band] = (squareData[loc].bands[spot.band] || 0) + 1;
    });

    Object.keys(squareData).forEach((loc) => {
        if (squareData[loc].visibleCount <= 0) {
            delete squareData[loc];
        }
    });

    return squareData;
}

function drawDxClusterSpots(ctx, width, height, filteredSpots) {
    const dxClusterSpots = filteredSpots.filter((spot) => String(spot?.sourceType || '').toLowerCase() === 'dxcluster');
    for (const spot of dxClusterSpots) {
        const p = projectToCanvas(spot.lat, spot.lng, width, height);
        if (!p) continue;
        const color = bandColors[spot.band] || bandColors.all;

        ctx.beginPath();
        ctx.arc(p.x, p.y, 4.3, 0, Math.PI * 2);
        ctx.strokeStyle = color;
        ctx.lineWidth = 2;
        ctx.globalAlpha = 0.95;
        ctx.stroke();

        ctx.beginPath();
        ctx.arc(p.x, p.y, 1.5, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.95;
        ctx.fill();
    }
    ctx.globalAlpha = 1;
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
    const enforceHorizon = options.enforceHorizon !== false;
    const p = projectAeqdNormalized(state.center, [lat, lng]);
    if (!p.visible) return null;

    const distanceKm = p.c * EARTH_RADIUS_KM;
    if (enforceHorizon && distanceKm > state.horizonKm) return null;

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
    const countryColoringEnabled = getCountryColoringEnabled();
    const featureFill = plan.countryFillMap || new Map();
    const fallbackLandFill = state.theme === 'dark' ? '#8a94a1' : '#e6ebf1';
    for (const feature of state.worldGeoJson.features) {
        const key = featureKey(feature);
        const fill = featureFill.get(key);
        if (!fill) continue;
        const geom = feature.geometry;
        if (!geom) continue;
        ctx.fillStyle = countryColoringEnabled ? fill : fallbackLandFill;
        ctx.globalAlpha = state.theme === 'dark' ? 0.52 : 0.64;
        ctx.strokeStyle = state.theme === 'dark' ? '#263340' : '#30353a';
        ctx.lineWidth = 0.5;

        const drawRing = (ring) => {
            const discontinuityPx = Math.max(24, Math.min(width, height) * 0.34);
            let segment = [];
            let hadBreak = false;
            let prevLng = null;
            let prevLat = null;

            const flush = (isBreak = true) => {
                if (segment.length < 2) {
                    if (isBreak) hadBreak = true;
                    segment = [];
                    return;
                }

                ctx.beginPath();
                ctx.moveTo(segment[0].x, segment[0].y);
                for (let i = 1; i < segment.length; i += 1) {
                    ctx.lineTo(segment[i].x, segment[i].y);
                }

                const first = segment[0];
                const last = segment[segment.length - 1];
                const closingDistance = Math.hypot(last.x - first.x, last.y - first.y);
                const canFill = !hadBreak && segment.length >= 3 && closingDistance <= discontinuityPx;

                if (canFill) {
                    ctx.closePath();
                    ctx.fill();
                }
                ctx.stroke();

                if (isBreak) hadBreak = true;
                segment = [];
            };

            for (const [lng, lat] of ring) {
                const p = projectToCanvas(lat, lng, width, height, { enforceHorizon: false });
                if (!p) {
                    flush(true);
                    prevLng = null;
                    prevLat = null;
                    continue;
                }

                if (segment.length > 0) {
                    const prevPoint = segment[segment.length - 1];
                    const jumpPx = Math.hypot(p.x - prevPoint.x, p.y - prevPoint.y);
                    const wrapsDateline = prevLng !== null && Math.abs(lng - prevLng) > 180;
                    const hasPrevGeo = prevLat !== null && prevLng !== null;
                    let midpointInvisible = false;
                    if (hasPrevGeo) {
                        const dLng = normalizeLng(lng - prevLng);
                        const midLat = (prevLat + lat) / 2;
                        const midLng = normalizeLng(prevLng + (dLng / 2));
                        midpointInvisible = !projectToCanvas(midLat, midLng, width, height, { enforceHorizon: false });
                    }

                    if (wrapsDateline || jumpPx > discontinuityPx || midpointInvisible) {
                        flush(true);
                    }
                }

                segment.push(p);
                prevLng = lng;
                prevLat = lat;
            }
            flush(false);
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
    // Item 5: use reduced center precision during drag to reuse cached world layer
    // across small movements, avoiding per-frame GeoJSON re-projection
    const precision = state.isDragging ? 1 : 3;
    const cLat = Number(state.center[0]).toFixed(precision);
    const cLng = Number(state.center[1]).toFixed(precision);
    const countryColoring = getCountryColoringEnabled() ? 'on' : 'off';
    return `${width}x${height}:${state.theme}:${state.zoom.toFixed(2)}:${state.horizonKm}:${cLat}:${cLng}:${countryColoring}`;
}

function getGridCellProjectionCacheKey(width, height) {
    const cLat = Number(state.center[0]).toFixed(3);
    const cLng = Number(state.center[1]).toFixed(3);
    return `${width}x${height}:${state.zoom.toFixed(2)}:${state.horizonKm}:${cLat}:${cLng}`;
}

function getProjectedGridCellCorners(locator, width, height) {
    const cacheKey = getGridCellProjectionCacheKey(width, height);
    if (state.gridCellProjectionCache.key !== cacheKey) {
        state.gridCellProjectionCache.key = cacheKey;
        state.gridCellProjectionCache.cells = new Map();
    }

    const cached = state.gridCellProjectionCache.cells.get(locator);
    if (cached) return cached;

    const bounds = locatorToBounds(locator);
    if (!bounds) return null;

    const south = bounds[0][0];
    const west = bounds[0][1];
    const north = bounds[1][0];
    const east = bounds[1][1];

    const corners = [
        projectToCanvas(south, west, width, height),
        projectToCanvas(south, east, width, height),
        projectToCanvas(north, east, width, height),
        projectToCanvas(north, west, width, height)
    ];

    const projected = corners.some((pt) => !pt) ? null : corners;
    state.gridCellProjectionCache.cells.set(locator, projected);
    return projected;
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

export function getAzimuthLatLngFromClientPoint(clientX, clientY) {
    const canvas = ensureCanvas();
    if (!canvas || typeof canvas.getBoundingClientRect !== 'function') return null;

    const rect = canvas.getBoundingClientRect();
    const x = Number(clientX) - rect.left;
    const y = Number(clientY) - rect.top;
    if (!Number.isFinite(x) || !Number.isFinite(y)) return null;
    if (x < 0 || y < 0 || x > rect.width || y > rect.height) return null;

    const width = canvas.clientWidth || rect.width || 1;
    const height = canvas.clientHeight || rect.height || 1;
    const geo = unprojectFromCanvas(x, y, width, height);
    if (!geo) return null;

    const distanceKm = geo.c * EARTH_RADIUS_KM;
    if (distanceKm > state.horizonKm) return null;

    return { lat: geo.lat, lng: geo.lng };
}


function drawGrayline(ctx, width, height) {
    const bucket = Math.floor(Date.now() / (5 * 60 * 1000));
    const nowMs = Date.now();
    const key = `${width}x${height}:${state.theme}:${state.zoom.toFixed(2)}:${state.horizonKm}:${state.center[0].toFixed(3)}:${state.center[1].toFixed(3)}:${bucket}`;

    if (state.graylineOverlayCache.key === key && state.graylineOverlayCache.canvas) {
        ctx.drawImage(state.graylineOverlayCache.canvas, 0, 0, width, height);
        return;
    }

    // During active pan/zoom interaction, recomputing grayline every frame is expensive.
    // Reuse the last overlay briefly and refresh at a bounded cadence.
    if (
        state.graylineOverlayCache.canvas &&
        (nowMs - (state.graylineOverlayCache.computedAtMs || 0)) < GRAYLINE_RECOMPUTE_MIN_INTERVAL_MS
    ) {
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
    const maxDim = Math.max(width, height);
    const sampleScale = Math.max(0.2, Math.min(0.4, 900 / Math.max(1, maxDim)));
    const sampleWidth = Math.max(1, Math.round(width * sampleScale));
    const sampleHeight = Math.max(1, Math.round(height * sampleScale));
    const sampleImageData = overlayCtx.createImageData(sampleWidth, sampleHeight);
    const sampleData = sampleImageData.data;
    const alphaNoiseFloor = 0.018;

    for (let sy = 0; sy < sampleHeight; sy += 1) {
        const py = ((sy + 0.5) / sampleHeight) * height;
        for (let sx = 0; sx < sampleWidth; sx += 1) {
            const px = ((sx + 0.5) / sampleWidth) * width;
            const geo = unprojectFromCanvas(px, py, width, height);
            if (!geo) continue;

            const distanceKm = geo.c * EARTH_RADIUS_KM;
            if (distanceKm > state.horizonKm) continue;

            const { graylineOpacity, nightOpacity } = getGraylineOverlayOpacities(geo.lat, geo.lng, subsolarPoint);
            const g = graylineOpacity > alphaNoiseFloor ? graylineOpacity : 0;
            const n = nightOpacity > alphaNoiseFloor ? nightOpacity : 0;
            if (g <= 0 && n <= 0) continue;

            let pixel = { r: 0, g: 0, b: 0, a: 0 };
            pixel = blendOverlayColors(pixel, twilightFill, g);
            pixel = blendOverlayColors(pixel, nightFill, n);
            if (pixel.a <= alphaNoiseFloor) continue;

            const offset = (sy * sampleWidth * 4) + (sx * 4);
            sampleData[offset] = Math.round(pixel.r);
            sampleData[offset + 1] = Math.round(pixel.g);
            sampleData[offset + 2] = Math.round(pixel.b);
            sampleData[offset + 3] = Math.round(pixel.a * 255);
        }
    }

    const sampleCanvas = typeof OffscreenCanvas !== 'undefined'
        ? new OffscreenCanvas(Math.max(1, sampleWidth), Math.max(1, sampleHeight))
        : document.createElement('canvas');
    sampleCanvas.width = Math.max(1, sampleWidth);
    sampleCanvas.height = Math.max(1, sampleHeight);
    const sampleCtx = sampleCanvas.getContext('2d');
    if (!sampleCtx) return;

    sampleCtx.putImageData(sampleImageData, 0, 0);

    overlayCtx.clearRect(0, 0, width, height);
    // Avoid global tint bleed caused by smoothing transparent/tinted samples over large distances.
    overlayCtx.imageSmoothingEnabled = false;
    overlayCtx.imageSmoothingQuality = 'low';
    overlayCtx.drawImage(sampleCanvas, 0, 0, width, height);

    state.graylineOverlayCache = {
        key,
        canvas: overlayCanvas,
        computedAtMs: nowMs
    };
    ctx.drawImage(overlayCanvas, 0, 0, width, height);
}

// bearingFromCenter returns the initial great-circle bearing (0–360°, 0 = N) from
// the station center to a point.
export function bearingFromCenter(lat, lng) {
    const lat1 = degToRad(state.center[0]);
    const lat2 = degToRad(lat);
    const dLng = degToRad(lng - state.center[1]);
    const y = Math.sin(dLng) * Math.cos(lat2);
    const x = (Math.cos(lat1) * Math.sin(lat2)) - (Math.sin(lat1) * Math.cos(lat2) * Math.cos(dLng));
    return (radToDeg(Math.atan2(y, x)) + 360) % 360;
}

// drawTrendHalo highlights azimuth sectors where spot activity is RISING over the
// streamed window (newer half vs older half), as a faint arc just inside the rim.
// This is recent momentum from live data, NOT a slot-of-day historical baseline.
export function drawTrendHalo(ctx, width, height, filteredSpots) {
    if (!filteredSpots || filteredSpots.length < 8) return;

    const SECTORS = 24;
    const older = new Array(SECTORS).fill(0);
    const newer = new Array(SECTORS).fill(0);
    // Per-sector, per-band newer/older counts so the halo can be tinted with
    // the band whose activity is rising most in that direction.
    const newerByBand = Array.from({ length: SECTORS }, () => ({}));
    const olderByBand = Array.from({ length: SECTORS }, () => ({}));
    let maxAge = 0;
    for (const s of filteredSpots) {
        const age = Number(s?.ageSeconds) || 0;
        if (age > maxAge) maxAge = age;
    }
    if (maxAge < 120) return; // window too short to read a trend
    const split = maxAge / 2;

    for (const s of filteredSpots) {
        if (String(s?.sourceType || '').toLowerCase() === 'dxcluster') continue;
        if (!Number.isFinite(s?.lat) || !Number.isFinite(s?.lng)) continue;
        const sector = Math.floor((bearingFromCenter(s.lat, s.lng) / 360) * SECTORS) % SECTORS;
        const band = s?.band || 'all';
        if ((Number(s.ageSeconds) || 0) <= split) {
            newer[sector] += 1;
            newerByBand[sector][band] = (newerByBand[sector][band] || 0) + 1;
        } else {
            older[sector] += 1;
            olderByBand[sector][band] = (olderByBand[sector][band] || 0) + 1;
        }
    }

    const cx = width / 2;
    const cy = height / 2;
    const radiusBase = Math.min(width, height) * 0.47;
    const horizonAngular = Math.min(MAX_VISIBLE_C, state.horizonKm / EARTH_RADIUS_KM);
    const scale = (radiusBase * state.zoom) / Math.PI;
    const rimRadius = Math.min(scale * horizonAngular, radiusBase - AZIMUTH_SCALE_CLEARANCE_PX) - 8;
    if (rimRadius <= 0) return;

    const sectorRad = (2 * Math.PI) / SECTORS;
    const neutralColor = state.theme === 'dark' ? '#c9a98f' : '#7d5d46';
    ctx.save();
    ctx.lineCap = 'butt';
    ctx.lineWidth = 5;
    for (let sec = 0; sec < SECTORS; sec += 1) {
        const delta = newer[sec] - older[sec];
        if (delta < 2) continue; // only meaningfully rising sectors
        // Tint the arc with whichever band is rising most in this sector.
        let risingBand = null;
        let bestRise = 0;
        const bands = newerByBand[sec];
        for (const band in bands) {
            const rise = bands[band] - (olderByBand[sec][band] || 0);
            if (rise > bestRise) { bestRise = rise; risingBand = band; }
        }
        ctx.strokeStyle = (risingBand && bandColors[risingBand]) || neutralColor;
        // Keep the tint faint: a touch dimmer than the old monochrome halo.
        ctx.globalAlpha = Math.min(0.42, 0.12 + (0.06 * delta));
        // Sector spans bearings [sec, sec+1) * (360/SECTORS); 0° = up (canvas -Y).
        const startBearing = sec * sectorRad;
        const endBearing = (sec + 1) * sectorRad;
        // Convert bearing (0=N, clockwise) to canvas angle (0=+X, clockwise, Y-down).
        const a0 = startBearing - (Math.PI / 2);
        const a1 = endBearing - (Math.PI / 2);
        ctx.beginPath();
        ctx.arc(cx, cy, rimRadius, a0, a1);
        ctx.stroke();
    }
    ctx.restore();
}

function drawAzimuthIndicator(ctx, width, height) {
    const centerX = width / 2;
    const centerY = height / 2;

    // Fixed screen-space radius (intentionally does not depend on map zoom).
    const radius = Math.min(width, height) * 0.47;
    const outerRadius = Math.max(8, radius - 1);
    const majorTickInner = Math.max(0, outerRadius - 11);  // 30°
    const mediumTickInner = Math.max(0, outerRadius - 8);  // 10°
    const minorTickInner = Math.max(0, outerRadius - 6);   // 2°
    const labelRadius = Math.max(0, outerRadius - 19);
    const spokeRadius = Math.max(0, outerRadius - 0.5);

    const ringColor = state.theme === 'dark' ? 'rgba(220,230,240,0.52)' : 'rgba(30,42,55,0.52)';
    const tickColor = state.theme === 'dark' ? 'rgba(220,230,240,0.72)' : 'rgba(23,35,46,0.72)';
    const minorTickColor = state.theme === 'dark' ? 'rgba(220,230,240,0.34)' : 'rgba(23,35,46,0.34)';
    const spokeColor = state.theme === 'dark' ? 'rgba(220,230,240,0.38)' : 'rgba(30,42,55,0.38)';
    const textColor = state.theme === 'dark' ? 'rgba(235,243,250,0.88)' : 'rgba(20,32,44,0.88)';

    ctx.save();

    // Circular scale ring.
    ctx.strokeStyle = ringColor;
    strokeCircle(ctx, centerX, centerY, outerRadius, 1.0);

    // NS6T-style helper spokes: draw full spokes every 10° and
    // emphasize cardinal N/S/E/W lines.
    for (let bearing = 0; bearing < 360; bearing += 10) {
        const isCardinal = bearing === 0 || bearing === 90 || bearing === 180 || bearing === 270;
        const isMajor = bearing % 30 === 0;
        ctx.strokeStyle = spokeColor;
        ctx.globalAlpha = isCardinal ? 0.42 : isMajor ? 0.26 : 0.16;
        radialLine(ctx, centerX, centerY, 0, spokeRadius, bearing, isCardinal ? 1.0 : isMajor ? 0.6 : 0.45);
    }

    // 2° minor subdivisions (skip 10° and 30° positions).
    ctx.globalAlpha = 1;
    ctx.strokeStyle = minorTickColor;
    for (let bearing = 0; bearing < 360; bearing += 2) {
        if (bearing % 10 === 0) continue;
        radialLine(ctx, centerX, centerY, minorTickInner, outerRadius, bearing, 0.6);
    }

    // 10° medium subdivisions (skip major 30° positions).
    ctx.strokeStyle = tickColor;
    for (let bearing = 0; bearing < 360; bearing += 10) {
        if (bearing % 30 === 0) continue;
        radialLine(ctx, centerX, centerY, mediumTickInner, outerRadius, bearing, 0.95);
    }

    // 30° major ticks + labels.
    ctx.strokeStyle = tickColor;
    ctx.fillStyle = textColor;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';

    for (let bearing = 0; bearing < 360; bearing += 30) {
        const angle = degToRad(bearing - 90);
        const cosA = Math.cos(angle);
        const sinA = Math.sin(angle);
        const cardinal = bearing === 0 ? 'N' : bearing === 90 ? 'E' : bearing === 180 ? 'S' : bearing === 270 ? 'W' : '';

        radialLine(ctx, centerX, centerY, majorTickInner, outerRadius, bearing, cardinal ? 2.0 : 1.4);

        const lx = centerX + (labelRadius * cosA);
        const ly = centerY + (labelRadius * sinA);

        if (cardinal) {
            ctx.font = '700 11px Verdana, Arial, sans-serif';
            ctx.fillText(cardinal, lx, ly);
        } else {
            ctx.font = '500 9px Verdana, Arial, sans-serif';
            ctx.fillText(`${bearing}°`, lx, ly);
        }
    }

    ctx.restore();
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

// drawDxSpotHighlight draws the Chase Queue spot highlight: a great-circle path
// from the operator origin to the spot (dashed = hover, solid = pinned) plus a
// ring marker + callsign label. Amber, to stay distinct from the red target
// marker and the cyan antenna target line.
function drawDxSpotHighlight(ctx, width, height) {
    const h = state.dxSpotHighlight;
    if (!h || !h.enabled) return;

    const color = state.theme === 'dark' ? '#ffd166' : '#d97706';
    const spot = projectToCanvas(h.spotLat, h.spotLng, width, height);
    ctx.save();

    if (Number.isFinite(h.originLat) && Number.isFinite(h.originLng)) {
        const pts = greatCirclePoints(h.originLat, h.originLng, h.spotLat, h.spotLng, 48)
            .map(([lat, lng]) => projectToCanvas(lat, lng, width, height));
        ctx.lineWidth = h.pinned ? 2.4 : 1.8;
        ctx.strokeStyle = color;
        ctx.globalAlpha = h.pinned ? 0.95 : 0.8;
        if (!h.pinned) ctx.setLineDash([2, 5]);
        // Break the line where the projection clips beyond the horizon.
        let started = false;
        ctx.beginPath();
        for (const p of pts) {
            if (!p) { started = false; continue; }
            if (!started) { ctx.moveTo(p.x, p.y); started = true; } else ctx.lineTo(p.x, p.y);
        }
        ctx.stroke();
        ctx.setLineDash([]);
        ctx.globalAlpha = 1;
    }

    if (spot) {
        ctx.beginPath();
        ctx.arc(spot.x, spot.y, h.pinned ? 6.5 : 5.5, 0, Math.PI * 2);
        ctx.strokeStyle = color;
        ctx.lineWidth = 2.4;
        ctx.stroke();
        ctx.beginPath();
        ctx.arc(spot.x, spot.y, 2, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.fill();

        if (h.label) {
            ctx.font = '700 12px ui-monospace, Menlo, monospace';
            const tx = spot.x + 9;
            const ty = spot.y - 9;
            const textW = ctx.measureText(h.label).width;
            ctx.fillStyle = state.theme === 'dark' ? 'rgba(0,0,0,0.6)' : 'rgba(255,255,255,0.78)';
            ctx.fillRect(tx - 3, ty - 12, textW + 6, 16);
            ctx.fillStyle = color;
            ctx.textBaseline = 'alphabetic';
            ctx.fillText(h.label, tx, ty);
        }
    }
    ctx.restore();
}

function drawTargetHighlight(ctx, width, height) {
    if (typeof document === 'undefined') return;

    const rawTarget = document.getElementById('target')?.value?.trim()?.toUpperCase() || '';
    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(rawTarget);
    if (!isLocator) return;

    const bounds = locatorToBounds(rawTarget);
    if (!bounds) return;

    const lat0 = bounds[0][0];
    const lng0 = bounds[0][1];
    const lat1 = bounds[1][0];
    const lng1 = bounds[1][1];

    if (rawTarget.length === 4) {
        const corners = [
            [lat0, lng0],
            [lat1, lng0],
            [lat1, lng1],
            [lat0, lng1]
        ].map(([lat, lng]) => projectToCanvas(lat, lng, width, height));

        if (corners.some((p) => !p)) return;

        ctx.save();
        ctx.beginPath();
        ctx.moveTo(corners[0].x, corners[0].y);
        for (let i = 1; i < corners.length; i += 1) {
            ctx.lineTo(corners[i].x, corners[i].y);
        }
        ctx.closePath();
        ctx.fillStyle = 'rgba(255, 0, 0, 0.10)';
        ctx.strokeStyle = '#ff2d2d';
        ctx.lineWidth = 2.2;
        ctx.fill();
        ctx.stroke();
        ctx.restore();
        return;
    }

    const centerLat = (lat0 + lat1) / 2;
    const centerLng = (lng0 + lng1) / 2;
    const p = projectToCanvas(centerLat, centerLng, width, height);
    if (!p) return;

    const size = 7;
    ctx.save();
    ctx.strokeStyle = '#ff2d2d';
    ctx.lineWidth = 2.2;
    ctx.beginPath();
    ctx.moveTo(p.x - size, p.y - size);
    ctx.lineTo(p.x + size, p.y + size);
    ctx.moveTo(p.x + size, p.y - size);
    ctx.lineTo(p.x - size, p.y + size);
    ctx.stroke();
    ctx.restore();
}

function drawAntennaDirectionalLobe(ctx, width, height, station, centerBearingDeg, beamwidthDeg, radiusKm, fillStyle, lineStyle) {
    const half = Math.max(2, beamwidthDeg / 2);
    const leftBearing = ((centerBearingDeg - half) % 360 + 360) % 360;
    const rightBearing = ((centerBearingDeg + half) % 360 + 360) % 360;

    const sampleGeodesic = (bearingDeg, maxDistanceKm, segments = 24) => {
        const pts = [{ x: station.x, y: station.y }];
        for (let i = 1; i <= segments; i += 1) {
            const d = (maxDistanceKm * i) / segments;
            const [lat, lng] = destinationPoint(station.lat, station.lng, bearingDeg, d);
            const p = projectToCanvas(lat, lng, width, height, { enforceHorizon: false });
            if (!p) break;
            pts.push(p);
        }
        return pts;
    };

    const leftEdge = sampleGeodesic(leftBearing, radiusKm, 24);
    const rightEdge = sampleGeodesic(rightBearing, radiusKm, 24);
    if (leftEdge.length < 2 || rightEdge.length < 2) return;

    // Rounded outer boundary: sample the constant-distance arc from leftBearing
    // to rightBearing at the full radius. This traces the map-border curve (when
    // the station is the map center), so the lobe always ends rounded — never a
    // chord — regardless of which quadrant it points to. enforceHorizon:false so
    // border points aren't culled by float rounding; the map clip trims overflow.
    const arcSteps = Math.max(12, Math.ceil(beamwidthDeg / 2));
    const arcPoints = [];
    for (let i = 0; i <= arcSteps; i += 1) {
        const t = i / arcSteps;
        const b = ((leftBearing + (beamwidthDeg * t)) % 360 + 360) % 360;
        const [lat, lng] = destinationPoint(station.lat, station.lng, b, radiusKm);
        const p = projectToCanvas(lat, lng, width, height, { enforceHorizon: false });
        if (p) arcPoints.push(p);
    }

    const rightInward = rightEdge.slice().reverse().slice(1);

    // Fill the full swept region: station → left edge → rounded arc → right edge.
    // Always filled (no connection gating), so the cone is never left empty.
    ctx.save();
    ctx.beginPath();
    ctx.moveTo(station.x, station.y);
    for (const p of leftEdge.slice(1)) {
        ctx.lineTo(p.x, p.y);
    }
    for (const p of arcPoints) {
        ctx.lineTo(p.x, p.y);
    }
    for (const p of rightInward) {
        ctx.lineTo(p.x, p.y);
    }
    ctx.closePath();
    ctx.fillStyle = fillStyle;
    ctx.fill();

    ctx.beginPath();
    ctx.moveTo(station.x, station.y);
    for (const p of leftEdge.slice(1)) {
        ctx.lineTo(p.x, p.y);
    }
    ctx.moveTo(station.x, station.y);
    for (const p of rightEdge.slice(1)) {
        ctx.lineTo(p.x, p.y);
    }
    ctx.strokeStyle = lineStyle;
    ctx.lineWidth = 1.7;
    ctx.stroke();
    ctx.restore();
}

function drawAntennaOverlay(ctx, width, height) {
    const overlay = state.antennaOverlay;
    if (!overlay?.enabled) return;

    const stationPoint = projectToCanvas(overlay.stationLat, overlay.stationLng, width, height);
    if (!stationPoint) return;

    const station = {
        lat: overlay.stationLat,
        lng: overlay.stationLng,
        x: stationPoint.x,
        y: stationPoint.y
    };

    const radiusKm = state.horizonKm;
    const heading = overlay.azimuthDeg;
    const beamwidth = overlay.beamwidth3dBDeg;
    const isDark = state.theme === 'dark';

    const forwardFill = isDark ? 'rgba(255, 196, 84, 0.10)' : 'rgba(255, 161, 64, 0.12)';
    const backwardFill = isDark ? 'rgba(111, 185, 255, 0.09)' : 'rgba(63, 137, 255, 0.08)';
    const forwardLine = isDark ? 'rgba(255, 222, 155, 0.92)' : 'rgba(184, 82, 0, 0.95)';
    const backwardLine = isDark ? 'rgba(155, 209, 255, 0.92)' : 'rgba(20, 91, 187, 0.95)';

    if (overlay.mode === 'forward' || overlay.mode === 'bidirectional') {
        drawAntennaDirectionalLobe(ctx, width, height, station, heading, beamwidth, radiusKm, forwardFill, forwardLine);
    }
    if (overlay.mode === 'backward' || overlay.mode === 'bidirectional') {
        drawAntennaDirectionalLobe(ctx, width, height, station, heading + 180, beamwidth, radiusKm, backwardFill, backwardLine);
    }

    const drawBearingCurve = (bearingDeg, dashed = false) => {
        const points = [];
        const segments = 28;
        for (let i = 0; i <= segments; i += 1) {
            const d = (radiusKm * i) / segments;
            const [lat, lng] = destinationPoint(station.lat, station.lng, bearingDeg, d);
            const p = projectToCanvas(lat, lng, width, height);
            if (!p) break;
            points.push(p);
        }
        if (points.length < 2) return;

        ctx.save();
        ctx.beginPath();
        ctx.moveTo(points[0].x, points[0].y);
        for (let i = 1; i < points.length; i += 1) {
            ctx.lineTo(points[i].x, points[i].y);
        }

        if (dashed) {
            // Use a stable highlight style (glow + dotted line) instead of animated
            // dash offset to avoid jitter when render cadence is irregular.
            ctx.strokeStyle = isDark ? 'rgba(112, 223, 255, 0.26)' : 'rgba(0, 123, 170, 0.22)';
            ctx.lineWidth = 4.2;
            ctx.setLineDash([]);
            ctx.stroke();

            ctx.beginPath();
            ctx.moveTo(points[0].x, points[0].y);
            for (let i = 1; i < points.length; i += 1) {
                ctx.lineTo(points[i].x, points[i].y);
            }
            ctx.strokeStyle = isDark ? 'rgba(130, 232, 255, 0.98)' : 'rgba(0, 123, 170, 0.96)';
            ctx.lineWidth = 1.8;
            ctx.lineCap = 'round';
            ctx.setLineDash([1.2, 6.2]);
            ctx.stroke();

            const tip = points[points.length - 1];
            if (tip) {
                ctx.beginPath();
                ctx.arc(tip.x, tip.y, 2.8, 0, Math.PI * 2);
                ctx.fillStyle = isDark ? 'rgba(130, 232, 255, 0.95)' : 'rgba(0, 123, 170, 0.9)';
                ctx.fill();
            }
        } else {
            ctx.strokeStyle = isDark ? 'rgba(255, 228, 181, 0.95)' : 'rgba(125, 56, 0, 0.95)';
            ctx.lineWidth = 0.9;
            ctx.setLineDash([]);
        }
        if (!dashed) ctx.stroke();
        ctx.setLineDash([]);
        ctx.restore();
    };

    drawBearingCurve(heading, false);

    if (Number.isFinite(overlay.pendingTargetBearingDeg)) {
        const angularDelta = Math.abs((((overlay.pendingTargetBearingDeg - heading + 540) % 360) - 180));
        const shouldShowTargetLine = angularDelta > 20;
        if (shouldShowTargetLine) {
            drawBearingCurve(overlay.pendingTargetBearingDeg, true);
        }
    }

    ctx.save();
    ctx.beginPath();
    ctx.arc(station.x, station.y, 4.5, 0, Math.PI * 2);
    ctx.fillStyle = isDark ? '#ffe0ab' : '#8b3a00';
    ctx.fill();
    ctx.restore();
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
        const p = projectToCanvas(lat, lng, width, height, { enforceHorizon: false });
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

// buildAzimuthSnrField interpolates a continuous SNR surface from the
// projected spots using inverse-distance weighting (IDW). Spots are projected
// into canvas space first, so the azimuthal projection's distortion is handled
// implicitly. The field is sampled on a regular grid and masked to where data
// actually supports an estimate: a vertex is only "valid" if at least one spot
// lies within `radius` pixels of it. Returns null when there is nothing to draw.
function buildAzimuthSnrField(filteredSpots, width, height, step, radius) {
    // Reports from one grid square all project to the same pixel; aggregate
    // them into a single weighted point so the wider smoothing kernel stays
    // cheap. Each aggregated point carries the mean SNR, a report count (used
    // as a weight multiplier), and the dominant band at that location.
    const agg = new Map();
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const spot of filteredSpots) {
        if (String(spot?.sourceType || '').toLowerCase() === 'dxcluster') continue;
        const lat = Number(spot.lat), lng = Number(spot.lng), snr = Number(spot.snr);
        if (!Number.isFinite(lat) || !Number.isFinite(lng) || !Number.isFinite(snr)) continue;
        const p = projectToCanvas(lat, lng, width, height);
        if (!p) continue;
        const kx = Math.round(p.x), ky = Math.round(p.y);
        const key = kx + ',' + ky;
        let a = agg.get(key);
        if (!a) { a = { x: kx, y: ky, snrSum: 0, n: 0, bands: {} }; agg.set(key, a); }
        a.snrSum += snr;
        a.n += 1;
        a.bands[spot.band || 'all'] = (a.bands[spot.band || 'all'] || 0) + 1;
        if (kx < minX) minX = kx;
        if (ky < minY) minY = ky;
        if (kx > maxX) maxX = kx;
        if (ky > maxY) maxY = ky;
    }
    if (agg.size === 0) return null;

    const pts = [];
    for (const a of agg.values()) {
        let bestBand = 'all', bestCount = -1;
        for (const b in a.bands) if (a.bands[b] > bestCount) { bestCount = a.bands[b]; bestBand = b; }
        pts.push({ x: a.x, y: a.y, snr: a.snrSum / a.n, n: a.n, band: bestBand });
    }

    // Bucket index for O(1) neighbour lookup (bucket size == search radius).
    const bucket = Math.max(1, radius);
    const buckets = new Map();
    const bkey = (bx, by) => bx + ',' + by;
    for (const p of pts) {
        const bx = Math.floor((p.x - minX) / bucket);
        const by = Math.floor((p.y - minY) / bucket);
        const k = bkey(bx, by);
        let arr = buckets.get(k);
        if (!arr) { arr = []; buckets.set(k, arr); }
        arr.push(p);
    }

    // Sample grid spans the data bounding box padded by one search radius so
    // the estimate can fall off smoothly beyond the outermost spots. A Gaussian
    // kernel blends neighbouring grid squares into continuous regions, and a
    // small background prior pulls the estimate toward NO_DATA where reports
    // are sparse — so the outer boundary tapers smoothly instead of cliff-edging
    // at the search radius, and there are no blocky cell edges anywhere.
    const x0 = minX - radius, y0 = minY - radius;
    const cols = Math.max(2, Math.ceil((maxX + radius - x0) / step) + 1);
    const rows = Math.max(2, Math.ceil((maxY + radius - y0) / step) + 1);
    const snr = new Float32Array(cols * rows);
    const bandIdx = new Array(cols * rows);
    const r2 = radius * radius;
    const sigma = radius / 2.2;
    const twoSigma2 = 2 * sigma * sigma;
    const PRIOR = 0.02;

    for (let j = 0; j < rows; j++) {
        const vy = y0 + j * step;
        const by = Math.floor((vy - minY) / bucket);
        for (let i = 0; i < cols; i++) {
            const vx = x0 + i * step;
            const bx = Math.floor((vx - minX) / bucket);
            let wsum = 0, vsum = 0;
            let bestBand = 'all', bestBandW = -1;
            const bandW = {};
            for (let dby = -1; dby <= 1; dby++) {
                for (let dbx = -1; dbx <= 1; dbx++) {
                    const nb = buckets.get(bkey(bx + dbx, by + dby));
                    if (!nb) continue;
                    for (const p of nb) {
                        const dx = p.x - vx, dy = p.y - vy;
                        const d2 = dx * dx + dy * dy;
                        if (d2 > r2) continue;
                        const w = Math.exp(-d2 / twoSigma2) * p.n;
                        wsum += w;
                        vsum += w * p.snr;
                        const bw = (bandW[p.band] || 0) + w;
                        bandW[p.band] = bw;
                        if (bw > bestBandW) { bestBandW = bw; bestBand = p.band; }
                    }
                }
            }
            const idx = j * cols + i;
            // Regularised estimate: blends toward NO_DATA when total weight is
            // small, giving a smooth taper at the edges.
            snr[idx] = (vsum + AZIMUTH_FIELD_NO_DATA * PRIOR) / (wsum + PRIOR);
            if (wsum > 0) bandIdx[idx] = bestBand;
        }
    }
    return { cols, rows, step, x0, y0, snr, bandIdx };
}

// cellAbove returns the polygon (as {x,y} points) of the part of one grid cell
// where the field value is >= threshold, using marching-squares edge
// interpolation so contour boundaries are smooth rather than blocky.
function cellAbove(threshold, cx, cy, cv) {
    const out = [];
    for (let i = 0; i < 4; i++) {
        const n = (i + 1) % 4;
        const vi = cv[i], vn = cv[n];
        const insideI = vi >= threshold;
        if (insideI) out.push({ x: cx[i], y: cy[i] });
        if (insideI !== (vn >= threshold)) {
            const f = (threshold - vi) / (vn - vi);
            out.push({ x: cx[i] + f * (cx[n] - cx[i]), y: cy[i] + f * (cy[n] - cy[i]) });
        }
    }
    return out;
}

// fillAzimuthContours renders the interpolated SNR field as stacked filled
// contour zones. The lowest threshold is the data-presence contour that wraps
// the field with a smooth outer boundary; the 0 dB and 10 dB breakpoints (the
// same ones grid-SNR uses) are layered on top. Lower zones are painted first
// (faint) so stronger signals read brighter, and every boundary is an
// interpolated iso-line, so there are no blocky cell edges anywhere.
function fillAzimuthContours(ctx, field) {
    if (!field) return;
    const { cols, rows, step, x0, y0, snr, bandIdx } = field;
    const layers = [
        { t: AZIMUTH_FIELD_PRESENCE, alpha: 0.22 },
        { t: 0, alpha: 0.30 },
        { t: 10, alpha: 0.40 }
    ];
    const ci = [0, 1, 1, 0];
    const cj = [0, 0, 1, 1];

    for (const layer of layers) {
        for (let j = 0; j < rows - 1; j++) {
            for (let i = 0; i < cols - 1; i++) {
                const i00 = j * cols + i;
                const i10 = j * cols + i + 1;
                const i11 = (j + 1) * cols + i + 1;
                const i01 = (j + 1) * cols + i;

                const cv = [snr[i00], snr[i10], snr[i11], snr[i01]];
                const cx = [x0 + i * step, x0 + (i + 1) * step, x0 + (i + 1) * step, x0 + i * step];
                const cy = [y0 + j * step, y0 + j * step, y0 + (j + 1) * step, y0 + (j + 1) * step];
                const poly = cellAbove(layer.t, cx, cy, cv);
                if (!poly || poly.length < 3) continue;

                // Hue follows the dominant band of the cell's strongest corner.
                let best = 0;
                for (let k = 1; k < 4; k++) if (cv[k] > cv[best]) best = k;
                const bandCornerIdx = (j + cj[best]) * cols + (i + ci[best]);
                const band = bandIdx[bandCornerIdx] || 'all';

                ctx.fillStyle = bandColors[band] || bandColors.all;
                ctx.globalAlpha = layer.alpha;
                ctx.beginPath();
                ctx.moveTo(poly[0].x, poly[0].y);
                for (let k = 1; k < poly.length; k++) ctx.lineTo(poly[k].x, poly[k].y);
                ctx.closePath();
                ctx.fill();
            }
        }
    }
    ctx.globalAlpha = 1;
}

function drawSpots(ctx, width, height, filteredSpots, style, gridSquares, maxClusterDist) {
    if (style === 'grid-snr') {
        const squares = gridSquares || collectGridSquares(filteredSpots, getGridResolution());
        let hiddenSquares = 0;
        for (const loc of Object.keys(squares)) {
            const corners = getProjectedGridCellCorners(loc, width, height);
            if (!corners) {
                hiddenSquares += 1;
                continue;
            }

            const entry = squares[loc];
            const maxSnr = Number.isFinite(entry.maxSnr)
                ? entry.maxSnr
                : (entry.snrSum / Math.max(1, entry.count));
            let dominantBand = 'all';
            let maxCount = 0;
            for (const band of Object.keys(entry.bands)) {
                if (entry.bands[band] > maxCount) {
                    maxCount = entry.bands[band];
                    dominantBand = band;
                }
            }
            ctx.fillStyle = bandColors[dominantBand] || bandColors.all;
            ctx.globalAlpha = maxSnr >= 10 ? 0.72 : maxSnr >= 0 ? 0.45 : 0.22;

            ctx.beginPath();
            ctx.moveTo(corners[0].x, corners[0].y);
            for (let i = 1; i < corners.length; i += 1) {
                ctx.lineTo(corners[i].x, corners[i].y);
            }
            ctx.closePath();
            ctx.fill();
        }
        state.hiddenGridSquaresCount = hiddenSquares;
        drawDxClusterSpots(ctx, width, height, filteredSpots);
        ctx.globalAlpha = 1;
        return;
    }

    if (style === 'active-area') {
        // Interpolate a continuous SNR surface and render it as filled contour
        // zones. Sample resolution and search radius scale with the canvas.
        const minDim = Math.min(width, height);
        const step = Math.max(8, Math.min(16, Math.round(minDim / 95)));
        const radius = step * 4;
        const field = buildAzimuthSnrField(filteredSpots, width, height, step, radius);
        fillAzimuthContours(ctx, field);
        state.hiddenGridSquaresCount = 0;
        drawDxClusterSpots(ctx, width, height, filteredSpots);
        ctx.globalAlpha = 1;
        return;
    }

    state.hiddenGridSquaresCount = 0;

    for (const spot of filteredSpots) {
        if (String(spot?.sourceType || '').toLowerCase() === 'dxcluster') continue;
        const p = projectToCanvas(spot.lat, spot.lng, width, height);
        if (!p) continue;
        const color = bandColors[spot.band] || bandColors.all;
        ctx.beginPath();
        ctx.arc(p.x, p.y, 4, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.8;
        ctx.fill();
    }
    drawDxClusterSpots(ctx, width, height, filteredSpots);
    ctx.globalAlpha = 1;
}

function withHorizonClip(ctx, width, height, drawFn) {
    const radiusBase = Math.min(width, height) * 0.47;
    const scale = (radiusBase * state.zoom) / Math.PI;
    const horizonAngular = Math.min(MAX_VISIBLE_C, state.horizonKm / EARTH_RADIUS_KM);
    const rawClipRadius = scale * horizonAngular;
    const maxInsideScaleRadius = Math.max(1, radiusBase - AZIMUTH_SCALE_CLEARANCE_PX);
    const clipRadius = Math.max(1, Math.min(rawClipRadius, maxInsideScaleRadius));

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
    const resolvedStyle = requestedStyle === 'active-area' ? 'active-area' : 'grid-snr';
    state.lastStyle = resolvedStyle;

    const renderCtx = {
        minSnrMode: getMinSnrMode(),
        ssbMinDb: parseInt(document.getElementById('ssb-min-db')?.value || '0', 10),
        cwMinDb: parseInt(document.getElementById('cw-min-db')?.value || '-15', 10),
        selectedBand: getSelectedBand(),
        enabledBands: getEnabledBands(),
        maxClusterDist: Math.max(100, parseInt(document.getElementById('cluster-distance')?.value || '500', 10) || 500)
    };

    state.hiddenGridSquaresCount = 0;

    if (profile) profile.filterStart = nowMs();
    const filteredSpots = getFilteredSpots(spots, renderCtx);
    if (profile) profile.filterEnd = nowMs();

    if (profile) profile.gridStart = nowMs();
    const gridSquares = resolvedStyle === 'grid-snr' ? collectGridSquares(spots, getGridResolution(), filteredSpots) : null;
    if (profile) profile.gridEnd = nowMs();

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
            if (profile) profile.graylineStart = nowMs();
            drawGrayline(state.ctx, width, height);
            if (profile) profile.graylineEnd = nowMs();
        }

        if (getForecastEnabled()) {
            drawTrendHalo(state.ctx, width, height, filteredSpots);
        }

        if (profile) profile.spotsStart = nowMs();
        drawSpots(state.ctx, width, height, filteredSpots, resolvedStyle, gridSquares, renderCtx.maxClusterDist);
        if (profile) profile.spotsEnd = nowMs();

        if (state.dxccLabelsEnabled) {
            if (profile) profile.dxccStart = nowMs();
            drawDxccLabels(state.ctx, width, height, plan);
            if (profile) profile.dxccEnd = nowMs();
        }

        if (profile) profile.targetStart = nowMs();
        drawTargetHighlight(state.ctx, width, height);
        if (profile) profile.targetEnd = nowMs();

        drawAntennaOverlay(state.ctx, width, height);
        drawDxSpotHighlight(state.ctx, width, height);
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
            filterMs: Number(((profile.filterEnd || end) - (profile.filterStart || profile.start)).toFixed(2)),
            gridMs: Number(((profile.gridEnd || end) - (profile.gridStart || profile.start)).toFixed(2)),
            planMs: Number(((profile.planEnd || end) - (profile.planStart || profile.start)).toFixed(2)),
            worldMs: Number(((profile.worldEnd || end) - (profile.worldStart || profile.start)).toFixed(2)),
            graylineMs: profile.graylineStart
                ? Number(((profile.graylineEnd || end) - profile.graylineStart).toFixed(2))
                : 0,
            spotsMs: Number(((profile.spotsEnd || end) - (profile.spotsStart || profile.start)).toFixed(2)),
            scaleMs: Number(((profile.scaleEnd || end) - (profile.scaleStart || profile.start)).toFixed(2)),
            targetMs: Number(((profile.targetEnd || end) - (profile.targetStart || profile.start)).toFixed(2)),
            dxccMs: state.dxccLabelsEnabled
                ? Number(((profile.dxccEnd || end) - (profile.dxccStart || profile.start)).toFixed(2))
                : 0,
            style: resolvedStyle,
            zoom: state.zoom
        });
    }
}
