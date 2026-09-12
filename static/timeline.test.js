// timeline.test.js — unit tests for the Time Travel controller's pure logic:
// moment slicing, live-shape conversion, snapping, LRU, key canonicalization,
// and URL round-trip. DOM-dependent controller paths stay untested here (they
// run under the browser).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import {
    MOMENT_WINDOW_SECONDS,
    SCRUB_SNAP_SECONDS,
    TIMELINE_SPEEDS,
    DEFAULT_SPEED,
    snapToScrub,
    clampPlayhead,
    sliceMoment,
    toLiveSpot,
    bundleKey,
    chunkBoundsFor,
    nextPrefetchAt,
    readTimelineURL,
    enterTimeline,
    exitTimeline,
    seek,
    invalidateBundles,
    refreshMoment,
    __internals,
} from './timeline.js';
import { sessionRing } from './session-ring.js';
import { state } from './state.js';

describe('snapToScrub', () => {
    it('rounds to the nearest 5-minute boundary', () => {
        expect(snapToScrub(0)).toBe(0);
        expect(snapToScrub(SCRUB_SNAP_SECONDS - 1)).toBe(SCRUB_SNAP_SECONDS);
        expect(snapToScrub(SCRUB_SNAP_SECONDS + 149)).toBe(SCRUB_SNAP_SECONDS);
        expect(snapToScrub(SCRUB_SNAP_SECONDS + 150)).toBe(2 * SCRUB_SNAP_SECONDS);
        expect(snapToScrub(-SCRUB_SNAP_SECONDS / 2)).toBe(0);
    });
});

describe('clampPlayhead', () => {
    it('clamps to [t0, t1]', () => {
        expect(clampPlayhead(50, 100, 200)).toBe(100);
        expect(clampPlayhead(250, 100, 200)).toBe(200);
        expect(clampPlayhead(150, 100, 200)).toBe(150);
    });
});

describe('sliceMoment', () => {
    const mk = (t) => ({ t, snr: 0, lat: 0, lng: 0, band: '20m' });

    it('returns spots with T-15min <= t <= T (inclusive bounds)', () => {
        const W = MOMENT_WINDOW_SECONDS;
        const spots = [1, 2, W - 1, W, W + 1, 2 * W, 3 * W].map((off) => mk(off));
        // Playhead at T=2W: window covers [W, 2W].
        const got = sliceMoment(spots, 2 * W);
        expect(got.map((s) => s.t)).toEqual([W, W + 1, 2 * W]);
    });

    it('returns empty for gaps outside the window', () => {
        const W = MOMENT_WINDOW_SECONDS;
        const spots = [0, 1, 2].map(mk);
        expect(sliceMoment(spots, 10 * W)).toEqual([]);
    });

    it('handles empty bundles', () => {
        expect(sliceMoment([], 100)).toEqual([]);
    });

    it('is inclusive at the trailing edge (t == T - W)', () => {
        const W = MOMENT_WINDOW_SECONDS;
        const spots = [mk(W), mk(W + 1)];
        // Playhead 2W: window [W, 2W] → both in. Playhead 2W+1: window
        // [W+1, 2W+1] → mk(W) drops out, mk(W+1) stays.
        expect(sliceMoment(spots, 2 * W).length).toBe(2);
        expect(sliceMoment(spots, 2 * W + 1).length).toBe(1);
    });
});

describe('toLiveSpot', () => {
    it('derives ageSeconds from the playhead, clamped at 0', () => {
        const s = { t: 1000, snr: -5, lat: 1, lng: 2, band: '40m', locator: 'JO32', reporterLocator: 'JO31', sourceType: 'mqtt', sender: 'A', receiver: 'B' };
        const live = toLiveSpot(s, 1900);
        expect(live.ageSeconds).toBe(900);
        expect(live.snr).toBe(-5);
        expect(live.band).toBe('40m');
        expect(live.locator).toBe('JO32');
        expect(live.sourceType).toBe('mqtt');
    });

    it('clamps future-stamped spots to age 0', () => {
        const s = { t: 2000, snr: 0, lat: 0, lng: 0, band: '20m' };
        expect(toLiveSpot(s, 1000).ageSeconds).toBe(0);
    });
});

describe('bundleKey', () => {
    it('is order-insensitive over enabled bands', () => {
        const a = bundleKey('JO32', true, 0, 'cw', 0, -15, new Set(['20m', '40m']), 100, 200);
        const b = bundleKey('JO32', true, 0, 'cw', 0, -15, new Set(['40m', '20m']), 100, 200);
        expect(a).toBe(b);
    });

    it('differs per qth / surroundings / snr mode / bands / window', () => {
        const base = bundleKey('JO32', false, 0, 'off', 0, -15, new Set(), 100, 200);
        expect(bundleKey('JO33', false, 0, 'off', 0, -15, new Set(), 100, 200)).not.toBe(base);
        expect(bundleKey('JO32', true, 0, 'off', 0, -15, new Set(), 100, 200)).not.toBe(base);
        expect(bundleKey('JO32', false, 0, 'cw', 0, -15, new Set(), 100, 200)).not.toBe(base);
        expect(bundleKey('JO32', false, 0, 'off', 0, -15, new Set(['20m']), 100, 200)).not.toBe(base);
        expect(bundleKey('JO32', false, 0, 'off', 0, -15, new Set(), 100, 300)).not.toBe(base);
    });
});

describe('chunkBoundsFor', () => {
    const H = 60 * 60;

    it('floors the playhead to its 1-hour chunk inside the range', () => {
        const [t0, t1] = chunkBoundsFor(1000 + 5 * H + 123, 0, 24 * H);
        expect(t0).toBe(5 * H);
        expect(t1).toBe(6 * H);
    });

    it('clamps the first chunk to the range start', () => {
        // Range starts mid-hour; chunk must not reach before rangeT0.
        const rangeT0 = 1000;
        const [t0] = chunkBoundsFor(rangeT0 + 30, rangeT0, rangeT0 + 24 * H);
        expect(t0).toBe(rangeT0);
    });

    it('clamps the last chunk to the range end (short tail chunk)', () => {
        const rangeT1 = 5 * H + 1234;
        const [t0, t1] = chunkBoundsFor(rangeT1 - 10, 0, rangeT1);
        expect(t1).toBe(rangeT1);
        expect(t1 - t0).toBeLessThanOrEqual(H);
    });

    it('is idempotent across a chunk (scrubbing within one hour reuses it)', () => {
        // Both positions snap (5-min grid) and floor into the [3H,4H) chunk.
        const a = chunkBoundsFor(3 * H + 60, 0, 24 * H);
        const b = chunkBoundsFor(3 * H + 60 + 2400, 0, 24 * H);
        expect(a).toEqual(b);
    });

    it('snaps the playhead to scrub boundaries first', () => {
        // 2*H + 149 snaps to 2*H (round down), floor(2H/1h)=2 → chunk [2H,3H].
        const [t0, t1] = chunkBoundsFor(2 * H + 149, 0, 24 * H);
        expect(t0).toBe(2 * H);
        expect(t1).toBe(3 * H);
    });
});

describe('nextPrefetchAt', () => {
    it('returns 0 when the bundle already reaches the range edge', () => {
        expect(nextPrefetchAt(1000, 1000)).toBe(0);
        expect(nextPrefetchAt(1000, 999)).toBe(0);
    });

    it('fires PREFETCH_AHEAD before the bundle edge', () => {
        const bT1 = 50_000, t1 = 100_000;
        const at = nextPrefetchAt(bT1, t1);
        expect(at).toBe(50_000 - 15 * 60);
        expect(at).toBeLessThan(bT1);
    });
});

describe('URL state', () => {
    beforeEach(() => {
        window.history.replaceState(null, '', '/');
    });

    it('readTimelineURL returns null without tl=1', () => {
        expect(readTimelineURL()).toBeNull();
    });

    it('readTimelineURL parses tl/t0/t1/spd', () => {
        window.history.replaceState(null, '', '/?tl=1&t0=100&t1=200&spd=600');
        const got = readTimelineURL();
        expect(got).toEqual({ t0: 100, t1: 200, speed: 600 });
    });

    it('readTimelineURL falls back to the default speed', () => {
        window.history.replaceState(null, '', '/?tl=1&t0=100&t1=200');
        expect(readTimelineURL().speed).toBe(DEFAULT_SPEED);
    });

    it('readTimelineURL rejects malformed windows', () => {
        window.history.replaceState(null, '', '/?tl=1&t0=200&t1=100');
        expect(readTimelineURL()).toBeNull();
        window.history.replaceState(null, '', '/?tl=1');
        expect(readTimelineURL()).toBeNull();
    });
});

describe('TIMELINE_SPEEDS', () => {
    it('matches the spec presets', () => {
        expect(TIMELINE_SPEEDS).toEqual([60, 240, 600]);
    });
});

// --- Unit U3 (plan 2026-09-11-002): ring-served timeline playback ---------
//
// The bundle-cache miss in ensurePlayheadBundle consults the session ring:
// covered 1-hour chunks are synthesized locally (zero /api/history fetches),
// uncovered chunks fall through to the archive fetch unchanged. session-ring.js
// is imported REAL (it is pure) and seeded via push() with synthetic
// __recvMs/__recvAge stamps; /api/history is a fetch mock that serves the same
// fixture so ring-served and archive-fetched moments can be compared for
// render parity (R9: set-identity, ±2s t tolerance).

// --- Shared fixtures (U3 + U4 describes) ------------------------------------
// Hour-agnostic instant: 1730000000 is NOT hour-aligned (chunk tail of
// 2000s) and NOT 5-minute-aligned, so clamped chunk edges are exercised.
const NOW_MS = 1_730_000_000_000;
const NOW_SEC = Math.floor(NOW_MS / 1000);
const H = 60 * 60;

const ctl = () => __internals.controller;

const mkSpot = (t, band = '20m') => ({
    t,
    lat: 52.5, lng: 7.0, snr: -6,
    locator: 'JO32', reporterLocator: 'JO31',
    sourceType: 'mqtt', band,
    sender: 'DL1ABC', receiver: 'DL9ET',
    __recvMs: NOW_MS, __recvAge: NOW_SEC - t, // derived t == t exactly
});

const setupDom = () => {
    document.body.innerHTML = `
        <input id="qth" value="JO32" />
        <input id="ssb-min-db" value="0" />
        <input id="cw-min-db" value="-15" />
        <input type="checkbox" id="surroundings" />
        <input type="radio" name="min-snr" value="none" checked />
        <div id="band-container"></div>
        <input type="checkbox" class="band-enable" value="20m" checked />
        <input type="checkbox" class="band-enable" value="40m" checked />
    `;
};

const resetController = () => {
    const c = ctl();
    c.active = false;
    c.playing = false;
    c.playhead = 0;
    c.bundle = null;
    c.bundles.clear();
    c.bundleOrder = [];
    c.inflightKeys.clear();
    c.inflight = null;
    c.loading = false;
    c.t0 = 0;
    c.t1 = 0;
    c.qth = '';
    c.reach = Infinity;
    c.listeners.moment.length = 0;
    c.listeners.status.length = 0;
    c.listeners.exit = null;
    document.body.classList.remove('timeline-active');
    const bar = document.getElementById('timeline-bar');
    if (bar) bar.remove();
};

describe('ring-served timeline playback (U3)', () => {
    let moments;

    // giveStreamFilter sets the filter the live stream is actually delivering
    // (state.streamedFilter, app.js) — the ring's cohort was filtered by it.
    const giveStreamFilter = (bands = ['20m', '40m']) => {
        state.streamedFilter = { bands: new Set(bands), minSnrMode: 'none', ssbMinDb: '0', cwMinDb: '-15' };
    };

    // seedRing pushes the fixture into the ring and returns the same spots in
    // the /api/history payload shape (absolute t, no receive stamps) for the
    // fetch mock.
    const seedRing = (fromT, toT, bands = ['20m']) => {
        const archive = [];
        for (let t = fromT; t <= toT; t += 60) {
            for (const band of bands) {
                const s = mkSpot(t, band);
                sessionRing.push(s);
                const { __recvMs, __recvAge, ...raw } = s;
                archive.push(raw);
            }
        }
        return archive;
    };

    // installFetch mocks /api/history serving the fixture within the queried
    // window (mirrors the server: spots are window-clipped and t-sorted).
    const installFetch = (archiveSpots) => {
        global.fetch = vi.fn(async (url) => {
            const u = new URL(String(url), 'http://localhost');
            const t0 = parseInt(u.searchParams.get('t0'), 10);
            const t1 = parseInt(u.searchParams.get('t1'), 10);
            return {
                ok: true,
                headers: { get: () => null },
                text: async () => '',
                json: async () => ({ t0, t1, spots: archiveSpots.filter((s) => s.t >= t0 && s.t <= t1) }),
            };
        });
        return global.fetch;
    };

    // assertMomentParity (R9): same rendered spot multiset, per-identity t
    // within ±2s (live frames derive t from the receive stamps; archive spots
    // carry server t).
    const assertMomentParity = (a, b) => {
        expect(a.ph).toBe(b.ph);
        const ident = (s) => `${s.band}|${s.sender}|${s.receiver}|${s.locator}|${s.reporterLocator}`;
        const collect = (m) => {
            const map = new Map();
            for (const s of m.live) {
                if (!map.has(ident(s))) map.set(ident(s), []);
                map.get(ident(s)).push(m.ph - s.ageSeconds);
            }
            return map;
        };
        const ma = collect(a);
        const mb = collect(b);
        expect([...mb.keys()].sort()).toEqual([...ma.keys()].sort());
        for (const [k, tb] of mb) {
            const ta = ma.get(k).sort((x, y) => x - y);
            tb.sort((x, y) => x - y);
            expect(ta.length).toBe(tb.length);
            for (let i = 0; i < ta.length; i++) {
                expect(Math.abs(ta[i] - tb[i])).toBeLessThanOrEqual(2);
            }
        }
    };

    beforeEach(() => {
        resetController();
        sessionRing.clear();
        state.streamedFilter = null;
        setupDom();
        moments = [];
        ctl().listeners.moment.push((live, ph) => { moments.push({ live, ph }); });
        global.fetch = vi.fn(async () => { throw new Error('unexpected /api/history fetch'); });
        vi.spyOn(Date, 'now').mockReturnValue(NOW_MS);
    });

    afterEach(() => {
        vi.restoreAllMocks();
        resetController();
    });

    it('in-coverage seek synthesizes a bundle with zero fetches and emits the same moment an archive bundle would', async () => {
        const archive = seedRing(NOW_SEC - 2 * H, NOW_SEC);
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(2 * H);
        expect(fetchMock).not.toHaveBeenCalled();
        await seek(NOW_SEC - 2 * H + 900); // first chunk (range-clamped edge)
        expect(fetchMock).not.toHaveBeenCalled();
        const ringRun = { enter: moments[0], seeked: moments[moments.length - 1] };

        // Same fixture through the archive path (ring emptied).
        resetController();
        sessionRing.clear();
        moments = [];
        ctl().listeners.moment.push((live, ph) => { moments.push({ live, ph }); });
        await enterTimeline(2 * H);
        await seek(NOW_SEC - 2 * H + 900);
        const archRun = { enter: moments[0], seeked: moments[moments.length - 1] };
        expect(fetchMock).toHaveBeenCalledTimes(2); // enter chunk + seek chunk
        assertMomentParity(ringRun.enter, archRun.enter);
        assertMomentParity(ringRun.seeked, archRun.seeked);
    });

    it('an uncovered chunk (before ring.minT) falls through to /api/history', async () => {
        const archive = seedRing(NOW_SEC - H, NOW_SEC); // ring covers only the last hour
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(2 * H);
        expect(fetchMock).not.toHaveBeenCalled(); // playhead chunk covered

        const seekT = NOW_SEC - 2 * H + 900;
        await seek(seekT);
        expect(fetchMock).toHaveBeenCalledTimes(1);
        const [ct0, ct1] = chunkBoundsFor(seekT, NOW_SEC - 2 * H, NOW_SEC);
        const u = new URL(fetchMock.mock.calls[0][0], 'http://localhost');
        expect(u.searchParams.get('t0')).toBe(String(ct0));
        expect(u.searchParams.get('t1')).toBe(String(ct1));
    });

    it('an interior gap in the ring makes the spanning chunk fall through to /api/history', async () => {
        // 10-minute hole inside the chunk containing the initial playhead.
        const archive = [
            ...seedRing(NOW_SEC - H, NOW_SEC - H + 1100),
            ...seedRing(NOW_SEC - H + 1700, NOW_SEC),
        ];
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(H);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it('a narrowing filter change re-slices the ring with no fetch; widening falls through (AE2)', async () => {
        const archive = seedRing(NOW_SEC - 2 * H, NOW_SEC, ['20m', '40m']);
        giveStreamFilter(['20m', '40m']); // stream delivered both bands
        const fetchMock = installFetch(archive);

        await enterTimeline(2 * H);
        expect(fetchMock).not.toHaveBeenCalled();

        // Narrowing: disable 40m → re-slice the first chunk, no fetch, the
        // moment is 20m-only.
        document.querySelector('.band-enable[value="40m"]').checked = false;
        await seek(NOW_SEC - 2 * H + 900);
        expect(fetchMock).not.toHaveBeenCalled();
        const m = moments[moments.length - 1];
        expect(m.live.length).toBeGreaterThan(0);
        expect(m.live.every((s) => s.band === '20m')).toBe(true);

        // Widening: re-enable 40m when the stream never delivered it. The
        // wide-filter key of THIS chunk has no cached bundle (the enter chunk
        // was a different window) and the delivered cohort no longer vouches
        // for 40m → fall through to /api/history.
        state.streamedFilter = { bands: new Set(['20m']), minSnrMode: 'none', ssbMinDb: '0', cwMinDb: '-15' };
        document.querySelector('.band-enable[value="40m"]').checked = true;
        await seek(NOW_SEC - 2 * H + 900);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it('the chunk containing "now" serves its covered portion; moments never extend beyond lastReceived', async () => {
        const lastT = NOW_SEC - 100; // stream stopped 100s ago
        const archive = seedRing(NOW_SEC - 2 * H, lastT);
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(H); // chunk t1 = "now" overhangs ring.lastT by 100s
        expect(fetchMock).not.toHaveBeenCalled();
        const m = moments[moments.length - 1];
        expect(m.ph).toBe(NOW_SEC);
        expect(m.live.length).toBeGreaterThan(0);
        const maxT = Math.max(...m.live.map((s) => m.ph - s.ageSeconds));
        expect(maxT).toBeLessThanOrEqual(lastT);
    });

    it('enter/exit LRU wipes still occur; the ring survives across them', async () => {
        const archive = seedRing(NOW_SEC - H, NOW_SEC);
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(H);
        expect(ctl().bundles.size).toBe(1); // ring bundle entered the LRU
        const ringCount = sessionRing.stats().count;
        expect(ringCount).toBe(61);

        exitTimeline();
        expect(ctl().bundles.size).toBe(0); // bundles wiped on exit
        expect(ctl().bundle).toBeNull();
        expect(sessionRing.stats().count).toBe(ringCount); // ring survives

        await enterTimeline(H);
        expect(fetchMock).not.toHaveBeenCalled(); // still zero-fetch after re-entry
    });

    it('prefetching the next chunk during playback resolves from the ring with zero fetches', async () => {
        const archive = seedRing(NOW_SEC - 2 * H, NOW_SEC);
        giveStreamFilter();
        const fetchMock = installFetch(archive);

        await enterTimeline(2 * H);
        await seek(NOW_SEC - 2 * H + 900);
        const b = ctl().bundle;
        expect(b.t1).toBeLessThan(ctl().t1); // a next chunk exists

        // Simulate the play tick crossing the chunk edge; maybePrefetch then
        // resolves the next chunk through the same ensurePlayheadBundle path.
        ctl().playhead = b.t1 + 50;
        __internals.maybePrefetch();
        expect(fetchMock).not.toHaveBeenCalled();
        expect(ctl().bundle.t0).toBe(chunkBoundsFor(b.t1 + 50, ctl().t0, ctl().t1)[0]);
        expect(ctl().bundle.spots.length).toBeGreaterThan(0);
    });

    it('a ring-served chunk never populates inflightKeys', async () => {
        seedRing(NOW_SEC - 2 * H, NOW_SEC);
        giveStreamFilter();
        installFetch([]);
        await enterTimeline(2 * H);
        await seek(NOW_SEC - 2 * H + 900);
        expect(global.fetch).not.toHaveBeenCalled();
        expect(ctl().inflightKeys.size).toBe(0);
    });

    it('a ring back-fill preempts an in-flight chunk fetch without touching the dedup map', async () => {
        seedRing(NOW_SEC - H, NOW_SEC); // final hour only: the first chunk is uncovered
        giveStreamFilter();
        let resolveFetch;
        global.fetch = vi.fn((url) => new Promise((res) => {
            const u = new URL(String(url), 'http://localhost');
            const t0 = Number(u.searchParams.get('t0'));
            const t1 = Number(u.searchParams.get('t1'));
            const settle = () => res({ ok: true, headers: { get: () => null }, text: async () => '', json: async () => ({ t0, t1, spots: [] }) });
            resolveFetch = settle;
            setTimeout(settle, 200); // never hangs, even when red
        }));

        await enterTimeline(2 * H);
        expect(global.fetch).not.toHaveBeenCalled();

        const [ct0, ct1] = chunkBoundsFor(NOW_SEC - 5000, NOW_SEC - 2 * H, NOW_SEC);
        const key = __internals.bundleCacheKey(ct0, ct1);
        const seekP = seek(NOW_SEC - 5000); // uncovered chunk → fetch starts (pending)
        await Promise.resolve();
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(ctl().inflightKeys.has(key)).toBe(true);

        // The reconnect dump back-fills the window while the fetch is pending.
        seedRing(NOW_SEC - 2 * H, NOW_SEC - H - 60);
        await seek(NOW_SEC - 5000); // now ring-covered → served, no second fetch
        expect(global.fetch).toHaveBeenCalledTimes(1);
        expect(ctl().inflightKeys.has(key)).toBe(true); // ring path left dedup alone
        expect(ctl().bundle.key).toBe(key);

        resolveFetch();
        await seekP;
        expect(ctl().inflightKeys.has(key)).toBe(false); // fetch cleanup intact
    });

    it('tryRingBundle returns null for a foreign qth or without a delivered stream filter', async () => {
        const archive = seedRing(NOW_SEC - H, NOW_SEC);
        giveStreamFilter();
        const fetchMock = installFetch(archive);
        await enterTimeline(H);
        expect(fetchMock).not.toHaveBeenCalled();

        const [ct0, ct1] = chunkBoundsFor(NOW_SEC - 1800, NOW_SEC - H, NOW_SEC);
        const key = __internals.bundleCacheKey(ct0, ct1);

        // Foreign qth (changed without re-entering the timeline): fall through.
        document.getElementById('qth').value = 'JO33';
        expect(__internals.tryRingBundle(ct0, ct1, key)).toBeNull();
        document.getElementById('qth').value = 'JO32';

        // No active stream (null streamedFilter): nothing vouches for the
        // cohort's filter → fall through.
        state.streamedFilter = null;
        expect(__internals.tryRingBundle(ct0, ct1, key)).toBeNull();

        // Uncovered window: fall through.
        state.streamedFilter = { bands: new Set(['20m']), minSnrMode: 'none', ssbMinDb: '0', cwMinDb: '-15' };
        expect(__internals.tryRingBundle(NOW_SEC - 2 * H - 60, NOW_SEC - 2 * H, key)).toBeNull();

        // Covered again (delivered filter restored): synthesized, LRU-populated.
        giveStreamFilter();
        const got = __internals.tryRingBundle(ct0, ct1, key);
        expect(got).not.toBeNull();
        expect(got.key).toBe(key);
        expect(got.spots.length).toBeGreaterThan(0);
        expect(ctl().bundles.has(key)).toBe(true);
    });

    it('skips WSPR spots in emitted moments (replay is unconditional; live keeps its own toggle)', async () => {
        // Seed one minute-step fixture, then splice a wspr spot into the ring
        // and the archive so BOTH chunk sources carry it.
        const archive = seedRing(NOW_SEC - 2 * H, NOW_SEC);
        const wspr = { ...mkSpot(NOW_SEC - H), sourceType: 'wspr' };
        sessionRing.push(wspr);
        const { __recvMs, __recvAge, ...rawWspr } = wspr;
        archive.push(rawWspr);
        archive.sort((a, b) => a.t - b.t);
        giveStreamFilter();
        installFetch(archive);

        await enterTimeline(2 * H);
        await seek(NOW_SEC - H); // playhead inside the minute the wspr spot lands in
        expect(moments.length).toBeGreaterThan(0);
        for (const { live } of moments) {
            expect(live.some((s) => s.sourceType === 'wspr')).toBe(false);
            expect(live.length).toBeGreaterThan(0); // mqtt spots still render
        }
    });
});
describe('bundle invalidation + moment refresh (U4)', () => {
    // app.js's mid-timeline band/SNR handlers (KTD-12) wipe the bundle LRU and
    // re-emit the current moment through refreshMoment — without snapping the
    // playhead (unlike seek(), which would yank a playing playhead back to its
    // 5-minute bucket).
    let moments;

    beforeEach(() => {
        resetController();
        sessionRing.clear();
        state.streamedFilter = { bands: new Set(['20m', '40m']), minSnrMode: 'none', ssbMinDb: '0', cwMinDb: '-15' };
        setupDom();
        moments = [];
        ctl().listeners.moment.push((live, ph) => { moments.push({ live, ph }); });
        global.fetch = vi.fn(async () => { throw new Error('unexpected /api/history fetch'); });
        vi.spyOn(Date, 'now').mockReturnValue(NOW_MS);
    });

    afterEach(() => {
        vi.restoreAllMocks();
        resetController();
        sessionRing.clear();
        state.streamedFilter = null;
    });

    it('invalidateBundles drops the LRU, the current bundle and in-flight keys', async () => {
        for (let t = NOW_SEC - H; t <= NOW_SEC; t += 60) sessionRing.push(mkSpot(t));
        await enterTimeline(H);
        const key0 = ctl().bundle?.key;
        expect(key0).toBeTruthy();
        expect(ctl().bundles.size).toBeGreaterThan(0);
        ctl().inflightKeys.set('pending', Promise.resolve({}));

        invalidateBundles();

        expect(ctl().bundles.size).toBe(0);
        expect(ctl().bundleOrder.length).toBe(0);
        expect(ctl().bundle).toBeNull();
        expect(ctl().inflightKeys.size).toBe(0);
    });

    it('refreshMoment re-resolves the playhead bundle without snapping and re-emits', async () => {
        for (let t = NOW_SEC - H; t <= NOW_SEC; t += 60) sessionRing.push(mkSpot(t));
        await enterTimeline(H);
        expect(moments.length).toBe(1);

        // Park the playhead between 5-minute scrub snaps (mid-playback state).
        const between = NOW_SEC - 123; // not 5-minute-aligned
        ctl().playhead = between;
        ctl().bundle = null; // simulate a stale/absent bundle at the playhead

        await refreshMoment();

        expect(moments.length).toBe(2);
        // No snap: the emitted playhead is the parked one, not its bucket.
        expect(moments[1].ph).toBe(between);
        // The bundle was re-resolved for the playhead's chunk.
        expect(ctl().bundle).toBeTruthy();
    });
});
