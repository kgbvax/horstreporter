#!/usr/bin/env node
// Measurement wrapper for ce-optimize run #3 (ingest-storage).
//
// Runs the env-gated in-process ingest harness (ingest_footprint_test.go)
// through `go test`, parses the single HARNESS_JSON line, flattens the
// primary metric + diagnostics to the top level, and enforces the gate
// (`go test ./...`). Pass MEASURE_SKIP_GATES=1 to skip the gate while
// iterating on experiments (the harness itself is immutable).
//
// Usage: node scripts/measure-ingest-footprint.mjs

import { execFileSync } from 'node:child_process';

const HARNESS_ENV = 'HORST_INGEST_HARNESS=1';
const RE = /^HARNESS_JSON:(\{.*)$/m;

function run(cmd, args, env = {}) {
  return execFileSync(cmd, args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'inherit'],
    env: { ...process.env, ...env },
  });
}

// --- gate: the whole test suite must pass for a measurement to count ------
if (!process.env.MEASURE_SKIP_GATES) {
  run('go', ['test', './...']);
}

// --- harness run ----------------------------------------------------------
const out = run('go', ['test', '-run', '^TestIngestFootprintHarness$', '-count=1', '-v', '.'], {
  [HARNESS_ENV.split('=')[0]]: HARNESS_ENV.split('=')[1],
});

const m = out.match(RE);
if (!m) {
  console.error('measure-ingest-footprint: no HARNESS_JSON line in go test output');
  process.exit(1);
}

const harness = JSON.parse(m[1]);
const flat = {
  ingest_cpu_ns_per_spot: harness.ingest_cpu_ns_per_spot,
  ingest_allocs_per_spot: harness.ingest_allocs_per_spot,
  ingest_alloc_bytes_per_spot: harness.ingest_alloc_bytes_per_spot,
  parse_only_ns_per_spot: harness.parse_only_ns_per_spot,
  raw_spot_row_width_bytes: harness.raw_spot_row_width_bytes,
  mqtt_message_struct_bytes: harness.mqtt_message_struct_bytes,
  spots_measured: harness.spots_measured,
};

console.log(JSON.stringify(flat, null, 2));