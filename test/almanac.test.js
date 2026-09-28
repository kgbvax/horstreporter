import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

// almanac.js (U7) — the Almanac panel: region rows of band lanes ("opened N
// of M days" per 30-min UTC slot), a UTC now line, the agenda above the
// lanes, and the area header. Fixtures mirror the BACKEND /api/almanac JSON
// (snake_case, see docs/api.md) so a naming mismatch surfaces here.
import { initAlmanac, __test } from '../static/almanac.js';

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
        <input id="qth" value="${qth}" />
        <div id="map-stack">
            <div id="map-toggles"><button id="${TOGGLE_ID}"></button></div>
            <div id="${PANEL_ID}" class="almanac-window is-hidden">
                <div class="almanac-window-header"><span id="almanac-title">Almanac</span></div>
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

describe('almanac panel (U7)', () => {
    let store;
    let originalFetch;

    beforeEach(() => {
        originalFetch = global.fetch;
        store = installLocalStorageMock();
        setupDom();
        reset();
        Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
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
        const open = body().querySelector('[aria-label="20m to NA, 14:00 UTC: opened 24 of 30 days"]');
        expect(open).not.toBeNull();
        expect(open.getAttribute('title')).toBe('20m to NA, 14:00 UTC: opened 24 of 30 days');
        expect(Number(open.style.opacity || getOpacity(open))).toBeGreaterThan(0.5);

        const unknown = body().querySelector('[aria-label="10m to KH6, 05:00 UTC: not enough data (6 days)"]');
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
        expect(body().querySelector('[aria-label="20m to NA, 13:00–14:30 UTC: opened 24 of 30 days"]')).not.toBeNull();
        const naLane = body().querySelector('.almanac-lane[data-band="20m"][data-region="NA"]');
        // closed 00:00–13:00, open 13:00–14:30, closed 14:30–24:00
        expect(naLane.querySelectorAll('.almanac-run')).toHaveLength(3);
    });

    it('draws a UTC now line and a legend (ramp, unknown hatch, now line)', async () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(new Date('2026-09-28T12:30:00Z'));
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
        expect(runtime.drilldown).toEqual({ band: '20m', region: 'JA' });
        openDrilldown('40m', 'NA');
        expect(runtime.drilldown).toEqual({ band: '40m', region: 'NA' });
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
        expect(runtime.drilldown).toEqual({ band: lanes[1].dataset.band, region: lanes[1].dataset.region });
    });

    describe('agenda', () => {
        it('lists open-now first, then by start; caps at 6 with "show all"', async () => {
            const agenda = [
                agendaEntry({ band: '40m', region: 'NA', start: '20:00', end: '23:00', starts_in_min: 400, start_slot: 40 }),
                agendaEntry({ band: '20m', region: 'SA', start: '14:00', end: '16:00', starts_in_min: 90, start_slot: 28 }),
                agendaEntry({ band: '17m', region: 'AF', start: '15:00', end: '16:00', starts_in_min: 150, start_slot: 30 }),
                agendaEntry({ band: '15m', region: 'AS', start: '16:00', end: '17:00', starts_in_min: 210, start_slot: 32 }),
                agendaEntry({ band: '12m', region: 'OC', start: '17:00', end: '18:00', starts_in_min: 270, start_slot: 34 }),
                agendaEntry({ band: '10m', region: 'VK', start: '18:00', end: '19:00', starts_in_min: 330, start_slot: 36 }),
                agendaEntry({ band: '20m', region: 'JA', start: '10:00', end: '13:30', status: 'ongoing', starts_in_min: 0, start_slot: 20, open_today: true }),
                agendaEntry({ band: '30m', region: 'EU', start: '08:00', end: '14:00', status: 'ongoing', starts_in_min: 0, start_slot: 16 }),
            ];
            mockFetchOnce(makePayload({ bands: ['20m'], agenda }));
            await openPanel();
            let rows = Array.from(body().querySelectorAll('.almanac-agenda-row'));
            expect(rows).toHaveLength(6);
            expect(rows[0].getAttribute('data-status')).toBe('ongoing');
            expect(rows[1].getAttribute('data-status')).toBe('ongoing');
            // Ongoing by start, then upcoming by start.
            expect(rows.map((r) => `${r.dataset.band}-${r.dataset.region}`)).toEqual(
                ['30m-EU', '20m-JA', '20m-SA', '17m-AF', '15m-AS', '12m-OC']);
            expect(rows[1].textContent).toContain('20m to JA');
            expect(rows[1].textContent).toContain('10:00–13:30 UTC');
            expect(rows[1].textContent).toContain('24/30 days');
            expect(rows[1].textContent).toMatch(/open today/i);
            expect(rows[2].textContent).toMatch(/in 1 h 30 min/);

            const more = body().querySelector('.almanac-agenda-more');
            expect(more.textContent).toBe('Show all (8)');
            more.click();
            rows = Array.from(body().querySelectorAll('.almanac-agenda-row'));
            expect(rows).toHaveLength(8);
            expect(body().querySelector('.almanac-agenda-more').textContent).toBe('Show fewer');
        });

        it('shows the empty-state line when nothing qualifies', async () => {
            mockFetchOnce(makePayload({ bands: ['20m'], agenda: [] }));
            await openPanel();
            expect(body().querySelector('.almanac-agenda').textContent).toContain('No usual openings in the next 12 h');
            expect(body().querySelector('.almanac-agenda-more')).toBeNull();
        });

        it('renders a window that crosses midnight as "20:00–08:00 UTC", plus a local-time label', async () => {
            mockFetchOnce(makePayload({ bands: ['40m'], agenda: [
                agendaEntry({ band: '40m', region: 'NA', start_slot: 40, len_slots: 24, start: '20:00', end: '08:00', crosses_midnight: true }),
            ] }));
            await openPanel();
            const row = body().querySelector('.almanac-agenda-row');
            expect(row.textContent).toContain('20:00–08:00 UTC');
            expect(row.querySelector('.almanac-local').textContent).toMatch(/local/);
        });

        it('labels an all-day window', async () => {
            mockFetchOnce(makePayload({ bands: ['40m'], agenda: [
                agendaEntry({ band: '40m', region: 'EU', start_slot: 0, len_slots: 48, start: '00:00', end: '00:00', all_day: true, status: 'ongoing', starts_in_min: 0 }),
            ] }));
            await openPanel();
            expect(body().querySelector('.almanac-agenda-row').textContent).toContain('all day');
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
