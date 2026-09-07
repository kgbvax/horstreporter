// videoexport.js — export the time-travel content as a video clip.
// The controls live INSIDE the time-travel bar (#timetravel-bar): the clip's
// start/stop IS the shared loop range (From/To inputs + draggable markers in
// timetravel.js) — there are no separate time inputs here by design. This
// module only adds step/fps/size choices, POSTs /api/video/render (proxied
// to the horstvideo sidecar), polls job status and hands over the MP4.
// The video itself is rendered from the clean /video-stage.html surface.
import { state } from './state.js';
import { map } from './map.js';
import { getLoopRange } from './timetravel.js';

const pad = (n) => String(n).padStart(2, '0');

// Done/failed notification: browser Notification (permission is requested at
// submit time, inside the click gesture) + a document-title badge as the
// no-permission fallback. The title is restored when the window regains focus.
let baseTitle = null;
function flagResult(text, clickedUrl) {
    if (baseTitle === null) baseTitle = document.title;
    document.title = `${text} — ${baseTitle}`;
    if (window.Notification?.permission === 'granted') {
        const n = new Notification('horstreporter video export', {
            body: clickedUrl ? 'Render finished — click to open the download.' : text,
            tag: 'video-export', // re-tag replaces instead of stacking
        });
        n.onclick = () => { window.focus(); n.close(); };
    }
}
window.addEventListener('focus', () => {
    if (baseTitle !== null) { document.title = baseTitle; baseTitle = null; }
});

let pollTimer = null;
// One active job per client: prevents N rapid clicks enqueueing N renders.
// (The sidecar also serializes globally: single worker, 429 past the queue.)
let jobActive = false;
let pollFailures = 0;

export function initVideoExport() {
    document.getElementById('videoexport-submit')?.addEventListener('click', submitJob);
    // The loop range changes via the bar's From/To inputs or marker drags —
    // refresh the frame/clip estimate whenever that happens.
    document.addEventListener('timetravel:range', updateEstimate);
}

function updateEstimate() {
    const { start, end } = getLoopRange();
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
    if (e.target.closest?.('#videoexport-controls')) updateEstimate();
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
    // WYSIWYG: the clip always renders the operator's current map view —
    // camera (center + zoom) and the light/dark switch exactly as on screen.
    if (map) {
        const c = map.getCenter();
        cfg.center_lat = c.lat;
        cfg.center_lng = c.lng;
        cfg.zoom = map.getZoom();
    }
    cfg.theme = document.body.getAttribute('data-theme') || 'light';
    const bands = Array.from(document.querySelectorAll('.band-enable:checked')).map((cb) => cb.value);
    if (bands.length && bands.length < document.querySelectorAll('.band-enable').length) {
        cfg.bands = bands;
    }
    if (document.getElementById('surroundings')?.checked) cfg.surroundings = true;
    if (!cfg.qth) {
        set('Enter a qth first.');
        return;
    }

    // Ask for notification permission once, inside the click gesture
    // (browsers reject requests not tied to a user action).
    if (window.Notification?.permission === 'default') {
        window.Notification.requestPermission();
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
                flagResult('Export ready', `/api/video${j.video}`);
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
                flagResult('Export failed', null);
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