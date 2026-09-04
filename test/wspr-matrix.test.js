import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

// wspr-matrix.js imports state.js / utils.js / panel-drag.js at module load;
// utils is mocked to keep the region columns and band palette test-local.
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a095' },
}));

import { initWsprMatrix, __test } from '../static/wspr-matrix.js';
import { state } from '../static/state.js';

const {
    cellColor,
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
        // Base + ssb/cw/rising/atypical/atypical-multi (flavor variants went
        // away with the v2 merge — v2 has no flavor field).
        expect(rules.length).toBeGreaterThanOrEqual(5);
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
        <button id="${TOGGLE_ID}"></button>
        <button id="drill-down-clear" style="display: none;"></button>
        <div id="${PANEL_ID}" class="wspr-matrix-window is-hidden">
            <div class="wspr-matrix-window-header">
                <span>Propagation Intel</span>
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

describe('wspr-matrix (Prop) panel', () => {
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

        toggle.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(panel.classList.contains('is-hidden')).toBe(false);
        expect(store.getItem(ENABLE_KEY)).toBe('true');
        expect(toggle.classList.contains('is-active')).toBe(true);

        toggle.click();
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe('false');
        expect(toggle.classList.contains('is-active')).toBe(false);
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
        expect(calls[0]).toContain('sources=wspr%2Cpskr%2Crbn%2Cdxcluster');
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
        expect(runtime.sources).toEqual(['wspr', 'pskr', 'dxcluster']);
        expect(store.getItem(SOURCES_KEY)).toBe('wspr,pskr,dxcluster');
        expect(calls.length).toBe(2);
        expect(calls[1]).toContain('sources=wspr%2Cpskr%2Cdxcluster');
        expect(calls[1]).not.toContain('rbn');
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

    it('atypical with multi-source agreement renders !! + the multi badge class', () => {
        const html = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR',
            atypical: { z_score: 3.1, confidence: 0.9 },
            active_sources: ['wspr', 'pskr'], atypical_agreement: 1.0,
        }), 12, 'light');
        expect(html).toContain('wspr-badge-atypical-multi');
        expect(html).toContain('>!!</span>');
        expect(html).toContain('wspr-matrix-surge');
        expect(html).toContain('×2');
        // No emoji in the UI — marks are text glyphs.
        expect(html).not.toContain('⚡');
    });

    it('atypical without agreement renders the plain ! badge', () => {
        const html = renderCell('10m', 'CAR', makeCell({
            band: '10m', region: 'CAR',
            atypical: { z_score: 2.4, confidence: 0.5 },
            active_sources: ['wspr'],
        }), 12, 'light');
        expect(html).toContain('wspr-badge-atypical');
        expect(html).not.toContain('wspr-badge-atypical-multi');
        expect(html).toContain('>!</span>');
    });

    it('×n mark fades when sources disagree on open', () => {
        const agree = renderCell('20m', 'EU', makeCell({ active_sources: ['wspr', 'pskr'], open_agreement: 1.0 }), 12, 'light');
        expect(agree).toContain('wspr-matrix-src');
        expect(agree).not.toContain('is-mixed');
        const mixed = renderCell('20m', 'EU', makeCell({
            active_sources: ['wspr', 'pskr'], open_agreement: 0.5,
        }), 12, 'light');
        expect(mixed).toContain('is-mixed');
    });

    it('renders an empty cell as a clickable drill-down target', () => {
        const html = renderCell('20m', 'AF', null, 12, 'light');
        expect(html).toContain('class="wspr-matrix-cell-empty"');
        expect(html).toContain('data-band="20m"');
        expect(html).toContain('data-region="AF"');
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
        expect(html).toContain('wspr: 30 spots');
        expect(html).toContain('rbn: 4 spots');
        expect(html).toContain('z=3.3');
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

        // Toggle off by clicking the same cell again.
        cell.click();
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
