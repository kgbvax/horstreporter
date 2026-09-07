// videoexport.js — config panel + job client for clip export (horstvideo).
// The heavy lifting happens server-side: this panel only POSTs a config to
// /api/video/render (proxied to the horstvideo sidecar), polls job status and
// hands the user the finished MP4. The captured video is rendered from the
// dedicated /video-stage.html surface — map zoom/center, band filters and
// surroundings are snapshotted from the current UI at submit time, and the
// export never includes this panel.
import { state } from './state.js';
import { map } from './map.js';
import { getLoopRange, applyExternalRange } from './timetravel.js';

const pad = (n) => String(n).padStart(2, '0');

function toLocalInputValue(unix) {
    const d = new Date(unix * 1000);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInputValue(value) {
    const t = Math.floor(new Date(value).getTime() / 1000);
    return Number.isFinite(t) ? t : 0;
}

let pollTimer = null;
// One active job per client: prevents N rapid clicks enqueueing N renders.
// (The sidecar also serializes globally: single worker, 429 past the queue.)
let jobActive = false;
let pollFailures = 0;

export function initVideoExport() {
    const toggle = document.getElementById('videoexport-toggle');
    toggle?.addEventListener('click', () => {
        const panel = document.getElementById('videoexport-panel');
        if (!panel) return;
        if (panel.classList.contains('is-hidden')) {
            syncDefaults();
            panel.classList.remove('is-hidden');
            toggle.classList.add('is-active');
        } else {
            panel.classList.add('is-hidden');
            toggle.classList.remove('is-active');
        }
    });

    document.getElementById('videoexport-close')?.addEventListener('click', () => {
        document.getElementById('videoexport-panel')?.classList.add('is-hidden');
        toggle?.classList.remove('is-active');
    });

    // From/To here edit the SHARED loop range (timetravel.js) — the same
    // mechanism the time-travel From/To inputs and draggable markers use.
    const rangeChanged = () => {
        applyExternalRange(
            fromLocalInputValue(document.getElementById('videoexport-from')?.value || ''),
            fromLocalInputValue(document.getElementById('videoexport-to')?.value || ''),
        );
    };
    document.getElementById('videoexport-from')?.addEventListener('change', rangeChanged);
    document.getElementById('videoexport-to')?.addEventListener('change', rangeChanged);
    // Marker drags / time-travel edits re-sync our inputs; refresh the estimate.
    document.addEventListener('timetravel:range', updateEstimate);

    document.getElementById('videoexport-submit')?.addEventListener('click', submitJob);
}

// Defaults re-sync every time the panel opens: the shared loop range.
function syncDefaults() {
    const { start, end } = getLoopRange();
    const from = document.getElementById('videoexport-from');
    const to = document.getElementById('videoexport-to');
    if (to) to.value = toLocalInputValue(end);
    if (from) from.value = toLocalInputValue(start);
    updateEstimate();
}

function updateEstimate() {
    const start = fromLocalInputValue(document.getElementById('videoexport-from')?.value || '');
    const end = fromLocalInputValue(document.getElementById('videoexport-to')?.value || '');
    const step = parseInt(document.getElementById('videoexport-step')?.value || '120', 10);
    const fps = parseInt(document.getElementById('videoexport-fps')?.value || '10', 10);
    const el = document.getElementById('videoexport-estimate');
    if (!el || !start || !end || end <= start) {
        if (el) el.textContent = '';
        return;
    }
    const frames = Math.ceil((end - start) / step);
    const seconds = Math.round(frames / fps);
    el.textContent = `${frames} frames -> ${Math.floor(seconds / 60)}:${pad(seconds % 60)} clip at ${fps} fps (render ~${Math.ceil((frames * 1.2) / 60)} min)`;
}

document.addEventListener('change', (e) => {
    if (e.target.closest?.('#videoexport-panel')) updateEstimate();
});

function setJobActive(active) {
    jobActive = active;
    const btn = document.getElementById('videoexport-submit');
    if (btn) btn.disabled = active;
}

async function submitJob() {
    const statusEl = document.getElementById('videoexport-status');
    const downloadEl = document.getElementById('videoexport-download');
    const set = (text) => { if (statusEl) statusEl.textContent = text; };
    if (jobActive) {
        set('A render is already in progress — one export at a time.');
        return;
    }
    if (downloadEl) downloadEl.hidden = true;
    clearInterval(pollTimer);

    // Source of truth is the shared loop range (From/To inputs mirror it).
    const { start: from, end: to } = getLoopRange();
    if (!from || !to || to <= from) {
        set('Pick a start before the end time.');
        return;
    }

    // Snapshot the current UI state: camera, bands, surroundings. The stage
    // renders exactly what the operator sees, minus all the chrome.
    const cfg = {
        qth: state.qth || '',
        start: from,
        end: to,
        step_seconds: parseInt(document.getElementById('videoexport-step')?.value || '120', 10),
        fps: parseInt(document.getElementById('videoexport-fps')?.value || '10', 10),
        width: 1280,
        height: 720,
    };
    const size = (document.getElementById('videoexport-size')?.value || '1280x720').split('x');
    cfg.width = parseInt(size[0], 10);
    cfg.height = parseInt(size[1], 10);
    if (document.getElementById('videoexport-use-view')?.checked && map) {
        const c = map.getCenter();
        cfg.center_lat = c.lat;
        cfg.center_lng = c.lng;
        cfg.zoom = map.getZoom();
    }
    const bands = Array.from(document.querySelectorAll('.band-enable:checked')).map((cb) => cb.value);
    if (bands.length && bands.length < document.querySelectorAll('.band-enable').length) {
        cfg.bands = bands;
    }
    if (document.getElementById('surroundings')?.checked) cfg.surroundings = true;
    if (!cfg.qth) {
        set('Enter a qth first.');
        return;
    }

    set('Submitting render job…');
    let resp;
    try {
        resp = await fetch('/api/video/render', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(cfg),
        });
    } catch (err) {
        set('Render service unreachable (is horstvideo running?).');
        return;
    }
    if (!resp.ok) {
        set(`Job rejected: ${(await resp.text()).trim()}`);
        return;
    }
    const { id } = await resp.json();
    set('Queued…');
    setJobActive(true);
    pollFailures = 0;
    pollTimer = setInterval(() => pollJob(id), 2000);
}

async function pollJob(id) {
    const statusEl = document.getElementById('videoexport-status');
    const downloadEl = document.getElementById('videoexport-download');
    try {
        const resp = await fetch(`/api/video/job/${encodeURIComponent(id)}`);
        if (resp.status === 404) {
            // The sidecar's job state is in-memory: a restart drops it. Stop
            // polling instead of waiting forever on a job that will never end.
            clearInterval(pollTimer);
            setJobActive(false);
            if (statusEl) statusEl.textContent = 'Job lost (render service restarted) — resubmit.';
            return;
        }
        if (!resp.ok) throw new Error(`job ${resp.status}`);
        pollFailures = 0;
        const j = await resp.json();
        switch (j.status) {
            case 'queued':
                if (statusEl) statusEl.textContent = 'Queued…';
                break;
            case 'rendering':
                if (statusEl) statusEl.textContent = `Rendering frame ${j.frames_done}/${j.frames_total}…`;
                break;
            case 'stitching':
                if (statusEl) statusEl.textContent = 'Encoding MP4…';
                break;
            case 'done':
                clearInterval(pollTimer);
                setJobActive(false);
                if (statusEl) statusEl.textContent = 'Done.';
                if (downloadEl) {
                    downloadEl.href = `/api/video${j.video}`;
                    downloadEl.setAttribute('download', `horstreporter-${id}.mp4`);
                    downloadEl.hidden = false;
                }
                break;
            case 'error':
                clearInterval(pollTimer);
                setJobActive(false);
                if (statusEl) statusEl.textContent = `Failed: ${j.error || 'unknown error'}`;
                break;
        }
    } catch (err) {
        // Short transient failures (proxy blip) are tolerated, but don't poll
        // forever if the service is down.
        pollFailures++;
        if (pollFailures > 15) {
            clearInterval(pollTimer);
            setJobActive(false);
            if (statusEl) statusEl.textContent = 'Render service unreachable — gave up waiting.';
            return;
        }
        if (statusEl) statusEl.textContent = 'Waiting for render service…';
    }
}