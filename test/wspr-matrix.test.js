import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

// wspr-matrix.js imports state.js / utils.js at module load;
// utils is mocked to keep the region columns and band palette test-local.
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a095' },
    getMinSnrMode: () => document.querySelector('input[name="min-snr"]:checked')?.value || 'none',
    // Same contract as utils.getEnabledBands: checked .band-enable values.
    getEnabledBands: () => new Set(Array.from(document.querySelectorAll('.band-enable'))
        .filter((cb) => cb.checked).map((cb) => cb.value)),
}));

import { initWsprMatrix, setWsprMatrixVisible, setRowExtras, refreshMatrix, __test } from '../static/wspr-matrix.js';
import { state } from '../static/state.js';

const {
    cellInk,
    topModeBadges,
    renderCell,
    reset,
    runtime,
    toggleSource,
    clearDrillDown,
    updateDrillDownButton,
    PANEL_ID,
    BODY_ID,
    SOURCES_KEY,
} = __test;

// WCAG relative luminance + contrast ratio, for asserting that every step of
// the heat ramp keeps the white spot count readable (AA, >=4.5:1).
function luminance([r, g, b]) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function parseRgb(str) {
    const m = str.match(/^rgb\((\d+), (\d+), (\d+)\)$/);
    if (!m) throw new Error(`not an rgb() string: ${str}`);
    return [Number(m[1]), Number(m[2]), Number(m[3])];
}

describe('wspr-matrix heat styles (viridis / inferno)', () => {
    const { styleFill, rampStops, legendHtml, surgeStrength, setStyle, ringColor, RING_COLOR } = __test;
    const { VIRIDIS_STOPS, INFERNO_STOPS, VIRIDIS_LIGHT_STOPS, INFERNO_LIGHT_STOPS } = __test;
    let store;

    beforeEach(() => {
        installLocalStorageMock();
        store = globalThis.localStorage;
        setupDom();
        reset();
    });

    it('dark theme: viridis hits its reference endpoints', () => {
        expect(styleFill('viridis', 0, 'dark')).toEqual([68, 1, 84]);   // #440154
        expect(styleFill('viridis', 1, 'dark')).toEqual([253, 231, 37]); // #fde725
    });

    it('dark theme: inferno hits its reference endpoints', () => {
        expect(styleFill('inferno', 0, 'dark')).toEqual([0, 0, 4]);        // #000004
        expect(styleFill('inferno', 1, 'dark')).toEqual([252, 255, 164]);  // #fcffa4
    });

    it('light theme: both ramps start pale and end on the reference dark stop', () => {
        // Sparse = a faint tint next to the white panel, peak = the deep end
        // of the same colormap; inferno stops short of the #000004 tail.
        expect(styleFill('viridis', 0, 'light')).toEqual([254, 248, 190]); // #fef8be
        expect(styleFill('viridis', 1, 'light')).toEqual([68, 1, 84]);     // #440154
        expect(styleFill('inferno', 0, 'light')).toEqual([253, 255, 196]); // #fdffc4
        expect(styleFill('inferno', 1, 'light')).toEqual([27, 12, 65]);    // #1b0c41
    });

    it('theme defaults to body[data-theme] (light unless dark)', () => {
        expect(styleFill('viridis', 0)).toEqual(styleFill('viridis', 0, 'light'));
        document.body.setAttribute('data-theme', 'dark');
        expect(styleFill('viridis', 0)).toEqual(styleFill('viridis', 0, 'dark'));
    });

    it('visual weight tracks activity in both themes: chips brighten on dark, darken on light', () => {
        // Sequential ramps must be lightness-monotone in the direction that
        // moves AWAY from the panel: dark theme low → high gets lighter, light
        // theme low → high gets darker. A dark chip on the least active path
        // of a white panel was the bug this guards.
        for (const style of ['viridis', 'inferno']) {
            for (const theme of ['dark', 'light']) {
                let prev = luminance(styleFill(style, 0, theme));
                for (let i = 1; i <= 40; i++) {
                    const l = luminance(styleFill(style, i / 40, theme));
                    if (theme === 'dark') expect(l, `${style}/${theme} @${i / 40}`).toBeGreaterThanOrEqual(prev - 1e-9);
                    else expect(l, `${style}/${theme} @${i / 40}`).toBeLessThanOrEqual(prev + 1e-9);
                    prev = l;
                }
            }
            // Light theme: the sparse end sits close to the white page, the
            // peak carries real weight.
            expect(luminance(styleFill(style, 0, 'light'))).toBeGreaterThan(0.85);
            expect(luminance(styleFill(style, 1, 'light'))).toBeLessThan(0.05);
        }
    });

    it('rampStops picks the theme table, unknown styles fall back to viridis', () => {
        expect(rampStops('viridis', 'dark')).toBe(VIRIDIS_STOPS);
        expect(rampStops('inferno', 'dark')).toBe(INFERNO_STOPS);
        expect(rampStops('viridis', 'light')).toBe(VIRIDIS_LIGHT_STOPS);
        expect(rampStops('inferno', 'light')).toBe(INFERNO_LIGHT_STOPS);
        expect(rampStops('aqua', 'light')).toBe(VIRIDIS_LIGHT_STOPS);
        expect(rampStops('aqua', 'dark')).toBe(VIRIDIS_STOPS);
    });

    it('keeps spot-count numerals at WCAG AA across both heat ramps in both themes', () => {
        for (const style of ['viridis', 'inferno']) {
            for (const theme of ['dark', 'light']) {
                for (let i = 0; i <= 40; i++) {
                    const bg = styleFill(style, i / 40, theme);
                    const ink = parseRgb(cellInk(bg));
                    const bgL = luminance(bg), inkL = luminance(ink);
                    const ratio = (Math.max(bgL, inkL) + 0.05) / (Math.min(bgL, inkL) + 0.05);
                    expect(ratio, `${style}/${theme} intensity ${i / 40} on rgb(${bg})`).toBeGreaterThanOrEqual(4.5);
                }
            }
        }
    });

    it('clamps out-of-range intensities', () => {
        for (const theme of ['dark', 'light']) {
            expect(styleFill('viridis', -1, theme)).toEqual(styleFill('viridis', 0, theme));
            expect(styleFill('inferno', 5, theme)).toEqual(styleFill('inferno', 1, theme));
        }
    });

    it('legend scale follows the theme ramp', () => {
        runtime.style = 'inferno';
        expect(legendHtml('dark')).toContain(`linear-gradient(90deg, ${INFERNO_STOPS.join(', ')})`);
        expect(legendHtml('light')).toContain(`linear-gradient(90deg, ${INFERNO_LIGHT_STOPS.join(', ')})`);
        runtime.style = 'viridis';
        expect(legendHtml('light')).toContain(`linear-gradient(90deg, ${VIRIDIS_LIGHT_STOPS.join(', ')})`);
    });

    it('ring color: amber on the dark theme, the chip ink on the light theme', () => {
        expect(ringColor('dark', 'rgb(0, 0, 0)')).toBe(RING_COLOR);
        expect(ringColor('light', 'rgb(0, 0, 0)')).toBe('rgb(0, 0, 0)');
        expect(ringColor('light', 'rgb(255, 255, 255)')).toBe('rgb(255, 255, 255)');
    });

    it('surgeStrength: z >= 4 or >=50% source agreement is the strong mark', () => {
        expect(surgeStrength({ atypical: null })).toBe(0);
        expect(surgeStrength({ atypical: { z_score: 2.4 } })).toBe(1);
        expect(surgeStrength({ atypical: { z_score: 4.2 } })).toBe(2);
        expect(surgeStrength({ atypical: { z_score: 3.1 }, atypical_agreement: 0.5 })).toBe(2);
        expect(surgeStrength({ atypical: { z_score: 3.1 }, atypical_agreement: 0.25 })).toBe(1);
    });

    it('viridis style: atypical renders up-chevrons, never the ! badge', () => {
        runtime.style = 'viridis';
        const html = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 4.2, confidence: 0.9 },
        }), 12, 'light');
        expect(html).toContain('class="wspr-chev"');
        expect(html).toContain('<polygon points="0,4.5 4.5,0.5 9,4.5"'); // apex up
        expect((html.match(/wspr-chev/g) || []).length).toBeGreaterThanOrEqual(1);
        expect(html).not.toContain('wspr-badge-atypical');
        expect(html).not.toContain('wspr-matrix-surge');
    });

    it('inferno style (dark theme): atypical renders an amber ring, thicker when strong', () => {
        runtime.style = 'inferno';
        const mild = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 2.4, confidence: 0.5 },
        }), 12, 'dark');
        expect(mild).toContain('box-shadow: inset 0 0 0 2px #f5b83d');
        const strong = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 3.1, confidence: 0.9 },
            active_sources: ['wspr', 'pskr'], atypical_agreement: 1.0,
        }), 12, 'dark');
        expect(strong).toContain('box-shadow: inset 0 0 0 3px #f5b83d');
        expect(strong).not.toContain('wspr-badge-atypical');
    });

    it('inferno style (light theme): the surge ring takes the chip ink, never amber', () => {
        runtime.style = 'inferno';
        // Peak chip (12 of 12): deep purple, white ink → white ring.
        const peak = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 2.4, confidence: 0.5 },
        }), 12, 'light');
        expect(peak).toContain('color: rgb(255, 255, 255); box-shadow: inset 0 0 0 2px rgb(255, 255, 255)');
        // Sparse chip (1 of 400): pale cream, black ink → black ring.
        const sparse = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', spot_count: 1, atypical: { z_score: 4.2, confidence: 0.9 },
        }), 400, 'light');
        expect(sparse).toContain('color: rgb(0, 0, 0); box-shadow: inset 0 0 0 3px rgb(0, 0, 0)');
        expect(sparse).not.toContain('#f5b83d');
    });

    it('viridis chevrons take the chip ink in both themes', () => {
        runtime.style = 'viridis';
        const cell = makeCell({ band: '10m', region: 'CAR', spot_count: 1, atypical: { z_score: 2.4, confidence: 0.5 } });
        // 1 of 400 on the light theme: pale chip, black ink.
        expect(renderCell('10m', 'CAR', cell, 400, 'light')).toContain('fill="rgb(0, 0, 0)"');
        // Same cell on the dark theme: deep purple chip, white ink.
        expect(renderCell('10m', 'CAR', cell, 400, 'dark')).toContain('fill="rgb(255, 255, 255)"');
    });

    it('setStyle persists, resets the render fingerprint, rejects unknown styles', () => {
        runtime.lastRenderKey = 'stale';
        runtime.style = 'inferno'; // reset() default is viridis; start elsewhere
        setStyle('viridis');
        expect(runtime.style).toBe('viridis');
        expect(store.getItem(__test.STYLE_KEY)).toBe('viridis');
        expect(runtime.lastRenderKey).toBe('');
        setStyle('plasma');
        expect(runtime.style).toBe('viridis');
    });

    it('a stored style survives a re-init', () => {
        store.setItem(__test.STYLE_KEY, 'inferno');
        initWsprMatrix();
        expect(runtime.style).toBe('inferno');
    });
});

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
        // ssb/cw/rising — the !/!! atypical badges and the flavor variants are
        // gone (v2 merge: anomalies are glyphs/rings, v2 has no flavor field).
        expect(rules.length).toBeGreaterThanOrEqual(3);
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

// ---- Merged-panel behavior (ported from the deleted prop-matrix.test.js,
// extended for the source chips + multi-source cell contract) --------------

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => { store.set(String(key), String(value)); },
        removeItem: (key) => { store.delete(key); },
        clear: () => { store.clear(); },
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    if (typeof window !== 'undefined') {
        Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    }
    return store;
}

function setupDom() {
    document.body.innerHTML = `
        <input id="qth" value="JO32" />
        <div id="min-snr-group">
            <input type="radio" name="min-snr" value="none" checked />
            <input type="radio" name="min-snr" value="cw" />
            <input type="radio" name="min-snr" value="ssb" />
        </div>
        <input type="range" id="ssb-min-db" min="-10" max="30" value="0" />
        <input type="range" id="cw-min-db" min="-30" max="0" value="-15" />
        <button id="drill-down-clear" style="display: none;"></button>
        <div id="${PANEL_ID}" class="wspr-matrix-window is-hidden">
            <div class="wspr-matrix-window-header">
                <span id="wspr-matrix-title">Propagation from your location</span>
            </div>
            <div id="${BODY_ID}"></div>
        </div>
    `;
    document.body.removeAttribute('data-theme');
}

function mockFetch(payload, ok = true) {
    const resp = { ok, status: ok ? 200 : 500, json: async () => payload };
    global.fetch = vi.fn(async () => resp);
}

function makeCell({ band = '20m', region = 'EU', spot_count = 12, ssb_open = true, cw_open = true,
    rising = false, atypical = null, active_sources = ['wspr'], open_agreement = 1.0,
    atypical_agreement, sources } = {}) {
    // Mirrors the BACKEND v2 JSON shape (snake_case) so a naming mismatch
    // between backend and renderer surfaces here, not in prod.
    const cell = { band, region, spot_count, ssb_open, cw_open, rising, from_here: true, active_sources, open_agreement, atypical };
    if (atypical_agreement !== undefined) cell.atypical_agreement = atypical_agreement;
    if (sources !== undefined) cell.sources = sources;
    return cell;
}

describe('wspr-matrix (Propagation) panel', () => {
    let store;
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        store = globalThis.localStorage;
        setupDom();
        reset();
        state.drillDownBand = '';
        state.drillDownRegion = '';
        window.__horstScheduleRender = vi.fn();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    });

    afterEach(() => {
        reset();
        state.drillDownBand = '';
        state.drillDownRegion = '';
        delete window.__horstScheduleRender;
        global.fetch = originalFetch;
        vi.restoreAllMocks();
    });

    it('setWsprMatrixVisible shows/hides the panel and starts/stops polling', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        const panel = document.getElementById(PANEL_ID);
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(global.fetch).not.toHaveBeenCalled();

        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(panel.classList.contains('is-hidden')).toBe(false);
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(runtime.pollTimer).not.toBeNull();

        setWsprMatrixVisible(false);
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(runtime.pollTimer).toBeNull();
    });

    it('asks for a locator when none is set', async () => {
        mockFetch({ cells: [] });
        document.getElementById('qth').value = '';
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.getElementById(BODY_ID).textContent).toBe('Enter your locator to see propagation.');
        expect(global.fetch).not.toHaveBeenCalled();
    });

    it('says the data is unavailable when the first fetch fails', async () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        mockFetch({}, false);
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.getElementById(BODY_ID).textContent).toBe('Propagation data unavailable.');
    });

    it('labels the source and color scale chips as separate groups', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        const groups = Array.from(document.querySelectorAll('.wspr-src-chips [role="group"]'));
        expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Sources', 'Color scale']);
        expect(groups[0].querySelectorAll('[data-source]').length).toBe(4);
        expect(groups[1].querySelectorAll('[data-style]').length).toBe(3);
        expect(groups[0].querySelector('[data-source="rbn"]').getAttribute('aria-pressed')).toBe('true');
        expect(groups[0].querySelector('[data-source="dxcluster"]').getAttribute('aria-pressed')).toBe('false');
    });

    it('opening starts polling; closing aborts in-flight requests', async () => {
        let abortSignal;
        global.fetch = vi.fn(async (_url, opts) => {
            abortSignal = opts?.signal;
            // Hold the request open so closing mid-flight triggers abort.
            return new Promise((_resolve, reject) => {
                if (abortSignal) {
                    abortSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
                }
            });
        });

        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(runtime.pollTimer).not.toBeNull();
        expect(runtime.abortController).not.toBeNull();

        const inFlightController = runtime.abortController;
        setWsprMatrixVisible(false);
        expect(runtime.pollTimer).toBeNull();
        expect(runtime.abortController).toBeNull();
        expect(inFlightController.signal.aborted).toBe(true);
    });

    it('fetches the v2 endpoint from-here with surroundings + all sources pinned', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);
        expect(calls[0]).toContain('/api/prop_intel/v2?');
        expect(calls[0]).toContain('from_here=true');
        expect(calls[0]).toContain('surroundings=true');
        expect(calls[0]).toContain('sources=wspr%2Cpskr%2Crbn'); // dxcluster off by default
    });

    it('TTL cache: a second update within 15s does not refetch', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);

        const { updateWsprMatrix } = await import('../static/wspr-matrix.js');
        await updateWsprMatrix();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);
    });

    it('changing QTH triggers a re-poll with the new QTH', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });

        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);
        expect(calls[0]).toContain('qth=JO32');

        const qthInput = document.getElementById('qth');
        qthInput.value = 'FN31';
        qthInput.dispatchEvent(new Event('change'));
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(2);
        expect(calls[1]).toContain('qth=FN31');
    });

    it('source chips: deselect re-fetches with the remaining sources; persisted', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        toggleSource('rbn');
        await new Promise((r) => setTimeout(r, 0));
        expect(runtime.sources).toEqual(['wspr', 'pskr']);
        expect(store.getItem(SOURCES_KEY)).toBe('wspr,pskr');
        expect(calls.length).toBe(2);
        expect(calls[1]).toContain('sources=wspr%2Cpskr');
        expect(calls[1]).not.toContain('rbn');
    });

    it('default source selection excludes dxcluster (prod has no DXC ingest)', async () => {
        expect(runtime.sources).toEqual(['wspr', 'pskr', 'rbn']);
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls[0]).toContain('sources=wspr%2Cpskr%2Crbn');
        expect(calls[0]).not.toContain('dxcluster');
    });

    it('the last remaining source chip cannot be deselected', () => {
        runtime.sources = ['wspr'];
        toggleSource('wspr');
        expect(runtime.sources).toEqual(['wspr']);
    });

    it('a stored chip selection survives a re-init', () => {
        store.setItem(SOURCES_KEY, 'dxcluster,wspr');
        initWsprMatrix();
        expect(runtime.sources).toEqual(['wspr', 'dxcluster']); // canonical order
    });

    it('anomalies never render the retired !/!! badges or ×n source mark', () => {
        const html = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR',
            atypical: { z_score: 3.1, confidence: 0.9 },
            active_sources: ['wspr', 'pskr'], atypical_agreement: 1.0,
        }), 12, 'light');
        expect(html).not.toContain('wspr-badge-atypical');
        expect(html).not.toContain('wspr-matrix-src');
        expect(html).toContain('wspr-chev'); // viridis default: up-chevrons
        expect(html).not.toContain('⚡');
    });

    it('min-snr=none sends no thresholds; cw/ssb modes send the active one', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls[0]).not.toContain('cw_min_db');
        expect(calls[0]).not.toContain('ssb_min_db');

        document.querySelector('input[name="min-snr"][value="cw"]').click();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(2);
        expect(calls[1]).toContain('cw_min_db=-15');
        expect(calls[1]).not.toContain('ssb_min_db');

        document.querySelector('input[name="min-snr"][value="ssb"]').click();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(3);
        expect(calls[2]).toContain('ssb_min_db=0');
        expect(calls[2]).not.toContain('cw_min_db');
    });

    it('changing a min-snr threshold slider re-fetches with the new value', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        document.querySelector('input[name="min-snr"][value="cw"]').click();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls[1]).toContain('cw_min_db=-15');

        const cwInput = document.getElementById('cw-min-db');
        cwInput.value = '-10';
        // Native input 'change' events bubble; the listener is delegated.
        cwInput.dispatchEvent(new Event('change', { bubbles: true }));
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(3);
        expect(calls[2]).toContain('cw_min_db=-10');
    });

    it('renders empty cells as inert grid cells (no button role, no tab stop)', () => {
        for (const cell of [null, makeCell({ band: '20m', region: 'AF', spot_count: 0 })]) {
            const html = renderCell('20m', 'AF', cell, 12, 'light');
            expect(html).toContain('class="wspr-matrix-cell-empty"');
            expect(html).toContain('role="gridcell"');
            expect(html).not.toContain('role="button"');
            expect(html).not.toContain('tabindex');
            expect(html).not.toContain('aria-label');
        }
    });

    it('per-source breakdown lands in the cell tooltip', () => {
        const html = renderCell('20m', 'NA', makeCell({
            band: '20m', region: 'NA',
            active_sources: ['wspr', 'rbn'],
            sources: [
                { source: 'wspr', spot_count: 30, open: true, open_basis: 'budget' },
                { source: 'rbn', spot_count: 4, open: true, open_basis: 'snr_floor', atypical: { z_score: 3.3 } },
            ],
        }), 30, 'light');
        expect(html).toContain('WSPR: 30 spots, open (link budget)');
        expect(html).toContain('RBN: 4 spots, open (SNR floor), z=3.3');
    });

    it('omits rows for bands disabled in the band rail', async () => {
        document.body.insertAdjacentHTML('beforeend', `
            <input type="checkbox" class="band-enable" value="20m" checked />
            <input type="checkbox" class="band-enable" value="2m" />`);
        mockFetch({
            cells: [makeCell({ band: '20m', region: 'EU' }), makeCell({ band: '2m', region: 'EU' })],
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        const rows = Array.from(document.querySelectorAll('.wspr-matrix-band')).map((td) => td.textContent);
        expect(rows).toEqual(['20m']);
    });

    it('says so when every open band is disabled', async () => {
        document.body.insertAdjacentHTML('beforeend', `
            <input type="checkbox" class="band-enable" value="2m" />`);
        mockFetch({ cells: [makeCell({ band: '2m', region: 'EU' })] });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        expect(document.querySelector('.wspr-matrix-band')).toBeNull();
        expect(document.getElementById(BODY_ID).textContent).toContain('No paths open on the enabled bands.');
    });

    it('clicking a cell sets the drill-down filter and shows the clear button; clicking again clears', async () => {
        mockFetch({
            cells: [makeCell({ band: '20m', region: 'EU' })],
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        const cell = document.querySelector('.wspr-matrix-cell');
        expect(cell).not.toBeNull();
        cell.click();
        expect(state.drillDownBand).toBe('20m');
        expect(state.drillDownRegion).toBe('EU');
        const clearBtn = document.getElementById('drill-down-clear');
        expect(clearBtn.style.display).toBe('');
        expect(window.__horstScheduleRender).toHaveBeenCalled();
        // The matrix re-rendered with the cell marked as the active drill-down.
        const active = document.querySelector('.wspr-matrix-cell');
        expect(active.getAttribute('aria-selected')).toBe('true');

        // Toggle off by clicking the same cell again.
        active.click();
        expect(document.querySelector('.wspr-matrix-cell').getAttribute('aria-selected')).toBe('false');
        expect(state.drillDownBand).toBe('');
        expect(state.drillDownRegion).toBe('');
        expect(clearBtn.style.display).toBe('none');
    });

    it('clearDrillDown resets the filter and schedules a render', () => {
        state.drillDownBand = '10m';
        state.drillDownRegion = 'JA';
        updateDrillDownButton();
        clearDrillDown();
        expect(state.drillDownBand).toBe('');
        expect(state.drillDownRegion).toBe('');
        expect(window.__horstScheduleRender).toHaveBeenCalled();
        expect(document.getElementById('drill-down-clear').style.display).toBe('none');
    });
});

// ---- Keyboard grid (roving tabindex, arrow keys, activation, names) --------
describe('wspr-matrix keyboard grid', () => {
    const { cellLabel } = __test;
    const REGION_NAMES = {
        EU: 'Europe', NA: 'North America', SA: 'South America', AF: 'Africa', AS: 'Asia',
        JA: 'Japan', OC: 'Oceania', VK: 'Australia', KH6: 'Hawaii', CAR: 'Caribbean', AN: 'Antarctica',
    };
    // Sparse on purpose (columns EU NA SA AF AS JA ...):
    //   20m: EU NA .. .. .. JA
    //   15m: EU .. .. .. AS ..
    //   10m: .. NA .. .. .. ..
    const payload = (overrides = {}) => ({
        region_names: REGION_NAMES,
        cells: [
            makeCell({ band: '20m', region: 'EU', spot_count: 12 }),
            makeCell({ band: '20m', region: 'NA', spot_count: 5, ssb_open: false, cw_open: true }),
            makeCell({ band: '20m', region: 'JA', spot_count: 1234, rising: true, ...overrides.ja }),
            makeCell({ band: '15m', region: 'EU', spot_count: 4 }),
            makeCell({ band: '15m', region: 'AS', spot_count: 2 }),
            makeCell({ band: '10m', region: 'NA', spot_count: 7 }),
        ].filter(Boolean),
    });
    let originalFetch;

    const cellAt = (band, region) =>
        document.querySelector(`.wspr-matrix-cell[data-band="${band}"][data-region="${region}"]`);
    const at = (el) => `${el.getAttribute('data-band')}/${el.getAttribute('data-region')}`;
    const tabStops = () => Array.from(document.querySelectorAll('#wspr-matrix-body [tabindex="0"]'));
    const press = (key, opts = {}) => {
        const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...opts });
        document.activeElement.dispatchEvent(ev);
        return ev;
    };

    async function openWith(data) {
        mockFetch(data);
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
    }

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        state.drillDownBand = '';
        state.drillDownRegion = '';
        window.__horstScheduleRender = vi.fn();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    });

    afterEach(() => {
        reset();
        state.drillDownBand = '';
        state.drillDownRegion = '';
        delete window.__horstScheduleRender;
        global.fetch = originalFetch;
        vi.restoreAllMocks();
    });

    it('renders an ARIA grid with row and column headers', async () => {
        await openWith(payload());
        const table = document.querySelector('.wspr-matrix-table');
        expect(table.getAttribute('role')).toBe('grid');
        expect(table.getAttribute('aria-label')).toBe('Propagation by band and region');
        expect(table.querySelectorAll('thead th[role="columnheader"]').length).toBe(12);
        const rowHeaders = Array.from(table.querySelectorAll('tbody th[role="rowheader"]'));
        expect(rowHeaders.map((th) => th.textContent)).toEqual(['20m', '15m', '10m']);
        expect(table.querySelector('thead th[title="Japan"]').textContent).toBe('JA');
        expect(table.querySelectorAll('tbody td[role="gridcell"]').length).toBe(3 * 11);
    });

    it('has exactly one tab stop: the first data cell; empty cells are inert', async () => {
        await openWith(payload());
        expect(tabStops().map(at)).toEqual(['20m/EU']);
        const dataCells = document.querySelectorAll('.wspr-matrix-cell');
        expect(dataCells.length).toBe(6);
        for (const td of dataCells) {
            if (td !== cellAt('20m', 'EU')) expect(td.getAttribute('tabindex')).toBe('-1');
        }
        for (const td of document.querySelectorAll('.wspr-matrix-cell-empty')) {
            expect(td.hasAttribute('tabindex')).toBe(false);
            expect(td.getAttribute('role')).toBe('gridcell');
            td.click();
        }
        expect(state.drillDownBand).toBe('');
        expect(window.__horstScheduleRender).not.toHaveBeenCalled();
    });

    it('makes the active drill-down cell the tab stop', async () => {
        state.drillDownBand = '15m';
        state.drillDownRegion = 'AS';
        await openWith(payload());
        expect(tabStops().map(at)).toEqual(['15m/AS']);
        expect(cellAt('15m', 'AS').getAttribute('aria-selected')).toBe('true');
        expect(cellAt('20m', 'EU').getAttribute('aria-selected')).toBe('false');
    });

    it('names each data cell after its path, count and marks', async () => {
        await openWith(payload());
        expect(cellAt('20m', 'JA').getAttribute('aria-label')).toBe('20m to Japan: 1,234 spots, SSB open, rising');
        expect(cellAt('20m', 'NA').getAttribute('aria-label')).toBe('20m to North America: 5 spots, CW open');
        expect(cellAt('20m', 'JA').title.split('\n')[0]).toBe('20m to Japan: 1,234 spots');
    });

    it('cellLabel: singular count, top mode only, surge strength, code fallback', () => {
        expect(cellLabel('10m', 'CAR', makeCell({ spot_count: 1, ssb_open: false, cw_open: false })))
            .toBe('10m to CAR: 1 spot');
        expect(cellLabel('10m', 'CAR', makeCell({ ssb_open: true, cw_open: true }), REGION_NAMES))
            .toBe('10m to Caribbean: 12 spots, SSB open');
        expect(cellLabel('10m', 'CAR', makeCell({ atypical: { z_score: 2.4 } }), REGION_NAMES))
            .toBe('10m to Caribbean: 12 spots, SSB open, surge');
        expect(cellLabel('10m', 'CAR', makeCell({ rising: true, atypical: { z_score: 4.1 } }), REGION_NAMES))
            .toBe('10m to Caribbean: 12 spots, SSB open, rising, strong surge');
    });

    it('escapes server-supplied region names in names and titles', () => {
        const html = renderCell('20m', 'EU', makeCell(), 12, 'light', { EU: 'Europe "<b>"' });
        expect(html).toContain('aria-label="20m to Europe &quot;&lt;b&gt;&quot;: 12 spots, SSB open"');
        expect(html).not.toContain('<b>');
    });

    it('arrow keys move between data cells, skipping empty ones, and move the tab stop', async () => {
        await openWith(payload());
        cellAt('20m', 'EU').focus();

        press('ArrowRight');
        expect(at(document.activeElement)).toBe('20m/NA');
        press('ArrowRight'); // skips SA, AF, AS
        expect(at(document.activeElement)).toBe('20m/JA');
        const edge = press('ArrowRight'); // last data cell in the row: stays
        expect(at(document.activeElement)).toBe('20m/JA');
        expect(edge.defaultPrevented).toBe(true);
        expect(tabStops().map(at)).toEqual(['20m/JA']);

        press('ArrowLeft');
        expect(at(document.activeElement)).toBe('20m/NA');

        // Down keeps the column where a later row has a data cell there
        // (15m has no NA, 10m does).
        press('ArrowDown');
        expect(at(document.activeElement)).toBe('10m/NA');
        press('ArrowUp');
        expect(at(document.activeElement)).toBe('20m/NA');
        expect(tabStops().map(at)).toEqual(['20m/NA']);
    });

    it('up/down fall back to the closest data cell of the next row with data', async () => {
        await openWith(payload());
        cellAt('20m', 'JA').focus();
        press('ArrowDown'); // no JA below: closest in 15m is AS
        expect(at(document.activeElement)).toBe('15m/AS');
        press('ArrowDown'); // no AS below: 10m has only NA
        expect(at(document.activeElement)).toBe('10m/NA');
        press('ArrowDown'); // last row: stays
        expect(at(document.activeElement)).toBe('10m/NA');
        cellAt('15m', 'EU').focus();
        press('ArrowUp');
        expect(at(document.activeElement)).toBe('20m/EU');
        press('ArrowUp'); // first row: stays
        expect(at(document.activeElement)).toBe('20m/EU');
    });

    it('Home/End jump within the row, Ctrl+Home/End within the grid', async () => {
        await openWith(payload());
        cellAt('20m', 'NA').focus();
        press('End');
        expect(at(document.activeElement)).toBe('20m/JA');
        press('Home');
        expect(at(document.activeElement)).toBe('20m/EU');
        press('End', { ctrlKey: true });
        expect(at(document.activeElement)).toBe('10m/NA');
        press('Home', { ctrlKey: true });
        expect(at(document.activeElement)).toBe('20m/EU');
    });

    it('ignores other keys', async () => {
        await openWith(payload());
        cellAt('20m', 'EU').focus();
        const ev = press('a');
        expect(ev.defaultPrevented).toBe(false);
        expect(at(document.activeElement)).toBe('20m/EU');
    });

    it('Enter and Space toggle the drill-down and keep focus on the cell', async () => {
        await openWith(payload());
        cellAt('20m', 'EU').focus();
        press('ArrowRight');
        press('ArrowRight');

        const enter = press('Enter');
        expect(enter.defaultPrevented).toBe(true);
        expect(state.drillDownBand).toBe('20m');
        expect(state.drillDownRegion).toBe('JA');
        expect(window.__horstScheduleRender).toHaveBeenCalledTimes(1);
        expect(document.getElementById('drill-down-clear').style.display).toBe('');
        // Re-rendered: new element, same path, selected, focused, tab stop.
        expect(at(document.activeElement)).toBe('20m/JA');
        expect(document.activeElement.getAttribute('aria-selected')).toBe('true');
        expect(tabStops().map(at)).toEqual(['20m/JA']);

        const space = press(' ');
        expect(space.defaultPrevented).toBe(true);
        expect(state.drillDownBand).toBe('');
        expect(state.drillDownRegion).toBe('');
        expect(at(document.activeElement)).toBe('20m/JA');
        expect(document.activeElement.getAttribute('aria-selected')).toBe('false');
        expect(document.getElementById('drill-down-clear').style.display).toBe('none');
    });

    it('clearing the filter from the map chip deselects the matrix cell', async () => {
        await openWith(payload());
        cellAt('15m', 'EU').click();
        expect(cellAt('15m', 'EU').getAttribute('aria-selected')).toBe('true');
        clearDrillDown();
        expect(cellAt('15m', 'EU').getAttribute('aria-selected')).toBe('false');
        expect(tabStops().map(at)).toEqual(['20m/EU']);
    });

    it('a periodic re-render puts focus back on the same band/region cell', async () => {
        await openWith(payload());
        cellAt('15m', 'AS').focus();
        const before = document.activeElement;

        // Next poll: counts changed, so the markup is rebuilt.
        mockFetch(payload({ ja: { spot_count: 1300 } }));
        runtime.lastFetchedAt = 0;
        const { updateWsprMatrix } = await import('../static/wspr-matrix.js');
        await updateWsprMatrix();
        await new Promise((r) => setTimeout(r, 0));

        expect(cellAt('20m', 'JA').getAttribute('aria-label')).toContain('1,300 spots');
        expect(document.activeElement).not.toBe(before);
        expect(at(document.activeElement)).toBe('15m/AS');
        expect(tabStops().map(at)).toEqual(['15m/AS']);
    });

    it('falls back to the grid tab stop when the focused cell is gone', async () => {
        await openWith(payload());
        cellAt('15m', 'AS').focus();

        const next = payload();
        next.cells = next.cells.filter((c) => !(c.band === '15m' && c.region === 'AS'));
        mockFetch(next);
        runtime.lastFetchedAt = 0;
        const { updateWsprMatrix } = await import('../static/wspr-matrix.js');
        await updateWsprMatrix();
        await new Promise((r) => setTimeout(r, 0));

        expect(cellAt('15m', 'AS')).toBeNull();
        expect(at(document.activeElement)).toBe('20m/EU');
        expect(tabStops().map(at)).toEqual(['20m/EU']);
    });

    it('does not take focus when it was outside the matrix', async () => {
        await openWith(payload());
        const qth = document.getElementById('qth');
        qth.focus();
        mockFetch(payload({ ja: { spot_count: 1300 } }));
        runtime.lastFetchedAt = 0;
        const { updateWsprMatrix } = await import('../static/wspr-matrix.js');
        await updateWsprMatrix();
        await new Promise((r) => setTimeout(r, 0));
        expect(cellAt('20m', 'JA').getAttribute('aria-label')).toContain('1,300 spots');
        expect(document.activeElement).toBe(qth);
    });

    it('keeps focus on a source chip across the re-fetch it triggers', async () => {
        await openWith(payload());
        const chip = document.querySelector('.wspr-src-chip[data-source="rbn"]');
        chip.focus();
        chip.click();
        await new Promise((r) => setTimeout(r, 0));
        const now = document.activeElement;
        expect(now).not.toBe(chip);
        expect(now.getAttribute('data-source')).toBe('rbn');
        expect(now.getAttribute('aria-pressed')).toBe('false');
    });

    it('keeps focus on a color scale chip when the look changes', async () => {
        await openWith(payload());
        const chip = document.querySelector('.wspr-src-chip[data-style="inferno"]');
        chip.focus();
        chip.click();
        expect(runtime.style).toBe('inferno');
        expect(document.activeElement.getAttribute('data-style')).toBe('inferno');
        expect(document.activeElement.getAttribute('aria-pressed')).toBe('true');
    });

    it('skips the rebuild when nothing changed, rebuilds when the drill-down did', async () => {
        await openWith(payload());
        const first = cellAt('20m', 'EU');
        const { updateWsprMatrix } = await import('../static/wspr-matrix.js');
        await updateWsprMatrix(); // cache hit, same key: DOM untouched
        expect(cellAt('20m', 'EU')).toBe(first);
        state.drillDownBand = '20m';
        state.drillDownRegion = 'EU';
        await updateWsprMatrix();
        expect(cellAt('20m', 'EU')).not.toBe(first);
        expect(cellAt('20m', 'EU').getAttribute('aria-selected')).toBe('true');
    });
});

describe('wspr-matrix row extras (Conditions dock)', () => {
    let after;
    const extras = () => ({
        columns: [{ label: 'Activity', className: 'x-head' }],
        cells: (band) => `<td class="x-cell" data-band="${band}">v-${band}</td>`,
        rowHeader: (band) => `<span class="x-line">h-${band}</span>`,
        key: (bands) => bands.join(','),
        hiddenRegions: ['AN'],
        after,
    });

    beforeEach(() => {
        installLocalStorageMock();
        setupDom();
        document.body.insertAdjacentHTML('beforeend',
            '<input type="checkbox" class="band-enable" value="20m" checked><input type="checkbox" class="band-enable" value="40m" checked>');
        reset();
        after = vi.fn();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    });

    afterEach(() => {
        reset();
        vi.restoreAllMocks();
    });

    it('adds a row per enabled band, also without a path', async () => {
        mockFetch({ cells: [makeCell({ band: '20m', region: 'EU' })], region_names: {} });
        initWsprMatrix();
        setRowExtras(extras());
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));

        const body = document.getElementById(BODY_ID);
        expect(document.getElementById(PANEL_ID).classList.contains('is-hidden')).toBe(false);
        // 40m has no path but is enabled: it still gets a row with its extras.
        const rows = Array.from(body.querySelectorAll('tbody tr')).map((tr) => tr.querySelector('th').firstChild.textContent);
        expect(rows).toEqual(['40m', '20m']);
        expect(body.querySelectorAll('.x-cell')).toHaveLength(2);
        expect(body.querySelector('thead .x-head').textContent).toBe('Activity');
        // Hidden regions get neither a heading nor a cell.
        const heads = Array.from(body.querySelectorAll('thead th')).map((th) => th.textContent);
        expect(heads).toContain('EU');
        expect(heads).not.toContain('AN');
        expect(body.querySelectorAll('tbody tr:first-child td.wspr-matrix-cell-empty, tbody tr:first-child td.wspr-matrix-cell')).toHaveLength(10);
        // The header lines stack inside the row header, under the band name.
        expect(body.querySelector('tbody tr th').textContent).toBe('40mh-40m');
        expect(after).toHaveBeenCalledWith(body);
    });

    it('renders the rows even when no path is open', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        setRowExtras(extras());
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.querySelectorAll('#wspr-matrix-body tbody tr')).toHaveLength(2);
    });

    it('hideCounts leaves the number out of the cell but keeps it in the tooltip and name', async () => {
        mockFetch({ cells: [makeCell({ band: '20m', region: 'EU', spot_count: 1234 })], region_names: {} });
        initWsprMatrix();
        setRowExtras({ ...extras(), hideCounts: true });
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        const cell = document.querySelector('#wspr-matrix-body .wspr-matrix-cell');
        expect(cell.textContent).not.toContain('1234');
        expect(cell.querySelector('.wspr-badge')).not.toBeNull();
        expect(cell.getAttribute('title')).toContain('1,234 spots');
        expect(cell.getAttribute('aria-label')).toContain('1,234 spots');
        // Without the option the count is shown.
        setRowExtras(extras());
        refreshMatrix();
        expect(document.querySelector('#wspr-matrix-body .wspr-matrix-cell').textContent).toContain('1234');
    });

    it('rebuilds only when the extras key changes', async () => {
        mockFetch({ cells: [makeCell({ band: '20m', region: 'EU' })] });
        initWsprMatrix();
        let tag = 'a';
        const e = extras();
        e.key = () => tag;
        setRowExtras(e);
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        const first = document.querySelector('#wspr-matrix-body table');
        refreshMatrix();
        expect(document.querySelector('#wspr-matrix-body table')).toBe(first);
        tag = 'b';
        refreshMatrix();
        expect(document.querySelector('#wspr-matrix-body table')).not.toBe(first);
    });

    it('still draws the rows when the propagation fetch fails', async () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        mockFetch({}, false);
        initWsprMatrix();
        setRowExtras(extras());
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.querySelectorAll('#wspr-matrix-body tbody tr')).toHaveLength(2);
        expect(document.getElementById(BODY_ID).textContent).not.toContain('unavailable');
    });

    it('refreshMatrix draws the rows before any payload, but not without a locator', () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        setRowExtras(extras());
        runtime.enabled = true;
        document.getElementById('qth').value = '';
        refreshMatrix();
        expect(document.querySelectorAll('#wspr-matrix-body tbody tr')).toHaveLength(0);
        document.getElementById('qth').value = 'JO32';
        refreshMatrix();
        expect(document.querySelectorAll('#wspr-matrix-body tbody tr')).toHaveLength(2);
    });
});

describe('wspr-matrix disc look', () => {
    const REGION_NAMES = { EU: 'Europe', NA: 'North America', JA: 'Japan', SA: 'South America' };
    let originalFetch;

    const cellAt = (band, region) =>
        document.querySelector(`#wspr-matrix-body td[data-band="${band}"][data-region="${region}"]`);
    const circles = (band, region) => Array.from(cellAt(band, region).querySelectorAll('circle'));
    // The ring is drawn as a page-coloured halo plus a text-coloured line.
    const ringOf = (cs) => cs.filter((c) => c.getAttribute('stroke') === 'var(--text-color)' && c.getAttribute('fill') === 'none');
    const discOf = (cs) => cs.find((c) => c.getAttribute('stroke') !== 'var(--bg-color)' && !ringOf([c]).length);

    async function openWith(data) {
        mockFetch(data);
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
    }

    const withNormal = (cell, expected, expectedSpots) => ({ ...cell, expected, expected_spots: expectedSpots });
    const silent = (band, region, expected) => ({
        band, region, spot_count: 0, silent: true, expected, expected_spots: 0, from_here: true,
        ssb_open: false, cw_open: false, rising: false, active_sources: [], open_agreement: 0, sources: [],
    });
    const payload = () => ({
        region_names: REGION_NAMES,
        cells: [
            withNormal(makeCell({ band: '20m', region: 'EU', spot_count: 130 }), 50, 100),        // ×2: livelier
            withNormal(makeCell({ band: '20m', region: 'NA', spot_count: 20, ssb_open: false, cw_open: true }), 400, 20), // ×0.05: quieter
            makeCell({ band: '20m', region: 'JA', spot_count: 3, ssb_open: false, cw_open: false }), // no normal
            withNormal(makeCell({ band: '20m', region: 'SA', spot_count: 6 }), 80, 0),            // normal, but only WSPR now
        ],
    });

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        state.drillDownBand = '';
        state.drillDownRegion = '';
        window.__horstScheduleRender = vi.fn();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    });

    afterEach(() => {
        reset();
        delete window.__horstScheduleRender;
        global.fetch = originalFetch;
    });

    it('discRadiusForRatio: the ring at x1, area proportional to the share, clamped', () => {
        const { discRadiusForRatio } = __test;
        expect(discRadiusForRatio(1)).toBeCloseTo(8.5, 9);
        // Area doubles with the ratio: radius by sqrt(2).
        expect(discRadiusForRatio(2) / discRadiusForRatio(1)).toBeCloseTo(Math.SQRT2, 9);
        expect(discRadiusForRatio(0.25)).toBeCloseTo(4.25, 9);
        expect(discRadiusForRatio(4)).toBeCloseTo(17, 9);
        expect(discRadiusForRatio(400)).toBe(discRadiusForRatio(4));
        expect(discRadiusForRatio(0.001)).toBe(2.5);
        expect(discRadiusForRatio(0)).toBe(0);
        expect(discRadiusForRatio(NaN)).toBe(0);
        // Halving the reports moves the edge by more than two units of 40,
        // the defect of the log-volume version was well under one.
        expect(discRadiusForRatio(1) - discRadiusForRatio(0.5)).toBeGreaterThan(2);
    });

    it('normalRatio / drawRatio / hasNormal / formatRatio', () => {
        const { normalRatio, drawRatio, hasNormal, formatRatio } = __test;
        // The drawing pads both counts by two reports: stray reports on an
        // empty path stay near normal, busy cells are unchanged.
        expect(drawRatio({ expected: 0.3, expected_spots: 1 })).toBeCloseTo(3 / 2.3, 9);
        expect(drawRatio({ expected: 4515, expected_spots: 771 })).toBeCloseTo(0.171, 3);
        expect(drawRatio({ expected: 0.5, expected_spots: 11 })).toBeGreaterThan(4); // a real opening still fills the cell
        expect(normalRatio({ expected: 50, expected_spots: 100 })).toBe(2);
        expect(normalRatio({ expected: 0, expected_spots: 3 })).toBe(3); // unseen path: a normal under one counts as one
        expect(hasNormal({ expected: 0, expected_spots: 0 })).toBe(true);
        expect(hasNormal({ expected: 10 })).toBe(false);
        expect(hasNormal({})).toBe(false);
        expect(formatRatio(0.172)).toBe('×0.17');
        expect(formatRatio(1.46)).toBe('×1.5');
        expect(formatRatio(12.4)).toBe('×12');
        expect(formatRatio(-1)).toBe('');
    });

    it('discInk: the band colour, blended toward white on the dark theme', () => {
        const { discInk } = __test;
        expect(discInk('20m', 'light')).toBe('rgb(230, 126, 34)');
        expect(discInk('20m', 'dark')).toBe('rgb(239, 171, 111)');
        expect(discInk('nonsense', 'light')).toBe('rgb(85, 85, 85)'); // falls back to "all"
    });

    it('formatExpected: whole numbers, "<1" below one', () => {
        const { formatExpected } = __test;
        expect(formatExpected(640.4)).toBe('640');
        expect(formatExpected(1234.5)).toBe('1,235');
        expect(formatExpected(0.4)).toBe('<1');
        expect(formatExpected(-1)).toBe('');
        expect(formatExpected('x')).toBe('');
    });

    it('always asks the backend for silent cells', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(calls[0]).toContain('silent=1');
        expect(calls[0]).toContain('from_here=true');
    });

    it('offers Disc next to the colour looks and persists the choice', async () => {
        await openWith(payload());
        const chip = document.querySelector('.wspr-src-chip[data-style="disc"]');
        expect(chip).not.toBeNull();
        chip.click();
        expect(runtime.style).toBe('disc');
        expect(globalThis.localStorage.getItem(__test.STYLE_KEY)).toBe('disc');
    });

    it('draws each cell as an SVG, with no heat fill and no numeral; still a drillable data cell', async () => {
        runtime.style = 'disc';
        await openWith(payload());
        const eu = cellAt('20m', 'EU');
        expect(eu.className).toContain('wspr-disc-cell');
        expect(eu.getAttribute('style')).toBeNull();
        expect(eu.querySelector('svg.wspr-disc')).not.toBeNull();
        expect(eu.textContent.trim()).toBe('');
        expect(eu.hasAttribute('tabindex')).toBe(true);
    });

    it('livelier than normal: the disc spills outside the fixed ring; quieter: it sits inside', async () => {
        runtime.style = 'disc';
        await openWith(payload());
        const eu = circles('20m', 'EU');
        const na = circles('20m', 'NA');
        const euRing = Number(ringOf(eu)[0].getAttribute('r'));
        const naRing = Number(ringOf(na)[0].getAttribute('r'));
        expect(euRing).toBe(8.5);
        expect(naRing).toBe(euRing); // the same ring in every cell
        expect(Number(discOf(eu).getAttribute('r'))).toBeCloseTo(8.5 * Math.sqrt(102 / 52), 1); // (100+2)/(50+2)
        expect(Number(discOf(na).getAttribute('r'))).toBeLessThan(naRing / 2);
        // Mode styles still apply: SSB filled, CW only hollow.
        expect(discOf(eu).getAttribute('fill-opacity')).toBe('0.8');
        expect(discOf(na).getAttribute('fill')).toBe('none');
    });

    it('reports with nothing to compare are a small dashed dot; without a normal there is no ring', async () => {
        runtime.style = 'disc';
        await openWith(payload());
        const ja = circles('20m', 'JA');
        expect(ringOf(ja)).toHaveLength(0);
        expect(ja).toHaveLength(1);
        expect(ja[0].getAttribute('r')).toBe('3.5');
        expect(ja[0].getAttribute('stroke-dasharray')).not.toBeNull();
        // A normal exists but no PSKReporter reports now (only WSPR): ring + dashed dot.
        const sa = circles('20m', 'SA');
        expect(ringOf(sa)).toHaveLength(1);
        expect(discOf(sa).getAttribute('r')).toBe('3.5');
        expect(discOf(sa).getAttribute('stroke-dasharray')).not.toBeNull();
    });

    it('labels and titles carry the share of normal', async () => {
        runtime.style = 'disc';
        await openWith(payload());
        const eu = cellAt('20m', 'EU');
        expect(eu.getAttribute('aria-label')).toContain('×2.0 normal');
        expect(eu.getAttribute('title')).toContain('PSKReporter: 100 now, normal about 50 at this hour (×2.0)');
        expect(cellAt('20m', 'JA').getAttribute('aria-label')).not.toContain('normal');
        const unseen = __test.renderCell('20m', 'EU', withNormal(makeCell({ spot_count: 4 }), 0, 4), 10, 'light', REGION_NAMES);
        expect(unseen).toContain('not normally open at this hour');
        expect(unseen).toContain('normally under 1 at this hour');
    });

    it('surge adds orange outlines around the disc, two when strong; rising adds a triangle', () => {
        const { discSvg } = __test;
        const base = withNormal({ spot_count: 50, ssb_open: true, cw_open: true }, 20, 40);
        const count = (cell, theme) => (discSvg('20m', cell, theme).match(/stroke="#(c2410c|f5b83d)"/g) || []).length;
        expect(count(base, 'light')).toBe(0);
        expect(count({ ...base, atypical: { z_score: 2.5, confidence: 0.9 } }, 'light')).toBe(1);
        expect(count({ ...base, atypical: { z_score: 4.5, confidence: 0.9 } }, 'light')).toBe(2);
        expect(discSvg('20m', { ...base, atypical: { z_score: 2.5 } }, 'dark')).toContain('#f5b83d');
        expect(discSvg('20m', { ...base, atypical: { z_score: 2.5 } }, 'light')).toContain('#c2410c');
        expect(discSvg('20m', { ...base, rising: true }, 'light')).toContain('<path');
        expect(discSvg('20m', base, 'light')).not.toContain('<path');
    });

    it('never draws the heat looks\' chevrons, rings or badges in disc mode', () => {
        runtime.style = 'disc';
        const html = renderCell('20m', 'EU', withNormal(
            makeCell({ band: '20m', region: 'EU', atypical: { z_score: 4.2, confidence: 0.9 }, rising: true }), 10, 12,
        ), 100, 'light', REGION_NAMES);
        expect(html).not.toContain('wspr-chev');
        expect(html).not.toContain('box-shadow');
        expect(html).not.toContain('wspr-badge');
    });

    it('a silent cell is the ring alone: inert, named, and outside the tab order', async () => {
        runtime.style = 'disc';
        await openWith({ region_names: REGION_NAMES, cells: [
            withNormal(makeCell({ band: '20m', region: 'EU', spot_count: 12 }), 10, 12),
            silent('20m', 'JA', 640),
        ] });
        const ja = cellAt('20m', 'JA');
        expect(ja.className).toContain('wspr-matrix-cell-empty');
        expect(ja.className).toContain('wspr-disc-silent');
        expect(ja.hasAttribute('tabindex')).toBe(false);
        expect(ja.getAttribute('aria-label')).toBe('20m to Japan: none now, usually about 640 PSKReporter reports at this hour');
        const cs = circles('20m', 'JA');
        expect(cs).toHaveLength(2); // page-coloured halo + the ring line, no disc
        for (const c of cs) expect(c.getAttribute('fill')).toBe('none');
        expect(Number(ringOf(cs)[0].getAttribute('r'))).toBe(8.5);
        ja.click();
        expect(state.drillDownBand).toBe('');
    });

    it('the colour looks drop silent cells, so they never add rows or cells', async () => {
        runtime.style = 'viridis';
        await openWith({ region_names: REGION_NAMES, cells: [silent('20m', 'JA', 640)] });
        expect(document.querySelector('#wspr-matrix-body .wspr-disc-silent')).toBeNull();
        expect(document.getElementById(BODY_ID).textContent).toContain('No paths open');
    });

    it('switching looks repaints from the cache without a refetch', async () => {
        await openWith(payload());
        const before = global.fetch.mock.calls.length;
        document.querySelector('.wspr-src-chip[data-style="disc"]').click();
        expect(cellAt('20m', 'EU').querySelector('svg.wspr-disc')).not.toBeNull();
        document.querySelector('.wspr-src-chip[data-style="viridis"]').click();
        expect(cellAt('20m', 'EU').querySelector('svg')).toBeNull();
        expect(global.fetch.mock.calls.length).toBe(before);
    });

    it('legend explains ring, disc, dot and the marks', () => {
        runtime.style = 'disc';
        const html = __test.legendHtml('light');
        expect(html).toContain('wspr-disc-key');
        expect(html).toContain('ring = normal for this hour from your area');
        expect(html).toContain('ring alone = usually open, nothing now');
        expect(html).toContain('dashed dot = reports but no normal to compare');
        expect(html).toContain('orange outline = surge');
        expect(html).not.toContain('wspr-heat-scale');
    });
});
