// Shared basemap tokens: one map look for both projections.
//
// Mercator shows CARTO tiles (light_all / dark_all); the azimuthal canvas has
// no tiles, so it paints water, land, borders, graticule and labels from the
// --map-* custom properties in style.css, whose values are sampled from those
// tiles. Mercator's country overlay and the Leaflet container background read
// the same tokens, so switching projection keeps one basemap.
//
// Canvas code can't use var(), so the values are read once per theme via
// getComputedStyle(document.body) and cached. Reads only trust the computed
// values when <body data-theme> matches the requested theme (otherwise the
// fallbacks below are returned uncached), so a read that races a theme switch
// can't cache the other theme's colors.

// Mirrors style.css; used before the stylesheet applies and in tests (jsdom
// has no stylesheet).
export const MAP_TOKEN_FALLBACKS = Object.freeze({
    light: Object.freeze({
        water: '#d4dadc',
        land: '#fafaf8',
        border: 'rgba(88, 99, 109, 0.5)',
        graticule: '#2b333b',
        label: '#2b333b',
        surface: '#ffffff',
        labelBg: 'rgba(255, 255, 255, 0.84)',
        labelText: '#263745',
        labelBorder: 'rgba(70, 86, 98, 0.30)'
    }),
    dark: Object.freeze({
        water: '#262626',
        land: '#090909',
        border: 'rgba(140, 150, 160, 0.4)',
        graticule: '#d8dde2',
        label: '#e1e5e9',
        surface: '#212529',
        labelBg: 'rgba(18, 28, 38, 0.84)',
        labelText: '#f3f6fb',
        labelBorder: 'rgba(216, 226, 236, 0.28)'
    })
});

// Token name -> CSS custom property. `surface` is the page surface (the area
// around the azimuth disc); the label* trio is the DXCC label pill that both
// projections draw.
const TOKEN_PROPERTIES = Object.freeze({
    water: '--map-water',
    land: '--map-land',
    border: '--map-border',
    graticule: '--map-graticule',
    label: '--map-label',
    surface: '--bg-color',
    labelBg: '--dxcc-label-bg',
    labelText: '--dxcc-label-text',
    labelBorder: '--dxcc-label-border'
});

// Country-coloring fill opacity over --map-land, shared by the Mercator
// overlay (drawn over the CARTO land) and the azimuth canvas (pre-blended
// with --map-land), so a country reads the same tint in both projections.
export const COUNTRY_FILL_OPACITY = Object.freeze({ light: 0.30, dark: 0.42 });

const tokenCache = new Map();

function normalizeTheme(theme) {
    return theme === 'dark' ? 'dark' : 'light';
}

function bodyTheme() {
    if (typeof document === 'undefined' || !document.body) return null;
    return normalizeTheme(document.body.getAttribute('data-theme'));
}

// readMapTokens reads the tokens for `theme` from the live stylesheet, falling
// back per token to MAP_TOKEN_FALLBACKS when a property is missing/empty or
// the body currently shows the other theme. Uncached; see getMapTokens.
export function readMapTokens(theme) {
    const key = normalizeTheme(theme);
    const fallback = MAP_TOKEN_FALLBACKS[key];
    let style = null;
    if (bodyTheme() === key && typeof getComputedStyle === 'function') {
        try {
            style = getComputedStyle(document.body);
        } catch (_) {
            style = null;
        }
    }
    const out = {};
    for (const [name, property] of Object.entries(TOKEN_PROPERTIES)) {
        const value = style ? String(style.getPropertyValue(property) || '').trim() : '';
        out[name] = value || fallback[name];
    }
    return out;
}

// getMapTokens returns the cached tokens for `theme`, reading them on first
// use. Only reads taken while the body shows that theme are cached.
export function getMapTokens(theme) {
    const key = normalizeTheme(theme);
    const cached = tokenCache.get(key);
    if (cached) return cached;
    const tokens = readMapTokens(key);
    if (bodyTheme() === key) tokenCache.set(key, tokens);
    return tokens;
}

// clearMapTokenCache drops the cached tokens so the next getMapTokens call
// re-reads the stylesheet (called on theme change).
export function clearMapTokenCache() {
    tokenCache.clear();
}

function parseHexColor(color) {
    const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(String(color || '').trim());
    if (!m) return null;
    const h = m[1].length === 3 ? m[1].split('').map((c) => c + c).join('') : m[1];
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
}

// mixHexColors returns `top` painted at `alpha` over `base` as an opaque
// #rrggbb (what a translucent fill over that base looks like). Non-hex input
// returns `top` unchanged.
export function mixHexColors(base, top, alpha) {
    const b = parseHexColor(base);
    const t = parseHexColor(top);
    if (!b || !t) return top;
    const a = Math.max(0, Math.min(1, Number(alpha) || 0));
    return '#' + b.map((v, i) => Math.round(v + ((t[i] - v) * a)).toString(16).padStart(2, '0')).join('');
}
