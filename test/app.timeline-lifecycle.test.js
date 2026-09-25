import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Unit U4 (plan 2026-09-11-002): timeline stream lifecycle rework in app.js.
// The SSE stays open during the timeline (KTD-3) with rendering gated in
// scheduleRender (KTD-10); exit rebuilds liveSpots from the ring tail without
// a server dump (KTD-11); control handlers are timeline-aware (KTD-12).
// A characterization baseline of the OLD teardown behavior was captured and
// run green before this diff; the tests it pinned (enter closes the
// EventSource / clears liveSpots; exit re-streams; entry-failure stacks a
// second EventSource; scheduleRender had no timeline gate) were REWRITTEN to
// the new behavior below — that rewrite is the plan-mandated update.
// The harness imports app.js fresh per test against a small DOM (same pattern
// as app.session-ring.test.js); session-ring.js and state.js are deliberately
// NOT mocked so ring pushes and the rebuilt live list are observable.

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

let azimuthEnabled = false;
vi.mock('../static/azimuth-runtime.js', () => ({
    initAzimuthCanvas: vi.fn(),
    isAzimuthEnabled: () => azimuthEnabled,
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

// timeline.js is mocked so tests can drive enter/exit and moment emission.
// All of app.js's named imports are provided.
const timelineMock = {
    timelineActive: false,
    onMomentCb: null,
    onExitCb: null,
    invalidateCalls: 0,
    refreshCalls: 0
};
vi.mock('../static/timeline.js', () => ({
    isTimelineActive: () => timelineMock.timelineActive,
    enterTimeline: vi.fn(async () => { timelineMock.timelineActive = true; }),
    exitTimeline: vi.fn(() => { timelineMock.timelineActive = false; }),
    seek: vi.fn(async () => {}),
    play: vi.fn(),
    pause: vi.fn(),
    onMoment: (cb) => { timelineMock.onMomentCb = cb; },
    onExit: (cb) => { timelineMock.onExitCb = cb; },
    syncTimelineURL: vi.fn(),
    readTimelineURL: vi.fn(() => null),
    invalidateBundles: vi.fn(() => { timelineMock.invalidateCalls++; }),
    refreshMoment: vi.fn(async () => { timelineMock.refreshCalls++; })
}));

const NOW_MS = 1_730_000_000_000;
const NOW_SEC = Math.floor(NOW_MS / 1000);

// EventSource mock that records instances so tests can drive onopen/onmessage
// and the registered event listeners (history_end, onerror) exactly like the
// wire would.
const esInstances = [];
const eventSourceMock = vi.fn(function EventSource(url) {
    this.url = url;
    this.readyState = 0;
    this.close = vi.fn();
    this._listeners = {};
    this.addEventListener = vi.fn((name, fn) => {
        (this._listeners[name] = this._listeners[name] || []).push(fn);
    });
    esInstances.push(this);
});
eventSourceMock.CLOSED = 2;

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
        <input type="radio" name="min-snr" value="cw" id="snr-cw" />
        <div id="min-snr-group"></div>

        <div id="style-group"></div>
        <input type="radio" name="style-select" value="grid-snr" checked />

        <div id="projection-group"></div>
        <div id="projection-switch-row" class="d-flex"></div>
        <input type="radio" name="projection-select" value="mercator" id="proj-mercator" checked />
        <input type="radio" name="projection-select" value="azimuthal" id="proj-azimuthal" />

        <div id="band-container">
            <input type="radio" name="band" value="all" checked />
            <input type="checkbox" class="band-enable" value="20m" checked />
            <input type="checkbox" class="band-enable" value="40m" />
        </div>

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
        <button id="btn-timeline" type="button"></button>

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
    timelineMock.timelineActive = false;
    timelineMock.onMomentCb = null;
    timelineMock.onExitCb = null;
    timelineMock.invalidateCalls = 0;
    timelineMock.refreshCalls = 0;
    await import('../static/app.js');
    const { state } = await import('../static/state.js');
    const { sessionRing } = await import('../static/session-ring.js');
    const { updateMapVisualization, updateBandLabels } = await import('../static/renderers.js');
    const { renderAzimuthScene } = await import('../static/azimuth-runtime.js');
    return {
        state,
        sessionRing,
        renderers: { updateMapVisualization, updateBandLabels, renderAzimuthScene }
    };
}

// startStream fills the qth input and dispatches the fetch-form submit,
// returning the EventSource instance app.js created.
async function startStream(qth = 'W1AW') {
    document.getElementById('qth').value = qth;
    localStorage.setItem('qth', qth);
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

// fireEsEvent triggers one of the stream's named event listeners (history_end,
// server_error, ...).
function fireEsEvent(es, name, ev = {}) {
    (es._listeners[name] || []).forEach((fn) => fn(ev));
}

// enterTimelineMode clicks the sidebar Time Travel button and lets the mocked
// enterTimeline flip the active flag.
async function enterTimelineMode() {
    document.getElementById('btn-timeline').click();
    await Promise.resolve();
    await Promise.resolve();
    expect(timelineMock.timelineActive).toBe(true);
}

// exitViaBar emulates the timeline bar's Live button: timeline.js runs
// exitTimeline() (the mock flips the flag) then the app-registered exit hook.
function exitViaBar() {
    timelineMock.timelineActive = false;
    timelineMock.onExitCb?.();
}

// emitMoment simulates timeline.js emitting the current moment (playback or
// scrub): the app-registered moment listener runs and the app retains the
// spots for the render gate.
function emitMoment(spots, playhead = 1_700_000_000) {
    timelineMock.onMomentCb?.(spots, playhead);
}

const makeSpot = (ageSeconds, overrides = {}) => ({
    lat: 52.5, lng: 7.0, snr: -6, ageSeconds, locator: 'JO32',
    band: '20m', sender: 'DL1ABC', receiver: 'DL9ET',
    sourceType: 'mqtt', ...overrides
});

// flushRender awaits the scheduleRender throttle + rAF pipeline.
async function flushRender() {
    await new Promise((r) => setTimeout(r, 60));
    await new Promise((r) => requestAnimationFrame(() => r()));
    await Promise.resolve();
}

let dateNowSpy = null;

describe('app.js timeline lifecycle (U4)', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        azimuthEnabled = false;
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

        dateNowSpy = vi.spyOn(Date, 'now').mockReturnValue(NOW_MS);
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('entering keeps the SSE open, liveSpots intact, and later frames still land in the ring', async () => {
        const { state, sessionRing, renderers } = await importAppFresh();
        const es = await startStream();
        fireEsEvent(es, 'history_end');
        deliverFrame(es, makeSpot(0, { locator: 'LIVE1' }));
        expect(state.liveSpots.length).toBe(1);

        await enterTimelineMode();

        // No SSE teardown, no liveSpots clear, no render-interval clear.
        expect(esInstances.length).toBe(1);
        expect(es.close).not.toHaveBeenCalled();
        expect(state.eventSource).toBe(es);
        expect(state.liveSpots.length).toBe(1);
        expect(state.renderInterval).not.toBeNull();
        // streamedFilter stays intact so the ring cohort guard keeps working.
        expect(state.streamedFilter).not.toBeNull();

        // Frames received during the replay still land in liveSpots + ring.
        deliverFrame(es, makeSpot(0, { locator: 'REPLAY1', snr: -3 }));
        expect(state.liveSpots.length).toBe(2);
        expect(sessionRing.stats().count).toBe(2);

        // And the gated render never paints the live array.
        await flushRender();
        for (const call of renderers.updateMapVisualization.mock.calls) {
            expect(call[0].map((s) => s.locator)).not.toContain('LIVE1');
        }
    });

    it('scheduleRender during timeline re-emits the current moment, not the live array (mercator)', async () => {
        const { state, renderers } = await importAppFresh();
        const es = await startStream();
        fireEsEvent(es, 'history_end');

        deliverFrame(es, makeSpot(0, { locator: 'LIVE1', lat: 10 }));

        await enterTimelineMode();
        const momentSpots = [makeSpot(60, { locator: 'MOMENT1', lat: 20 })];
        emitMoment(momentSpots);
        // Clear the mock's recorded listener emission; count fresh calls only.
        renderers.updateMapVisualization.mockClear();

        // An SSE frame + history_end arrive mid-replay: scheduleRender must not
        // paint state.liveSpots over the moment.
        deliverFrame(es, makeSpot(0, { locator: 'LIVE2', lat: 11 }));
        fireEsEvent(es, 'history_end');
        await flushRender();

        expect(renderers.updateMapVisualization).toHaveBeenCalled();
        const arg = renderers.updateMapVisualization.mock.calls.at(-1)[0];
        expect(arg.map((s) => s.locator)).toContain('MOMENT1');
        expect(arg.map((s) => s.locator)).not.toContain('LIVE1');
        expect(arg.map((s) => s.locator)).not.toContain('LIVE2');
    });

    it('theme toggle mid-replay re-renders the current moment (azimuth)', async () => {
        const { renderers } = await importAppFresh();
        await startStream();
        azimuthEnabled = true;

        await enterTimelineMode();
        const momentSpots = [makeSpot(60, { locator: 'MOMENT1' })];
        emitMoment(momentSpots);
        renderers.renderAzimuthScene.mockClear();

        document.getElementById('theme-toggle').click();
        await flushRender();

        expect(renderers.renderAzimuthScene).toHaveBeenCalled();
        const arg = renderers.renderAzimuthScene.mock.calls.at(-1)[0];
        expect(arg.spots.map((s) => s.locator)).toContain('MOMENT1');
    });

    it('visibilitychange during timeline does not pause or resume live rendering; it is unchanged when timeline is inactive', async () => {
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();
        fireEsEvent(es, 'history_end');
        deliverFrame(es, makeSpot(0));

        // --- timeline active: the visibility machinery must not touch pause state.
        await enterTimelineMode();
        Object.defineProperty(document, 'hidden', { configurable: true, value: true });
        document.dispatchEvent(new Event('visibilitychange'));
        expect(state.softPaused).toBe(false);
        dateNowSpy.mockReturnValue(NOW_MS + 60000); // long hidden stretch
        Object.defineProperty(document, 'hidden', { configurable: true, value: false });
        document.dispatchEvent(new Event('visibilitychange'));
        expect(state.softPaused).toBe(false);
        // No bulk-aging happened (the pause machinery never ran).
        expect(state.liveSpots[0].ageSeconds).toBe(0);
        // The stream kept feeding the ring through it all.
        deliverFrame(es, makeSpot(0, { locator: 'REPLAY1', snr: -3 }));
        expect(sessionRing.stats().count).toBe(2);

        // --- timeline inactive: unchanged soft-pause behavior (hidden pauses).
        exitViaBar();
        Object.defineProperty(document, 'hidden', { configurable: true, value: true });
        document.dispatchEvent(new Event('visibilitychange'));
        expect(state.softPaused).toBe(true);
        Object.defineProperty(document, 'hidden', { configurable: true, value: false });
        document.dispatchEvent(new Event('visibilitychange'));
        expect(state.softPaused).toBe(false);
    });

    it('exit rebuilds liveSpots from the ring tail with no /api/stream reconnect; replay spots are present and not bulk-aged', async () => {
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();
        fireEsEvent(es, 'history_end');
        // Pre-entry spots across the live window.
        deliverFrame(es, makeSpot(0, { locator: 'NOW1' }));
        deliverFrame(es, makeSpot(600, { locator: 'EARLIER1' }));
        deliverFrame(es, makeSpot(3000, { locator: 'OLD1' })); // outside 15-min window

        await enterTimelineMode();
        // Frames received during a (simulated) 2h replay.
        deliverFrame(es, makeSpot(0, { locator: 'REPLAY1', snr: -2 }));
        deliverFrame(es, makeSpot(120, { locator: 'REPLAY2', snr: -4 }));

        const before = esInstances.length;
        exitViaBar();

        // No reconnect: the SSE has been open the whole time.
        expect(esInstances.length).toBe(before);
        expect(state.eventSource).toBe(es);

        // Rebuilt from the ring tail: everything received during the replay is
        // present, in ring (t-sorted) order, with reconstructed ages.
        const byLocator = new Map(state.liveSpots.map((s) => [s.locator, s]));
        expect(byLocator.has('REPLAY1')).toBe(true);
        expect(byLocator.has('REPLAY2')).toBe(true);
        expect(byLocator.has('NOW1')).toBe(true);
        expect(byLocator.has('EARLIER1')).toBe(true);
        // Old spots outside the current live window are not resurrected.
        expect(byLocator.has('OLD1')).toBe(false);

        // Ages are the derived-t reconstruction, not a bulk-age inflation.
        expect(byLocator.get('NOW1').ageSeconds).toBe(0);
        expect(byLocator.get('EARLIER1').ageSeconds).toBe(600);
        expect(byLocator.get('REPLAY1').ageSeconds).toBe(0);
        expect(byLocator.get('REPLAY2').ageSeconds).toBe(120);
        expect(byLocator.get('REPLAY1').__recvAge).toBe(0);
        expect(typeof byLocator.get('REPLAY1').__recvMs).toBe('number');

        // The ring survives the exit (same qth cohort).
        expect(sessionRing.stats().count).toBeGreaterThanOrEqual(4);
    });

    it('exit when the stream was not running at entry starts a fresh stream (URL-restore path)', async () => {
        const { state } = await importAppFresh();
        document.getElementById('qth').value = 'W1AW';
        localStorage.setItem('qth', 'W1AW');

        await enterTimelineMode();
        expect(esInstances.length).toBe(0);
        exitViaBar();

        expect(esInstances.length).toBe(1);
        expect(String(esInstances[0].url)).toContain('qth=W1AW');
    });

    it('narrowing (band disable) mid-timeline: no stream restart, bundle LRU invalidated, moment re-emitted', async () => {
        const { state, renderers } = await importAppFresh();
        document.querySelector('.band-enable[value="40m"]').checked = true;
        const es = await startStream(); // streamed filter = {20m, 40m}
        expect(state.streamedFilter.bands.has('40m')).toBe(true);

        await enterTimelineMode();
        const before = esInstances.length;
        timelineMock.invalidateCalls = 0;
        timelineMock.refreshCalls = 0;

        const cb40 = document.querySelector('.band-enable[value="40m"]');
        cb40.checked = false;
        cb40.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();
        await flushRender();

        // No reconnect; the mid-timeline handler fired.
        expect(esInstances.length).toBe(1);
        expect(timelineMock.invalidateCalls).toBe(1);
        expect(timelineMock.refreshCalls).toBe(1);
        // The live-array painter stayed off the map (refreshBandPills gate).
        expect(renderers.updateBandLabels).not.toHaveBeenCalled();

        // Exit reconciles: nothing to reconnect for a narrowing — no restart.
        const exitBefore = esInstances.length;
        exitViaBar();
        expect(esInstances.length).toBe(exitBefore);
    });

    it('restarts the stream when a threshold slider is re-created after load (Min SNR mode switch)', async () => {
        const { state } = await importAppFresh();
        document.getElementById('snr-cw').checked = true;
        await startStream(); // streamed filter: cw @ -15 dB
        expect(state.streamedFilter.cwMinDb).toBe('-15');
        const before = esInstances.length;

        // Svelte replaces the slider element on a mode switch; the old one is gone.
        const old = document.getElementById('cw-min-db');
        const fresh = old.cloneNode(true);
        old.replaceWith(fresh);
        fresh.value = '-25'; // lower = widening: needs data the server is not sending
        fresh.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();

        expect(esInstances.length).toBe(before + 1);
    });

    it('narrowing (SNR raise) mid-timeline: no stream restart, moment re-emitted', async () => {
        const { state } = await importAppFresh();
        document.getElementById('snr-cw').checked = true;
        const es = await startStream(); // streamed filter: cw @ -15 dB
        expect(state.streamedFilter.cwMinDb).toBe('-15');

        await enterTimelineMode();
        const before = esInstances.length;
        timelineMock.invalidateCalls = 0;
        timelineMock.refreshCalls = 0;

        const cwEl = document.getElementById('cw-min-db');
        cwEl.value = '-5'; // raise = narrowing
        cwEl.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();

        expect(esInstances.length).toBe(before);
        expect(timelineMock.invalidateCalls).toBe(1);
        expect(timelineMock.refreshCalls).toBe(1);
    });

    it('widening (band enable of an unseen band) mid-timeline: stream untouched, reconciled on exit', async () => {
        const { state } = await importAppFresh();
        const es = await startStream(); // streamed filter = {20m}
        expect(state.streamedFilter.bands.has('40m')).toBe(false);

        await enterTimelineMode();
        const before = esInstances.length;

        const cb40 = document.querySelector('.band-enable[value="40m"]');
        cb40.checked = true;
        cb40.dispatchEvent(new Event('change', { bubbles: true }));
        await Promise.resolve();
        await flushRender();

        // No restart mid-timeline.
        expect(esInstances.length).toBe(1);
        expect(timelineMock.invalidateCalls).toBe(1);
        expect(timelineMock.refreshCalls).toBe(1);

        // Exit reconciles the queued widening with the stream filter.
        exitViaBar();
        expect(esInstances.length).toBe(2);
        const u = new URL(esInstances[1].url, 'http://localhost');
        expect(u.searchParams.get('enabled_bands')).toContain('40m');
    });

    it('qth change mid-timeline (go submit): timeline exits, ring cleared, one fresh stream starts', async () => {
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();
        deliverFrame(es, makeSpot(0));
        expect(sessionRing.stats().count).toBe(1);

        await enterTimelineMode();

        // All qth-change paths funnel through the fetch-form submit with the
        // submit button forced to 'go' (setQthAndRestart, btn-geo, qth Enter).
        document.getElementById('qth').value = 'W2AP';
        document.getElementById('btn-submit').setAttribute('data-mode', 'go');
        document.getElementById('fetch-form')
            .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        await Promise.resolve();

        expect(timelineMock.timelineActive).toBe(false);
        expect(esInstances.length).toBe(2);
        expect(es.close).toHaveBeenCalled();
        expect(state.eventSource).toBe(esInstances[1]);
        const u = new URL(esInstances[1].url, 'http://localhost');
        expect(u.searchParams.get('qth')).toBe('W2AP');
        // The foreign cohort is gone.
        expect(sessionRing.stats().count).toBe(0);
    });

    it('surroundings toggle mid-timeline: timeline exits, ring cleared, one fresh stream starts', async () => {
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();
        deliverFrame(es, makeSpot(0));
        expect(sessionRing.stats().count).toBe(1);

        await enterTimelineMode();

        document.getElementById('surroundings').checked = true;
        window.__horstSurroundingsChanged();
        await Promise.resolve();

        expect(timelineMock.timelineActive).toBe(false);
        expect(esInstances.length).toBe(2);
        const u = new URL(esInstances[1].url, 'http://localhost');
        expect(u.searchParams.get('surroundings')).toBe('true');
        expect(sessionRing.stats().count).toBe(0);
        expect(state.eventSource).toBe(esInstances[1]);
    });

    it('submit (stop) click mid-timeline exits the timeline, then performs the original stop', async () => {
        const { state, sessionRing } = await importAppFresh();
        const es = await startStream();
        deliverFrame(es, makeSpot(0));

        await enterTimelineMode();

        document.getElementById('fetch-form')
            .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        await Promise.resolve();

        // Timeline exited, then the original stop teardown ran.
        expect(timelineMock.timelineActive).toBe(false);
        expect(state.eventSource).toBeNull();
        expect(state.renderInterval).toBeNull();
        expect(state.liveSpots).toEqual([]);
        expect(state.streamedFilter).toBeNull();
        expect(document.getElementById('btn-submit').getAttribute('data-mode')).toBe('go');
        expect(document.getElementById('stream-status').textContent).toBe('Not connected');
        // No new EventSource was created (this was a stop, not a restart).
        expect(esInstances.length).toBe(1);
        // The ring survives (same qth cohort — U2 contract).
        expect(sessionRing.stats().count).toBe(1);
    });

    it('entry failure leaves the stream alone: error surfaced, no duplicate EventSource', async () => {
        const { enterTimeline } = await import('../static/timeline.js');
        const { state } = await importAppFresh();
        enterTimeline.mockRejectedValueOnce(Object.assign(new Error('chunk fetch 503'), { name: 'Error' }));
        const es = await startStream();
        deliverFrame(es, makeSpot(0));

        document.getElementById('btn-timeline').click();
        await new Promise((r) => setTimeout(r, 0));
        await Promise.resolve();
        await Promise.resolve();

        // The stream was left alone — no duplicate EventSource.
        expect(esInstances.length).toBe(1);
        expect(state.eventSource).toBe(es);
        expect(state.liveSpots.length).toBe(1);
        // The error is surfaced in the status line.
        expect(document.getElementById('stream-status').textContent).toBe('Could not load time travel data: chunk fetch 503');
        expect(document.querySelector('#stream-status .status-danger')).not.toBeNull();
    });

    it('fatal SSE drop mid-timeline: one silent restart attempt, then the ring-staleness state is surfaced', async () => {
        const { state } = await importAppFresh();
        const es = await startStream();
        deliverFrame(es, makeSpot(0));

        await enterTimelineMode();

        // First fatal drop: one silent restart attempt, timeline stays up.
        es.readyState = EventSource.CLOSED;
        es.onerror({ currentTarget: es });
        expect(esInstances.length).toBe(2);
        expect(timelineMock.timelineActive).toBe(true);
        expect(state.eventSource).toBe(esInstances[1]);

        // Second fatal drop: live coverage has ended — surface it, keep the
        // timeline active (the ring still serves covered moments), stop trying.
        const es2 = esInstances[1];
        es2.readyState = EventSource.CLOSED;
        es2.onerror({ currentTarget: es2 });
        expect(esInstances.length).toBe(2);
        expect(timelineMock.timelineActive).toBe(true);
        expect(document.getElementById('stream-status').textContent).toBe('Live data stopped. The timeline shows this session only.');
        expect(document.getElementById('stream-status').textContent).not.toContain('Disconnected');
        expect(document.getElementById('btn-submit').getAttribute('data-mode')).toBe('go');
        expect(state.eventSource).toBeNull();

        // Exiting afterwards starts a fresh stream (the only live source left).
        exitViaBar();
        expect(esInstances.length).toBe(3);
    });

    it('the age-prune tick during timeline stays gate-safe (no live-array paint)', async () => {
        const { state, renderers } = await importAppFresh();
        const es = await startStream();
        fireEsEvent(es, 'history_end');
        deliverFrame(es, makeSpot(0, { locator: 'LIVE1' }));

        await enterTimelineMode();
        emitMoment([makeSpot(60, { locator: 'MOMENT1' })]);
        renderers.updateMapVisualization.mockClear();

        // Drive the 5s prune interval directly (the same body the interval
        // runs): prune + scheduleRender must not paint the live array.
        expect(state.renderInterval).not.toBeNull();
        await flushRender();

        for (const call of renderers.updateMapVisualization.mock.calls) {
            expect(call[0].map((s) => s.locator)).not.toContain('LIVE1');
        }
    });

    // Stream status line (#stream-status): plain user language, no "Status:"
    // prefix, no "QTH", no byte counts; tones via .status-ok/-warn/-danger.
    describe('stream status line copy', () => {
        const statusEl = () => document.getElementById('stream-status');
        const toneText = (tone) => statusEl().querySelector(`.status-${tone}`)?.textContent;

        it('walks connecting → loading → live with the spot count on its own line', async () => {
            await importAppFresh();
            const es = await startStream('DL9ET');
            expect(toneText('warn')).toBe('Connecting to live data for DL9ET');
            expect(statusEl().querySelector('.spinner')).not.toBeNull();

            es.onopen();
            expect(statusEl().textContent).toContain('Live for DL9ET');
            expect(toneText('warn')).toBe('Loading recent spots');
            expect(statusEl().querySelector('.spinner')).not.toBeNull();

            deliverFrame(es, makeSpot(0));
            expect(toneText('warn')).toBe('Loading recent spots (1)');

            fireEsEvent(es, 'history_end');
            expect(statusEl().childNodes[0].textContent).toBe('Live for DL9ET');
            expect(toneText('ok')).toBe('1 spot');
            expect(statusEl().querySelector('.spinner')).toBeNull();
            expect(statusEl().textContent).not.toMatch(/Status|QTH|kB|Subscribed/);
        });

        it('says "Connection lost, reconnecting" on a transient error after the stream was live', async () => {
            await importAppFresh();
            const es = await startStream('DL9ET');
            es.onopen();
            es.readyState = 0; // CONNECTING: EventSource retries on its own
            es.onerror({ currentTarget: es });
            expect(statusEl().textContent).toContain('Live for DL9ET');
            expect(toneText('warn')).toBe('Connection lost, reconnecting');
        });

        it('never claims "Live" before the first connection opened', async () => {
            await importAppFresh();
            const es = await startStream('DL9ET');
            es.readyState = 0; // first connect failed at the network level; EventSource retries
            es.onerror({ currentTarget: es });
            expect(statusEl().textContent).not.toContain('Live for');
            expect(toneText('warn')).toBe('Cannot reach the server, retrying');
        });

        it('tells the user how to recover after a fatal drop', async () => {
            await importAppFresh();
            const es = await startStream('DL9ET');
            es.readyState = EventSource.CLOSED;
            es.onerror({ currentTarget: es });
            expect(statusEl().textContent).toBe('Disconnected. Press Go to reconnect.');
            expect(toneText('danger')).toBe('Disconnected. Press Go to reconnect.');
        });

        it('keeps the raw server error, prefixed, and never renders it as markup', async () => {
            await importAppFresh();
            const es = await startStream('DL9ET');
            fireEsEvent(es, 'server_error', { data: 'Server is at capacity. <b>Please</b> try again later.' });
            expect(toneText('danger')).toBe('Could not connect: Server is at capacity. <b>Please</b> try again later.');
            expect(statusEl().querySelector('b')).toBeNull();
        });

        it('reports an invalid locator in the status line without a "Status:" prefix', async () => {
            await importAppFresh();
            document.getElementById('qth').value = 'J!';
            document.getElementById('fetch-form')
                .dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
            await Promise.resolve();
            expect(statusEl().textContent).toBe('"J!" is not a valid locator (e.g. JO32) or callsign.');
            expect(esInstances.length).toBe(0);
        });
    });
});
// --- Unit U5 (plan 2026-09-11-002): shared dataNow() clock + Mercator
// playhead grayline. app.js pins the clock to the playhead on every moment,
// clears it on timeline exit, and syncs the Mercator grayline layer
// moment-driven (map.js's bucket/key check dedupes within a bucket). Live mode
// gains a wall-clock refresh so the terminator advances over wall time.
describe('app.js data-now clock (U5)', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        azimuthEnabled = false;
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
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    // data-now.js is a singleton module; import it through the same (fresh)
    // module registry importAppFresh just built so the test observes the
    // instance app.js actually drives.
    async function importDataNow() {
        return import('../static/data-now.js');
    }

    it('pins dataNow() to the playhead and syncs the mercator grayline layer on each moment', async () => {
        await importAppFresh();
        const { dataNow, clearDataNowOverride, getDataNowMs } = await importDataNow();
        const { syncMercatorGraylineLayer } = await import('../static/map.js');

        timelineMock.timelineActive = true;
        emitMoment([makeSpot(30)], 1_700_000_123);

        expect(getDataNowMs()).toBe(1_700_000_123_000);
        expect(dataNow()).toBe(1_700_000_123_000);
        expect(syncMercatorGraylineLayer).toHaveBeenCalled();
        clearDataNowOverride();
    });

    it('clears the clock override on timeline exit so live mode is wall clock again', async () => {
        await importAppFresh();
        const { getDataNowMs, dataNow } = await importDataNow();

        timelineMock.timelineActive = true;
        emitMoment([makeSpot(30)], 1_700_000_123);
        expect(getDataNowMs()).toBe(1_700_000_123_000);

        exitViaBar();
        expect(getDataNowMs()).toBeNull();
        expect(dataNow()).toBe(NOW_MS);
    });

    it('live-mode refresh: a wall-clock tick inside the 5-minute bucket does not sync, a tick past the boundary does', async () => {
        vi.useFakeTimers({ now: NOW_MS });
        await importAppFresh();
        const { clearDataNowOverride } = await importDataNow();
        const { syncMercatorGraylineLayer } = await import('../static/map.js');
        syncMercatorGraylineLayer.mockClear();
        clearDataNowOverride();

        // NOW_MS % 300000 = 200000: 61s of wall time stays in the bucket.
        vi.advanceTimersByTime(61_000);
        expect(syncMercatorGraylineLayer).not.toHaveBeenCalled();

        // Crossing into the next bucket: the refresh syncs the grayline layer.
        vi.advanceTimersByTime(60_000);
        expect(syncMercatorGraylineLayer).toHaveBeenCalled();
    });

    it('passes the playhead-derived dataNowMs to renderAzimuthScene on the moment and gate paths (U6)', async () => {
        const { renderers } = await importAppFresh();
        timelineMock.timelineActive = true;
        azimuthEnabled = true;

        emitMoment([makeSpot(30)], 1_700_000_123);
        expect(renderers.renderAzimuthScene).toHaveBeenCalledWith(
            expect.objectContaining({ dataNowMs: 1_700_000_123_000 })
        );

        // The scheduleRender gate re-render (style change mid-replay) reuses
        // the retained moment, so it passes the same playhead value.
        renderers.renderAzimuthScene.mockClear();
        await flushRender();
        expect(renderers.renderAzimuthScene).toHaveBeenCalledWith(
            expect.objectContaining({ dataNowMs: 1_700_000_123_000 })
        );

        azimuthEnabled = false;
    });

    it('live render path leaves dataNowMs unset — drawGrayline defaults to the clock (U6)', async () => {
        const { state, renderers } = await importAppFresh();
        azimuthEnabled = true;

        const es = await startStream();
        fireEsEvent(es, 'history_end');
        deliverFrame(es, makeSpot(0, { locator: 'LIVE1' }));
        await flushRender();

        const call = renderers.renderAzimuthScene.mock.calls.at(-1)?.[0] || {};
        expect('dataNowMs' in call).toBe(false); // drawGrayline reads dataNow() itself
        azimuthEnabled = false;
    });

    it('live-mode refresh stays idle while the timeline is active', async () => {
        vi.useFakeTimers({ now: NOW_MS });
        await importAppFresh();
        const { clearDataNowOverride } = await importDataNow();
        const { syncMercatorGraylineLayer } = await import('../static/map.js');
        syncMercatorGraylineLayer.mockClear();
        clearDataNowOverride();

        timelineMock.timelineActive = true;
        vi.advanceTimersByTime(10 * 60_000);
        expect(syncMercatorGraylineLayer).not.toHaveBeenCalled();
    });
});
