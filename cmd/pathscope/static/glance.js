// glance.js — render the 10×11 band × region matrix. The shape is
// hand-rolled rather than templated so the SSR-friendly behaviour is
// trivial: each cell is one DOM node, no nested tables, no React.

import { scoreToColor } from './color.js';

const MODES_DISPLAY = ['FT8', 'FT4', 'CW', 'RTTY', 'SSB'];

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

function cellHTML(cell, opts) {
  const bg = scoreToColor(cell.score);
  const isHome = cell.region === opts.homeRegion;
  const cls = isHome ? 'cell region is-home' : 'cell region';
  const prob = (cell.probability * 100).toFixed(0);
  const conf = (cell.confidence * 100).toFixed(0);
  const top = MODES_DISPLAY.map(m => {
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
  const byKey = new Map();
  for (const c of cells) byKey.set(`${c.band}|${c.region}`, c);

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
