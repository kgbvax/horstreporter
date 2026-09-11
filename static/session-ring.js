// session-ring.js — client-side session ring for timeline rewind (plan
// 2026-09-11-002, U1): a bounded in-memory ring of immutable raw copies of
// every received live spot, with coverage tracking and a filter re-slice.
//
// Why it exists: the timeline fetches every chunk from /api/history even for
// windows this tab already received, and the live list is pruned in place, so
// the session's own coverage is thrown away. The ring retains it: timeline
// playback over ring-covered 1-hour windows resolves with zero history
// fetches (U3 consults it at the bundle-cache miss), and narrowing filter
// changes re-slice it instead of refetching.
//
// Key contracts (KTD-4..KTD-7, KTD-13 in the plan):
//   - Each push stores a frozen raw copy (historySpot field shape) with an
//     absolute derived time t = floor(recvMs / 1000) - recvAge, computed from
//     the __recvMs/__recvAge stamps app.js sets in onmessage. Those stamps
//     stay fixed while the age-prune loop mutates ageSeconds, so re-delivered
//     spots (reconnect dump) derive the same t and dedup collapses them.
//   - Coverage is tracked in derived spot-time, NOT receive-time intervals:
//     the connect/reconnect history dump's receive interval is a short burst
//     while its content spans up to 60 min earlier in derived t, so
//     receive-time tracking would permanently false-miss the dump-seeded
//     hour. A window is covered iff ring.minT <= t0 and there is no interior
//     gap larger than COVERAGE_GAP_SECONDS in [t0, min(t1, lastT)]; the
//     future-side overhang of the chunk containing "now" is harmless because
//     moment windows never extend beyond now.
//   - Eviction is 2-dimensional on push: spots received more than 6h ago are
//     evicted, plus a count backstop of 50k (mirrors historyMaxSpotCount,
//     history.go) with an oldest-received batch drop (mirrors MAX_LIVE_SPOTS
//     in app.js). Eviction only ever removes a prefix of the receive-order
//     list, so the ring never forgets newer data to remember older data.
//   - The ring stores RAW copies: filtering is applied at slice time, so a
//     narrowed filter re-slices without data loss and widening later restores
//     previously excluded spots. Widening beyond what the server delivered
//     cannot be satisfied locally (server-side filtering) — callers fall
//     through to /api/history for that (KTD-12).

// --- Tunables ------------------------------------------------------------

// Ring depth in receive time: covers the timeline's 6h preset end to end.
export const RING_DEPTH_SECONDS = 6 * 60 * 60;

// Count backstop for hot bands. Mirrors the server's own history ceiling
// (historyMaxSpotCount, history.go:34); the live-list cap MAX_LIVE_SPOTS
// (20000) would evict the 6h preset's tail on an active QTH.
export const RING_MAX_SPOTS = 50_000;

// Coverage gap tolerance in derived spot-time (seconds). Two consecutive
// retained spots whose derived t differs by more than this leave a hole:
// a chunk spanning the hole is a miss and falls through to /api/history.
// 5 minutes covers FT8-cycle spacing, RBN/WSPR bursts and quiet-band lulls;
// real outages are back-filled by the reconnect dump, so only content gaps
// the dump could not fill (or stretches this quiet) create misses.
export const COVERAGE_GAP_SECONDS = 5 * 60;

// --- Pure helpers --------------------------------------------------------

// deriveT converts the stamped receive inputs into the absolute derived spot
// time (seconds). Single source of truth: the parity checks in U7 reuse it.
export function deriveT(recvMs, recvAge) {
    return Math.floor(recvMs / 1000) - recvAge;
}

// spotIdentity builds the full-identity dedup key: sourceType, band, sender,
// receiver, locator, reporterLocator, lat, lng, snr and the derived t. The
// live wire strips sender/receiver for non-dxcluster spots, so a coarser
// band/sender/receiver key would collapse distinct same-second spots during
// FT8/WSPR bursts — hence every distinguishing field participates (the
// spotKey precedent in app.js buckets coarser; this key is finer on purpose).
export function spotIdentity(s) {
    return [
        String(s.sourceType || '').toLowerCase(),
        String(s.band || '').toLowerCase(),
        String(s.sender || '').toUpperCase(),
        String(s.receiver || '').toUpperCase(),
        String(s.locator || '').toUpperCase(),
        String(s.reporterLocator || '').toUpperCase(),
        String(s.lat ?? ''),
        String(s.lng ?? ''),
        String(s.snr ?? ''),
        String(Math.floor(s.t ?? 0)),
    ].join('|');
}

// filterAllowed is a client-side replica of streamClientFilter.spotAllowed
// (server.go:506-528): enabled-band set, then SNR thresholds applied only to
// the mqtt-calibrated source types — empty sourceType counts as
// mqtt-calibrated, as does rbn; dxcluster and wspr are SNR-exempt.
// selectedBand is deliberately NOT applied (KTD-13): /api/history ignores it,
// band focus is client-side.
export function filterAllowed(spot, filter) {
    if (!filter) return true;
    const enabled = filter.enabledBands;
    if (enabled && enabled.size > 0) {
        const band = String(spot.band || '').toLowerCase().trim();
        if (!enabled.has(band)) return false;
    }
    const src = String(spot.sourceType || '').toLowerCase().trim();
    if (src && src !== 'mqtt' && src !== 'rbn') return true; // dxcluster/wspr exempt
    const mode = String(filter.minSnrMode || '').toLowerCase().trim();
    const snr = Number.isFinite(spot.snr) ? spot.snr : 0;
    if (mode === 'ssb') {
        if (snr < filter.ssbMinDb) return false;
    } else if (mode === 'cw') {
        if (snr < filter.cwMinDb) return false;
    }
    return true;
}

// lowerBound finds the first index in the t-sorted array with t >= target.
function lowerBound(arr, target) {
    let lo = 0;
    let hi = arr.length;
    while (lo < hi) {
        const mid = (lo + hi) >>> 1;
        if (arr[mid].t < target) lo = mid + 1;
        else hi = mid;
    }
    return lo;
}

// --- Controller ----------------------------------------------------------

const controller = {
    // byT: entries sorted ascending by derived t (ties keep insertion order).
    byT: [],
    // recv: entries in receive (push) order — eviction drops its prefix.
    recv: [],
    // index: dedup key -> entry.
    index: new Map(),
    // coverage: cached merged [start, end] intervals in derived t, rebuilt
    // from byT on demand (lazy: pushes only set the dirty flag).
    coverage: [],
};

let coverageDirty = true;

function insertByT(entry) {
    const arr = controller.byT;
    // Fast path: live streaming is t-increasing, so appends dominate.
    if (arr.length === 0 || entry.t >= arr[arr.length - 1].t) {
        arr.push(entry);
        return;
    }
    arr.splice(lowerBound(arr, entry.t), 0, entry);
}

function removeFromByT(entry) {
    const arr = controller.byT;
    let i = lowerBound(arr, entry.t);
    // Same-second spots differ in identity, not in t — scan the small run.
    while (i < arr.length && arr[i].t === entry.t) {
        if (arr[i] === entry) {
            arr.splice(i, 1);
            return;
        }
        i++;
    }
}

// evict drops the receive-order prefix: first everything received more than
// RING_DEPTH_SECONDS before the current frame, then a batch back down to the
// count cap (mirrors the MAX_LIVE_SPOTS oldest-arrived batch drop).
function evict(recvNowMs) {
    const cutoffMs = recvNowMs - RING_DEPTH_SECONDS * 1000;
    let n = 0;
    const recv = controller.recv;
    while (n < recv.length && recv[n].recvMs < cutoffMs) n++;
    if (recv.length - n > RING_MAX_SPOTS) n = recv.length - RING_MAX_SPOTS;
    if (n <= 0) return;
    const dropped = recv.splice(0, n);
    for (const entry of dropped) {
        controller.index.delete(entry.key);
        removeFromByT(entry);
    }
    coverageDirty = true;
}

// buildCoverageIntervals merges the retained spots' derived t into maximal
// [start, end] runs whose consecutive gaps stay within COVERAGE_GAP_SECONDS.
function buildCoverageIntervals() {
    const ivs = [];
    for (const entry of controller.byT) {
        const last = ivs[ivs.length - 1];
        if (last && entry.t - last[1] <= COVERAGE_GAP_SECONDS) last[1] = entry.t;
        else ivs.push([entry.t, entry.t]);
    }
    controller.coverage = ivs;
    coverageDirty = false;
    return ivs;
}

function getIntervals() {
    if (coverageDirty) return buildCoverageIntervals();
    return controller.coverage;
}

function push(spot) {
    if (!spot) return false;
    const recvMs = Number.isFinite(spot.__recvMs) ? spot.__recvMs : Date.now();
    const recvAge = Number.isFinite(spot.__recvAge)
        ? spot.__recvAge
        : (Number.isFinite(spot.ageSeconds) ? spot.ageSeconds : 0);
    const t = deriveT(recvMs, recvAge);
    // Immutable raw copy in the historySpot field shape (parity with
    // /api/history bundles); the caller's spot object is never retained.
    const raw = Object.freeze({
        t,
        lat: spot.lat,
        lng: spot.lng,
        snr: spot.snr,
        locator: spot.locator,
        reporterLocator: spot.reporterLocator,
        sourceType: spot.sourceType,
        band: spot.band,
        sender: spot.sender,
        receiver: spot.receiver,
    });
    const key = spotIdentity(raw);
    if (controller.index.has(key)) return false; // reconnect dump re-seed
    const entry = { key, t, recvMs, recvAge, raw };
    controller.index.set(key, entry);
    controller.recv.push(entry);
    insertByT(entry);
    evict(recvMs);
    coverageDirty = true;
    return true;
}

// covers reports whether the exact window [t0, t1] is fully covered by the
// ring (KTD-7): ring.minT <= t0 and no interior gap in
// [t0, min(t1, lastT)]. The future-side overhang past lastT is harmless —
// moment windows never extend beyond now.
function covers(t0, t1) {
    const byT = controller.byT;
    if (byT.length === 0) return false;
    const minT = byT[0].t;
    const lastT = byT[byT.length - 1].t;
    if (minT > t0) return false;
    const upper = Math.min(t1, lastT);
    if (upper < t0) return false;
    let cursor = t0;
    for (const [start, end] of getIntervals()) {
        if (end < cursor) continue;
        if (start > cursor) return false; // interior hole
        cursor = Math.min(end, upper);
        if (cursor >= upper) return true;
    }
    return cursor >= upper;
}

// slice returns the raw copies in [t0, t1] (inclusive bounds, t-sorted
// ascending) — the same ordering an /api/history bundle constructs.
function slice(t0, t1) {
    const byT = controller.byT;
    const out = [];
    for (let i = lowerBound(byT, t0); i < byT.length && byT[i].t <= t1; i++) {
        out.push(byT[i].raw);
    }
    return out;
}

// sliceFiltered is the re-slice: slice + the spotAllowed replica applied at
// slice time (the ring stores raw copies, so narrowing loses nothing and a
// later widening slice restores previously excluded spots).
function sliceFiltered(t0, t1, filter) {
    return slice(t0, t1).filter((s) => filterAllowed(s, filter));
}

function clear() {
    controller.byT = [];
    controller.recv = [];
    controller.index = new Map();
    controller.coverage = [];
    coverageDirty = true;
}

function stats() {
    const byT = controller.byT;
    return {
        count: byT.length,
        minT: byT.length ? byT[0].t : null,
        lastT: byT.length ? byT[byT.length - 1].t : null,
    };
}

// --- Public API ----------------------------------------------------------

export const sessionRing = {
    push,
    covers,
    slice,
    sliceFiltered,
    clear,
    stats,
};

// Expose internals for vitest (importers in tests use these exports).
export const __internals = {
    controller,
    deriveT,
    spotIdentity,
    buildCoverageIntervals,
    lowerBound,
};