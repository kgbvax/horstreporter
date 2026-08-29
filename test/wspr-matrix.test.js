import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

// wspr-matrix.js imports state.js / utils.js / panel-drag.js at module load;
// utils is mocked to keep the region columns and band palette test-local
// (same convention as prop-matrix.test.js).
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a095' },
}));

import { __test } from '../static/wspr-matrix.js';

const { cellColor, cellInk, topModeBadges } = __test;

// WCAG relative luminance + contrast ratio, for asserting that every step of
// the heat ramp keeps the white spot count readable (AA, >=4.5:1).
function luminance([r, g, b]) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function contrastWithWhite(rgb) {
    return (1.0 + 0.05) / (luminance(rgb) + 0.05);
}

function parseRgb(str) {
    const m = str.match(/^rgb\((\d+), (\d+), (\d+)\)$/);
    if (!m) throw new Error(`not an rgb() string: ${str}`);
    return [Number(m[1]), Number(m[2]), Number(m[3])];
}

describe('wspr-matrix topModeBadges', () => {
    it('shows only SSB when both SSB and CW are open', () => {
        expect(topModeBadges({ ssb_open: true, cw_open: true }))
            .toBe('<span class="wspr-badge wspr-badge-ssb">S</span>');
    });

    it('shows CW when SSB is closed', () => {
        expect(topModeBadges({ ssb_open: false, cw_open: true }))
            .toBe('<span class="wspr-badge wspr-badge-cw">C</span>');
    });

    it('shows no mode badge when neither mode is open', () => {
        expect(topModeBadges({ ssb_open: false, cw_open: false })).toBe('');
    });
});

describe('wspr-matrix cellColor heat ramp', () => {
    // Panel background approximations: --bg-color composites to near-white in
    // the light theme and near-black in the dark theme.
    const PANEL_LUMINANCE = { light: 1.0, dark: 0.022 };

    it('hits the ramp endpoints at 0 and 1 per theme', () => {
        expect(cellColor(0, 'light')).toBe('rgb(159, 217, 226)');
        expect(cellColor(1, 'light')).toBe('rgb(8, 55, 67)');
        expect(cellColor(0, 'dark')).toBe('rgb(13, 71, 83)');
        expect(cellColor(1, 'dark')).toBe('rgb(127, 220, 234)');
    });

    it('clamps out-of-range intensities', () => {
        expect(cellColor(-2, 'light')).toBe(cellColor(0, 'light'));
        expect(cellColor(5, 'dark')).toBe(cellColor(1, 'dark'));
    });

    it('defaults to the light theme (backward-compatible call sites)', () => {
        expect(cellColor(0)).toBe(cellColor(0, 'light'));
    });

    it('moves chips AWAY from the panel as activity rises, in both themes', () => {
        for (const theme of ['light', 'dark']) {
            let prevDist = -1;
            for (let i = 0; i <= 20; i++) {
                const l = luminance(parseRgb(cellColor(i / 20, theme)));
                const dist = Math.abs(l - PANEL_LUMINANCE[theme]);
                expect(dist, `${theme} intensity ${i}`).toBeGreaterThan(prevDist);
                prevDist = dist;
            }
        }
    });

    it('spans at least 40 L* so the gradient is actually perceivable', () => {
        for (const theme of ['light', 'dark']) {
            const lo = luminance(parseRgb(cellColor(0, theme)));
            const hi = luminance(parseRgb(cellColor(1, theme)));
            // L* ≈ 116 * sqrt(luminance) − 16; assert the span, not the exact L*.
            expect(Math.abs(hi - lo), theme).toBeGreaterThanOrEqual(0.32);
        }
    });

    it('keeps numerals at WCAG AA with the selected ink across both ramps', () => {
        for (const theme of ['light', 'dark']) {
            for (let i = 0; i <= 40; i++) {
                const bg = parseRgb(cellColor(i / 40, theme));
                const ink = parseRgb(cellInk(bg));
                const bgL = luminance(bg), inkL = luminance(ink);
                const ratio = (Math.max(bgL, inkL) + 0.05) / (Math.min(bgL, inkL) + 0.05);
                expect(ratio, `${theme} intensity ${i / 40} on rgb(${bg})`).toBeGreaterThanOrEqual(4.5);
            }
        }
    });
});

// The .wspr-badge-* flag chips sit ON the teal heat cells, so each bg/fg pair
// in style.css must hold WCAG AA on its own (0.65rem bold = normal-size text).
// Guards the dark-ink badge fix (white on green/teal/orange was 2.6-3.1:1).
describe('wspr-matrix badge contrast (style.css)', () => {
    const css = readFileSync('static/style.css', 'utf8'); // vitest runs from the repo root
    const section = css.slice(css.indexOf('/* WSPR matrix flag badges'), css.indexOf('.wspr-matrix-legend'));
    const rules = [...section.matchAll(/\.wspr-badge[\w-]*\s*\{[^}]*\}/g)]
        .map((m) => m[0])
        .filter((rule) => !rule.includes('inline-block')); // skip the sizing-only base rule

    const parseColor = (rule, prop) => {
        const m = rule.match(new RegExp(`${prop}:\\s*([^;]+)`));
        if (!m) return null;
        const v = m[1].trim();
        if (v.startsWith('#')) {
            const h = v.slice(1);
            if (h.length === 3) return [0, 1, 2].map((i) => parseInt(h[i] + h[i], 16));
            return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
        }
        return null; // var() etc. — not asserted here
    };

    it('covers every badge variant', () => {
        expect(rules.length).toBeGreaterThanOrEqual(8);
    });

    it('keeps badge text at WCAG AA against its own background', () => {
        for (const rule of rules) {
            const bg = parseColor(rule, 'background');
            const fg = parseColor(rule, 'color');
            expect(bg, rule).not.toBeNull();
            expect(fg, rule).not.toBeNull();
            const fgL = luminance(fg);
            const ratio = (Math.max(fgL, luminance(bg)) + 0.05) / (Math.min(fgL, luminance(bg)) + 0.05);
            expect(ratio, rule).toBeGreaterThanOrEqual(4.5);
        }
    });
});