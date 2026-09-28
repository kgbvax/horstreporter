import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

// almanac.js (U7) — the Almanac panel: region rows of band lanes ("opened N
// of M days" per 30-min UTC slot), a UTC now line, the schedule above the
// lanes, and the area header. Fixtures mirror the BACKEND /api/almanac JSON
// (snake_case, see docs/api.md) so a naming mismatch surfaces here.
import { initAlmanac, scheduleHtml, __test } from '../static/almanac.js';

const { runtime, reset, openDrilldown, PANEL_ID, TOGGLE_ID, BODY_ID, ENABLE_KEY } = __test;

const REGIONS = ['EU', 'NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN'];
const IN_SCOPE = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m'];

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => { store.set(String(key), String(value)); },
        removeItem: (key) => { store.delete(key); },
        clear: () => { store.clear(); },
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    return mock;
}

function setupDom(qth = 'JO32') {
    document.body.innerHTML = `
        <form id="fetch-form"><input id="qth" value="${qth}" /></form>
        <div id="map-stack">
            <div id="map-toggles"><button id="${TOGGLE_ID}"></button></div>
            <div id="${PANEL_ID}" class="almanac-window is-hidden">
                <div class="almanac-window-header"><span id="almanac-title">Typical openings from your area</span></div>
                <div id="${BODY_ID}"></div>
            </div>
        </div>
    `;
}

function zeros() { return new Array(48).fill(0); }

// One lane per (in-scope band × region), all known (m = 30) and closed
// unless patched via `patch(lane)`.
function makePayload({ grid4 = 'JO32', source = 'locator', approximate = false, radius = 1,
    bands = IN_SCOPE, lanes, agenda = [], patch } = {}) {
    const out = [];
    if (lanes) {
        out.push(...lanes);
    } else {
        for (const band of bands) {
            for (const region of REGIONS) {
                const lane = { band, region, n: zeros(), m: new Array(48).fill(30), open_today: false };
                if (patch) patch(lane);
                out.push(lane);
            }
        }
    }
    const squares = radius === 0 ? [grid4] : Array.from({ length: 9 }, (_, i) => `${grid4}${i}`);
    return {
        qth: grid4,
        area: { grid4, source, approximate, radius, squares },
        window: { start_day: '2026-08-29', end_day: '2026-09-27', days: 30, start_day_index: 20694, end_day_index: 20723 },
        slot_minutes: 30, m_min: 10, k: 2, usually_share: 0.5,
        watermark_day: 20721, now_slot: 25,
        generated_at: 1790000000, today_as_of: 1790000000,
        lanes: out,
        agenda,
    };
}

function agendaEntry({ band = '20m', region = 'NA', start_slot = 26, len_slots = 10, start = '13:00', end = '18:00',
    crosses_midnight = false, all_day = false, status = 'upcoming', starts_in_min = 30,
    peak_slot = 27, peak_n = 24, peak_m = 30, open_today = false } = {}) {
    return { band, region, start_slot, len_slots, start, end, crosses_midnight, all_day, status,
        starts_in_min, peak_slot, peak_n, peak_m, open_today };
}

function response(payload, status = 200) {
    return { ok: status >= 200 && status < 300, status, json: async () => payload };
}

function mockFetchOnce(payload, status = 200) {
    global.fetch = vi.fn(async () => response(payload, status));
}

// A fetch whose responses are resolved by hand, in any order.
function deferredFetch() {
    const pending = [];
    global.fetch = vi.fn((url) => new Promise((resolve) => { pending.push({ url, resolve }); }));
    return pending;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

async function openPanel() {
    initAlmanac();
    document.getElementById(TOGGLE_ID).click();
    await flush();
    await flush();
}

function body() { return document.getElementById(BODY_ID); }

// Stubs the browser's UTC offset: `offset` is minutes EAST of UTC (+120 =
// CEST), or a function (date) => minutes for zone rules (DST). Install AFTER
// vi.useFakeTimers so the spy sits on the (possibly faked) Date prototype.
function stubOffset(offset) {
    return vi.spyOn(Date.prototype, 'getTimezoneOffset').mockImplementation(function stubbed() {
        const east = typeof offset === 'function' ? offset(this) : offset;
        return east === 0 ? 0 : -east;
    });
}

describe('almanac panel (U7)', () => {
    let store;
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        store = installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
        stubOffset(0);
    });

    afterEach(() => {
        reset();
        global.fetch = originalFetch;
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it('toggle shows/hides the panel, persists the state and fetches /api/almanac for the QTH', async () => {
        mockFetchOnce(makePayload());
        initAlmanac();
        const panel = document.getElementById(PANEL_ID);
        const toggle = document.getElementById(TOGGLE_ID);
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(toggle.getAttribute('aria-pressed')).toBe('false');

        toggle.click();
        await flush();
        expect(panel.classList.contains('is-hidden')).toBe(false);
        expect(store.getItem(ENABLE_KEY)).toBe('true');
        expect(toggle.getAttribute('aria-pressed')).toBe('true');
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(global.fetch.mock.calls[0][0]).toBe('/api/almanac?qth=JO32');

        toggle.click();
        expect(panel.classList.contains('is-hidden')).toBe(true);
        expect(store.getItem(ENABLE_KEY)).toBe('false');
    });

    it('restores the open state from localStorage', async () => {
        mockFetchOnce(makePayload());
        store.setItem(ENABLE_KEY, 'true');
        initAlmanac();
        await flush();
        expect(document.getElementById(PANEL_ID).classList.contains('is-hidden')).toBe(false);
        expect(global.fetch).toHaveBeenCalledTimes(1);
    });

    it('JO32: renders 11 region rows with EU last and one labelled lane per band', async () => {
        mockFetchOnce(makePayload());
        await openPanel();
        const rows = Array.from(body().querySelectorAll('.almanac-region'));
        expect(rows.map((r) => r.getAttribute('data-region'))).toEqual(
            ['NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR', 'AN', 'EU']);
        for (const row of rows) {
            const lanes = Array.from(row.querySelectorAll('.almanac-lane'));
            expect(lanes.map((l) => l.getAttribute('data-band'))).toEqual(IN_SCOPE);
            expect(Array.from(row.querySelectorAll('.almanac-lane-label')).map((l) => l.textContent)).toEqual(IN_SCOPE);
        }
    });

    it('a non-EU area puts its own region last', async () => {
        mockFetchOnce(makePayload({ grid4: 'FN31', bands: ['20m'] }));
        await openPanel();
        const order = Array.from(body().querySelectorAll('.almanac-region')).map((r) => r.getAttribute('data-region'));
        expect(order).toHaveLength(11);
        expect(order[order.length - 1]).toBe('NA');
        expect(order[0]).toBe('EU');
    });

    it('bands are ordered low to high regardless of payload order', async () => {
        mockFetchOnce(makePayload({ bands: ['10m', '40m', '20m'] }));
        await openPanel();
        const lanes = Array.from(body().querySelector('.almanac-region').querySelectorAll('.almanac-lane'));
        expect(lanes.map((l) => l.getAttribute('data-band'))).toEqual(['40m', '20m', '10m']);
    });

    it('an open slot reads "opened N of M days"; an unknown slot reads "not enough data (M days)" and is hatched', async () => {
        mockFetchOnce(makePayload({
            bands: ['20m', '10m'],
            patch(lane) {
                if (lane.band === '20m' && lane.region === 'NA') lane.n[28] = 24;
                if (lane.band === '10m') lane.m[10] = 6;
            },
        }));
        await openPanel();
        const open = body().querySelector('[aria-label="20m to NA - North America, 14:00 local: opened 24 of 30 days"]');
        expect(open).not.toBeNull();
        expect(open.getAttribute('title')).toBe('20m to NA - North America, 14:00 local: opened 24 of 30 days');
        expect(Number(open.style.opacity || getOpacity(open))).toBeGreaterThan(0.5);

        const unknown = body().querySelector('[aria-label="10m to KH6 - Hawaii, 05:00 local: not enough data (6 days)"]');
        expect(unknown).not.toBeNull();
        expect(unknown.classList.contains('is-unknown')).toBe(true);
        // Unknown is never drawn in the band colour (distinct from closed).
        expect(unknown.style.background || '').toBe('');
    });

    it('adjacent slots with the same reading collapse into one run with a time range', async () => {
        mockFetchOnce(makePayload({
            bands: ['20m'],
            patch(lane) {
                if (lane.region === 'NA') { lane.n[26] = 24; lane.n[27] = 24; lane.n[28] = 24; }
            },
        }));
        await openPanel();
        expect(body().querySelector('[aria-label="20m to NA - North America, 13:00–14:30 local: opened 24 of 30 days"]')).not.toBeNull();
        const naLane = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"]');
        // closed 00:00–13:00, open 13:00–14:30, closed 14:30–24:00
        expect(naLane.querySelectorAll('.almanac-run')).toHaveLength(3);
    });

    it('draws a now line and a legend (ramp, unknown hatch, now line)', async () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date('2026-09-28T12:30:00Z'));
        stubOffset(0);
        mockFetchOnce(makePayload({ bands: ['20m'] }));
        await openPanel();
        const now = body().querySelector('.almanac-now');
        expect(now).not.toBeNull();
        expect(now.style.left).toBe('52.0833%');
        const legend = body().querySelector('.almanac-legend');
        expect(legend.textContent).toContain('not enough data');
        expect(legend.textContent).toMatch(/now/i);
        expect(legend.querySelector('.almanac-legend-ramp')).not.toBeNull();
        expect(legend.querySelector('.almanac-legend-unknown')).not.toBeNull();
    });

    it('clicking a lane calls the drill-down hook with band and region', async () => {
        mockFetchOnce(makePayload({ bands: ['20m'] }));
        await openPanel();
        body().querySelector('.almanac-lane[data-band="20m"][data-region="JA"] .almanac-run').click();
        expect(runtime.drilldown).toMatchObject({ band: '20m', region: 'JA' });
        openDrilldown('40m', 'NA');
        expect(runtime.drilldown).toMatchObject({ band: '40m', region: 'NA' });
    });

    it('Enter on a focused lane opens the drill-down; arrows move the tab stop', async () => {
        mockFetchOnce(makePayload({ bands: ['20m', '40m'] }));
        await openPanel();
        const lanes = Array.from(body().querySelectorAll('.almanac-lane'));
        expect(lanes.filter((l) => l.getAttribute('tabindex') === '0')).toHaveLength(1);
        lanes[0].focus();
        lanes[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
        expect(document.activeElement).toBe(lanes[1]);
        expect(lanes[1].getAttribute('tabindex')).toBe('0');
        lanes[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        expect(runtime.drilldown).toMatchObject({ band: lanes[1].dataset.band, region: lanes[1].dataset.region });
    });

    describe('schedule', () => {
        // 12:30 UTC (slot 25), offset 0 (stubOffset(0) in beforeEach).
        const NOW = new Date('2026-09-28T12:30:00Z');
        const render = (agenda, extra = {}) => {
            const host = document.createElement('div');
            host.innerHTML = scheduleHtml({ slot_minutes: 30, now_slot: 25, agenda, ...extra }, NOW);
            return host;
        };
        const ids = (host) => Array.from(host.querySelectorAll('.cond-sched-row')).map((r) => `${r.dataset.band}-${r.dataset.region}`);

        it('groups open-now first (longest running first), then upcoming by start; drops what starts after 3 h', () => {
            const host = render([
                agendaEntry({ band: '15m', region: 'AS', start_slot: 40, len_slots: 2, starts_in_min: 200 }),
                agendaEntry({ band: '17m', region: 'AF', start_slot: 30, len_slots: 2, starts_in_min: 150 }),
                agendaEntry({ band: '20m', region: 'SA', start_slot: 28, len_slots: 4, starts_in_min: 90 }),
                agendaEntry({ band: '20m', region: 'JA', start_slot: 20, len_slots: 14, status: 'ongoing', starts_in_min: 0, open_today: true }),
                agendaEntry({ band: '30m', region: 'EU', start_slot: 16, len_slots: 12, status: 'ongoing', starts_in_min: 0 }),
            ]);
            expect(ids(host)).toEqual(['30m-EU', '20m-JA', '20m-SA', '17m-AF']);
            expect(Array.from(host.querySelectorAll('.almanac-subhead')).map((h) => h.textContent))
                .toEqual(['Open now, usually', 'Opens within 3 h']);
        });

        it('places bars on the 3 h axis: outlined when open now, cut when the window runs past it', () => {
            const host = render([
                // 08:00-14:00 UTC: 90 min left -> half the axis, not cut.
                agendaEntry({ band: '30m', region: 'EU', start_slot: 16, len_slots: 12, status: 'ongoing', starts_in_min: 0 }),
                // 10:00-17:00 UTC: runs past the axis.
                agendaEntry({ band: '20m', region: 'JA', start_slot: 20, len_slots: 14, status: 'ongoing', starts_in_min: 0 }),
                // Starts in 90 min for 2 h: right half, cut.
                agendaEntry({ band: '20m', region: 'SA', start_slot: 28, len_slots: 4, starts_in_min: 90 }),
            ]);
            const bar = (id) => host.querySelector(`.cond-sched-row[data-band="${id[0]}"][data-region="${id[1]}"] .cond-sched-bar`);
            const eu = bar(['30m', 'EU']);
            expect(eu.classList.contains('is-now')).toBe(true);
            expect(eu.classList.contains('is-cut')).toBe(false);
            expect(eu.style.left).toBe('0%');
            expect(eu.style.width).toBe('50%');
            const ja = bar(['20m', 'JA']);
            expect(ja.classList.contains('is-cut')).toBe(true);
            expect(ja.style.width).toBe('100%');
            const sa = bar(['20m', 'SA']);
            expect(sa.classList.contains('is-now')).toBe(false);
            expect(sa.classList.contains('is-cut')).toBe(true);
            expect(sa.style.left).toBe('50%');
            expect(sa.style.width).toBe('50%');
        });

        it('shows "n of m" and shades the bar by n/m in the band colour', () => {
            const host = render([agendaEntry({ band: '20m', region: 'NA', peak_n: 24, peak_m: 30, starts_in_min: 30 })]);
            expect(host.querySelector('.cond-sched-n').textContent).toBe('24 of 30');
            expect(host.querySelector('.cond-sched-bar .almanac-run').style.opacity).toBe('0.83');
        });

        it('says so when nothing opens in the next 3 h', () => {
            const host = render([agendaEntry({ starts_in_min: 240 })]);
            expect(host.textContent).toContain('No usual openings in the next 3 h');
            expect(host.querySelector('.cond-sched-row')).toBeNull();
            expect(render([]).textContent).toContain('No usual openings in the next 3 h');
        });

        it('an all-day window fills the axis and says so in its title', () => {
            const host = render([agendaEntry({ band: '40m', region: 'EU', start_slot: 0, len_slots: 48, start: '00:00', end: '00:00', all_day: true, status: 'ongoing', starts_in_min: 0 })]);
            const bar = host.querySelector('.cond-sched-bar');
            expect(bar.style.width).toBe('100%');
            expect(bar.classList.contains('is-cut')).toBe(false);
            expect(host.querySelector('.cond-sched-row').title).toContain('usually all day');
        });

        it('renders into the panel', async () => {
            mockFetchOnce(makePayload({ bands: ['40m'], agenda: [
                agendaEntry({ band: '40m', region: 'EU', start_slot: 0, len_slots: 48, all_day: true, status: 'ongoing', starts_in_min: 0 }),
            ] }));
            await openPanel();
            expect(body().querySelectorAll('.almanac-schedule .cond-sched-row')).toHaveLength(1);
            expect(body().querySelector('.almanac-agenda')).toBeNull();
        });
    });

    describe('header', () => {
        it('shows the area square and radius', async () => {
            mockFetchOnce(makePayload({ radius: 1 }));
            await openPanel();
            const head = body().querySelector('.almanac-area');
            expect(head.textContent).toContain('JO32');
            expect(head.textContent).toContain('radius 1');
            expect(head.textContent).not.toMatch(/approximate/i);
        });

        it('source=dxcc shows the approximate-location note', async () => {
            mockFetchOnce(makePayload({ source: 'dxcc', approximate: true, radius: 1 }));
            await openPanel();
            const head = body().querySelector('.almanac-area');
            expect(head.textContent).toContain('radius 1');
            expect(head.textContent).toMatch(/approximate location/i);
        });
    });

    describe('states', () => {
        it('asks for a locator when no QTH is set, without fetching', async () => {
            mockFetchOnce(makePayload());
            document.getElementById('qth').value = '';
            await openPanel();
            expect(body().textContent).toContain('Enter your locator');
            expect(global.fetch).not.toHaveBeenCalled();
        });

        it('shows "Loading" while the first request is in flight', async () => {
            deferredFetch();
            await openPanel();
            expect(body().textContent).toContain('Loading');
            expect(body().getAttribute('aria-busy')).toBe('true');
        });

        it('dims the lanes and shows "Loading" while a refetch is in flight', async () => {
            const pending = deferredFetch();
            await openPanel();
            pending[0].resolve(response(makePayload({ bands: ['20m'] })));
            await flush();
            expect(body().querySelector('.almanac-lanes').classList.contains('is-loading')).toBe(false);

            const qth = document.getElementById('qth');
            qth.value = 'JO62';
            qth.dispatchEvent(new Event('change'));
            await flush();
            expect(body().querySelector('.almanac-lanes').classList.contains('is-loading')).toBe(true);
            expect(body().querySelector('.almanac-status').textContent).toContain('Loading');

            pending[1].resolve(response(makePayload({ grid4: 'JO62', bands: ['20m'] })));
            await flush();
            expect(body().querySelector('.almanac-lanes').classList.contains('is-loading')).toBe(false);
            expect(body().getAttribute('aria-busy')).toBe('false');
            expect(body().querySelector('.almanac-area').textContent).toContain('JO62');
        });

        it('drops a stale response after a #qth change mid-fetch', async () => {
            const pending = deferredFetch();
            await openPanel();
            const qth = document.getElementById('qth');
            qth.value = 'FN31';
            qth.dispatchEvent(new Event('change'));
            await flush();
            expect(pending).toHaveLength(2);
            expect(pending[1].url).toBe('/api/almanac?qth=FN31');

            // New response lands first; the old one arrives late and must be ignored.
            pending[1].resolve(response(makePayload({ grid4: 'FN31', bands: ['20m'] })));
            await flush();
            pending[0].resolve(response(makePayload({ grid4: 'JO32', bands: ['20m'] })));
            await flush();
            expect(body().querySelector('.almanac-area').textContent).toContain('FN31');
            expect(body().querySelector('.almanac-area').textContent).not.toContain('JO32');
        });

        it('a late stale response never replaces the loading state for the new QTH', async () => {
            const pending = deferredFetch();
            await openPanel();
            const qth = document.getElementById('qth');
            qth.value = 'FN31';
            qth.dispatchEvent(new Event('change'));
            await flush();
            pending[0].resolve(response(makePayload({ grid4: 'JO32', bands: ['20m'] })));
            await flush();
            expect(body().querySelector('.almanac-area')).toBeNull();
            expect(body().textContent).toContain('Loading');
        });

        it('400 reads as an invalid QTH', async () => {
            mockFetchOnce({}, 400);
            await openPanel();
            expect(body().textContent).toMatch(/invalid qth/i);
        });

        it('404 says the callsign could not be located', async () => {
            document.getElementById('qth').value = 'XX9XYZ';
            mockFetchOnce({}, 404);
            await openPanel();
            expect(body().textContent).toMatch(/could not locate XX9XYZ/i);
        });

        it('no lanes reads "no data from this area yet"', async () => {
            mockFetchOnce(makePayload({ lanes: [] }));
            await openPanel();
            expect(body().textContent).toMatch(/no data from this area yet/i);
            expect(body().querySelector('.almanac-lane')).toBeNull();
        });

        it('503 shows "temporarily unavailable" with no stale lanes left on screen', async () => {
            const pending = deferredFetch();
            await openPanel();
            pending[0].resolve(response(makePayload({ bands: ['20m'] })));
            await flush();
            expect(body().querySelectorAll('.almanac-lane').length).toBeGreaterThan(0);

            const qth = document.getElementById('qth');
            qth.value = 'JO62';
            qth.dispatchEvent(new Event('change'));
            await flush();
            pending[1].resolve(response({}, 503));
            await flush();
            expect(body().textContent).toMatch(/temporarily unavailable/i);
            expect(body().querySelector('.almanac-lane')).toBeNull();
            expect(body().querySelector('.almanac-agenda')).toBeNull();
        });

        it('a quiet same-QTH refresh that gets a 503 keeps the lanes and says it could not refresh', async () => {
            const pending = deferredFetch();
            await openPanel();
            pending[0].resolve(response(makePayload({ bands: ['20m'] })));
            await flush();
            const before = body().querySelectorAll('.almanac-lane').length;
            expect(before).toBeGreaterThan(0);

            __test.fetchAlmanac({ quiet: true });
            await flush();
            pending[1].resolve(response({}, 503));
            await flush();
            expect(body().querySelectorAll('.almanac-lane')).toHaveLength(before);
            expect(body().textContent).not.toMatch(/temporarily unavailable/i);
            expect(body().querySelector('.almanac-status').textContent).toMatch(/couldn't refresh; showing last data/i);
            expect(body().getAttribute('aria-busy')).toBe('false');
            expect(runtime.cache).not.toBeNull();
            expect(runtime.loading).toBe(false);

            // The next good refresh clears the notice.
            __test.fetchAlmanac({ quiet: true });
            await flush();
            pending[2].resolve(response(makePayload({ bands: ['20m'] })));
            await flush();
            expect(body().querySelector('.almanac-status').textContent).toBe('');
        });

        it('a quiet same-QTH refresh that fails on the network keeps the lanes', async () => {
            vi.spyOn(console, 'warn').mockImplementation(() => {});
            mockFetchOnce(makePayload({ bands: ['20m'] }));
            await openPanel();
            const before = body().querySelectorAll('.almanac-lane').length;
            global.fetch = vi.fn(async () => { throw new Error('offline'); });
            await __test.fetchAlmanac({ quiet: true });
            expect(body().querySelectorAll('.almanac-lane')).toHaveLength(before);
            expect(body().querySelector('.almanac-status').textContent).toMatch(/couldn't refresh/i);
        });

        it('a non-quiet same-QTH fetch failure still shows the error', async () => {
            mockFetchOnce(makePayload({ bands: ['20m'] }));
            await openPanel();
            mockFetchOnce({}, 503);
            await __test.fetchAlmanac();
            expect(body().textContent).toMatch(/temporarily unavailable/i);
            expect(body().querySelector('.almanac-lane')).toBeNull();
        });

        it('submitting #fetch-form with a new QTH (no change event) refetches for it', async () => {
            const pending = deferredFetch();
            await openPanel();
            pending[0].resolve(response(makePayload({ bands: ['20m'] })));
            await flush();
            document.getElementById('qth').value = 'FN31';
            const ev = new Event('submit', { cancelable: true, bubbles: true });
            document.getElementById('fetch-form').dispatchEvent(ev);
            await flush();
            expect(ev.defaultPrevented).toBe(false);
            expect(pending).toHaveLength(2);
            expect(pending[1].url).toBe('/api/almanac?qth=FN31');

            // Same QTH again: no extra fetch.
            document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
            await flush();
            expect(pending).toHaveLength(2);
        });

        it('works without a #fetch-form', async () => {
            document.getElementById('fetch-form').replaceWith(document.getElementById('qth'));
            mockFetchOnce(makePayload({ bands: ['20m'] }));
            await openPanel();
            expect(body().querySelectorAll('.almanac-lane').length).toBeGreaterThan(0);
        });

        it('a network failure also reads as temporarily unavailable', async () => {
            vi.spyOn(console, 'warn').mockImplementation(() => {});
            global.fetch = vi.fn(async () => { throw new Error('offline'); });
            await openPanel();
            expect(body().textContent).toMatch(/temporarily unavailable/i);
        });
    });
});

function getOpacity(el) {
    const m = (el.getAttribute('style') || '').match(/opacity:\s*([\d.]+)/);
    return m ? m[1] : '0';
}

// --- U8: seasonal drill-down (month × hour) ------------------------------------
// Fixtures mirror GET /api/almanac/season (docs/api.md): always 12 months,
// index 0 = January, each either status "ok" (year + layer pskr|wspr, n[48],
// m[48]) or "not_collected".

const MONTH_NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function seasonMonth(i, { status = 'ok', year = 2026, layer = 'pskr', patch } = {}) {
    if (status !== 'ok') {
        return { month: i + 1, name: MONTH_NAMES[i], status: 'not_collected', year: null, layer: null,
            days: 0, k: null, n: null, m: null };
    }
    const mo = { month: i + 1, name: MONTH_NAMES[i], status: 'ok', year, layer,
        days: 20, k: layer === 'wspr' ? 1 : 2, n: zeros(), m: new Array(48).fill(20) };
    if (patch) patch(mo);
    return mo;
}

// Jan–Mar never collected; Apr–Sep 2026 PSKR; Oct–Dec 2025 backfilled WSPR.
function makeSeason({ band = '20m', region = 'OC', grid4 = 'JO32', months } = {}) {
    return {
        qth: grid4,
        area: { grid4, source: 'locator', approximate: false, radius: 1, squares: [grid4] },
        band, region, slot_minutes: 30, m_min: 8, k: { pskr: 2, wspr: 1 },
        through_day: '2026-09-27', watermark_day: 20721,
        months: months || MONTH_NAMES.map((_, i) => {
            if (i < 3) return seasonMonth(i, { status: 'not_collected' });
            if (i < 9) return seasonMonth(i, { year: 2026, layer: 'pskr', patch(mo) {
                if (i === 8) { mo.n[28] = 15; mo.m[5] = 3; }
            } });
            return seasonMonth(i, { year: 2025, layer: 'wspr' });
        }),
    };
}

// Landing and season requests answered by URL.
function routeFetch({ landing = makePayload({ bands: ['20m', '40m'] }), season = makeSeason(), seasonStatus = 200 } = {}) {
    global.fetch = vi.fn(async (url) => (String(url).startsWith('/api/almanac/season')
        ? response(season, seasonStatus)
        : response(landing)));
}

function lane(band, region) {
    return body().querySelector(`.almanac-lane[data-band="${band}"][data-region="${region}"]`);
}

function monthRows() {
    return Array.from(body().querySelectorAll('.almanac-drill .almanac-month'));
}

describe('almanac seasonal drill-down (U8)', () => {
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date('2026-09-28T12:30:00Z'));
        stubOffset(0);
    });

    afterEach(() => {
        reset();
        global.fetch = originalFetch;
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    async function openOC() {
        await openPanel();
        lane('20m', 'OC').click();
        await flush();
        await flush();
    }

    it('clicking a lane fetches /api/almanac/season and replaces the lanes with "<band> to <region>"', async () => {
        routeFetch();
        await openOC();
        expect(global.fetch.mock.calls.map((c) => c[0])).toContain('/api/almanac/season?qth=JO32&band=20m&region=OC');
        const drill = body().querySelector('.almanac-drill');
        expect(drill).not.toBeNull();
        expect(drill.querySelector('.almanac-drill-title').textContent).toBe('20m to OC - Oceania');
        expect(body().querySelector('.almanac-lanes')).toBeNull();
        expect(body().querySelector('.almanac-drill-back')).not.toBeNull();
        // The area header stays.
        expect(body().querySelector('.almanac-area').textContent).toContain('JO32');
    });

    it('mixed PSKR and WSPR months render both layer labels with the year', async () => {
        routeFetch();
        await openOC();
        const labels = monthRows().map((r) => r.querySelector('.almanac-month-label').textContent);
        expect(labels).toContain('Sep 2026 · PSKR');
        expect(labels).toContain('Dec 2025 · WSPR');
        expect(body().querySelector('.almanac-drill .almanac-legend').textContent).toMatch(/not directly comparable/i);
    });

    it('an empty month renders "not collected yet"', async () => {
        routeFetch();
        await openOC();
        const jan = monthRows()[0];
        expect(jan.classList.contains('is-empty')).toBe(true);
        expect(jan.textContent).toContain('not collected yet');
        expect(jan.querySelector('.almanac-run')).toBeNull();
        expect(monthRows()[3].textContent).not.toContain('not collected yet');
    });

    it('runs Jan to Dec and highlights the current month', async () => {
        routeFetch();
        await openOC();
        const rows = monthRows();
        expect(rows).toHaveLength(12);
        expect(rows.map((r) => r.dataset.month)).toEqual(MONTH_NAMES.map((_, i) => String(i + 1)));
        expect(rows.map((r) => r.querySelector('.almanac-month-label').textContent.slice(0, 3))).toEqual(MONTH_NAMES);
        const current = rows.filter((r) => r.classList.contains('is-current'));
        expect(current).toHaveLength(1);
        expect(current[0].dataset.month).toBe('9');
        expect(current[0].getAttribute('aria-current')).toBe('date');
    });

    it('slot cells carry opened-N-of-M labels, the band colour and the unknown hatch; the now line carries over', async () => {
        routeFetch();
        await openOC();
        const sep = monthRows()[8];
        const open = sep.querySelector('[aria-label="20m to OC - Oceania, Sep 2026, 14:00 local: opened 15 of 20 days"]');
        expect(open).not.toBeNull();
        expect(open.getAttribute('title')).toBe(open.getAttribute('aria-label'));
        expect(Number(getOpacity(open))).toBeGreaterThan(0.5);
        const unknown = sep.querySelector('[aria-label="20m to OC - Oceania, Sep 2026, 02:30 local: not enough data (3 days)"]');
        expect(unknown).not.toBeNull();
        expect(unknown.classList.contains('is-unknown')).toBe(true);
        const now = body().querySelector('.almanac-drill .almanac-now');
        expect(now).not.toBeNull();
        expect(now.style.left).toBe('52.0833%');
    });

    it('shows "Loading" while the season fetch is in flight', async () => {
        const pending = deferredFetch();
        await openPanel();
        pending[0].resolve(response(makePayload({ bands: ['20m'] })));
        await flush();
        lane('20m', 'OC').click();
        await flush();
        expect(pending).toHaveLength(2);
        const drill = body().querySelector('.almanac-drill');
        expect(drill.querySelector('.almanac-drill-title').textContent).toBe('20m to OC - Oceania');
        expect(drill.textContent).toContain('Loading');
        expect(drill.querySelector('.almanac-month')).toBeNull();
    });

    it('a 503 reads "temporarily unavailable"', async () => {
        routeFetch({ season: {}, seasonStatus: 503 });
        await openOC();
        const drill = body().querySelector('.almanac-drill');
        expect(drill.textContent).toMatch(/temporarily unavailable/i);
        expect(drill.querySelector('.almanac-month')).toBeNull();
        expect(drill.querySelector('.almanac-drill-back')).not.toBeNull();
    });

    it('any other failure shows an error message', async () => {
        routeFetch({ season: {}, seasonStatus: 500 });
        await openOC();
        expect(body().querySelector('.almanac-drill').textContent).toMatch(/could not load the seasonal view/i);
    });

    it('the back control closes the view, restores the lanes and returns focus to the opening lane', async () => {
        routeFetch();
        await openOC();
        body().querySelector('.almanac-drill-back').click();
        expect(body().querySelector('.almanac-drill')).toBeNull();
        expect(body().querySelector('.almanac-lanes')).not.toBeNull();
        expect(runtime.drilldown).toBeNull();
        expect(document.activeElement).toBe(lane('20m', 'OC'));
        expect(lane('20m', 'OC').getAttribute('tabindex')).toBe('0');
    });

    it('Escape closes the view and restores the lanes', async () => {
        routeFetch();
        await openOC();
        const back = body().querySelector('.almanac-drill-back');
        expect(document.activeElement).toBe(back);
        back.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        expect(body().querySelector('.almanac-drill')).toBeNull();
        expect(body().querySelectorAll('.almanac-lane').length).toBeGreaterThan(0);
        expect(document.activeElement).toBe(lane('20m', 'OC'));
    });

    it('a QTH change closes the view', async () => {
        routeFetch();
        await openOC();
        expect(body().querySelector('.almanac-drill')).not.toBeNull();
        routeFetch({ landing: makePayload({ grid4: 'JO62', bands: ['20m'] }) });
        const qth = document.getElementById('qth');
        qth.value = 'JO62';
        qth.dispatchEvent(new Event('change'));
        await flush();
        await flush();
        expect(runtime.drilldown).toBeNull();
        expect(body().querySelector('.almanac-drill')).toBeNull();
        expect(body().querySelector('.almanac-area').textContent).toContain('JO62');
        expect(body().querySelector('.almanac-lanes')).not.toBeNull();
    });

    it('a quiet same-QTH refresh that fails keeps the open drill-down', async () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        routeFetch();
        await openOC();
        global.fetch = vi.fn(async () => response({}, 503));
        await __test.fetchAlmanac({ quiet: true });
        expect(runtime.drilldown).toMatchObject({ band: '20m', region: 'OC' });
        expect(body().querySelector('.almanac-drill-title').textContent).toBe('20m to OC - Oceania');
        expect(monthRows()).toHaveLength(12);
        expect(body().querySelector('.almanac-status').textContent).toMatch(/couldn't refresh/i);

        global.fetch = vi.fn(async () => { throw new Error('offline'); });
        await __test.fetchAlmanac({ quiet: true });
        expect(runtime.drilldown).not.toBeNull();
        expect(monthRows()).toHaveLength(12);
    });

    it('a failed fetch after a QTH change still closes the view and wipes the lanes', async () => {
        routeFetch();
        await openOC();
        global.fetch = vi.fn(async () => response({}, 503));
        const qth = document.getElementById('qth');
        qth.value = 'JO62';
        qth.dispatchEvent(new Event('change'));
        await flush();
        await flush();
        expect(runtime.drilldown).toBeNull();
        expect(runtime.cache).toBeNull();
        expect(body().querySelector('.almanac-drill')).toBeNull();
        expect(body().querySelector('.almanac-lane')).toBeNull();
        expect(body().textContent).toMatch(/temporarily unavailable/i);
    });

    it('hiding the panel with the view open leaves no stale drill-down behind on show', async () => {
        routeFetch();
        await openOC();
        expect(body().querySelector('.almanac-drill')).not.toBeNull();
        const toggle = document.getElementById(TOGGLE_ID);
        toggle.click(); // hide
        expect(runtime.drilldown).toBeNull();
        expect(body().querySelector('.almanac-drill')).toBeNull();
        toggle.click(); // show again
        expect(body().querySelector('.almanac-drill')).toBeNull();
        await flush();
        await flush();
        expect(body().querySelector('.almanac-drill')).toBeNull();
        expect(body().querySelector('.almanac-lanes')).not.toBeNull();
    });

    it('drops a stale season response (closed view, or a newer lane opened)', async () => {
        const pending = deferredFetch();
        await openPanel();
        pending[0].resolve(response(makePayload({ bands: ['20m', '40m'] })));
        await flush();

        lane('20m', 'OC').click();
        await flush();
        body().querySelector('.almanac-drill-back').click();
        lane('40m', 'NA').click();
        await flush();
        expect(pending).toHaveLength(3);
        expect(pending[2].url).toBe('/api/almanac/season?qth=JO32&band=40m&region=NA');

        pending[2].resolve(response(makeSeason({ band: '40m', region: 'NA' })));
        await flush();
        pending[1].resolve(response(makeSeason({ band: '20m', region: 'OC' })));
        await flush();
        expect(body().querySelector('.almanac-drill-title').textContent).toBe('40m to NA - North America');
        expect(body().querySelector('[aria-label^="20m to OC - Oceania"]')).toBeNull();
        expect(body().querySelector('[aria-label^="40m to NA - North America, Sep 2026"]')).not.toBeNull();
    });

    it('a season response arriving after a QTH change never reopens the view', async () => {
        const pending = deferredFetch();
        await openPanel();
        pending[0].resolve(response(makePayload({ bands: ['20m'] })));
        await flush();
        lane('20m', 'OC').click();
        await flush();
        const qth = document.getElementById('qth');
        qth.value = 'JO62';
        qth.dispatchEvent(new Event('change'));
        await flush();
        pending[1].resolve(response(makeSeason()));
        await flush();
        expect(runtime.drilldown).toBeNull();
        expect(body().querySelector('.almanac-drill')).toBeNull();
    });
});

// SNR floor (plan KTD13): the page's Min SNR control drives min_snr.
describe('almanac SNR floor (KTD13)', () => {
    let originalFetch;

    function addSnrControls(mode = 'none', cw = '-15', ssb = '0') {
        const group = document.createElement('div');
        group.id = 'min-snr-group';
        group.innerHTML = ['none', 'cw', 'ssb'].map((v) =>
            `<input type="radio" name="min-snr" value="${v}" id="snr-${v}"${v === mode ? ' checked' : ''} />`).join('') +
            `<input type="range" id="cw-min-db" min="-30" max="10" value="${cw}" />` +
            `<input type="range" id="ssb-min-db" min="-30" max="10" value="${ssb}" />`;
        document.body.appendChild(group);
    }

    function snrPayload() {
        const p = makePayload({
            bands: ['20m'],
            patch(l) {
                if (l.region === 'NA') {
                    l.m = new Array(48).fill(26);
                    l.n = new Array(48).fill(0);
                    l.n[30] = 18;
                    l.share = new Array(48).fill(null);
                    l.share[30] = 0.64;
                }
            },
            agenda: [{ ...agendaEntry({ status: 'ongoing', peak_n: 18, peak_m: 26 }), peak_share: 0.64 }],
        });
        return { ...p, min_snr: -12, snr_tier: -10, snr_available: true, snr_since: '2026-09-29' };
    }

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
        stubOffset(0);
    });

    afterEach(() => {
        reset();
        global.fetch = originalFetch;
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    it('sends the active mode\'s slider value as min_snr (none sends nothing)', async () => {
        addSnrControls('cw', '-12');
        mockFetchOnce(makePayload());
        await openPanel();
        expect(global.fetch.mock.calls[0][0]).toBe('/api/almanac?qth=JO32&min_snr=-12');
        reset();
        document.getElementById('min-snr-group').remove();
        addSnrControls('ssb', '0', '3');
        mockFetchOnce(makePayload());
        await openPanel();
        expect(global.fetch.mock.calls[0][0]).toBe('/api/almanac?qth=JO32&min_snr=3');
        expect(__test.currentMinSnr()).toBe(3);
        document.getElementById('snr-none').checked = true;
        expect(__test.currentMinSnr()).toBeNull();
    });

    it('refetches on a mode switch and, debounced, on slider input', async () => {
        addSnrControls('none');
        mockFetchOnce(makePayload());
        await openPanel();
        expect(global.fetch).toHaveBeenCalledTimes(1);
        vi.useFakeTimers();

        const cw = document.getElementById('snr-cw');
        cw.checked = true;
        cw.dispatchEvent(new Event('change', { bubbles: true }));
        await vi.advanceTimersByTimeAsync(__test.SNR_DEBOUNCE_MS - 1);
        expect(global.fetch).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(1);
        expect(global.fetch).toHaveBeenCalledTimes(2);
        expect(global.fetch.mock.calls[1][0]).toBe('/api/almanac?qth=JO32&min_snr=-15');

        const slider = document.getElementById('cw-min-db');
        for (const v of ['-14', '-13', '-9']) {
            slider.value = v;
            slider.dispatchEvent(new Event('input', { bubbles: true }));
            await vi.advanceTimersByTimeAsync(100);
        }
        expect(global.fetch).toHaveBeenCalledTimes(2);
        await vi.advanceTimersByTimeAsync(__test.SNR_DEBOUNCE_MS);
        expect(global.fetch).toHaveBeenCalledTimes(3);
        expect(global.fetch.mock.calls[2][0]).toBe('/api/almanac?qth=JO32&min_snr=-9');

        // An unchanged value does not refetch.
        slider.dispatchEvent(new Event('change', { bubbles: true }));
        await vi.advanceTimersByTimeAsync(__test.SNR_DEBOUNCE_MS);
        expect(global.fetch).toHaveBeenCalledTimes(3);
    });

    it('labels the header, slot titles and schedule with the tier and share', async () => {
        addSnrControls('cw', '-12');
        mockFetchOnce(snrPayload());
        await openPanel();
        const header = body().querySelector('.almanac-snr').textContent;
        expect(header).toBe('FT8/FT4 openings — spots ≥ −10 dB (slider −12 → −10 dB tier); SNR data since 2026-09-29');
        const run = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"] [data-slot="30"]');
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 15:00 local: opened 18 of 26 days · 64% of spots ≥ −10 dB');
        expect(body().querySelector('.cond-sched-row').title).toContain('opened 18 of 26 days, 64% of spots ≥ −10 dB');
    });

    it('any SNR: plain header and slot titles; no SNR data yet is said so', () => {
        expect(__test.snrHeaderText(makePayload())).toBe('FT8/FT4 openings — any SNR');
        expect(__test.snrHeaderText({ min_snr: -10, snr_tier: -10, snr_available: false, snr_since: null }))
            .toBe('FT8/FT4 openings — spots ≥ −10 dB; no SNR data collected yet');
        expect(__test.slotLabel('20m to NA', 30, 1, 18, 26, 10, 30)).toBe('20m to NA, 15:00 local: opened 18 of 26 days');
    });

    it('the drill-down passes min_snr and refetches on a floor change', async () => {
        addSnrControls('ssb', '0', '-4');
        routeFetch({ landing: snrPayload() });
        await openPanel();
        openDrilldown('20m', 'NA');
        await flush();
        const urls = () => global.fetch.mock.calls.map((c) => c[0]);
        expect(urls()).toContain('/api/almanac/season?qth=JO32&band=20m&region=NA&min_snr=-4');
        vi.useFakeTimers();
        const slider = document.getElementById('ssb-min-db');
        slider.value = '-20';
        slider.dispatchEvent(new Event('input', { bubbles: true }));
        await vi.advanceTimersByTimeAsync(__test.SNR_DEBOUNCE_MS);
        expect(urls()).toContain('/api/almanac?qth=JO32&min_snr=-20');
        expect(urls()).toContain('/api/almanac/season?qth=JO32&band=20m&region=NA&min_snr=-20');
        expect(runtime.drilldown?.band).toBe('20m');
    });
});

// Local time: the server works in UTC slots (data-slot, API params); the
// panel shows every time in the browser's local time. Lanes are rotated by
// the UTC offset (offset / 30 min slots), drill-down month rows by that
// month's own offset (DST), the agenda leads with local and keeps UTC as the
// muted secondary label.
describe('almanac local time', () => {
    let originalFetch;

    // Central Europe: CEST (+120) from the last Sunday of March to the last
    // Sunday of October, CET (+60) otherwise — close enough by month here.
    const europeBerlin = (d) => {
        const m = d.getUTCMonth();
        return m >= 3 && m <= 9 ? 120 : 60;
    };

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date('2026-09-28T12:30:00Z'));
    });

    afterEach(() => {
        reset();
        global.fetch = originalFetch;
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    // Display position (in slots) of a run: the flex-grow of the runs before it.
    function displayStart(run) {
        let pos = 0;
        for (let el = run.previousElementSibling; el; el = el.previousElementSibling) {
            pos += Number(el.style.flexGrow) || 0;
        }
        return pos;
    }

    it('helpers: slot shift, rotation with wrap, offset label', () => {
        expect(__test.slotShift(120, 30)).toBe(4);
        expect(__test.slotShift(-300, 30)).toBe(-10);
        expect(__test.slotShift(345, 30)).toBe(12);   // +5:45 rounds to +6:00
        expect(__test.slotShift(-570, 30)).toBe(-19); // −9:30 exact
        const utc = Array.from({ length: 48 }, (_, i) => i);
        const rotated = __test.rotateSlots(utc, 4);
        expect(rotated[0]).toBe(44);   // local 00:00 = UTC 22:00
        expect(rotated[4]).toBe(0);    // local 02:00 = UTC 00:00
        expect(rotated[47]).toBe(43);
        expect(__test.rotateSlots(utc, -10)[40]).toBe(2); // UTC−5: local 20:00 = UTC 01:00
        expect(__test.offsetLabel(120)).toBe('UTC+2');
        expect(__test.offsetLabel(0)).toBe('UTC');
        expect(__test.offsetLabel(-300)).toBe('UTC−5');
        expect(__test.offsetLabel(345)).toBe('UTC+5:45');
        expect(__test.offsetLabel(-570)).toBe('UTC−9:30');
    });

    it('rotates the lanes so the axis is local midnight; titles read local time; data-slot stays UTC', async () => {
        stubOffset(120);
        mockFetchOnce(makePayload({
            bands: ['20m'],
            patch(lane) { if (lane.region === 'NA') lane.n[28] = 24; },
        }));
        await openPanel();
        const run = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"] [data-slot="28"]');
        expect(run).not.toBeNull();
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 16:00 local: opened 24 of 30 days');
        expect(run.getAttribute('aria-label')).toBe(run.getAttribute('title'));
        expect(displayStart(run)).toBe(32);
        // The leading closed run starts at local 00:00 = UTC 22:00.
        const first = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"] .almanac-run');
        expect(first.dataset.slot).toBe('44');
        expect(first.getAttribute('title')).toBe('20m to NA - North America, 00:00–16:00 local: opened 0 of 30 days');
        expect(body().querySelector('.almanac-lanes').getAttribute('aria-label')).toMatch(/local time/);
    });

    it('a UTC run across 00:00 UTC stays one run in local time (wrap)', async () => {
        stubOffset(120);
        mockFetchOnce(makePayload({
            bands: ['20m'],
            patch(lane) { if (lane.region === 'NA') { lane.n[46] = 24; lane.n[47] = 24; lane.n[0] = 24; } },
        }));
        await openPanel();
        const lane = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"]');
        const run = lane.querySelector('[data-slot="46"]');
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 01:00–02:30 local: opened 24 of 30 days');
        expect(displayStart(run)).toBe(2);
        expect(lane.querySelectorAll('.almanac-run')).toHaveLength(3);
    });

    it('west of UTC: an early-UTC slot lands on the local evening', async () => {
        stubOffset(-300);
        mockFetchOnce(makePayload({
            bands: ['20m'],
            patch(lane) { if (lane.region === 'NA') lane.n[2] = 24; },
        }));
        await openPanel();
        const run = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"] [data-slot="2"]');
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 20:00 local: opened 24 of 30 days');
        expect(displayStart(run)).toBe(40);
        // The lane's last run ends at local midnight.
        const runs = Array.from(body().querySelectorAll('.almanac-lane[data-band="20m"][data-region="NA"] .almanac-run'));
        expect(runs[runs.length - 1].getAttribute('title')).toMatch(/20:30–24:00 local/);
    });

    it('the now line uses local time', async () => {
        stubOffset(120);
        mockFetchOnce(makePayload({ bands: ['20m'] }));
        await openPanel();
        // 12:30 UTC = 14:30 CEST → 870 / 1440.
        expect(body().querySelector('.almanac-now').style.left).toBe('60.4167%');
        vi.restoreAllMocks();
        stubOffset(-300);
        __test.render();
        // 12:30 UTC = 07:30 UTC−5 → 450 / 1440.
        expect(body().querySelector('.almanac-now').style.left).toBe('31.25%');
        expect(body().querySelector('.almanac-legend').textContent).toContain('now');
        expect(body().querySelector('.almanac-legend').textContent).not.toMatch(/local time/);
    });

    it('a non-30-minute offset rounds the lanes to the nearest slot and says so', async () => {
        stubOffset(345); // Nepal, UTC+5:45
        mockFetchOnce(makePayload({
            bands: ['20m'],
            patch(lane) { if (lane.region === 'NA') lane.n[28] = 24; },
        }));
        await openPanel();
        const head = body().querySelector('.almanac-area').textContent;
        expect(head).toContain('UTC+5:45');
        expect(head).toMatch(/rounded to the nearest 30 min/);
        const run = body().querySelector('[data-slot="28"]');
        // Exact local time in the label; the drawing sits on the rounded slot.
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 19:45 local: opened 24 of 30 days');
        expect(displayStart(run)).toBe(40);
        // Now line on the rounded grid: 12:30 UTC + 6:00 = 18:30.
        expect(body().querySelector('.almanac-now').style.left).toBe('77.0833%');
    });

    it('the header carries no time-zone or window qualifiers for a whole-slot offset', async () => {
        stubOffset(120);
        mockFetchOnce(makePayload({ bands: ['20m'] }));
        await openPanel();
        const head = body().querySelector('.almanac-area').textContent;
        expect(head).not.toMatch(/local time|last \d+ days|UTC\+2/);
        expect(head).not.toMatch(/rounded/);
    });

    describe('schedule', () => {
        // 12:30 UTC; CEST.
        const NOW = new Date('2026-09-28T12:30:00Z');
        const render = (agenda) => {
            const host = document.createElement('div');
            host.innerHTML = scheduleHtml({ slot_minutes: 30, now_slot: 25, agenda }, NOW);
            return host;
        };

        it('titles a bar with the local range and keeps UTC in brackets', () => {
            stubOffset(120);
            const host = render([agendaEntry({ band: '20m', region: 'NA', start_slot: 36, len_slots: 4, start: '18:00', end: '20:00', starts_in_min: 90 })]);
            const row = host.querySelector('.cond-sched-row');
            expect(row.title).toContain('usually 20:00\u201322:00 (18:00\u201320:00 UTC), in 1 h 30 min');
            expect(row.dataset.band).toBe('20m');
        });

        it('crosses midnight in local terms', () => {
            stubOffset(120);
            const host = render([
                // 21:00-23:00 UTC (no UTC midnight) = 23:00-01:00 CEST.
                agendaEntry({ band: '40m', region: 'NA', start_slot: 42, len_slots: 4, start: '21:00', end: '23:00', starts_in_min: 150 }),
                // 20:00-08:00 UTC (crosses UTC midnight) = 22:00-10:00 CEST.
                agendaEntry({ band: '40m', region: 'SA', start_slot: 40, len_slots: 24, start: '20:00', end: '08:00', crosses_midnight: true, starts_in_min: 60 }),
            ]);
            const title = (region) => host.querySelector(`.cond-sched-row[data-region="${region}"]`).title;
            expect(title('NA')).toContain('usually 23:00\u201301:00 (21:00\u201323:00 UTC)');
            expect(title('SA')).toContain('usually 22:00\u201310:00 (20:00\u201308:00 UTC)');
        });

        it('labels the axis with local hours from now', () => {
            stubOffset(120); // now = 14:30 local; axis to 17:30
            const host = render([agendaEntry({ starts_in_min: 30 })]);
            expect(Array.from(host.querySelectorAll('.cond-sched-hour')).map((h) => h.textContent)).toEqual(['15', '16', '17']);
        });
    });

    it('drill-down: each month row is rotated by its own mid-month offset (CEST vs CET)', async () => {
        stubOffset(europeBerlin);
        const season = makeSeason({ months: MONTH_NAMES.map((_, i) => {
            if (i === 8) return seasonMonth(i, { year: 2026, layer: 'pskr', patch(mo) { mo.n[28] = 15; } });
            if (i === 11) return seasonMonth(i, { year: 2025, layer: 'wspr', patch(mo) { mo.n[28] = 5; } });
            return seasonMonth(i, { status: 'not_collected' });
        }) });
        routeFetch({ season });
        await openPanel();
        lane('20m', 'OC').click();
        await flush();
        await flush();
        const rows = monthRows();
        const sep = rows[8].querySelector('[data-slot="28"]');
        const dec = rows[11].querySelector('[data-slot="28"]');
        expect(sep.getAttribute('title')).toBe('20m to OC - Oceania, Sep 2026, 16:00 local: opened 15 of 20 days');
        expect(dec.getAttribute('title')).toBe('20m to OC - Oceania, Dec 2025, 15:00 local: opened 5 of 20 days');
        expect(displayStart(sep)).toBe(32);
        expect(displayStart(dec)).toBe(30);
        // The drill-down now line uses today's offset (CEST): 14:30.
        expect(body().querySelector('.almanac-drill .almanac-now').style.left).toBe('60.4167%');
        expect(body().querySelector('.almanac-months').getAttribute('aria-label')).toMatch(/local time/);
    });
});

describe('almanac preliminary SNR view', () => {
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
        stubOffset(0);
    });

    afterEach(() => {
        reset();
        global.fetch = originalFetch;
        vi.useRealTimers();
        vi.restoreAllMocks();
    });

    function prelimPayload(days = 3) {
        const p = makePayload({
            bands: ['20m'],
            patch(l) {
                l.m = new Array(48).fill(days);
                if (l.region === 'NA') l.n[20] = days;
            },
        });
        return { ...p, m_min: Math.max(2, days), min_snr: -15, snr_tier: -15, snr_available: true,
            snr_since: '2026-09-25', snr_days: days, preliminary: true };
    }

    it('shows the preliminary note with the SNR day count and the full-view date', async () => {
        mockFetchOnce(prelimPayload(3));
        await openPanel();
        const note = body().querySelector('.almanac-preliminary');
        expect(note).not.toBeNull();
        expect(note.textContent).toBe('preliminary — based on 3 days of SNR data (full view from 2026-10-05)');
        // m = 3 ≥ the effective m_min 3: known, not hatched.
        const run = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"] [data-slot="20"]');
        expect(run.classList.contains('is-unknown')).toBe(false);
        expect(run.getAttribute('title')).toBe('20m to NA - North America, 10:00 local: opened 3 of 3 days');
    });

    it('one SNR day reads singular; no note without the flag', () => {
        expect(__test.preliminaryText({ preliminary: true, snr_days: 1, snr_since: '2026-12-30' }))
            .toBe('preliminary — based on 1 day of SNR data (full view from 2027-01-09)');
        expect(__test.preliminaryText({ preliminary: false, snr_days: 12, snr_since: '2026-09-25' })).toBe('');
        expect(__test.preliminaryText(makePayload())).toBe('');
    });

    it('drill-down months use their own m_min and are marked preliminary', async () => {
        const season = makeSeason({ months: MONTH_NAMES.map((_, i) => {
            if (i !== 8) return seasonMonth(i, { status: 'not_collected' });
            return seasonMonth(i, { year: 2026, layer: 'pskr', patch(mo) {
                mo.m = new Array(48).fill(4);
                mo.n[28] = 4;
                mo.days = 4;
                mo.m_min = 4;
                mo.snr_days = 4;
                mo.preliminary = true;
            } });
        }) });
        routeFetch({ landing: prelimPayload(3), season: { ...season, min_snr: -15, snr_tier: -15, preliminary: true } });
        await openPanel();
        openDrilldown('20m', 'NA');
        await flush();
        await flush();
        const sep = monthRows()[8];
        expect(sep.querySelector('.almanac-month-label').textContent).toBe('Sep 2026 · PSKR · preliminary');
        const run = sep.querySelector('[data-slot="28"]');
        expect(run.classList.contains('is-unknown')).toBe(false);
        expect(run.getAttribute('title')).toBe('20m to NA - North America, Sep 2026, 14:00 local: opened 4 of 4 days');
    });
});
