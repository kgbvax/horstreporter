export function formatNumber(num) {
    if (num == null) return '';
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

// --- Inline SVG icons (replaces the Font Awesome webfont + all.min.css) ---
// Path data extracted from the vendored Font Awesome 6 Free fa-solid-900 font
// (CC BY 4.0 — https://fontawesome.com/license/free; the licensed font itself
// stays untouched in static/vendor/), so the rendered glyph shapes are
// unchanged. Keys are the FA names the app used (`fa-<key>`). Each SVG renders
// in a 1em currentColor box aligned like FA's svg-inline--fa (.icon in
// style.css); pass spin=true for the rotating variant (was `fa-spin`).
// data-glyph records the FA provenance (tests assert on `fa-stop`/`fa-play`).
const ICONS = {
    'play':            [384, 'M73 39Q49 25 25 38Q1 52 0 80V432Q1 460 25 474Q49 487 73 473L361 297Q383 283 384 256Q383 230 361 215L73 39Z'],
    'pause':           [320, 'M48 64Q28 65 14 78Q1 92 0 112V400Q1 420 14 434Q28 447 48 448H80Q100 447 114 434Q127 420 128 400V112Q127 92 114 78Q100 65 80 64H48ZM240 64Q220 65 206 78Q193 92 192 112V400Q193 420 206 434Q220 447 240 448H272Q292 447 306 434Q319 420 320 400V112Q319 92 306 78Q292 65 272 64H240Z'],
    'stop':            [384, 'M0 128Q1 101 19 83Q37 65 64 64H320Q347 65 365 83Q383 101 384 128V384Q383 411 365 429Q347 447 320 448H64Q37 447 19 429Q1 411 0 384V128Z'],
    'spinner':         [512, 'M304 48Q303 21 280 6Q256 -6 232 6Q209 21 208 48Q209 75 232 90Q256 102 280 90Q303 75 304 48ZM304 464Q303 437 280 422Q256 410 232 422Q209 437 208 464Q209 491 232 506Q256 518 280 506Q303 491 304 464ZM48 304Q75 303 90 280Q102 256 90 232Q75 209 48 208Q21 209 6 232Q-6 256 6 280Q21 303 48 304ZM512 256Q511 229 488 214Q464 202 440 214Q417 229 416 256Q417 283 440 298Q464 310 488 298Q511 283 512 256ZM143 437Q162 417 155 391Q147 365 121 357Q95 350 75 369Q56 389 63 415Q70 442 97 449Q123 456 143 437ZM143 143Q163 123 156 96Q149 69 122 61Q95 55 75 75Q56 95 63 121Q70 147 97 155Q123 162 143 143ZM369 437Q389 456 415 449Q442 442 449 415Q456 389 437 369Q417 350 391 357Q365 365 357 391Q350 417 369 437Z'],
    'sun':             [512, 'M362 1Q369 5 371 13L391 121L499 141Q507 143 511 150Q514 158 509 166L447 256L509 346Q514 354 511 362Q507 369 499 371L391 391L371 499Q369 507 362 511Q354 514 346 509L256 447L166 509Q158 514 151 511Q143 507 141 499L121 391L13 371Q5 369 1 362Q-2 354 3 346L65 256L3 166Q-2 158 1 151Q5 143 13 141L121 121L141 13Q143 5 151 1Q158 -2 166 3L256 65L346 3Q354 -2 362 1ZM160 256Q160 230 173 208Q186 186 208 173Q231 160 256 160Q281 160 304 173Q326 186 339 208Q352 230 352 256Q352 282 339 304Q326 326 304 339Q281 352 256 352Q231 352 208 339Q186 326 173 304Q160 282 160 256ZM384 256Q384 221 367 192Q350 163 320 145Q290 128 256 128Q222 128 192 145Q162 163 145 192Q128 221 128 256Q128 291 145 320Q162 349 192 367Q222 384 256 384Q290 384 320 367Q350 349 367 320Q384 291 384 256Z'],
    'moon':            [384, 'M224 32Q161 33 111 63Q60 92 30 143Q1 193 0 256Q1 319 31 369Q60 420 111 449Q161 479 224 480Q317 478 379 417Q387 408 382 398Q377 388 365 389Q351 392 335 392Q261 390 211 340Q162 291 160 216Q160 166 184 126Q209 86 249 63Q259 57 257 45Q254 35 242 33Q233 32 223 32Z'],
    'question-circle': [512, 'M256 512Q326 511 384 478Q442 444 478 384Q512 323 512 256Q512 189 478 128Q442 68 384 34Q326 1 256 0Q186 1 128 34Q70 68 34 128Q0 189 0 256Q0 323 34 384Q70 444 128 478Q186 511 256 512ZM170 165Q176 148 190 138Q204 128 223 128H281Q308 129 326 147Q343 164 344 191Q343 227 312 246L280 264Q278 286 256 288Q234 286 232 264V251Q232 237 244 230L288 204Q296 200 296 191Q295 177 281 176H223Q217 176 215 181V183Q205 203 184 197Q164 188 170 167V165ZM224 352Q224 338 233 329Q242 320 256 320Q270 320 279 329Q288 338 288 352Q288 366 279 375Q270 384 256 384Q242 384 233 375Q224 366 224 352Z'],
    'chevron-right':   [320, 'M311 233Q320 243 320 256Q320 269 311 279L119 471Q109 480 96 480Q83 480 73 471Q64 461 64 448Q64 435 73 425L243 256L73 87Q64 77 64 64Q64 51 73 41Q83 32 96 32Q109 32 119 41L311 233Z'],
    'chevron-left':    [320, 'M9 233Q0 243 0 256Q0 269 9 279L201 471Q211 480 224 480Q237 480 247 471Q256 461 256 448Q256 435 247 425L77 256L247 87Q256 77 256 64Q256 51 247 41Q237 32 224 32Q211 32 201 41L9 233Z'],
    'map-marker-alt':  [384, 'M216 499Q243 466 282 410Q321 355 352 296Q382 237 384 192Q382 110 328 56Q274 2 192 0Q110 2 56 56Q2 110 0 192Q2 237 32 296Q63 355 102 410Q141 466 168 499Q178 511 192 511Q206 511 216 499ZM192 128Q228 129 247 160Q265 192 247 224Q228 255 192 256Q156 255 137 224Q119 192 137 160Q156 129 192 128Z']
};

// icon returns inline SVG markup for an ICONS glyph; spin=true adds the
// rotating variant (the old `fa-spin`, now `.icon-spin` in style.css).
export function icon(name, spin = false) {
    const g = ICONS[name];
    if (!g) return '';
    return `<svg class="icon${spin ? ' icon-spin' : ''}" data-glyph="fa-${name}" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${g[0]} 512" width="1em" height="1em" fill="currentColor" aria-hidden="true"><path d="${g[1]}"/></svg>`;
}

// The Go/Stop submit button (#btn-submit) toggles between an idle ("go") and a
// streaming ("stop") state. The state lives in the data-mode attribute rather
// than the visible text so the button can stay an icon-only, fixed-width pill.
// setSubmitMode keeps the icon, accessible label, and state attribute in sync;
// isStreaming reads the attribute (replacing the old textContent === 'Stop'
// check).
export function setSubmitMode(btn, mode) {
    if (!btn) return;
    const stop = mode === 'stop';
    btn.dataset.mode = stop ? 'stop' : 'go';
    btn.innerHTML = stop ? icon('stop') : icon('play');
    const label = stop ? 'Stop' : 'Go';
    btn.title = label;
    btn.setAttribute('aria-label', label);
    // Red highlight = not playing: while streaming the button is the normal
    // primary; once stopped (or before the first start) it goes red.
    btn.classList.toggle('not-streaming', !stop);
}

export function isStreaming(btn) {
    return btn?.dataset?.mode === 'stop';
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

function getCountryFeatureKey(feature) {
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

function daysSinceJ2000(date) {
    return (date.getTime() / 86400000) - 10957.5;
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

// freqHzToBand maps a frequency in Hz to its amateur band label (matching the
// bandColors keys), or '' when it falls outside a known band. Used by the opmode
// status line to show the band for the last-tuned frequency.
const BAND_RANGES_HZ = [
    ['160m', 1_800_000, 2_000_000],
    ['80m', 3_500_000, 4_000_000],
    ['60m', 5_250_000, 5_450_000],
    ['40m', 7_000_000, 7_300_000],
    ['30m', 10_100_000, 10_150_000],
    ['20m', 14_000_000, 14_350_000],
    ['17m', 18_068_000, 18_168_000],
    ['15m', 21_000_000, 21_450_000],
    ['12m', 24_890_000, 24_990_000],
    ['10m', 28_000_000, 29_700_000],
    ['6m', 50_000_000, 54_000_000],
    ['4m', 70_000_000, 70_500_000],
    ['2m', 144_000_000, 148_000_000]
];

export function freqHzToBand(hz) {
    const f = Number(hz);
    if (!Number.isFinite(f) || f <= 0) return '';
    for (const [label, lo, hi] of BAND_RANGES_HZ) {
        if (f >= lo && f <= hi) return label;
    }
    return '';
}

export function getGridResolution() {
    // Grid mode always highlights at the 4-character Maidenhead square level,
    // regardless of the target locator's precision. This keeps the Mercator Grid
    // view readable and consistent; subsquare precision is not appropriate for
    // the colored cell overlay.
    return 4;
}

// Grid-square highlight grading (Grid-SNR view). A square's brightness is
// driven by topQuartileMean() of its filtered spot SNRs mapped through the
// gridSnrOpacity() ramp — a robust reachability signal: a single lucky decode
// among many weak ones can no longer light up a square (the old max-SNR
// 3-tier grading was removed after the A/B experiment, Jul 2026).
//
// Robust square score: mean of the strongest quarter of (filtered) reports
// (k = ceil(n/4)). Tiny squares (n <= 4) fall back to their best spot, which
// keeps sparse-but-strong squares visible.
export function topQuartileMean(snrs) {
    if (!snrs || snrs.length === 0) return NaN;
    // Callers push Number(spot.snr), so values are already numbers; a single
    // filter pass drops any non-finite values without the extra spread/map
    // allocations (3 -> 1 per square, called on every heat-layer rebuild).
    const sorted = snrs.filter((v) => Number.isFinite(Number(v))).sort((a, b) => b - a);
    if (sorted.length === 0) return NaN;
    const k = Math.max(1, Math.ceil(sorted.length / 4));
    let sum = 0;
    for (let i = 0; i < k; i++) sum += sorted[i];
    return sum / k;
}

// Continuous opacity ramp for a square score (dB), anchored to keep today's
// feel: 0 dB -> 0.45 (old "medium"), cap 0.75 near the old max. Below 0 dB
// the ramp falls off faster (-10 dB -> 0.15, floor 0.10) so weak squares stay
// subdued; above 0 dB it rises gently so strong paths don't oversaturate.
export function gridSnrOpacity(scoreDb) {
    const s = Number(scoreDb);
    if (!Number.isFinite(s)) return 0.10;
    if (s < 0) return Math.max(0.10, 0.45 + 0.03 * s);
    return Math.min(0.75, 0.45 + 0.015 * s);
}

export function getMinSnrMode() {
    const checkedRadio = document.querySelector('input[name="min-snr"]:checked');
    return checkedRadio ? checkedRadio.value : 'none';
}

export function getSelectedBand() {
    // Focus (solo) band lives on the band-container's data-focus-band attribute;
    // empty means no focus ('all' = show all enabled bands).
    const container = document.getElementById('band-container');
    const focus = container?.dataset.focusBand;
    return focus && focus.length ? focus : 'all';
}

// pillTextColor picks a legible text color (dark/light) for a given band's
// solid hex background using perceived luminance.
export function pillTextColor(hex) {
    const v = String(hex || '').replace('#', '');
    if (v.length !== 6) return '#ffffff';
    const r = Number.parseInt(v.slice(0, 2), 16);
    const g = Number.parseInt(v.slice(2, 4), 16);
    const b = Number.parseInt(v.slice(4, 6), 16);
    const lum = (0.299 * r) + (0.587 * g) + (0.114 * b);
    return lum > 150 ? '#212529' : '#ffffff';
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

// --- WSPR region classification + cluster anchor ---

// locatorToLatLngJS converts a Maidenhead locator to (lat, lng).
// Mirrors Go's spot.go:locatorToLatLng. Returns null for invalid locators.
function locatorToLatLngJS(locator) {
    if (!locator || typeof locator !== 'string') return null;
    locator = locator.toUpperCase().trim();
    if (locator.length < 2) return null;
    let lng = (locator.charCodeAt(0) - 65) * 20 - 180;
    let lat = (locator.charCodeAt(1) - 65) * 10 - 90;
    if (locator.length >= 4) {
        lng += Number(locator[2]) * 2;
        lat += Number(locator[3]) * 1;
        if (locator.length >= 6) {
            lng += (locator.charCodeAt(4) - 65) * (5 / 60) + (5 / 120);
            lat += (locator.charCodeAt(5) - 65) * (2.5 / 60) + (2.5 / 120);
        } else {
            lng += 1.0;
            lat += 0.5;
        }
    } else {
        lng += 10.0;
        lat += 5.0;
    }
    return { lat, lng };
}

// DXPulse region bounding-box classifier — mirrors Go's dx_regions.go:48-83.
// Order matters: sub-regions tested before their containing continent.
// Returns one of: EU, NA, SA, AF, AS, OC, AN, JA, VK, KH6, CAR, or '' for unknown.
export function regionForLocator(loc) {
    const ll = locatorToLatLngJS(loc);
    if (!ll) return '';
    return regionForLatLng(ll.lat, ll.lng);
}

// Memoized regionForLocator for hot per-frame paths (WSPR marker scoping, the
// WSPR matrix). Locators repeat heavily across spots, so the cache turns the
// per-spot locator parse + classification into an O(1) lookup. Bounded so a
// pathological locator flood can't grow it unboundedly.
const regionForLocatorCache = new Map();
export function regionForLocatorCached(loc) {
    if (!loc) return '';
    let r = regionForLocatorCache.get(loc);
    if (r === undefined) {
        r = regionForLocator(loc);
        regionForLocatorCache.set(loc, r);
        if (regionForLocatorCache.size > 20000) regionForLocatorCache.clear();
    }
    return r;
}

export function regionForLatLng(lat, lng) {
    if (lat <= -60) return 'AN';
    if (lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146) return 'JA';
    if (lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154) return 'KH6';
    if (lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60) return 'CAR';
    if (lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180) return 'VK';
    if (lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45) return 'EU';
    if (lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55) return 'AF';
    if (lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50) return 'NA';
    if (lat >= -60 && lat < 15 && lng >= -90 && lng <= -30) return 'SA';
    if (lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180) return 'AS';
    if (lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130)) return 'OC';
    return '';
}

// DXPulse region display order (matches Go's dxPulseAllRegions).
export const WSPR_REGIONS = ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'];