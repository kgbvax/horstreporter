import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

// Mock utils.js deps that prop-matrix.js imports. bandColors + getEnabledBands
// are used; WSPR_REGIONS is the region column list.
vi.mock('../static/utils.js', () => ({
    WSPR_REGIONS: ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'],
    bandColors: { all: '#555', '20m': '#e67e22', '10m': '#16a085' },
    getEnabledBands: () => new Set(['20m', '10m']),
}));

import { initPropMatrix, __test } from '../static/prop-matrix.js';

const {
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    VIEW_TOGGLE_ID,
    ENABLE_KEY,
    VIEW_KEY,
    reset,
    renderCell,
    setPropMatrixVisible,
} = __test;

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
        <div id="${PANEL_ID}" class="prop-matrix-window is-hidden">
            <div class="prop-matrix-window-header">
                <span>Propagation Intel</span>
                <div class="prop-matrix-window-header-actions">
                    <button id="${VIEW_TOGGLE_ID}" type="button"></button>
                    <button type="button" class="prop-matrix-close"></button>
                </div>
            </div>
            <div id="${BODY_ID}"></div>
        </div>
    `;
}

function mockFetchOnce(payload, ok = true) {
    const resp = {
        ok,
        status: ok ? 200 : 500,
        json: async () => payload,
    };
    global.fetch = vi.fn(async () => resp);
}

function makeCell(band, region, { pOpen = 0.8, expectedCount = 12, confidence = 0.8, surge = false, view = 'nowcast' } = {}) {
    return {
        band,
        region,
        confidence,
        surge,
        nowcast: { pOpen, expectedCount, confidence },
        forecast: view === 'forecast'
            ? { pOpen, expectedCount, confidence }
            : { pOpen: pOpen * 0.5, expectedCount: expectedCount * 0.5, confidence: confidence * 0.5 },
    };
}

describe('prop-matrix panel', () => {
    let store;
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        store = globalThis.localStorage;
        setupDom();
        reset();
        // jsdom sets document.visibilityState = 'visible' by default; ensure.
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    });

    afterEach(() => {
        __test.runtime.enabled && setPropMatrixVisible(false);
        reset();
        global.fetch = originalFetch;
        vi.restoreAllMocks();
    });

    it('toggle button shows/hides the panel and persists state to localStorage', async () => {
        mockFetchOnce({ regions: ['EU', 'NA'], bands: ['20m'], cells: [] });
        initPropMatrix();

        const panel = document.getElementById(PANEL_ID);
        const toggle = document.getElementById(TOGGLE_ID);
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe(null);

        toggle.click();
        // Let the in-flight fetch resolve.
        await new Promise((r) => setTimeout(r, 0));
        expect(panel.classList.contains('is-hidden')).toBe(false);
        expect(store.getItem(ENABLE_KEY)).toBe('true');
        expect(toggle.style.display).toBe('none');

        // Close via the panel close button.
        const closeBtn = panel.querySelector('.prop-matrix-close');
        closeBtn.click();
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe('false');
        expect(toggle.style.display).toBe('');
    });

    it('opening the panel starts polling; closing aborts in-flight requests', async () => {
        let abortSignal;
        global.fetch = vi.fn(async (_url, opts) => {
            abortSignal = opts?.signal;
            // Hold the request open so closing mid-flight triggers abort.
            return new Promise((resolve, reject) => {
                if (abortSignal) {
                    abortSignal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
                }
            });
        });

        initPropMatrix();
        const toggle = document.getElementById(TOGGLE_ID);
        toggle.click();
        await new Promise((r) => setTimeout(r, 0));
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(__test.runtime.pollTimer).not.toBeNull();
        expect(__test.runtime.abortController).not.toBeNull();

        const inFlightController = __test.runtime.abortController;
        // Close the panel — should abort the in-flight request.
        setPropMatrixVisible(false);
        expect(__test.runtime.pollTimer).toBeNull();
        expect(__test.runtime.abortController).toBeNull();
        // The controller we captured before close should now be aborted.
        expect(inFlightController.signal.aborted).toBe(true);
    });

    it('renders a cell with green background, expected-count badge, and surge border', () => {
        const html = renderCell('10m', 'CAR', makeCell('10m', 'CAR', { pOpen: 0.8, expectedCount: 12, surge: true }));
        // Green background at ~83% alpha (0.15 + 0.8 * 0.85)
        expect(html).toContain('background: rgba(40, 167, 69,');
        expect(html).toContain('class="prop-matrix-cell prop-matrix-surge');
        expect(html).toContain('<span class="prop-matrix-badge">12</span>');
        expect(html).toContain('⚡');
    });

    it('renders an empty cell when no data is present', () => {
        const html = renderCell('20m', 'AF', null);
        expect(html).toBe('<td class="prop-matrix-cell-empty" data-band="20m" data-region="AF" role="button" tabindex="0"></td>');
    });

    it('low confidence produces a dashed border class', () => {
        const html = renderCell('20m', 'AF', makeCell('20m', 'AF', { pOpen: 0.5, expectedCount: 3, confidence: 0.2 }));
        expect(html).toContain('prop-matrix-low-confidence');
    });

    it('forecast toggle renders forecast values instead of nowcast', async () => {
        const payload = {
            regions: ['EU', 'NA'],
            bands: ['20m', '10m'],
            cells: [
                makeCell('20m', 'EU', { pOpen: 0.9, expectedCount: 20, surge: false }),
            ],
        };
        mockFetchOnce(payload);
        initPropMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));

        const body = document.getElementById(BODY_ID);
        // Nowcast view default: 20 expected.
        expect(body.innerHTML).toContain('<span class="prop-matrix-badge">20</span>');

        // Switch to forecast view.
        const viewToggle = document.getElementById(VIEW_TOGGLE_ID);
        viewToggle.click();
        expect(store.getItem(VIEW_KEY)).toBe('forecast');
        // Forecast values are 50% of nowcast in the helper → 10 expected.
        expect(body.innerHTML).toContain('<span class="prop-matrix-badge">10</span>');
    });

    it('changing QTH triggers a re-poll with the new QTH', async () => {
        const calls = [];
        global.fetch = vi.fn(async (url) => {
            calls.push(url);
            return {
                ok: true,
                status: 200,
                json: async () => ({ regions: ['EU'], bands: ['20m'], cells: [] }),
            };
        });

        initPropMatrix();
        document.getElementById(TOGGLE_ID).click();
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(1);
        expect(calls[0]).toContain('qth=JO32');

        // Change QTH and dispatch a change event.
        const qthInput = document.getElementById('qth');
        qthInput.value = 'FN31';
        qthInput.dispatchEvent(new Event('change'));
        await new Promise((r) => setTimeout(r, 0));
        expect(calls.length).toBe(2);
        expect(calls[1]).toContain('qth=FN31');
    });
});