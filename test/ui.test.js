// ui.test.js — unit tests for ui.js's pure helpers (extracted into
// ui-helpers.js so they're testable without ui.js's map.js import and
// init/control wiring): HTML escaping, the hovered grid-square highlight
// style, the hover tooltip's azimuth line, and the hover-cache request key.
// The init/control wiring in ui.js itself stays untested here (it runs under
// the browser).

import { describe, it, expect } from 'vitest';

import {
    escapeHtml,
    getHoverSquareStyle,
    hoverSquareAzimuth,
    buildHoverRequestKey,
} from '../static/ui-helpers.js';

describe('escapeHtml', () => {
    it('escapes all five HTML metacharacters', () => {
        expect(escapeHtml('<a href="x">&\'')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;&#39;');
        expect(escapeHtml('<script>alert(1)</script>')).toBe('&lt;script&gt;alert(1)&lt;/script&gt;');
    });

    it('passes safe text through unchanged', () => {
        expect(escapeHtml('JO62')).toBe('JO62');
        expect(escapeHtml('')).toBe('');
        expect(escapeHtml(null)).toBe('');
        expect(escapeHtml(undefined)).toBe('');
    });
});

describe('getHoverSquareStyle', () => {
    it('returns the tuned dark-theme style', () => {
        expect(getHoverSquareStyle('dark')).toEqual({
            color: '#ffe39a',
            weight: 2,
            opacity: 1,
            fillColor: '#ffe39a',
            fillOpacity: 0.20,
            interactive: false,
        });
    });

    it('returns the tuned light-theme style', () => {
        expect(getHoverSquareStyle('light')).toEqual({
            color: '#cc9a1f',
            weight: 2,
            opacity: 0.95,
            fillColor: '#ffd166',
            fillOpacity: 0.14,
            interactive: false,
        });
    });

    it('treats anything but "dark" as light', () => {
        expect(getHoverSquareStyle('')).toEqual(getHoverSquareStyle('light'));
        expect(getHoverSquareStyle(undefined)).toEqual(getHoverSquareStyle('light'));
    });
});

describe('hoverSquareAzimuth', () => {
    // JO62 bounds [[52,12],[53,14]] (center 52.5°N 13°E); JO63 is the square
    // directly north, JO72 directly east (center 52.5°N 15°E).
    it('returns the bearing from the qth square center to the hovered square center', () => {
        expect(hoverSquareAzimuth('JO62', 'JO63')).toBe(' 0°'); // adjacent north
        expect(hoverSquareAzimuth('JO62', 'JO72')).toBe(' 89°'); // adjacent east (spherical, not exactly 90)
        expect(hoverSquareAzimuth('JO72', 'JO62')).toBe(' 271°'); // reciprocal
        expect(hoverSquareAzimuth('JO62QM', 'JO63QN')).toBe(' 0°'); // 6-char locators
    });

    it('is empty when the hover equals the qth', () => {
        expect(hoverSquareAzimuth('JO62', 'JO62')).toBe('');
    });

    it('is empty for invalid qth or hover values (the qth field accepts callsigns)', () => {
        expect(hoverSquareAzimuth('DX0AB', 'JO63')).toBe(''); // callsign, not a locator
        expect(hoverSquareAzimuth('JO62', 'ZZ99')).toBe(''); // field letters outside A-R
        expect(hoverSquareAzimuth('JO62', 'jo72')).toBe(''); // lowercase fails the pattern
        expect(hoverSquareAzimuth('JO62', '')).toBe('');
        expect(hoverSquareAzimuth('', 'JO63')).toBe('');
    });
});

describe('buildHoverRequestKey', () => {
    const params = {
        qth: 'JO62',
        locator: 'JO63',
        minutes: 15,
        surroundings: true,
        minSnrMode: 'cw',
        ssbMinDb: 0,
        cwMinDb: -15,
        selectedBand: '20m',
        enabledBands: '17m,20m',
    };

    it('joins the time bucket and every filter param', () => {
        // 45s into the epoch → 30s bucket 1.
        expect(buildHoverRequestKey(params, 45000)).toBe('1|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
    });

    it('buckets by wall-clock time in 30s steps', () => {
        expect(buildHoverRequestKey(params, 0)).toBe('0|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
        expect(buildHoverRequestKey(params, 29999)).toBe('0|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
        expect(buildHoverRequestKey(params, 30000)).toBe('1|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
        expect(buildHoverRequestKey(params, 900000)).toBe('30|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
        expect(buildHoverRequestKey(params, -1)).toBe('-1|JO62|JO63|15|1|cw|0|-15|20m|17m,20m');
    });

    it('renders falsy/NaN params as empty or "NaN" fields (current behavior)', () => {
        const bare = { qth: 'JO62', locator: 'JO63', minutes: 15, surroundings: false, minSnrMode: '', ssbMinDb: NaN, cwMinDb: NaN, selectedBand: '', enabledBands: '' };
        expect(buildHoverRequestKey(bare, 45000)).toBe('1|JO62|JO63|15|0||NaN|NaN||');
    });

    it('keys differing params differently within one bucket', () => {
        expect(buildHoverRequestKey(params, 45000))
            .not.toBe(buildHoverRequestKey({ ...params, surroundings: false }, 45000));
    });
});