// glance.js — render the 10×11 band × region matrix. The shape is
// hand-rolled rather than templated so the SSR-friendly behaviour is
// trivial: each cell is one DOM node, no nested tables, no React.

import { scoreToColor } from './color.js';

// FALLBACK_MODES is used when the very first SSE event arrives with no
// cells yet (race between health load and stream open) — the matrix
// would otherwise render without any mode ordering. Once a real payload
// lands, modesFromPayload() takes over and we never consult this list
// again.
const FALLBACK_MODES = ['FT8', 'FT4', 'CW', 'RTTY', 'SSB'];

// modesFromPayload derives the per-mode display order from the first
// cell's mode_breakdown so adding a new mode on the server side
// propagates to the UI without touching this file. Falls back to the
// hard-coded list only when the payload lacks a mode breakdown entirely.
function modesFromPayload(payload) {
  for (const c of payload.cells || []) {
    if (c.mode_breakdown && c.mode_breakdown.length > 0) {
      return c.mode_breakdown.map(m => m.mode);
    }
  }
  return FALLBACK_MODES;
}

function scoreBar(mode) {
  // Normalise the z-score to 0..1 for the bar width. Clamp to [-3, +6] to
  // match the color mapping; 0 is the midpoint.
  const s = Math.max(-3, Math.min(6, mode.score));
  const ratio = (s + 3) / 9;
  const cls = mode.workable ? 'bar workable' : 'bar unworkable';
  return `
    <div class="${cls} ${mode.mode.toLowerCase()}" title="${mode.mode} z=${mode.score.toFixed(2)} conf=${mode.confidence.toFixed(2)}">
      <span class="mode">${mode.mode}</span>
      <span class="bar-fill"><span style="width: ${Math.round(ratio * 100)}%"></span></span>
    </div>
  `;
}

// isLight returns true when the hex colour's Rec. 709 luminance is
// above the midpoint of [0, 255]. The threshold is intentionally
// close to the middle of the range so the same cell always picks the
// same text colour regardless of which palette (viridis / rocket /
// future) is in use.
function isLight(hex) {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  const L = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  return L > 140;
}

function cellHTML(cell, opts) {
  const bg = scoreToColor(cell.score);
  const textClass = isLight(bg) ? 'cell region on-light' : 'cell region on-dark';
  const isHome = cell.region === opts.homeRegion;
  const cls = isHome ? `${textClass} is-home` : textClass;
  const prob = (cell.probability * 100).toFixed(0);
  const conf = (cell.confidence * 100).toFixed(0);
  const top = modesDisplay.map(m => {
    const mode = cell.mode_breakdown.find(x => x.mode === m);
    return mode ? scoreBar(mode) : '';
  }).join('');
  return `
    <div class="${cls}" style="background:${bg}"
         title="${cell.band} → ${cell.region}: score ${cell.score.toFixed(2)}, P(contact)=${prob}%, conf=${conf}%, samples=${cell.samples}">
      <div class="top">
        <span class="region">${cell.region}</span>
        <span class="score">${cell.score.toFixed(1)}</span>
      </div>
      <div class="bars">${top}</div>
      <div class="badges">
        <span class="prob" title="headline probability of contact in the next 5 minutes">P ${prob}%</span>
        <span class="conf" title="confidence in the headline probability">c ${conf}</span>
      </div>
    </div>
  `;
}

export function renderMatrix(root, payload, opts) {
  const { bands, regions, cells } = payload;
  // Derive the per-mode display order from the first cell's breakdown so
  // adding a new mode server-side shows up automatically. We can't do
  // this once per cell because modes would then sort cell-by-cell.
  const modesDisplay = modesFromPayload(payload);
  const byKey = new Map();
  // Normalise the lookup key: the API today returns uppercase bands and
  // uppercase region strings, but trim/lowercase before composing so a
  // future caller (a debug tool, a stale service) can't silently blank
  // out the matrix by sending mixed casing.
  const normalise = (s) => String(s || '').trim().toUpperCase();
  for (const c of cells) byKey.set(`${normalise(c.band)}|${normalise(c.region)}`, c);

  const fragments = [];
  fragments.push('<div class="axis"></div>');
  for (const r of regions) {
    fragments.push(`<div class="axis col" title="${r}">${r}</div>`);
  }
  for (const band of bands) {
    fragments.push(`<div class="axis row">${band}</div>`);
    for (const reg of regions) {
      const cell = byKey.get(`${band}|${reg}`);
      if (!cell) {
        fragments.push('<div class="cell"></div>');
        continue;
      }
      fragments.push(cellHTML(cell, opts));
    }
  }
  root.innerHTML = fragments.join('');
}
