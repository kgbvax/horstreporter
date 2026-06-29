export function formatNumber(num) {
    if (num == null) return '';
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

const COUNTRY_PALETTE_LIGHT = [
    '#FBEFF0', '#FBD3D1', '#FEE5DA', '#FFE2B7', '#FFFBD4', '#E8EDAD', '#E4F0DB',
    '#C3E6E5', '#E1F3FC', '#BFD3ED', '#E0DEEF', '#DFCBE3', '#E3D9C6', '#FFFFFF'
];
const COUNTRY_PALETTE_DARK = [
    '#4c383c', '#5a3c3b', '#5b4b44', '#615640', '#5a5a3c', '#4b5637', '#3f5441',
    '#355453', '#375360', '#334967', '#45425f', '#55445d', '#544d43', '#2f343a'
];

export function degToRad(v) {
    return (v * Math.PI) / 180;
}

export function radToDeg(v) {
    return (v * 180) / Math.PI;
}

function normalizeAngle(angle) {
    let out = Number(angle) || 0;
    while (out >= 360) out -= 360;
    while (out < 0) out += 360;
    return out;
}

function crossProduct(a, b) {
    return [
        (a[1] * b[2]) - (a[2] * b[1]),
        (a[2] * b[0]) - (a[0] * b[2]),
        (a[0] * b[1]) - (a[1] * b[0])
    ];
}

function normalizeVector(v) {
    const magnitude = Math.hypot(v[0], v[1], v[2]);
    if (!magnitude) return null;
    return [v[0] / magnitude, v[1] / magnitude, v[2] / magnitude];
}

function getCountryPalette(theme) {
    return theme === 'dark' ? COUNTRY_PALETTE_DARK : COUNTRY_PALETTE_LIGHT;
}

function latLngToVector(lat, lng) {
    const latRad = degToRad(lat);
    const lngRad = degToRad(lng);
    const cosLat = Math.cos(latRad);
    return [
        cosLat * Math.cos(lngRad),
        cosLat * Math.sin(lngRad),
        Math.sin(latRad)
    ];
}

export function getCountryFeatureKey(feature) {
    const p = feature?.properties || {};
    return p.ADM0_A3 || p.ISO_A2 || p.SOV_A3 || p.BRK_A3 || p.NAME || p.ADMIN || 'UNKNOWN';
}

export function getCountryFillForKey(key, theme = 'light') {
    const palette = getCountryPalette(theme);
    let hash = 0;
    const text = String(key || 'UNKNOWN');
    for (let i = 0; i < text.length; i++) {
        hash = ((hash << 5) - hash) + text.charCodeAt(i);
        hash |= 0;
    }
    return palette[Math.abs(hash) % palette.length];
}

export function getCountryFillForFeature(feature, theme = 'light') {
    const p = feature?.properties || {};
    const rawIndex = Number(p.MAPCOLOR13 ?? p.mapcolor13 ?? p.MAPCOLOR9 ?? p.mapcolor9);
    if (Number.isFinite(rawIndex) && rawIndex > 0) {
        const palette = getCountryPalette(theme);
        return palette[(Math.floor(rawIndex) - 1) % palette.length];
    }
    return getCountryFillForKey(getCountryFeatureKey(feature), theme);
}

function dotProduct(a, b) {
    return (a[0] * b[0]) + (a[1] * b[1]) + (a[2] * b[2]);
}

function vectorToLatLng(vector) {
    const [x, y, z] = vector;
    return [
        radToDeg(Math.asin(Math.max(-1, Math.min(1, z)))),
        normalizeLongitude(radToDeg(Math.atan2(y, x)))
    ];
}

function daysSinceJ2000(date) {
    return (date.getTime() / 86400000) - 10957.5;
}

const J1970 = 2440588;
const J2000 = 2451545;
const JULIAN_DAY_MS = 86400000;
const SUNRISE_SET_ALTITUDE = -0.833;

function buildGraylineBasis(subsolarLat, subsolarLng) {
    const sunVector = latLngToVector(subsolarLat, subsolarLng);
    let refVector = Math.abs(sunVector[2]) > 0.9 ? [1, 0, 0] : [0, 0, 1];
    let u = normalizeVector(crossProduct(sunVector, refVector));

    if (!u) {
        refVector = [0, 1, 0];
        u = normalizeVector(crossProduct(sunVector, refVector));
    }

    if (!u) return null;

    const v = normalizeVector(crossProduct(sunVector, u));
    if (!v) return null;

    return { u, v };
}

export function latLngToLocator(lat, lng, precision = 4) {
    lng = Math.max(-180, Math.min(180, lng));
    lat = Math.max(-90, Math.min(90, lat));
    let _lon = lng + 180;
    let _lat = lat + 90;
    let char1 = String.fromCharCode(65 + Math.floor(_lon / 20));
    let char2 = String.fromCharCode(65 + Math.floor(_lat / 10));
    _lon = _lon % 20;
    _lat = _lat % 10;
    let char3 = String.fromCharCode(48 + Math.floor(_lon / 2));
    let char4 = String.fromCharCode(48 + Math.floor(_lat / 1));
    
    if (precision < 6) return char1 + char2 + char3 + char4;
    
    _lon = (_lon % 2) * 60;
    _lat = (_lat % 1) * 60;
    let char5 = String.fromCharCode(65 + Math.floor(_lon / 5));
    let char6 = String.fromCharCode(65 + Math.floor(_lat / 2.5));
    return char1 + char2 + char3 + char4 + char5 + char6;
}

export function normalizeLongitude(lng) {
    let out = Number(lng) || 0;
    while (out > 180) out -= 360;
    while (out < -180) out += 360;
    return out;
}

export function getSubsolarPoint(date = new Date()) {
    const dayCount = daysSinceJ2000(date);
    const meanLongitude = normalizeAngle(280.460 + (0.9856474 * dayCount));
    const meanAnomaly = normalizeAngle(357.528 + (0.9856003 * dayCount));
    const meanAnomalyRad = degToRad(meanAnomaly);
    const eclipticLongitude = normalizeAngle(
        meanLongitude +
        (1.915 * Math.sin(meanAnomalyRad)) +
        (0.020 * Math.sin(2 * meanAnomalyRad))
    );
    const obliquity = 23.439 - (0.0000004 * dayCount);
    const eclipticLongitudeRad = degToRad(eclipticLongitude);
    const obliquityRad = degToRad(obliquity);

    const declination = radToDeg(
        Math.asin(Math.sin(obliquityRad) * Math.sin(eclipticLongitudeRad))
    );
    const rightAscension = normalizeAngle(radToDeg(
        Math.atan2(
            Math.cos(obliquityRad) * Math.sin(eclipticLongitudeRad),
            Math.cos(eclipticLongitudeRad)
        )
    ));
    const gmst = normalizeAngle(280.46061837 + (360.98564736629 * dayCount));

    return {
        lat: declination,
        lng: normalizeLongitude(rightAscension - gmst)
    };
}

function resolveSubsolarPoint(source = new Date()) {
    if (source instanceof Date) return getSubsolarPoint(source);
    if (source && Number.isFinite(source.lat) && Number.isFinite(source.lng)) {
        return {
            lat: Number(source.lat),
            lng: normalizeLongitude(Number(source.lng))
        };
    }
    return getSubsolarPoint(new Date());
}

export const GRAYLINE_TWILIGHT_WIDTH_DEGREES = 15;

function toJulian(date) {
    return (date.getTime() / JULIAN_DAY_MS) - 0.5 + J1970;
}

function fromJulian(julianDate) {
    return new Date((julianDate + 0.5 - J1970) * JULIAN_DAY_MS);
}

function toDays(date) {
    return toJulian(date) - J2000;
}

function solarMeanAnomaly(days) {
    return degToRad(357.5291 + (0.98560028 * days));
}

function eclipticLongitude(meanAnomaly) {
    const equationOfCenter = degToRad(
        (1.9148 * Math.sin(meanAnomaly)) +
        (0.02 * Math.sin(2 * meanAnomaly)) +
        (0.0003 * Math.sin(3 * meanAnomaly))
    );
    const perihelion = degToRad(102.9372);
    return meanAnomaly + equationOfCenter + perihelion + Math.PI;
}

function rightAscension(eclipticLng) {
    return Math.atan2(Math.sin(eclipticLng) * Math.cos(degToRad(23.4397)), Math.cos(eclipticLng));
}

function solarDeclination(eclipticLng) {
    return Math.asin(Math.sin(eclipticLng) * Math.sin(degToRad(23.4397)));
}

function julianCycle(days, lw) {
    return Math.round(days - 0.0009 - (lw / (2 * Math.PI)));
}

function approxTransit(hourAngle, lw, cycle) {
    return 0.0009 + ((hourAngle + lw) / (2 * Math.PI)) + cycle;
}

function solarTransitJulian(ds, meanAnomaly, eclipticLng) {
    return J2000 + ds + (0.0053 * Math.sin(meanAnomaly)) - (0.0069 * Math.sin(2 * eclipticLng));
}

function solarHourAngle(altitude, latitudeRad, declinationRad) {
    const numerator = Math.sin(altitude) - (Math.sin(latitudeRad) * Math.sin(declinationRad));
    const denominator = Math.cos(latitudeRad) * Math.cos(declinationRad);
    const value = numerator / denominator;

    if (value <= -1) return Math.PI;
    if (value >= 1) return null;
    return Math.acos(value);
}

function getSunTimesForDate(date, lat, lng) {
    const lw = degToRad(-lng);
    const latitudeRad = degToRad(lat);
    const days = toDays(date);
    const cycle = julianCycle(days, lw);
    const ds = approxTransit(0, lw, cycle);
    const meanAnomaly = solarMeanAnomaly(ds);
    const eclipticLng = eclipticLongitude(meanAnomaly);
    const declinationRad = solarDeclination(eclipticLng);
    const solarNoonJulian = solarTransitJulian(ds, meanAnomaly, eclipticLng);
    const hourAngle = solarHourAngle(degToRad(SUNRISE_SET_ALTITUDE), latitudeRad, declinationRad);

    if (hourAngle == null) {
        return { sunrise: null, sunset: null };
    }

    const setJulian = solarTransitJulian(approxTransit(hourAngle, lw, cycle), meanAnomaly, eclipticLng);
    const riseJulian = solarNoonJulian - (setJulian - solarNoonJulian);

    return {
        sunrise: fromJulian(riseJulian),
        sunset: fromJulian(setJulian)
    };
}

export function getClosestSunEvent(lat, lng, now = new Date(), windowMinutes = 90) {
    const events = [];
    const dayOffsets = [-1, 0, 1];

    dayOffsets.forEach(offset => {
        const date = new Date(now);
        date.setUTCDate(date.getUTCDate() + offset);
        date.setUTCHours(12, 0, 0, 0);

        const { sunrise, sunset } = getSunTimesForDate(date, lat, lng);
        if (sunrise) events.push({ type: 'sunrise', time: sunrise });
        if (sunset) events.push({ type: 'sunset', time: sunset });
    });

    if (!events.length) return null;

    let closest = null;
    events.forEach(event => {
        const deltaMinutes = Math.round((now.getTime() - event.time.getTime()) / 60000);
        if (!closest || Math.abs(deltaMinutes) < Math.abs(closest.deltaMinutes)) {
            closest = { type: event.type, time: event.time, deltaMinutes };
        }
    });

    if (!closest || Math.abs(closest.deltaMinutes) > windowMinutes) return null;
    return closest;
}

export function getSolarZenithAngle(lat, lng, source = new Date()) {
    const subsolarPoint = resolveSubsolarPoint(source);
    const pointVector = latLngToVector(lat, lng);
    const sunVector = latLngToVector(subsolarPoint.lat, subsolarPoint.lng);
    const cosine = Math.max(-1, Math.min(1, dotProduct(pointVector, sunVector)));
    return radToDeg(Math.acos(cosine));
}

export function getGraylineOverlayOpacities(lat, lng, source = new Date(), options = {}) {
    const twilightWidthDegrees = Math.max(1, Number(options.twilightWidthDegrees) || GRAYLINE_TWILIGHT_WIDTH_DEGREES);
    const maxGraylineOpacity = Math.max(0, Math.min(1, Number(options.maxGraylineOpacity) || 0.18));
    const maxNightOpacity = Math.max(0, Math.min(1, Number(options.maxNightOpacity) || 0.34));
    const subsolarPoint = resolveSubsolarPoint(source);
    const zenithAngle = getSolarZenithAngle(lat, lng, subsolarPoint);
    const twilightHalfWidth = twilightWidthDegrees / 2;
    const twilightDayEdge = 90 - twilightHalfWidth;
    const twilightNightEdge = 90 + twilightHalfWidth;

    if (zenithAngle <= twilightDayEdge) {
        return { graylineOpacity: 0, nightOpacity: 0, zenithAngle };
    }

    if (zenithAngle < twilightNightEdge) {
        const twilightRatio = (zenithAngle - twilightDayEdge) / (twilightNightEdge - twilightDayEdge);
        const graylineOpacity = maxGraylineOpacity * Math.sin(Math.PI * twilightRatio);
        const nightOpacity = twilightRatio > 0.5
            ? maxNightOpacity * 0.35 * ((twilightRatio - 0.5) / 0.5)
            : 0;
        return { graylineOpacity, nightOpacity, zenithAngle };
    }

    const nightRatio = Math.min(1, Math.max(0, (zenithAngle - twilightNightEdge) / (180 - twilightNightEdge)));
    const nightOpacity = (maxNightOpacity * 0.35) + ((maxNightOpacity * 0.65) * Math.pow(nightRatio, 0.72));
    return { graylineOpacity: 0, nightOpacity, zenithAngle };
}

export function getGraylineOverlayCells(source = new Date(), options = {}) {
    const latStep = Math.max(1, Number(options.latStep) || 5);
    const lngStep = Math.max(1, Number(options.lngStep) || 10);
    const subsolarPoint = resolveSubsolarPoint(source);
    const cells = [];

    for (let lat = -90; lat < 90; lat += latStep) {
        const nextLat = Math.min(90, lat + latStep);
        const centerLat = lat + ((nextLat - lat) / 2);

        for (let lng = -180; lng < 180; lng += lngStep) {
            const nextLng = Math.min(180, lng + lngStep);
            const centerLng = lng + ((nextLng - lng) / 2);
            const { graylineOpacity, nightOpacity } = getGraylineOverlayOpacities(centerLat, centerLng, subsolarPoint, options);

            if (graylineOpacity <= 0 && nightOpacity <= 0) continue;

            cells.push({
                bounds: [[lat, lng], [nextLat, nextLng]],
                graylineOpacity,
                nightOpacity
            });
        }
    }

    return cells;
}

export function getGraylineSegments(date = new Date(), stepDegrees = 2) {
    const { lat, lng } = getSubsolarPoint(date);
    const basis = buildGraylineBasis(lat, lng);
    if (!basis) return [];

    const segments = [];
    let currentSegment = [];
    let previousPoint = null;

    for (let angle = 0; angle <= 360; angle += stepDegrees) {
        const angleRad = degToRad(angle);
        const pointVector = [
            (basis.u[0] * Math.cos(angleRad)) + (basis.v[0] * Math.sin(angleRad)),
            (basis.u[1] * Math.cos(angleRad)) + (basis.v[1] * Math.sin(angleRad)),
            (basis.u[2] * Math.cos(angleRad)) + (basis.v[2] * Math.sin(angleRad))
        ];
        const currentPoint = vectorToLatLng(pointVector);

        if (previousPoint && Math.abs(currentPoint[1] - previousPoint[1]) > 180) {
            if (currentSegment.length > 1) segments.push(currentSegment);
            currentSegment = [];
        }

        currentSegment.push(currentPoint);
        previousPoint = currentPoint;
    }

    if (currentSegment.length > 1) segments.push(currentSegment);
    return segments;
}

export function locatorToBounds(locator) {
    if (!locator || locator.length < 4) return null;
    locator = locator.toUpperCase();
    let lng = (locator.charCodeAt(0) - 65) * 20 - 180;
    let lat = (locator.charCodeAt(1) - 65) * 10 - 90;
    lng += (locator.charCodeAt(2) - 48) * 2;
    lat += (locator.charCodeAt(3) - 48) * 1;
    
    if (locator.length >= 6) {
        lng += (locator.charCodeAt(4) - 65) * (5/60);
        lat += (locator.charCodeAt(5) - 65) * (2.5/60);
        return [[lat, lng], [lat + (2.5/60), lng + (5/60)]];
    }
    return [[lat, lng], [lat + 1, lng + 2]];
}

// --- Great-circle geometry (shared by both map projections) ---
// Spherical-earth approximations; accurate enough for drawing beam/path lines.

const _toRad = degToRad;
const _toDeg = radToDeg;

// Shared geodesic distance (haversine, 6371km). Alias kept for callers that
// import haversineKm by name; greatCircleDistanceKm is the same computation.
export function haversineKm(aLat, aLng, bLat, bLng) {
    return greatCircleDistanceKm(aLat, aLng, bLat, bLng);
}

// Shared hex color helpers. hexToRgb returns [r,g,b]; hexToRgba returns a CSS
// string; blendOverlayColors does premultiplied-alpha compositing.
export function hexToRgb(hex) {
    const value = String(hex || '').replace('#', '');
    if (value.length !== 6) return [0, 0, 0];
    return [
        Number.parseInt(value.slice(0, 2), 16),
        Number.parseInt(value.slice(2, 4), 16),
        Number.parseInt(value.slice(4, 6), 16)
    ];
}

export function hexToRgba(hex, alpha = 1) {
    const c = String(hex || '').replace('#', '').trim();
    if (c.length !== 6) return `rgba(100,116,139,${alpha})`;
    const r = Number.parseInt(c.slice(0, 2), 16);
    const g = Number.parseInt(c.slice(2, 4), 16);
    const b = Number.parseInt(c.slice(4, 6), 16);
    return `rgba(${r},${g},${b},${alpha})`;
}

export function blendOverlayColors(base, color, alpha) {
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

export function greatCircleDistanceKm(aLat, aLng, bLat, bLng) {
    const dLat = _toRad(bLat - aLat);
    const dLng = _toRad(bLng - aLng);
    const la1 = _toRad(aLat);
    const la2 = _toRad(bLat);
    const h = Math.sin(dLat / 2) ** 2 + Math.cos(la1) * Math.cos(la2) * Math.sin(dLng / 2) ** 2;
    return 6371 * 2 * Math.atan2(Math.sqrt(h), Math.sqrt(1 - h));
}

export function initialBearingDeg(aLat, aLng, bLat, bLng) {
    const la1 = _toRad(aLat);
    const la2 = _toRad(bLat);
    const dLng = _toRad(bLng - aLng);
    const y = Math.sin(dLng) * Math.cos(la2);
    const x = Math.cos(la1) * Math.sin(la2) - Math.sin(la1) * Math.cos(la2) * Math.cos(dLng);
    return (_toDeg(Math.atan2(y, x)) + 360) % 360;
}

// greatCirclePoints samples the geodesic between two points (inclusive of both
// ends) via spherical linear interpolation, returning [[lat,lng],…]. Used for
// the Mercator polyline and the azimuthal canvas line so the math lives once.
export function greatCirclePoints(aLat, aLng, bLat, bLng, segments = 48) {
    const la1 = _toRad(aLat);
    const lo1 = _toRad(aLng);
    const la2 = _toRad(bLat);
    const lo2 = _toRad(bLng);

    const d = 2 * Math.asin(Math.sqrt(
        Math.sin((la2 - la1) / 2) ** 2 +
        Math.cos(la1) * Math.cos(la2) * Math.sin((lo2 - lo1) / 2) ** 2
    ));
    const n = Math.max(1, Math.floor(segments));
    const out = [];
    if (d === 0 || !Number.isFinite(d)) {
        return [[aLat, aLng], [bLat, bLng]];
    }
    const sinD = Math.sin(d);
    for (let i = 0; i <= n; i += 1) {
        const f = i / n;
        const A = Math.sin((1 - f) * d) / sinD;
        const B = Math.sin(f * d) / sinD;
        const x = A * Math.cos(la1) * Math.cos(lo1) + B * Math.cos(la2) * Math.cos(lo2);
        const y = A * Math.cos(la1) * Math.sin(lo1) + B * Math.cos(la2) * Math.sin(lo2);
        const z = A * Math.sin(la1) + B * Math.sin(la2);
        const lat = Math.atan2(z, Math.sqrt(x * x + y * y));
        const lng = Math.atan2(y, x);
        out.push([_toDeg(lat), _toDeg(lng)]);
    }
    return out;
}

export function continentFromLatLng(lat, lng) {
    if (!Number.isFinite(lat) || !Number.isFinite(lng)) return null;

    // Antarctica
    if (lat <= -60) return 'antarctica';

    // North America (incl. Central America + Greenland)
    if (lat >= 7 && lng >= -170 && lng <= -20) return 'north-america';

    // South America
    if (lat < 13 && lat >= -56 && lng >= -92 && lng <= -30) return 'south-america';

    // Europe
    if (lat >= 35 && lat <= 72 && lng >= -31 && lng <= 60) return 'europe';

    // Africa
    if (lat >= -35 && lat <= 38 && lng >= -20 && lng <= 55) return 'africa';

    // Asia
    if (lat >= 5 && lat <= 82 && lng >= 25 && lng <= 180) return 'asia';

    // Oceania (Australia, NZ, Pacific islands)
    if (lat >= -50 && lat <= 30 && ((lng >= 110 && lng <= 180) || (lng >= -180 && lng <= -140))) return 'oceania';

    return null;
}

export function continentFromLocator(locator) {
    const bounds = locatorToBounds(locator);
    if (!bounds) return null;
    const lat = (bounds[0][0] + bounds[1][0]) / 2;
    const lng = (bounds[0][1] + bounds[1][1]) / 2;
    return continentFromLatLng(lat, lng);
}

export function setFaviconColor(color) {
    const favicon = document.getElementById('favicon');
    if (favicon) {
        const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="8" fill="${color}"/></svg>`;
        favicon.href = `data:image/svg+xml,${encodeURIComponent(svg)}`;
    }
}

export const bandColors = {
    '160m': '#8B0000',
    '80m': '#800080',
    '60m': '#4B0082',
    '40m': '#0000FF',
    '30m': '#03b1b1',
    '20m': '#008000',
    '17m': '#808000',
    '15m': '#FFA500',
    '12m': '#00FFFF',
    '10m': '#FF0000',
    '6m':  '#FF00FF',
    '4m':  '#FF1493',
    '2m':  '#008080',
    'all': '#555555'
};

export function getGridResolution() {
    const target = document.getElementById('target')?.value.trim() || '';
    if (/^[A-Za-z]{2}[0-9]{2}[A-Za-z]{2}/.test(target)) {
        return 6;
    }
    return 4;
}

export function normalizeDxPulseTarget(target) {
    const value = String(target || '').trim().toUpperCase();
    if (!/^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(value)) {
        return '';
    }
    return value.slice(0, 4);
}

export function buildDxPulseUrl({ target, minutes, surroundings = false, mode = 'quality', lookbackDays = 45 } = {}) {
    const locator = normalizeDxPulseTarget(target);
    if (!locator) {
        return '';
    }
    const params = new URLSearchParams();
    params.set('target', locator);
    params.set('mode', mode === 'anomaly' ? 'anomaly' : 'quality');

    const parsedMinutes = Number.parseInt(minutes, 10);
    if (Number.isFinite(parsedMinutes) && parsedMinutes > 0) {
        params.set('minutes', String(parsedMinutes));
    }

    const parsedLookback = Number.parseInt(lookbackDays, 10);
    if (Number.isFinite(parsedLookback) && parsedLookback > 0) {
        params.set('lookback_days', String(parsedLookback));
    }

    if (surroundings) {
        params.set('surroundings', 'true');
    }

    return `/dxpulse/?${params.toString()}`;
}

export function getMinSnrMode() {
    const checkedRadio = document.querySelector('input[name="min-snr"]:checked');
    return checkedRadio ? checkedRadio.value : 'none';
}

export function getSelectedBand() {
    const checkedRadio = document.querySelector('input[name="band"]:checked');
    return checkedRadio ? checkedRadio.value : 'all';
}

export function getEnabledBands() {
    const enabled = new Set();
    document.querySelectorAll('.band-enable').forEach(cb => {
        if (cb.checked) enabled.add(cb.value);
    });
    return enabled;
}

export function getGraylineEnabled() {
    return true;
}

// Forecast overlay: rising-activity directional halo.
// Local-only toggle (no server config), default on.
export function getForecastEnabled() {
    const toggle = document.getElementById('show-forecast');
    if (toggle) return toggle.checked;

    const saved = (typeof localStorage !== 'undefined' && localStorage)
        ? localStorage.getItem('forecastEnabled')
        : null;
    if (saved === null) return true;
    return saved === 'true';
}

export function getCountryColoringEnabled() {
    const toggle = document.getElementById('show-country-coloring');
    if (toggle) return toggle.checked;

    const saved = (typeof localStorage !== 'undefined' && localStorage)
        ? localStorage.getItem('countryColoringEnabled')
        : null;
    if (saved === null) return true;
    return saved === 'true';
}

export function getMercatorDxccLabelsEnabled() {
    const toggle = document.getElementById('show-dxcc-labels');
    if (toggle) return toggle.checked;
    return localStorage.getItem('mercatorDxccLabelsEnabled') === 'true';
}