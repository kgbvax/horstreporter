// hot-band-indicator.test.js — unit tests for the hot-band pill's pure color
// helper hexToRgba (band palette → CSS rgba at the alpha ramp the pills use).
// The module has no auto-init side effect, so importing it under vitest is
// safe. The pill copy tests drive initHotBandIndicator against a mocked
// /api/hot_bands response.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import { hexToRgba, initHotBandIndicator } from '../static/hot-band-indicator.js';

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
describe('initHotBandIndicator pill copy', () => {
    const REC = {
        band: '20m',
        kind: 'dx_surge',
        reason: 'DX surge',
        priority: 'high',
        spots_per_minute: 3.5,
        activity_level: 'above',
        activity_ratio: 1.8,
        p90_distance_km: 5200,
        baseline_p90_distance_km: 3100,
    };
    let ctl = null;

    beforeEach(() => {
        document.body.innerHTML = `
            <input type="checkbox" class="band-enable" value="20m" checked>
            <div id="hot-band-indicator" class="is-hidden"></div>
        `;
        globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ recommendations: [REC] }) }));
    });

    afterEach(() => {
        ctl?.stop();
        ctl = null;
    });

    async function renderPill() {
        ctl = initHotBandIndicator({ getQth: () => 'JO32', getCurrentBand: () => 'all' });
        await vi.waitFor(() => {
            expect(document.querySelector('.hot-band-pill')).not.toBeNull();
        });
        return document.querySelector('.hot-band-pill');
    }

    it('renders band and reason as separate elements without a separator character', async () => {
        const pill = await renderPill();
        expect(pill.querySelector('.hot-band-name').textContent).toBe('20m');
        expect(pill.querySelector('.hot-band-reason').textContent).toBe('DX surge');
        expect(pill.textContent).not.toContain('·');
        expect(pill.getAttribute('aria-label')).toBe('Switch to 20m: DX surge');
    });

    it('writes the tooltip in sentence case with a colon instead of a middle dot', async () => {
        const pill = await renderPill();
        expect(pill.title.split('\n')).toEqual([
            '20m: DX surge',
            'Rate: 3.50/min (1.8× normal)',
            'P90 distance: 5200 km vs baseline 3100 km',
        ]);
    });
});
