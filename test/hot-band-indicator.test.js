// hot-band-indicator.test.js — unit tests for the hot-band pill's pure color
// helper hexToRgba (band palette → CSS rgba at the alpha ramp the pills use).
// The module has no auto-init side effect, so importing it under vitest is
// safe; initHotBandIndicator's DOM/poll wiring stays untested here (it runs
// under the browser).

import { describe, it, expect } from 'vitest';

import { hexToRgba } from '../static/hot-band-indicator.js';

describe('hexToRgba (hot-band variant)', () => {
    it('converts ramp endpoints', () => {
        expect(hexToRgba('#000000', 0.18)).toBe('rgba(0,0,0,0.18)');
        expect(hexToRgba('#ffffff', 0.85)).toBe('rgba(255,255,255,0.85)');
    });

    it('converts midpoints and the pill alpha ramp', () => {
        // The pills ramp background 0.18 → 0.28 → border 0.85 for one band color.
        expect(hexToRgba('#ff0000', 0.18)).toBe('rgba(255,0,0,0.18)');
        expect(hexToRgba('#ff0000', 0.28)).toBe('rgba(255,0,0,0.28)');
        expect(hexToRgba('#ff0000', 0.85)).toBe('rgba(255,0,0,0.85)');
        expect(hexToRgba('#7f7f7f', 0.5)).toBe('rgba(127,127,127,0.5)');
        expect(hexToRgba('#808080', 0.5)).toBe('rgba(128,128,128,0.5)');
    });

    it('accepts lowercase hex digits', () => {
        expect(hexToRgba('#00ff7f', 1)).toBe('rgba(0,255,127,1)');
    });

    it('degrades to the neutral grey fallback for malformed input', () => {
        expect(hexToRgba('ff0000', 0.18)).toBe('rgba(85,85,85,0.18)'); // no '#'
        expect(hexToRgba('#fff', 0.18)).toBe('rgba(85,85,85,0.18)'); // shorthand not supported
        expect(hexToRgba('#gg0000', 0.18)).toBe('rgba(85,85,85,0.18)'); // non-hex digits → NaN
        expect(hexToRgba('#ff00000', 0.18)).toBe('rgba(85,85,85,0.18)'); // 8 chars
        expect(hexToRgba('', 0.18)).toBe('rgba(85,85,85,0.18)');
        expect(hexToRgba(null, 0.18)).toBe('rgba(85,85,85,0.18)');
        expect(hexToRgba(undefined, 0.18)).toBe('rgba(85,85,85,0.18)');
    });
});