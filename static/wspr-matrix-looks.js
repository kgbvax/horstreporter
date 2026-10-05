import { escapeHtml } from './ui-helpers.js';

// wspr-matrix-looks.js — the looks of the Propagation matrix
// (docs/ideation/2026-10-05-now-matrix-ten-more-ways.html, proposals 3, 6 and
// 7; they replaced the viridis / inferno heatmaps). Each one shows a cell
// against its normal for this hour instead of its raw count, in one ink
// (currentColor), no hue:
//
//   - dots:  dot strip. One axis per band row from ×⅛ to ×8; every region is a
//            labelled dot placed where it stands against its normal.
//   - day:   day strip. Each cell draws its path's usual day (the area normal
//            per 30-minute slot), a cursor at now and a dot for now.
//   - trend: sparkline. The last hour in five-minute bins against the normal.
//
// The comparison is PSKReporter (+ DX cluster) reports from the operator's
// area against their own normal (backend expected / expected_spots /
// normal_day / trend, prop_intel_fromhere*.go); the dot size and the stroke
// weight carry all selected reports. wspr-matrix.js owns the table, the grid
// keyboard model and the tooltips; this module only draws.

export const LOOKS = [
    { key: 'dots', label: 'Dots', title: 'Dot strip: each region placed by its reports now against the normal for this hour' },
    { key: 'day', label: 'Day', title: 'Day strip: each path\'s usual day with a dot for now' },
    { key: 'trend', label: 'Trend', title: 'Sparkline: the last hour in 5-minute steps against the normal' },
];

// The log2 factor is clamped to three doublings either way.
export const LF_MAX = 3;
const SLOT_SEC = 1800;

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));

// Five volume steps over five decades of reports: 1–9, 10s, 100s, 1,000s,
// 10,000s (0 for none).
export function volumeStep(n) {
    const v = Number(n) || 0;
    if (v <= 0) return 0;
    if (v < 10) return 1;
    if (v < 100) return 2;
    if (v < 1000) return 3;
    if (v < 10000) return 4;
    return 5;
}

// cellModel: what every look needs from a v2 cell. lf is log2 of reports now
// against the normal, smoothed by one report on both sides so a path that is
// normally near empty does not swing to ×8 on one stray report; null without a
// normal (PSKReporter not selected, or the area normal not loaded yet).
export function cellModel(cell) {
    const n = Number(cell?.spot_count) || 0;
    const hasNormal = cell?.expected != null && cell?.expected_spots != null &&
        Number.isFinite(Number(cell.expected)) && Number.isFinite(Number(cell.expected_spots));
    const e = hasNormal ? Number(cell.expected) : null;
    const es = hasNormal ? Number(cell.expected_spots) : null;
    const lf = hasNormal ? clamp(Math.log2((es + 1) / (e + 1)), -LF_MAX, LF_MAX) : null;
    const surge = cell?.atypical ? (((Number(cell.atypical.z_score) || 0) >= 4 || (cell.atypical_agreement ?? 0) >= 0.5) ? 2 : 1) : 0;
    return {
        n,
        e,
        es,
        lf,
        hasNormal,
        silent: Boolean(cell?.silent) || (n === 0 && hasNormal && e >= 1),
        cwOnly: Boolean(cell?.cw_open) && !cell?.ssb_open,
        surge,
        step: volumeStep(n),
    };
}

// Payload-level availability of the data each look needs.
export function payloadHas(data) {
    const cells = Array.isArray(data?.cells) ? data.cells : [];
    return {
        normal: cells.some((c) => c.expected != null),
        day: cells.some((c) => Array.isArray(c.normal_day) && c.normal_day.length === 48),
        trend: cells.some((c) => Array.isArray(c.trend) && c.trend.length > 0),
    };
}

// --- Dot strip ---------------------------------------------------------------------

// Horizontal position (percent of the strip) of a log2 factor.
export function stripX(lf) {
    return 3 + 94 * (clamp(lf, -LF_MAX, LF_MAX) + LF_MAX) / (2 * LF_MAX);
}

const STRIP_TICKS = [[-3, '×⅛'], [-2, '×¼'], [-1, '×½'], [0, '×1'], [1, '×2'], [2, '×4'], [3, '×8']];

// Column header: the factor axis.
export function stripAxisHtml() {
    const ticks = STRIP_TICKS.map(([lf, t]) =>
        `<span class="wspr-strip-tick${lf === 0 ? ' is-centre' : ''}" style="left:${stripX(lf).toFixed(1)}%">${t}</span>`).join('');
    return `<div class="wspr-strip-ticks" aria-hidden="true">${ticks}</div>`;
}

// Dot diameter in px for a volume step.
export function stripDotSize(step) {
    return 6 + 1.6 * Math.max(1, step);
}

// One band row of the strip. dots: [{ region, cell, attrs }] where attrs is the
// gridcell attribute string wspr-matrix.js builds (role, label, title,
// aria-selected, tabindex, data-band/region). Dots come out in x order so the
// arrow keys walk left to right.
export function stripRowHtml(dots) {
    const placed = dots.map((d) => {
        const m = cellModel(d.cell);
        // Silent: far left. No normal: the centre, drawn as unknown.
        const lf = m.silent ? -LF_MAX : (m.lf ?? 0);
        return { ...d, m, x: stripX(lf) };
    }).sort((a, b) => a.x - b.x || a.region.localeCompare(b.region));
    const grid = STRIP_TICKS.map(([lf]) =>
        `<span class="wspr-strip-line${lf === 0 ? ' is-centre' : ''}" style="left:${stripX(lf).toFixed(1)}%"></span>`).join('');
    const marks = placed.map(({ region, m, x, attrs }) => {
        const cls = ['wspr-matrix-cell', 'wspr-strip-dot'];
        if (m.silent) cls.push('is-silent');
        else if (!m.hasNormal) cls.push('is-unknown');
        else if (m.lf <= -0.5) cls.push('is-below');
        if (!m.silent && m.cwOnly) cls.push('is-cw');
        if (m.surge) cls.push(m.surge === 2 ? 'is-surge is-strong' : 'is-surge');
        const size = m.silent ? 7 : stripDotSize(m.step);
        return `<span class="${cls.join(' ')}" style="left:${x.toFixed(1)}%;--d:${size.toFixed(1)}px" data-col="${Math.round(x * 10)}" ${attrs}>` +
            `<span class="wspr-strip-label" aria-hidden="true">${escapeHtml(region)}</span></span>`;
    }).join('');
    return `<div class="wspr-strip">${grid}${marks}</div>`;
}

// Strip placement, from measured geometry (px, relative to the strip; items
// in x order: centre cx, radius r, label width w and height h). Dots that
// would overlap step into a lane above or below the axis (a small beeswarm);
// each label takes the first free slot of: above its dot, below, further
// above, further below, avoiding every other label and dot, or is left out
// (slot -1; the region stays in the dot's name and tooltip). Returns per item
// the lane offset dy and the label's top relative to the dot's top edge.
export const STRIP_LANES = [0, -9, 9];

function overlaps(a, b, pad) {
    return a.l < b.r + pad && b.l < a.r + pad && a.t < b.b + pad && b.t < a.b + pad;
}

export function placeStrip(items, height) {
    const lanes = STRIP_LANES.map(() => []);
    const out = items.map((it) => {
        let li = STRIP_LANES.findIndex((_, k) => lanes[k].every((p) => Math.abs(p.cx - it.cx) >= p.r + it.r + 1));
        if (li < 0) li = 0;
        lanes[li].push(it);
        return { dy: STRIP_LANES[li], slot: -1, top: 0 };
    });
    const dots = items.map((it, i) => {
        const cy = height / 2 + out[i].dy;
        return { l: it.cx - it.r, r: it.cx + it.r, t: cy - it.r, b: cy + it.r };
    });
    const labels = [];
    items.forEach((it, i) => {
        const d = dots[i];
        const slots = [d.t - 1 - it.h, d.b + 1, d.t - 2 - 2 * it.h, d.b + 2 + it.h];
        for (let k = 0; k < slots.length; k++) {
            const rect = { l: it.cx - it.w / 2, r: it.cx + it.w / 2, t: slots[k], b: slots[k] + it.h };
            if (rect.t < -8 || rect.b > height + 8) continue;
            if (labels.some((b) => overlaps(b, rect, 1))) continue;
            if (dots.some((o, j) => j !== i && overlaps(o, rect, 0))) continue;
            labels.push(rect);
            out[i].slot = k;
            out[i].top = rect.t - d.t;
            break;
        }
    });
    return out;
}

// Measure the live strips and apply placeStrip (reads first, then writes).
export function layoutStripLabels(root) {
    for (const strip of root.querySelectorAll('.wspr-strip')) {
        const dotEls = Array.from(strip.querySelectorAll('.wspr-strip-dot'));
        for (const el of dotEls) el.style.setProperty('--dy', '0px');
        const box = strip.getBoundingClientRect();
        if (box.width === 0) continue;
        const items = dotEls.map((el) => {
            const r = el.getBoundingClientRect();
            const label = el.querySelector('.wspr-strip-label');
            return { cx: r.left - box.left + r.width / 2, r: r.width / 2, w: label?.offsetWidth || 0, h: label?.offsetHeight || 0 };
        });
        const placed = placeStrip(items, box.height);
        dotEls.forEach((el, i) => {
            el.style.setProperty('--dy', `${placed[i].dy}px`);
            const label = el.querySelector('.wspr-strip-label');
            if (!label) return;
            label.classList.toggle('is-hidden', placed[i].slot < 0);
            label.style.top = `${placed[i].top.toFixed(1)}px`;
            label.style.bottom = 'auto';
        });
    }
}

// --- Day strip ----------------------------------------------------------------------

const SVG_W = 40;
const SVG_H = 34;

// The SVG sits absolutely in a fixed-height box, so it never widens its
// column: it scales into whatever width the table gives the cell.
function svgOpen(label) {
    return `<span class="wspr-look-box"><svg class="wspr-look-svg" viewBox="0 0 ${SVG_W} ${SVG_H}" role="img" aria-label="${escapeHtml(label)}">`;
}

const SVG_CLOSE = '</svg></span>';

function dotMark(x, y, m, r = 2.6) {
    let s = m.cwOnly
        ? `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}" fill="none" stroke="currentColor" stroke-width="1.3"/>`
        : `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}" fill="currentColor"/>`;
    if (m.surge) {
        s += `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${(r + 2.4).toFixed(1)}" fill="none" stroke="currentColor" stroke-width="${m.surge === 2 ? 1.6 : 1}"/>`;
    }
    return s;
}

// Seconds since 00:00 UTC.
function secondsOfDay(unixSec) {
    return ((Math.floor(unixSec) % 86400) + 86400) % 86400;
}

// Day strip cell: the usual day (normal_day scaled to the request window) as
// a silhouette on a square-root scale, a dashed cursor at now, a dot for the
// reports now. Silent: a dashed ring on the baseline under the cursor.
export function dayStripSvg(cell, nowSec, minutes, label) {
    const m = cellModel(cell);
    const top = 3;
    const base = SVG_H - 3;
    const curve = Array.isArray(cell?.normal_day) && cell.normal_day.length === 48
        ? cell.normal_day.map((v) => Math.max(0, Number(v) || 0) * (minutes / 30))
        : null;
    const now = m.es ?? 0;
    const max = Math.max(1, now, ...(curve || [0]));
    const yOf = (v) => base - (base - top) * Math.sqrt(Math.max(0, v) / max);
    const xOf = (frac) => 1 + (SVG_W - 2) * frac;
    let s = svgOpen(label);
    if (curve) {
        const pts = curve.map((v, i) => `${xOf((i + 0.5) / 48).toFixed(1)} ${yOf(v).toFixed(1)}`);
        s += `<path d="M${xOf(0).toFixed(1)} ${base} L${pts.join(' L')} L${xOf(1).toFixed(1)} ${base}Z" fill="currentColor" fill-opacity="0.16"/>`;
        s += `<path d="M${pts.join(' L')}" fill="none" stroke="currentColor" stroke-opacity="0.5" stroke-width="0.8"/>`;
    }
    s += `<line x1="1" x2="${SVG_W - 1}" y1="${base}" y2="${base}" stroke="currentColor" stroke-opacity="0.3" stroke-width="0.8"/>`;
    const xn = xOf(secondsOfDay(nowSec) / 86400);
    s += `<line x1="${xn.toFixed(1)}" x2="${xn.toFixed(1)}" y1="1" y2="${base + 1}" stroke="currentColor" stroke-opacity="0.55" stroke-width="0.8" stroke-dasharray="1.5 1.5"/>`;
    if (m.silent) {
        s += `<circle cx="${xn.toFixed(1)}" cy="${(base - 2.6).toFixed(1)}" r="2.4" fill="none" stroke="currentColor" stroke-opacity="0.7" stroke-dasharray="1.4 1.2"/>`;
    } else if (m.hasNormal) {
        s += dotMark(xn, yOf(now), m);
    }
    return `${s}${SVG_CLOSE}`;
}

// --- Sparkline ------------------------------------------------------------------------

// Normal per trend bin: the day curve's slot at each bin's middle (scaled to
// the bin width), else the window normal spread evenly; null without either.
export function trendNormals(cell, nowSec, binMinutes, bins, minutes) {
    const binSec = binMinutes * 60;
    if (Array.isArray(cell?.normal_day) && cell.normal_day.length === 48) {
        const out = [];
        for (let i = 0; i < bins; i++) {
            const mid = nowSec - (bins - i - 0.5) * binSec;
            const slot = Math.floor(secondsOfDay(mid) / SLOT_SEC) % 48;
            out.push((Number(cell.normal_day[slot]) || 0) * (binMinutes / 30));
        }
        return out;
    }
    if (cell?.expected != null && Number.isFinite(Number(cell.expected)) && minutes > 0) {
        return new Array(bins).fill(Number(cell.expected) * (binMinutes / minutes));
    }
    return null;
}

// Sparkline cell: reports per bin over the last hour (stroke weight follows
// the volume), the normal as a dashed line, an end dot. Dotted line: CW only.
export function sparklineSvg(cell, nowSec, minutes, binMinutes, label) {
    const m = cellModel(cell);
    const series = Array.isArray(cell?.trend) ? cell.trend.map((v) => Math.max(0, Number(v) || 0)) : null;
    const bins = series?.length || 12;
    const normals = trendNormals(cell, nowSec, binMinutes || 5, bins, minutes);
    const top = 4;
    const base = SVG_H - 4;
    const max = Math.max(1, ...(series || [0]), ...(normals || [0])) * 1.1;
    const yOf = (v) => base - (base - top) * (v / max);
    const xOf = (i) => 2 + (SVG_W - 4) * (bins === 1 ? 1 : i / (bins - 1));
    let s = svgOpen(label);
    s += `<line x1="1" x2="${SVG_W - 1}" y1="${base}" y2="${base}" stroke="currentColor" stroke-opacity="0.18" stroke-width="0.8"/>`;
    if (normals) {
        s += `<path d="M${normals.map((v, i) => `${xOf(i).toFixed(1)} ${yOf(v).toFixed(1)}`).join(' L')}" fill="none" stroke="currentColor" stroke-opacity="0.55" stroke-width="0.9" stroke-dasharray="2 1.6"/>`;
    }
    if (series) {
        const w = (0.9 + 0.3 * Math.max(1, m.step)).toFixed(1);
        s += `<path d="M${series.map((v, i) => `${xOf(i).toFixed(1)} ${yOf(v).toFixed(1)}`).join(' L')}" fill="none" stroke="currentColor" stroke-width="${w}" stroke-linejoin="round" stroke-linecap="round"${m.cwOnly ? ' stroke-dasharray="2.2 1.6"' : ''}/>`;
        s += dotMark(xOf(bins - 1), yOf(series[bins - 1]), { ...m, cwOnly: false }, 1.9);
    }
    return `${s}${SVG_CLOSE}`;
}

// --- Legend -----------------------------------------------------------------------------

function keySvg(w, h, body) {
    return `<svg class="wspr-look-key" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" aria-hidden="true">${body}</svg>`;
}

// Legend line for a look, with a note when its data is missing.
export function lookLegendHtml(style, has, pskrSelected) {
    let note = '';
    if (!pskrSelected) note = 'Select PSKR to compare with the normal.';
    else if (!has.normal) note = 'The normal for your area is loading.';
    else if (style === 'day' && !has.day) note = 'The usual day for your area is loading.';
    let text = '';
    if (style === 'dots') {
        const key = keySvg(70, 14, '<line x1="35" x2="35" y1="0" y2="14" stroke="currentColor" stroke-opacity=".5"/>' +
            '<circle cx="12" cy="7" r="3" fill="currentColor" fill-opacity=".5"/><circle cx="35" cy="7" r="3.5" fill="currentColor"/><circle cx="58" cy="7" r="5" fill="currentColor"/>');
        text = `${key} position = PSKReporter reports now against the normal for this hour (×½ halves, ×2 doubles), size = all reports, ` +
            'hollow = CW only, ring = surge, dashed = usually open, silent now';
    } else if (style === 'day') {
        const key = keySvg(44, 16, '<path d="M1 15 L8 14 L15 9 L22 3 L29 4 L36 10 L43 14 L43 15Z" fill="currentColor" fill-opacity=".16"/>' +
            '<line x1="31" x2="31" y1="0" y2="16" stroke="currentColor" stroke-opacity=".55" stroke-dasharray="1.5 1.5"/><circle cx="31" cy="3" r="2.4" fill="currentColor"/>');
        text = `${key} shaded = this path's usual day, 00 to 24 UTC; dashed = now; dot = PSKReporter reports now, above the curve is busier than usual; ` +
            'hollow = CW only, ring = surge, dashed ring = usually open, silent now';
    } else if (style === 'trend') {
        const key = keySvg(44, 16, '<path d="M2 9 L42 9" stroke="currentColor" stroke-opacity=".55" stroke-dasharray="2 1.6"/>' +
            '<path d="M2 14 L12 13 L22 11 L32 7 L42 3" fill="none" stroke="currentColor" stroke-width="1.5"/><circle cx="42" cy="3" r="1.9" fill="currentColor"/>');
        text = `${key} line = PSKReporter reports per 5 minutes over the last hour; dashed = normal; heavier line = more reports; dotted = CW only, ring = surge`;
    }
    return `<div class="wspr-matrix-legend wspr-look-legend small text-muted">${note ? `<span class="wspr-look-note">${escapeHtml(note)}</span> ` : ''}${text}</div>`;
}
