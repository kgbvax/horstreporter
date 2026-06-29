package main

import "net/http"

// handleConfigPage serves a tiny self-contained settings UI. The tray "Settings"
// item opens this in the operator's browser; it talks to /v1/config and
// /v1/restart on the same origin. No build step, no external assets.
func (s *server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(configPageHTML))
}

const configPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>HorstOperator agent settings</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 15px/1.45 system-ui, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; }
  h1 { font-size: 1.3rem; }
  .sub { opacity: .7; margin-top: -.4rem; }
  fieldset { border: 1px solid #8884; border-radius: 8px; margin: 1rem 0; padding: 1rem; }
  .row { margin: .75rem 0; }
  label { display: block; font-weight: 600; }
  .help { font-size: .82rem; opacity: .7; }
  input[type=text], input[type=password], input[type=number] { width: 100%; padding: .45rem .5rem; box-sizing: border-box; border-radius: 6px; border: 1px solid #8886; }
  .set-badge { font-size: .72rem; color: #2eb84c; margin-left: .4rem; }
  #testpst { margin: .2rem 0 .6rem; }
  #testresult { font-size: .85rem; margin-left: .5rem; }
  .actions { position: sticky; bottom: 0; background: Canvas; padding: .8rem 0; display: flex; gap: .6rem; flex-wrap: wrap; }
  button { font: inherit; padding: .5rem .9rem; border-radius: 6px; border: 1px solid #8886; cursor: pointer; }
  button.primary { background: #2eb84c; color: #fff; border-color: #2eb84c; }
  #msg { margin: .6rem 0; min-height: 1.2em; }
  .ok { color: #2eb84c; } .err { color: #d13a3a; }
  #diag { border: 1px solid #8884; border-radius: 8px; margin: 1rem 0; padding: 1rem; }
  #diag h2 { font-size: 1.05rem; margin: 0 0 .5rem; }
  .ready-badge { font-weight: 700; padding: .15rem .6rem; border-radius: 999px; color: #fff; }
  .ready-badge.yes { background: #2eb84c; } .ready-badge.no { background: #d13a3a; } .ready-badge.wait { background: #888; }
  .check { display: flex; align-items: baseline; gap: .5rem; padding: .3rem 0; border-top: 1px solid #8882; }
  .dot { width: .7rem; height: .7rem; border-radius: 50%; flex: 0 0 auto; }
  .dot.up { background: #2eb84c; } .dot.down { background: #d13a3a; } .dot.off { background: #bbb; }
  .check .name { font-weight: 600; min-width: 12rem; }
  .check .detail { opacity: .8; font-size: .86rem; }
  .check .lat { margin-left: auto; opacity: .55; font-size: .78rem; }
</style>
</head>
<body>
<h1>HorstOperator agent</h1>
<p class="sub">Settings are saved to the agent's <code id="envpath">.env</code> and applied on restart.<br>
<span id="logline" style="display:none">Log file: <code id="logpath"></code> &middot; <a href="/v1/status" target="_blank">status JSON</a></span></p>

<section id="diag">
  <h2>Readiness <span id="ready" class="ready-badge wait">checking…</span>
    <button type="button" id="recheck" style="float:right">Re-check all</button></h2>
  <div id="checks"></div>
</section>
<form id="form"><div id="fields">Loading…</div>
<div id="testpst"><button type="button" id="testbtn">Test PSTrotator connection</button><span id="testresult"></span></div>
<div class="actions">
  <button type="button" class="primary" id="save">Save</button>
  <button type="button" id="saverestart" class="primary">Save &amp; Restart</button>
  <button type="button" id="restart">Restart only</button>
</div></form>
<div id="msg"></div>
<script>
const $ = (s) => document.querySelector(s);
let cfg = null;
function esc(s){ return (s||"").replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }

async function load() {
  const r = await fetch('/v1/config'); cfg = await r.json();
  $('#envpath').textContent = cfg.env_file || '.env';
  if (cfg.log_file) { $('#logpath').textContent = cfg.log_file; $('#logline').style.display = ''; }
  const wrap = $('#fields'); wrap.innerHTML = '';
  for (const f of cfg.fields) {
    const v = cfg.values[f.key] || '';
    const isSet = cfg.set[f.key];
    const row = document.createElement('div'); row.className = 'row';
    if (f.kind === 'bool') {
      const checked = String(v).toLowerCase() === 'true' ? 'checked' : '';
      row.innerHTML = '<label>' + esc(f.label) + ' <input type="checkbox" data-key="' + f.key + '" ' + checked + '></label>' +
                      '<div class="help">' + esc(f.help) + '</div>';
    } else if (f.kind === 'select') {
      const opts = (f.options || []).map(o =>
        '<option value="' + esc(o.value) + '"' + (String(v) === o.value ? ' selected' : '') + '>' + esc(o.label) + '</option>').join('');
      row.innerHTML = '<label>' + esc(f.label) + '</label>' +
                      '<select data-key="' + f.key + '">' + opts + '</select>' +
                      '<div class="help">' + esc(f.help) + '</div>';
    } else {
      const type = f.kind === 'password' ? 'password' : (f.kind === 'number' ? 'number' : 'text');
      const ph = f.secret && isSet ? 'placeholder="•••••• (leave blank to keep)"' : '';
      const badge = isSet ? '<span class="set-badge">set</span>' : '';
      row.innerHTML = '<label>' + esc(f.label) + badge + '</label>' +
                      '<input type="' + type + '" data-key="' + f.key + '" value="' + esc(f.secret ? '' : v) + '" ' + ph + '>' +
                      '<div class="help">' + esc(f.help) + '</div>';
    }
    wrap.appendChild(row);
  }
}

function collect() {
  const values = {};
  document.querySelectorAll('[data-key]').forEach(el => {
    values[el.dataset.key] = el.type === 'checkbox' ? String(el.checked) : el.value;
  });
  return values;
}

async function save() {
  $('#msg').textContent = 'Saving…'; $('#msg').className = '';
  const r = await fetch('/v1/config', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({ values: collect() }) });
  const j = await r.json();
  if (!r.ok) { $('#msg').textContent = 'Error: ' + (j.error || r.status); $('#msg').className = 'err'; return false; }
  $('#msg').textContent = 'Saved. Restart to apply.'; $('#msg').className = 'ok';
  await load();
  return true;
}

async function restart() {
  $('#msg').textContent = 'Restarting agent…'; $('#msg').className = '';
  try { await fetch('/v1/restart', { method:'POST' }); } catch (e) {}
  $('#msg').textContent = 'Agent restarting — this page will reconnect in a few seconds.'; $('#msg').className = 'ok';
  setTimeout(() => location.reload(), 3500);
}

$('#save').onclick = save;
$('#saverestart').onclick = async () => { if (await save()) restart(); };
$('#restart').onclick = restart;

async function testPST() {
  const get = (k) => { const el = document.querySelector('[data-key="' + k + '"]'); return el ? el.value.trim() : ''; };
  const host = get('PST_HOST'), port = parseInt(get('PST_PORT') || '12000', 10);
  const out = $('#testresult');
  if (!host) { out.textContent = 'Enter a PSTrotator host first.'; out.className = 'err'; return; }
  out.textContent = 'Testing ' + host + ':' + port + '…'; out.className = '';
  try {
    const r = await fetch('/v1/pst/test', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({ host, port, timeout_ms: 1500 }) });
    const j = await r.json();
    if (j.ok) { out.textContent = '✓ Reachable — azimuth ' + Math.round(j.azimuth_deg) + '°'; out.className = 'ok'; }
    else { out.textContent = '✗ ' + (j.error || 'no reply'); out.className = 'err'; }
  } catch (e) { out.textContent = '✗ ' + e; out.className = 'err'; }
}
$('#testbtn').onclick = testPST;

async function runDiag() {
  const badge = $('#ready'); badge.className = 'ready-badge wait'; badge.textContent = 'checking…';
  const wrap = $('#checks');
  try {
    const r = await fetch('/v1/diagnostics'); const j = await r.json();
    badge.className = 'ready-badge ' + (j.ready ? 'yes' : 'no');
    badge.textContent = j.ready ? 'READY' : 'NOT READY';
    wrap.innerHTML = '';
    for (const c of j.checks) {
      const cls = !c.configured ? 'off' : (c.ok ? 'up' : 'down');
      const lat = c.configured && c.latency_ms ? (c.latency_ms + ' ms') : '';
      const req = c.required ? ' *' : '';
      const row = document.createElement('div'); row.className = 'check';
      row.innerHTML = '<span class="dot ' + cls + '"></span>' +
                      '<span class="name">' + esc(c.label) + req + '</span>' +
                      '<span class="detail">' + esc(c.detail || (c.configured ? '' : 'not configured')) + '</span>' +
                      '<span class="lat">' + lat + '</span>';
      wrap.appendChild(row);
    }
  } catch (e) {
    badge.className = 'ready-badge no'; badge.textContent = 'error';
    wrap.textContent = 'Diagnostics failed: ' + e;
  }
}
$('#recheck').onclick = runDiag;

load().then(runDiag).catch(e => { $('#msg').textContent = 'Failed to load: ' + e; $('#msg').className = 'err'; });
</script>
</body>
</html>`
