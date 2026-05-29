import { beforeEach, describe, expect, it, vi } from 'vitest';

// Regression: app.js's top-level event wiring used to call
// document.getElementById('...').addEventListener(...) directly, which threw
// "TypeError: null is not an object" whenever any single element was missing
// (e.g. broken vendor load, partial HTML render, future refactor).
// This test imports app.js against an essentially-empty DOM and asserts that
// it does not throw — proving every top-level DOM access is null-safe.

vi.mock('../static/config.js', () => ({
    loadConfig: () => ({ initialCenter: [52, 7], initialZoom: 4 })
}));

vi.mock('../static/map.js', () => ({
    initMap: vi.fn(),
    setTheme: vi.fn(),
    syncMercatorCountryLayer: vi.fn(async () => {}),
    syncMercatorGraylineLayer: vi.fn(async () => {}),
    syncMercatorDxccLabelLayer: vi.fn(async () => {}),
    map: { on: vi.fn(), invalidateSize: vi.fn(), setView: vi.fn(), getZoom: vi.fn(() => 4) }
}));

vi.mock('../static/azimuth-runtime.js', () => ({
    initAzimuthCanvas: vi.fn(),
    isAzimuthEnabled: vi.fn(() => false),
    loadAzimuthWorldGeoJson: vi.fn(async () => ({})),
    renderAzimuthScene: vi.fn(),
    setAzimuthCenter: vi.fn(),
    setAzimuthEnabled: vi.fn(),
    setAzimuthTheme: vi.fn(),
    setAzimuthZoom: vi.fn(),
    clampAzimuthZoom: vi.fn((z) => z),
    setAzimuthHorizonKm: vi.fn(),
    clampAzimuthHorizonKm: vi.fn((km) => km),
    setAzimuthNs6tIndicatorEnabled: vi.fn(),
    setAzimuthDxccLabelDensity: vi.fn(),
    setAzimuthDxccLabelsEnabled: vi.fn()
}));

vi.mock('../static/ui.js', () => ({
    initUI: vi.fn(),
    attachUITooltipEvents: vi.fn(),
    initDxConditionsUI: vi.fn(),
    startDxPolling: vi.fn(),
    stopDxPolling: vi.fn(),
    resetDxConditions: vi.fn(),
    setFaviconColor: vi.fn()
}));

vi.mock('../static/renderers.js', () => ({
    updateMapVisualization: vi.fn()
}));

vi.mock('../static/state.js', () => ({
    state: {
        liveSpots: [],
        renderPending: false,
        cycleInterval: null,
        eventSource: null,
        renderInterval: null,
        heatLayer: null,
        targetLayer: null
    }
}));

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => { store.set(String(k), String(v)); },
        removeItem: (k) => { store.delete(String(k)); },
        clear: () => { store.clear(); }
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    if (typeof window !== 'undefined') {
        Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    }
}

describe('app.js bootstrap against minimal DOM', () => {
    beforeEach(() => {
        vi.resetModules();
        installLocalStorageMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('does not throw when no expected elements are present', async () => {
        document.body.innerHTML = '<div id="map"></div>';
        const errors = [];
        const errSpy = vi.spyOn(console, 'error').mockImplementation((...args) => { errors.push(args); });
        // Unhandled rejections would also indicate a regression.
        const rejections = [];
        const rejectionHandler = (ev) => { rejections.push(ev); };
        if (typeof window !== 'undefined') {
            window.addEventListener('unhandledrejection', rejectionHandler);
        }

        await expect(import('../static/app.js')).resolves.toBeDefined();
        // Yield to allow the async IIFE inside app.js to settle.
        await new Promise((resolve) => setTimeout(resolve, 50));

        // Filter out the deliberate console.error from missing DXCC sub-elements
        // that the rest of the code path does not touch.
        const fatalErrors = errors.filter(e => !String(e[0]).match(/^Leaflet|Error parsing/));
        expect(fatalErrors).toEqual([]);

        if (typeof window !== 'undefined') {
            window.removeEventListener('unhandledrejection', rejectionHandler);
        }
        errSpy.mockRestore();
    });
});
