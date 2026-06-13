#!/usr/bin/env node
import fs from 'node:fs/promises';
import path from 'node:path';

function parseArgs(argv) {
    const out = {};
    for (let i = 2; i < argv.length; i += 1) {
        const arg = argv[i];
        if (!arg.startsWith('--')) continue;
        const key = arg.slice(2);
        const next = argv[i + 1];
        if (!next || next.startsWith('--')) {
            out[key] = 'true';
            continue;
        }
        out[key] = next;
        i += 1;
    }
    return out;
}

function toInt(value, fallback) {
    const n = Number.parseInt(String(value ?? ''), 10);
    return Number.isFinite(n) ? n : fallback;
}

function toBool(value, fallback = false) {
    if (value == null) return fallback;
    const normalized = String(value).trim().toLowerCase();
    return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
}

const args = parseArgs(process.argv);
const baseUrl = String(args['base-url'] || 'http://127.0.0.1:8080').replace(/\/$/, '');
const target = String(args.target || 'JO32').trim().toUpperCase();
const minutes = toInt(args.minutes, 15);
const durationMinutes = toInt(args['duration-minutes'], 360);
const stepSeconds = toInt(args['step-seconds'], 60);
const width = toInt(args.width, 1920);
const height = toInt(args.height, 1080);
const ssbMinDb = toInt(args['ssb-min-db'], 0);
const cwMinDb = toInt(args['cw-min-db'], -15);
const minSnrModeRaw = String(args['min-snr-mode'] || 'ssb').trim().toLowerCase();
const minSnrMode = minSnrModeRaw === 'cw' || minSnrModeRaw === 'ssb' ? minSnrModeRaw : 'none';
const selectedBand = String(args['selected-band'] || 'all').trim().toLowerCase();
const enabledBands = String(args['enabled-bands'] || '160m,80m,60m,40m,30m,20m,17m,15m,12m,10m,6m,4m,2m').trim().toLowerCase();
const projection = String(args.projection || 'mercator').trim().toLowerCase();
const style = String(args.style || 'active-area').trim().toLowerCase();
const countryColoring = toBool(args['country-coloring'], false);
const surroundings = toBool(args.surroundings, false);
const dk3jfMode = toBool(args['dk3jf-mode'], true);
const includeDxcluster = toBool(args['include-dxcluster'], false);
const outputDir = path.resolve(args['output-dir'] || path.join('tmp', 'dk3jf-capture', String(Math.floor(Date.now() / 1000))));

const frameCount = Math.max(1, Math.floor((durationMinutes * 60) / Math.max(1, stepSeconds)) + 1);
const startAt = toInt(args['start-at'], Math.floor(Date.now() / 1000) - durationMinutes * 60);

async function ensureDir(dirPath) {
    await fs.mkdir(dirPath, { recursive: true });
}

async function main() {
    let playwright;
    try {
        playwright = await import('playwright');
    } catch {
        console.error('Missing dependency: playwright. Install with "npm i -D playwright" before running capture.');
        process.exit(1);
    }

    await ensureDir(outputDir);

    const browser = await playwright.chromium.launch({ headless: true });
    const page = await browser.newPage({ viewport: { width, height } });

    const runMeta = {
        baseUrl,
        target,
        minutes,
        durationMinutes,
        stepSeconds,
        width,
        height,
        projection,
        style,
        countryColoring,
        surroundings,
        dk3jfMode,
        includeDxcluster,
        minSnrMode,
        selectedBand,
        enabledBands,
        outputDir,
        frames: []
    };

    for (let index = 0; index < frameCount; index += 1) {
        const snapshotAt = startAt + index * stepSeconds;
        const frameName = `frame_${String(index).padStart(5, '0')}.png`;
        const framePath = path.join(outputDir, frameName);

        const url = new URL(baseUrl);
        url.searchParams.set('capture', '1');
        url.searchParams.set('target', target);
        url.searchParams.set('minutes', String(minutes));
        url.searchParams.set('snapshot_at', String(snapshotAt));
        url.searchParams.set('projection', projection);
        url.searchParams.set('style', style);
        url.searchParams.set('min_snr_mode', minSnrMode);
        url.searchParams.set('ssb_min_db', String(ssbMinDb));
        url.searchParams.set('cw_min_db', String(cwMinDb));
        url.searchParams.set('selected_band', selectedBand);
        url.searchParams.set('enabled_bands', enabledBands);
        url.searchParams.set('country_coloring', countryColoring ? 'true' : 'false');
        url.searchParams.set('surroundings', surroundings ? 'true' : 'false');
        url.searchParams.set('dk3jf_mode', dk3jfMode ? 'true' : 'false');
        url.searchParams.set('include_dxcluster', includeDxcluster ? 'true' : 'false');

        await page.goto(url.toString(), { waitUntil: 'networkidle' });
        await page.waitForFunction(
            () => {
                const marker = window.__horstCaptureReady;
                return marker && typeof marker === 'object' && marker.ready === true;
            },
            { timeout: 30000 }
        );

        await page.screenshot({ path: framePath, type: 'png' });

        const captureState = await page.evaluate(() => window.__horstCaptureReady || null);
        runMeta.frames.push({
            index,
            frameName,
            framePath,
            snapshotAt,
            captureState
        });

        process.stdout.write(`Captured ${index + 1}/${frameCount}: ${frameName}\n`);
    }

    await browser.close();

    const manifestPath = path.join(outputDir, 'capture-manifest.json');
    await fs.writeFile(manifestPath, JSON.stringify(runMeta, null, 2), 'utf8');

    process.stdout.write(`Done. Frames: ${frameCount}\n`);
    process.stdout.write(`Manifest: ${manifestPath}\n`);
}

main().catch((err) => {
    console.error(err);
    process.exit(1);
});
