#!/usr/bin/env node
// Headless-Chromium UI profiler: page load, 20 s idle, wheel zoom, drag pan,
// style / band / projection switches and the Conditions dock. Reports CPU time
// (script / layout / style), rAF frame times and long tasks per scenario, plus
// the app's own perf timers (localStorage perfProfiling, static/perf.js).
//
// Usage: node scripts/profile-ui.mjs [label]
//   env: URL=http://localhost:8080  (point at scripts/stress-proxy.mjs, :8090,
//        for a contest-scale feed)   MINUTES=60  SURR=1  CPUPROF=1
// Writes tmp/prof/<label>.json (+ .png, and .cpuprofile with CPUPROF=1; the
// latter loads in Chrome DevTools or scripts/cpuprofile-report.mjs).
// Headless Chromium has no GPU: compare runs against each other, not against
// a real browser. Needs a running server (go run . -dev).
import { chromium } from 'playwright';
import fs from 'node:fs';
fs.mkdirSync('tmp/prof', { recursive: true });
const label = process.argv[2] || 'run';
const URL_ = process.env.URL || 'http://localhost:8080';
const browser = await chromium.launch({ args: ['--enable-precise-memory-info', '--js-flags=--expose-gc'] });
const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 } });
await ctx.addInitScript(() => {
  localStorage.setItem('qth', 'JO32'); localStorage.setItem('infoShown', 'true');
  localStorage.setItem('perfProfiling', 'true');
  window.__lt = []; window.__frames = [];
  new PerformanceObserver(l => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask', buffered: true });
  window.__rafOn = false; let last = 0;
  const loop = t => { if (window.__rafOn) { if (last) window.__frames.push(t - last); last = t; } else last = 0; requestAnimationFrame(loop); };
  requestAnimationFrame(loop);
});
const page = await ctx.newPage();
const cdp = await ctx.newCDPSession(page);
await cdp.send('Performance.enable');
await cdp.send('Profiler.enable'); await cdp.send('Profiler.setSamplingInterval', { interval: 500 });
const metrics = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map(m => [m.name, m.value]));
const pct = (a, p) => { if (!a.length) return 0; const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(p / 100 * s.length))]; };
const out = { label, scenarios: {} };

if (process.env.CPUPROF) await cdp.send('Profiler.start');
const t0 = Date.now();
await page.goto(URL_ + '/', { waitUntil: 'load' });
out.load = await page.evaluate(() => ({
  fcp: performance.getEntriesByType('paint').find(p => p.name === 'first-contentful-paint')?.startTime,
  dcl: performance.getEntriesByType('navigation')[0].domContentLoadedEventEnd,
  loadEv: performance.getEntriesByType('navigation')[0].loadEventEnd,
  resources: performance.getEntriesByType('resource').length,
  transfer: performance.getEntriesByType('resource').reduce((a, r) => a + r.transferSize, 0),
  longtasks: window.__lt.slice(), }));
await page.waitForSelector('#qth');
if (process.env.MINUTES) await page.selectOption('#minutes', process.env.MINUTES).catch(e => console.error('minutes', e.message));
if (process.env.SURR) await page.check('#surroundings').catch(e => console.error('surr', e.message));
await page.waitForTimeout(12000); // let stream + first renders settle
out.afterSettle = await page.evaluate(() => ({ heapMB: performance.memory.usedJSHeapSize / 1048576, nodes: document.getElementsByTagName('*').length, svgPaths: document.querySelectorAll('path').length, canvases: document.querySelectorAll('canvas').length, spots: window.__horstDebugSpots?.() }));

async function scenario(name, fn) {
  await page.evaluate(() => { window.__lt.length = 0; window.__frames.length = 0; window.__rafOn = true; globalThis.__horstPerf?.reset(); });
  const m0 = await metrics(); const s = Date.now();
  await fn();
  const wall = Date.now() - s; const m1 = await metrics();
  const r = await page.evaluate(() => { window.__rafOn = false; return { lt: window.__lt.slice(), fr: window.__frames.slice(), perf: globalThis.__horstPerf?.snapshot() }; });
  const d = k => +(((m1[k] - m0[k]) * 1000)).toFixed(0);
  const pm = {}; for (const [k, v] of Object.entries(r.perf?.metrics || {})) pm[k] = { n: v.count, avg: +v.avgMs.toFixed(1), p95: +v.p95Ms.toFixed(1), max: +v.maxMs.toFixed(1) };
  out.scenarios[name] = { wallMs: wall, cpuMs: { task: d('TaskDuration'), script: d('ScriptDuration'), layout: d('LayoutDuration'), style: d('RecalcStyleDuration') },
    frames: { n: r.fr.length, p50: +pct(r.fr, 50).toFixed(1), p95: +pct(r.fr, 95).toFixed(1), max: +Math.max(0, ...r.fr).toFixed(1), over50: r.fr.filter(x => x > 50).length },
    longtasks: { n: r.lt.length, totalMs: +r.lt.reduce((a, b) => a + b, 0).toFixed(0), max: +Math.max(0, ...r.lt).toFixed(0) }, perf: pm };
}
const mapBox = await page.locator('#map').boundingBox();
const cx = mapBox.x + mapBox.width * 0.6, cy = mapBox.y + mapBox.height * 0.5;
await page.mouse.move(cx, cy);
const wheel = async (n, dy, gap) => { for (let i = 0; i < n; i++) { await page.mouse.wheel(0, dy); await page.waitForTimeout(gap); } };
const drag = async (n, dx) => { await page.mouse.move(cx, cy); await page.mouse.down(); for (let i = 1; i <= n; i++) { await page.mouse.move(cx + dx * i / n, cy + (i % 2 ? 6 : -6)); await page.waitForTimeout(16); } await page.mouse.up(); };

await scenario('idle_20s', () => page.waitForTimeout(20000));
await scenario('mercator_wheel_zoom_in', async () => { await wheel(25, -120, 30); await page.waitForTimeout(800); });
await scenario('mercator_wheel_zoom_out', async () => { await wheel(25, 120, 30); await page.waitForTimeout(800); });
await scenario('mercator_drag_pan', async () => { await drag(60, 300); await drag(60, -300); await page.waitForTimeout(500); });
await scenario('style_area', async () => { await page.click('label[for="style-area"]'); await page.waitForTimeout(1500); });
await scenario('style_grid', async () => { await page.click('label[for="style-grid"]'); await page.waitForTimeout(1500); });
await scenario('toggle_band_20m', async () => { await page.locator('#band-toggle-20m, input[id*="20m"]').first().click({ force: true }).catch(() => {}); await page.waitForTimeout(1200); });
await scenario('azimuth_switch', async () => { await page.click('label[for="proj-azimuthal"]'); await page.waitForTimeout(2500); });
await scenario('azimuth_wheel', async () => { await wheel(20, -120, 30); await wheel(20, 120, 30); await page.waitForTimeout(800); });
await scenario('mercator_switch_back', async () => { await page.click('label[for="proj-mercator"]'); await page.waitForTimeout(2500); });
await scenario('conditions_dock_toggle', async () => { await page.getByText('Conditions', { exact: true }).first().click().catch(() => {}); await page.waitForTimeout(2500); });

out.final = await page.evaluate(() => { globalThis.gc?.(); return { heapMB: performance.memory.usedJSHeapSize / 1048576, nodes: document.getElementsByTagName('*').length, svgPaths: document.querySelectorAll('path').length }; });
const m = await metrics(); out.totals = { sec: (Date.now() - t0) / 1000, scriptMs: +(m.ScriptDuration * 1000).toFixed(0), taskMs: +(m.TaskDuration * 1000).toFixed(0), layoutMs: +(m.LayoutDuration * 1000).toFixed(0), styleMs: +(m.RecalcStyleDuration * 1000).toFixed(0), layoutCount: m.LayoutCount, styleCount: m.RecalcStyleCount, jsHeapMB: +(m.JSHeapUsedSize / 1048576).toFixed(1), domNodes: m.Nodes, listeners: m.JSEventListeners };
await page.screenshot({ path: `tmp/prof/${label}.png` });
if (process.env.CPUPROF) {
  const { profile } = await cdp.send('Profiler.stop');
  fs.writeFileSync(`tmp/prof/${label}.cpuprofile`, JSON.stringify(profile));
  // self-time aggregation by function+url
  const dt = profile.timeDeltas; const self = new Map(); const byId = new Map(profile.nodes.map(n => [n.id, n]));
  profile.samples.forEach((id, i) => { const n = byId.get(id); const cf = n.callFrame; const k = `${cf.functionName || '(anon)'} ${cf.url.replace(/^.*\/\/[^/]+/, '')}:${cf.lineNumber + 1}`; self.set(k, (self.get(k) || 0) + (dt[i] || 0)); });
  out.topSelf = [...self.entries()].sort((a, b) => b[1] - a[1]).slice(0, 30).map(([k, v]) => `${(v / 1000).toFixed(0)}ms ${k}`);
}
fs.writeFileSync(`tmp/prof/${label}.json`, JSON.stringify(out, null, 1));
function printSummary(o, perfDetail, topN) {

console.log(o.label, 'load', JSON.stringify({fcp:o.load.fcp, dcl:o.load.dcl, load:o.load.loadEv, res:o.load.resources, KB:Math.round(o.load.transfer/1024), lt:o.load.longtasks.length}), 'settled', JSON.stringify(o.afterSettle));
console.log('scenario'.padEnd(26), 'wall  task script layout style | frames p50 p95 max >50 | LT n/total/max');
for (const [k, s] of Object.entries(o.scenarios)) console.log(k.padEnd(26), String(s.wallMs).padStart(5), String(s.cpuMs.task).padStart(5), String(s.cpuMs.script).padStart(6), String(s.cpuMs.layout).padStart(6), String(s.cpuMs.style).padStart(5), '|', String(s.frames.n).padStart(6), String(s.frames.p50).padStart(4), String(s.frames.p95).padStart(5), String(s.frames.max).padStart(5), String(s.frames.over50).padStart(3), '|', s.longtasks.n, s.longtasks.totalMs, s.longtasks.max);
console.log('final', JSON.stringify(o.final), 'totals', JSON.stringify(o.totals));
if (perfDetail) { for (const [k, s] of Object.entries(o.scenarios)) { const p = Object.entries(s.perf).filter(([n]) => !/render.frame|schedule/.test(n)).map(([n, v]) => `${n}:${v.avg}/${v.max}(${v.n})`).join(' '); if (p) console.log(' ', k, p); } }
if (o.topSelf) console.log(o.topSelf.slice(0, topN || 20).join('\n'));

}
printSummary(out, !!process.env.PERF_DETAIL, 25);
await browser.close();
