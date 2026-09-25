import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

// wspr-matrix.js imports state.js / utils.js / panel-drag.js at module load;
// utils is mocked to keep the region columns and band palette test-local.
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a095' },
    getMinSnrMode: () => document.querySelector('input[name="min-snr"]:checked')?.value || 'none',
    // Same contract as utils.getEnabledBands: checked .band-enable values.
    getEnabledBands: () => new Set(Array.from(document.querySelectorAll('.band-enable'))
        .filter((cb) => cb.checked).map((cb) => cb.value)),
}));

import { initWsprMatrix, __test } from '../static/wspr-matrix.js';
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
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
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
    const { styleFill, surgeStrength, setStyle } = __test;
    let store;

    beforeEach(() => {
        installLocalStorageMock();
        store = globalThis.localStorage;
        setupDom();
        reset();
    });

    it('viridis hits its reference endpoints and ignores the theme', () => {
        expect(styleFill('viridis', 0)).toEqual([68, 1, 84]);   // #440154
        expect(styleFill('viridis', 1)).toEqual([253, 231, 37]); // #fde725
    });

    it('inferno hits its reference endpoints', () => {
        expect(styleFill('inferno', 0)).toEqual([0, 0, 4]);        // #000004
        expect(styleFill('inferno', 1)).toEqual([252, 255, 164]);  // #fcffa4
    });

    it('keeps spot-count numerals at WCAG AA across both heat ramps', () => {
        for (const style of ['viridis', 'inferno']) {
            for (let i = 0; i <= 40; i++) {
                const bg = styleFill(style, i / 40);
                const ink = parseRgb(cellInk(bg));
                const bgL = luminance(bg), inkL = luminance(ink);
                const ratio = (Math.max(bgL, inkL) + 0.05) / (Math.min(bgL, inkL) + 0.05);
                expect(ratio, `${style} intensity ${i / 40} on rgb(${bg})`).toBeGreaterThanOrEqual(4.5);
            }
        }
    });

    it('clamps out-of-range intensities', () => {
        expect(styleFill('viridis', -1)).toEqual(styleFill('viridis', 0));
        expect(styleFill('inferno', 5)).toEqual(styleFill('inferno', 1));
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

    it('inferno style: atypical renders an amber ring, thicker when strong', () => {
        runtime.style = 'inferno';
        const mild = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 2.4, confidence: 0.5 },
        }), 12, 'light');
        expect(mild).toContain('box-shadow: inset 0 0 0 2px #f5b83d');
        const strong = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR', atypical: { z_score: 3.1, confidence: 0.9 },
            active_sources: ['wspr', 'pskr'], atypical_agreement: 1.0,
        }), 12, 'light');
        expect(strong).toContain('box-shadow: inset 0 0 0 3px #f5b83d');
        expect(strong).not.toContain('wspr-badge-atypical');
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
        <button id="${TOGGLE_ID}"></button>
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

    it('toggle button shows/hides the panel and persists state to localStorage', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();

        const panel = document.getElementById(PANEL_ID);
        const toggle = document.getElementById(TOGGLE_ID);
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe(null);

        expect(toggle.getAttribute('aria-pressed')).toBe('false');
        expect(toggle.title).toBe('Show propagation');

        toggle.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(panel.classList.contains('is-hidden')).toBe(false);
        expect(store.getItem(ENABLE_KEY)).toBe('true');
        expect(toggle.classList.contains('is-active')).toBe(true);
        expect(toggle.getAttribute('aria-pressed')).toBe('true');
        expect(toggle.title).toBe('Hide propagation');
        // The toggle stays visible while the panel is open.
        expect(toggle.style.display).toBe('');

        toggle.click();
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe('false');
        expect(toggle.classList.contains('is-active')).toBe(false);
        expect(toggle.getAttribute('aria-pressed')).toBe('false');
        expect(toggle.title).toBe('Show propagation');
    });

    it('a stored open state restores the pressed toggle', () => {
        mockFetch({ cells: [] });
        store.setItem(ENABLE_KEY, 'true');
        initWsprMatrix();
        const toggle = document.getElementById(TOGGLE_ID);
        expect(toggle.getAttribute('aria-pressed')).toBe('true');
        expect(toggle.title).toBe('Hide propagation');
    });

    it('asks for a locator when none is set', async () => {
        mockFetch({ cells: [] });
        document.getElementById('qth').value = '';
        initWsprMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));
        expect(document.getElementById(BODY_ID).textContent).toBe('Enter your locator to see propagation.');
        expect(global.fetch).not.toHaveBeenCalled();
    });

    it('says the data is unavailable when the first fetch fails', async () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        mockFetch({}, false);
        initWsprMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));
        expect(document.getElementById(BODY_ID).textContent).toBe('Propagation data unavailable.');
    });

    it('labels the source and color scale chips as separate groups', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));
        const groups = Array.from(document.querySelectorAll('.wspr-src-chips [role="group"]'));
        expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Sources', 'Color scale']);
        expect(groups[0].querySelectorAll('[data-source]').length).toBe(4);
        expect(groups[1].querySelectorAll('[data-style]').length).toBe(2);
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
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(runtime.pollTimer).not.toBeNull();
        expect(runtime.abortController).not.toBeNull();

        const inFlightController = runtime.abortController;
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));

        const rows = Array.from(document.querySelectorAll('.wspr-matrix-band')).map((td) => td.textContent);
        expect(rows).toEqual(['20m']);
    });

    it('says so when every open band is disabled', async () => {
        document.body.insertAdjacentHTML('beforeend', `
            <input type="checkbox" class="band-enable" value="2m" />`);
        mockFetch({ cells: [makeCell({ band: '2m', region: 'EU' })] });
        initWsprMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));

        expect(document.querySelector('.wspr-matrix-band')).toBeNull();
        expect(document.getElementById(BODY_ID).textContent).toContain('No paths open on the enabled bands.');
    });

    it('clicking a cell sets the drill-down filter and shows the clear button; clicking again clears', async () => {
        mockFetch({
            cells: [makeCell({ band: '20m', region: 'EU' })],
        });
        initWsprMatrix();
        document.getElementById(TOGGLE_ID).click();
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
        document.getElementById(TOGGLE_ID).click();
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

describe('wspr-matrix toggle row placement', () => {
    afterEach(() => {
        reset();
        delete globalThis.ResizeObserver;
    });

    it('publishes the toggle row bottom edge for the mobile panel placement', () => {
        installLocalStorageMock();
        document.body.innerHTML = `
            <div id="map-stack">
                <div id="map-toggles"><button id="${TOGGLE_ID}"></button></div>
                <div id="${PANEL_ID}" class="wspr-matrix-window is-hidden">
                    <div class="wspr-matrix-window-header"></div>
                    <div id="${BODY_ID}"></div>
                </div>
            </div>`;
        const observed = [];
        let callback = null;
        globalThis.ResizeObserver = class {
            constructor(cb) { callback = cb; }
            observe(el) { observed.push(el); }
            disconnect() {}
        };
        const row = document.getElementById('map-toggles');
        Object.defineProperty(row, 'offsetTop', { configurable: true, value: 12 });
        Object.defineProperty(row, 'offsetHeight', { configurable: true, value: 31 });
        initWsprMatrix();
        const stack = document.getElementById('map-stack');
        expect(observed).toEqual([row]);
        expect(stack.style.getPropertyValue('--map-toggles-bottom')).toBe('43px');

        // The row wraps to a second line.
        Object.defineProperty(row, 'offsetHeight', { configurable: true, value: 68 });
        callback();
        expect(stack.style.getPropertyValue('--map-toggles-bottom')).toBe('80px');
    });
});
