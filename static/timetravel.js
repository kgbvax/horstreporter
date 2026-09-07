// Time-travel replay: step through the last 24-48h of spots in 30-min
// buckets. While active, the replay bucket array REPLACES state.liveSpots
// (the real live list is parked in liveSpotsBackup) so scheduleRender,
// band-lab and both projections follow with zero changes — see state.js.
// SSE arrivals and the 5s prune are suspended in app.js until exit.
// Data comes from /api/replay/histogram (timeline bars) and
// /api/replay/spots (one bucket per fetch); both read Postgres read-only.
import { state } from './state.js';
import { getMinSnrMode, getEnabledBands, getSelectedBand, bandColors, pillTextColor } from './utils.js';

const BUCKET_SECONDS = 1800;
const DAY_SECONDS = 86400;

// scheduleRender lives in app.js (which imports this module), so it is
// injected at init time instead of imported — keeps the graph one-way.
let render = () => {};
// Restores the top-right band display (app.js owns it); injected at init.
let restoreBandDisplay = () => {};

// Module-level handle. Defaults in state.js may be absent when state.js is
// mock-partial in tests, so self-initialize on first import.
const rt = (state.timeTravel = state.timeTravel || {
    active: false,
    liveSpotsBackup: null,
    start: 0,
    end: 0,
    rangeStart: 0,
    rangeEnd: 0,
    bucketSeconds: 1800,
    currentBucketEnd: 0,
    playing: false,
    speedMs: 3000,
    histogram: null,
    bucketCache: new Map(),
    bucketCacheLimit: 24,
    timer: null,
    abort: null
});

export function isReplayActive() {
    return rt.active;
}

// Simulated clock for time-aware overlays: during replay the grayline
// terminator tracks the current bucket (on the same 5-min grid both
// projections use for their cache keys); otherwise the real clock.
const GRAYLINE_BUCKET_MS = 5 * 60 * 1000;

export function overlayNowMs() {
    if (rt.active && rt.currentBucketEnd) {
        return Math.floor((rt.currentBucketEnd * 1000) / GRAYLINE_BUCKET_MS) * GRAYLINE_BUCKET_MS;
    }
    return Date.now();
}

// Snap a draggable loop-range marker to the bucket grid and clamp it so the
// two markers keep a minimum separation and stay inside the data extent.
// Pure, for tests.
export function snapRangeMarker(which, t, rangeStart, rangeEnd, extentStart, extentEnd, bucketSeconds) {
    const minSpan = 2 * bucketSeconds;
    let snapped = extentStart + Math.round((t - extentStart) / bucketSeconds) * bucketSeconds;
    if (which === 'start') {
        snapped = Math.min(Math.max(snapped, extentStart), rangeEnd - minSpan);
    } else {
        snapped = Math.min(Math.max(snapped, rangeStart + minSpan), extentEnd);
    }
    return snapped;
}

export function initTimeTravel({ scheduleRender, updateBandDisplay }) {
    render = scheduleRender || (() => {});
    restoreBandDisplay = updateBandDisplay || (() => {});

    document.getElementById('timetravel-toggle')?.addEventListener('click', () => {
        if (rt.active) {
            exitTimeTravel();
        } else {
            enterTimeTravel();
        }
    });

    initRangeMarkers();

    document.getElementById('timetravel-close')?.addEventListener('click', () => exitTimeTravel());
    document.getElementById('timetravel-prev')?.addEventListener('click', () => { pause(); stepBucket(-1); });
    document.getElementById('timetravel-next')?.addEventListener('click', () => { pause(); stepBucket(1); });
    document.getElementById('timetravel-play')?.addEventListener('click', () => { rt.playing ? pause() : play(); });
    document.getElementById('timetravel-speed')?.addEventListener('change', (e) => {
        rt.speedMs = parseInt(e.target.value, 10) || 3000;
        if (rt.playing) restartTimer();
    });
    document.getElementById('timetravel-scrubber')?.addEventListener('input', (e) => {
        pause();
        const v = parseInt(e.target.value, 10);
        if (Number.isFinite(v)) loadBucket(v);
    });
    document.getElementById('timetravel-apply')?.addEventListener('click', () => applyRange());

    // Filter changes (band pills, source checkboxes, SNR mode, surroundings)
    // must refetch from the server — the bucket cache is filtered at fetch time
    // and cannot be re-filtered client-side.
    document.addEventListener('change', (e) => {
        if (!rt.active) return;
        if (e.target.closest?.('#band-container, #min-snr-group, #show-dxcluster-spots, #show-rbn-spots, #show-wspr-spots, #surroundings')) {
            notifyFiltersChanged();
        }
    });
}

function floorToBucketEnd(t, secs = BUCKET_SECONDS) {
    return Math.floor(t / secs) * secs + secs;
}

function clampToSpan(start, end) {
    const maxSpan = 2 * DAY_SECONDS;
    if (end - start > maxSpan) start = end - maxSpan;
    return [start, end];
}

// --- enter / exit ------------------------------------------------------------

export async function enterTimeTravel() {
    if (rt.active) return;
    const now = Math.floor(Date.now() / 1000);
    const end = floorToBucketEnd(now);
    // Full 48h timeline; the loop range defaults to the most recent 12h.
    const [start] = clampToSpan(end - 2 * DAY_SECONDS, end);
    const rangeStart = Math.max(start, end - 12 * 3600);

    rt.active = true;
    rt.liveSpotsBackup = state.liveSpots;
    state.liveSpots = []; // replay array: swapped in, mutated in place per bucket
    rt.start = start;
    rt.end = end;
    rt.rangeStart = rangeStart;
    rt.rangeEnd = end;
    rt.bucketSeconds = BUCKET_SECONDS;
    rt.currentBucketEnd = end;
    rt.playing = false;
    rt.histogram = null;
    rt.bucketCache.clear();

    document.getElementById('timetravel-bar')?.classList.remove('is-hidden');
    document.getElementById('timetravel-toggle')?.classList.add('is-active');
    document.getElementById('wspr-matrix-window')?.classList.add('replay-live');
    syncRangeInputs();
    positionMarkers();

    loadHistogram().then(() => render());
    await loadBucket(end);
}

export function exitTimeTravel() {
    if (!rt.active) return;
    stopTimer();
    rt.abort?.abort();
    rt.abort = null;
    rt.playing = false;

    // The live list never aged (prune suspended) and SSE arrivals were dropped,
    // so the backup is still exactly what the stream had. The __recvMs-based
    // trueAge gate in getRenderableMapSpots culls anything over the #minutes
    // slider on the next render. A null backup (replay started before any
    // stream) restores an empty live array for SSE to fill.
    state.liveSpots = rt.liveSpotsBackup || [];
    rt.liveSpotsBackup = null;
    rt.active = false;
    rt.bucketCache.clear();

    document.getElementById('timetravel-bar')?.classList.add('is-hidden');
    document.getElementById('timetravel-toggle')?.classList.remove('is-active');
    document.getElementById('wspr-matrix-window')?.classList.remove('replay-live');
    restoreBandDisplay();
    render();
}

// --- filters -----------------------------------------------------------------

export function notifyFiltersChanged() {
    if (!rt.active) return;
    rt.bucketCache.clear();
    loadHistogram();
    loadBucket(rt.currentBucketEnd, { skipCache: true });
}

function sourceToggles() {
    return {
        dxcluster: document.getElementById('show-dxcluster-spots')?.checked !== false,
        rbn: document.getElementById('show-rbn-spots')?.checked !== false,
        wspr: document.getElementById('show-wspr-spots')?.checked !== false,
    };
}

function collectParams(bucketEnd) {
    const params = new URLSearchParams();
    params.set('qth', state.qth || '');
    if (document.getElementById('surroundings')?.checked) params.set('surroundings', 'true');
    const enabledBands = Array.from(getEnabledBands()).sort();
    if (enabledBands.length) params.set('enabled_bands', enabledBands.join(','));
    const selectedBand = getSelectedBand();
    if (selectedBand && selectedBand !== 'all') params.set('selected_band', selectedBand);
    const minSnrMode = getMinSnrMode();
    if (minSnrMode && minSnrMode !== 'none') {
        params.set('min_snr_mode', minSnrMode);
        if (minSnrMode === 'ssb') params.set('ssb_min_db', document.getElementById('ssb-min-db')?.value || '0');
        if (minSnrMode === 'cw') params.set('cw_min_db', document.getElementById('cw-min-db')?.value || '-15');
    }
    const toggles = sourceToggles();
    if (!toggles.dxcluster) params.set('include_dxcluster', 'false');
    if (!toggles.rbn) params.set('include_rbn', 'false');
    if (!toggles.wspr) params.set('include_wspr', 'false');
    params.set('bucket_seconds', String(rt.bucketSeconds));
    if (bucketEnd) params.set('bucket_end', String(bucketEnd));
    return params;
}

// --- fetching ----------------------------------------------------------------

async function loadHistogram() {
    const params = collectParams(0);
    params.set('start', String(rt.start));
    params.set('end', String(rt.end));
    try {
        const res = await fetch(`/api/replay/histogram?${params.toString()}`);
        if (!res.ok) throw new Error(`histogram ${res.status}`);
        rt.histogram = await res.json();
        renderHistogram();
        syncScrubber();
    } catch (err) {
        console.warn('time travel: histogram fetch failed', err);
    }
}

function cacheGet(bucketEnd) {
    if (!rt.bucketCache.has(bucketEnd)) return null;
    const data = rt.bucketCache.get(bucketEnd);
    rt.bucketCache.delete(bucketEnd); // refresh LRU order
    rt.bucketCache.set(bucketEnd, data);
    return data;
}

function cachePut(bucketEnd, spots) {
    rt.bucketCache.set(bucketEnd, spots);
    while (rt.bucketCache.size > rt.bucketCacheLimit) {
        rt.bucketCache.delete(rt.bucketCache.keys().next().value);
    }
}

export async function loadBucket(bucketEnd, { skipCache = false } = {}) {
    if (!rt.active) return;
    bucketEnd = Math.max(Math.min(bucketEnd, rt.end), rt.start);
    rt.currentBucketEnd = bucketEnd;

    const cached = skipCache ? null : cacheGet(bucketEnd);
    if (cached) {
        installBucket(bucketEnd, cached);
        return;
    }

    const controller = new AbortController();
    rt.abort?.abort();
    rt.abort = controller;
    try {
        const res = await fetch(`/api/replay/spots?${collectParams(bucketEnd).toString()}`, { signal: controller.signal });
        if (!res.ok) throw new Error(`replay ${res.status}`);
        const json = await res.json();
        if (!rt.active || controller.signal.aborted) return;
        cachePut(bucketEnd, json);
        installBucket(bucketEnd, json);
    } catch (err) {
        if (err?.name !== 'AbortError') console.warn('time travel: bucket fetch failed', err);
    }
}

function installBucket(bucketEnd, json) {
    if (!rt.active) return;
    const spots = json.spots || [];
    state.liveSpots.length = 0;
    for (const spot of spots) {
        spot.__replay = true;
        spot.__bucketEnd = bucketEnd;
        // __recvMs/__recvAge stay unset: they belong to the live trueAge gate,
        // which is bypassed in replay mode. ageSeconds is bucket-relative.
        state.liveSpots.push(spot);
    }
    rt.currentBucketEnd = bucketEnd;
    updateBucketLabel(json);
    setReplayBandDisplay();
    syncScrubber();
    render();
    markHistogramPosition();
    if (json.truncated) showTruncationNote();
}

// --- playback ----------------------------------------------------------------

function play() {
    if (!rt.active || rt.playing) return;
    rt.playing = true;
    document.getElementById('timetravel-play')?.classList.add('is-active');
    restartTimer();
}

function pause() {
    if (!rt.playing) return;
    rt.playing = false;
    document.getElementById('timetravel-play')?.classList.remove('is-active');
    stopTimer();
}

// One playback tick: advance one bucket, wrapping to the loop-range start
// when the range end is reached — playback loops until paused or stopped.
export function advancePlayback() {
    if (rt.currentBucketEnd >= rt.rangeEnd) {
        loadBucket(rt.rangeStart);
        return;
    }
    stepBucket(1);
}

function restartTimer() {
    stopTimer();
    rt.timer = setInterval(advancePlayback, rt.speedMs);
}

function stopTimer() {
    if (rt.timer) clearInterval(rt.timer);
    rt.timer = null;
}

function stepBucket(delta) {
    loadBucket(rt.currentBucketEnd + delta * rt.bucketSeconds);
    // Prefetch the next bucket one step ahead so playback rarely waits on I/O.
    const prefetchEnd = rt.currentBucketEnd + 2 * delta * rt.bucketSeconds;
    if (prefetchEnd <= rt.rangeEnd && !cacheGet(prefetchEnd)) {
        const params = collectParams(prefetchEnd);
        fetch(`/api/replay/spots?${params.toString()}`)
            .then((res) => (res.ok ? res.json() : null))
            .then((json) => { if (json) cachePut(prefetchEnd, json); })
            .catch(() => { /* prefetch is best-effort */ });
    }
}

// --- range pickers -----------------------------------------------------------

function syncRangeInputs() {
    const from = document.getElementById('timetravel-from');
    const to = document.getElementById('timetravel-to');
    if (from) from.value = toLocalInputValue(rt.start - rt.bucketSeconds);
    if (to) to.value = toLocalInputValue(rt.end);
}

function toLocalInputValue(unix) {
    const d = new Date(unix * 1000);
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function applyRange() {
    const fromVal = document.getElementById('timetravel-from')?.value;
    const toVal = document.getElementById('timetravel-to')?.value;
    let start = fromVal ? Math.floor(new Date(fromVal).getTime() / 1000) : rt.start - rt.bucketSeconds;
    let end = toVal ? Math.floor(new Date(toVal).getTime() / 1000) : rt.end;
    if (!Number.isFinite(start) || !Number.isFinite(end)) return;
    const now = floorToBucketEnd(Math.floor(Date.now() / 1000));
    if (end > now) end = now;
    if (end <= start) return;
    [start, end] = clampToSpan(start, end);
    rt.start = start;
    rt.end = end;
    // Keep the loop range inside the new data extent.
    rt.rangeStart = Math.min(Math.max(rt.rangeStart, rt.start), rt.end - 2 * rt.bucketSeconds);
    rt.rangeEnd = Math.min(Math.max(rt.rangeEnd, rt.rangeStart + 2 * rt.bucketSeconds), rt.end);
    rt.bucketCache.clear();
    pause();
    syncRangeInputs();
    positionMarkers();
    loadHistogram();
    loadBucket(Math.min(rt.currentBucketEnd, end));
}

// --- overlay rendering -------------------------------------------------------

const dateTimeFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' });
const replayClockFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });

function fmtBucketEnd(unix) {
    return dateTimeFmt.format(new Date(unix * 1000));
}

// During replay the top-right display (normally the band pill) shows the
// simulated time — one timestamp with the date. exitTimeTravel hands the
// element back via the injected restoreBandDisplay.
function setReplayBandDisplay() {
    const display = document.getElementById('current-band-display');
    if (!display) return;
    display.textContent = `Replay ${replayClockFmt.format(new Date(rt.currentBucketEnd * 1000))}`;
    // pillTextColor needs a concrete hex; resolve the CSS var first.
    const accent = (getComputedStyle(document.documentElement).getPropertyValue('--accent-color') || '').trim();
    const bg = /^#[0-9a-fA-F]{6}$/.test(accent) ? accent : '#5b9bd5';
    display.style.backgroundColor = bg;
    display.style.color = pillTextColor(bg);
}

function updateBucketLabel(json) {
    const label = document.getElementById('timetravel-label');
    if (!label) return;
    const count = json?.count ?? state.liveSpots.length;
    label.textContent = `${fmtBucketEnd(rt.currentBucketEnd)} · ${count} spots`;
}

function showTruncationNote() {
    const label = document.getElementById('timetravel-label');
    if (label) label.textContent += ' (capped at 20000)';
}

function syncScrubber() {
    const scrub = document.getElementById('timetravel-scrubber');
    if (!scrub) return;
    scrub.min = String(rt.start);
    scrub.max = String(rt.end);
    scrub.step = String(rt.bucketSeconds);
    scrub.value = String(rt.currentBucketEnd);
}

function renderHistogram() {
    const container = document.getElementById('timetravel-histogram');
    if (!container || !rt.histogram) return;
    container.textContent = '';
    const buckets = rt.histogram.buckets || [];
    const max = Math.max(1, ...buckets.map((b) => b.count));
    const selectedBand = getSelectedBand();
    const barColor = selectedBand && selectedBand !== 'all'
        ? (bandColors[selectedBand] || 'var(--control-border)')
        : 'var(--accent-color, #5b9bd5)';
    for (const b of buckets) {
        const bar = document.createElement('div');
        bar.className = 'timetravel-bar-seg';
        bar.style.height = `${Math.max(4, Math.round((b.count / max) * 100))}%`;
        bar.style.setProperty('--bar-color', barColor);
        bar.title = `${fmtBucketEnd(b.t)} · ${b.count}`;
        bar.dataset.bucketEnd = String(b.t + rt.bucketSeconds);
        bar.addEventListener('click', () => {
            pause();
            loadBucket(parseInt(bar.dataset.bucketEnd, 10));
        });
        container.appendChild(bar);
    }
    markHistogramPosition();
}

function markHistogramPosition() {
    const container = document.getElementById('timetravel-histogram');
    if (!container) return;
    for (const seg of container.children) {
        seg.classList.toggle('is-current', parseInt(seg.dataset.bucketEnd, 10) === rt.currentBucketEnd);
    }
}

// --- draggable loop-range markers ---------------------------------------------
// Two markers on the timeline track set the loop range (rt.rangeStart /
// rt.rangeEnd): playback wraps from rangeEnd back to rangeStart. They are
// distinct from rt.start/rt.end, which stay the histogram's data extent.

const pct = (t, span) => `${((t - span[0]) / Math.max(1, span[1] - span[0])) * 100}%`;

function markerExtent() {
    return [rt.start, rt.end];
}

function positionMarkers() {
    const track = document.getElementById('timetravel-track');
    if (!track || !rt.end || !rt.start) return;
    const span = markerExtent();
    const startEl = document.getElementById('timetravel-marker-start');
    const endEl = document.getElementById('timetravel-marker-end');
    const startLbl = document.getElementById('timetravel-marker-start-time');
    const endLbl = document.getElementById('timetravel-marker-end-time');
    const highlight = document.getElementById('timetravel-range-highlight');
    const rs = rt.rangeStart || rt.start;
    const re = rt.rangeEnd || rt.end;
    if (startEl) startEl.style.left = pct(rs, span);
    if (endEl) endEl.style.left = pct(re, span);
    if (startLbl) startLbl.textContent = timeFmt.format(new Date(rs * 1000));
    if (endLbl) {
        endLbl.textContent = timeFmt.format(new Date(re * 1000));
        // Flip the label left of the marker when it would overflow the track.
        const flip = ((re - span[0]) / Math.max(1, span[1] - span[0])) > 0.92;
        endLbl.style.left = flip ? 'auto' : 'calc(100% + 5px)';
        endLbl.style.right = flip ? 'calc(100% + 5px)' : 'auto';
    }
    if (highlight) {
        highlight.style.left = pct(rs, span);
        highlight.style.width = `${((re - rs) / Math.max(1, span[1] - span[0])) * 100}%`;
    }
}

function initRangeMarkers() {
    const track = document.getElementById('timetravel-track');
    if (!track) return;

    const drag = (which, markerId) => {
        const marker = document.getElementById(markerId);
        if (!marker) return;
        let dragging = false;
        marker.addEventListener('pointerdown', (e) => {
            if (!rt.active) return;
            dragging = true;
            pause();
            marker.setPointerCapture(e.pointerId);
            e.preventDefault();
        });
        marker.addEventListener('pointermove', (e) => {
            if (!dragging || !rt.active) return;
            const rect = track.getBoundingClientRect();
            if (!rect.width) return;
            const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
            const t = rt.start + frac * (rt.end - rt.start);
            const snapped = snapRangeMarker(which, t, rt.rangeStart, rt.rangeEnd, rt.start, rt.end, rt.bucketSeconds);
            if (which === 'start') rt.rangeStart = snapped; else rt.rangeEnd = snapped;
            positionMarkers();
        });
        const drop = () => {
            if (!dragging) return;
            dragging = false;
            if (rt.active) {
                rt.bucketCache.clear();
                syncRangeInputs();
                // Keep the playhead inside the reshaped loop range.
                if (rt.currentBucketEnd < rt.rangeStart || rt.currentBucketEnd > rt.rangeEnd) {
                    loadBucket(Math.min(Math.max(rt.currentBucketEnd, rt.rangeStart), rt.rangeEnd));
                }
            }
        };
        marker.addEventListener('pointerup', drop);
        marker.addEventListener('pointercancel', drop);
    };
    drag('start', 'timetravel-marker-start');
    drag('end', 'timetravel-marker-end');
}