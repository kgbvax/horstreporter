import { beforeEach, describe, expect, it, vi } from 'vitest';
import { writePerfScenarioReport } from './perf-report.js';

let perfApi;

const handlers = {};
const mockMap = {
    on: vi.fn((eventName, callback) => {
        handlers[eventName] = callback;
    }),
    invalidateSize: vi.fn(),
    setView: vi.fn(),
    getZoom: vi.fn(() => 4)
};

vi.mock('../static/config.js', () => ({
    loadConfig: () => ({ initialCenter: [52, 7], initialZoom: 4 })
}));

vi.mock('../static/map.js', () => ({
    initMap: vi.fn(),
    setTheme: vi.fn(),
    syncMercatorCountryLayer: vi.fn(async () => {}),
    syncMercatorGraylineLayer: vi.fn(async () => {}),
    syncMercatorDxccLabelLayer: vi.fn(async () => {}),
    map: mockMap
}));

vi.mock('../static/azimuth-runtime.js', () => ({
    initAzimuthCanvas: vi.fn(),
    isAzimuthEnabled: vi.fn(() => false),
    loadAzimuthWorldGeoJson: vi.fn(async () => ({})),
    renderAzimuthScene: vi.fn(),
    setAzimuthAntennaOverlay: vi.fn(),
    getAzimuthLatLngFromClientPoint: vi.fn(() => null),
    getAzimuthCenter: vi.fn(() => [52, 7]),
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
    initGridSnrLegend: vi.fn()
}));

vi.mock('../static/renderers.js', () => ({
    updateMapVisualization: vi.fn(),
    updateBandLabels: vi.fn()
}));

vi.mock('../static/band-lab.js', () => ({
    initBandLab: vi.fn(),
    updateBandLab: vi.fn()
}));

vi.mock('../static/state.js', () => ({
    state: {
        liveSpots: [],
        renderPending: false,
        cycleInterval: null,
        eventSource: null,
        renderInterval: null,
        heatLayer: null,
        targetLayer: null,
        lastMercatorInteractionAt: 0,
        lastMercatorAutoZoomAt: 0
    }
}));

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => {
            store.set(String(k), String(v));
        },
        removeItem: (k) => {
            store.delete(String(k));
        },
        clear: () => {
            store.clear();
        }
    };

    Object.defineProperty(globalThis, 'localStorage', {
        configurable: true,
        value: mock
    });
    Object.defineProperty(window, 'localStorage', {
        configurable: true,
        value: mock
    });
}

function setupDom() {
    document.body.innerHTML = `
        <div id="controls"></div>
        <button id="theme-toggle"></button>
        <button id="hide-sidebar"></button>
        <button id="show-sidebar"></button>

        <form id="fetch-form"></form>
        <input id="target" value="JO32" />
        <input id="minutes" value="15" />
        <input id="ssb-min-db" value="0" />
        <input id="cw-min-db" value="-15" />
        <input id="cycle-time" value="3" />

        <div id="style-group"></div>
        <div id="projection-group"></div>
        <input type="radio" name="projection-select" value="mercator" checked />
        <input type="radio" name="style-select" value="grid-snr" checked />
        <input type="radio" name="min-snr" value="none" checked />
        <input type="radio" name="band" value="all" checked />
        <div class="band-wrapper"></div>
        <div id="band-container"></div>

        <input type="checkbox" class="band-enable" value="20m" checked />
        <input type="checkbox" id="surroundings" />
        <input type="checkbox" id="auto-zoom" />
        <input type="checkbox" id="show-dxcc-labels" />
        <input type="checkbox" id="dk3jf-mode" />

        <input id="azimuth-horizon-km" value="16000" />
        <input type="checkbox" id="azimuth-ns6t-indicator" checked />
        <input id="dxcc-label-density" value="1" />
        <span id="dxcc-label-density-val">1.0</span>
        <input id="cluster-distance" value="500" />
        <span id="cluster-dist-val">500</span>

        <button id="btn-geo" type="button"></button>
        <button id="btn-submit" type="button">Go</button>
        <button id="btn-cycle" type="button"></button>
        <button id="btn-center" type="button"></button>

        <div id="current-band-display"></div>
        <div id="stream-status"></div>

        <div id="projection-switch-row"></div>
        <div id="azimuth-options-group"></div>
        <div id="band-wrapper-2m"></div>

        <div id="map"></div>
        <canvas id="azimuth-canvas"></canvas>
        <div id="azimuth-zoom-controls"></div>
    `;
}

describe('mercator interaction perf instrumentation', () => {
    beforeEach(async () => {
        vi.resetModules();
        vi.clearAllMocks();
        Object.keys(handlers).forEach((k) => delete handlers[k]);
        globalThis.__HORST_PERF_TEST__ = true;
        installLocalStorageMock();
        setupDom();

        global.navigator.geolocation = {
            getCurrentPosition: vi.fn()
        };

        const eventSourceMock = vi.fn(function EventSource() {
            this.close = vi.fn();
            this.addEventListener = vi.fn();
        });
        global.EventSource = eventSourceMock;
        window.EventSource = eventSourceMock;

        perfApi = await import('../static/perf.js');
        perfApi.resetPerfMetrics();

        const appModule = await import('../static/app.js');
        await Promise.resolve();
        await Promise.resolve();
        if (appModule && typeof appModule.attachMapEvents === 'function') {
            appModule.attachMapEvents();
        }
    });

    it('captures zoom/pan timing metrics on Mercator event flow', () => {
        expect(typeof handlers.zoomstart).toBe('function');
        expect(typeof handlers.zoomend).toBe('function');
        expect(typeof handlers.movestart).toBe('function');
        expect(typeof handlers.moveend).toBe('function');

        for (let i = 0; i < 5; i += 1) {
            handlers.zoomstart();
            handlers.zoomend();
            handlers.movestart();
            handlers.moveend();
        }

        const snapshot = perfApi.getPerfSnapshot();
        writePerfScenarioReport('mercator-interaction-flow', {
            metrics: snapshot.metrics,
            counters: snapshot.counters,
            metadata: {
                interactions: 5,
                events: ['zoomstart', 'zoomend', 'movestart', 'moveend']
            }
        });

        expect(snapshot.metrics['mercator.interaction.zoom_duration_ms']).toBeDefined();
        expect(snapshot.metrics['mercator.interaction.pan_duration_ms']).toBeDefined();
        expect(snapshot.counters['mercator.interaction.zoom_start']).toBeGreaterThanOrEqual(5);
        expect(snapshot.counters['mercator.interaction.zoom_end']).toBeGreaterThanOrEqual(5);
        expect(snapshot.counters['mercator.interaction.pan_start']).toBeGreaterThanOrEqual(5);
        expect(snapshot.counters['mercator.interaction.pan_end']).toBeGreaterThanOrEqual(5);
    });
});
