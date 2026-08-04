// color_test.js — sanity checks for the viridis mapping. Run with
//
//   node --test cmd/pathscope/static/color_test.js
//
// These are not exhaustive; they document the contract:
//   • viridis(0) and viridis(1) match matplotlib's published endpoints
//   • scoreToColor clamps below MIN and above MAX
//   • the mapping is monotone (no score-step flips the colour backwards)
//   • NaN produces a defined colour rather than throwing

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rocketAt, scoreToColor } from './color.js';

// seaborn's "rocket" endpoints at t=0 and t=1, sampled.
const ENDPOINT_LOW = '#03051a';
const ENDPOINT_HIGH = '#faebdd';

test('rocket endpoints match seaborn', () => {
  assert.equal(rocketAt(0), ENDPOINT_LOW);
  assert.equal(rocketAt(1), ENDPOINT_HIGH);
});

test('rocket clamps outside [0, 1]', () => {
  assert.equal(rocketAt(-0.5), ENDPOINT_LOW);
  assert.equal(rocketAt(1.5), ENDPOINT_HIGH);
});

test('scoreToColor clamps at MIN and MAX', () => {
  // Anything ≤ -3 should be the dark endpoint; anything ≥ +6 the bright.
  assert.equal(scoreToColor(-3), scoreToColor(-99));
  assert.equal(scoreToColor(+6), scoreToColor(+99));
  assert.equal(scoreToColor(-3), ENDPOINT_LOW);
  assert.equal(scoreToColor(+6), ENDPOINT_HIGH);
});

// Perceptual luminance per Rec. 709 — the property rocket is designed
// to be monotone in. RGB-integer compare is meaningless here because
// the hue rotates (black → purple → magenta → orange → cream) while
// luminance rises.
function luminance(hex) {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

test('scoreToColor is monotone non-decreasing in score', () => {
  // Rocket is perceptually uniform: luminance rises monotonically from
  // #03051a (L≈2) at t=0 to #faebdd (L≈234) at t=1. Any drop would be
  // a regression in the lookup table.
  const samples = 50;
  let prev = luminance(scoreToColor(-3));
  for (let i = 1; i <= samples; i++) {
    const score = -3 + (9 * i) / samples;
    const next = luminance(scoreToColor(score));
    assert.ok(next >= prev, `luminance dipped at score=${score}: ${prev.toFixed(1)} → ${next.toFixed(1)}`);
    prev = next;
  }
});

test('rocket endpoint luminances bracket the full range', () => {
  // #03051a (dead) should be far darker than #faebdd (open) — sanity
  // check that the orientation is correct ("dead = dark").
  const dark = luminance(ENDPOINT_LOW);
  const bright = luminance(ENDPOINT_HIGH);
  assert.ok(bright - dark > 200, `expected luminance span >200, got ${(bright - dark).toFixed(1)}`);
});

test('scoreToColor handles NaN without throwing', () => {
  // Defined behaviour is "return a colour" — even an ugly one is better
  // than a NaN that bricks the render.
  const out = scoreToColor(Number.NaN);
  assert.match(out, /^#[0-9a-f]{6}$/);
});
