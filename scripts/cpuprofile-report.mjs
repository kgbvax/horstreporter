#!/usr/bin/env node
// Inclusive-time report for a .cpuprofile (from scripts/profile-ui.mjs with
// CPUPROF=1): node scripts/cpuprofile-report.mjs <file> [topN=30] [filterRegex]
import fs from 'node:fs';
const p = JSON.parse(fs.readFileSync(process.argv[2])); const top = +process.argv[3] || 30; const re = process.argv[4] ? new RegExp(process.argv[4]) : null;
const byId = new Map(p.nodes.map(n => [n.id, n])); const parent = new Map();
p.nodes.forEach(n => (n.children || []).forEach(c => parent.set(c, n.id)));
const selfT = new Map(); p.samples.forEach((id, i) => selfT.set(id, (selfT.get(id) || 0) + (p.timeDeltas[i] || 0)));
const incl = new Map();
for (const [id, t] of selfT) { const seen = new Set(); let cur = id; while (cur !== undefined) { const cf = byId.get(cur).callFrame; const k = `${cf.functionName || '(anon)'} ${cf.url.replace(/^.*\/\/[^/]+/, '')}:${cf.lineNumber + 1}`; if (!seen.has(k)) { seen.add(k); incl.set(k, (incl.get(k) || 0) + t); } cur = parent.get(cur); } }
const rows = [...incl.entries()].filter(([k]) => !/^\((root|program|idle)\)/.test(k) && (!re || re.test(k))).sort((a, b) => b[1] - a[1]).slice(0, top);
for (const [k, v] of rows) console.log(`${(v / 1000).toFixed(0).padStart(6)}ms ${k}`);
