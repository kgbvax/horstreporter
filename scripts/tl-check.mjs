// Ad-hoc browser verification of the time-travel flow (local dev server).
// Simulates: enter timeline → in-coverage rewind (zero /api/history fetches,
// AE1) → switch to 24h preset → scrub deep (many chunk fetches) → verify the
// bar persists and moments render; exit restores live WITHOUT a new /api/stream
// reconnect (R6: the SSE stays open through the whole replay, KTD-3).
// Run: node scripts/tl-check.mjs [port]
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://localhost:8125';

const browser = await chromium.launch();
const page = await browser.newPage();
const errors = [];
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', (e) => errors.push(String(e)));
const fetches = [];
const streamRequests = [];
page.on('request', (r) => {
    if (r.url().includes('/api/history')) fetches.push(r.url());
    if (r.url().includes('/api/stream')) streamRequests.push(r.url());
});

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

// 1.5) In-coverage rewind (plan U7, AE1). startTimelineMode begins playing at
// 240x the moment enter resolves (and play() jumps the playhead to t0), so
// pause first, then let any in-flight prefetch settle. The initial chunk fetch
// count is taken AFTER the initial moment loads — if the ring already covered
// it this is 0 (ring-synthesized); a non-dev-server session with a seeded ring
// shows 0 here, the empty dev session fetches once. Either way the chunk is
// cached, so the rewind below must add ZERO new /api/history fetches.
await page.click('.timeline-playpause');
await page.waitForTimeout(800);
results.enterFetchCount = fetches.length;
const fetchesBeforeRewind = fetches.length;
// The rewind target is computed from the URL's exact t0/t1 (syncTimelineURL
// writes them on every moment), snapped like the slider path: the input
// handler recomputes t from the frac and seek() re-snaps to 5 min — picking a
// true 5-min boundary at least ~150s below t1 (plus a round-trip margin) makes
// the landed playhead deterministic. The target is kept inside the enter
// chunk's hour (or, in the first minutes of an hour, inside the [t0,t1] chunk
// playback already materialized) so the seek provably stays in
// already-covered territory.
const rewind = await page.evaluate(() => {
    const p = new URLSearchParams(window.location.search);
    const t0 = parseInt(p.get('t0'), 10);
    const t1 = parseInt(p.get('t1'), 10);
    if (!t0 || !t1) return null;
    let s = Math.floor((t1 - 150) / 300) * 300;
    const hourFloor = Math.floor(t1 / 3600) * 3600;
    if (s < hourFloor) s = hourFloor; // never leave the enter chunk's hour
    if (t1 - s < 120) s = Math.floor((t1 - 450) / 300) * 300; // ensure a visible clock move
    if (s <= t0 || s >= t1) return null; // degenerate: nothing to seek
    return { frac: Math.round(((s - t0) / (t1 - t0)) * 1000), s };
});
if (rewind) {
    const clockBefore = await page.textContent('.timeline-clock');
    await page.$eval('.timeline-scrub', (el, v) => {
        el.value = String(v);
        el.dispatchEvent(new Event('input', { bubbles: true }));
    }, rewind.frac);
    await page.waitForFunction((prev) => {
        const el = document.querySelector('.timeline-clock');
        return el && el.textContent !== prev;
    }, clockBefore, { timeout: 10000 });
    results.rewindTargetT = rewind.s;
    results.rewindFetches = fetches.length - fetchesBeforeRewind;
    results.inCoverageRewindNoFetch = results.rewindFetches === 0;
} else {
    results.rewindSkipped = true;
}

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
// 24h, but the client must not have requested one giant window). The
// deep-past chunks are legitimately OUTSIDE the session ring's coverage, so
// the archive path must still fetch there (AE2's inverse: chunkFetches > 1).
const spans = fetches.map((u) => {
    const p = new URL(u).searchParams;
    return (Number(p.get('t1')) - Number(p.get('t0'))) / 60;
});
results.maxRequestedSpanMin = Math.max(...spans, 0);
results.chunkFetches = fetches.length;
results.allChunksBounded = results.maxRequestedSpanMin <= 60;

// 3) Afterglow canvas present and painted.
results.afterglowCanvas = !!(await page.$('.afterglow-canvas'));

// 4) Exit restores live. R6 evidence: the SSE never closed during the timeline
// (KTD-3), so exit must NOT start a new /api/stream request — count stream
// requests across the exit.
const streamCountBeforeExit = streamRequests.length;
await page.click('.timeline-exit');
await page.waitForTimeout(1500);
results.barGoneAfterExit = !(await page.$('#timeline-bar'));
const status = await page.textContent('#stream-status').catch(() => '');
results.liveRestored = /Live for|Loading recent spots|Connecting to live data/.test(status || '');
results.streamReconnectAfterExit = streamRequests.length - streamCountBeforeExit;
results.streamRequestsTotal = streamRequests.length;

results.consoleErrors = errors.slice(0, 5);
console.log(JSON.stringify(results, null, 2));

await browser.close();
const ok = results.barPersistedAfterDeepScrub
    && results.barGoneAfterExit
    && results.allChunksBounded !== false
    && results.chunkFetches > 1
    && (results.rewindSkipped || results.inCoverageRewindNoFetch === true)
    && results.streamReconnectAfterExit === 0;
process.exit(ok ? 0 : 1);