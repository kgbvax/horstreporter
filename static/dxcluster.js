// dxcluster.js — HorstOperator "Chase Queue" module.
//
// A right-docked panel of live DX-cluster spots (from /api/dxspots), each scored
// by the local horstprop service (GET /v1/score). Auto-mode: cards are sorted by
// live score. Self-contained — injects its own styles + toggle button; no edits
// to app.js required. See docs/dxcluster-*.md and docs/horstprop.md.

import { canControlRig, rigTune, operate, canLookup, enrichSpots } from './opmode.js';
import { isChaseQueueEnabled } from './cq-flag.js';
import { setChaseQueueHighlight, clearChaseQueueHighlight } from './app.js';
import { getEnabledBands } from './utils.js';
// Pure helpers (band palette, band plan, mode/POTA classification, formatting)
// live in dxcluster-helpers.js so they can be unit-tested without this module's
// DOM/localStorage/auto-init surface (see that file's header).
import {
  bandColor, gradeToDecision, trimComment, escapeHtml, parseMode, modeCat,
  modeCatFromFreq, guessMode, parsePotaRef, enrichSpot,
  fmtFreq, fmtAge, degToCardinal, meterPct,
} from './dxcluster-helpers.js';

const DXSPOTS_URL = '/api/dxspots?minutes=30';
// Same-origin by default: HorstReporter reverse-proxies /horstprop/* to the
// local horstprop service (see horstprop_mount.go), so this works over the app's
// TLS with no CORS/mixed-content. Override with ?hp=http://127.0.0.1:9970 to hit
// a horstprop instance directly.
const HORSTPROP_URL = new URLSearchParams(location.search).get('hp') || '/horstprop';
const REFRESH_MS = 30000;
const TOP_N = 40; // cap how many spots we score per cycle

// Hidden by default: the Chase Queue is an optional operator surface (prod has
// no DX-cluster ingest). Opt in with ?cq=1 or localStorage 'showChaseQueue'='1'
// — predicate shared with app.js via cq-flag.js.
const CQ_ENABLED = isChaseQueueEnabled();

// Opt-in demo data (?cqdemo=1) so the panel renders without live cluster creds.
const DEMO_SPOTS = [
  { dx_call: '3Y0J', spotter: 'LA7GIA', freq_khz: 18145, band: '17m', dx_locator: 'IB59', age_seconds: 21, comment: 'up 5-10 listening EU', op_name: 'Ken', country: 'Bouvet', country_iso: '' },
  { dx_call: 'VK6LC', spotter: 'G3TXF', freq_khz: 14018, band: '20m', dx_locator: 'OF87', age_seconds: 48, comment: '599 long path', op_name: 'Wayne', country: 'Australia', country_iso: 'AU' },
  { dx_call: 'JA3XYZ', spotter: 'DL1ABC', freq_khz: 21091, band: '15m', dx_locator: 'PM74', age_seconds: 12, comment: 'calling EU', op_name: 'Kenji', country: 'Japan', country_iso: 'JP' },
  { dx_call: '5R8AL', spotter: 'EA4XYZ', freq_khz: 14205, band: '20m', dx_locator: 'LH31', age_seconds: 55, comment: 'booming in', op_name: 'Eric', country: 'Madagascar', country_iso: 'MG' },
  { dx_call: 'PY2NY', spotter: 'F5ABC', freq_khz: 21094, band: '15m', dx_locator: 'GG66', age_seconds: 64, comment: '-14 dB', op_name: 'Vitor', country: 'Brazil', country_iso: 'BR' },
];

const isDemo = () => location.search.includes('cqdemo');

const MODE_CATS = [['cw', 'CW'], ['phone', 'Phone'], ['digi', 'Digi']];

// Enabled mode buckets (persisted). Default all on. A spot whose mode can't be
// determined is shown only while no bucket is filtered out, so the default view
// stays complete but narrowing to e.g. CW drops ambiguous spots too.
const MODE_KEY = 'cqModes';
let enabledModes;
try { enabledModes = new Set(JSON.parse(localStorage.getItem(MODE_KEY) || '["cw","phone","digi"]')); } catch (e) { enabledModes = new Set(['cw', 'phone', 'digi']); }
function modePass(s) {
  const cat = modeCat(s);
  if (!cat) return enabledModes.size >= MODE_CATS.length; // unknown: only when unfiltered
  return enabledModes.has(cat);
}

// Starred ("watch") callsigns — stations the operator always wants to see,
// persisted locally and sortable to the top via the ★ column.
const STAR_KEY = 'cqStars';
let stars;
try { stars = new Set(JSON.parse(localStorage.getItem(STAR_KEY) || '[]')); } catch (e) { stars = new Set(); }
const isStar = (c) => stars.has(c);
function toggleStar(c) {
  if (stars.has(c)) stars.delete(c); else stars.add(c);
  try { localStorage.setItem(STAR_KEY, JSON.stringify([...stars])); } catch (e) { /* ignore quota */ }
}
const starSvg = (on) => `<svg viewBox="0 0 14 14"><path class="${on ? 's-on' : 's-off'}" d="M7 1l1.7 3.9 4.3.4-3.2 2.8 1 4.2L7 10.9 3.2 12.3l1-4.2L1 5.3l4.3-.4z"/></svg>`;

function injectStyles() {
  if (document.getElementById('cq-styles')) return;
  const css = `
  #chase-queue { flex: 0 0 380px; width: 380px; height: 100%; display: flex; flex-direction: column;
    position: relative; z-index: 1001;
    border-left: 1px solid var(--border-color); background: var(--bg-color); color: var(--text-color);
    --cq-go:#22c55e; --cq-watch:#f59e0b; --cq-wait:#64748b; --cq-atno:#f3c14b; --cq-unknown: var(--status-color);
    --cq-gold-strong:#9a7000; --cq-cc:#ffffff;
    --cq-was:#2f9e8f; --cq-pota:#3f8f4f;
    --cq-mono: ui-monospace,"SF Mono","JetBrains Mono",Menlo,Consolas,monospace;
    font-size: 15px; }
  body[data-theme="dark"] #chase-queue { --cq-gold-strong:#f3c14b; --cq-cc:#08201d; }
  #chase-queue.is-hidden { display: none; }

  /* Header: de-prioritised title row, then a separated sortable column band. */
  .cq-head { padding:7px 12px; display:flex; align-items:center; gap:8px; background:var(--bg-color); border-bottom:1px solid var(--border-color); }
  .cq-title { font-size:.86rem; font-weight:600; color:var(--status-color); letter-spacing:.02em; }
  .cq-modes { margin-left:auto; display:flex; gap:3px; }
  .cq-mode { all:unset; cursor:pointer; box-sizing:border-box; font:700 10px/1 sans-serif; letter-spacing:.03em;
    text-transform:uppercase; color:var(--status-color); padding:4px 7px; border-radius:3px; border:1px solid var(--control-border); }
  .cq-mode:hover { color:var(--accent-strong); border-color:var(--accent); }
  .cq-mode.act { background:var(--accent); color:var(--cq-cc); border-color:var(--accent); }
  #cq-close { border:0; background:transparent; color:var(--status-color); font-size:17px; line-height:1; cursor:pointer; padding:0 2px; }
  #cq-close:hover { color:var(--text-color); }
  .cq-status { font:600 11px sans-serif; color:var(--cq-watch); padding:4px 12px 0; }
  .cq-status:empty { display:none; }
  /* tiny validation footer: how many spots we know vs show vs hide, by reason */
  .cq-counts { flex:0 0 auto; font:9.5px/1.3 var(--cq-mono); color:var(--status-color); opacity:.72;
    padding:3px 12px 4px 15px; border-top:1px solid color-mix(in srgb,var(--border-color) 60%,transparent);
    white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  .cq-counts:empty { display:none; }

  .cq-colhead { background:var(--surface-1); border-bottom:1px solid var(--control-border); }
  .cq-colhead.is-hidden { display:none; }
  .cq-fh1, .cq-row .cq-f1 { display:grid; grid-template-columns:24px auto auto 1fr; column-gap:6px; align-items:center; }
  .cq-fh2, .cq-row .cq-f2 { display:grid; grid-template-columns:24px 52px 54px 78px 62px 44px; column-gap:6px; align-items:center; }
  .cq-fh1 { padding:7px 12px 2px 15px; } .cq-fh2 { padding:0 12px 8px 15px; }
  /* every header keeps the same box in every state (constant padding + a reserved
     arrow slot) so sorting never reflows the columns; only background changes */
  .cq-fh1 button, .cq-fh2 button { all:unset; cursor:pointer; display:block; box-sizing:border-box; white-space:nowrap; text-align:right;
    font:700 11px/1.2 sans-serif; letter-spacing:.02em; text-transform:uppercase; color:var(--status-color); padding:3px 6px; border-radius:3px; }
  .cq-fh1 button.lft, .cq-fh2 button.lft { text-align:left; }
  .cq-fh1 button.starh { padding:3px 2px; }
  .cq-fh1 button:hover, .cq-fh2 button:hover { color:var(--accent-strong); }
  .cq-fh1 button.act, .cq-fh2 button.act { background:var(--accent); color:var(--cq-cc); }
  .cq-ar { display:inline-block; width:10px; text-align:center; font-size:9px; }

  .cq-body { flex:1 1 auto; overflow-y:auto; padding:0; }
  .cq-empty { color:var(--status-color); font-size:14px; text-align:center; padding:24px 12px; line-height:1.5; }

  .cq-row { border-left:6px solid var(--cq-spine,#555); border-bottom:1px solid color-mix(in srgb,var(--border-color) 60%,transparent);
    padding:7px 12px 8px 9px; cursor:pointer; }
  .cq-row:hover { background:var(--accent-tint); }
  .cq-row.cq-pinned { background:color-mix(in srgb,var(--cq-atno) 12%,var(--bg-color)); }
  .cq-row.w-atno { border-left:8px solid var(--cq-atno); background:color-mix(in srgb,var(--cq-atno) 11%,var(--bg-color)); }
  .cq-row.w-atno .cq-c { color:var(--cq-gold-strong); }
  .cq-row .cq-f2 { margin-top:3px; }
  .cq-c { font:700 16px var(--cq-mono); }
  .cq-age { text-align:right; color:var(--status-color); font:13.5px var(--cq-mono); }
  .cq-d { font:13.5px var(--cq-mono); color:var(--status-color); }
  /* left padding aligns the comment with the call/band column (past the 24px
     star column + 6px gap), not the row edge */
  .cq-cmt { font:13.5px/1.35 sans-serif; color:var(--status-color); padding:3px 12px 0 30px;
    white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  .cq-d.bandc { font-weight:700; } .cq-d.r { text-align:right; white-space:nowrap; }
  .cq-star { all:unset; cursor:pointer; width:14px; height:14px; display:block; }
  .cq-star svg { width:14px; height:14px; display:block; }
  .cq-star .s-on { fill:var(--cq-atno); } .cq-star .s-off { fill:none; stroke:color-mix(in srgb,var(--text-color) 32%,transparent); stroke-width:1.1; }
  .cq-fbadge { justify-self:start; box-sizing:border-box; font:800 10px/1 sans-serif; text-transform:uppercase; letter-spacing:.03em; padding:3px 6px; border-radius:3px; }
  .cq-fbadge.atno { color:#1c1400; background:var(--cq-atno); }
  .cq-fbadge.band, .cq-fbadge.mode { color:var(--cq-cc); background:var(--accent); }
  .cq-fbadge.was { color:var(--cq-cc); background:var(--cq-was); }
  .cq-fbadge.pota { color:var(--cq-cc); background:var(--cq-pota); }
  .cq-fbadge.worked { color:var(--status-color); background:color-mix(in srgb,var(--status-color) 26%,var(--bg-color)); }
  .cq-bar { justify-self:end; display:inline-block; width:44px; height:6px; border-radius:3px;
    background:color-mix(in srgb,var(--text-color) 13%,transparent); overflow:hidden; }
  .cq-bar > i { display:block; height:100%; border-radius:3px; }
  .cq-bar.g-go>i{background:var(--cq-go);} .cq-bar.g-watch>i{background:var(--cq-watch);} .cq-bar.g-wait>i{background:var(--cq-wait);} .cq-bar.g-unknown>i{background:var(--cq-unknown);}

  /* expand-on-click detail: operator/comment + rig actions */
  .cq-detail { display:none; font:13px/1.4 sans-serif; color:var(--status-color); padding:6px 12px 2px 15px; }
  .cq-row.cq-open .cq-detail { display:block; }
  .cq-detail .k { color:var(--text-color); font-weight:600; }
  .cq-detail .sep { opacity:.4; margin:0 5px; }
  .cq-actions { display:flex; gap:6px; margin-top:7px; }
  .cq-act { flex:1 1 auto; font:600 13px sans-serif; border:1px solid var(--control-border); background:transparent; color:var(--text-color);
    border-radius:var(--btn-radius-sm,6px); padding:5px 8px; cursor:pointer; transition:background .12s, border-color .12s, color .12s; }
  .cq-act:hover { background:var(--accent-tint); border-color:var(--accent); color:var(--accent-strong); }
  .cq-act:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
  .cq-act:disabled { opacity:.5; cursor:default; } .cq-act.busy { opacity:.6; cursor:progress; }

  /* #cq-toggle uses the shared .panel-toggle pill style (style.css). */
  @media (max-width: 820px){ #chase-queue{ position:absolute; right:0; top:0; z-index:1150; box-shadow:0 0 24px var(--shadow-color);} }
  .dx-highlight-label span { font:700 12px var(--cq-mono); color:#1c1400; background:var(--cq-atno);
    padding:1px 5px; border-radius:4px; white-space:nowrap; box-shadow:0 1px 4px rgba(0,0,0,.35); }
  `;
  const s = document.createElement('style');
  s.id = 'cq-styles';
  s.textContent = css;
  document.head.appendChild(s);
}

let panelEl, bodyEl, modesEl, statusEl, colheadEl, countsEl;
let lastSpots = []; // last fetched+scored+enriched spots (band-filtered); sorted client-side
// Spot-flow tallies for the validation footer: raw = from the feed, afterBand =
// survived the band filter, afterCap = survived the TOP_N scoring cap. The mode
// filter is applied last (per render), so its share is computed in renderCounts.
let cqCounts = { raw: 0, afterBand: 0, afterCap: 0 };

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
  toggle.className = 'panel-toggle'; // shared pill style (see style.css)
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
      <span class="cq-title">Chase Queue</span>
      <div class="cq-modes" id="cq-modes"></div>
      <button id="cq-close" title="Hide Chase Queue">×</button>
    </div>
    <div id="cq-status" class="cq-status"></div>
    <div class="cq-colhead is-hidden" id="cq-colhead"></div>
    <div class="cq-body" id="cq-body"><div class="cq-empty">Loading spots…</div></div>
    <div class="cq-counts" id="cq-counts" title="Spots known (from the cluster feed) vs shown, and why the rest are hidden"></div>`;
  document.body.appendChild(panelEl);
  bodyEl = panelEl.querySelector('#cq-body');
  modesEl = panelEl.querySelector('#cq-modes');
  statusEl = panelEl.querySelector('#cq-status');
  colheadEl = panelEl.querySelector('#cq-colhead');
  countsEl = panelEl.querySelector('#cq-counts');
  renderModeFilter();

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
      if (!cb.checked) { window.__horstSetDxcluster ? window.__horstSetDxcluster(true) : (cb.checked = true, cb.dispatchEvent(new Event('change'))); }
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
    if (cb && dxLayerWasOn === false && cb.checked) { window.__horstSetDxcluster ? window.__horstSetDxcluster(false) : (cb.checked = false, cb.dispatchEvent(new Event('change'))); }
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

// wantInfo maps the "needed" enrichment to a wanted state: a sort rank (ATNO
// highest) plus a filled badge. The needed[] vocabulary comes from the operator
// agent: dxcc = all-time-new entity, band/mode = a new DXCC slot, was = a new US
// state (Worked All States), pota = a new POTA park. worked_band_mode = already
// worked. No enrichment (no Wavelog/horstawards) → no label.
function wantInfo(s) {
  const e = s._enrich;
  const needed = e && Array.isArray(e.needed) ? e.needed : [];
  if (needed.includes('dxcc')) return { rank: 0, cls: 'atno', label: 'ATNO' };
  if (needed.includes('band')) return { rank: 1, cls: 'band', label: '+BAND' };
  if (needed.includes('mode')) return { rank: 2, cls: 'mode', label: '+MODE' };
  if (needed.includes('was')) return { rank: 3, cls: 'was', label: '+STATE' };
  if (needed.includes('pota')) return { rank: 4, cls: 'pota', label: 'POTA' };
  if (e && e.worked_before && e.worked_before.worked_band_mode) return { rank: 5, cls: 'worked', label: 'WORKED' };
  return { rank: 6, cls: '', label: '' };
}

// Sort state (persisted). The operator's reading modes are just sort orders:
// DX hunting = wanted, confidence = score, old friends = star, browsing = age.
let sortKey = localStorage.getItem('cqSortKey') || 'wanted';
let sortAsc = localStorage.getItem('cqSortAsc') === '1';
const scoreOf = (s) => (s._score && s._score.score != null) ? s._score.score : -1;
const distOf = (s) => (s._score && s._score.distance_km) ? s._score.distance_km : -1;
// Each comparator is the column's default direction; sortAsc reverses it.
const SORTS = {
  star: (a, b) => (isStar(b.dx_call) - isStar(a.dx_call)) || (scoreOf(b) - scoreOf(a)),
  call: (a, b) => a.dx_call.localeCompare(b.dx_call),
  wanted: (a, b) => (wantInfo(a).rank - wantInfo(b).rank) || (scoreOf(b) - scoreOf(a)),
  score: (a, b) => scoreOf(b) - scoreOf(a),
  age: (a, b) => a.age_seconds - b.age_seconds,
  band: (a, b) => (a.freq_khz || 0) - (b.freq_khz || 0),
  mode: (a, b) => (parseMode(a.comment) || '~~').localeCompare(parseMode(b.comment) || '~~'),
  dist: (a, b) => distOf(b) - distOf(a),
};

// fHead builds the two-tier sortable column header. The arrow slot is always
// present (glyph only on the active column) so the columns never reflow when the
// sort changes; the active header is a solid fill, click it again to flip ▲/▼.
function fHead() {
  const H = (k, label, lft) => {
    const act = sortKey === k;
    const cls = [lft ? 'lft' : '', act ? 'act' : ''].filter(Boolean).join(' ');
    return `<button data-k="${k}" class="${cls}">${label}<span class="cq-ar">${act ? (sortAsc ? '▲' : '▼') : ''}</span></button>`;
  };
  const star = `<button data-k="star" class="starh lft${sortKey === 'star' ? ' act' : ''}">★</button>`;
  return `<div class="cq-fh1">${star}${H('call', 'Call', 1)}${H('wanted', 'Wanted', 1)}<span></span></div>`
       + `<div class="cq-fh2"><span></span>${H('band', 'Band', 1)}${H('mode', 'Mode', 1)}${H('dist', 'Dist')}${H('score', 'Score')}${H('age', 'Age')}</div>`;
}

// renderRow builds one two-line ledger entry: line 1 = ★ · call · wanted · score
// bar · age; line 2 = the folded detail columns band · mode · dist · dir. Click
// expands a detail strip (operator/comment + rig actions) and pins the map
// highlight; the ★ toggles the watch flag.
function renderRow(s) {
  const wi = wantInfo(s);
  const sc = s._score;
  const dec = sc ? gradeToDecision(sc.grade) : 'unknown';
  const scored = sc && sc.score != null;
  const fill = scored ? meterPct(sc.score) : 0;
  const dist = sc && sc.distance_km ? Math.round(sc.distance_km).toLocaleString() : '';
  const bearing = sc && Number.isFinite(sc.bearing_deg) ? sc.bearing_deg : null;
  const dir = bearing != null ? degToCardinal(bearing) : '';
  const mode = parseMode(s.comment);
  const freqHz = Math.round((s.freq_khz || 0) * 1000);
  const comment = trimComment(s.comment);
  const key = spotKey(s);

  const el = document.createElement('div');
  el.className = 'cq-row w-' + (wi.cls || 'none');
  el.style.setProperty('--cq-spine', bandColor(s.band));

  const badge = wi.label ? `<span class="cq-fbadge ${wi.cls}">${escapeHtml(wi.label)}</span>` : '<span></span>';
  el.innerHTML = `
    <div class="cq-f1">
      <button class="cq-star" tabindex="-1">${starSvg(isStar(s.dx_call))}</button>
      <span class="cq-c">${escapeHtml(s.dx_call)}</span>
      ${badge}
      <span></span>
    </div>
    <div class="cq-f2">
      <span></span>
      <span class="cq-d bandc" style="color:${bandColor(s.band)}">${escapeHtml(s.band)}</span>
      <span class="cq-d">${escapeHtml(mode)}</span>
      <span class="cq-d r">${dist ? dist + ' km' : ''}</span>
      <span class="cq-bar g-${dec}"><i style="width:${fill}%"></i></span>
      <span class="cq-age">${fmtAge(s.age_seconds)}</span>
    </div>
    ${comment ? `<div class="cq-cmt" title="${escapeHtml(comment)}">${escapeHtml(comment)}</div>` : ''}
    <div class="cq-detail"></div>`;
  if (sc && sc.reason) el.title = sc.reason;

  // ★ toggles the watch flag (stop propagation so it doesn't pin/expand the row).
  const starBtn = el.querySelector('.cq-star');
  starBtn.title = isStar(s.dx_call) ? 'Starred — click to unstar' : 'Star this station';
  starBtn.addEventListener('click', (e) => { e.stopPropagation(); toggleStar(s.dx_call); sortAndRender(); });

  // Detail strip: operator · country · mode · frequency — comment, + rig actions.
  const detail = el.querySelector('.cq-detail');
  const bits = [];
  if (s.op_name) bits.push(`<span class="k">${escapeHtml(s.op_name)}</span>`);
  if (s.country) bits.push(escapeHtml(s.country));
  if (mode) bits.push(escapeHtml(mode));
  bits.push(`${fmtFreq(s.freq_khz)} MHz`);
  if (dir) bits.push(`beam ${dir}`); // direction lives here now (dropped from the row)
  detail.innerHTML = bits.join('<span class="sep">·</span>'); // comment now shown on its own always-visible line

  // Rig mode prefers the spotter-reported mode, else the frequency default.
  const rigMode = mode || guessMode(s.freq_khz);
  const tunable = freqHz > 0 && canControlRig();
  let tuneBtn = null;
  if (tunable) {
    const actions = document.createElement('div');
    actions.className = 'cq-actions';
    tuneBtn = document.createElement('button');
    tuneBtn.className = 'cq-act';
    tuneBtn.textContent = 'Tune';
    tuneBtn.title = `QSY to ${fmtFreq(s.freq_khz)} MHz${rigMode ? ` (${rigMode})` : ''}`;
    tuneBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      runCardAction(tuneBtn, 'Tune', () => rigTune(freqHz, rigMode), `Tuned ${s.dx_call} — ${fmtFreq(s.freq_khz)} MHz`);
    });
    actions.appendChild(tuneBtn);
    if (bearing != null) {
      const turnBtn = document.createElement('button');
      turnBtn.className = 'cq-act';
      turnBtn.textContent = 'Tune + Turn';
      turnBtn.title = `QSY + rotate beam to ${Math.round(bearing)}°`;
      turnBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        runCardAction(turnBtn, 'Tune + Turn', () => operate(freqHz, rigMode, bearing, s.dx_call),
          `${s.dx_call} — tuned + beam ${Math.round(bearing)}°`);
      });
      actions.appendChild(turnBtn);
    }
    detail.appendChild(actions);
    const tuneHint = `Double-click to tune ${fmtFreq(s.freq_khz)} MHz${rigMode ? ` (${rigMode})` : ''}`;
    el.title = el.title ? `${el.title} · ${tuneHint}` : tuneHint;
  }

  // Hover previews the map highlight; click pins it (and expands the row).
  if (s.dx_locator) {
    el.addEventListener('mouseenter', () => setChaseQueueHighlight({ ...highlightData(s), pinned: pinned?.key === key }));
    el.addEventListener('mouseleave', () => { if (pinned) setChaseQueueHighlight({ ...pinned.data, pinned: true }); else clearChaseQueueHighlight(); });
    if (pinned?.key === key) el.classList.add('cq-pinned', 'cq-open');
  }

  // Single click folds the row open + pins the map highlight; double click tunes.
  // When the row is tunable we defer the single-click action briefly so a
  // double-click can cancel it — otherwise the fold toggle "shadows" the tune.
  const foldAndPin = () => {
    const wasOpen = el.classList.contains('cq-open');
    bodyEl.querySelectorAll('.cq-row.cq-open').forEach((n) => n.classList.remove('cq-open'));
    if (!wasOpen) el.classList.add('cq-open');
    if (s.dx_locator) {
      pinned = { key, data: highlightData(s) };
      bodyEl.querySelectorAll('.cq-pinned').forEach((n) => n.classList.remove('cq-pinned'));
      el.classList.add('cq-pinned');
      setChaseQueueHighlight({ ...pinned.data, pinned: true, select: true });
    }
  };
  if (tunable) {
    let clickTimer = null;
    el.addEventListener('click', () => {
      if (clickTimer) clearTimeout(clickTimer);
      clickTimer = setTimeout(() => { clickTimer = null; foldAndPin(); }, 220);
    });
    el.addEventListener('dblclick', (e) => {
      e.preventDefault(); // cancel text-selection and the pending single-click fold
      if (clickTimer) { clearTimeout(clickTimer); clickTimer = null; }
      runCardAction(tuneBtn, 'Tune', () => rigTune(freqHz, rigMode), `Tuned ${s.dx_call} — ${fmtFreq(s.freq_khz)} MHz`);
    });
  } else {
    el.addEventListener('click', foldAndPin);
  }
  return el;
}

// renderModeFilter draws the CW/Phone/Digi toggle pills in the header and wires
// their clicks. Toggling is client-side (re-filters cached spots, no refetch).
function renderModeFilter() {
  modesEl.innerHTML = MODE_CATS.map(([k, label]) =>
    `<button data-m="${k}" class="cq-mode${enabledModes.has(k) ? ' act' : ''}" title="Show ${label} spots">${label}</button>`).join('');
  modesEl.querySelectorAll('button[data-m]').forEach((b) => b.addEventListener('click', () => {
    const m = b.dataset.m;
    if (enabledModes.has(m)) enabledModes.delete(m); else enabledModes.add(m);
    try { localStorage.setItem(MODE_KEY, JSON.stringify([...enabledModes])); } catch (e) { /* ignore quota */ }
    renderModeFilter();
    sortAndRender();
  }));
}

// sortAndRender rebuilds the sortable header (so the active marker tracks the
// current sort) and the row list. Runs on refresh, on a header click, and on a
// star toggle — all client-side off the cached lastSpots.
// renderCounts writes the tiny validation footer: how many spots the queue knows
// about (from the feed) vs how many it shows, with the hidden remainder broken
// down by reason — band filter, the TOP_N scoring cap, and the mode filter.
function renderCounts(shown) {
  if (!countsEl) return;
  const { raw, afterBand, afterCap } = cqCounts;
  const bandHidden = Math.max(0, raw - afterBand);
  const capHidden = Math.max(0, afterBand - afterCap);
  const modeHidden = Math.max(0, afterCap - shown);
  const hidden = bandHidden + capHidden + modeHidden;
  let txt = `${raw} known \u00b7 ${shown} shown`;
  if (hidden > 0) {
    const reasons = [];
    if (bandHidden) reasons.push(`${bandHidden} band`);
    if (capHidden) reasons.push(`${capHidden} cap`);
    if (modeHidden) reasons.push(`${modeHidden} mode`);
    txt += ` \u00b7 ${hidden} hidden (${reasons.join(', ')})`;
  }
  countsEl.textContent = txt;
}

function sortAndRender() {
  colheadEl.innerHTML = fHead();
  colheadEl.querySelectorAll('button[data-k]').forEach((b) => b.addEventListener('click', () => {
    const k = b.dataset.k;
    if (k === sortKey) sortAsc = !sortAsc; else { sortKey = k; sortAsc = false; }
    try { localStorage.setItem('cqSortKey', sortKey); localStorage.setItem('cqSortAsc', sortAsc ? '1' : '0'); } catch (e) { /* ignore */ }
    sortAndRender();
  }));

  const spots = lastSpots.filter(modePass);
  renderCounts(spots.length);
  bodyEl.innerHTML = '';
  if (!spots.length) {
    colheadEl.classList.add('is-hidden');
    let msg;
    if (getEnabledBands().size === 0) msg = 'No bands selected.<br>Enable bands on the left.';
    else if (enabledModes.size === 0) msg = 'No modes selected.<br>Enable CW / Phone / Digi above.';
    else if (lastSpots.length) msg = 'No spots match the mode filter.';
    else msg = 'No DX spots on the selected bands.<br>Is the cluster connected?';
    bodyEl.innerHTML = `<div class="cq-empty">${msg}</div>`;
    return;
  }
  colheadEl.classList.remove('is-hidden');
  spots.sort(SORTS[sortKey] || SORTS.wanted);
  if (sortAsc) spots.reverse();
  spots.forEach((s) => bodyEl.appendChild(renderRow(s)));
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
    cqCounts = { raw: 0, afterBand: 0, afterCap: 0 };
    if (countsEl) countsEl.textContent = '';
    return;
  }
  // Mirror the band selection on the left (the .band-enable checkboxes): show
  // only spots on the enabled bands. This deliberately reads the enabled set, not
  // the single-band "current band" radio, so band cycling never narrows the
  // queue — it stays filtered to all selected bands. Filter before the TOP_N cap
  // and scoring so the cap (and horstprop load) applies to relevant spots only.
  const raw = Array.isArray(spots) ? spots.length : 0;
  const enabled = getEnabledBands();
  const banded = (spots || []).filter((s) => enabled.has(s.band));
  spots = banded.slice(0, TOP_N);
  cqCounts = { raw, afterBand: banded.length, afterCap: spots.length };

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
  lastSpots = spots;
  sortAndRender();
}

function init() {
  if (!CQ_ENABLED) return;
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
