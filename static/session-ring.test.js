// session-ring.test.js — unit tests for the client-side session ring:
// immutable raw copies with derived spot-time, 2-dimensional eviction
// (6h receive depth + count backstop), derived-t coverage with gap
// tolerance, dedup, and the streamClientFilter re-slice replica.
// The SSE wiring (U2) and timeline consult (U3) live elsewhere.

import { describe, it, expect } from 'vitest';

import {
    RING_DEPTH_SECONDS,
    RING_MAX_SPOTS,
    COVERAGE_GAP_SECONDS,
    sessionRing,
    filterAllowed,
    __internals,
} from './session-ring.js';
import { sliceMoment, toLiveSpot } from './timeline.js';

// makeSpot builds a live-wire-shaped spot stamped the way app.js onmessage
// stamps it (__recvMs / __recvAge) before the ring push.
const recvBase = 1_700_000_000_000; // fixed client wall clock (ms)
let recvSeq = 0;
function makeSpot(over = {}) {
    recvSeq += 1;
    const recvAge = over.recvAge ?? 0;
    const recvMs = over.recvMs ?? recvBase + recvSeq * 1000;
    return {
        ageSeconds: recvAge,
        band: '20m',
        snr: -5,
        lat: 52.5,
        lng: 13.4,
        locator: 'JO62qm',
        reporterLocator: 'JO31ab',
        sourceType: 'mqtt',
        sender: 'DL1ABC',
        receiver: 'DL9ET',
        ...over,
        __recvMs: recvMs,
        __recvAge: recvAge,
    };
}

// deriveT is the module's single source of truth for the absolute derived
// time: t = floor(recvMs / 1000) - recvAge (seconds).
function expectT(spot) {
    return Math.floor(spot.__recvMs / 1000) - spot.__recvAge;
}

describe('push keeps an immutable raw copy', () => {
    it('derives absolute t from the stamped inputs', () => {
        sessionRing.clear();
        const spot = makeSpot({ recvAge: 120 });
        sessionRing.push(spot);
        const got = sessionRing.slice(0, Infinity);
        expect(got).toHaveLength(1);
        expect(got[0].t).toBe(expectT(spot));
    });

    it('ring values do not change when the live list mutates afterwards', () => {
        sessionRing.clear();
        const spot = makeSpot({ recvAge: 60 });
        const live = [spot];
        sessionRing.push(spot);
        const before = sessionRing.slice(0, Infinity)[0];

        // The age-prune loop steps ageSeconds and mutates live spots in place.
        spot.ageSeconds = spot.ageSeconds + 5;
        spot.snr = -99;
        spot.band = '40m';
        live.pop();

        const after = sessionRing.slice(0, Infinity)[0];
        expect(after).toEqual(before);
        expect(after.t).toBe(expectT({ __recvMs: spot.__recvMs, __recvAge: 60 }));
        expect(after.snr).toBe(-5);
        expect(after.band).toBe('20m');
    });

    it('stored raws are frozen (cannot be mutated through the slice)', () => {
        sessionRing.clear();
        sessionRing.push(makeSpot());
        const got = sessionRing.slice(0, Infinity)[0];
        expect(Object.isFrozen(got)).toBe(true);
        expect(() => { got.snr = 42; }).toThrow();
    });

    it('slice output is t-sorted ascending and inclusive at both bounds', () => {
        sessionRing.clear();
        const spots = [
            makeSpot({ recvAge: 300 }),
            makeSpot({ recvAge: 100 }),
            makeSpot({ recvAge: 200 }),
        ];
        // Push out of t order (receive order differs from derived-t order).
        sessionRing.push(spots[0]);
        sessionRing.push(spots[1]);
        sessionRing.push(spots[2]);
        const ts = sessionRing.slice(0, Infinity).map((s) => s.t);
        expect(ts).toEqual([...ts].sort((a, b) => a - b));
        const newest = Math.max(...spots.map(expectT));
        const oldest = Math.min(...spots.map(expectT));
        expect(sessionRing.slice(oldest, oldest).map((s) => s.t)).toEqual([oldest]);
        expect(sessionRing.slice(newest, newest)).toHaveLength(1);
        expect(sessionRing.slice(oldest, newest)).toHaveLength(3);
    });
});

describe('eviction', () => {
    it('evicts spots received more than RING_DEPTH_SECONDS ago on push', () => {
        sessionRing.clear();
        const old = makeSpot({ recvAge: 0 });
        sessionRing.push(old);
        // A later frame whose receive wall clock is past the 6h depth relative
        // to the earlier push.
        const fresh = makeSpot({ recvMs: old.__recvMs + (RING_DEPTH_SECONDS + 60) * 1000 });
        sessionRing.push(fresh);
        const got = sessionRing.slice(0, Infinity);
        expect(got).toHaveLength(1);
        expect(got[0].t).toBe(expectT(fresh));
        // The evicted spot's coverage is gone with it.
        expect(sessionRing.covers(expectT(old) - 10, expectT(old) + 10)).toBe(false);
    });

    it('keeps spots inside the 6h receive depth', () => {
        sessionRing.clear();
        const a = makeSpot();
        sessionRing.push(a);
        const b = makeSpot({ recvMs: a.__recvMs + (RING_DEPTH_SECONDS - 60) * 1000 });
        sessionRing.push(b);
        expect(sessionRing.slice(0, Infinity)).toHaveLength(2);
    });

    it('drops the oldest batch when the count passes the cap', () => {
        sessionRing.clear();
        const total = RING_MAX_SPOTS + 3;
        // Simulate a hot-band live stream: 0.4s receive spacing keeps the whole
        // stream inside the 6h depth (so only the count criterion evicts), and
        // per-spot snr variation keeps identities distinct for same-second spots.
        for (let i = 0; i < total; i++) {
            sessionRing.push(makeSpot({ recvMs: recvBase + i * 400, recvAge: 0, snr: -(i % 40) }));
        }
        const got = sessionRing.slice(0, Infinity);
        expect(got).toHaveLength(RING_MAX_SPOTS);
        // Oldest-received entries are gone; the newest survived.
        expect(got[0].t).toBeGreaterThan(recvBase / 1000);
        expect(got[got.length - 1].t).toBe(Math.floor((recvBase + (total - 1) * 400) / 1000));
    });
});

describe('coverage (derived spot-time per KTD-7)', () => {
    it('a contiguous receive stream covers its whole window', () => {
        sessionRing.clear();
        // 20 spots, 10s apart in derived t — gaps well inside tolerance.
        const spots = [];
        for (let i = 0; i < 20; i++) {
            const s = makeSpot({ recvMs: recvBase + i * 1000, recvAge: 10 });
            sessionRing.push(s);
            spots.push(s);
        }
        const tFirst = expectT(spots[0]);
        const tLast = expectT(spots[spots.length - 1]);
        expect(sessionRing.covers(tFirst, tLast)).toBe(true);
        expect(sessionRing.covers(tFirst, tLast + COVERAGE_GAP_SECONDS)).toBe(true);
    });

    it('a derived-t gap larger than COVERAGE_GAP_SECONDS is an interior miss', () => {
        sessionRing.clear();
        // Stream, then a simulated SSE drop: derived t jumps by 1800s.
        const spotA = makeSpot({ recvMs: recvBase, recvAge: 0 });
        const spotB = makeSpot({ recvMs: recvBase + 1000, recvAge: 0 });
        const spotC = makeSpot({ recvMs: recvBase + 1800 * 1000, recvAge: 0 });
        const spotD = makeSpot({ recvMs: recvBase + 1801 * 1000, recvAge: 0 });
        [spotA, spotB, spotC, spotD].forEach((s) => sessionRing.push(s));
        const tA = expectT(spotA), tB = expectT(spotB), tC = expectT(spotC), tD = expectT(spotD);
        // Chunk spanning the outage: miss.
        expect(sessionRing.covers(tA, tC)).toBe(false);
        // Each side of the gap individually: covered.
        expect(sessionRing.covers(tA, tB)).toBe(true);
        expect(sessionRing.covers(tC, tD)).toBe(true);
        // A window inside the gap: miss.
        expect(sessionRing.covers(tB + 10, tC - 10)).toBe(false);
    });

    it('chunk bottom below ring.minT is a miss', () => {
        sessionRing.clear();
        const spot = makeSpot({ recvAge: 0 });
        sessionRing.push(spot);
        // Chunk starting before the oldest retained derived t.
        expect(sessionRing.covers(expectT(spot) - 3600, expectT(spot))).toBe(false);
        expect(sessionRing.covers(expectT(spot), expectT(spot) + 30)).toBe(true);
    });

    it('future-side overhang past the last received t is still covered', () => {
        sessionRing.clear();
        const spotA = makeSpot({ recvMs: recvBase, recvAge: 0 });
        const spotB = makeSpot({ recvMs: recvBase + 5000, recvAge: 0 });
        sessionRing.push(spotA);
        sessionRing.push(spotB);
        // The chunk containing "now" reaches past lastReceived (its bottom is
        // ring.minT); moment windows never extend beyond now, so the overhang
        // is harmless.
        expect(sessionRing.covers(expectT(spotA), expectT(spotB) + 3600)).toBe(true);
    });

    it('an empty ring covers nothing', () => {
        sessionRing.clear();
        expect(sessionRing.covers(0, 3600)).toBe(false);
    });
});

describe('dedup', () => {
    it('a re-delivered identical spot collapses to a single entry', () => {
        sessionRing.clear();
        const wire = makeSpot({ recvAge: 45 });
        // First delivery (live frame), then the reconnect dump re-seeds it.
        // The dump arrives recvMs + 4000 with recvAge also advanced by exactly
        // 4s (the server stamps age against its own clock), so the DERIVED t
        // is identical — that is the whole point of t = recvMs − recvAge.
        const copy = {
            ...wire,
            __recvMs: wire.__recvMs + 4000,
            __recvAge: wire.__recvAge + 4,
        };
        sessionRing.push(wire);
        sessionRing.push(copy);
        expect(sessionRing.slice(0, Infinity)).toHaveLength(1);
    });

    it('distinct same-second spots on the same band do not collapse', () => {
        sessionRing.clear();
        const a = makeSpot({ recvMs: recvBase, recvAge: 0, lat: 52.5, lng: 13.4 });
        const b = makeSpot({ recvMs: recvBase, recvAge: 0, lat: 48.1, lng: 11.5 });
        sessionRing.push(a);
        sessionRing.push(b);
        expect(sessionRing.slice(0, Infinity)).toHaveLength(2);
    });
});

describe('filter re-slice (streamClientFilter.spotAllowed replica)', () => {
    const enabledBands = new Set(['20m', '40m']);
    const narrow = { enabledBands, minSnrMode: 'cw', ssbMinDb: 0, cwMinDb: -10, selectedBand: '20m' };

    function seed() {
        sessionRing.clear();
        sessionRing.push(makeSpot({ recvAge: 0, band: '20m', snr: -5 }));
        sessionRing.push(makeSpot({ recvAge: 10, band: '40m', snr: -8 }));
        sessionRing.push(makeSpot({ recvAge: 20, band: '30m', snr: -5 })); // disabled band
        sessionRing.push(makeSpot({ recvAge: 30, band: '20m', snr: -40 })); // below cw threshold
    }

    it('narrowing excludes the disabled band and below-threshold spots', () => {
        seed();
        const got = sessionRing.sliceFiltered(0, Infinity, narrow);
        expect(got.map((s) => s.band).sort()).toEqual(['20m', '40m']);
    });

    it('selectedBand is not applied (KTD-13)', () => {
        seed();
        // selectedBand is '20m' in the filter object, yet 40m spots survive —
        // band focus is client-side, /api/history ignores it.
        const got = sessionRing.sliceFiltered(0, Infinity, narrow);
        expect(got.some((s) => s.band === '40m')).toBe(true);
    });

    it('widening restores previously excluded spots from the ring (no data loss)', () => {
        seed();
        const wide = { enabledBands, minSnrMode: 'off', ssbMinDb: 0, cwMinDb: -15 };
        const got = sessionRing.sliceFiltered(0, Infinity, wide);
        // The below-threshold spot comes back; only the disabled band stays out
        // (widening a band set beyond what the server sent cannot resurrect it,
        // but the ring stores raw copies so threshold wideners do).
        expect(got.map((s) => s.band).sort()).toEqual(['20m', '20m', '40m']);
    });

    it('dxcluster and wspr spots are never SNR-filtered', () => {
        sessionRing.clear();
        sessionRing.push(makeSpot({ recvAge: 0, sourceType: 'dxcluster', snr: -80, band: '20m' }));
        sessionRing.push(makeSpot({ recvAge: 10, sourceType: 'wspr', snr: -80, band: '20m' }));
        sessionRing.push(makeSpot({ recvAge: 20, sourceType: 'mqtt', snr: -80, band: '20m' }));
        sessionRing.push(makeSpot({ recvAge: 30, sourceType: '', snr: -80, band: '20m' })); // empty = mqtt-calibrated
        sessionRing.push(makeSpot({ recvAge: 40, sourceType: 'rbn', snr: -80, band: '20m' })); // rbn = mqtt-calibrated scale
        const strict = { enabledBands: new Set(['20m']), minSnrMode: 'cw', ssbMinDb: 0, cwMinDb: 0 };
        const got = sessionRing.sliceFiltered(0, Infinity, strict);
        // Per server.go:506-528 the SNR thresholds apply to "", mqtt and rbn
        // (empty sourceType counts as mqtt-calibrated); dxcluster/wspr are exempt.
        expect(got.map((s) => s.sourceType).sort()).toEqual(['dxcluster', 'wspr']);
    });

    it('empty enabledBands set means all bands allowed', () => {
        seed();
        const allBands = { enabledBands: new Set(), minSnrMode: 'off', ssbMinDb: 0, cwMinDb: -15 };
        const got = sessionRing.sliceFiltered(0, Infinity, allBands);
        expect(got).toHaveLength(4);
    });
});

describe('clear', () => {
    it('empties spots and coverage', () => {
        sessionRing.clear();
        sessionRing.push(makeSpot());
        expect(sessionRing.slice(0, Infinity)).toHaveLength(1);
        sessionRing.clear();
        expect(sessionRing.slice(0, Infinity)).toEqual([]);
        expect(sessionRing.covers(0, Infinity)).toBe(false);
        expect(__internals.controller.index.size).toBe(0);
        expect(__internals.controller.coverage).toEqual([]);
    });
});

describe('filterAllowed (pure replica of server.go streamClientFilter)', () => {
    const base = { band: '20m', sourceType: 'mqtt', snr: -10 };

    it('applies the cw threshold to mqtt spots', () => {
        const f = { enabledBands: new Set(), minSnrMode: 'cw', ssbMinDb: 0, cwMinDb: -15 };
        expect(filterAllowed({ ...base, snr: -14 }, f)).toBe(true);
        expect(filterAllowed({ ...base, snr: -16 }, f)).toBe(false);
    });

    it('applies the ssb threshold to mqtt spots', () => {
        const f = { enabledBands: new Set(), minSnrMode: 'ssb', ssbMinDb: 5, cwMinDb: -15 };
        expect(filterAllowed({ ...base, snr: 5 }, f)).toBe(true);
        expect(filterAllowed({ ...base, snr: 4 }, f)).toBe(false);
    });

    it('minSnrMode off filters nothing by SNR', () => {
        const f = { enabledBands: new Set(), minSnrMode: 'off', ssbMinDb: 99, cwMinDb: 99 };
        expect(filterAllowed({ ...base, snr: -99 }, f)).toBe(true);
    });

    it('rbn is SNR-filtered like the server (empty/mqtt/rbn calibrated)', () => {
        const f = { enabledBands: new Set(), minSnrMode: 'cw', ssbMinDb: 0, cwMinDb: 10 };
        expect(filterAllowed({ ...base, sourceType: 'rbn', snr: 10 }, f)).toBe(true);
        expect(filterAllowed({ ...base, sourceType: 'rbn', snr: -30 }, f)).toBe(false);
    });

    it('case-insensitive on sourceType and mode like the server', () => {
        const f = { enabledBands: new Set(), minSnrMode: 'CW', ssbMinDb: 0, cwMinDb: 10 };
        expect(filterAllowed({ ...base, sourceType: 'MQTT', snr: -30 }, f)).toBe(false);
        expect(filterAllowed({ ...base, sourceType: 'DXCLUSTER', snr: -30 }, f)).toBe(true);
    });
});

describe('tunables', () => {
    it('matches the settled contract values', () => {
        expect(RING_DEPTH_SECONDS).toBe(6 * 60 * 60);
        expect(RING_MAX_SPOTS).toBe(50_000);
        expect(COVERAGE_GAP_SECONDS).toBeGreaterThan(0);
    });
});

describe('__internals', () => {
    it('exposes the derived-t helper used by parity checks', () => {
        expect(__internals.deriveT(recvBase, 30)).toBe(Math.floor(recvBase / 1000) - 30);
    });

    it('exposes the dedup key builder over full spot identity', () => {
        const a = makeSpot({ recvAge: 7, snr: -12 });
        // Same identity fields and the same derived t (recvMs + 9000 with recvAge
        // advanced by exactly 9s) → same key: the reconnect dump case.
        const b = makeSpot({ recvMs: a.__recvMs + 9000, recvAge: 16, snr: -12 });
        expect(__internals.deriveT(b.__recvMs, b.__recvAge)).toBe(__internals.deriveT(a.__recvMs, a.__recvAge));
        expect(__internals.spotIdentity(b)).toBe(__internals.spotIdentity(a));
    });
});

// --- Unit U7 (plan 2026-09-11-002): full-bundle render parity (R9) ---------
//
// U3's fixture in timeline.test.js already proves the controller path
// (ring-synthesized vs archive-fetched moments) with a fixture whose live and
// archive shapes are identical. This block closes the remaining gap: the
// recorded /api/history bundle carries the FULL historySpot field shape
// (history.go:68-79, 242-253) — sender/receiver included for EVERY source
// type — while the live wire strips sender/receiver for non-dxcluster spots
// (server.go:467-485), so the ring only ever saw them for dxcluster. The
// comparison is therefore render-equivalent, not byte-identical (KTD-5):
// sender/receiver for mqtt/wspr are excluded from the comparison (the
// renderers consume them only in the dxcluster popup), and t is compared with
// a ±2s tolerance because live frames derive t from the receive stamps
// (t = floor(recvMs/1000) − recvAge) and inherit client/server clock skew.
//
// Deliberate choices (both stated in the unit packet):
//   - The recorded bundle is HAND-CONSTRUCTED in exactly the shape fetchBundle
//     produces ({key, t0, t1, spots t-sorted}), not via a fetch mock: the
//     fetch-mock leg is already proven by U3, and the hand-built object lets
//     this fixture pin the exact historySpot field list without DOM wiring.
//   - The ring-synthesized bundle is built the same way tryRingBundle builds
//     it (sessionRing.sliceFiltered over a filter the streamed cohort
//     satisfies), just without the DOM-dependent cohort gate — the gate and
//     the fall-through are U3/AE2 territory, already covered.
describe('render parity: ring-synthesized vs recorded /api/history bundle (U7)', () => {
    // 1-hour window, one spot every 60s, cycling mqtt/wspr/dxcluster. Ages are
    // stamped relative to a notional stream "now" = T0 + 3600, so the pushes
    // model exactly the connect/reconnect dump: a short receive burst whose
    // content spans the last hour in derived t.
    const T0 = 1_730_000_000; // unix seconds (not hour-aligned)
    const HOUR = 60 * 60;
    const NOW = T0 + HOUR;
    // Client/server clock skew in ms, varying per frame — the realistic shape
    // of the skew the ±2s t tolerance exists for. All skews stay well inside
    // the tolerance: derived t drifts by at most 1s from the server t.
    const skewFor = (i) => (i % 5) * 300;

    const makeWireSpot = (t, sourceType, age, i) => {
        // Live-wire shape (server.go toStreamSpot): sender/receiver ONLY on
        // dxcluster spots; app.js stamps __recvMs/__recvAge on every frame.
        const spot = {
            ageSeconds: age,
            lat: 52.5, lng: 13.4, snr: -6,
            locator: 'JO62qm', reporterLocator: 'JO31ab',
            sourceType, band: '20m',
            __recvMs: (t + age) * 1000 + skewFor(i),
            __recvAge: age,
        };
        if (sourceType === 'dxcluster') {
            spot.sender = 'DL1ABC';
            spot.receiver = 'DL9ET';
        }
        return spot;
    };

    // historySpot (history.go:68-79): absolute t, sender/receiver always
    // present (historySpotsFromMessages includes them unconditionally).
    const makeRecordedSpot = (t, sourceType) => ({
        t,
        lat: 52.5, lng: 13.4, snr: -6,
        locator: 'JO62qm', reporterLocator: 'JO31ab',
        sourceType, band: '20m',
        sender: 'DL1ABC', receiver: 'DL9ET',
    });

    const seed = () => {
        sessionRing.clear();
        const recorded = [];
        for (let i = 0; i <= 60; i++) {
            const t = T0 + i * 60;
            const sourceType = ['mqtt', 'wspr', 'dxcluster'][i % 3];
            const age = NOW - t;
            sessionRing.push(makeWireSpot(t, sourceType, age, i));
            recorded.push(makeRecordedSpot(t, sourceType));
        }
        return recorded;
    };

    // renderedKey: the fields the renderers actually consume. sender/receiver
    // participate ONLY for dxcluster (the popup); for mqtt/wspr they are
    // excluded per KTD-5.
    const renderedKey = (s) => {
        const parts = [
            s.sourceType, s.band, s.locator, s.reporterLocator,
            String(s.lat), String(s.lng), String(s.snr),
        ];
        if (s.sourceType === 'dxcluster') parts.push(s.sender, s.receiver);
        return parts.join('|');
    };

    const assertParity = (ringLive, archLive, ph) => {
        const tOf = (s) => ph - s.ageSeconds;
        // Ordering preserved: both moment arrays are t-sorted ascending, and
        // positionally the t's pair within the ±2s tolerance.
        const ringTs = ringLive.map(tOf);
        const archTs = archLive.map(tOf);
        expect(ringTs).toEqual([...ringTs].sort((a, b) => a - b));
        expect(archTs).toEqual([...archTs].sort((a, b) => a - b));
        expect(ringTs).toHaveLength(archTs.length);
        for (let i = 0; i < ringTs.length; i++) {
            expect(Math.abs(ringTs[i] - archTs[i])).toBeLessThanOrEqual(2);
        }
        // Set-identity of rendered inputs: same identity multiset, per-identity
        // t lists within ±2s.
        const collect = (list) => {
            const m = new Map();
            for (const s of list) {
                const k = renderedKey(s);
                if (!m.has(k)) m.set(k, []);
                m.get(k).push(tOf(s));
            }
            return m;
        };
        const mr = collect(ringLive);
        const ma = collect(archLive);
        expect([...mr.keys()].sort()).toEqual([...ma.keys()].sort());
        for (const [k, tr] of mr) {
            const ta = ma.get(k).sort((a, b) => a - b);
            tr.sort((a, b) => a - b);
            expect(tr).toHaveLength(ta.length);
            for (let i = 0; i < tr.length; i++) {
                expect(Math.abs(tr[i] - ta[i])).toBeLessThanOrEqual(2);
            }
        }
    };

    it('moments sliced from the ring bundle and the recorded bundle are render-equivalent (R9)', () => {
        const recorded = seed();

        // Ring-synthesized bundle — built exactly like tryRingBundle does it:
        // {key, t0, t1, spots: sliceFiltered(...)}. The filter below is the
        // widest one (all bands, SNR off) — a cohort the streamed filter
        // trivially satisfies.
        expect(sessionRing.covers(T0, NOW)).toBe(true);
        const ringBundle = {
            key: 'ring-synthesized',
            t0: T0,
            t1: NOW,
            spots: sessionRing.sliceFiltered(T0, NOW, {
                enabledBands: new Set(),
                minSnrMode: 'off',
                ssbMinDb: 0,
                cwMinDb: -15,
            }),
        };

        // Recorded bundle — the exact shape fetchBundle constructs from an
        // /api/history payload (spots sorted by t ascending).
        const recordedBundle = {
            key: 'recorded',
            t0: T0,
            t1: NOW,
            spots: recorded.slice().sort((a, b) => a.t - b.t),
        };

        expect(ringBundle.spots).toHaveLength(recordedBundle.spots.length);

        // Compare moments at several playheads (window end + mid-window): the
        // 15-min trailing slice each time, through the same toLiveSpot the
        // render path consumes.
        for (const playhead of [NOW, T0 + 1800, T0 + 60]) {
            const ringLive = sliceMoment(ringBundle.spots, playhead).map((s) => toLiveSpot(s, playhead));
            const archLive = sliceMoment(recordedBundle.spots, playhead).map((s) => toLiveSpot(s, playhead));
            assertParity(ringLive, archLive, playhead);
        }
    });

    it('the archive leg carries sender/receiver for mqtt/wspr while the ring (live-wire) leg does not — the KTD-5 exclusion is load-bearing', () => {
        const recorded = seed();
        // Sanity: the recorded historySpots are the FULL server shape (the
        // server includes sender/receiver for every source type), so a naive
        // byte-compare of the two legs would fail.
        const recMqtt = recorded.find((s) => s.sourceType === 'mqtt');
        const recWspr = recorded.find((s) => s.sourceType === 'wspr');
        expect(recMqtt.sender).toBe('DL1ABC');
        expect(recWspr.receiver).toBe('DL9ET');

        const ringRaws = sessionRing.slice(0, Infinity);
        const ringMqtt = ringRaws.find((s) => s.sourceType === 'mqtt');
        const ringWspr = ringRaws.find((s) => s.sourceType === 'wspr');
        // The live wire strips sender/receiver for non-dxcluster spots, so the
        // ring (fed from the SSE) never saw them. Matching live rendering.
        expect(ringMqtt.sender).toBeUndefined();
        expect(ringMqtt.receiver).toBeUndefined();
        expect(ringWspr.sender).toBeUndefined();
        expect(ringWspr.receiver).toBeUndefined();

        // Rendered through toLiveSpot at the same playhead: the mqtt/wspr
        // dxcluster-popup inputs differ between the legs and MUST be excluded
        // from the comparison, while the rendered map/tooltip inputs match.
        const playhead = NOW;
        const ringLive = sliceMoment(sessionRing.slice(0, Infinity), playhead).map((s) => toLiveSpot(s, playhead));
        const archLive = sliceMoment(recorded.slice().sort((a, b) => a.t - b.t), playhead).map((s) => toLiveSpot(s, playhead));
        const ringMqttLive = ringLive.find((s) => s.sourceType === 'mqtt');
        const archMqttLive = archLive.find((s) => s.sourceType === 'mqtt');
        expect(archMqttLive.sender).toBe('DL1ABC'); // present in the archive leg…
        expect(ringMqttLive.sender).toBeUndefined(); // …absent in the live leg
        // Yet the rendered inputs the map consumes are identical.
        expect(renderedKey(ringMqttLive)).toBe(renderedKey({ ...archMqttLive, sender: undefined, receiver: undefined }));
    });

    it('dxcluster sender/receiver survive BOTH legs (the popup input is preserved)', () => {
        seed();
        const ringRaw = sessionRing.slice(0, Infinity).find((s) => s.sourceType === 'dxcluster');
        expect(ringRaw.sender).toBe('DL1ABC');
        expect(ringRaw.receiver).toBe('DL9ET');
        const playhead = NOW;
        const live = toLiveSpot(ringRaw, playhead);
        expect(live.sender).toBe('DL1ABC');
        expect(live.receiver).toBe('DL9ET');
    });
});