// dxcluster.js — HorstOperator "Chase Queue" module.
//
// A right-docked panel of live DX-cluster spots (from /api/dxspots), each scored
// by the local horstprop service (GET /v1/score). Auto-mode: cards are sorted by
// live score. Self-contained — injects its own styles + toggle button; no edits
// to app.js required. See docs/dxcluster-*.md and docs/horstprop.md.

import { canControlRig, rigTune, operate, canLookup, enrichSpots } from './opmode.js';
import { setChaseQueueHighlight, clearChaseQueueHighlight } from './app.js';
import { getEnabledBands } from './utils.js';

const DXSPOTS_URL = '/api/dxspots?minutes=30';
// Same-origin by default: HorstReporter reverse-proxies /horstprop/* to the
// local horstprop service (see horstprop_mount.go), so this works over the app's
// TLS with no CORS/mixed-content. Override with ?hp=http://127.0.0.1:9970 to hit
// a horstprop instance directly.
const HORSTPROP_URL = new URLSearchParams(location.search).get('hp') || '/horstprop';
const REFRESH_MS = 30000;
const TOP_N = 40; // cap how many spots we score per cycle

// Canonical band palette (mirrors static/utils.js bandColors).
const BAND_COLORS = {
  '160m': '#8B0000', '80m': '#800080', '60m': '#4B0082', '40m': '#0000FF',
  '30m': '#03b1b1', '20m': '#008000', '17m': '#808000', '15m': '#FFA500',
  '12m': '#00FFFF', '10m': '#FF0000', '6m': '#FF00FF', '4m': '#FF1493',
  '2m': '#008080', 'all': '#555555',
};
const bandColor = (b) => BAND_COLORS[b] || BAND_COLORS.all;
const gradeToDecision = (g) => (g === 'A' || g === 'B') ? 'go' : g === 'C' ? 'watch' : g === 'D' ? 'wait' : 'unknown';

// Opt-in demo data (?cqdemo=1) so the panel renders without live cluster creds.
const DEMO_SPOTS = [
  { dx_call: '3Y0J', spotter: 'LA7GIA', freq_khz: 18145, band: '17m', dx_locator: 'IB59', age_seconds: 21, comment: 'up 5-10 listening EU', op_name: 'Ken', country: 'Bouvet', country_iso: '' },
  { dx_call: 'VK6LC', spotter: 'G3TXF', freq_khz: 14018, band: '20m', dx_locator: 'OF87', age_seconds: 48, comment: '599 long path', op_name: 'Wayne', country: 'Australia', country_iso: 'AU' },
  { dx_call: 'JA3XYZ', spotter: 'DL1ABC', freq_khz: 21091, band: '15m', dx_locator: 'PM74', age_seconds: 12, comment: 'calling EU', op_name: 'Kenji', country: 'Japan', country_iso: 'JP' },
  { dx_call: '5R8AL', spotter: 'EA4XYZ', freq_khz: 14205, band: '20m', dx_locator: 'LH31', age_seconds: 55, comment: 'booming in', op_name: 'Eric', country: 'Madagascar', country_iso: 'MG' },
  { dx_call: 'PY2NY', spotter: 'F5ABC', freq_khz: 21094, band: '15m', dx_locator: 'GG66', age_seconds: 64, comment: '-14 dB', op_name: 'Vitor', country: 'Brazil', country_iso: 'BR' },
];

const isDemo = () => location.search.includes('cqdemo');
// DXSpider comments arrive padded and end with the spot time + BEL control chars
// (e.g. "...  1015Z\x07\x07"). Strip control chars, the trailing time, and collapse padding.
const trimComment = (c) => (c || '')
  .replace(/[\u0000-\u001f]+/g, ' ')
  .replace(/\s*\d{3,4}Z\s*$/i, '')
  .replace(/\s{2,}/g, ' ')
  .trim();

// Flag emoji from an ISO-3166 alpha-2 code (supplied by the backend, resolved
// via cty.dat). Empty/unknown -> no flag.
const flagFromISO = (iso) => {
  iso = (iso || '').trim().toUpperCase();
  if (iso.length !== 2) return '';
  return String.fromCodePoint(...[...iso].map((ch) => 0x1f1e6 + ch.charCodeAt(0) - 65));
};

// Well-known countries: the flag alone is enough, so we suppress the country
// NAME text on the card (less clutter). Lesser-known entities keep the name.
// Extend freely (e.g. later from log analysis of who you actually work).
const WELL_KNOWN_ISO = new Set([
  // all of Europe
  'AD', 'AL', 'AT', 'AX', 'BA', 'BE', 'BG', 'BY', 'CH', 'CY', 'CZ', 'DE', 'DK',
  'EE', 'ES', 'FI', 'FO', 'FR', 'GB', 'GG', 'GI', 'GR', 'HR', 'HU', 'IE', 'IM',
  'IS', 'IT', 'JE', 'LI', 'LT', 'LU', 'LV', 'MC', 'MD', 'ME', 'MK', 'MT', 'NL',
  'NO', 'PL', 'PT', 'RO', 'RS', 'SE', 'SI', 'SJ', 'SK', 'SM', 'UA', 'VA',
  // + commonly-worked majors
  'AU', 'IN', 'JP', 'CN', 'BR', 'AR', 'US', 'CA', 'RU', 'TR', 'ID',
]);
const isWellKnown = (iso) => WELL_KNOWN_ISO.has((iso || '').trim().toUpperCase());
// guessMode picks a sensible rig mode from the spot frequency. Cluster spots
// carry no mode field, so we infer from the band plan: FT8 watering holes →
// data, the CW portion at the bottom of each band → CW, otherwise '' (the agent
// then defaults to LSB/USB by frequency, and WaveLogGate refines via mode-on-QSY).
const FT8_DIALS_KHZ = [1840, 3573, 5357, 7074, 10136, 14074, 18100, 21074, 24915, 28074, 50313];
// CW segment upper edges (kHz) — at or below these (and within the band) → CW.
const CW_EDGE_KHZ = [1838, 3580, 7040, 10150, 14070, 18095, 21070, 24920, 28070];
function guessMode(freqKhz) {
  const f = Number(freqKhz) || 0;
  if (f <= 0) return '';
  if (FT8_DIALS_KHZ.some((d) => Math.abs(f - d) <= 1.5)) return 'FT8';
  if (CW_EDGE_KHZ.some((edge) => f <= edge && f >= edge - 100)) return 'CW';
  return '';
}

// enrichSpot maps a spot to the {id, call, band, mode} the agent's enrich
// endpoint expects. The id is echoed back so results merge by identity.
const enrichSpot = (s) => {
  const mode = guessMode(s.freq_khz);
  return { id: `${s.dx_call}|${s.band}|${mode}`, call: s.dx_call, band: s.band, mode };
};

const fmtFreq = (khz) => (khz >= 1000 ? (khz / 1000).toFixed(3) : String(khz));
const fmtAge = (s) => s < 60 ? `${s}s` : s < 3600 ? `${Math.round(s / 60)}m` : `${Math.round(s / 3600)}h`;
// degToCardinal maps a beam bearing to a 16-point compass abbreviation (N, NNE,
// NE, …) — operators think in compass headings, not raw degrees. The exact
// degrees stay available in the element title for anyone who needs them.
const COMPASS16 = ['N', 'NNE', 'NE', 'ENE', 'E', 'ESE', 'SE', 'SSE', 'S', 'SSW', 'SW', 'WSW', 'W', 'WNW', 'NW', 'NNW'];
const degToCardinal = (deg) => COMPASS16[Math.round((((deg % 360) + 360) % 360) / 22.5) % 16];
const meterPct = (v) => Math.max(6, Math.min(100, v || 0));

function injectStyles() {
  if (document.getElementById('cq-styles')) return;
  const css = `
  #chase-queue { flex: 0 0 380px; width: 380px; height: 100%; display: flex; flex-direction: column;
    position: relative; z-index: 1001;
    border-left: 1px solid var(--border-color); background: var(--bg-color); color: var(--text-color);
    --cq-go:#22c55e; --cq-watch:#f59e0b; --cq-wait:#64748b; --cq-atno:#f3c14b; --cq-unknown: var(--status-color);
    --cq-mono: ui-monospace,"SF Mono","JetBrains Mono",Menlo,Consolas,monospace;
    font-size: 14px; }
  #chase-queue.is-hidden { display: none; }
  .cq-head { padding: 10px 12px; border-bottom: 1px solid var(--border-color);
    background: var(--surface-1); }
  .cq-title-row { display:flex; align-items:baseline; gap:8px; }
  .cq-title { font-weight:700; font-size:.98rem; }
  .cq-status { font:600 10px sans-serif; color: var(--cq-watch); margin-top:6px; }
  .cq-status:empty { display:none; }
  .cq-count { margin-left:auto; font:600 11px var(--cq-mono); color: var(--status-color); }
  .cq-body { flex:1 1 auto; overflow-y:auto; padding:10px; display:flex; flex-direction:column; gap:8px; }
  .cq-body > * { flex:0 0 auto; }
  .cq-empty { color: var(--status-color); font-size:12px; text-align:center; padding:24px 12px; }
  .cq-card { border:1px solid var(--control-border); border-radius:10px; background: var(--bg-color);
    border-left:5px solid var(--cq-spine,#555); padding:7px 10px; cursor:pointer; transition:border-color .12s, background .12s; }
  .cq-card:hover { background: color-mix(in srgb, var(--bg-color) 94%, var(--text-color) 6%); }
  .cq-r1 { display:flex; align-items:center; gap:8px; }
  .cq-flag { font-size:1.05rem; line-height:1; }
  .cq-flag:empty { display:none; }
  .cq-call { font:700 1.12rem/1 var(--cq-mono); letter-spacing:.01em; }
  .cq-op { font:12.5px/1 sans-serif; color: var(--status-color); }
  .cq-op:empty { display:none; }
  .cq-spacer { flex:1 1 auto; }
  .cq-score { display:flex; align-items:center; gap:6px; }
  .cq-meter { width:46px; height:6px; border-radius:4px; background: color-mix(in srgb, var(--bg-color) 80%, var(--text-color) 20%); overflow:hidden; }
  .cq-meter > i { display:block; height:100%; }
  .cq-num { font:700 12px var(--cq-mono); min-width:20px; text-align:right; }
  .cq-grade { font:800 10px var(--cq-mono); padding:1px 5px; border-radius:5px; }
  .cq-grade:empty { display:none; }
  .m-go>i,.g-go{ background:var(--cq-go);} .g-go{color:#053; background:color-mix(in srgb,var(--cq-go) 20%,transparent);}
  .m-watch>i{background:var(--cq-watch);} .g-watch{color:var(--cq-watch); background:color-mix(in srgb,var(--cq-watch) 18%,transparent);}
  .m-wait>i{background:var(--cq-wait);} .g-wait{color:var(--cq-wait); background:color-mix(in srgb,var(--cq-wait) 20%,transparent);}
  .m-unknown>i{background:var(--cq-unknown);} .g-unknown{color:var(--cq-unknown); background:color-mix(in srgb,var(--cq-unknown) 18%,transparent);}
  .cq-r2 { font:12.5px/1.5 var(--cq-mono); color: var(--status-color); margin-top:4px;
    display:flex; flex-wrap:wrap; align-items:baseline; }
  .cq-r2 b { color: var(--text-color); font-weight:700; }
  .cq-r2 .band { font-weight:700; }
  .cq-r2 .dir { font-variant-numeric:tabular-nums; cursor:help; }
  .cq-r2 .sep { opacity:.4; margin:0 5px; }
  .cq-comment { font:13px/1.4 sans-serif; color: var(--status-color); margin-top:5px;
    white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  .cq-chips { display:flex; flex-wrap:wrap; gap:5px; margin-top:5px; }
  .cq-chip { font:800 10.5px/1 sans-serif; letter-spacing:.04em; text-transform:uppercase;
    padding:3px 7px; border-radius:5px; }
  .cq-chip-atno { color:#1c1400; background: var(--cq-atno); }
  .cq-chip-new  { color: var(--text-color); background:transparent;
    border:1px solid color-mix(in srgb, var(--text-color) 45%, transparent); }
  .cq-chip-dupe { color: var(--status-color);
    background: color-mix(in srgb, var(--status-color) 14%, transparent); font-weight:700; }
  .cq-actions { display:flex; gap:6px; margin-top:8px; }
  .cq-act { flex:1 1 auto; font:600 12px sans-serif; border:1px solid var(--control-border);
    background: transparent; color: var(--text-color);
    border-radius: var(--btn-radius-sm, 6px); padding:6px 8px; cursor:pointer;
    transition:background .12s, border-color .12s, color .12s; }
  .cq-act:hover { background: var(--accent-tint); border-color: var(--accent); color: var(--accent-strong); }
  .cq-act:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
  .cq-act:disabled { opacity:.5; cursor:default; }
  .cq-act.busy { opacity:.6; cursor:progress; }
  #cq-toggle { font:600 12px sans-serif; border:1px solid var(--control-border);
    background: var(--bg-color); color: var(--text-color); border-radius: var(--btn-radius, 8px);
    padding:6px 12px; cursor:pointer; box-shadow:0 2px 8px var(--shadow-color); white-space:nowrap;
    transition:background .12s, border-color .12s, color .12s; }
  #cq-toggle:hover { background: var(--accent-tint); border-color: var(--accent); color: var(--accent-strong); }
  #cq-close { border:0; background:transparent; color:var(--status-color); font-size:18px; line-height:1; cursor:pointer; padding:0 2px; margin-left:8px; }
  #cq-close:hover { color: var(--text-color); }
  @media (max-width: 820px){ #chase-queue{ position:absolute; right:0; top:0; z-index:1150; box-shadow:0 0 24px var(--shadow-color);} }
  .cq-card.cq-pinned { border-color: var(--cq-atno); box-shadow: 0 0 0 1px var(--cq-atno) inset; }
  .dx-highlight-label span { font: 700 12px var(--cq-mono); color:#1c1400;
    background: var(--cq-atno); padding:1px 5px; border-radius:4px; white-space:nowrap;
    box-shadow: 0 1px 4px rgba(0,0,0,.35); }
  `;
  const s = document.createElement('style');
  s.id = 'cq-styles';
  s.textContent = css;
  document.head.appendChild(s);
}

let panelEl, bodyEl, countEl, statusEl;

// Map-highlight state. `pinned` is the click-pinned spot (persists until another
// is pinned or the panel closes); hover previews transiently and reverts to the
// pinned spot on mouse-leave. `dxLayerWasOn` remembers the 'DX Cluster' toggle.
let pinned = null;
let dxLayerWasOn = null;
const spotKey = (s) => `${s.dx_call}|${s.band}|${s.freq_khz}`;
const highlightData = (s) => ({ dx_call: s.dx_call, dx_locator: s.dx_locator, band: s.band, freq_khz: s.freq_khz });

function mount() {
  injectStyles();

  const toggle = document.createElement('button');
  toggle.id = 'cq-toggle';
  toggle.textContent = 'Chase Queue';
  // Dock the toggle into the app's existing top-right control cluster if present,
  // otherwise float it top-right.
  const trc = document.getElementById('top-right-controls');
  const trcRight = trc ? (trc.style.right || '15px') : ''; // preserve original anchor
  if (trc) {
    trc.style.transition = 'right .2s ease';
    trc.insertBefore(toggle, trc.firstChild);
  } else {
    Object.assign(toggle.style, { position: 'fixed', top: '10px', right: '12px', zIndex: '1200' });
    document.body.appendChild(toggle);
  }

  panelEl = document.createElement('aside');
  panelEl.id = 'chase-queue';
  panelEl.className = 'is-hidden';
  panelEl.innerHTML = `
    <div class="cq-head">
      <div class="cq-title-row">
        <div class="cq-title">Chase Queue</div>
        <span class="cq-count" id="cq-count">—</span>
        <button id="cq-close" title="Hide Chase Queue">×</button>
      </div>
      <div id="cq-status" class="cq-status"></div>
    </div>
    <div class="cq-body" id="cq-body"><div class="cq-empty">Loading spots…</div></div>`;
  document.body.appendChild(panelEl);
  bodyEl = panelEl.querySelector('#cq-body');
  countEl = panelEl.querySelector('#cq-count');
  statusEl = panelEl.querySelector('#cq-status');

  const PANEL_W = 380;
  // Docking the panel changes the map container width; nudge Leaflet (trackResize)
  // and the azimuth canvas to re-fit so the map isn't left distorted/hidden until
  // a manual zoom. rAF lets the flex layout settle; the timeout covers the control
  // slide transition.
  const reflowMap = () => {
    const fire = () => window.dispatchEvent(new Event('resize'));
    requestAnimationFrame(fire);
    setTimeout(fire, 250);
  };
  const open = () => {
    panelEl.classList.remove('is-hidden');
    toggle.style.display = 'none';
    if (trc) trc.style.right = (PANEL_W + 15) + 'px'; // slide app controls left, over the map
    // Reuse the existing 'DX Cluster' map layer so the listed spots are visible
    // on the map; remember its prior state to restore on close.
    const cb = document.getElementById('show-dxcluster-spots');
    if (cb) {
      dxLayerWasOn = cb.checked;
      if (!cb.checked) { cb.checked = true; cb.dispatchEvent(new Event('change')); }
    }
    reflowMap();
    refresh();
  };
  const close = () => {
    panelEl.classList.add('is-hidden');
    toggle.style.display = '';
    if (trc) trc.style.right = trcRight; // restore original anchor (not '')
    // Clear any highlight and restore the DX Cluster layer to its prior state.
    pinned = null;
    clearChaseQueueHighlight();
    const cb = document.getElementById('show-dxcluster-spots');
    if (cb && dxLayerWasOn === false && cb.checked) { cb.checked = false; cb.dispatchEvent(new Event('change')); }
    reflowMap();
  };
  toggle.addEventListener('click', open);
  panelEl.querySelector('#cq-close').addEventListener('click', close);
}

let hpReachable = true; // false only on a network/5xx failure (not per-spot 4xx)
async function scoreSpot(s) {
  const freqHz = Math.round((s.freq_khz || 0) * 1000);
  if (freqHz <= 0) return null; // no frequency → can't score; don't 400-spam horstprop
  const u = `${HORSTPROP_URL}/v1/score?dx_call=${encodeURIComponent(s.dx_call)}&freq_hz=${freqHz}&grid=${encodeURIComponent(s.dx_locator || '')}`;
  try {
    const r = await fetch(u);
    if (r.status >= 500) { hpReachable = false; return null; } // proxy/horstprop down
    if (!r.ok) return null; // 4xx: horstprop is up, this spot just couldn't be scored
    return await r.json();
  } catch (e) {
    hpReachable = false; // network-level failure
    return null;
  }
}

// flash shows a transient message on the panel status line (tune results, errors).
let flashTimer = null;
function flash(msg, isErr = false) {
  if (!statusEl) return;
  statusEl.textContent = msg;
  statusEl.style.color = isErr ? 'var(--cq-wait)' : 'var(--cq-go)';
  if (flashTimer) clearTimeout(flashTimer);
  flashTimer = setTimeout(() => { statusEl.textContent = ''; statusEl.style.color = ''; }, 5000);
}

// Run a rig action from a card button: mark busy, await, report. Gated at call
// time too (rigTune/operate throw if control isn't permitted).
async function runCardAction(btn, label, fn, okMsg) {
  if (btn.disabled || btn.classList.contains('busy')) return;
  btn.classList.add('busy');
  try {
    await fn();
    flash(okMsg);
  } catch (err) {
    flash(`${label} failed: ${err?.message || 'unknown error'}`, true);
  } finally {
    btn.classList.remove('busy');
  }
}

// chipsHTML renders Wavelog "needed" status as chips. needed is award-oriented
// (confirmation-based): dxcc = new entity, band/mode = new slot. A worked
// band+mode shows a muted "worked" marker (likely a dupe), but only when nothing
// is needed — a needed chip is the more useful signal.
function chipsHTML(enrich) {
  if (!enrich) return '';
  const needed = Array.isArray(enrich.needed) ? enrich.needed : [];
  const chips = [];
  if (needed.includes('dxcc')) chips.push('<span class="cq-chip cq-chip-atno">ATNO</span>');
  if (needed.includes('band')) chips.push('<span class="cq-chip cq-chip-new">New band</span>');
  if (needed.includes('mode')) chips.push('<span class="cq-chip cq-chip-new">New mode</span>');
  if (!chips.length && enrich.worked_before?.worked_band_mode) {
    chips.push('<span class="cq-chip cq-chip-dupe">worked</span>');
  }
  return chips.length ? `<div class="cq-chips">${chips.join('')}</div>` : '';
}

function renderCard(s) {
  const el = document.createElement('div');
  el.className = 'cq-card';
  el.style.setProperty('--cq-spine', bandColor(s.band));
  const sc = s._score;
  const dec = sc ? gradeToDecision(sc.grade) : 'unknown'; // still drives the gauge colour
  const num = sc && sc.score != null ? sc.score : '··';
  const dist = sc && sc.distance_km ? `${Math.round(sc.distance_km).toLocaleString()} km` : '';
  const bearing = sc && Number.isFinite(sc.bearing_deg) ? sc.bearing_deg : null;
  const comment = trimComment(s.comment);
  const flag = flagFromISO(s.country_iso);
  // Show the country name only when it isn't a well-known flag (reduce clutter).
  const showCountry = s.country && !isWellKnown(s.country_iso);
  const freqHz = Math.round((s.freq_khz || 0) * 1000);
  const mode = guessMode(s.freq_khz);

  // Meta line: one consistently-separated row. Build the parts then join with a
  // single separator so every gap is identical (previously the bearing used a
  // raw " · " while everything else used a styled span).
  const meta = [];
  if (showCountry) meta.push(s.country);
  meta.push(`<span class="band" style="color:${bandColor(s.band)}">${s.band}</span>`);
  meta.push(`${fmtFreq(s.freq_khz)} MHz`);
  if (dist) meta.push(dist);
  if (bearing != null) meta.push(`<span class="dir" title="bearing ${Math.round(bearing)}°">${degToCardinal(bearing)}</span>`);
  meta.push(fmtAge(s.age_seconds));
  const metaHTML = meta.join('<span class="sep">·</span>');
  el.innerHTML = `
    <div class="cq-r1">
      <span class="cq-flag">${flag}</span>
      <span class="cq-call">${s.dx_call}</span>
      <span class="cq-op">${s.op_name || ''}</span>
      <span class="cq-spacer"></span>
      <span class="cq-score">
        <span class="cq-meter m-${dec}"><i style="width:${meterPct(num)}%"></i></span>
        <span class="cq-num">${num}</span>
      </span>
    </div>
    <div class="cq-r2">${metaHTML}</div>
    ${chipsHTML(s._enrich)}
    ${comment ? `<div class="cq-comment" title="${comment}">${comment}</div>` : ''}`;
  if (sc && sc.reason) el.title = sc.reason;

  // Rig actions only appear when the local agent exposes a tune-capable rig AND
  // control is permitted (server + agent + UI). Otherwise the card stays a pure
  // readout — most viewers have no agent at all.
  if (freqHz > 0 && canControlRig()) {
    const actions = document.createElement('div');
    actions.className = 'cq-actions';

    const tuneBtn = document.createElement('button');
    tuneBtn.className = 'cq-act';
    tuneBtn.textContent = 'Tune';
    tuneBtn.title = `QSY to ${fmtFreq(s.freq_khz)} MHz${mode ? ` (${mode})` : ''}`;
    tuneBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      runCardAction(tuneBtn, 'Tune', () => rigTune(freqHz, mode), `Tuned ${s.dx_call} — ${fmtFreq(s.freq_khz)} MHz`);
    });
    actions.appendChild(tuneBtn);

    if (bearing != null) {
      const turnBtn = document.createElement('button');
      turnBtn.className = 'cq-act';
      turnBtn.textContent = 'Tune + Turn';
      turnBtn.title = `QSY + rotate beam to ${Math.round(bearing)}°`;
      turnBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        runCardAction(turnBtn, 'Tune + Turn', () => operate(freqHz, mode, bearing, s.dx_call),
          `${s.dx_call} — tuned + beam ${Math.round(bearing)}°`);
      });
      actions.appendChild(turnBtn);
    }
    el.appendChild(actions);

    // Double-click anywhere on the card is a Tune shortcut.
    el.addEventListener('dblclick', () => {
      runCardAction(tuneBtn, 'Tune', () => rigTune(freqHz, mode), `Tuned ${s.dx_call} — ${fmtFreq(s.freq_khz)} MHz`);
    });
  }

  // Map highlight: hover previews this spot (line + marker); leaving reverts to
  // the pinned spot (if any); clicking pins it. Needs a locator to place it.
  if (s.dx_locator) {
    const key = spotKey(s);
    el.classList.add('cq-hl');
    el.addEventListener('mouseenter', () => {
      setChaseQueueHighlight({ ...highlightData(s), pinned: pinned?.key === key });
    });
    el.addEventListener('mouseleave', () => {
      if (pinned) setChaseQueueHighlight({ ...pinned.data, pinned: true });
      else clearChaseQueueHighlight();
    });
    el.addEventListener('click', () => {
      // Idempotent for the same spot, so a double-click (Tune) doesn't unpin.
      pinned = { key, data: highlightData(s) };
      el.parentElement?.querySelectorAll('.cq-pinned').forEach((n) => n.classList.remove('cq-pinned'));
      el.classList.add('cq-pinned');
      setChaseQueueHighlight({ ...pinned.data, pinned: true, select: true });
    });
    if (pinned?.key === key) el.classList.add('cq-pinned');
  }
  return el;
}

function render(spots) {
  bodyEl.innerHTML = '';
  if (!spots.length) {
    const msg = getEnabledBands().size === 0
      ? 'No bands selected.<br>Enable bands on the left.'
      : 'No DX spots on the selected bands.<br>Is the cluster connected?';
    bodyEl.innerHTML = `<div class="cq-empty">${msg}</div>`;
    countEl.textContent = '0';
    return;
  }
  // Auto mode: highest score first; unscored fall to the bottom by recency.
  spots.sort((a, b) => {
    const sa = a._score?.score ?? -1, sb = b._score?.score ?? -1;
    if (sb !== sa) return sb - sa;
    return a.age_seconds - b.age_seconds;
  });
  spots.forEach((s) => bodyEl.appendChild(renderCard(s)));
  countEl.textContent = `${spots.length} spots`;
}

async function refresh() {
  let spots;
  try {
    if (isDemo()) {
      spots = DEMO_SPOTS.slice();
    } else {
      const r = await fetch(DXSPOTS_URL);
      spots = await r.json();
    }
  } catch (e) {
    bodyEl.innerHTML = '<div class="cq-empty">Could not load /api/dxspots.</div>';
    statusEl.textContent = 'spots feed offline';
    return;
  }
  // Mirror the band selection on the left (the .band-enable checkboxes): show
  // only spots on the enabled bands. This deliberately reads the enabled set, not
  // the single-band "current band" radio, so band cycling never narrows the
  // queue — it stays filtered to all selected bands. Filter before the TOP_N cap
  // and scoring so the cap (and horstprop load) applies to relevant spots only.
  const enabled = getEnabledBands();
  spots = (spots || []).filter((s) => enabled.has(s.band)).slice(0, TOP_N);

  hpReachable = true;
  // Score (horstprop, per-spot) and enrich (Wavelog via agent, one batch) run
  // concurrently. Enrichment is best-effort: any failure leaves cards unchipped.
  const enrichP = canLookup()
    ? enrichSpots(spots.map(enrichSpot)).catch(() => null)
    : Promise.resolve(null);
  await Promise.all(spots.map(async (s) => { s._score = await scoreSpot(s); }));
  const enrich = await enrichP;
  if (enrich && Array.isArray(enrich.results)) {
    const byId = new Map(enrich.results.map((r) => [r.id, r]));
    spots.forEach((s) => { s._enrich = byId.get(enrichSpot(s).id) || null; });
  } else {
    spots.forEach((s) => { s._enrich = null; });
  }

  // Status line only surfaces a real problem (horstprop down), not spots that
  // merely lack a frequency to score.
  statusEl.textContent = hpReachable ? '' : 'horstprop unreachable — showing unscored';
  render(spots);
}

function init() {
  mount();
  refresh();
  setInterval(() => { if (!panelEl.classList.contains('is-hidden')) refresh(); }, REFRESH_MS);

  // Follow the left-side band selection live. Debounced so toggling several bands
  // coalesces into one refresh; skipped while hidden (opening refreshes anyway).
  let bandTimer = null;
  const onBandChange = () => {
    if (panelEl.classList.contains('is-hidden')) return;
    clearTimeout(bandTimer);
    bandTimer = setTimeout(refresh, 300);
  };
  document.querySelectorAll('.band-enable').forEach((cb) => cb.addEventListener('change', onBandChange));
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
