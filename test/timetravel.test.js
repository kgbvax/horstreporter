import { describe, it, expect, beforeEach, vi } from 'vitest';
import { state } from '../static/state.js';

// timetravel.js reads the DOM per call and talks to /api/replay/*; tests stub
// fetch and mount the minimal overlay markup, following the house jsdom style.
const { initTimeTravel, enterTimeTravel, exitTimeTravel, isReplayActive, notifyFiltersChanged, loadBucket, snapRangeMarker, advancePlayback } =
    await import('../static/timetravel.js');

function mountOverlay() {
    document.body.innerHTML = `
        <button id="timetravel-toggle"></button>
        <div id="timetravel-bar" class="is-hidden"></div>
        <div id="timetravel-track">
            <div id="timetravel-shade-start"></div>
            <div id="timetravel-shade-end"></div>
            <div id="timetravel-histogram"></div>
            <input id="timetravel-scrubber" />
            <div id="timetravel-marker-start"></div>
            <div id="timetravel-marker-end"></div>
        </div>
        <span id="timetravel-label"></span>
        <button id="timetravel-play"></button>
        <input id="timetravel-from" />
        <input id="timetravel-to" />
        <input id="timetravel-speed" />
        <div id="wspr-matrix-window"></div>
        <div id="current-band-display"></div>
        <input id="surroundings" type="checkbox" />
        <input id="qth" value="JO62QM" />
    `;
}

function bucketResponse(count = 2, truncated = false) {
    return {
        qth: 'JO62QM', surroundings: false, bucket_end: 0, bucket_seconds: 1800,
        count, truncated,
        spots: Array.from({ length: count }, (_, i) => ({
            lat: 50 + i, lng: 8 + i, snr: -5, ageSeconds: 60 * (i + 1),
            locator: 'JO62', band: '20m', sourceType: 'mqtt',
        })),
    };
}

const histResponse = {
    start: 0, end: 0, bucket_seconds: 1800, total: 3,
    buckets: [{ t: 0, count: 1 }, { t: 1800, count: 2 }],
};

describe('time travel', () => {
    let fetchMock;
    beforeEach(() => {
        vi.restoreAllMocks();
        mountOverlay();
        state.liveSpots = [{ ageSeconds: 10, band: '20m', __recvMs: Date.now(), __recvAge: 10 }];
        state.qth = 'JO62QM';
        fetchMock = vi.fn(async (url) => {
            if (url.includes('/api/replay/histogram')) {
                return { ok: true, json: async () => histResponse };
            }
            return { ok: true, json: async () => bucketResponse(2) };
        });
        vi.stubGlobal('fetch', fetchMock);
    });

    it('starts inactive', () => {
        expect(isReplayActive()).toBe(false);
    });

    it('enter swaps liveSpots for a replay array and fetches a bucket', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        expect(isReplayActive()).toBe(true);
        expect(state.liveSpots).not.toBe(state.timeTravel.liveSpotsBackup);
        expect(state.timeTravel.liveSpotsBackup).toHaveLength(1);
        expect(state.liveSpots).toHaveLength(2); // from bucketResponse
        expect(state.liveSpots[0].__replay).toBe(true);
        expect(state.liveSpots[0].__bucketEnd).toBe(state.timeTravel.currentBucketEnd);
        expect(document.getElementById('timetravel-bar').classList.contains('is-hidden')).toBe(false);
        await exitTimeTravel();
    });

    it('exit restores the live list and hides the overlay', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        const backup = state.timeTravel.liveSpotsBackup;
        await exitTimeTravel();
        expect(isReplayActive()).toBe(false);
        expect(state.liveSpots).toBe(backup);
        expect(state.liveSpots).toHaveLength(1);
        expect(document.getElementById('timetravel-bar').classList.contains('is-hidden')).toBe(true);
    });

    it('replaying buckets never mutates the backup list', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        const backup = state.timeTravel.liveSpotsBackup;
        // simulate a second bucket install overwriting the replay array in place
        state.liveSpots.length = 0;
        state.liveSpots.push({ ageSeconds: 5, __replay: true });
        expect(backup).toHaveLength(1);
        expect(backup[0].__replay).toBeUndefined();
        await exitTimeTravel();
        expect(state.liveSpots[0].ageSeconds).toBe(10);
    });

    it('buckets are cached and reused (LRU)', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        const bucketCalls = () => fetchMock.mock.calls.filter(([u]) => u.includes('/api/replay/spots')).length;
        const first = bucketCalls();
        const end = state.timeTravel.currentBucketEnd;
        // Same bucket again: served from cache, no new fetch.
        await loadBucket(end);
        expect(bucketCalls()).toBe(first);
        // An earlier in-range bucket: a new fetch.
        await loadBucket(end - 1800);
        expect(bucketCalls()).toBe(first + 1);
        await exitTimeTravel();
    });

    it('filter changes invalidate the bucket cache', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        notifyFiltersChanged();
        expect(state.timeTravel.bucketCache.size).toBe(0);
        await exitTimeTravel();
    });

    it('exiting before any stream restores an empty live array for SSE to fill', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        state.timeTravel.liveSpotsBackup = null;
        state.timeTravel.active = true;
        state.liveSpots = [{ ageSeconds: 1, __replay: true }];
        await exitTimeTravel();
        expect(isReplayActive()).toBe(false);
        expect(state.liveSpots).toEqual([]);
    });

    it('snapRangeMarker snaps to the bucket grid and keeps markers apart', () => {
        const extent = [0, 48 * 3600];
        const bs = 1800;
        // Snaps to the nearest bucket boundary.
        expect(snapRangeMarker('start', 1000, 0, extent[1], ...extent, bs)).toBe(1800);
        expect(snapRangeMarker('start', 2800, 0, extent[1], ...extent, bs)).toBe(3600);
        // Start never crosses past the end marker (min span 2 buckets).
        expect(snapRangeMarker('start', extent[1], 0, 4 * bs, ...extent, bs)).toBe(4 * bs - 2 * bs);
        // End never crosses before the start marker.
        expect(snapRangeMarker('end', 0, 0, 6 * bs, ...extent, bs)).toBe(2 * bs);
        // Both stay inside the data extent.
        expect(snapRangeMarker('end', extent[1] + 9e6, 0, extent[1], ...extent, bs)).toBe(extent[1]);
    });

    it('playback wraps from the range end back to the range start', async () => {
        initTimeTravel({ scheduleRender: () => {} });
        await enterTimeTravel();
        const rt = state.timeTravel;
        // Shrink the loop range away from the data extent.
        rt.rangeStart = rt.start + 2 * rt.bucketSeconds;
        rt.rangeEnd = rt.start + 5 * rt.bucketSeconds;
        await loadBucket(rt.rangeEnd); // playhead sits at the range end
        const calls = () => fetchMock.mock.calls.filter(([u]) => u.includes('/api/replay/spots')).length;
        const before = calls();
        advancePlayback();
        expect(rt.currentBucketEnd).toBe(rt.rangeStart); // wrapped, not stopped
        expect(calls()).toBe(before + 1); // a wrap triggers a bucket load
        await exitTimeTravel();
    });

    it('the top-right display shows replay time during playback and is restored on exit', async () => {
        const restore = vi.fn();
        initTimeTravel({ scheduleRender: () => {}, updateBandDisplay: restore });
        await enterTimeTravel();
        const display = document.getElementById('current-band-display');
        expect(display.textContent).toMatch(/^Replay /);
        await exitTimeTravel();
        expect(restore).toHaveBeenCalled();
    });
});