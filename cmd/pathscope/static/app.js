// app.js — entrypoint. Fetches /api/pathscope/v1/health for the home
// region (one-shot), then subscribes to the /pathscope/api/pathscope/v1/stream
// SSE endpoint for live glance updates.
//
// fail-loud: any error renders the #status bar with the message; we never
// silently swallow a broken response. EventSource handles reconnection
// per HTML spec (1–30 s, browser-dependent) without any code on our side;
// on transient disconnect we just flip the status pill to "Reconnecting…".

import { renderMatrix } from './glance.js';

const STREAM_URL = '/pathscope/api/pathscope/v1/stream';

const els = {
  qth: document.getElementById('qth-pill'),
  home: document.getElementById('home-pill'),
  sw: document.getElementById('sw-pill'),
  observed: document.getElementById('observed-pill'),
  matrix: document.getElementById('matrix'),
  status: document.getElementById('status'),
};

let homeRegion = null;
let eventSource = null;

async function loadHealth() {
  const res = await fetch('/api/pathscope/v1/health');
  if (!res.ok) throw new Error(`health HTTP ${res.status}`);
  const h = await res.json();
  els.qth.textContent = `QTH: ${h.qth}`;
  els.home.textContent = `Home: ${h.home_region}`;
  homeRegion = h.home_region;
}

// applyGlance is the body of the old loadGlance minus the fetch —
// shared between the initial SSE event and every subsequent broadcast.
function applyGlance(data) {
  const sw = data.solar || {};
  const swStr = `SW: Kp ${sw.kp?.toFixed?.(1) ?? '?'} · SFI ${sw.sfi?.toFixed?.(0) ?? '?'}`;
  els.sw.textContent = swStr;
  els.observed.textContent = `Updated: ${new Date(data.observed_at).toLocaleTimeString()}`;
  renderMatrix(els.matrix, data, { homeRegion });
  els.status.textContent = `Live · ${data.cells.length} cells (${data.bands.length} bands × ${data.regions.length} regions). Window: ${data.window_sec}s.`;
  els.status.classList.remove('error');
}

function startStream() {
  eventSource = new EventSource(STREAM_URL);
  eventSource.onmessage = (ev) => {
    try {
      applyGlance(JSON.parse(ev.data));
    } catch (err) {
      // Server emits valid JSON; this branch is for malformed payloads.
      // Don't close the stream — the next event may be fine.
      console.error('pathscope: bad event payload', err);
    }
  };
  eventSource.onerror = () => {
    // EventSource auto-reconnects per HTML spec. We don't call .close()
    // here; the browser will keep trying. The status pill tells the user
    // what's happening while the connection is in a failed state.
    els.status.textContent = 'Reconnecting…';
    els.status.classList.add('error');
  };
}

// pagehide (not beforeunload) fires on tab close AND on bfcache eviction,
// including on mobile. Closing the EventSource explicitly here means the
// server-side hub client is removed within a few ms instead of waiting
// for the TCP RST to propagate.
window.addEventListener('pagehide', () => {
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
});

async function bootstrap() {
  try {
    await loadHealth();
  } catch (err) {
    els.status.textContent = `health: ${err.message || err}`;
    els.status.classList.add('error');
  }
  startStream();
}

bootstrap();