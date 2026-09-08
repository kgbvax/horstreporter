// ce-optimize measurement harness for the "prop-latency" run.
// Outputs a single JSON object to stdout; progress/warnings go to stderr.
//
// Delegates the actual measurement to latency_harness_test.go in the root
// package (HORST_LATENCY_HARNESS guard), which serves the real handlers
// in-process over a synthetic prod-scale hub.history with no Postgres
// configured — the measured steady state is the in-memory request path.
// Per-endpoint p50/p95/max/mean in milliseconds.
//
// Gates: tests_passed (go test ./...).

import { spawnSync } from 'node:child_process';

const root = process.cwd();
const out = {
  tests_passed: 0,
  history_size: 0,
  requests_per_ep: 0,
  endpoints: [],
};

const log = (msg) => process.stderr.write(`[measure] ${msg}\n`);

log('running latency harness (go test, in-process)...');
{
  const env = { ...process.env, HORST_LATENCY_HARNESS: '1' };
  const r = spawnSync('go', ['test', '-run', '^TestPropLatencyHarness$', '-count=1', '-v', '.'], {
    cwd: root,
    stdio: 'pipe',
    env,
    timeout: 480_000,
  });
  const stdout = r.stdout?.toString() || '';
  if (r.status !== 0) {
    log(`harness FAILED (exit ${r.status}):\n${(r.stderr?.toString() || '').slice(-2000)}`);
    process.stdout.write(JSON.stringify(out, null, 2) + '\n');
    process.exit(1);
  }
  const line = stdout.split('\n').find((l) => l.startsWith('HARNESS_JSON:'));
  if (!line) {
    log('FATAL: no HARNESS_JSON line in go test output:\n' + stdout.slice(-2000));
    process.stdout.write(JSON.stringify(out, null, 2) + '\n');
    process.exit(1);
  }
  const parsed = JSON.parse(line.slice('HARNESS_JSON:'.length));
  out.history_size = parsed.history_size;
  out.requests_per_ep = parsed.requests_per_ep;
  out.endpoints = parsed.endpoints;
  for (const ep of parsed.endpoints) {
    // Flatten the primary metric onto the top level for the gate/diff tooling.
    if (ep.endpoint === '/api/prop_intel?qth=QTH&minutes=15') {
      out.prop_intel_p50_ms = ep.p50_ms;
      out.prop_intel_p95_ms = ep.p95_ms;
    }
    if (ep.endpoint === '/api/prop_intel/v2?qth=QTH&minutes=15') out.prop_intel_v2_p95_ms = ep.p95_ms;
    if (ep.endpoint === '/api/prop_intel/summary?qth=QTH&minutes=15') out.prop_intel_summary_p95_ms = ep.p95_ms;
    if (ep.endpoint === '/api/dx_conditions?qth=QTH&minutes=15') out.dx_conditions_p95_ms = ep.p95_ms;
    if (ep.endpoint === '/api/hot_bands?qth=QTH&minutes=15') out.hot_bands_p95_ms = ep.p95_ms;
  }
}

if (process.env.MEASURE_SKIP_GATES === '1') {
  out.tests_passed = 1;
  log('gates skipped (MEASURE_SKIP_GATES=1)');
} else {
  log('gate 1/1: go test ./... ...');
  const r = spawnSync('go', ['test', './...'], { cwd: root, stdio: 'pipe' });
  if (r.status !== 0) log(`go test FAILED:\n${(r.stdout?.toString() || '').slice(-2000)}`);
  out.tests_passed = r.status === 0 ? 1 : 0;
}

process.stdout.write(JSON.stringify(out, null, 2) + '\n');
process.exit(out.tests_passed === 1 ? 0 : 1);