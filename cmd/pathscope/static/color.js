// color.js — score-to-color mapping for the matrix cells. The score is a
// clamped z-score in [-3, +6]; eight discrete buckets match the legend
// gradient. Negative buckets are dark/blue (no activity), positive buckets
// go through teal/green into amber/red (surges).

const BUCKETS = [
  '#1d2a3a', // -3..-2
  '#26415e', // -2..-1
  '#345b7a', // -1.. 0
  '#4f7d9a', //  0..+1
  '#7ba66b', // +1..+2
  '#c3b14a', // +2..+3
  '#d77a3a', // +3..+4
  '#b03a2e', // +4..+6
];

const MIN = -3;
const MAX = 6;
const STEP = (MAX - MIN) / BUCKETS.length;

export function scoreToColor(score) {
  if (Number.isNaN(score)) return BUCKETS[0];
  if (score <= MIN) return BUCKETS[0];
  if (score >= MAX) return BUCKETS[BUCKETS.length - 1];
  const idx = Math.floor((score - MIN) / STEP);
  return BUCKETS[Math.max(0, Math.min(BUCKETS.length - 1, idx))];
}

export const LEGEND_COLORS = BUCKETS;
