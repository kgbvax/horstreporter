// Time-travel replay: step through the last 24-48h of spots in 30-min
// buckets. While active, the replay bucket array REPLACES state.liveSpots
// (the real live list is parked in liveSpotsBackup) so scheduleRender,
// band-lab and both projections follow with zero changes — see state.js.
// SSE arrivals and the 5s prune are suspended in app.js until exit.
// Data comes from /api/replay/histogram (timeline bars) and
// /api/replay/spots (one bucket per fetch); both read Postgres read-only.
import { state } from './state.js';
import { getMinSnrMode, getEnabledBands, getSelectedBand, bandColors } from './utils.js';

const BUCKET_SECONDS = 1800;
const DAY_SECONDS = 86400;

// scheduleRender lives in app.js (which imports this module), so it is
// injected at init time instead of imported — keeps the graph one-way.
let render = () => {};

// Module-level handle. Defaults in state.js may be absent when state.js is
// mock-partial in tests, so self-initialize on first import.
const rt = (state.timeTravel = state.timeTravel || {
    active: false,
    liveSpotsBackup: null,
    start: 0,
    end: 0,
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

export function initTimeTravel({ scheduleRender }) {
    render = scheduleRender || (() => {});

    document.getElementById('timetravel-toggle')?.addEventListener('click', () => {
        if (rt.active) {
            exitTimeTravel();
        } else {
            enterTimeTravel();
        }
    });

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
    const [start] = clampToSpan(end - DAY_SECONDS, end);

    rt.active = true;
    rt.liveSpotsBackup = state.liveSpots;
    state.liveSpots = []; // replay array: swapped in, mutated in place per bucket
    rt.start = start;
    rt.end = end;
    rt.bucketSeconds = BUCKET_SECONDS;
    rt.currentBucketEnd = end;
    rt.playing = false;
    rt.histogram = null;
    rt.bucketCache.clear();

    document.getElementById('timetravel-bar')?.classList.remove('is-hidden');
    document.getElementById('timetravel-toggle')?.classList.add('is-active');
    document.getElementById('wspr-matrix-window')?.classList.add('replay-live');
    syncRangeInputs();

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

function restartTimer() {
    stopTimer();
    rt.timer = setInterval(() => {
        if (rt.currentBucketEnd >= rt.end) {
            pause();
            return;
        }
        stepBucket(1);
    }, rt.speedMs);
}

function stopTimer() {
    if (rt.timer) clearInterval(rt.timer);
    rt.timer = null;
}

function stepBucket(delta) {
    loadBucket(rt.currentBucketEnd + delta * rt.bucketSeconds);
    // Prefetch the next bucket one step ahead so playback rarely waits on I/O.
    const prefetchEnd = rt.currentBucketEnd + 2 * delta * rt.bucketSeconds;
    if (prefetchEnd <= rt.end && !cacheGet(prefetchEnd)) {
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
    rt.bucketCache.clear();
    pause();
    syncRangeInputs();
    loadHistogram();
    loadBucket(Math.min(rt.currentBucketEnd, end));
}

// --- overlay rendering -------------------------------------------------------

const dateTimeFmt = new Intl.DateTimeFormat(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });

function fmtBucketEnd(unix) {
    return dateTimeFmt.format(new Date(unix * 1000));
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