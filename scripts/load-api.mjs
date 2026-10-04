#!/usr/bin/env node
// API load generator against a running server: C concurrent loops over the
// JSON endpoints the web app and mobile widgets poll (hot_bands, dx_conditions,
// prop_intel, prop_intel/summary) for several QTHs, plus S SSE clients.
// Pair with a pprof capture:
//   curl -so cpu.pb.gz 'localhost:6060/debug/pprof/profile?seconds=30' &
//   node scripts/load-api.mjs 30 8 20      (secs, concurrency, sse clients)
// then `go tool pprof -top <binary> cpu.pb.gz` (server started with -pprof).
const secs = +process.argv[2] || 30, conc = +process.argv[3] || 8, sse = +process.argv[4] || 20;
const qths = ['JO32', 'JO62', 'FN31', 'IO91', 'JN48', 'EM73', 'PM95', 'QF56'];
const eps = q => [`hot_bands?qth=${q}&rings=auto`, `dx_conditions?qth=${q}&minutes=15&rings=auto`, `prop_intel?qth=${q}&minutes=15&from_here=1`, `prop_intel/summary?qth=${q}`];
const lat = {}; let stop = false; const end = Date.now() + secs * 1000;
async function worker(id) { let i = id; while (Date.now() < end) { const q = qths[i % qths.length]; const list = eps(q); const u = list[i % list.length]; i++; const t = performance.now(); try { const r = await fetch('http://localhost:8080/api/' + u, { headers: { 'accept-encoding': 'gzip' } }); await r.arrayBuffer(); } catch (e) {} const k = u.split('?')[0]; (lat[k] ||= []).push(performance.now() - t); } }
let sseBytes = 0, sseConn = 0;
async function sseClient(id) { const ac = new AbortController(); setTimeout(() => ac.abort(), secs * 1000); try { const r = await fetch(`http://localhost:8080/api/stream?qth=${qths[id % qths.length]}&minutes=15&rings=auto&v=2`, { signal: ac.signal, headers: { 'accept-encoding': 'gzip' } }); sseConn++; for await (const c of r.body) sseBytes += c.length; } catch (e) {} }
await Promise.all([...Array(conc).keys()].map(worker).concat([...Array(sse).keys()].map(sseClient)));
const pc = (a, p) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(p / 100 * s.length))].toFixed(1); };
for (const [k, v] of Object.entries(lat)) console.log(k.padEnd(20), 'n', String(v.length).padStart(5), 'p50', pc(v, 50).padStart(7), 'p95', pc(v, 95).padStart(7), 'max', pc(v, 100).padStart(7));
console.log('sse clients connected', sseConn, 'bytes', (sseBytes / 1e6).toFixed(1) + 'MB');
