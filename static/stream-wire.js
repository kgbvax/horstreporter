// stream-wire.js — decoder for the compact /api/stream?v=2 frames (spec:
// stream_wire.go, docs/api.md). The decoder rebuilds the exact spot object
// shape the v1 stream produced so renderers, the session ring and the
// timeline never learn about the wire format.

import { locatorToLatLngJS } from './utils.js';

const SOURCE_NAMES = { d: 'dxcluster', r: 'rbn', w: 'wspr' };

// Locators repeat heavily (a few hundred squares per session), so lat/lng are
// memoized; the cap only guards against a pathological feed.
const LATLNG_CACHE_MAX = 5000;
const latLngCache = new Map();

function latLngFor(locator) {
    let hit = latLngCache.get(locator);
    if (hit) return hit;
    // Go's locatorToLatLng returns 0,0 for a locator shorter than 2 chars.
    const ll = locatorToLatLngJS(locator) || { lat: 0, lng: 0 };
    hit = ll;
    if (latLngCache.size >= LATLNG_CACHE_MAX) latLngCache.clear();
    latLngCache.set(locator, hit);
    return hit;
}

// decodeSpotTuple turns [locator, band, snr, age, reporter?, src?, sender?,
// receiver?] into a v1-shaped spot. n is the frame's server time (unix s).
function decodeSpotTuple(t, n) {
    if (!Array.isArray(t) || t.length < 4) return null;
    const [locator, band, snr, age, reporter, src, sender, receiver] = t;
    if (typeof locator !== 'string' || typeof band !== 'string' || !Number.isFinite(snr) || !Number.isFinite(age)) {
        return null;
    }
    const { lat, lng } = latLngFor(locator);
    const spot = {
        lat,
        lng,
        snr,
        ageSeconds: Math.max(0, age),
        locator,
        sourceType: src ? (SOURCE_NAMES[src] || src) : 'mqtt',
        band,
        // Exact spot time (server clock): stable across redeliveries, so the
        // resume overlap and reconnect dumps dedup exactly.
        __t: n - age,
    };
    if (reporter) spot.reporterLocator = reporter;
    if (sender) spot.sender = sender;
    if (receiver) spot.receiver = receiver;
    return spot;
}

/**
 * Decode one `spots` event payload. Returns { n, spots } or null when the
 * frame is malformed; individual malformed tuples are skipped.
 */
export function decodeSpotsFrame(data) {
    let frame;
    try {
        frame = JSON.parse(data);
    } catch (_) {
        return null;
    }
    if (!frame || !Number.isFinite(frame.n) || !Array.isArray(frame.s)) return null;
    const spots = [];
    for (const t of frame.s) {
        const spot = decodeSpotTuple(t, frame.n);
        if (spot) spots.push(spot);
    }
    return { n: frame.n, spots };
}

/**
 * Exact identity of a delivered spot, for deduping overlap and reconnect
 * dumps: source, band, calls, both locators, SNR and the spot time.
 * `fallbackT` supplies the time for spots that carry no __t (v1 frames).
 */
export function exactSpotKey(spot, fallbackT = 0) {
    const t = Number.isFinite(spot.__t) ? spot.__t : fallbackT;
    return [
        String(spot.sourceType || '').toLowerCase(),
        String(spot.band || '').toLowerCase(),
        String(spot.sender || '').toUpperCase(),
        String(spot.receiver || '').toUpperCase(),
        String(spot.locator || '').toUpperCase(),
        String(spot.reporterLocator || '').toUpperCase(),
        String(spot.snr ?? ''),
        String(t),
    ].join('|');
}

// Test hook.
export function __resetLatLngCache() {
    latLngCache.clear();
}
export const __latLngCacheSize = () => latLngCache.size;
