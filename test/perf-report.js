import fs from 'node:fs';
import path from 'node:path';

function shouldWriteReport() {
    return process.env.HORST_PERF_WRITE_REPORT === '1';
}

function getReportDir() {
    const configured = process.env.HORST_PERF_REPORT_DIR?.trim();
    return configured || path.resolve(process.cwd(), 'tmp/perf-reports');
}

export function writePerfScenarioReport(scenarioName, payload) {
    if (!shouldWriteReport()) return;

    const reportDir = getReportDir();
    fs.mkdirSync(reportDir, { recursive: true });

    const safeName = String(scenarioName || '')
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9_-]+/g, '_');

    const report = {
        scenario: scenarioName,
        createdAt: new Date().toISOString(),
        ...payload
    };

    const outFile = path.join(reportDir, `${safeName}.json`);
    fs.writeFileSync(outFile, JSON.stringify(report, null, 2));
}
