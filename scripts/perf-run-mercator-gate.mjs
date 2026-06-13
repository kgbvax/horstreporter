import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const rootDir = process.cwd();
const reportDir = path.join(rootDir, 'tmp/perf-reports');
const baselinePath = path.join(rootDir, '.perf-baseline.json');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: rootDir,
    stdio: 'inherit',
    shell: process.platform === 'win32',
    env: {
      ...process.env,
      ...(options.env || {})
    }
  });

  if (result.error) {
    console.error(`Failed to run ${command}: ${result.error.message}`);
    process.exit(1);
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

fs.rmSync(reportDir, { recursive: true, force: true });
fs.mkdirSync(reportDir, { recursive: true });

// Use npm script execution for reliable glob handling across environments.
run('npm', ['run', 'test:perf:mercator:report'], {
  env: {
    HORST_PERF_WRITE_REPORT: '1',
    HORST_PERF_REPORT_DIR: reportDir
  }
});
run('node', ['scripts/perf-assert.mjs', baselinePath, reportDir]);
