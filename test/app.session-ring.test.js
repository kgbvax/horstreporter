import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Unit U2 (plan 2026-09-11-002): app.js wires the session ring into the SSE
// onmessage handler and invalidates the whole ring when the qth changes.
// The harness imports app.js fresh per test against a small DOM (same pattern
// as app.bootstrap.test.js / app.dk3jf.test.js); session-ring.js and state.js
// are deliberately NOT mocked so the app's own pushes are observable through
// their public API.

vi.mock('../static/config.js', () => ({
    loadConfig: () => ({ initialCenter: [52, 7], initialZoom: 4 })
}));

vi.mock('../static/cq-flag.js', () => ({
    isChaseQueueEnabled: () => false
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
    setMercatorDxHighlight: vi.fn(),
    clearMercatorDxHighlight: vi.fn(),
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
    setAzimuthDragging: vi.fn(),
    setAzimuthTheme: vi.fn(),
    setAzimuthZoom: vi.fn(),
    clampAzimuthZoom: vi.fn((z) => z),
    setAzimuthHorizonKm: vi.fn(),
    clampAzimuthHorizonKm: vi.fn((km) => km),
    setAzimuthNs6tIndicatorEnabled: vi.fn(),
    setAzimuthDxccLabelDensity: vi.fn(),
    setAzimuthDxccLabelsEnabled: vi.fn(),
    getAzimuthHiddenGridSquaresCount: vi.fn(() => 0),
    setAzimuthDxSpotHighlight: vi.fn()
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
    clearWsprMarkers: vi.fn(),
    resetRenderFingerprint: vi.fn(),
    whenActiveAreaRendered: vi.fn()
}));

vi.mock('../static/band-lab.js', () => ({
    initBandLab: vi.fn(),
    updateBandLab: vi.fn(),
    getBandLabLookbackMinutes: vi.fn(() => 15)
}));

vi.mock('../static/wspr-matrix.js', () => ({
    initWsprMatrix: vi.fn(),
    updateWsprMatrix: vi.fn(),
    clearDrillDown: vi.fn(),
    updateDrillDownButton: vi.fn()
}));

// timeline.js is mocked so a test can flip isTimelineActive() to prove the
// ring feed does not go through any render gate. All of app.js's named
// imports are provided.
const timelineMock = {
    timelineActive: false,
    onMomentCb: null,
    onExitCb: null
};
vi.mock('../static/timeline.js', () => ({
    isTimelineActive: () => timelineMock.timelineActive,
    enterTimeline: vi.fn(async () => {}),
    exitTimeline: vi.fn(),
    seek: vi.fn(async () => {}),
    play: vi.fn(),
    pause: vi.fn(),
    onMoment: (cb) => { timelineMock.onMomentCb = cb; },
    onExit: (cb) => { timelineMock.onExitCb = cb; },
    syncTimelineURL: vi.fn(),
    readTimelineURL: vi.fn(() => null),
    // U4 exports: mid-timeline filter changes invalidate + re-emit.
    invalidateBundles: vi.fn(),
    refreshMoment: vi.fn(async () => {})
}));

const NOW_MS = 1_730_000_000_000;

// EventSource mock that records instances so tests can drive onopen/onmessage
// exactly like the wire would (app.js assigns onopen/onmessage properties).
const esInstances = [];
const eventSourceMock = vi.fn(function EventSource(url) {
    this.url = url;
    this.close = vi.fn();
    this.addEventListener = vi.fn();
    esInstances.push(this);
});

function setupDom() {
    document.body.innerHTML = `
        <div id="controls"></div>
        <button id="theme-toggle"></button>
        <button id="hide-sidebar"></button>
        <button id="show-sidebar"></button>

        <form id="fetch-form"></form>
        <input id="qth" value="" />
        <input id="minutes" value="15" />
        <input id="ssb-min-db" value="0" />
        <input id="cw-min-db" value="-15" />

        <input type="radio" name="min-snr" value="none" id="snr-none" checked />
        <div id="min-snr-group"></div>

        <div id="style-group"></div>
        <input type="radio" name="style-select" value="grid-snr" checked />

        <div id="projection-group"></div>
        <div id="projection-switch-row" class="d-flex"></div>
        <input type="radio" name="projection-select" value="mercator" id="proj-mercator" checked />

        <div id="band-container"></div>
        <input type="radio" name="band" value="all" checked />
        <input type="checkbox" class="band-enable" value="20m" checked />

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
        <button id="btn-submit" type="button" data-mode="go" title="Go" aria-label="Go"></button>

        <button id="band-solo-clear" type="button"><span class="band-solo-name"></span></button>
        <div id="stream-status"></div>
        <div id="map"></div>
    `;
}

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => { store.set(String(k), String(v)); },
        removeItem: (k) => { store.delete(k); },
        clear: () => { store.clear(); }
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    if (typeof window !== 'undefined') {
        Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    }
}

// importAppFresh re-imports app.js against a fresh module registry so the real
// state.js / session-ring.js singletons start clean per test.
async function importAppFresh() {
    vi.resetModules();
    esInstances.length = 0;
    await import('../static/app.js');
    const { state } = await import('../static/state.js');
    const { sessionRing } = await import('../static/session-ring.js');
    return { state, sessionRing };
}

// startStream fills the qth input and dispatches the fetch-form submit,
// returning the EventSource instance app.js created.
async function startStream() {
    const form = document.getElementById('fetch-form');
    form.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    await Promise.resolve();
    expect(esInstances.length).toBeGreaterThan(0);
    return esInstances[esInstances.length - 1];
}

// deliverFrame drives one SSE message through the captured handler.
function deliverFrame(es, spot) {
    es.onmessage({ data: JSON.stringify(spot) });
}

const makeSpot = (ageSeconds, overrides = {}) => ({
    lat: 52.5, lng: 7.0, snr: -6, ageSeconds, locator: 'JO32',
    band: '20m', sender: 'DL1ABC', receiver: 'DL9ET',
    sourceType: 'mqtt', ...overrides
});

describe('app.js session ring feed (U2)', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        timelineMock.timelineActive = false;
        timelineMock.onMomentCb = null;
        timelineMock.onExitCb = null;
        installLocalStorageMock();
        localStorage.clear();
        setupDom();
        window.history.replaceState({}, '', '/');

        global.EventSource = eventSourceMock;
        window.EventSource = eventSourceMock;

        global.fetch = vi.fn(async () => ({
            ok: true,
            json: async () => ({ snapshot_at: 0, generated_at: 0, spots: [] }),
            text: async () => ''
        }));

        vi.spyOn(Date, 'now').mockReturnValue(NOW_MS);
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('pushes every received live frame into the ring with derived t from the stamps', async () => {
        document.getElementById('qth').value = 'W1AW';
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();

        expect(esInstances.length).toBe(1);

        deliverFrame(es, makeSpot(120));

        expect(state.liveSpots.length).toBe(1);
        expect(sessionRing.stats()).toEqual({
            count: 1,
            minT: Math.floor(NOW_MS / 1000) - 120,
            lastT: Math.floor(NOW_MS / 1000) - 120
        });

        // The stored entry is an immutable raw copy in the historySpot shape,
        // keyed by the same derived t.
        const raw = sessionRing.slice(Math.floor(NOW_MS / 1000) - 120, Math.floor(NOW_MS / 1000) - 120);
        expect(raw.length).toBe(1);
        expect(raw[0]).toMatchObject({
            t: Math.floor(NOW_MS / 1000) - 120,
            lat: 52.5, lng: 7.0, snr: -6, locator: 'JO32', band: '20m'
        });

        // A second distinct frame accumulates.
        deliverFrame(es, makeSpot(60, { locator: 'JO33', snr: -2 }));
        expect(sessionRing.stats().count).toBe(2);
        expect(sessionRing.stats().lastT).toBe(Math.floor(NOW_MS / 1000) - 60);
    });

    it('a connect/reconnect history dump seeds pre-session coverage', async () => {
        document.getElementById('qth').value = 'W1AW';
        const { sessionRing } = await importAppFresh();
        const es = await startStream();

        // Simulate the history dump: 31 frames spanning 30 minutes of
        // data-time delivered in one burst (all received at the same wall
        // clock — Date.now is frozen).
        for (let age = 0; age <= 1800; age += 60) {
            deliverFrame(es, makeSpot(age, { locator: 'JO3' + String(age).padStart(2, '0') }));
        }

        expect(sessionRing.stats().count).toBe(31);
        const t0 = Math.floor(NOW_MS / 1000);
        expect(sessionRing.stats().minT).toBe(t0 - 1800);
        expect(sessionRing.stats().lastT).toBe(t0);

        // The whole dump-seeded span is covered even though every frame was
        // received in the same instant — coverage tracks derived spot-time.
        expect(sessionRing.covers(t0 - 1800, t0)).toBe(true);
        // Anything earlier than the dump is a miss.
        expect(sessionRing.covers(t0 - 1801, t0)).toBe(false);

        // Re-delivery of a dump frame (reconnect re-seed) dedups. The dump's
        // locator at age 600 was 'JO3600' (same formula as above).
        deliverFrame(es, makeSpot(600, { locator: 'JO3600' }));
        expect(sessionRing.stats().count).toBe(31);
    });

    it('a qth change clears the ring; a resubmit with the same qth does not', async () => {
        document.getElementById('qth').value = 'W1AW';
        const { sessionRing } = await importAppFresh();
        const es = await startStream();

        deliverFrame(es, makeSpot(0));
        deliverFrame(es, makeSpot(30, { locator: 'JO33', snr: -2 }));
        expect(sessionRing.stats().count).toBe(2);

        // Stop the stream (submit while streaming) and start again with the
        // SAME qth: the ring survives the stream restart.
        document.getElementById('fetch-form')
            .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        await Promise.resolve();
        expect(sessionRing.stats().count).toBe(2);

        const es2 = await startStream();
        expect(es2).not.toBe(es);
        expect(sessionRing.stats().count).toBe(2);

        // New frames from the fresh connection still accumulate.
        deliverFrame(es2, makeSpot(0, { locator: 'JO34', snr: -3 }));
        expect(sessionRing.stats().count).toBe(3);

        // Now change the qth and resubmit: the whole ring is invalidated.
        document.getElementById('qth').value = 'W2AP';
        document.getElementById('fetch-form')
            .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        await Promise.resolve();
        const es3 = await startStream();

        expect(sessionRing.stats()).toEqual({ count: 0, minT: null, lastT: null });
        expect(sessionRing.covers(0, Math.floor(NOW_MS / 1000))).toBe(false);

        // The fresh cohort accumulates from empty.
        deliverFrame(es3, makeSpot(0));
        expect(sessionRing.stats().count).toBe(1);
    });

    it('frames still feed the ring while the timeline is active (feed independent of the render gate)', async () => {
        document.getElementById('qth').value = 'W1AW';
        const { sessionRing } = await importAppFresh();
        const es = await startStream();

        timelineMock.timelineActive = true;

        // The stream is still delivering frames (rendering is gated elsewhere);
        // the ring must receive every frame regardless of the timeline mode.
        deliverFrame(es, makeSpot(0));
        deliverFrame(es, makeSpot(300, { locator: 'JO33', snr: -1 }));
        expect(sessionRing.stats().count).toBe(2);
        const t0 = Math.floor(NOW_MS / 1000);
        expect(sessionRing.covers(t0 - 300, t0)).toBe(true);

        timelineMock.timelineActive = false;
    });
});