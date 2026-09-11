// Ad-hoc browser verification of the time-travel flow (local dev server).
// Simulates: enter timeline → switch to 24h preset → scrub deep (many chunk
// fetches) → verify the bar persists and moments render; exit restores live.
// Run: node scripts/tl-check.mjs [port]
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://localhost:8125';

const browser = await chromium.launch();
const page = await browser.newPage();
const errors = [];
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', (e) => errors.push(String(e)));
const fetches = [];
page.on('request', (r) => { if (r.url().includes('/api/history')) fetches.push(r.url()); });

await page.goto(BASE, { waitUntil: 'domcontentloaded' });
await page.evaluate(() => document.getElementById('info-overlay')?.remove());

// Seed QTH and start the live stream.
await page.fill('#qth', 'JO62');
await page.click('#btn-submit');
await page.waitForTimeout(3000);

const results = {};

// 1) Enter time travel via the sidebar button (deep in a scrollable sidebar —
// scroll it into view programmatically; Playwright's auto-scroll loses to the
// overflow-auto + body-overflow-hidden combination).
await page.setViewportSize({ width: 1400, height: 1000 });
await page.$eval('#btn-timeline', (el) => el.scrollIntoView({ block: 'center' }));
await page.waitForTimeout(200);
await page.$eval('#btn-timeline', (el) => el.click());

await page.waitForSelector('#timeline-bar', { timeout: 10000 });
results.barShown = true;
await page.waitForFunction(() => {
    const el = document.querySelector('.timeline-clock');
    return el && el.textContent.includes('UTC');
}, { timeout: 20000 });
results.initialMomentLoaded = true;

// 2) Switch to the 24h preset and deep-scrub: 30 slider steps to the far past.
await page.click('.timeline-presets .btn[data-seconds="86400"]');
await page.waitForTimeout(500);
let vanished = false;
for (let i = 0; i < 30; i++) {
    if (!(await page.$('#timeline-bar'))) { vanished = true; break; }
    await page.$eval('.timeline-scrub', (el) => {
        el.value = String(Math.max(0, Number(el.value) - 33));
        el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.waitForTimeout(300);
}
results.barVanishedDuringDeepScrub = vanished;
results.barPersistedAfterDeepScrub = !!(await page.$('#timeline-bar'));

// The deep-past fetch must have used bounded 1h chunks (server caps bundles at
// 24h, but the client must not have requested one giant window).
const spans = fetches.map((u) => {
    const p = new URL(u).searchParams;
    return (Number(p.get('t1')) - Number(p.get('t0'))) / 60;
});
results.maxRequestedSpanMin = Math.max(...spans, 0);
results.chunkFetches = fetches.length;
results.allChunksBounded = results.maxRequestedSpanMin <= 60;

// 3) Afterglow canvas present and painted.
results.afterglowCanvas = !!(await page.$('.afterglow-canvas'));

// 4) Exit restores live.
await page.click('.timeline-exit');
await page.waitForTimeout(1500);
results.barGoneAfterExit = !(await page.$('#timeline-bar'));
const status = await page.textContent('#stream-status').catch(() => '');
results.liveRestored = /Status/.test(status || '');

results.consoleErrors = errors.slice(0, 5);
console.log(JSON.stringify(results, null, 2));

await browser.close();
const ok = results.barPersistedAfterDeepScrub && results.barGoneAfterExit && results.allChunksBounded !== false && results.chunkFetches > 1;
process.exit(ok ? 0 : 1);