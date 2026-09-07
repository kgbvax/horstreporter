// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { state } from './state.js';
import { getLoopRange, snapRangeMarker } from './timetravel.js';

// The shared loop range (rt.rangeStart/rangeEnd) backs the time-travel
// markers + From/To AND the video export panel — test it while replay is
// INACTIVE: that's the state the export panel always edits it in.

const NOW = new Date('2026-09-07T12:34:56Z').getTime();
const nowSec = () => Math.floor(NOW / 1000);
const bucketEnd = () => Math.floor(nowSec() / 1800) * 1800 + 1800;
const DAY = 86400;

beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    Object.assign(state.timeTravel, {
        active: false,
        rangeStart: 0,
        rangeEnd: 0,
        start: 0,
        end: 0,
        currentBucketEnd: 0,
    });
});

afterEach(() => {
    vi.useRealTimers();
});

describe('getLoopRange', () => {
    it('defaults to the most recent 12h ending on the bucket grid', () => {
        const r = getLoopRange();
        expect(r.end).toBe(bucketEnd());
        expect(r.start).toBe(bucketEnd() - 12 * 3600);
    });

    it('returns the existing range unchanged once set', () => {
        state.timeTravel.rangeStart = 10000;
        state.timeTravel.rangeEnd = 20000;
        expect(getLoopRange()).toEqual({ start: 10000, end: 20000 });
    });
});

// The export controls no longer have own From/To inputs — the clip range IS
// the loop range — so the invariant that matters: a range picked outside an
// active replay session survives entering/leaving time travel intact.
describe('loop range across replay enter/exit', () => {
    beforeEach(() => {
        vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ spots: [], buckets: [] }) })));
        state.liveSpots = [];
    });

    it('enterTimeTravel keeps a valid existing range (the video export case)', async () => {
        const tt = state.timeTravel;
        tt.rangeStart = bucketEnd() - 7200;
        tt.rangeEnd = bucketEnd() - 3600;
        const { enterTimeTravel, exitTimeTravel } = await import('./timetravel.js');
        await enterTimeTravel();
        expect(tt.rangeStart).toBe(bucketEnd() - 7200);
        expect(tt.rangeEnd).toBe(bucketEnd() - 3600);
        exitTimeTravel();
    });

    it('enterTimeTravel falls back to the default window for an out-of-extent range', async () => {
        const tt = state.timeTravel;
        tt.rangeStart = bucketEnd() - 100 * 3600; // beyond the 48h extent
        tt.rangeEnd = bucketEnd() - 99 * 3600;
        const { enterTimeTravel, exitTimeTravel } = await import('./timetravel.js');
        await enterTimeTravel();
        expect(tt.rangeEnd).toBe(bucketEnd());
        expect(tt.rangeStart).toBe(Math.max(tt.start, bucketEnd() - 12 * 3600));
        exitTimeTravel();
    });
});

describe('snapRangeMarker (pure)', () => {
    const extent = [0, 48 * 3600];
    it('start marker keeps min span below the end marker', () => {
        expect(snapRangeMarker('start', 40000, 0, 40200, ...extent, 1800)).toBe(40200 - 3600);
    });
    it('end marker cannot pass the extent end', () => {
        expect(snapRangeMarker('end', 99999999, 0, 1000, ...extent, 1800)).toBe(48 * 3600);
    });
});
