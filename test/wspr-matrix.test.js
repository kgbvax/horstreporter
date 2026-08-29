import { describe, expect, it, vi } from 'vitest';

// wspr-matrix.js imports state.js / utils.js / panel-drag.js at module load;
// utils is mocked to keep the region columns and band palette test-local
// (same convention as prop-matrix.test.js).
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a095' },
}));

import { __test } from '../static/wspr-matrix.js';

const { cellColor, topModeBadges } = __test;

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
    it('hits the ramp endpoints at 0 and 1', () => {
        expect(cellColor(0)).toBe('rgb(10, 61, 71)');
        expect(cellColor(1)).toBe('rgb(17, 121, 138)');
    });

    it('clamps out-of-range intensities', () => {
        expect(cellColor(-2)).toBe(cellColor(0));
        expect(cellColor(5)).toBe(cellColor(1));
    });

    it('keeps white text at WCAG AA across the whole ramp', () => {
        for (let i = 0; i <= 20; i++) {
            const intensity = i / 20;
            const ratio = contrastWithWhite(parseRgb(cellColor(intensity)));
            expect(ratio, `intensity ${intensity}`).toBeGreaterThanOrEqual(4.5);
        }
    });

    it('brightens monotonically with activity', () => {
        let prev = -1;
        for (let i = 0; i <= 20; i++) {
            const l = luminance(parseRgb(cellColor(i / 20)));
            expect(l).toBeGreaterThan(prev);
            prev = l;
        }
    });
});