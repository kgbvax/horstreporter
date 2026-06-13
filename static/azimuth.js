import { bandColors, getEnabledBands, getMinSnrMode, getSelectedBand, locatorToBounds, getGridResolution } from './utils.js';

const EARTH_RADIUS_KM = 6371;
const ANTIPODE_KM = Math.PI * EARTH_RADIUS_KM;
const MAX_VISIBLE_C = Math.PI - 0.02;

const PALETTE_LIGHT = ['#b8d98c', '#d7ca8d', '#9fd0a8', '#9ecbcc', '#d8b996', '#c8c1dd'];
const PALETTE_DARK = ['#4f6f3d', '#6f6538', '#3f6b4f', '#3b6465', '#705740', '#5a5073'];

const DXCC_PREFIX_BY_ISO_A2 = {
    US: 'K', CA: 'VE', RU: 'UA', CN: 'BY', IN: 'VU', AU: 'VK', BR: 'PY', AR: 'LU', ZA: 'ZS', CL: 'CE',
    JP: 'JA', ID: 'YB', MX: 'XE', ES: 'EA', FR: 'F', DE: 'DL', IT: 'I', GB: 'G', NO: 'LA', SE: 'SM',
    FI: 'OH', NZ: 'ZL', EG: 'SU', ET: 'ET', SD: 'ST', DJ: 'J2', ER: 'E3', YE: '7O', SA: 'HZ',
    IR: 'EP', PK: 'AP', CO: 'HK', PE: 'OA', NG: '5N', CD: '9Q', KZ: 'UN', TH: 'HS', TZ: '5H',
    UA: 'UR', UZ: 'UK', MY: '9M2', VN: 'XV', KR: 'HL', DZ: '7X', MA: 'CN', PT: 'CT', NL: 'PA',
    CH: 'HB', BE: 'ON', AT: 'OE', PL: 'SP', RO: 'YO', GR: 'SV', TR: 'TA', IS: 'TF',
    GL: 'OX', PF: 'FO', FJ: 'FW', MP: 'KH0', GU: 'KH2', PW: 'PY', RE: 'FR', AX: 'OH0'
};

const state = {
    enabled: false,
    canvas: null,
    ctx: null,
    worldGeoJson: null,
    center: [20, 0],
    theme: 'light',
    lastSpots: [],
    lastStyle: 'grid-snr',
    zoom: 1.5
};

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

function projectToCanvas(center, point, width, height, scale = 1) {
    const p = projectAeqdNormalized(center, point);
    if (!p.visible) return null;

    const radius = Math.min(width, height) * 0.47 * scale * state.zoom;
    const cx = width / 2;
    const cy = height / 2;

    return {
        x: cx + (p.x / Math.PI) * radius,
        y: cy - (p.y / Math.PI) * radius,
        c: p.c
    };
}

function featureKey(feature) {
    const p = feature?.properties || {};
    return p.ADM0_A3 || p.ISO_A2 || p.SOV_A3 || p.BRK_A3 || p.NAME || p.ADMIN || 'UNKNOWN';
}

function featurePrefix(feature) {
    const p = feature?.properties || {};
    const explicit = p.DXCC_PREFIX || p.DXCC || p.dxccPrefix;
    if (explicit) return String(explicit).toUpperCase();
    const iso = String(p.ISO_A2 || '').toUpperCase();
    return DXCC_PREFIX_BY_ISO_A2[iso] || null;
}

function featureAnchor(feature) {
    const p = feature?.properties || {};
    const x = Number(p.LABEL_X);
    const y = Number(p.LABEL_Y);
    if (Number.isFinite(x) && Number.isFinite(y) && Math.abs(x) <= 180 && Math.abs(y) <= 90) {
        return [y, normalizeLng(x)];
    }
    return null;
}

export function selectProminentDxccLabels(featureCollection, center, options = {}) {
    const maxLabels = Number(options.maxLabels ?? 28);
    const minDistanceKm = Number(options.minDistanceKm ?? 900);
    const features = featureCollection?.features || [];

    const out = [];
    const usedPrefix = new Set();
    for (const feature of features) {
        const prefix = featurePrefix(feature);
        const anchor = featureAnchor(feature);
        if (!prefix || !anchor) continue;

        const [lat, lng] = anchor;
        const distanceKm = haversineKm(center[0], center[1], lat, lng);
        if (distanceKm > ANTIPODE_KM - 300) continue;

        if (usedPrefix.has(prefix)) continue;
        const tooClose = out.some(p => haversineKm(p.lat, p.lng, lat, lng) < minDistanceKm);
        if (tooClose) continue;

        out.push({ key: featureKey(feature), prefix, lat, lng, distanceKm });
        usedPrefix.add(prefix);
        if (out.length >= maxLabels) break;
    }

    return out;
}

export function computeAzimuthLabelSpecs(center, increment = 30, distanceKm = ANTIPODE_KM - 900) {
    const labels = [];
    for (let bearing = 0; bearing < 360; bearing += increment) {
        const [lat, lng] = destinationPoint(center[0], center[1], bearing, distanceKm);
        const cardinal = bearing === 0 ? 'N' : bearing === 90 ? 'E' : bearing === 180 ? 'S' : bearing === 270 ? 'W' : '';
        labels.push({
            bearing,
            label: cardinal ? `${bearing}° ${cardinal}` : `${bearing}°`,
            lat,
            lng
        });
    }
    return labels;
}

function getCurrentStyle() {
    const checked = document.querySelector('input[name="style-select"]:checked');
    return checked ? checked.value : 'grid-snr';
}

function getFilteredSpots(spots) {
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();

    return spots.filter(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
        if (!enabledBands.has(spot.band)) return false;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
        return true;
    });
}

export function createAzimuthRenderPlan({ featureCollection, center, spots = [], style = 'grid-snr', theme = 'light', zoomLevel = 2 }) {
    const labels = selectProminentDxccLabels(featureCollection, center, {
        maxLabels: zoomLevel <= 1.5 ? 14 : zoomLevel <= 2.5 ? 24 : 36,
        minDistanceKm: zoomLevel <= 1.5 ? 1500 : 950
    }).map(l => ({
        key: l.key,
        prefix: l.prefix,
        lat: Number(l.lat.toFixed(3)),
        lng: Number(l.lng.toFixed(3))
    }));

    const azimuthLabels = computeAzimuthLabelSpecs(center, 30).map(l => ({
        bearing: l.bearing,
        label: l.label,
        lat: Number(l.lat.toFixed(3)),
        lng: Number(l.lng.toFixed(3))
    }));

    let itemCount = 0;
    if (style === 'grid-snr') itemCount = spots.length;
    else if (style === 'active-area') itemCount = new Set(spots.map(s => s.band)).size;

    return {
        center: [Number(center[0].toFixed(3)), Number(center[1].toFixed(3))],
        theme,
        dxccLabels: labels,
        azimuthLabels,
        overlaySummary: { style, itemCount }
    };
}

export async function loadAzimuthWorldGeoJson() {
    if (!state.worldGeoJson) {
        const response = await fetch('vendor/world.geojson');
        state.worldGeoJson = await response.json();
    }
    return state.worldGeoJson;
}

function ensureCanvasSize() {
    if (!state.canvas) return;
    const rect = state.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(1, Math.floor(rect.width * dpr));
    const h = Math.max(1, Math.floor(rect.height * dpr));
    if (state.canvas.width !== w || state.canvas.height !== h) {
        state.canvas.width = w;
        state.canvas.height = h;
    }
}

function drawBackground(ctx, width, height) {
    ctx.fillStyle = state.theme === 'dark' ? '#1a2a38' : '#b9d3ea';
    ctx.fillRect(0, 0, width, height);
}

function drawCountries(ctx, width, height) {
    const features = state.worldGeoJson?.features || [];
    const borderColor = state.theme === 'dark' ? '#7f8d98' : '#7d6d59';

    for (const feature of features) {
        const geom = feature?.geometry;
        if (!geom || geom.type !== 'Polygon' || !Array.isArray(geom.coordinates)) continue;

        ctx.beginPath();
        let started = false;
        for (const ring of geom.coordinates) {
            for (const coord of ring) {
                const p = projectToCanvas(state.center, [coord[1], coord[0]], width, height, 1);
                if (!p) continue;
                if (!started) {
                    ctx.moveTo(p.x, p.y);
                    started = true;
                } else {
                    ctx.lineTo(p.x, p.y);
                }
            }
        }
        if (!started) continue;

        const fill = (state.theme === 'dark' ? PALETTE_DARK : PALETTE_LIGHT)[Math.abs(featureKey(feature).length) % 6];
        ctx.fillStyle = fill;
        ctx.globalAlpha = 0.9;
        ctx.fill();
        ctx.globalAlpha = 1;
        ctx.strokeStyle = borderColor;
        ctx.lineWidth = 1;
        ctx.stroke();
    }
}

function drawGridOverlay(ctx, width, height, spots) {
    const byLoc = {};
    for (const spot of spots) {
        const loc = (spot.locator || '').substring(0, getGridResolution());
        if (!loc || loc.length < 4) continue;
        if (!byLoc[loc]) byLoc[loc] = { count: 0, band: spot.band };
        byLoc[loc].count += 1;
    }

    for (const loc of Object.keys(byLoc)) {
        const bounds = locatorToBounds(loc);
        if (!bounds) continue;
        const color = bandColors[byLoc[loc].band] || bandColors.all;
        const corners = [
            [bounds[0][0], bounds[0][1]],
            [bounds[0][0], bounds[1][1]],
                [bounds[1][0], bounds[0][1]],
                [bounds[0][0], bounds[0][1]]
        ];

        ctx.beginPath();
        let started = false;
        for (const c of corners) {
            const p = projectToCanvas(state.center, c, width, height, 1);
            if (!p) continue;
            if (!started) {
                ctx.moveTo(p.x, p.y);
                started = true;
            } else {
                ctx.lineTo(p.x, p.y);
            }
        }
        if (!started) continue;

        ctx.fillStyle = color;
        ctx.globalAlpha = 0.45;
        ctx.fill();
        ctx.globalAlpha = 1;
        ctx.strokeStyle = color;
        ctx.lineWidth = 1;
        ctx.stroke();
    }
}

function drawActiveAreaOverlay(ctx, width, height, spots) {
    for (const spot of spots) {
        const p = projectToCanvas(state.center, [spot.lat, spot.lng], width, height, 1);
        if (!p) continue;
        const color = bandColors[spot.band] || bandColors.all;
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.65;
        ctx.beginPath();
        ctx.arc(p.x, p.y, 3, 0, Math.PI * 2);
        ctx.fill();
        ctx.globalAlpha = 1;
    }
}

function drawAzimuthLabels(ctx, width, height) {
    const labels = computeAzimuthLabelSpecs(state.center, 30, ANTIPODE_KM - 950);
    ctx.font = '600 12px Arial';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';

    labels.forEach(label => {
        const p = projectToCanvas(state.center, [label.lat, label.lng], width, height, 1);
        if (!p) return;
        ctx.fillStyle = state.theme === 'dark' ? '#dce9f2' : '#263745';
        ctx.fillText(label.label, p.x, p.y);
    });
}

function drawDxccLabels(ctx, width, height) {
    const labels = selectProminentDxccLabels(state.worldGeoJson, state.center, {
        maxLabels: state.zoom <= 1.5 ? 14 : 26,
        minDistanceKm: state.zoom <= 1.5 ? 1500 : 900
    });

    ctx.font = '700 14px Arial Narrow, Arial';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';

    for (const label of labels) {
        const p = projectToCanvas(state.center, [label.lat, label.lng], width, height, 1);
        if (!p) continue;
        ctx.strokeStyle = state.theme === 'dark' ? 'rgba(0,0,0,0.7)' : 'rgba(255,255,255,0.92)';
        ctx.lineWidth = 3.2;
        ctx.strokeText(label.prefix, p.x, p.y);
        ctx.fillStyle = state.theme === 'dark' ? '#f6f6f6' : '#111';
        ctx.fillText(label.prefix, p.x, p.y);
    }
}

export function clampAzimuthZoom(zoom) {
    const z = Number(zoom);
    if (!Number.isFinite(z)) return 1.5;
    return Math.max(1, Math.min(4, z));
}

export function setAzimuthZoom(zoom) {
    state.zoom = clampAzimuthZoom(zoom);
}

export function initAzimuthCanvas(canvasId = 'azimuth-canvas') {
    state.canvas = document.getElementById(canvasId);
    if (!state.canvas) return null;

    state.ctx = state.canvas.getContext('2d');
    state.canvas.style.display = 'none';

    state.canvas.addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
    });

    window.addEventListener('resize', () => {
        if (state.enabled) renderAzimuthScene({ spots: state.lastSpots, style: state.lastStyle });
    });

    return state.canvas;
}

export function isAzimuthEnabled() {
    return state.enabled;
}

export function setAzimuthEnabled(enabled) {
    state.enabled = !!enabled;
    if (state.canvas) {
        state.canvas.style.display = state.enabled ? 'block' : 'none';
    }

    const mapEl = document.getElementById('map');
    if (mapEl) {
        mapEl.style.display = state.enabled ? 'none' : 'block';
    }
}

export function setAzimuthTheme(theme) {
    state.theme = theme === 'dark' ? 'dark' : 'light';
}

export function setAzimuthCenter(center) {
    if (Array.isArray(center) && center.length >= 2) {
        state.center = [Number(center[0]), normalizeLng(Number(center[1]))];
    }
}

export async function renderAzimuthScene({ spots = [], style } = {}) {
    if (!state.enabled || !state.canvas || !state.ctx) return;
    await loadAzimuthWorldGeoJson();

    state.lastSpots = spots;
    state.lastStyle = style || getCurrentStyle();

    ensureCanvasSize();
    const ctx = state.ctx;
    const width = state.canvas.width;
    const height = state.canvas.height;

    ctx.clearRect(0, 0, width, height);
    drawBackground(ctx, width, height);
    drawCountries(ctx, width, height);

    const filtered = getFilteredSpots(spots);
    if (state.lastStyle === 'grid-snr') drawGridOverlay(ctx, width, height, filtered);
    else if (state.lastStyle === 'active-area') drawActiveAreaOverlay(ctx, width, height, filtered);

    drawDxccLabels(ctx, width, height);
    drawAzimuthLabels(ctx, width, height);
}
