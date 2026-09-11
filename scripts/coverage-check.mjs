import { execSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// Coverage floor for the Go side (unit U8, mirroring scripts/perf-assert.mjs +
// .perf-baseline.json). Parses `go test ./... -cover` and fails when an in-scope
// package drops below its recorded baseline by more than the tolerance.
//
// Tolerance: 0.5 percentage points. Go's reported percentage is rounded to one
// decimal, and per-run statement counts can shift by ~0.1pp from cached/uncached
// runs, so sub-tolerance drift is not treated as a regression.
//
// Accounting scope: only the packages covered by the test-coverage work
// (horstreporter, cmd/horstoperator-agent) are recorded. internal/awards* and
// internal/awardcontract are intentionally out of scope (award logic stays
// operator-local), and packages reporting 0.0% ("no statements") are skipped,
// never recorded as 0.

const TOLERANCE_PP = 0.5;

const rootDir = process.cwd();
const baselinePath = process.argv[2] || path.join(rootDir, '.coverage-baseline.json');

function fail(message) {
  console.error(`\n❌ Coverage gate failed: ${message}`);
  process.exit(1);
}

function readJson(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch (error) {
    fail(`Could not read JSON file ${filePath}: ${error.message}`);
  }
}

let baseline;
if (fs.existsSync(baselinePath)) {
  baseline = readJson(baselinePath);
} else {
  fail(`Baseline file not found: ${baselinePath}`);
}

const baselinePkgs = baseline?.packages;
if (!baselinePkgs || typeof baselinePkgs !== 'object' || Object.keys(baselinePkgs).length === 0) {
  fail('Baseline file has no "packages" section with recorded coverage values.');
}

let rawOutput;
try {
  rawOutput = execSync('go test ./... -cover', {
    cwd: rootDir,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe']
  });
} catch (error) {
  fail(`go test ./... -cover failed (${error.status}):\n${error.stdout || ''}${error.stderr || ''}`);
}

// Parse `go test` output. Coverage lines look like:
//   ok  	horstreporter	6.892s	coverage: 57.5% of statements
//   FAIL	horstreporter/cmd/x	0.5s	coverage: 12.3% of statements
//   	horstreporter/cmd/no-tests		coverage: 0.0% of statements
const actual = {};
for (const line of rawOutput.split('\n')) {
  const match = line.match(/coverage:\s*([\d.]+)% of statements/);
  if (!match) continue;
  const fields = line.split('\t').map((f) => f.trim()).filter((f) => f !== '');
  const pkg = fields[0] === 'ok' || fields[0] === 'FAIL' || fields[0] === '?' ? fields[1] : fields[0];
  if (!pkg) continue;
  actual[pkg] = Number(match[1]);
}

const checks = [];
const failures = [];

for (const [pkg, floorRaw] of Object.entries(baselinePkgs)) {
  const floor = Number(floorRaw);
  if (!Number.isFinite(floor)) {
    failures.push(`Package "${pkg}" has a non-numeric baseline value.`);
    continue;
  }
  const coverage = actual[pkg];
  if (coverage === undefined) {
    failures.push(`Package "${pkg}" not found in go test output (no coverage reported).`);
    continue;
  }
  const pass = coverage >= floor - TOLERANCE_PP;
  checks.push({ pkg, coverage, floor, pass });
  if (!pass) {
    failures.push(
      `Package "${pkg}" coverage ${coverage.toFixed(1)}% is below baseline ${floor.toFixed(1)}% minus tolerance ${TOLERANCE_PP}pp.`
    );
  }
}

// Informational: packages measured but not tracked by the baseline.
const untracked = Object.keys(actual)
  .filter((pkg) => !(pkg in baselinePkgs) && actual[pkg] > 0)
  .sort();

console.log('\nGo coverage gate summary:');
checks.forEach((check) => {
  const status = check.pass ? 'PASS' : 'FAIL';
  console.log(`- [${status}] ${check.pkg} actual=${check.coverage.toFixed(1)}% floor=${check.floor.toFixed(1)}% (tolerance ${TOLERANCE_PP}pp)`);
});
if (untracked.length > 0) {
  console.log('Measured but not floored (out of U8 scope):');
  untracked.forEach((pkg) => console.log(`- ${pkg} actual=${actual[pkg].toFixed(1)}%`));
}

if (failures.length > 0) {
  console.error('\nGo coverage gate failures:');
  failures.forEach((msg) => console.error(`- ${msg}`));
  process.exit(1);
}

console.log('\n✅ Go coverage gate passed.');