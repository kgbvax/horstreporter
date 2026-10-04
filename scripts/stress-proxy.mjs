#!/usr/bin/env node
// Synthetic contest-scale SSE feed in front of a real dev server, for UI
// profiling: serves /api/stream itself (an initial dump of N spots, then live
// frames at R spots/s, spread over the world and weighted to EU/NA) and
// proxies everything else to localhost:8080.
// Usage: node scripts/stress-proxy.mjs [initialSpots=20000] [livePerSec=80]
// then: URL=http://localhost:8090 node scripts/profile-ui.mjs stress
import http from 'node:http';
const N0 = +process.argv[2] || 20000, RATE = +process.argv[3] || 80;
const bands = ['160m', '80m', '40m', '20m', '17m', '15m', '12m', '10m', '6m'];
const bandW = [1, 4, 8, 12, 5, 6, 3, 4, 1];
const pickBand = () => { let r = Math.random() * bandW.reduce((a, b) => a + b); for (let i = 0; i < bands.length; i++) { if ((r -= bandW[i]) < 0) return bands[i]; } return '20m'; };
// remote-end locators: heavy EU/NA, tail elsewhere
const L = 'ABCDEFGHIJKLMNOPQR';
const regions = [[0.55, 'IJK', 'MNO'], [0.25, 'EFG', 'LMN'], [0.1, 'PQR', 'KLMN'], [0.1, 'ABCDEFGHIJKLMNOPQR', 'ABCDEFGHIJKLMNOPQR']];
const rnd = s => s[Math.floor(Math.random() * s.length)];
function loc() { let r = Math.random(); let reg = regions[3]; for (const x of regions) { if ((r -= x[0]) < 0) { reg = x; break; } } return rnd(reg[1]) + rnd(reg[2]) + Math.floor(Math.random() * 10) + Math.floor(Math.random() * 10) + rnd('abcdefghijklmnopqrstuvwx') + rnd('abcdefghijklmnopqrstuvwx'); }
const homeLocs = ['JO32', 'JO31', 'JO33', 'JO22', 'JO42', 'JN32', 'JN33', 'JO21', 'JO41'];
const home = () => rnd(homeLocs) + rnd('abcdefghijklmnopqrstuvwx') + rnd('abcdefghijklmnopqrstuvwx');
const tuple = (n, age) => [loc(), pickBand(), Math.floor(Math.random() * 45) - 28, age, home()];
let seq = 1000;
const clients = new Set();
setInterval(() => { const n = Math.floor(Date.now() / 1000); const cnt = Math.round(RATE * (0.7 + Math.random() * 0.6)); const s = []; for (let i = 0; i < cnt; i++) s.push(tuple(n, 0)); seq += cnt; const frame = `event: spots\nid: stress-${seq}\ndata: ${JSON.stringify({ n, s })}\n\n`; for (const r of clients) r.write(frame); }, 1000);
setInterval(() => { for (const r of clients) r.write(': hb\n\n'); }, 15000);
http.createServer((req, res) => {
  if (req.url.startsWith('/api/stream')) {
    const minutes = +(new URL(req.url, 'http://x').searchParams.get('minutes') || 15);
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
    res.write(`event: area\ndata: {"centre":"JO32","base_radius":0,"radius":0,"widened":false}\n\n`);
    const n = Math.floor(Date.now() / 1000); const s = [];
    for (let i = 0; i < N0; i++) s.push(tuple(n, Math.floor(Math.random() * minutes * 60)));
    res.write(`event: spots\nid: stress-${seq}\ndata: ${JSON.stringify({ n, s })}\n\n`);
    res.write(`event: history_end\nid: stress-${seq}\ndata: {}\n\n`); // ends the "Loading recent spots" spinner
    clients.add(res); req.on('close', () => clients.delete(res)); return;
  }
  const p = http.request({ host: 'localhost', port: +(process.env.TARGET_PORT || 8080), path: req.url, method: req.method, headers: req.headers }, pr => { res.writeHead(pr.statusCode, pr.headers); pr.pipe(res); });
  p.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(p);
}).listen(+(process.env.PORT || 8090), () => console.log(`stress proxy :8090 initial=${N0} live=${RATE}/s`));
