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