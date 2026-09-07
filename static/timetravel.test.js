// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { state } from './state.js';
import { getLoopRange, applyExternalRange, snapRangeMarker } from './timetravel.js';

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

describe('applyExternalRange (replay inactive)', () => {
    // Same 30-min snap grid as the time-travel markers — the range must not
    // visibly jump when switching between the export panel and time travel.
    it('snaps to the shared 30-min bucket grid', () => {
        const end = bucketEnd();
        // Odd minutes: off the grid; must land on 1800s boundaries.
        applyExternalRange(end - 3600 + 61, end - 1800 + 37);
        const r = getLoopRange();
        expect(r.start % 1800).toBe(0);
        expect(r.end % 1800).toBe(0);
        expect(Math.abs(r.start - (end - 3600))).toBeLessThanOrEqual(1800);
        expect(Math.abs(r.end - (end - 1800))).toBeLessThanOrEqual(1800);
    });

    it('enforces the shared min span of two buckets (1h)', () => {
        const end = bucketEnd();
        applyExternalRange(end - 1800, end); // only 30 min apart
        const r = getLoopRange();
        expect(r.end - r.start).toBeGreaterThanOrEqual(2 * 1800);
    });

    it('clamps to the rolling 48h extent', () => {
        const end = bucketEnd();
        applyExternalRange(end - 100 * 3600, end);
        const r = getLoopRange();
        expect(r.start).toBeGreaterThanOrEqual(end - 2 * DAY);
    });

    it('rejects non-finite input by keeping the current side', () => {
        const before = getLoopRange();
        applyExternalRange(0, NaN);
        expect(getLoopRange()).toEqual(before);
    });

    it('updates the export From/To inputs via syncRangeInputs', () => {
        const from = document.createElement('input');
        from.id = 'videoexport-from';
        const to = document.createElement('input');
        to.id = 'videoexport-to';
        document.body.append(from, to);
        const end = bucketEnd();
        applyExternalRange(end - 7200, end - 3600);
        expect(from.value).toBeTruthy();
        expect(to.value).toBeTruthy();
        expect(new Date(from.value).getTime() / 1000).toBe(getLoopRange().start);
        expect(new Date(to.value).getTime() / 1000).toBe(getLoopRange().end);
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
