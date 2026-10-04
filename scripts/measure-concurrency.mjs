#!/usr/bin/env node
// Runs the concurrent-load harness (concurrency_harness_test.go) and prints
// a compact summary. Env: HORST_LATENCY_HISTORY, HORST_CONCURRENCY_CLIENTS,
// HORST_CONCURRENCY_SECONDS, HORST_CONCURRENCY_INGEST_PER_SEC.
import { spawnSync } from 'node:child_process';

const r = spawnSync('go', ['test', '-run', '^TestConcurrentLoadHarness$', '-count=1', '-v', '.'], {
  stdio: 'pipe',
  env: { ...process.env, HORST_CONCURRENCY_HARNESS: '1' },
  timeout: 600_000,
});
const stdout = r.stdout?.toString() || '';
const line = stdout.split('\n').find((l) => l.startsWith('HARNESS_JSON:'));
if (r.status !== 0 || !line) {
  process.stderr.write(stdout.slice(-3000) + (r.stderr?.toString() || '').slice(-2000));
  process.exit(1);
}
const j = JSON.parse(line.slice('HARNESS_JSON:'.length));
console.log(JSON.stringify(j, null, 1));
