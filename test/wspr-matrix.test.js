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
    renderLookCell,
    reset,
    runtime,
    toggleSource,
    clearDrillDown,
    updateDrillDownButton,
    PANEL_ID,
    BODY_ID,
    SOURCES_KEY,
    STYLE_KEY,
} = __test;

// Payload envelope for renderLookCell: 22:30 UTC, 15-minute window.
const LOOK_DATA = { now: Date.UTC(2026, 9, 4, 22, 30) / 1000, minutes: 15, trend_bin_minutes: 5 };

describe('wspr-matrix looks (dots / day / trend)', () => {
    const { setStyle, STYLES, DEFAULT_STYLE } = __test;
    let store;

    beforeEach(() => {
        installLocalStorageMock();
        store = globalThis.localStorage;
        setupDom();
        reset();
    });

    it('offers the three looks, dot strip first and default', () => {
        expect(STYLES.map((s) => s.key)).toEqual(['dots', 'day', 'trend']);
        expect(DEFAULT_STYLE).toBe('dots');
        expect(runtime.style).toBe('dots');
    });

    it('setStyle persists, resets the render fingerprint, rejects unknown looks', () => {
        runtime.lastRenderKey = 'x';
        setStyle('day');
        expect(runtime.style).toBe('day');
        expect(store.getItem(STYLE_KEY)).toBe('day');
        expect(runtime.lastRenderKey).toBe('');
        setStyle('viridis');
        expect(runtime.style).toBe('day');
    });

    it('the top-left corner anchors time for the looks that draw it', () => {
        const { timeAnchor } = __test;
        runtime.style = 'trend';
        expect(timeAnchor({ trend_bin_minutes: 5, cells: [{ trend: new Array(12).fill(0) }] })).toContain('>last 1 h<');
        expect(timeAnchor({ trend_bin_minutes: 5, cells: [{ trend: new Array(6).fill(0) }] })).toContain('>last 30 min<');
        expect(timeAnchor(null)).toContain('>last 1 h<'); // before the first payload
        expect(timeAnchor({})).toContain('aria-hidden="true"');
        runtime.style = 'day';
        expect(timeAnchor({})).toContain('>00\u201324 UTC<');
        runtime.style = 'dots';
        expect(timeAnchor({})).toBe('');
    });

    it('a stored look survives a re-init; a retired one falls back to the default', () => {
        store.setItem(STYLE_KEY, 'trend');
        initWsprMatrix();
        expect(runtime.style).toBe('trend');
        reset();
        store.setItem(STYLE_KEY, 'inferno');
        initWsprMatrix();
        expect(runtime.style).toBe('dots');
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

    it('labels the source and look chips as separate groups', async () => {
        mockFetch({ cells: [] });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        const groups = Array.from(document.querySelectorAll('.wspr-src-chips [role="group"]'));
        expect(groups.map((g) => g.getAttribute('aria-label'))).toEqual(['Sources', 'Look', 'Overlay']);
        expect(groups[2].querySelector('[data-toggle="heat"]').getAttribute('aria-pressed')).toBe('false');
        expect(groups[0].querySelectorAll('[data-source]').length).toBe(4);
        expect(Array.from(groups[1].querySelectorAll('[data-style]')).map((c) => c.textContent)).toEqual(['Dots', 'Day', 'Trend']);
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
        // What the looks draw: silent cells, the usual day, the last hour.
        expect(calls[0]).toContain('silent=1');
        expect(calls[0]).toContain('normal_day=1');
        expect(calls[0]).toContain('trend=1');
    });

    it('switching looks repaints from cache without a refetch', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return { ok: true, status: 200, json: async () => ({ cells: [makeCell({ band: '20m', region: 'EU' })] }) };
        });
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.querySelector('.wspr-strip-dot')).not.toBeNull();
        document.querySelector('.wspr-src-chip[data-style="trend"]').click();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);
        expect(document.querySelector('.wspr-strip-dot')).toBeNull();
        expect(document.querySelector('.wspr-look-cell svg')).not.toBeNull();
        expect(document.querySelector('.wspr-matrix-table').classList.contains('wspr-look-trend')).toBe(true);
    });

    it('the Heat chip tints cells by reports now, persists and needs no refetch', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return {
                ok: true, status: 200, json: async () => ({
                    cells: [
                        makeCell({ band: '20m', region: 'EU', spot_count: 1000 }),
                        makeCell({ band: '20m', region: 'NA', spot_count: 1 }),
                        { band: '20m', region: 'JA', spot_count: 0, silent: true, expected: 30, expected_spots: 0, sources: [], active_sources: [] },
                    ],
                }),
            };
        });
        runtime.style = 'day';
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        expect(document.querySelector('.has-heat')).toBeNull();

        const chip = document.querySelector('.wspr-src-chip[data-toggle="heat"]');
        chip.focus();
        chip.click();
        expect(calls.length).toBe(1);
        expect(store.getItem(__test.HEAT_KEY)).toBe('1');
        expect(document.activeElement.getAttribute('data-toggle')).toBe('heat');
        expect(document.activeElement.getAttribute('aria-pressed')).toBe('true');
        const eu = document.querySelector('.wspr-look-cell[data-region="EU"]');
        const na = document.querySelector('.wspr-look-cell[data-region="NA"]');
        expect(eu.classList.contains('has-heat')).toBe(true);
        expect(eu.style.getPropertyValue('--heat')).toBe('rgb(253, 48, 0)'); // busiest: red end
        expect(na.style.getPropertyValue('--heat')).toBe('rgb(0, 52, 245)'); // one report: blue end
        expect(document.querySelector('.wspr-look-cell[data-region="JA"]').classList.contains('has-heat')).toBe(false);
        expect(document.querySelector('.wspr-heat-key').textContent).toContain('1 to 1.0k');

        // The strip colours its dots the same way.
        document.querySelector('.wspr-src-chip[data-style="dots"]').click();
        expect(document.querySelectorAll('.wspr-strip-dot.has-heat')).toHaveLength(2);

        // Off again; a re-init restores the stored choice.
        document.querySelector('.wspr-src-chip[data-toggle="heat"]').click();
        expect(document.querySelector('.has-heat')).toBeNull();
        expect(store.getItem(__test.HEAT_KEY)).toBe('0');
        store.setItem(__test.HEAT_KEY, '1');
        initWsprMatrix();
        expect(runtime.heat).toBe(true);
    });

    it('refetches once, a few seconds later, while the normal is still loading', async () => {
        vi.useFakeTimers();
        try {
            const calls = [];
            let payload = { now: 1, cells: [makeCell({ band: '20m', region: 'EU' })] };
            global.fetch = vi.fn(async (url) => {
                calls.push(url);
                return { ok: true, status: 200, json: async () => payload };
            });
            initWsprMatrix();
            setWsprMatrixVisible(true);
            await vi.advanceTimersByTimeAsync(0);
            expect(calls.length).toBe(1);
            expect(document.querySelector('.wspr-look-note').textContent).toBe('The normal for your area is loading.');

            await vi.advanceTimersByTimeAsync(__test.DETAIL_RETRY_MS);
            expect(calls.length).toBe(2);
            // Still missing: no second retry for the same request.
            await vi.advanceTimersByTimeAsync(__test.DETAIL_RETRY_MS * 2);
            expect(calls.length).toBe(2);

            // Once everything is there no retry is scheduled.
            payload = { now: 2, cells: [{ ...makeCell({ band: '20m', region: 'EU' }), expected: 5, expected_spots: 6, normal_day: new Array(48).fill(1), trend: new Array(12).fill(0) }] };
            runtime.detailRetryKey = '';
            __test.invalidateCache();
            toggleSource('rbn');
            await vi.advanceTimersByTimeAsync(0);
            const n = calls.length;
            expect(document.querySelector('.wspr-look-note')).toBeNull();
            await vi.advanceTimersByTimeAsync(__test.DETAIL_RETRY_MS);
            expect(calls.length).toBe(n);
        } finally {
            vi.useRealTimers();
        }
    });

    it('asks to select PSKR when it is off, and does not retry', () => {
        runtime.sources = ['wspr', 'rbn'];
        __test.scheduleDetailRetry({ cells: [makeCell()] }, 'k');
        expect(runtime.detailRetryTimer).toBeNull();
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

    it('a surge is drawn as a ring in the look, named in the cell, never a badge', () => {
        runtime.style = 'day';
        const html = renderLookCell('10m', 'CAR', {
            ...makeCell({
                band: '10m', region: 'CAR',
                atypical: { z_score: 3.1, confidence: 0.9 },
                active_sources: ['wspr', 'pskr'], atypical_agreement: 1.0,
            }),
            expected: 2, expected_spots: 12, normal_day: new Array(48).fill(4),
        }, LOOK_DATA, {});
        expect(html).not.toContain('wspr-badge');
        expect(html).not.toContain('⚡');
        expect(html).toContain('strong surge');
        expect((html.match(/<circle/g) || []).length).toBe(2); // dot + surge ring
    });

    it('a silent cell is drawn and named as usually open, silent now', () => {
        runtime.style = 'day';
        const silent = { band: '15m', region: 'JA', spot_count: 0, silent: true, expected: 12.4, expected_spots: 0, normal_day: new Array(48).fill(20), sources: [], active_sources: [] };
        const html = renderLookCell('15m', 'JA', silent, LOOK_DATA, { JA: 'Japan' });
        expect(html).toContain('class="wspr-matrix-cell wspr-look-cell is-silent"');
        expect(html).toContain('aria-label="15m to Japan: no reports now, usually about 12 at this hour"');
        expect(html).toContain('stroke-dasharray="1.4 1.2"');
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
            const html = renderLookCell('20m', 'AF', cell, LOOK_DATA, {});
            expect(html).toContain('class="wspr-matrix-cell-empty"');
            expect(html).toContain('role="gridcell"');
            expect(html).not.toContain('role="button"');
            expect(html).not.toContain('tabindex');
            expect(html).not.toContain('aria-label');
        }
    });

    it('per-source breakdown lands in the cell tooltip', () => {
        runtime.style = 'trend';
        const html = renderLookCell('20m', 'NA', makeCell({
            band: '20m', region: 'NA',
            active_sources: ['wspr', 'rbn'],
            sources: [
                { source: 'wspr', spot_count: 30, open: true, open_basis: 'budget' },
                { source: 'rbn', spot_count: 4, open: true, open_basis: 'snr_floor', atypical: { z_score: 3.3 } },
            ],
        }), LOOK_DATA, {});
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

    // The day look keeps one <td> per region; the dot strip's keyboard model
    // has its own tests below.
    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        runtime.style = 'day';
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
        const html = renderLookCell('20m', 'EU', makeCell(), LOOK_DATA, { EU: 'Europe "<b>"' });
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

    it('keeps focus on a look chip when the look changes', async () => {
        await openWith(payload());
        const chip = document.querySelector('.wspr-src-chip[data-style="trend"]');
        chip.focus();
        chip.click();
        expect(runtime.style).toBe('trend');
        expect(document.activeElement.getAttribute('data-style')).toBe('trend');
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
        runtime.style = 'day';
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

    it('the dot strip puts one strip cell per row after the extras', async () => {
        runtime.style = 'dots';
        mockFetch({ cells: [makeCell({ band: '20m', region: 'EU' }), makeCell({ band: '20m', region: 'AN' })], region_names: {} });
        initWsprMatrix();
        setRowExtras(extras());
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
        const body = document.getElementById(BODY_ID);
        const row = Array.from(body.querySelectorAll('tbody tr')).find((tr) => tr.querySelector('th').firstChild.textContent === '20m');
        expect(Array.from(row.children).map((c) => c.className)).toEqual(['wspr-matrix-band', 'x-cell', 'wspr-strip-td']);
        // Hidden regions get no dot.
        expect(Array.from(row.querySelectorAll('.wspr-strip-dot')).map((d) => d.dataset.region)).toEqual(['EU']);
        expect(body.querySelector('thead .wspr-strip-head .wspr-strip-ticks')).not.toBeNull();
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

describe('wspr-matrix from-here normal in the tooltip', () => {
    beforeEach(() => {
        installLocalStorageMock();
        setupDom();
        reset();
    });

    it('expectedLine spells out now, normal and the factor', () => {
        const { expectedLine, hasNormal } = __test;
        expect(hasNormal({ expected: 4515.3, expected_spots: 771 })).toBe(true);
        expect(hasNormal({ expected: 10 })).toBe(false);
        expect(hasNormal({})).toBe(false);
        expect(expectedLine({ expected: 4515.3, expected_spots: 771 })).toBe('PSKReporter: 771 now, normal about 4,515 at this hour (\u00d70.17)');
        expect(expectedLine({ expected: 40, expected_spots: 60 })).toBe('PSKReporter: 60 now, normal about 40 at this hour (\u00d71.5)');
        expect(expectedLine({ expected: 0.4, expected_spots: 11 })).toBe('PSKReporter: 11 now, normally under 1 at this hour');
    });

    it('the cell title carries the normal only when the backend sent one', () => {
        const { cellTitle } = __test;
        const withNormal = cellTitle('20m', 'NA', { ...makeCell({ band: '20m', region: 'NA', spot_count: 800 }), expected: 4515.3, expected_spots: 771 });
        expect(withNormal).toContain('normal about 4,515 at this hour');
        const without = cellTitle('20m', 'NA', makeCell({ band: '20m', region: 'NA', spot_count: 800 }));
        expect(without).not.toContain('PSKReporter:');
        expect(cellTitle('20m', 'NA', { ...makeCell({ spot_count: 0 }), silent: true, expected: 40, expected_spots: 0 }, { NA: 'North America' }))
            .toBe('20m to North America: no reports now, usually about 40 at this hour');
    });
});

describe('wspr-matrix dot strip keyboard grid', () => {
    let originalFetch;
    const at = (el) => `${el.getAttribute('data-band')}/${el.getAttribute('data-region')}`;
    const dot = (band, region) =>
        document.querySelector(`.wspr-strip-dot[data-band="${band}"][data-region="${region}"]`);
    const press = (key, opts = {}) => {
        const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...opts });
        document.activeElement.dispatchEvent(ev);
        return ev;
    };
    const withNormal = (band, region, es, e, extra = {}) => ({
        ...makeCell({ band, region, spot_count: es + 3, ...extra }), expected: e, expected_spots: es,
    });
    // 20m: JA ×⅛ (silent), NA ×½, EU ×1, SA ×4    15m: AS ×2, EU ×1
    const payload = () => ({
        cells: [
            withNormal('20m', 'EU', 9, 9),
            withNormal('20m', 'NA', 4, 9),
            withNormal('20m', 'SA', 39, 9),
            { band: '20m', region: 'JA', spot_count: 0, silent: true, expected: 30, expected_spots: 0, sources: [], active_sources: [] },
            withNormal('15m', 'EU', 9, 9),
            withNormal('15m', 'AS', 19, 9, { atypical: { z_score: 4.5 } }),
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
        state.drillDownBand = '';
        state.drillDownRegion = '';
        delete window.__horstScheduleRender;
        global.fetch = originalFetch;
        vi.restoreAllMocks();
    });

    async function open() {
        mockFetch(payload());
        initWsprMatrix();
        setWsprMatrixVisible(true);
        await new Promise((r) => setTimeout(r, 0));
    }

    it('one axis column, dots in x order with their state classes', async () => {
        await open();
        const heads = document.querySelectorAll('thead th[role="columnheader"]');
        expect(heads.length).toBe(2);
        expect(heads[1].textContent).toContain('×1');
        const row20 = Array.from(document.querySelectorAll('tbody tr'))[0];
        expect(Array.from(row20.querySelectorAll('.wspr-strip-dot')).map((d) => d.dataset.region)).toEqual(['JA', 'NA', 'EU', 'SA']);
        expect(dot('20m', 'JA').classList.contains('is-silent')).toBe(true);
        expect(dot('20m', 'NA').classList.contains('is-below')).toBe(true);
        expect(dot('15m', 'AS').className).toContain('is-surge is-strong');
        expect(dot('20m', 'EU').getAttribute('role')).toBe('gridcell');
        expect(dot('20m', 'EU').closest('td').getAttribute('role')).toBe('presentation');
        expect(document.querySelectorAll('#wspr-matrix-body [tabindex="0"]')).toHaveLength(1);
        expect(dot('20m', 'JA').getAttribute('tabindex')).toBe('0');
    });

    it('arrow keys walk the dots by position, up/down to the nearest dot', async () => {
        await open();
        dot('20m', 'EU').focus();
        press('ArrowRight');
        expect(at(document.activeElement)).toBe('20m/SA');
        press('ArrowLeft');
        press('ArrowLeft');
        expect(at(document.activeElement)).toBe('20m/NA');
        press('ArrowDown'); // ×½ → nearest in 15m is EU (×1)
        expect(at(document.activeElement)).toBe('15m/EU');
        press('ArrowUp');
        expect(at(document.activeElement)).toBe('20m/EU');
        press('End');
        expect(at(document.activeElement)).toBe('20m/SA');
        press('Home');
        expect(at(document.activeElement)).toBe('20m/JA');
    });

    it('Enter on a dot toggles the drill-down and keeps focus on it', async () => {
        await open();
        dot('15m', 'AS').focus();
        press('Enter');
        expect(state.drillDownBand).toBe('15m');
        expect(state.drillDownRegion).toBe('AS');
        expect(at(document.activeElement)).toBe('15m/AS');
        expect(document.activeElement.getAttribute('aria-selected')).toBe('true');
        dot('20m', 'EU').click();
        expect(state.drillDownRegion).toBe('EU');
    });
});
