// dxcluster-helpers.js — pure helpers for the Chase Queue module (dxcluster.js).
//
// dxcluster.js auto-runs init() at import time (and touches document/localStorage
// at module top level), so its logic can't be unit-tested in place without
// stubbing the whole DOM graph. This module holds the genuinely pure helpers —
// no DOM, no network, no module state — and dxcluster.js imports them from here.
// The auto-init path in dxcluster.js stays untested (runs in the browser).
// See test/… convention in static/timeline.test.js.

// Canonical band palette (mirrors static/utils.js bandColors).
export const BAND_COLORS = {
  '160m': '#8B0000', '80m': '#800080', '60m': '#4B0082', '40m': '#0000FF',
  '30m': '#03b1b1', '20m': '#008000', '17m': '#808000', '15m': '#FFA500',
  '12m': '#00FFFF', '10m': '#FF0000', '6m': '#FF00FF', '4m': '#FF1493',
  '2m': '#008080', 'all': '#555555',
};
export const bandColor = (b) => BAND_COLORS[b] || BAND_COLORS.all;

export const gradeToDecision = (g) => (g === 'A' || g === 'B') ? 'go' : g === 'C' ? 'watch' : g === 'D' ? 'wait' : 'unknown';

// DXSpider comments arrive padded and end with the spot time + BEL control chars
// (e.g. "...  1015Z\x07\x07"). Strip control chars, the trailing time, and collapse padding.
export const trimComment = (c) => (c || '')
  .replace(/[\u0000-\u001f]+/g, ' ')
  .replace(/\s*\d{3,4}Z\s*$/i, '')
  .replace(/\s{2,}/g, ' ')
  .trim();

// Escape HTML metacharacters before interpolating attacker-influenced fields
// (DX-cluster comments, callsigns, operator names) into innerHTML. Cluster
// comments are free text from any ham on the network, so they must never be
// injected raw.
export const escapeHtml = (v) => String(v ?? '')
  .replaceAll('&', '&amp;')
  .replaceAll('<', '&lt;')
  .replaceAll('>', '&gt;')
  .replaceAll('"', '&quot;')
  .replaceAll("'", '&#39;');

// parseMode reads the mode from the spotter's comment when present (e.g.
// "FT8 -12dB", "CW UP", "599 SSB"). Cluster spots carry no mode field and we
// deliberately do NOT guess it from frequency for display — that's band-map
// logic the operator doesn't want. Empty when the comment names no mode.
export const MODE_RE = /\b(FT8|FT4|JT65|JT9|FST4W?|Q65|RTTY|PSK\d*|MFSK|OLIVIA|CW|SSB|LSB|USB|AM|FM)\b/i;
export function parseMode(comment) {
  const m = (comment || '').match(MODE_RE);
  if (!m) return '';
  const v = m[1].toUpperCase();
  return (v === 'LSB' || v === 'USB') ? 'SSB' : v;
}

// IARU Region 1 HF/VHF band-plan mode segments (this station is in DL / Region 1).
// Each band lists ascending upper-edge kHz → bucket: CW at the band bottom, the
// digimode sub-band next, then the phone ("all modes") segment. Used ONLY to
// bucket comment-less spots for the mode filter — the row still shows no mode
// text, since we deliberately don't guess a mode for display.
export const BAND_PLAN = [
  { lo: 1810, hi: 2000, segs: [[1838, 'cw'], [1843, 'digi'], [2000, 'phone']] },
  { lo: 3500, hi: 3800, segs: [[3570, 'cw'], [3600, 'digi'], [3800, 'phone']] },
  { lo: 5351.5, hi: 5366.5, segs: [[5354, 'cw'], [5366.5, 'phone']] },
  { lo: 7000, hi: 7200, segs: [[7040, 'cw'], [7050, 'digi'], [7200, 'phone']] },
  { lo: 10100, hi: 10150, segs: [[10130, 'cw'], [10150, 'digi']] },
  { lo: 14000, hi: 14350, segs: [[14070, 'cw'], [14099, 'digi'], [14350, 'phone']] },
  { lo: 18068, hi: 18168, segs: [[18095, 'cw'], [18109, 'digi'], [18168, 'phone']] },
  { lo: 21000, hi: 21450, segs: [[21070, 'cw'], [21150, 'digi'], [21450, 'phone']] },
  { lo: 24890, hi: 24990, segs: [[24915, 'cw'], [24931, 'digi'], [24990, 'phone']] },
  { lo: 28000, hi: 29700, segs: [[28070, 'cw'], [28190, 'digi'], [29700, 'phone']] },
  { lo: 50000, hi: 52000, segs: [[50100, 'cw'], [52000, 'phone']] },
  { lo: 70000, hi: 70500, segs: [[70250, 'cw'], [70500, 'phone']] },
  { lo: 144000, hi: 146000, segs: [[144150, 'cw'], [146000, 'phone']] },
];
// Digital watering holes (FT8/FT4/RTTY dials) that sit inside an otherwise-phone
// segment — e.g. 7074, 50313 — so they classify as digi regardless of segment.
export const DIGI_DIALS_KHZ = [1840, 3573, 3575, 5357, 7074, 7047.5, 10136, 10140,
  14074, 14080, 18100, 18104, 21074, 21140, 24915, 24919, 28074, 28180,
  50313, 50318, 70154, 144174];
// modeCatFromFreq buckets a frequency via the band plan; '' when out of any band.
export function modeCatFromFreq(freqKhz) {
  const f = Number(freqKhz) || 0;
  if (f <= 0) return '';
  if (DIGI_DIALS_KHZ.some((d) => Math.abs(f - d) <= 1.5)) return 'digi';
  const band = BAND_PLAN.find((b) => f >= b.lo && f <= b.hi);
  if (!band) return '';
  const seg = band.segs.find(([to]) => f <= to);
  return seg ? seg[1] : 'phone';
}

// modeCat folds a spot into the three operator filter buckets: cw, phone
// (SSB/AM/FM), digi (FT8/FT4/RTTY/PSK/…). Prefers the spotter-reported mode;
// when the comment names none, infers the bucket from the frequency + band plan.
// Returns '' only when neither yields anything (e.g. an out-of-band frequency).
export function modeCat(s) {
  const m = parseMode(s.comment);
  if (m === 'CW') return 'cw';
  if (m === 'SSB' || m === 'AM' || m === 'FM') return 'phone';
  if (m) return 'digi';
  return modeCatFromFreq(s.freq_khz);
}

// guessMode picks a sensible rig mode from the spot frequency. Cluster spots
// carry no mode field, so we infer from the band plan: FT8 watering holes →
// data, the CW portion at the bottom of each band → CW, otherwise '' (the agent
// then defaults to LSB/USB by frequency, and WaveLogGate refines via mode-on-QSY).
export const FT8_DIALS_KHZ = [1840, 3573, 5357, 7074, 10136, 14074, 18100, 21074, 24915, 28074, 50313];
// CW segment upper edges (kHz) — at or below these (and within the band) → CW.
export const CW_EDGE_KHZ = [1838, 3580, 7040, 10150, 14070, 18095, 21070, 24920, 28070];
export function guessMode(freqKhz) {
  const f = Number(freqKhz) || 0;
  if (f <= 0) return '';
  if (FT8_DIALS_KHZ.some((d) => Math.abs(f - d) <= 1.5)) return 'FT8';
  if (CW_EDGE_KHZ.some((edge) => f <= edge && f >= edge - 100)) return 'CW';
  return '';
}

// parsePotaRef extracts a POTA park reference (e.g. "K-1234", "DL-0123") from a
// spot comment. POTA-to-cluster gateways put the park ref in the comment; the
// format is a letter-led prefix, a dash, then 4-6 digits (e.g. K-0817, KH6-0123,
// VK-1234). The letter-led prefix avoids matching freq ranges like "5-10".
// Returns '' when the comment names no park.
export const POTA_RE = /\b([A-Z][A-Z0-9]{0,3}-\d{4,6})\b/i;
export function parsePotaRef(comment) {
  const m = (comment || '').match(POTA_RE);
  return m ? m[1].toUpperCase() : '';
}

// enrichSpot maps a spot to the {id, call, band, mode, pota_ref} the agent's
// enrich endpoint expects. The id is echoed back so results merge by identity;
// pota_ref (when the comment carries a park) drives POTA "wanted".
export const enrichSpot = (s) => {
  const mode = parseMode(s.comment) || guessMode(s.freq_khz);
  return { id: `${s.dx_call}|${s.band}|${mode}`, call: s.dx_call, band: s.band, mode, pota_ref: parsePotaRef(s.comment) };
};

export const fmtFreq = (khz) => (khz >= 1000 ? (khz / 1000).toFixed(3) : String(khz));
export const fmtAge = (s) => s < 60 ? `${s}s` : s < 3600 ? `${Math.round(s / 60)}m` : `${Math.round(s / 3600)}h`;
// degToCardinal maps a beam bearing to a 16-point compass abbreviation (N, NNE,
// NE, …) — operators think in compass headings, not raw degrees. The exact
// degrees stay available in the element title for anyone who needs them.
export const COMPASS16 = ['N', 'NNE', 'NE', 'ENE', 'E', 'ESE', 'SE', 'SSE', 'S', 'SSW', 'SW', 'WSW', 'W', 'WNW', 'NW', 'NNW'];
export const degToCardinal = (deg) => COMPASS16[Math.round((((deg % 360) + 360) % 360) / 22.5) % 16];
export const meterPct = (v) => Math.max(6, Math.min(100, v || 0));