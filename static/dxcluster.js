// dxcluster.js — HorstOperator "Chase Queue" module.
//
// A right-docked panel of live DX-cluster spots (from /api/dxspots), each scored
// by the local horstprop service (GET /v1/score). Auto-mode: cards are sorted by
// live score. Self-contained — injects its own styles + toggle button; no edits
// to app.js required. See docs/dxcluster-*.md and docs/horstprop.md.

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
  { dx_call: '3Y0J', spotter: 'LA7GIA', freq_khz: 18145, band: '17m', dx_locator: 'IB59', age_seconds: 21, comment: 'up 5-10 listening EU' },
  { dx_call: 'VK6LC', spotter: 'G3TXF', freq_khz: 14018, band: '20m', dx_locator: 'OF87', age_seconds: 48, comment: '599 long path' },
  { dx_call: 'JA3XYZ', spotter: 'DL1ABC', freq_khz: 21091, band: '15m', dx_locator: 'PM74', age_seconds: 12, comment: 'calling EU' },
  { dx_call: 'W7RV', spotter: 'EA4XYZ', freq_khz: 14205, band: '20m', dx_locator: 'DM43', age_seconds: 55, comment: 'booming in' },
  { dx_call: 'PY2NY', spotter: 'F5ABC', freq_khz: 21094, band: '15m', dx_locator: 'GG66', age_seconds: 64, comment: '-14 dB' },
];

const isDemo = () => location.search.includes('cqdemo');
const trimComment = (c) => (c || '').replace(/\s*\d{3,4}Z\s*$/, '').trim();
const fmtFreq = (khz) => (khz >= 1000 ? (khz / 1000).toFixed(3) : String(khz));
const fmtAge = (s) => s < 60 ? `${s}s` : s < 3600 ? `${Math.round(s / 60)}m` : `${Math.round(s / 3600)}h`;
const meterPct = (v) => Math.max(6, Math.min(100, v || 0));

function injectStyles() {
  if (document.getElementById('cq-styles')) return;
  const css = `
  #chase-queue { flex: 0 0 380px; width: 380px; height: 100%; display: flex; flex-direction: column;
    position: relative; z-index: 1001;
    border-left: 1px solid var(--border-color); background: var(--bg-color); color: var(--text-color);
    --cq-go:#22c55e; --cq-watch:#f59e0b; --cq-wait:#64748b; --cq-atno:#f3c14b; --cq-unknown: var(--status-color);
    --cq-mono: ui-monospace,"SF Mono","JetBrains Mono",Menlo,Consolas,monospace;
    font-size: 13px; }
  #chase-queue.is-hidden { display: none; }
  .cq-head { padding: 10px 12px; border-bottom: 1px solid var(--border-color);
    background: color-mix(in srgb, var(--bg-color) 92%, var(--text-color) 8%); }
  .cq-title-row { display:flex; align-items:baseline; gap:8px; }
  .cq-title { font-weight:700; font-size:.98rem; }
  .cq-status { font:600 10px sans-serif; color: var(--cq-watch); margin-top:6px; }
  .cq-status:empty { display:none; }
  .cq-count { margin-left:auto; font:600 11px var(--cq-mono); color: var(--status-color); }
  .cq-body { flex:1 1 auto; overflow-y:auto; padding:10px; display:flex; flex-direction:column; gap:8px; }
  .cq-body > * { flex:0 0 auto; }
  .cq-empty { color: var(--status-color); font-size:12px; text-align:center; padding:24px 12px; }
  .cq-card { border:1px solid var(--border-color); border-radius:10px; background: var(--bg-color);
    border-left:5px solid var(--cq-spine,#555); padding:7px 10px; cursor:pointer; transition:border-color .12s, background .12s; }
  .cq-card:hover { background: color-mix(in srgb, var(--bg-color) 94%, var(--text-color) 6%); }
  .cq-r1 { display:flex; align-items:center; gap:8px; }
  .cq-call { font:700 1.02rem/1 var(--cq-mono); }
  .cq-spotter { font:11px/1 sans-serif; color: var(--status-color); }
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
  .cq-r2 { font:11px/1.4 var(--cq-mono); color: var(--status-color); margin-top:3px; }
  .cq-r2 b { color: var(--text-color); font-weight:700; }
  .cq-r2 .band { font-weight:700; }
  .cq-r2 .sep { opacity:.5; margin:0 6px; }
  .cq-comment { font:11px/1.3 sans-serif; font-style:italic; color: var(--status-color); margin-top:4px;
    white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  #cq-toggle { font:600 12px sans-serif; border:1px solid var(--border-color);
    background: var(--bg-color); color: var(--text-color); border-radius:999px;
    padding:6px 12px; cursor:pointer; box-shadow:0 2px 8px var(--shadow-color); white-space:nowrap; }
  #cq-close { border:0; background:transparent; color:var(--status-color); font-size:18px; line-height:1; cursor:pointer; padding:0 2px; margin-left:8px; }
  #cq-close:hover { color: var(--text-color); }
  @media (max-width: 820px){ #chase-queue{ position:absolute; right:0; top:0; z-index:1150; box-shadow:0 0 24px var(--shadow-color);} }
  `;
  const s = document.createElement('style');
  s.id = 'cq-styles';
  s.textContent = css;
  document.head.appendChild(s);
}

let panelEl, bodyEl, countEl, statusEl;

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
  const open = () => {
    panelEl.classList.remove('is-hidden');
    toggle.style.display = 'none';
    if (trc) trc.style.right = (PANEL_W + 15) + 'px'; // slide app controls left, over the map
    refresh();
  };
  const close = () => {
    panelEl.classList.add('is-hidden');
    toggle.style.display = '';
    if (trc) trc.style.right = trcRight; // restore original anchor (not '')
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

function renderCard(s) {
  const el = document.createElement('div');
  el.className = 'cq-card';
  el.style.setProperty('--cq-spine', bandColor(s.band));
  const sc = s._score;
  const dec = sc ? gradeToDecision(sc.grade) : 'unknown';
  const num = sc && sc.score != null ? sc.score : '··';
  const grade = sc ? sc.grade : '';
  const dist = sc && sc.distance_km ? `${Math.round(sc.distance_km).toLocaleString()} km` : '';
  const az = sc && sc.bearing_deg ? `${Math.round(sc.bearing_deg)}°` : '';
  const comment = trimComment(s.comment);
  el.innerHTML = `
    <div class="cq-r1">
      <span class="cq-call">${s.dx_call}</span>
      <span class="cq-spotter">de ${s.spotter || '?'}</span>
      <span class="cq-spacer"></span>
      <span class="cq-score">
        <span class="cq-meter m-${dec}"><i style="width:${meterPct(num)}%"></i></span>
        <span class="cq-num">${num}</span>
        <span class="cq-grade g-${dec}">${grade}</span>
      </span>
    </div>
    <div class="cq-r2">
      <span class="band" style="color:${bandColor(s.band)}">${s.band}</span>
      <span class="sep">·</span>${fmtFreq(s.freq_khz)} MHz
      ${dist ? `<span class="sep">·</span>${dist}` : ''}${az ? ` · ${az}` : ''}
      <span class="sep">·</span>${fmtAge(s.age_seconds)}${s.dx_locator ? ` · ${s.dx_locator}` : ''}
    </div>
    ${comment ? `<div class="cq-comment" title="${comment}">${comment}</div>` : ''}`;
  if (sc && sc.reason) el.title = sc.reason;
  return el;
}

function render(spots) {
  bodyEl.innerHTML = '';
  if (!spots.length) {
    bodyEl.innerHTML = '<div class="cq-empty">No DX spots in the window.<br>Is the cluster connected?</div>';
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
  spots = (spots || []).slice(0, TOP_N);

  hpReachable = true;
  await Promise.all(spots.map(async (s) => { s._score = await scoreSpot(s); }));

  // Status line only surfaces a real problem (horstprop down), not spots that
  // merely lack a frequency to score.
  statusEl.textContent = hpReachable ? '' : 'horstprop unreachable — showing unscored';
  render(spots);
}

function init() {
  mount();
  refresh();
  setInterval(() => { if (!panelEl.classList.contains('is-hidden')) refresh(); }, REFRESH_MS);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
