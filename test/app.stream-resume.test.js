import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Stream v2 client behaviour in app.js: compact `spots` frames, resume ids,
// since=/prev_* on restarts, exact dedup of the overlap, and closing the
// stream after the tab has been hidden for a while. Same harness pattern as
// app.session-ring.test.js (app.js imported fresh against a small DOM;
// state.js and session-ring.js are real).

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
    attachUITooltipEvents: vi.fn()
}));

vi.mock('../static/renderers.js', () => ({
    updateMapVisualization: vi.fn(),
    updateBandLabels: vi.fn(),
    refreshBandLabels: vi.fn(),
    setBandNormalRateProvider: vi.fn(),
    clearDxClusterMarkers: vi.fn(),
    clearWsprMarkers: vi.fn(),
    resetRenderFingerprint: vi.fn(),
    whenActiveAreaRendered: vi.fn()
}));

vi.mock('../static/cond-now.js', () => ({ initCondNow: vi.fn() }));
vi.mock('../static/cond-dock.js', () => ({ initCondDock: vi.fn() }));
vi.mock('../static/band-lab.js', () => ({
    initBandLab: vi.fn(),
    updateBandLab: vi.fn(),
    getBandLabLookbackMinutes: vi.fn(() => 15),
    getBandNormalRate: vi.fn(() => null)
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
        <input type="checkbox" id="show-rbn-spots" checked />
        <input type="checkbox" id="show-wspr-spots" checked />
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

async function importAppFresh() {
    vi.resetModules();
    esInstances.length = 0;
    const app = await import('../static/app.js');
    const { state } = await import('../static/state.js');
    return { state, HIDDEN_MS: app.HIDDEN_STREAM_CLOSE_MS };
}

const submit = () => document.getElementById('fetch-form')
    .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));

async function startStream() {
    submit();
    await Promise.resolve();
    return esInstances[esInstances.length - 1];
}

const handler = (es, type) => es.addEventListener.mock.calls.find((c) => c[0] === type)?.[1];
const urlOf = (es) => new URL(es.url, 'http://localhost');

// Deliver one v2 `spots` frame (server time n = the mocked wall clock).
function deliverSpots(es, tuples, id = '', nOffset = 0) {
    handler(es, 'spots')({ data: JSON.stringify({ n: Math.floor(NOW_MS / 1000) + nOffset, s: tuples }), lastEventId: id });
}
const endHistory = (es, id) => handler(es, 'history_end')({ data: '{}', lastEventId: id });

let hidden = false;
function setHidden(value) {
    hidden = value;
    document.dispatchEvent(new Event('visibilitychange'));
}

describe('app.js stream v2 client', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
        timelineMock.timelineActive = false;
        installLocalStorageMock();
        localStorage.clear();
        setupDom();
        window.history.replaceState({}, '', '/');
        hidden = false;
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });

        global.EventSource = eventSourceMock;
        window.EventSource = eventSourceMock;
        global.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ spots: [] }), text: async () => '' }));
        vi.spyOn(Date, 'now').mockReturnValue(NOW_MS);
        document.getElementById('qth').value = 'W1AW';
    });

    afterEach(() => {
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it('opens the stream with v=2 and decodes spots frames into v1-shaped spots', async () => {
        const { state } = await importAppFresh();
        const es = await startStream();
        expect(urlOf(es).searchParams.get('v')).toBe('2');
        expect(urlOf(es).searchParams.has('since')).toBe(false);

        deliverSpots(es, [['JO32', '20m', -6, 120], ['EM12', '40m', -20, 30, '', 'w']]);
        expect(state.liveSpots).toHaveLength(2);
        expect(state.liveSpots[0]).toMatchObject({
            locator: 'JO32', band: '20m', snr: -6, ageSeconds: 120, sourceType: 'mqtt', lat: 52.5, lng: 7,
        });
        expect(state.liveSpots[1]).toMatchObject({ sourceType: 'wspr' });
        expect(state.liveSpots[0].__t).toBe(Math.floor(NOW_MS / 1000) - 120);
    });

    it('tracks the last event id from history_end and live frames', async () => {
        const { state } = await importAppFresh();
        const es = await startStream();
        deliverSpots(es, [['JO32', '20m', -6, 120]]);
        expect(state.streamLastId).toBeNull();
        endHistory(es, 'ep-10');
        expect(state.streamLastId).toBe('ep-10');
        deliverSpots(es, [['JO33', '20m', -6, 1]], 'ep-12');
        expect(state.streamLastId).toBe('ep-12');
    });

    it('closes the stream after the tab is hidden for 3 minutes and resumes with since on return', async () => {
        const { state, HIDDEN_MS } = await importAppFresh();
        const es = await startStream();
        deliverSpots(es, [['JO32', '20m', -6, 120]]);
        endHistory(es, 'ep-10');

        setHidden(true);
        vi.advanceTimersByTime(HIDDEN_MS - 1000);
        expect(es.close).not.toHaveBeenCalled();
        vi.advanceTimersByTime(2000);
        expect(es.close).toHaveBeenCalled();
        expect(state.eventSource).toBeNull();
        expect(state.streamSuspended).toBe(true);
        expect(document.getElementById('btn-submit').dataset.mode).toBe('stop');

        setHidden(false);
        const es2 = esInstances[esInstances.length - 1];
        expect(es2).not.toBe(es);
        const q = urlOf(es2).searchParams;
        expect(q.get('since')).toBe('ep-10');
        expect(q.get('prev')).toBe('1');
        expect(state.streamSuspended).toBe(false);
        expect(state.liveSpots).toHaveLength(1); // kept while resuming
    });

    it('does not close the stream when the tab returns in time, or while the timeline is active', async () => {
        await importAppFresh();
        const es = await startStream();
        endHistory(es, 'ep-1');
        setHidden(true);
        vi.advanceTimersByTime(60_000);
        setHidden(false);
        vi.advanceTimersByTime(600_000);
        expect(es.close).not.toHaveBeenCalled();

        timelineMock.timelineActive = true;
        setHidden(true); // timeline gate: visibility machinery stays out of it
        vi.advanceTimersByTime(600_000);
        expect(es.close).not.toHaveBeenCalled();
    });

    it('re-showing a hidden source restarts with since/prev_* and never duplicates the overlap', async () => {
        document.getElementById('show-rbn-spots').checked = false;
        const { state } = await importAppFresh();
        const es = await startStream();
        expect(urlOf(es).searchParams.get('include_rbn')).toBe('false');
        deliverSpots(es, [['JO32', '20m', -6, 120], ['JO33', '20m', -4, 90]]);
        endHistory(es, 'ep-5');
        expect(state.liveSpots).toHaveLength(2);

        const rbn = document.getElementById('show-rbn-spots');
        rbn.checked = true;
        rbn.dispatchEvent(new Event('change', { bubbles: true }));
        const es2 = esInstances[esInstances.length - 1];
        expect(es2).not.toBe(es);
        const q = urlOf(es2).searchParams;
        expect(q.has('include_rbn')).toBe(false);
        expect(q.get('since')).toBe('ep-5');
        expect(q.get('prev')).toBe('1');
        expect(q.get('prev_include_rbn')).toBe('false');

        // The server replays the overlap plus the newly allowed RBN spot.
        handler(es2, 'resume')({ data: '{"mode":"delta"}' });
        expect(state.lastResumeMode).toBe('delta');
        deliverSpots(es2, [['JO32', '20m', -6, 120], ['JO33', '20m', -4, 90], ['EM12', '20m', 20, 60, 'JO31', 'r']]);
        endHistory(es2, 'ep-9');
        expect(state.liveSpots).toHaveLength(3);
        expect(state.liveSpots.filter((s) => s.sourceType === 'rbn')).toHaveLength(1);
    });

    it('a replay after an automatic reconnect is deduped against what is already held', async () => {
        const { state } = await importAppFresh();
        const es = await startStream();
        deliverSpots(es, [['JO32', '20m', -6, 120]]);
        endHistory(es, 'ep-3');

        // EventSource reconnects on its own and replays the window.
        es.onopen();
        deliverSpots(es, [['JO32', '20m', -6, 125]], '', 5); // same spot 5 s later: age +5, n +5
        expect(state.liveSpots).toHaveLength(1);
        endHistory(es, 'ep-3');

        // After history_end a same-looking spot is new information.
        deliverSpots(es, [['JO33', '20m', -6, 0]], 'ep-4');
        expect(state.liveSpots).toHaveLength(2);
    });

    it('a qth change starts fresh without since', async () => {
        const { state } = await importAppFresh();
        const es = await startStream();
        deliverSpots(es, [['JO32', '20m', -6, 120]]);
        endHistory(es, 'ep-5');

        submit(); // stop
        await Promise.resolve();
        expect(state.streamLastId).toBeNull();
        document.getElementById('qth').value = 'DL1ABC';
        const es2 = await startStream();
        const q = urlOf(es2).searchParams;
        expect(q.get('qth')).toBe('DL1ABC');
        expect(q.has('since')).toBe(false);
        expect(q.has('prev')).toBe(false);
    });
});
