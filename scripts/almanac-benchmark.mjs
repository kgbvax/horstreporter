#!/usr/bin/env node
// Almanac benchmark (plan U9): evaluates the JO62/JO32 acceptance tests from
// docs/reference/typical-hf-openings-jo62.md section 3 against a live
// /api/almanac response. Read-only: a single GET per run.
//
// Usage:
//   node scripts/almanac-benchmark.mjs <base-url> [--qth JO32] [--sfi <n>] [--json]
//   node scripts/almanac-benchmark.mjs --file <almanac.json> [--sfi <n>] [--json]
//   node scripts/almanac-benchmark.mjs --selftest
//
// Adaptations from the reference doc (plan Success Criteria, KTD1):
//   - NA is one region: NA-East tests are judged against all of NA.
//   - Test 8 (NA-West only, incl. "10 m NA-West any hour") is dropped.
//   - 10 m tests are SFI-dependent: reported, never gating.
//   - Test 13 (AN) is informational: reported, never gating.
//   - Test 7 "OC (VK)" uses the app's own VK region (lat -50..-10, lng 110..180).
//
// Evaluation: for each test the UTC hour window maps to 30-min slots
// [from*2, to*2) (slot s = s*30 min after 00:00 UTC). Per slot share = n/m;
// slots with m < m_min are unknown and skipped. The test statistic is the
// median share over known slots (or the max, for "any hour" rarely-tests, so a
// single frequently-open slot fails them). INCONCLUSIVE when fewer than
// MIN_KNOWN_FRACTION of the window's slots are known.
//
// Exit code: 0 when no gating test FAILs, 1 otherwise, 2 on usage/fetch error.

import { readFileSync } from 'node:fs';

// ---- Test table -----------------------------------------------------------
// expect: 'usually' (stat >= min), 'rarely' (stat <= max), 'sometimes'
// (min <= stat <= max). bands/regions with several entries: 'any' = pass if
// any (band, region) combination passes; 'all' = every combination must pass.
// hours: [fromUTC, toUTC) in whole hours; null = all 24 h. stat: 'median'|'max'.
export const TESTS = [
  { id: '1', bands: ['20m'], regions: ['NA'], hours: [13, 18], expect: 'usually', min: 0.80, stat: 'median',
    note: 'NA-East judged against all of NA (AE1)' },
  { id: '2', bands: ['15m'], regions: ['NA'], hours: [13, 16], expect: 'usually', min: 0.60, stat: 'median',
    note: 'NA-East judged against all of NA' },
  { id: '3', bands: ['40m'], regions: ['NA'], hours: [0, 5], expect: 'usually', min: 0.70, stat: 'median',
    note: 'NA-East judged against all of NA' },
  { id: '4', bands: ['20m'], regions: ['AF'], hours: [18, 21], expect: 'usually', min: 0.85, stat: 'median' },
  { id: '5', bands: ['40m', '30m'], regions: ['SA'], combine: 'any', hours: [1, 5], expect: 'usually', min: 0.70, stat: 'median',
    note: '40 m or 30 m: passes if either band passes' },
  { id: '6', bands: ['20m'], regions: ['AS'], hours: [11, 16], expect: 'usually', min: 0.80, stat: 'median',
    note: 'AS excludes JA (own region)' },
  { id: '7', bands: ['20m'], regions: ['VK'], hours: [14, 18], expect: 'usually', min: 0.50, stat: 'median',
    note: 'doc "OC (VK)" -> app region VK' },
  // Test 8 (NA-West 20 m 08-13z rarely; 10 m NA-West any hour) dropped: NA is one region (KTD1).
  { id: '9', bands: ['10m'], regions: ['NA'], hours: [13, 16], expect: 'sometimes', min: 0.30, max: 0.70, stat: 'median',
    sfiDependent: true, note: 'tracks SFI; NA-East judged against all of NA' },
  { id: '10', bands: ['20m'], regions: ['JA'], hours: [6, 10], expect: 'usually', min: 0.50, stat: 'median',
    note: 'medium confidence: check a FAIL before treating it as a bug (Kp-sensitive polar path)' },
  { id: '11a', bands: ['15m'], regions: ['KH6'], hours: null, expect: 'rarely', max: 0.20, stat: 'max',
    note: 'any hour: every known slot <= 20% (AE2)' },
  { id: '11b', bands: ['10m'], regions: ['KH6'], hours: null, expect: 'rarely', max: 0.20, stat: 'max',
    sfiDependent: true, note: 'any hour: every known slot <= 20% (AE2)' },
  { id: '12', bands: ['160m'], regions: ['NA', 'SA', 'AF', 'AS', 'JA', 'OC', 'VK', 'KH6', 'CAR'], combine: 'all',
    hours: null, expect: 'rarely', max: 0.20, stat: 'max',
    note: 'late-September expectation; every non-EU/non-AN region, any hour' },
  { id: '13', bands: ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m'], regions: ['AN'], combine: 'all',
    hours: null, expect: 'rarely', max: 0.20, stat: 'max', informational: true,
    note: 'excluded from pass/fail (few stations)' },
];

// A test with fewer known slots than this fraction of its window is INCONCLUSIVE.
export const MIN_KNOWN_FRACTION = 0.5;
const SLOTS = 48;

// ---- Evaluator ------------------------------------------------------------

function slotRange(hours) {
  if (!hours) return Array.from({ length: SLOTS }, (_, i) => i);
  const [from, to] = hours;
  const out = [];
  // Windows are within one UTC day here; handle wrap anyway.
  const end = to > from ? to * 2 : to * 2 + SLOTS;
  for (let s = from * 2; s < end; s++) out.push(s % SLOTS);
  return out;
}

function slotLabel(s) {
  const min = s * 30;
  return `${String(Math.floor(min / 60)).padStart(2, '0')}${min % 60 ? '30' : '00'}`;
}

function median(xs) {
  const a = [...xs].sort((x, y) => x - y);
  const m = a.length >> 1;
  return a.length % 2 ? a[m] : (a[m - 1] + a[m]) / 2;
}

// Returns {n, m, source} for (band, region). A missing region lane on a band
// that has other lanes means that region never reached k: n = 0 with the
// band-level m borrowed from a sibling lane. A band with no lanes -> null.
function laneFor(payload, band, region) {
  const lanes = Array.isArray(payload.lanes) ? payload.lanes : [];
  const exact = lanes.find((l) => l.band === band && l.region === region);
  if (exact) return { n: exact.n, m: exact.m, source: 'lane' };
  const sibling = lanes.find((l) => l.band === band);
  if (sibling) return { n: new Array(SLOTS).fill(0), m: sibling.m, source: 'no-lane (n=0, band m)' };
  return null;
}

function judge(test, stat) {
  switch (test.expect) {
    case 'usually': return stat >= test.min;
    case 'rarely': return stat <= test.max;
    case 'sometimes': return stat >= test.min && stat <= test.max;
    default: throw new Error(`unknown expect ${test.expect}`);
  }
}

function evalCombo(test, payload, band, region, mMin) {
  const slots = slotRange(test.hours);
  const lane = laneFor(payload, band, region);
  const perSlot = slots.map((s) => {
    const n = lane ? Number(lane.n?.[s] ?? 0) : 0;
    const m = lane ? Number(lane.m?.[s] ?? 0) : 0;
    const known = !!lane && m >= mMin && m > 0;
    return { slot: s, n, m, known, share: known ? n / m : null };
  });
  const shares = perSlot.filter((p) => p.known).map((p) => p.share);
  const base = { band, region, lane: lane ? lane.source : 'band absent', slots: perSlot,
    known: shares.length, total: slots.length };
  const agg = shares.length
    ? { median: median(shares), min: Math.min(...shares), max: Math.max(...shares) }
    : { median: null, min: null, max: null };
  if (shares.length === 0 || shares.length < Math.ceil(slots.length * MIN_KNOWN_FRACTION)) {
    return { ...base, verdict: 'INCONCLUSIVE', ...agg, stat: null };
  }
  const stat = test.stat === 'max' ? agg.max : agg.median;
  return { ...base, ...agg, stat, verdict: judge(test, stat) ? 'PASS' : 'FAIL' };
}

function combineVerdicts(mode, verdicts) {
  if (mode === 'any') {
    if (verdicts.includes('PASS')) return 'PASS';
    if (verdicts.includes('INCONCLUSIVE')) return 'INCONCLUSIVE';
    return 'FAIL';
  }
  // 'all'
  if (verdicts.includes('FAIL')) return 'FAIL';
  if (verdicts.every((v) => v === 'PASS')) return 'PASS';
  // Some inconclusive, none failing: pass on the known ones only if at least one is known.
  return verdicts.includes('PASS') ? 'PASS' : 'INCONCLUSIVE';
}

export function evaluate(payload, opts = {}) {
  const mMin = Number.isFinite(payload?.m_min) ? payload.m_min : 10;
  const results = TESTS.map((test) => {
    const combos = [];
    for (const band of test.bands) for (const region of test.regions) {
      combos.push(evalCombo(test, payload, band, region, mMin));
    }
    const verdict = combineVerdicts(test.combine || 'all', combos.map((c) => c.verdict));
    const gating = !test.informational && !test.sfiDependent;
    return { test, verdict, gating, combos };
  });
  const gatingFails = results.filter((r) => r.gating && r.verdict === 'FAIL').map((r) => r.test.id);
  return { mMin, sfi: opts.sfi ?? null, results, gatingFails, exitCode: gatingFails.length ? 1 : 0 };
}

// ---- Reporting ------------------------------------------------------------

const pct = (x) => (x == null ? '  -' : `${Math.round(x * 100)}%`.padStart(4));

function expectText(t) {
  const win = t.hours ? `${String(t.hours[0]).padStart(2, '0')}-${String(t.hours[1]).padStart(2, '0')}z` : 'any hour';
  const thr = t.expect === 'usually' ? `usually >= ${Math.round(t.min * 100)}%`
    : t.expect === 'rarely' ? `rarely <= ${Math.round(t.max * 100)}%`
      : `sometimes ${Math.round(t.min * 100)}-${Math.round(t.max * 100)}%`;
  return `${t.bands.join('/')} ${t.regions.length > 3 ? 'non-EU' : t.regions.join('/')} ${win}: ${thr} (${t.stat})`;
}

function slotSummary(c) {
  return c.slots.map((p) => `${slotLabel(p.slot)} ${p.known ? `${p.n}/${p.m}` : `?/${p.m}`}`).join('  ');
}

export function formatReport(payload, report) {
  const lines = [];
  const a = payload.area || {};
  const w = payload.window || {};
  lines.push(`Almanac benchmark  qth=${payload.qth ?? '?'}  area=${a.grid4 ?? '?'} r=${a.radius ?? '?'}`
    + ` (${(a.squares || []).length} squares${a.approximate ? ', approximate' : ''})`);
  lines.push(`window ${w.start_day ?? '?'}..${w.end_day ?? '?'} (${w.days ?? '?'} d)  m_min=${report.mMin}`
    + `  k=${payload.k ?? '?'}  watermark_day=${payload.watermark_day ?? '?'}  lanes=${(payload.lanes || []).length}`);
  lines.push(`SFI: ${report.sfi ?? 'unknown'} (10 m tests are SFI-dependent and non-gating)`);
  lines.push('');
  for (const r of report.results) {
    const flags = [r.test.informational ? 'informational' : null, r.test.sfiDependent ? `SFI-dependent, SFI=${report.sfi ?? 'unknown'}` : null]
      .filter(Boolean);
    lines.push(`[${r.verdict.padEnd(12)}] test ${r.test.id.padEnd(3)} ${expectText(r.test)}${flags.length ? `  [${flags.join('; ')}]` : ''}`);
    if (r.test.note) lines.push(`    note: ${r.test.note}`);
    for (const c of r.combos) {
      const showSlots = c.total <= 12;
      lines.push(`    ${c.band} ${c.region}: ${c.verdict} median ${pct(c.median)} min ${pct(c.min)} max ${pct(c.max)}`
        + `  known ${c.known}/${c.total} slots${c.lane !== 'lane' ? `  (${c.lane})` : ''}`);
      if (showSlots) lines.push(`      ${slotSummary(c)}`);
      else if (c.known > 0 && c.max != null) {
        const worst = c.slots.filter((p) => p.known).sort((x, y) => y.share - x.share).slice(0, 3);
        lines.push(`      top slots: ${worst.map((p) => `${slotLabel(p.slot)} ${p.n}/${p.m}`).join('  ')}`);
      }
    }
  }
  lines.push('');
  const count = (v) => report.results.filter((r) => r.gating && r.verdict === v).length;
  lines.push(`gating: ${count('PASS')} pass, ${count('FAIL')} fail, ${count('INCONCLUSIVE')} inconclusive`
    + `${report.gatingFails.length ? `  (failed: ${report.gatingFails.join(', ')})` : ''}`);
  lines.push(`exit ${report.exitCode}`);
  return lines.join('\n');
}

function toJson(payload, report) {
  return {
    qth: payload.qth ?? null, area: payload.area ?? null, window: payload.window ?? null,
    m_min: report.mMin, k: payload.k ?? null, sfi: report.sfi, exit_code: report.exitCode,
    gating_fails: report.gatingFails,
    tests: report.results.map((r) => ({
      id: r.test.id, verdict: r.verdict, gating: r.gating,
      informational: !!r.test.informational, sfi_dependent: !!r.test.sfiDependent,
      expect: r.test.expect, min: r.test.min ?? null, max: r.test.max ?? null, stat: r.test.stat,
      hours: r.test.hours,
      combos: r.combos.map((c) => ({
        band: c.band, region: c.region, verdict: c.verdict, lane: c.lane,
        median: c.median, min: c.min, max: c.max, known: c.known, total: c.total,
        slots: c.slots.map((p) => ({ slot: p.slot, n: p.n, m: p.m, known: p.known })),
      })),
    })),
  };
}

// ---- Self-test ------------------------------------------------------------

// Builds a synthetic payload. spec: [{band, region, share | shareFn(slot), m}]
export function synthPayload(spec, { mMin = 10 } = {}) {
  const lanes = spec.map(({ band, region, share = 0, shareFn, m = 30 }) => {
    const mArr = Array.from({ length: SLOTS }, (_, s) => (typeof m === 'function' ? m(s) : m));
    const n = mArr.map((mm, s) => Math.round(mm * (shareFn ? shareFn(s) : share)));
    return { band, region, n, m: mArr, open_today: false };
  });
  return { qth: 'JO32', area: { grid4: 'JO32', radius: 1, squares: ['JO32'], approximate: false },
    window: { start_day: 'synthetic', end_day: 'synthetic', days: 30 }, m_min: mMin, k: 2,
    watermark_day: -1, lanes, agenda: [] };
}

function selftest() {
  const inWin = (from, to) => (s) => s >= from * 2 && s < to * 2;
  const p = synthPayload([
    // 1: 20m NA 13-18z at 90% -> PASS
    { band: '20m', region: 'NA', shareFn: (s) => (inWin(13, 18)(s) ? 0.9 : 0.1) },
    // 2: 15m NA 13-16z at 40% -> FAIL (needs 60%)
    { band: '15m', region: 'NA', shareFn: (s) => (inWin(13, 16)(s) ? 0.4 : 0.0) },
    // 3: 40m NA: m below m_min over 00-05z -> INCONCLUSIVE
    { band: '40m', region: 'NA', share: 0.9, m: (s) => (s < 10 ? 5 : 30) },
    // 4: 20m AF absent while band 20m present -> n=0 -> FAIL
    // 5: 40m SA 20% (fail) but 30m SA 80% (pass) -> any -> PASS
    { band: '40m', region: 'SA', share: 0.2, m: 30 },
    { band: '30m', region: 'SA', share: 0.8 },
    // 6: 20m AS 11-16z: median 85% with one weak slot -> PASS
    { band: '20m', region: 'AS', shareFn: (s) => (s === 22 ? 0.1 : 0.85) },
    // 7: 20m VK exactly 50% -> PASS (>=)
    { band: '20m', region: 'VK', share: 0.5 },
    // 9: 10m NA 13-16z at 90% -> FAIL but SFI-dependent, non-gating
    { band: '10m', region: 'NA', share: 0.9 },
    // 10: 20m JA 60% -> PASS
    { band: '20m', region: 'JA', share: 0.6 },
    // 11a: 15m KH6 10% but one slot at 40% -> FAIL (max stat)
    { band: '15m', region: 'KH6', shareFn: (s) => (s === 30 ? 0.4 : 0.1) },
    // 12: 160m all-closed lanes, band present via NA lane at 0 -> PASS
    { band: '160m', region: 'NA', share: 0 },
    // 13: 20m AN at 50% -> FAIL, informational only
    { band: '20m', region: 'AN', share: 0.5 },
  ]);
  const expected = { 1: 'PASS', 2: 'FAIL', 3: 'INCONCLUSIVE', 4: 'FAIL', 5: 'PASS', 6: 'PASS', 7: 'PASS',
    9: 'FAIL', 10: 'PASS', '11a': 'FAIL', '11b': 'PASS', 12: 'PASS', 13: 'FAIL' };
  const report = evaluate(p, { sfi: 110 });
  let bad = 0;
  for (const r of report.results) {
    const want = expected[r.test.id];
    const ok = want === r.verdict;
    if (!ok) bad++;
    console.log(`${ok ? 'ok  ' : 'FAIL'} test ${r.test.id.padEnd(3)} got ${r.verdict}${ok ? '' : ` want ${want}`}`);
  }
  const wantFails = ['2', '4', '11a'];
  const failsOk = JSON.stringify(report.gatingFails) === JSON.stringify(wantFails) && report.exitCode === 1;
  if (!failsOk) bad++;
  console.log(`${failsOk ? 'ok  ' : 'FAIL'} gating fails ${JSON.stringify(report.gatingFails)} exit ${report.exitCode}`
    + ` (want ${JSON.stringify(wantFails)} exit 1; 9 and 13 must not gate)`);
  // All-pass payload must exit 0 even with SFI/informational failures.
  const p2 = synthPayload([
    { band: '20m', region: 'NA', share: 0.9 }, { band: '15m', region: 'NA', share: 0.7 },
    { band: '40m', region: 'NA', share: 0.8 }, { band: '20m', region: 'AF', share: 0.9 },
    { band: '40m', region: 'SA', share: 0.8 }, { band: '20m', region: 'AS', share: 0.9 },
    { band: '20m', region: 'VK', share: 0.6 }, { band: '20m', region: 'JA', share: 0.6 },
    { band: '15m', region: 'KH6', share: 0.05 }, { band: '160m', region: 'NA', share: 0.1 },
    { band: '10m', region: 'NA', share: 0.95 }, { band: '10m', region: 'KH6', share: 0.5 },
    { band: '20m', region: 'AN', share: 0.5 },
  ]);
  const r2 = evaluate(p2, {});
  const ok2 = r2.exitCode === 0 && r2.gatingFails.length === 0;
  if (!ok2) bad++;
  console.log(`${ok2 ? 'ok  ' : 'FAIL'} all-gating-pass payload exits 0 (got ${r2.exitCode}, fails ${JSON.stringify(r2.gatingFails)})`);
  // Formatter must not throw.
  formatReport(p, report);
  console.log(bad ? `selftest: ${bad} failure(s)` : 'selftest: all ok');
  return bad ? 1 : 0;
}

// ---- CLI ------------------------------------------------------------------

function usage(msg) {
  if (msg) console.error(`error: ${msg}`);
  console.error('usage: node scripts/almanac-benchmark.mjs <base-url> [--qth JO32] [--sfi <n>] [--json]\n'
    + '       node scripts/almanac-benchmark.mjs --file <almanac.json> [--sfi <n>] [--json]\n'
    + '       node scripts/almanac-benchmark.mjs --selftest');
  return 2;
}

async function main(argv) {
  let base = null; let qth = 'JO32'; let sfi = null; let json = false; let file = null; let self = false;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--qth') qth = argv[++i];
    else if (a === '--sfi') sfi = Number(argv[++i]);
    else if (a === '--json') json = true;
    else if (a === '--file') file = argv[++i];
    else if (a === '--selftest') self = true;
    else if (a === '-h' || a === '--help') return usage();
    else if (a.startsWith('--')) return usage(`unknown flag ${a}`);
    else if (!base) base = a;
    else return usage(`unexpected argument ${a}`);
  }
  if (self) return selftest();
  if (sfi != null && !Number.isFinite(sfi)) return usage('--sfi must be a number');
  if (!qth) return usage('--qth needs a value');

  let payload;
  if (file) {
    try { payload = JSON.parse(readFileSync(file, 'utf8')); } catch (e) { return usage(`cannot read ${file}: ${e.message}`); }
  } else {
    if (!base) return usage('missing <base-url>');
    const url = `${base.replace(/\/+$/, '')}/api/almanac?qth=${encodeURIComponent(qth)}`;
    let res;
    try { res = await fetch(url, { signal: AbortSignal.timeout(20000), headers: { accept: 'application/json' } }); } catch (e) {
      console.error(`fetch ${url} failed: ${e.message}`); return 2;
    }
    if (!res.ok) {
      console.error(`GET ${url} -> ${res.status} ${res.statusText}${res.headers.get('retry-after') ? ` (Retry-After ${res.headers.get('retry-after')})` : ''}`);
      return 2;
    }
    payload = await res.json();
  }
  if (!payload || !Array.isArray(payload.lanes)) { console.error('response has no lanes[] array'); return 2; }

  const report = evaluate(payload, { sfi });
  if (json) console.log(JSON.stringify(toJson(payload, report), null, 2));
  else console.log(formatReport(payload, report));
  return report.exitCode;
}

main(process.argv.slice(2)).then((code) => { process.exitCode = code; }, (e) => {
  console.error(e?.stack || String(e)); process.exitCode = 2;
});
