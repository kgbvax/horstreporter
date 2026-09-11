// timeline.js — Time Travel controller: fetches a /api/history bundle for a
// (qth, filters, t0..t1) window, slices trailing 15-min "moments" from it as
// the playhead moves, animates through the window (speeds in data-time
// multipliers), and drives the map renderers with spot arrays shaped exactly
// like live spots (ageSeconds derived per playhead) so the existing render
// path works unmodified.
//
// Architecture:
//   - One bundle per (qth, filters, range) — the heavy 24h fetch happens once,
//     cached in an LRU; scrubbing is pure client-side slicing.
//   - The playhead emits moments as {playhead, spots}; app.js subscribes and
//     feeds them into the normal render pipeline while the live stream is
//     soft-paused.
//   - Afterglow is NOT here: static/afterglow.js subscribes to the same
//     playhead feed and draws the decaying canvas overlay.

import { getMinSnrMode, getSelectedBand, getEnabledBands } from './utils.js';
import { state } from './state.js';
import { sessionRing } from './session-ring.js';

// --- Tunables ------------------------------------------------------------

// Trailing window: a moment at playhead T shows spots with T-15min <= t <= T.
export const MOMENT_WINDOW_SECONDS = 15 * 60;

// Play speeds: data-time multiplier (24h at 600x = 2.4 min). 0 = paused.
export const TIMELINE_SPEEDS = [60, 240, 600];

// Default play speed.
export const DEFAULT_SPEED = 240;

// Scrub snaps to 5-minute boundaries (the spec's snap decision).
export const SCRUB_SNAP_SECONDS = 5 * 60;

// Range presets (seconds).
export const RANGE_PRESETS = [
    { label: '1h', seconds: 60 * 60 },
    { label: '6h', seconds: 6 * 60 * 60 },
    { label: '24h', seconds: 24 * 60 * 60 },
];

// Bundle LRU: keep the last N fetched chunks (keyed by exact [t0,t1] window +
// filters). 3 one-hour chunks ≈ 3 × ~10k spots ≈ a few MB — bounded memory.
const BUNDLE_CACHE_MAX = 4;

// Fetch chunk size: bundles are fetched per contiguous 1-hour slice. Scrubbing
// past a chunk edge fetches that hour on demand (fast: one indexed Postgres
// query + a few thousand spots); playing into an uncovered stretch prefetches
// the next chunk ahead of the playhead. Never one giant [t0..t1] fetch: a 24h
// span materializes ~240k rows server-side and would blow the 50k bundle cap,
// hiding all the deep hours the user just asked for.
const CHUNK_SECONDS = 60 * 60;

// While playing, start fetching the next chunk when the playhead comes within
// this distance of the current chunk's t1 edge (at 600x that's 90 s of
// wall-clock; a chunk fetch completes well inside that on prod).
const PREFETCH_AHEAD_SECONDS = 15 * 60;

// RAF tick advances the playhead by (dt * speed); a tick per animation frame.
// Playhead updates render at ~24fps equivalent — the map render path is
// fingerprint-gated so stationary playheads cost nothing.
const PLAY_TICK_MS = 1000 / 24;

// --- Controller state ----------------------------------------------------

const controller = {
    active: false,
    qth: '',
    t0: 0,
    t1: 0,          // absolute end of the timeline range (wall-clock anchored)
    playhead: 0,    // current moment (unix seconds)
    speed: DEFAULT_SPEED,
    playing: false,
    loop: false,
    rafId: 0,
    lastTickAt: 0,
    bundle: null,   // { key, t0, t1, spots: [{t,...}] sorted by t asc }
    bundles: new Map(), // LRU: key -> {t0, t1, spots} (keyed by exact chunk window)
    bundleOrder: [],
    inflight: null, // AbortController of the most recent fetch
    inflightKeys: new Map(), // chunk key -> in-flight fetch promise (dedup)
    loading: false,
    reach: Infinity, // server-advertised oldest servable time (Infinity w/ archive)
    listeners: { moment: [], status: [], exit: null },
};

// --- Pure helpers (exported for tests) -----------------------------------

// snapToScrub rounds a unix second to the nearest SCRUB_SNAP_SECONDS boundary.
export function snapToScrub(t) {
    const v = Math.round(t / SCRUB_SNAP_SECONDS) * SCRUB_SNAP_SECONDS;
    return v === 0 ? 0 : v; // normalize -0 (round-half-up at t = -S/2)
}

// clampPlayhead keeps a moment inside [t0, t1] of the active range.
export function clampPlayhead(t, t0, t1) {
    if (t < t0) return t0;
    if (t > t1) return t1;
    return t;
}

// sliceMoment returns the spots with t in [T - MOMENT_WINDOW_SECONDS, T],
// via binary search over the chronologically sorted bundle. Returns a new
// array (callers may mutate); O(log n + k).
export function sliceMoment(spots, T) {
    const lo = T - MOMENT_WINDOW_SECONDS;
    // First index with t >= lo (lower bound).
    let a = 0, b = spots.length;
    while (a < b) {
        const mid = (a + b) >> 1;
        if (spots[mid].t < lo) a = mid + 1; else b = mid;
    }
    const start = a;
    // First index with t > T (upper bound), scanning forward from start.
    let end = start;
    while (end < spots.length && spots[end].t <= T) end++;
    return spots.slice(start, end);
}

// toLiveSpot converts a history bundle spot into the live-spot shape the
// renderers already consume: ageSeconds is derived from the playhead (not
// wall clock) so the age-graded rendering looks identical to live.
export function toLiveSpot(s, playhead) {
    const age = Math.max(0, playhead - s.t);
    return {
        lat: s.lat,
        lng: s.lng,
        snr: s.snr,
        ageSeconds: age,
        locator: s.locator,
        reporterLocator: s.reporterLocator,
        sourceType: s.sourceType,
        band: s.band,
        sender: s.sender,
        receiver: s.receiver,
    };
}

// bundleKey canonicalizes (qth, filters, exact window) into an LRU key. The
// window is part of the key: chunks are distinct cache entries (a 24h session
// walks through 24 of them), while a filter change invalidates all of them the
// same way it restarts the live stream.
export function bundleKey(qth, surroundings, rings, minSnrMode, ssbMinDb, cwMinDb, enabledBands, t0, t1) {
    return [
        qth, surroundings ? 's1' : 's0', rings, minSnrMode, ssbMinDb, cwMinDb,
        [...enabledBands].sort().join(','), Math.floor(t0), Math.floor(t1),
    ].join('|');
}

// nextPrefetchAt: while playing toward t1, when should the next chunk
// prefetch fire? When the playhead comes within PREFETCH_AHEAD of the
// bundle's t1. Returns 0 when prefetch is pointless (the bundle already
// reaches the range edge).
export function nextPrefetchAt(bundleT1, t1) {
    if (bundleT1 >= t1 - 1) return 0;
    return bundleT1 - PREFETCH_AHEAD_SECONDS;
}

// --- Controller ----------------------------------------------------------

function emitStatus() {
    const payload = {
        active: controller.active,
        loading: controller.loading,
        playing: controller.playing,
        t0: controller.t0,
        t1: controller.t1,
        playhead: controller.playhead,
        speed: controller.speed,
        loop: controller.loop,
        reach: controller.reach,
    };
    controller.listeners.status.forEach((fn) => { try { fn(payload); } catch (_) { /* listener error */ } });
    renderBar(payload);
}

function emitMoment(spots, playhead) {
    const live = spots.map((s) => toLiveSpot(s, playhead));
    controller.listeners.moment.forEach((fn) => { try { fn(live, playhead); } catch (_) { /* listener error */ } });
}

// currentFilterState reads the same DOM controls the live stream uses so past
// moments match live semantics byte-for-byte.
function currentFilterState() {
    const qth = (document.getElementById('qth')?.value || '').trim().toUpperCase();
    const surroundings = document.getElementById('surroundings')?.checked === true;
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const enabledBands = getEnabledBands();
    const selectedBand = getSelectedBand();
    return { qth, surroundings, minSnrMode, ssbMinDb, cwMinDb, enabledBands, selectedBand };
}

function bundleCacheKey(t0, t1) {
    const f = currentFilterState();
    const rings = 0; // rings is a region-feed param, not a UI control here
    return bundleKey(f.qth, f.surroundings, rings, f.minSnrMode, f.ssbMinDb, f.cwMinDb, f.enabledBands, t0, t1);
}

function getBundle(key) {
    const b = controller.bundles.get(key);
    if (b) {
        // LRU touch.
        const idx = controller.bundleOrder.indexOf(key);
        if (idx >= 0) controller.bundleOrder.splice(idx, 1);
        controller.bundleOrder.push(key);
    }
    return b;
}

function putBundle(key, bundle) {
    if (controller.bundles.has(key)) {
        const idx = controller.bundleOrder.indexOf(key);
        if (idx >= 0) controller.bundleOrder.splice(idx, 1);
    }
    controller.bundles.set(key, bundle);
    controller.bundleOrder.push(key);
    while (controller.bundleOrder.length > BUNDLE_CACHE_MAX) {
        const oldest = controller.bundleOrder.shift();
        if (oldest && controller.bundles.get(oldest) !== bundle) controller.bundles.delete(oldest);
    }
}

// fetchBundle loads /api/history for a chunk window and caches it. Returns the
// bundle; throws on HTTP errors. In-flight dedup: two requests for the same
// window share one fetch (scrub + play prefetch can race onto the same chunk).
// Transient gate rejections (503 "history endpoint busy") retry a few times —
// the server caps concurrent history queries globally, and a scrubbing session
// legitimately overlaps a couple of chunk fetches.
const GATE_RETRIES = 4;
const GATE_RETRY_MS = 400;

async function fetchBundle(t0, t1) {
    const key = bundleCacheKey(t0, t1);
    const cached = getBundle(key);
    if (cached) return cached;
    if (controller.inflightKeys.has(key)) return controller.inflightKeys.get(key);

    const f = currentFilterState();
    const params = new URLSearchParams();
    params.set('qth', f.qth);
    params.set('t0', String(Math.floor(t0)));
    params.set('t1', String(Math.floor(t1)));
    if (f.surroundings) params.set('surroundings', 'true');
    if (f.minSnrMode && f.minSnrMode !== 'off') {
        params.set('min_snr_mode', f.minSnrMode);
        params.set('ssb_min_db', String(f.ssbMinDb));
        params.set('cw_min_db', String(f.cwMinDb));
    }
    if (f.enabledBands && f.enabledBands.size > 0) {
        params.set('enabled_bands', [...f.enabledBands].join(','));
    }
    if (f.selectedBand && f.selectedBand !== 'all') {
        params.set('selected_band', f.selectedBand);
    }

    const ac = new AbortController();
    const promise = (async () => {
        let resp;
        for (let attempt = 0; ; attempt++) {
            resp = await fetch(`/api/history?${params.toString()}`, { signal: ac.signal });
            if (resp.ok) break;
            if (resp.status === 503 && attempt < GATE_RETRIES) {
                // Concurrency gate busy — back off and retry.
                await new Promise((r) => setTimeout(r, GATE_RETRY_MS * (attempt + 1)));
                continue;
            }
            let msg = '';
            try { msg = await resp.text(); } catch (_) { /* body read race */ }
            throw new Error(`history fetch failed (${resp.status}): ${msg || resp.statusText}`);
        }
        const payload = await resp.json();
        const reachHeader = resp.headers.get('X-History-Reach');
        if (reachHeader) {
            const r = parseInt(reachHeader, 10);
            if (Number.isFinite(r) && r > 0) controller.reach = r;
        }
        const bundle = {
            key,
            t0: payload.t0,
            t1: payload.t1,
            spots: (payload.spots || []).slice().sort((a, b) => a.t - b.t),
        };
        putBundle(key, bundle);
        return bundle;
    })();
    promise.catch(() => { /* unhandled-rejection guard; callers await it */ });
    controller.inflightKeys.set(key, promise);
    try {
        return await promise;
    } finally {
        controller.inflightKeys.delete(key);
        controller.inflight = ac;
    }
}

// ringCohortSatisfies: can the session ring answer the current UI filter? The
// ring holds only what the live server stream delivered (session-ring.js is
// fed from the SSE onmessage hook), so a bundle may be synthesized from it
// only when the current filter is a narrowing (or equal) of the filter the
// stream actually fetched — state.streamedFilter (app.js). Widening (enabling
// an unseen band, lowering an SNR threshold, changing the SNR mode) asks for
// data the server never delivered; serving it from the ring would render a
// silent hole, so it falls through to /api/history (AE2, KTD-12). The qth
// guard matches the cohort too: app.js clears the ring on a qth change, so a
// qth the timeline was not entered with is foreign.
function ringCohortSatisfies(f) {
    const sf = state.streamedFilter;
    if (!sf) return false;
    if ((f.qth || '') !== controller.qth) return false;
    if (String(f.minSnrMode || 'none') !== String(sf.minSnrMode || 'none')) return false;
    // An empty enabled-band set means "all bands" (the fetch path omits the
    // param) — that is the widest filter, never a narrowing.
    if (f.enabledBands.size === 0) return false;
    for (const band of f.enabledBands) {
        if (!sf.bands.has(band)) return false;
    }
    // Raising an SNR threshold narrows; lowering it widens (the raised
    // threshold would exclude spots the stream delivered).
    if (f.minSnrMode === 'ssb' && f.ssbMinDb < parseInt(sf.ssbMinDb ?? '0', 10)) return false;
    if (f.minSnrMode === 'cw' && f.cwMinDb < parseInt(sf.cwMinDb ?? '-15', 10)) return false;
    return true;
}

// tryRingBundle serves a chunk window from the session ring (plan U3): a
// zero-fetch stand-in for fetchBundle, shaped exactly like the bundle
// fetchBundle constructs (key, t0, t1, spots sorted by t ascending) so
// sliceMoment and toLiveSpot work unmodified. Returns null when the ring
// cannot serve the window — uncovered (below ring.minT or an interior gap,
// KTD-7), a filter wider than the delivered cohort, or a foreign qth — and
// the caller falls through to the archive path unchanged. Never touches
// controller.inflightKeys: a ring-served chunk needs no dedup (it resolves
// synchronously) and must not interfere with an already-in-flight fetch.
function tryRingBundle(chunkT0, chunkT1, key) {
    const f = currentFilterState();
    if (!ringCohortSatisfies(f)) return null;
    if (!sessionRing.covers(chunkT0, chunkT1)) return null;
    const bundle = {
        key,
        t0: chunkT0,
        t1: chunkT1,
        spots: sessionRing.sliceFiltered(chunkT0, chunkT1, {
            enabledBands: f.enabledBands,
            minSnrMode: f.minSnrMode,
            ssbMinDb: f.ssbMinDb,
            cwMinDb: f.cwMinDb,
        }),
    };
    putBundle(key, bundle);
    return bundle;
}

// chunkBoundsFor returns the [t0,t1] of the 1-hour fetch chunk covering time t
// within the active range: [floor(t/1h)*1h, +1h), clamped to the range.
export function chunkBoundsFor(t, rangeT0, rangeT1) {
    const snapped = snapToScrub(t);
    let chunkT0 = Math.floor(snapped / CHUNK_SECONDS) * CHUNK_SECONDS;
    if (chunkT0 < rangeT0) chunkT0 = rangeT0;
    let chunkT1 = chunkT0 + CHUNK_SECONDS;
    if (chunkT1 > rangeT1) chunkT1 = rangeT1;
    if (chunkT1 <= chunkT0) chunkT1 = chunkT0 + 1;
    return [chunkT0, chunkT1];
}

// ensurePlayheadBundle guarantees controller.bundle covers the current
// playhead, fetching its chunk on demand — or serving it from the session
// ring when the chunk lies inside the session's own received coverage
// (zero-fetch rewind; the only /api/history fetch gateway is below, so seek,
// play ticks and prefetch all consult the ring through this single path).
async function ensurePlayheadBundle() {
    const t = controller.playhead;
    const [chunkT0, chunkT1] = chunkBoundsFor(t, controller.t0, controller.t1);
    const key = bundleCacheKey(chunkT0, chunkT1);
    const b = getBundle(key) || (controller.bundle && controller.bundle.key === key ? controller.bundle : null);
    if (b) {
        controller.bundle = b;
        return b;
    }
    // Ring consult (plan 2026-09-11-002 U3): the ring handles the non-hour-
    // aligned clamped edges (first/last chunk of a range) and the future-side
    // overhang of the chunk containing "now" via its coverage contract. On a
    // hit the bundle is synthesized (filter applied at slice time, so
    // narrowing re-slices without refetching) and put into the LRU like a
    // fetched one; controller.loading stays untouched — no loading UI for a
    // synchronous local slice.
    const rb = tryRingBundle(chunkT0, chunkT1, key);
    if (rb) {
        controller.bundle = rb;
        return rb;
    }
    controller.loading = true;
    emitStatus();
    try {
        const nb = await fetchBundle(chunkT0, chunkT1);
        controller.bundle = nb;
        return nb;
    } finally {
        controller.loading = false;
        emitStatus();
    }
}

function emitCurrentMoment() {
    const b = controller.bundle;
    if (!b) return;
    const spots = sliceMoment(b.spots, controller.playhead);
    controller.listeners.moment.forEach((fn) => { try { fn(spots.map((s) => toLiveSpot(s, controller.playhead)), controller.playhead); } catch (_) { /* listener error */ } });
    renderBar({
        active: true, loading: controller.loading, playing: controller.playing,
        t0: controller.t0, t1: controller.t1, playhead: controller.playhead,
        speed: controller.speed, loop: controller.loop, reach: controller.reach,
    });
}

function playTick(ts) {
    if (!controller.playing) return;
    const nowMs = typeof ts === 'number' ? ts : performance.now();
    if (controller.lastTickAt > 0) {
        const dtSec = Math.min((nowMs - controller.lastTickAt) / 1000, 5);
        controller.playhead += dtSec * controller.speed;
        if (controller.playhead >= controller.t1) {
            if (controller.loop) {
                controller.playhead = controller.t0;
            } else {
                controller.playhead = controller.t1;
                controller.playing = false;
                emitStatus();
            }
        }
        // Crossed into an uncovered chunk → fetch it before emitting (the
        // moment would otherwise be empty for the chunk's whole first hour).
        const b = controller.bundle;
        const covered = b && b.t0 <= controller.playhead && controller.playhead <= b.t1;
        if (!covered) {
            void ensurePlayheadBundle().then(() => { if (controller.playing) emitCurrentMoment(); }).catch(() => { /* keep animating; next tick retries */ });
        } else {
            emitCurrentMoment();
        }
        maybePrefetch();
    }
    controller.lastTickAt = nowMs;
    if (controller.playing) controller.rafId = requestAnimationFrame(playTick);
}

// maybePrefetch: while playing, fetch the NEXT chunk (or the current one, if
// somehow uncovered) once the playhead is within PREFETCH_AHEAD of the chunk
// edge — so playback at 600x crosses edges without a visible loading pause.
function maybePrefetch() {
    const b = controller.bundle;
    if (!b || controller.loading) return;
    if (b.t1 < controller.t1 && controller.t1 - b.t1 >= PREFETCH_AHEAD_SECONDS && controller.playhead >= b.t1 - PREFETCH_AHEAD_SECONDS) {
        void ensurePlayheadBundle().catch(() => { /* prefetch failure is non-fatal */ });
    }
}

// --- Public API ----------------------------------------------------------

export function onMoment(fn) { controller.listeners.moment.push(fn); }
export function onStatus(fn) { controller.listeners.status.push(fn); }

// onExit registers a callback invoked when the user leaves timeline mode via
// the bar's Live button — app.js uses it to restore the live stream. (Calling
// exitTimeline() directly still works for programmatic exits; the hook only
// covers user-initiated exits through the UI.)
export function onExit(fn) { controller.listeners.exit = fn; }

export function isTimelineActive() { return controller.active; }

// enterTimeline switches the app from live streaming to time travel: the live
// stream soft-pauses (app.js owns SSE lifecycle; this only flips the flag),
// the timeline bar shows, and the initial window is loaded + rendered.
export async function enterTimeline(rangeSeconds = 60 * 60) {
    if (controller.active) return;
    const f = currentFilterState();
    if (!f.qth) return;

    controller.active = true;
    controller.qth = f.qth;
    controller.t1 = Math.floor(Date.now() / 1000);
    controller.t0 = controller.t1 - rangeSeconds;
    controller.playhead = controller.t1;
    controller.playing = false;
    controller.reach = Infinity;
    // Stale chunks from a previous session (older qth/filters/windows) must
    // not leak into this one: the LRU keys on filters+window, but a same-key
    // chunk with different filter values can't exist (key includes filters) —
    // clearing is still the safe, cheap choice on every fresh entry.
    controller.bundle = null;
    controller.bundles.clear();
    controller.bundleOrder = [];
    controller.inflightKeys.clear();

    document.body.classList.add('timeline-active');
    emitStatus();
    try {
        await ensurePlayheadBundle();
    } catch (err) {
        if ((err?.name || '') === 'AbortError') return;
        controller.active = false;
        document.body.classList.remove('timeline-active');
        emitStatus();
        throw err;
    }
    if (!controller.active) return; // exited mid-load
    emitCurrentMoment();
}

// exitTimeline restores live mode: stops playback, clears the bar, drops
// bundles (they are re-fetchable; holding them across sessions risks stale
// qth semantics). The session ring is NOT wiped here: it holds the raw
// received cohort, not qth/filter-scoped bundles — only bundles are stale.
export function exitTimeline() {
    if (!controller.active) return;
    controller.playing = false;
    cancelAnimationFrame(controller.rafId);
    controller.rafId = 0;
    controller.inflight?.abort();
    controller.active = false;
    controller.bundle = null;
    controller.bundles.clear();
    controller.bundleOrder = [];
    controller.inflightKeys.clear();
    document.body.classList.remove('timeline-active');
    emitStatus();
}

// seek moves the playhead (snapped) and emits the moment at it.
export async function seek(t) {
    if (!controller.active) return;
    const snapped = snapToScrub(clampPlayhead(t, controller.t0, controller.t1));
    controller.playhead = snapped;
    await ensurePlayheadBundle();
    emitCurrentMoment();
}

// play starts (or resumes) animation; pause stops it in place.
export function play(speed) {
    if (!controller.active) return;
    if (typeof speed === 'number' && TIMELINE_SPEEDS.includes(speed)) controller.speed = speed;
    if (controller.playhead >= controller.t1) controller.playhead = controller.t0;
    controller.playing = true;
    controller.lastTickAt = 0;
    emitStatus();
    controller.rafId = requestAnimationFrame(playTick);
}

export function pause() {
    if (!controller.playing) return;
    controller.playing = false;
    cancelAnimationFrame(controller.rafId);
    controller.rafId = 0;
    emitStatus();
}

// setSpeed switches animation speed (keeps playing state).
export function setSpeed(speed) {
    if (TIMELINE_SPEEDS.includes(speed)) {
        controller.speed = speed;
        emitStatus();
    }
}

// setLoop toggles range looping.
export function setLoop(on) {
    controller.loop = !!on;
    emitStatus();
}

// setRange re-anchors the timeline to a new span ending at wall-clock now.
export async function setRange(rangeSeconds) {
    if (!controller.active) return;
    controller.t1 = Math.floor(Date.now() / 1000);
    controller.t0 = controller.t1 - rangeSeconds;
    controller.playhead = controller.t1;
    controller.bundle = null;
    controller.loading = true;
    emitStatus();
    try {
        await ensurePlayheadBundle();
    } finally {
        controller.loading = false;
        emitCurrentMoment();
    }
}

// --- Timeline bar UI -----------------------------------------------------

// renderBar paints the timeline bar from a status payload. The bar markup is
// created lazily once (single #timeline-bar element above the map footer).
let barEl = null;

function ensureBar() {
    if (barEl && document.body.contains(barEl)) return barEl;
    barEl = document.createElement('div');
    barEl.id = 'timeline-bar';
    barEl.className = 'timeline-bar';
    barEl.innerHTML = `
        <div class="timeline-row timeline-row-main">
            <button type="button" class="btn btn-outline-secondary btn-sm timeline-btn timeline-exit" title="Exit time travel (back to live)">Live</button>
            <button type="button" class="btn btn-outline-secondary btn-sm timeline-btn timeline-playpause" title="Play / pause">
                <svg class="icon timeline-icon-play" data-glyph="fa-play" viewBox="0 0 384 512" width="1em" height="1em" fill="currentColor" aria-hidden="true"><path d="M73 39Q49 25 25 38Q1 52 0 80V432Q1 460 25 474Q49 487 73 473L361 297Q383 283 384 256Q383 230 361 215L73 39Z"/></svg>
                <svg class="icon timeline-icon-pause" data-glyph="fa-pause" viewBox="0 0 320 512" width="1em" height="1em" fill="currentColor" aria-hidden="true" style="display:none"><path d="M48 32Q35 32 35 45V467Q35 480 48 480H112Q125 480 125 467V45Q125 32 112 32H48ZM208 32Q195 32 195 45V467Q195 480 208 480H272Q285 480 285 467V45Q285 32 272 32H208Z"/></svg>
            </button>
            <input type="range" class="form-range timeline-scrub" min="0" max="1000" value="1000" step="1" aria-label="Timeline position">
            <div class="timeline-clock" title="Playhead time (UTC / local)">--:--</div>
            <div class="timeline-speed btn-group btn-group-sm" role="group"></div>
            <button type="button" class="btn btn-outline-secondary btn-sm timeline-btn timeline-loop" title="Loop the range">Loop</button>
        </div>
        <div class="timeline-row timeline-row-presets">
            <div class="timeline-presets btn-group btn-group-sm" role="group"></div>
            <div class="timeline-hint">Spots shown = 15 min trailing window. Scrub snaps to 5 min.</div>
        </div>
    `;
    document.body.appendChild(barEl);

    barEl.querySelector('.timeline-exit').addEventListener('click', () => {
        exitTimeline();
        if (typeof controller.listeners.exit === 'function') {
            try { controller.listeners.exit(); } catch (_) { /* hook error */ }
        }
    });
    barEl.querySelector('.timeline-playpause').addEventListener('click', () => {
        if (controller.playing) pause(); else play();
    });
    barEl.querySelector('.timeline-loop').addEventListener('click', (e) => {
        setLoop(!controller.loop);
        e.currentTarget.classList.toggle('active', controller.loop);
    });

    const scrub = barEl.querySelector('.timeline-scrub');
    scrub.addEventListener('input', () => {
        const frac = scrub.value / 1000;
        const t = controller.t0 + frac * (controller.t1 - controller.t0);
        pause();
        seek(snapToScrub(t));
    });

    const speedGroup = barEl.querySelector('.timeline-speed');
    TIMELINE_SPEEDS.forEach((s) => {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'btn btn-outline-secondary btn-sm';
        b.dataset.speed = String(s);
        b.textContent = `${s}x`;
        b.addEventListener('click', () => { setSpeed(s); if (!controller.playing) play(s); });
        speedGroup.appendChild(b);
    });

    const presetGroup = barEl.querySelector('.timeline-presets');
    RANGE_PRESETS.forEach((p) => {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'btn btn-outline-secondary btn-sm';
        b.dataset.seconds = String(p.seconds);
        b.textContent = p.label;
        b.addEventListener('click', () => { void setRange(p.seconds); });
        presetGroup.appendChild(b);
    });

    return barEl;
}

// fmtClock renders the playhead as HH:MM UTC + local offset label.
function fmtClock(unixSec) {
    const d = new Date(unixSec * 1000);
    const hh = String(d.getUTCHours()).padStart(2, '0');
    const mm = String(d.getUTCMinutes()).padStart(2, '0');
    const loc = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return `${hh}:${mm} UTC (${loc})`;
}

function renderBar(s) {
    if (!controller.active) {
        if (barEl) barEl.remove();
        return;
    }
    const bar = ensureBar();
    const frac = s.t1 > s.t0 ? (s.playhead - s.t0) / (s.t1 - s.t0) : 1;
    const scrub = bar.querySelector('.timeline-scrub');
    if (document.activeElement !== scrub) scrub.value = String(Math.round(frac * 1000));
    bar.querySelector('.timeline-clock').textContent = s.loading ? 'loading…' : fmtClock(s.playhead);
    const pp = bar.querySelector('.timeline-playpause');
    bar.querySelector('.timeline-icon-play').style.display = s.playing ? 'none' : '';
    bar.querySelector('.timeline-icon-pause').style.display = s.playing ? '' : 'none';
    bar.querySelectorAll('.timeline-speed .btn').forEach((b) => {
        b.classList.toggle('active', Number(b.dataset.speed) === s.speed);
    });
    bar.querySelectorAll('.timeline-presets .btn').forEach((b) => {
        const secs = Number(b.dataset.seconds);
        b.classList.toggle('active', Math.abs((s.t1 - s.t0) - secs) < 2);
    });
    bar.querySelector('.timeline-loop').classList.toggle('active', s.loop);
}

// --- URL state (shareable past views) ------------------------------------

// encodeTimelineURL writes ?tl=1&t0=&t1=&spd= into the URL (replaceState, no
// history spam) while timeline mode is active.
export function syncTimelineURL() {
    const u = new URL(window.location.href);
    if (controller.active) {
        u.searchParams.set('tl', '1');
        u.searchParams.set('t0', String(controller.t0));
        u.searchParams.set('t1', String(controller.t1));
        u.searchParams.set('spd', String(controller.speed));
    } else {
        u.searchParams.delete('tl');
        u.searchParams.delete('t0');
        u.searchParams.delete('t1');
        u.searchParams.delete('spd');
    }
    window.history.replaceState(null, '', u.toString());
}

// readTimelineURL parses the timeline params (null when absent).
export function readTimelineURL() {
    const p = new URLSearchParams(window.location.search || '');
    if (p.get('tl') !== '1') return null;
    const t0 = parseInt(p.get('t0') || '0', 10);
    const t1 = parseInt(p.get('t1') || '0', 10);
    const spd = parseInt(p.get('spd') || String(DEFAULT_SPEED), 10);
    if (!t0 || !t1 || t1 <= t0) return null;
    return { t0, t1, speed: TIMELINE_SPEEDS.includes(spd) ? spd : DEFAULT_SPEED };
}

// Expose internals for vitest (importers in tests use these exports).
export const __internals = {
    controller,
    currentFilterState,
    bundleCacheKey,
    getBundle,
    putBundle,
    fetchBundle,
    ensurePlayheadBundle,
    maybePrefetch,
    renderBar,
    ensureBar,
    tryRingBundle,
    ringCohortSatisfies,
};