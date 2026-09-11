// ui-helpers.js — pure helpers for the UI wiring module (ui.js).
//
// ui.js pulls in map.js (Leaflet/DOM setup) and owns the init/control wiring,
// so its logic can't be unit-tested in place. This module holds the genuinely
// pure helpers — no DOM, no network, no module state — and ui.js imports them
// from here. The init/control closure paths stay untested (they run under the
// browser). Convention as in static/timeline.test.js.

import { locatorToBounds, initialBearingDeg } from './utils.js';

// Escape HTML metacharacters before interpolating attacker-influenced or
// server-supplied text into innerHTML (tooltips, report lists).
export const escapeHtml = (v) => String(v ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');

// getHoverSquareStyle returns the Leaflet polygon style for the hovered
// grid-square highlight, tuned per theme. `theme` is 'dark' or 'light' (the
// caller derives it from document.body[data-theme]).
export function getHoverSquareStyle(theme) {
    if (theme === 'dark') {
        return {
            color: '#ffe39a',
            weight: 2,
            opacity: 1,
            fillColor: '#ffe39a',
            fillOpacity: 0.20,
            interactive: false
        };
    }

    return {
        color: '#cc9a1f',
        weight: 2,
        opacity: 0.95,
        fillColor: '#ffd166',
        fillOpacity: 0.14,
        interactive: false
    };
}

// hoverSquareAzimuth returns the great-circle bearing from the qth square
// center to the hovered square center as a display string like " 302°".
// Empty when either value isn't a valid 4- or 6-char locator (the qth field
// also accepts callsigns), or when the hover equals the qth itself.
export function hoverSquareAzimuth(qth, hoverLocator) {
    const locatorRe = /^[A-R]{2}[0-9]{2}([A-X]{2})?$/;
    if (!locatorRe.test(qth) || !locatorRe.test(hoverLocator)) return '';
    if (qth === hoverLocator) return '';
    const qthBounds = locatorToBounds(qth);
    const hoverBounds = locatorToBounds(hoverLocator);
    if (!qthBounds || !hoverBounds) return '';
    const center = (b) => [(b[0][0] + b[1][0]) / 2, (b[0][1] + b[1][1]) / 2];
    const [tLat, tLng] = center(qthBounds);
    const [hLat, hLng] = center(hoverBounds);
    const bearing = Math.round(initialBearingDeg(tLat, tLng, hLat, hLng)) % 360;
    return ` ${bearing}°`;
}

// buildHoverRequestKey builds the hover-cache key. Bucketed by wall-clock time
// (30s buckets) so the cache naturally expires: spot counts change as spots
// arrive and age out, and without a time component the cached payload would be
// served forever for unchanged filter params. `now` defaults to Date.now() and
// is injectable so tests can pin the bucket.
export function buildHoverRequestKey(params, now = Date.now()) {
    const timeBucket = Math.floor(now / 30000);
    return [
        timeBucket,
        params.qth,
        params.locator,
        params.minutes,
        params.surroundings ? '1' : '0',
        params.minSnrMode,
        params.ssbMinDb,
        params.cwMinDb,
        params.selectedBand,
        params.enabledBands
    ].join('|');
}