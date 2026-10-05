import { describe, expect, it } from 'vitest';
import {
    LOOKS, LF_MAX, STRIP_LANES, volumeStep, cellModel, payloadHas, stripX, stripAxisHtml, stripDotSize,
    stripRowHtml, placeStrip, layoutStripLabels, dayStripSvg, trendNormals, sparklineSvg, lookLegendHtml,
    CET_R2, heatRgb, heatT, heatCss, heatLegendHtml,
} from '../static/wspr-matrix-looks.js';

const NOW = Date.UTC(2026, 9, 4, 18, 0) / 1000; // 18:00 UTC: slot 36

const cell = (o = {}) => ({ band: '20m', region: 'EU', spot_count: 20, ssb_open: true, cw_open: true, ...o });

describe('wspr-matrix-looks model', () => {
    it('lists three looks with plain titles', () => {
        expect(LOOKS.map((l) => [l.key, l.label])).toEqual([['dots', 'Dots'], ['day', 'Day'], ['trend', 'Trend']]);
        for (const l of LOOKS) expect(l.title).not.toMatch(/experiment/i);
    });

    it('volumeStep buckets five decades', () => {
        expect([0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 1e6].map(volumeStep)).toEqual([0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5]);
        expect(volumeStep(undefined)).toBe(0);
    });

    it('cellModel: smoothed log2 factor, clamped to three doublings', () => {
        expect(cellModel(cell({ expected: 9, expected_spots: 9 })).lf).toBe(0);
        expect(cellModel(cell({ expected: 9, expected_spots: 19 })).lf).toBe(1);
        expect(cellModel(cell({ expected: 9, expected_spots: 4 })).lf).toBe(-1);
        expect(cellModel(cell({ expected: 2000, expected_spots: 3 })).lf).toBe(-LF_MAX);
        expect(cellModel(cell({ expected: 0.2, expected_spots: 40 })).lf).toBe(LF_MAX);
        // Normally under one report, one now: barely above normal, not ×8.
        expect(cellModel(cell({ expected: 0.3, expected_spots: 1 })).lf).toBeCloseTo(Math.log2(2 / 1.3));
        const none = cellModel(cell());
        expect(none.lf).toBeNull();
        expect(none.hasNormal).toBe(false);
        expect(none.silent).toBe(false);
    });

    it('cellModel: silent, CW only and surge strength', () => {
        expect(cellModel({ spot_count: 0, silent: true, expected: 12, expected_spots: 0 }).silent).toBe(true);
        expect(cellModel(cell({ ssb_open: false, cw_open: true })).cwOnly).toBe(true);
        expect(cellModel(cell({ ssb_open: true, cw_open: true })).cwOnly).toBe(false);
        expect(cellModel(cell({ atypical: { z_score: 2.5 } })).surge).toBe(1);
        expect(cellModel(cell({ atypical: { z_score: 4.2 } })).surge).toBe(2);
        expect(cellModel(cell({ atypical: { z_score: 2.5 }, atypical_agreement: 0.5 })).surge).toBe(2);
        expect(cellModel(cell({ spot_count: 1234 })).step).toBe(4);
    });

    it('payloadHas reports what the looks can draw', () => {
        expect(payloadHas(null)).toEqual({ normal: false, day: false, trend: false });
        expect(payloadHas({ cells: [cell({ expected: 1, normal_day: new Array(48).fill(0), trend: [1] })] }))
            .toEqual({ normal: true, day: true, trend: true });
        expect(payloadHas({ cells: [cell({ normal_day: [1, 2] })] }).day).toBe(false);
    });
});

describe('wspr-matrix-looks dot strip', () => {
    it('maps the factor axis onto 3..97 percent', () => {
        expect(stripX(-LF_MAX)).toBe(3);
        expect(stripX(0)).toBe(50);
        expect(stripX(LF_MAX)).toBe(97);
        expect(stripX(10)).toBe(97);
        expect(stripAxisHtml()).toContain('<span class="wspr-strip-tick is-centre" style="left:50.0%">×1</span>');
        expect(stripDotSize(0)).toBe(stripDotSize(1));
        expect(stripDotSize(5)).toBeGreaterThan(stripDotSize(1));
    });

    it('stripRowHtml sorts dots by position and escapes labels', () => {
        const html = stripRowHtml([
            { region: 'SA', cell: cell({ expected: 9, expected_spots: 39 }), attrs: 'data-region="SA"' },
            { region: 'JA', cell: { spot_count: 0, silent: true, expected: 30, expected_spots: 0 }, attrs: 'data-region="JA"' },
            { region: 'NA', cell: cell({ expected: 9, expected_spots: 4, ssb_open: false }), attrs: 'data-region="NA"' },
            { region: '<X>', cell: cell(), attrs: 'data-region="X"' },
        ]);
        const order = [...html.matchAll(/data-region="(\w+)"/g)].map((m) => m[1]);
        expect(order).toEqual(['JA', 'NA', 'X', 'SA']);
        expect(html).toContain('wspr-matrix-cell wspr-strip-dot is-silent');
        expect(html).toContain('wspr-matrix-cell wspr-strip-dot is-below is-cw');
        expect(html).toContain('wspr-matrix-cell wspr-strip-dot is-unknown');
        expect(html).toContain('&lt;X&gt;');
        expect(html).toContain('data-col="500"');
        expect((html.match(/wspr-strip-line/g) || []).length).toBe(7);
    });

    it('placeStrip: overlapping dots step into lanes, labels avoid each other', () => {
        const items = [
            { cx: 100, r: 5, w: 14, h: 10 },
            { cx: 104, r: 5, w: 14, h: 10 }, // overlaps the first: lane above
            { cx: 108, r: 5, w: 14, h: 10 }, // overlaps both: lane below
            { cx: 300, r: 4, w: 14, h: 10 },
        ];
        const placed = placeStrip(items, 48);
        expect(placed.map((p) => p.dy)).toEqual([STRIP_LANES[0], STRIP_LANES[1], STRIP_LANES[2], 0]);
        // The lone dot keeps its label right above it.
        expect(placed[3]).toMatchObject({ slot: 0, top: -11 });
        // No two shown labels overlap.
        const rects = placed.map((p, i) => {
            const cy = 24 + p.dy;
            const t = cy - items[i].r + p.top;
            return p.slot < 0 ? null : { l: items[i].cx - 7, r: items[i].cx + 7, t, b: t + 10 };
        }).filter(Boolean);
        for (let a = 0; a < rects.length; a++) {
            for (let b = a + 1; b < rects.length; b++) {
                const A = rects[a];
                const B = rects[b];
                expect(A.l < B.r && B.l < A.r && A.t < B.b && B.t < A.b).toBe(false);
            }
        }
    });

    it('placeStrip hides a label with no free slot', () => {
        const items = Array.from({ length: 8 }, (_, i) => ({ cx: 100 + i, r: 3, w: 20, h: 10 }));
        const placed = placeStrip(items, 48);
        expect(placed.some((p) => p.slot === -1)).toBe(true);
    });

    it('layoutStripLabels applies the placement to the live DOM', () => {
        document.body.innerHTML = `<div id="root"><div class="wspr-strip">
            <span class="wspr-strip-dot"><span class="wspr-strip-label">EU</span></span>
            <span class="wspr-strip-dot"><span class="wspr-strip-label">AF</span></span></div></div>`;
        const strip = document.querySelector('.wspr-strip');
        strip.getBoundingClientRect = () => ({ left: 0, top: 0, width: 300, height: 48 });
        const dots = document.querySelectorAll('.wspr-strip-dot');
        dots[0].getBoundingClientRect = () => ({ left: 95, width: 10 });
        dots[1].getBoundingClientRect = () => ({ left: 99, width: 10 });
        for (const l of document.querySelectorAll('.wspr-strip-label')) {
            Object.defineProperty(l, 'offsetWidth', { value: 14 });
            Object.defineProperty(l, 'offsetHeight', { value: 10 });
        }
        layoutStripLabels(document.getElementById('root'));
        expect(dots[0].style.getPropertyValue('--dy')).toBe('0px');
        expect(dots[1].style.getPropertyValue('--dy')).toBe(`${STRIP_LANES[1]}px`);
        const labels = document.querySelectorAll('.wspr-strip-label');
        // AF stepped into the lane above, so EU's label goes below its dot.
        expect(labels[0].style.top).toBe('11px');
        expect(labels[0].style.bottom).toBe('auto');

        // Not laid out yet (zero width): left alone.
        strip.getBoundingClientRect = () => ({ left: 0, top: 0, width: 0, height: 0 });
        labels[0].style.top = '';
        layoutStripLabels(document.getElementById('root'));
        expect(labels[0].style.top).toBe('');
    });
});

describe('wspr-matrix-looks day strip', () => {
    const curve = Array.from({ length: 48 }, (_, i) => (i >= 30 && i <= 40 ? 40 : 4));

    it('draws the usual day, the cursor at now and the dot', () => {
        const svg = dayStripSvg(cell({ expected: 20, expected_spots: 30, normal_day: curve }), NOW, 15, '20m to EU');
        expect(svg).toContain('aria-label="20m to EU"');
        expect(svg).toContain('fill-opacity="0.16"'); // silhouette
        // Cursor at 18:00 UTC = 3/4 of the day: x = 1 + 38 × 0.75.
        expect(svg).toContain('x1="29.5" x2="29.5"');
        expect(svg).toMatch(/<circle cx="29.5" cy="[\d.]+" r="2.6" fill="currentColor"\/>/);
        expect(svg.startsWith('<span class="wspr-look-box"><svg')).toBe(true);
        expect(svg.endsWith('</svg></span>')).toBe(true);
    });

    it('silent: a dashed ring on the baseline; no normal: no dot; no curve: no silhouette', () => {
        const silent = dayStripSvg({ spot_count: 0, silent: true, expected: 12, expected_spots: 0, normal_day: curve }, NOW, 15, 'x');
        expect(silent).toContain('stroke-dasharray="1.4 1.2"');
        expect(silent).not.toContain('fill="currentColor"/>');
        const noNormal = dayStripSvg(cell({ normal_day: curve }), NOW, 15, 'x');
        expect(noNormal).not.toContain('<circle');
        const noCurve = dayStripSvg(cell({ expected: 3, expected_spots: 5, cw_open: true, ssb_open: false }), NOW, 15, 'x');
        expect(noCurve).not.toContain('fill-opacity="0.16"');
        expect(noCurve).toContain('fill="none" stroke="currentColor" stroke-width="1.3"'); // hollow: CW only
    });
});

describe('wspr-matrix-looks sparkline', () => {
    it('trendNormals follows the day curve per bin, else the window normal', () => {
        const curve = Array.from({ length: 48 }, (_, i) => i); // slot index as the normal
        // Bins end at 18:00; the first starts at 17:00 (slot 34), the last two halves in slot 35.
        const byDay = trendNormals({ normal_day: curve }, NOW, 5, 12, 15);
        expect(byDay).toHaveLength(12);
        expect(byDay[0]).toBeCloseTo(34 * 5 / 30);
        expect(byDay[11]).toBeCloseTo(35 * 5 / 30);
        expect(trendNormals({ expected: 30 }, NOW, 5, 12, 15)).toEqual(new Array(12).fill(10));
        expect(trendNormals({}, NOW, 5, 12, 15)).toBeNull();
    });

    it('draws the series, the dashed normal and the end dot', () => {
        const trend = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12];
        const svg = sparklineSvg(cell({ expected: 15, expected_spots: 33, trend, atypical: { z_score: 5 } }), NOW, 15, 5, 'x');
        expect(svg).toContain('stroke-dasharray="2 1.6"'); // normal
        expect((svg.match(/<path/g) || []).length).toBe(2);
        expect((svg.match(/<circle/g) || []).length).toBe(2); // end dot + surge ring
        const cw = sparklineSvg(cell({ ssb_open: false, trend }), NOW, 15, 5, 'x');
        expect(cw).toContain('stroke-dasharray="2.2 1.6"');
        expect(cw).not.toContain('stroke-dasharray="2 1.6"'); // no normal without one
        const empty = sparklineSvg(cell({ expected: 4 }), NOW, 15, 0, 'x');
        expect(empty).toContain('stroke-dasharray="2 1.6"');
        expect(empty).not.toContain('<circle');
    });
});

describe('wspr-matrix-looks legend', () => {
    const all = { normal: true, day: true, trend: true };

    it('explains each look', () => {
        expect(lookLegendHtml('dots', all, true)).toContain('position = PSKReporter reports now against the normal');
        expect(lookLegendHtml('day', all, true)).toContain('this path\'s usual day');
        expect(lookLegendHtml('trend', all, true)).toContain('reports per 5 minutes');
        expect(lookLegendHtml('dots', all, true)).not.toContain('wspr-look-note');
    });

    it('says what is missing', () => {
        expect(lookLegendHtml('dots', all, false)).toContain('Select PSKR to compare with the normal.');
        expect(lookLegendHtml('dots', { ...all, normal: false }, true)).toContain('The normal for your area is loading.');
        expect(lookLegendHtml('day', { ...all, day: false }, true)).toContain('The usual day for your area is loading.');
        expect(lookLegendHtml('trend', { ...all, day: false }, true)).not.toContain('wspr-look-note');
    });
});

describe('wspr-matrix-looks heat overlay', () => {
    it('uses CET-R2 from blue to red', () => {
        expect(CET_R2).toHaveLength(17);
        expect(heatRgb(0)).toEqual([0, 52, 245]);   // #0034f5
        expect(heatRgb(1)).toEqual([253, 48, 0]);   // #fd3000
        expect(heatRgb(0.5)).toEqual([181, 193, 32]); // #b5c120, the middle sample
        expect(heatRgb(-1)).toEqual(heatRgb(0));
        expect(heatRgb(7)).toEqual(heatRgb(1));
        // Halfway between two samples is their mean.
        expect(heatRgb(0.5 / 16)).toEqual([0, 71, 223]);
    });

    it('heatT: log of reports now against the busiest cell; none without reports', () => {
        expect(heatT(1, 10000)).toBe(0);
        expect(heatT(100, 10000)).toBe(0.5);
        expect(heatT(10000, 10000)).toBe(1);
        expect(heatT(0, 10000)).toBe(-1);
        expect(heatT(5, 0)).toBe(-1);
        expect(heatT(1, 1)).toBe(1);
        expect(heatCss(0, 100)).toBe('');
        expect(heatCss(100, 100)).toBe('rgb(253, 48, 0)');
    });

    it('colours strip dots with reports, never silent ones', () => {
        const html = stripRowHtml([
            { region: 'EU', cell: cell({ spot_count: 100, expected: 9, expected_spots: 9 }), attrs: 'data-region="EU"' },
            { region: 'JA', cell: { spot_count: 0, silent: true, expected: 30, expected_spots: 0 }, attrs: 'data-region="JA"' },
        ], 100);
        expect(html).toContain('wspr-strip-dot has-heat" style="left:50.0%;--d:10.8px;--heat:rgb(253, 48, 0)"');
        expect((html.match(/has-heat/g) || []).length).toBe(1);
        expect(stripRowHtml([{ region: 'EU', cell: cell(), attrs: '' }])).not.toContain('has-heat');
    });

    it('adds the scale to the legend only when the overlay is on', () => {
        expect(heatLegendHtml(0)).toBe('');
        expect(heatLegendHtml(12345)).toContain('1 to 12k (log scale)');
        expect(heatLegendHtml(2500)).toContain('1 to 2.5k');
        expect(heatLegendHtml(40)).toContain('1 to 40 ');
        const all = { normal: true, day: true, trend: true };
        expect(lookLegendHtml('day', all, true, 900)).toContain('wspr-heat-bar');
        expect(lookLegendHtml('day', all, true)).not.toContain('wspr-heat-bar');
    });
});
