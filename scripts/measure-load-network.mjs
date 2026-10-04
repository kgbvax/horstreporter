#!/usr/bin/env node
// Page load under an emulated slow network (Chrome DevTools throttling): time
// to the load event and to the first "N spots" status, 4 cold runs.
// Usage: LAT=80 KBPS=3000 node scripts/measure-load-network.mjs   (URL=...)
import { chromium } from 'playwright';
const LAT = +process.env.LAT || 80, KBPS = +process.env.KBPS || 3000, URL_ = process.env.URL || 'http://localhost:8080/';
const b = await chromium.launch(); const res = [];
for (let i = 0; i < 4; i++) {
  const ctx = await b.newContext({ viewport: { width: 1400, height: 800 }, serviceWorkers: 'block' });
  await ctx.addInitScript(() => { localStorage.setItem('qth', 'JO32'); localStorage.setItem('infoShown', 'true'); });
  const p = await ctx.newPage(); const cdp = await ctx.newCDPSession(p);
  await cdp.send('Network.enable'); await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: LAT, downloadThroughput: KBPS * 1024 / 8, uploadThroughput: KBPS * 1024 / 8 }); await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  const t0 = Date.now(); await p.goto(URL_, { waitUntil: 'load' }); const tLoad = Date.now() - t0;
  await p.waitForFunction(() => /spots/.test(document.getElementById('stream-status')?.textContent || ''), null, { timeout: 60000 }).catch(() => {}); const tData = Date.now() - t0;
  await p.waitForFunction(() => document.querySelectorAll('.leaflet-country-fill-pane canvas, #map canvas').length > 1, null, { timeout: 30000 }).catch(() => {});
  res.push({ loadMs: tLoad, firstDataMs: tData }); await ctx.close();
}
console.log(`LAT=${LAT}ms KBPS=${KBPS}`); console.table(res); await b.close();
