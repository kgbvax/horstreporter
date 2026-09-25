// map-tokens.test.js: the shared basemap tokens (--map-* in style.css) that
// the azimuthal canvas and Mercator's country overlay read, the per-theme
// cache, the colour blend used for azimuth country fills, and the contrast
// guarantees for map text.

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

import {
    MAP_TOKEN_FALLBACKS,
    COUNTRY_FILL_OPACITY,
    clearMapTokenCache,
    getMapTokens,
    mixHexColors,
    readMapTokens
} from '../static/map-tokens.js';

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(resolve(here, '../static/style.css'), 'utf8');

// Custom properties declared directly in the `selector {` rule blocks of
// style.css (a selector can have several blocks; later ones win).
function declaredProps(selector) {
    const out = {};
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const re = new RegExp(`(?:^|\\n)${escaped} \\{([^}]*)\\}`, 'g');
    let m;
    while ((m = re.exec(css)) !== null) {
        for (const d of m[1].matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) out[d[1]] = d[2].trim();
    }
    return out;
}

function hexToRgb(hex) {
    const h = hex.replace('#', '');
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
}

function luminance(rgb) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return (0.2126 * lin(rgb[0])) + (0.7152 * lin(rgb[1])) + (0.0722 * lin(rgb[2]));
}

function contrast(a, b) {
    const la = luminance(hexToRgb(a));
    const lb = luminance(hexToRgb(b));
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

function setBodyTokens(theme, props = {}) {
    document.body.setAttribute('data-theme', theme);
    document.body.removeAttribute('style');
    for (const [k, v] of Object.entries(props)) document.body.style.setProperty(k, v);
}

beforeEach(() => {
    clearMapTokenCache();
    setBodyTokens('light');
});

afterEach(() => {
    clearMapTokenCache();
    document.body.removeAttribute('style');
    document.body.removeAttribute('data-theme');
});

describe('map tokens: style.css and the JS fallbacks agree', () => {
    const cssName = {
        water: '--map-water',
        land: '--map-land',
        border: '--map-border',
        graticule: '--map-graticule',
        label: '--map-label',
        surface: '--bg-color',
        labelBg: '--dxcc-label-bg',
        labelText: '--dxcc-label-text',
        labelBorder: '--dxcc-label-border'
    };

    it('light fallbacks mirror the body rule', () => {
        const props = declaredProps('body');
        for (const [name, prop] of Object.entries(cssName)) {
            expect(props[prop], prop).toBe(MAP_TOKEN_FALLBACKS.light[name]);
        }
    });

    it('dark fallbacks mirror the body[data-theme="dark"] rule', () => {
        const props = declaredProps('body[data-theme="dark"]');
        for (const [name, prop] of Object.entries(cssName)) {
            expect(props[prop], prop).toBe(MAP_TOKEN_FALLBACKS.dark[name]);
        }
    });

    it('water and land are the sampled CARTO tile colors', () => {
        expect(MAP_TOKEN_FALLBACKS.light.water).toBe('#d4dadc');
        expect(MAP_TOKEN_FALLBACKS.light.land).toBe('#fafaf8');
        expect(MAP_TOKEN_FALLBACKS.dark.water).toBe('#262626');
        expect(MAP_TOKEN_FALLBACKS.dark.land).toBe('#090909');
    });
});

describe('map tokens: contrast of azimuth map text', () => {
    // Night shading as drawn by the azimuth grayline (azimuth-runtime.js):
    // night fill at its full 0.34 opacity over the water.
    const NIGHT = { light: '#182534', dark: '#01050a' };

    for (const theme of ['light', 'dark']) {
        it(`${theme}: bearing labels keep >= 4.5:1 on water, land, page and night-shaded water`, () => {
            const t = MAP_TOKEN_FALLBACKS[theme];
            expect(contrast(t.label, t.water)).toBeGreaterThanOrEqual(4.5);
            // --map-land is also the halo drawn behind the text.
            expect(contrast(t.label, t.land)).toBeGreaterThanOrEqual(4.5);
            expect(contrast(t.label, t.surface)).toBeGreaterThanOrEqual(4.5);
            expect(contrast(t.label, mixHexColors(t.water, NIGHT[theme], 0.34))).toBeGreaterThanOrEqual(4.5);
        });

        it(`${theme}: DXCC label text keeps >= 8:1 on its pill over black and white`, () => {
            const t = MAP_TOKEN_FALLBACKS[theme];
            const [r, g, b, a] = t.labelBg.match(/[\d.]+/g).map(Number);
            const pillHex = '#' + [r, g, b].map((v) => v.toString(16).padStart(2, '0')).join('');
            for (const under of ['#000000', '#ffffff']) {
                const pill = mixHexColors(under, pillHex, a);
                expect(contrast(t.labelText, pill)).toBeGreaterThanOrEqual(8);
            }
        });
    }
});

describe('readMapTokens / getMapTokens', () => {
    it('returns the fallbacks when the stylesheet defines nothing (jsdom)', () => {
        expect(readMapTokens('light')).toEqual({ ...MAP_TOKEN_FALLBACKS.light });
        setBodyTokens('dark');
        expect(readMapTokens('dark')).toEqual({ ...MAP_TOKEN_FALLBACKS.dark });
    });

    it('reads the custom properties from the body and trims them', () => {
        setBodyTokens('light', { '--map-water': ' #101010', '--map-label': '#202020', '--bg-color': '#fefefe' });
        const t = readMapTokens('light');
        expect(t.water).toBe('#101010');
        expect(t.label).toBe('#202020');
        expect(t.surface).toBe('#fefefe');
        // Unset properties still fall back per token.
        expect(t.land).toBe(MAP_TOKEN_FALLBACKS.light.land);
    });

    it("ignores the body's values when it shows the other theme", () => {
        setBodyTokens('light', { '--map-water': '#101010' });
        expect(readMapTokens('dark').water).toBe(MAP_TOKEN_FALLBACKS.dark.water);
        // A missing data-theme counts as light.
        document.body.removeAttribute('data-theme');
        expect(readMapTokens('light').water).toBe('#101010');
    });

    it('caches per theme until the cache is cleared', () => {
        setBodyTokens('light', { '--map-water': '#101010' });
        expect(getMapTokens('light').water).toBe('#101010');
        document.body.style.setProperty('--map-water', '#303030');
        expect(getMapTokens('light').water).toBe('#101010');
        clearMapTokenCache();
        expect(getMapTokens('light').water).toBe('#303030');
    });

    it('does not cache a read taken while the body shows the other theme', () => {
        setBodyTokens('light', { '--map-water': '#101010' });
        expect(getMapTokens('dark').water).toBe(MAP_TOKEN_FALLBACKS.dark.water);
        setBodyTokens('dark', { '--map-water': '#404040' });
        expect(getMapTokens('dark').water).toBe('#404040');
    });

    it('shares one country fill opacity per theme', () => {
        expect(COUNTRY_FILL_OPACITY).toEqual({ light: 0.30, dark: 0.42 });
    });
});

describe('mixHexColors', () => {
    it('paints the top color at alpha over the base', () => {
        expect(mixHexColors('#000000', '#ffffff', 0)).toBe('#000000');
        expect(mixHexColors('#000000', '#ffffff', 1)).toBe('#ffffff');
        expect(mixHexColors('#000000', '#ffffff', 0.5)).toBe('#808080');
        // Light country fill: palette #FEE5DA at 30% over the CARTO land.
        expect(mixHexColors('#fafaf8', '#FEE5DA', 0.3)).toBe('#fbf4ef');
    });

    it('accepts 3-digit hex and clamps alpha', () => {
        expect(mixHexColors('#000', '#fff', 2)).toBe('#ffffff');
        expect(mixHexColors('#000', '#fff', -1)).toBe('#000000');
    });

    it('returns the top color unchanged for non-hex input', () => {
        expect(mixHexColors('rgb(0, 0, 0)', '#ffffff', 0.5)).toBe('#ffffff');
        expect(mixHexColors('#000000', 'red', 0.5)).toBe('red');
    });
});
