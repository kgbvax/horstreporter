import fs from 'node:fs';
import path from 'node:path';

const rootDir = process.cwd();
const baselinePath = process.argv[2] || path.join(rootDir, '.perf-baseline.json');
const reportDir = process.argv[3] || path.join(rootDir, 'tmp/perf-reports');

const statKeyMap = {
  p95: 'p95Ms',
  p90: 'p90Ms',
  median: 'medianMs',
  avg: 'avgMs',
  min: 'minMs',
  max: 'maxMs',
  count: 'count'
};

function fail(message) {
  console.error(`\n❌ Perf gate failed: ${message}`);
  process.exit(1);
}

function readJson(filePath) {
  try {
    return JSON.parse(fs.readFileSync(filePath, 'utf8'));
  } catch (error) {
    fail(`Could not read JSON file ${filePath}: ${error.message}`);
  }
}

function resolveMetricValue(metrics, thresholdKey) {
  const lastDot = thresholdKey.lastIndexOf('.');
  if (lastDot <= 0) return { found: false, reason: 'invalid-threshold-key' };

  const metricName = thresholdKey.slice(0, lastDot);
  const statName = thresholdKey.slice(lastDot + 1);
  const mappedStat = statKeyMap[statName];
  if (!mappedStat) {
    return { found: false, reason: `unknown-stat:${statName}` };
  }

  const metric = metrics?.[metricName];
  if (!metric || typeof metric !== 'object') {
    return { found: false, reason: `missing-metric:${metricName}` };
  }

  const value = Number(metric[mappedStat]);
  if (!Number.isFinite(value)) {
    return { found: false, reason: `missing-stat:${metricName}.${mappedStat}` };
  }

  return { found: true, value, metricName, statName, mappedStat };
}

function readScenarioReport(scenarioName) {
  const safeName = scenarioName.toLowerCase().replace(/[^a-z0-9_-]+/g, '_');
  const filePath = path.join(reportDir, `${safeName}.json`);
  if (!fs.existsSync(filePath)) {
    return { exists: false, path: filePath };
  }
  return { exists: true, path: filePath, data: readJson(filePath) };
}

if (!fs.existsSync(baselinePath)) {
  fail(`Baseline file not found: ${baselinePath}`);
}
if (!fs.existsSync(reportDir)) {
  fail(`Perf report directory not found: ${reportDir}`);
}

const baseline = readJson(baselinePath);
const scenarios = baseline?.scenarios;
if (!scenarios || typeof scenarios !== 'object') {
  fail('Baseline scenarios section is missing or invalid.');
}

const failures = [];
const checks = [];

for (const [scenarioName, scenarioDef] of Object.entries(scenarios)) {
  const report = readScenarioReport(scenarioName);
  if (!report.exists) {
    failures.push(`Missing report for scenario "${scenarioName}" (${report.path})`);
    continue;
  }

  const thresholds = scenarioDef?.thresholds;
  if (!thresholds || typeof thresholds !== 'object') {
    failures.push(`Scenario "${scenarioName}" has no valid thresholds in baseline.`);
    continue;
  }

  for (const [thresholdKey, maxAllowed] of Object.entries(thresholds)) {
    const max = Number(maxAllowed);
    if (!Number.isFinite(max)) {
      failures.push(`Scenario "${scenarioName}" threshold "${thresholdKey}" has non-numeric max value.`);
      continue;
    }

    const resolved = resolveMetricValue(report.data?.metrics, thresholdKey);
    if (!resolved.found) {
      failures.push(`Scenario "${scenarioName}" missing metric for threshold "${thresholdKey}" (${resolved.reason}).`);
      continue;
    }

    const actual = resolved.value;
    const pass = actual <= max;
    checks.push({ scenarioName, thresholdKey, actual, max, pass });

    if (!pass) {
      failures.push(
        `Scenario "${scenarioName}" exceeded threshold ${thresholdKey}: actual=${actual.toFixed(2)} max=${max.toFixed(2)}`
      );
    }
  }
}

console.log('\nPerf gate summary:');
checks.forEach((check) => {
  const status = check.pass ? 'PASS' : 'FAIL';
  console.log(`- [${status}] ${check.scenarioName} :: ${check.thresholdKey} actual=${check.actual.toFixed(2)} max=${check.max.toFixed(2)}`);
});

if (failures.length > 0) {
  console.error('\nPerf gate failures:');
  failures.forEach((msg) => console.error(`- ${msg}`));
  process.exit(1);
}

console.log('\n✅ Perf gate passed.');
