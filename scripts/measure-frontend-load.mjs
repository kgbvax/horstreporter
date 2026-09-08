// ce-optimize measurement harness for the "frontend-load" run.
// Outputs a single JSON object to stdout; progress/warnings go to stderr.
//
// wire_gzip_kb (primary): eager initial payload — index.html, linked
// stylesheets, blocking vendor <script>s, the vite dist bundle, and the full
// ES-module import graph reachable (static + dynamic imports) from the module
// script entries. Images (e.g. the hk.jpg preload) are excluded by design.
// Script tags injected at runtime via createElement (the lazy-loaded turf
// bundle and the opt-in Chase Queue module) are NOT in this total; they are
// reported separately in lazy_gzip_kb so the eager and deferred halves of the
// first load stay visible.
//
// Gates: tests_passed (npm run check), perf_gate_passed (mercator perf gate).
// Set MEASURE_SKIP_GATES=1 to skip gates (fast local iteration only).

import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';

const root = process.cwd();
const staticDir = path.join(root, 'static');
const out = {
  wire_gzip_kb: 0,
  lazy_gzip_kb: 0,
  tests_passed: 0,
  perf_gate_passed: 0,
  js_raw_bytes: 0,
  css_raw_bytes: 0,
  html_bytes: 0,
  module_request_count: 0,
  dist_bytes: 0,
  vendor_referenced_bytes: 0,
  wire_raw_kb: 0,
  lazy_raw_kb: 0,
};

const log = (msg) => process.stderr.write(`[measure] ${msg}\n`);
const gz = (buf) => zlib.gzipSync(buf, { level: 9 }).length;

let rawTotal = 0;
let gzTotal = 0;
const seen = new Set();

function addFile(absPath, relPath, kind) {
  if (seen.has(relPath)) return;
  seen.add(relPath);
  let buf;
  try {
    buf = fs.readFileSync(absPath);
  } catch {
    log(`WARN missing file: ${relPath}`);
    return;
  }
  rawTotal += buf.length;
  gzTotal += gz(buf);
  if (kind === 'js') out.js_raw_bytes += buf.length;
  else if (kind === 'css') out.css_raw_bytes += buf.length;
  else if (kind === 'html') out.html_bytes += buf.length;
  if (relPath.startsWith('vendor/')) out.vendor_referenced_bytes += buf.length;
  if (relPath.startsWith('dist/')) out.dist_bytes += buf.length;
}

// --- JS module graph walk ------------------------------------------------
const importRe = /\bimport\s+(?:[\w${}\s,*]+\s+from\s+)?['"]([^'"]+)['"]|\bimport\(\s*['"]([^'"]+)['"]\s*\)|\bexport\s+[\w${}\s,*]*\sfrom\s+['"]([^'"]+)['"]/g;
// Runtime-injected script tags: `.src = 'something.js'` in first-party code
// (e.g. renderers.js's ensureTurf, app.js's Chase Queue injection). These are
// deferred fetches the eager payload walk cannot see; they are tallied
// separately as lazy_gzip_kb.
const lazySrcRe = /\.src\s*=\s*['"]([^'"]+)['"]/g;
const lazySeen = new Set();
let lazyRaw = 0;
let lazyGz = 0;

function addLazyFile(absPath, relPath) {
  if (lazySeen.has(relPath) || seen.has(relPath)) return;
  lazySeen.add(relPath);
  let buf;
  try {
    buf = fs.readFileSync(absPath);
  } catch {
    log(`WARN missing lazy script: ${relPath}`);
    return;
  }
  lazyRaw += buf.length;
  lazyGz += gz(buf);
}

function walkModule(absPath, relPath) {
  addFile(absPath, relPath, 'js');
  out.module_request_count++;
  let src;
  try {
    src = fs.readFileSync(absPath, 'utf8');
  } catch {
    return;
  }
  const dir = path.dirname(relPath);
  for (const m of src.matchAll(lazySrcRe)) {
    const spec = m[1];
    // Injected script srcs are root-relative ('dxcluster.js',
    // 'vendor/js/turf.min.js'); skip absolute URLs.
    if (spec.startsWith('http') || spec.startsWith('//') || !spec.endsWith('.js')) continue;
    const lazyRel = path.posix.normalize(path.posix.join(dir, spec));
    if (lazyRel.startsWith('..')) continue;
    addLazyFile(path.join(staticDir, lazyRel), lazyRel);
  }
  for (const m of src.matchAll(importRe)) {
    const spec = m[1] || m[2] || m[3];
    if (!spec || !spec.startsWith('.')) continue; // bare/absolute imports: none expected in first-party code
    const childRel = path.posix.normalize(path.posix.join(dir, spec));
    if (childRel.startsWith('..') || childRel.includes('vendor/')) continue;
    if (childRel.endsWith('.css')) { addFile(path.join(staticDir, childRel), childRel, 'css'); continue; }
    walkModule(path.join(staticDir, childRel), childRel);
  }
}

// --- payload ---------------------------------------------------------------
log('building vite dist (npm run build)...');
{
  const r = spawnSync('npm', ['run', 'build', '--silent'], { cwd: root, stdio: 'pipe', shell: false });
  if (r.status !== 0) {
    process.stderr.write(r.stderr?.toString() || '');
    log('FATAL: vite build failed');
    process.exit(1);
  }
}

const html = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
addFile(path.join(staticDir, 'index.html'), 'index.html', 'html');

// linked stylesheets (blocking; fonts inside css are lazy and excluded)
for (const m of html.matchAll(/<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"/g)) {
  const rel = m[1];
  if (rel.startsWith('http')) continue;
  addFile(path.join(staticDir, rel), rel, 'css');
}
// vite dist css, if emitted and referenced
if (fs.existsSync(path.join(staticDir, 'dist/horst-ui.css'))) {
  addFile(path.join(staticDir, 'dist/horst-ui.css'), 'dist/horst-ui.css', 'css');
}

// classic (non-module) scripts: plain bytes
for (const m of html.matchAll(/<script\s+(?!type="module")[^>]*src="([^"]+)"/g)) {
  addFile(path.join(staticDir, m[1]), m[1], 'js');
}
// module scripts: walk the import graph
for (const m of html.matchAll(/<script\s+type="module"\s+src="([^"]+)"/g)) {
  walkModule(path.join(staticDir, m[1]), m[1]);
}

out.wire_raw_kb = Math.round((rawTotal / 1024) * 10) / 10;
out.wire_gzip_kb = Math.round((gzTotal / 1024) * 10) / 10;
out.lazy_raw_kb = Math.round((lazyRaw / 1024) * 10) / 10;
out.lazy_gzip_kb = Math.round((lazyGz / 1024) * 10) / 10;
log(`payload: raw=${out.wire_raw_kb}KB gzip=${out.wire_gzip_kb}KB modules=${out.module_request_count} lazy=${out.lazy_gzip_kb}KB`);

// --- gates -----------------------------------------------------------------
if (process.env.MEASURE_SKIP_GATES === '1') {
  out.tests_passed = 1;
  out.perf_gate_passed = 1;
  log('gates skipped (MEASURE_SKIP_GATES=1)');
} else {
  log('gate 1/2: npm run check ...');
  {
    const r = spawnSync('npm', ['run', 'check', '--silent'], { cwd: root, stdio: 'pipe' });
    if (r.status !== 0) log(`check FAILED:\n${(r.stderr?.toString() || '').slice(-2000)}`);
    out.tests_passed = r.status === 0 ? 1 : 0;
  }
  log('gate 2/2: mercator perf gate ...');
  {
    const r = spawnSync('node', ['scripts/perf-run-mercator-gate.mjs'], { cwd: root, stdio: 'pipe' });
    if (r.status !== 0) log(`perf gate FAILED:\n${(r.stderr?.toString() || '').slice(-2000)}`);
    out.perf_gate_passed = r.status === 0 ? 1 : 0;
  }
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n');
process.exit(out.tests_passed === 1 && out.perf_gate_passed === 1 ? 0 : 1);
