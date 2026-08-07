import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../static/config.js', () => ({
    loadConfig: () => ({ initialCenter: [52, 7], initialZoom: 4 })
}));

const mockMap = {
    on: vi.fn(),
    invalidateSize: vi.fn(),
    setView: vi.fn(),
    getZoom: vi.fn(() => 4)
};

vi.mock('../static/map.js', () => ({
    initMap: vi.fn(),
    setTheme: vi.fn(),
    syncMercatorCountryLayer: vi.fn(async () => {}),
    syncMercatorGraylineLayer: vi.fn(async () => {}),
    syncMercatorDxccLabelLayer: vi.fn(async () => {}),
    map: mockMap
}));

let azimuthEnabled = false;
const setAzimuthEnabledMock = vi.fn((enabled) => {
    azimuthEnabled = !!enabled;
});

vi.mock('../static/azimuth-runtime.js', () => ({
    initAzimuthCanvas: vi.fn(),
    isAzimuthEnabled: vi.fn(() => azimuthEnabled),
    loadAzimuthWorldGeoJson: vi.fn(async () => ({})),
    renderAzimuthScene: vi.fn(),
    setAzimuthAntennaOverlay: vi.fn(),
    getAzimuthLatLngFromClientPoint: vi.fn(() => null),
    getAzimuthCenter: vi.fn(() => [52, 7]),
    setAzimuthCenter: vi.fn(),
    setAzimuthEnabled: setAzimuthEnabledMock,
    setAzimuthTheme: vi.fn(),
    setAzimuthZoom: vi.fn(),
    clampAzimuthZoom: vi.fn((z) => Math.max(1, Math.min(5, Number(z) || 1.5))),
    setAzimuthHorizonKm: vi.fn(),
    clampAzimuthHorizonKm: vi.fn((km) => Math.max(1000, Math.min(20015, Number(km) || 16000))),
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
    updateBandLabels: vi.fn(),
    clearDxClusterMarkers: vi.fn(),
    resetRenderFingerprint: vi.fn()
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
        qthLayer: null
    }
}));

function setupDom() {
    document.body.innerHTML = `
        <div id="controls"></div>
        <button id="theme-toggle"></button>
        <button id="hide-sidebar"></button>
        <button id="show-sidebar"></button>

        <form id="fetch-form"></form>
        <input id="qth" value="JO32" />
        <input id="minutes" value="15" />
        <input id="ssb-min-db" value="0" />
        <input id="cw-min-db" value="-15" />
        <input id="cycle-time" value="3" />

        <input type="radio" name="min-snr" value="none" id="snr-none" />
        <input type="radio" name="min-snr" value="cw" id="snr-cw" />
        <input type="radio" name="min-snr" value="ssb" id="snr-ssb" checked />
        <div id="min-snr-group"></div>

        <div id="style-group"></div>
        <input type="radio" name="style-select" value="grid-snr" checked />

        <div id="projection-group"></div>
        <div id="projection-switch-row" class="d-flex"></div>
        <input type="radio" name="projection-select" value="mercator" id="proj-mercator" checked />
        <input type="radio" name="projection-select" value="azimuthal" id="proj-azimuthal" />

        <div id="band-container"></div>
        <input type="radio" name="band" value="all" checked />
        <input type="radio" name="band" value="2m" id="band-2m" />
        <input type="checkbox" class="band-enable" value="2m" checked />
        <div id="band-wrapper-2m" class="d-flex"></div>

        <div id="azimuth-options-group" class="d-flex"></div>
        <input id="azimuth-horizon-km" value="16000" />
        <input type="checkbox" id="azimuth-ns6t-indicator" checked />
        <input type="checkbox" id="azimuth-dxcc-labels" checked />
        <input id="dxcc-label-density" value="1" />
        <span id="dxcc-label-density-val">1.0</span>
        <input id="cluster-distance" value="500" />
        <span id="cluster-dist-val">500</span>

        <input type="checkbox" id="dk3jf-mode" />
        <input type="checkbox" id="auto-zoom" />
        <input type="checkbox" id="surroundings" />

        <button id="btn-geo" type="button"></button>
        <button id="btn-submit" type="button" data-mode="go" title="Go" aria-label="Go"><i class="fas fa-play"></i></button>
        <button id="btn-cycle" type="button"></button>

        <div id="current-band-display"></div>
        <div id="stream-status"></div>

        <div id="map"></div>
        <canvas id="azimuth-canvas"></canvas>
        <div id="azimuth-zoom-controls"></div>
    `;
}

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => {
            store.set(String(key), String(value));
        },
        removeItem: (key) => {
            store.delete(String(key));
        },
        clear: () => {
            store.clear();
        }
    };

    Object.defineProperty(globalThis, 'localStorage', {
        configurable: true,
        value: mock
    });
    if (typeof window !== 'undefined') {
        Object.defineProperty(window, 'localStorage', {
            configurable: true,
            value: mock
        });
    }
}

async function importAppFresh() {
    vi.resetModules();
    await import('../static/app.js');
    await Promise.resolve();
    await Promise.resolve();
}

describe('app.js DK3JF mode behavior', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        azimuthEnabled = false;
        installLocalStorageMock();
        localStorage.clear();
        setupDom();
        window.history.replaceState({}, '', '/');

        global.navigator.geolocation = {
            getCurrentPosition: vi.fn()
        };

        const eventSourceMock = vi.fn(function EventSource() {
            this.close = vi.fn();
            this.addEventListener = vi.fn();
        });
        global.EventSource = eventSourceMock;
        window.EventSource = eventSourceMock;

        global.fetch = vi.fn(async () => ({
            ok: true,
            json: async () => ({
                snapshot_at: Math.floor(Date.now() / 1000),
                generated_at: Math.floor(Date.now() / 1000),
                spots: [{ lat: 52.5, lng: 7.0, snr: -6, ageSeconds: 0, locator: 'JO32', band: '20m' }]
            }),
            text: async () => ''
        }));
    });

    it('initializes with DK3JF disabled: keeps projection/azimuth options available and only disables 2m', async () => {
        localStorage.setItem('dk3jfModeEnabled', 'false');
        localStorage.setItem('mapProjection', 'azimuthal');

        // 2m is the focused (solo) band before dk3jf turns it off.
        localStorage.setItem('selectedBand', '2m');
        document.getElementById('band-container').dataset.focusBand = '2m';

        await importAppFresh();

        const projectionRow = document.getElementById('projection-switch-row');
        const band2mWrapper = document.getElementById('band-wrapper-2m');
        const azimuthOptionsGroup = document.getElementById('azimuth-options-group');

        expect(projectionRow.style.getPropertyValue('display')).toBe('');
        expect(band2mWrapper.style.getPropertyValue('display')).toBe('none');
        expect(azimuthOptionsGroup.style.getPropertyValue('display')).toBe('');

        expect(document.querySelector('input[name="projection-select"][value="azimuthal"]').checked).toBe(true);
        expect(localStorage.getItem('mapProjection')).toBe('azimuthal');

        expect(document.querySelector('.band-enable[value="2m"]').checked).toBe(false);
        // 2m was focused; disabling it drops focus back to "all" (empty dataset).
        expect(document.getElementById('band-container').dataset.focusBand).toBe('');
    });

    it('keeps projection/azimuth options visible while toggling DK3JF-specific 2m controls', async () => {
        localStorage.setItem('dk3jfModeEnabled', 'false');
        await importAppFresh();

        const toggle = document.getElementById('dk3jf-mode');
        const projectionRow = document.getElementById('projection-switch-row');
        const band2mWrapper = document.getElementById('band-wrapper-2m');
        const azimuthOptionsGroup = document.getElementById('azimuth-options-group');

        toggle.checked = true;
        toggle.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();

        expect(projectionRow.style.getPropertyValue('display')).toBe('');
        expect(band2mWrapper.style.getPropertyValue('display')).toBe('');
        expect(azimuthOptionsGroup.style.getPropertyValue('display')).toBe('');
        expect(localStorage.getItem('dk3jfModeEnabled')).toBe('true');

        toggle.checked = false;
        toggle.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();

        expect(projectionRow.style.getPropertyValue('display')).toBe('');
        expect(band2mWrapper.style.getPropertyValue('display')).toBe('none');
        expect(azimuthOptionsGroup.style.getPropertyValue('display')).toBe('');
        expect(localStorage.getItem('dk3jfModeEnabled')).toBe('false');
    });

    it('automatically starts retrieving data on load when a qth is remembered', async () => {
        localStorage.setItem('qth', 'W1AW');
        document.getElementById('qth').value = 'W1AW';

        await importAppFresh();
        await new Promise((resolve) => setTimeout(resolve, 0));

        const submitBtn = document.getElementById('btn-submit');
        expect(submitBtn.dataset.mode).toBe('stop');
        expect(submitBtn.innerHTML).toContain('fa-stop');
        expect(document.getElementById('stream-status').innerHTML).toContain('Connecting to QTH: W1AW');
    });

});
