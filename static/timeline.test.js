// timeline.test.js — unit tests for the Time Travel controller's pure logic:
// moment slicing, live-shape conversion, snapping, LRU, key canonicalization,
// and URL round-trip. DOM-dependent controller paths stay untested here (they
// run under the browser).

import { describe, it, expect } from 'vitest';

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
} from './timeline.js';

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