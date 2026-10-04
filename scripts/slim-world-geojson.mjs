#!/usr/bin/env node
// Builds static/world-slim.geojson from the vendored Natural Earth file.
//
// static/vendor/world.geojson (ne_50m_admin_0_countries, 2.9 MB / ~1 MB gzip)
// carries ~170 properties per country (translations, per-country ADM0 variants,
// ...) and 6-decimal coordinates (0.1 m). The map only reads the handful of
// properties in KEEP and draws at country scale, so the slim copy keeps those
// and rounds coordinates to 4 decimals (~11 m). static/vendor/ stays untouched
// (CLAUDE.md); the app fetches the slim copy.
//
// Usage: node scripts/slim-world-geojson.mjs [in] [out]
import fs from 'node:fs';

const IN = process.argv[2] || 'static/vendor/world.geojson';
const OUT = process.argv[3] || 'static/world-slim.geojson';
const DECIMALS = Number(process.env.DECIMALS || 3);
// Every property name the frontend reads (static/*.js, src/): country key and
// colour (utils.js, azimuth-runtime.js), DXCC prefix lookup, label placement
// and ranking. Re-run this script's check (`--check`) after adding a reader.
const KEEP = ['ADMIN', 'ADM0_A3', 'NAME', 'NAME_LONG', 'SOV_A3', 'BRK_A3', 'ISO_A2', 'ISO_A2_EH', 'ISO_A3', 'ISO_A3_EH',
  'POSTAL', 'MAPCOLOR9', 'MAPCOLOR13', 'LABELRANK', 'POP_EST', 'LABEL_X', 'LABEL_Y', 'CONTINENT', 'TINY', 'LEVEL', 'TYPE'];

const f = 10 ** DECIMALS;
const round = (v) => Math.round(v * f) / f;

function slimRing(ring, minPoints) {
  const out = [];
  for (const [x, y] of ring) {
    const p = [round(x), round(y)];
    const last = out[out.length - 1];
    if (!last || last[0] !== p[0] || last[1] !== p[1]) out.push(p);
  }
  // Rounding can collapse a tiny ring; keep the original rather than emit an invalid one.
  return out.length >= minPoints ? out : ring.map(([x, y]) => [round(x), round(y)]);
}

function slimGeometry(g) {
  if (g.type === 'Polygon') return { type: g.type, coordinates: g.coordinates.map((r) => slimRing(r, 4)) };
  if (g.type === 'MultiPolygon') return { type: g.type, coordinates: g.coordinates.map((poly) => poly.map((r) => slimRing(r, 4))) };
  return g;
}

const src = JSON.parse(fs.readFileSync(IN, 'utf8'));
const slim = {
  type: 'FeatureCollection',
  features: src.features.map((ft) => ({
    type: 'Feature',
    properties: Object.fromEntries(KEEP.filter((k) => k in ft.properties).map((k) => [k, ft.properties[k]])),
    geometry: slimGeometry(ft.geometry),
  })),
};
const text = JSON.stringify(slim);
fs.writeFileSync(OUT, text);
console.log(`${IN} -> ${OUT}: ${(fs.statSync(IN).size / 1024).toFixed(0)} KB -> ${(text.length / 1024).toFixed(0)} KB (${slim.features.length} features)`);
