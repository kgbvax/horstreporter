import { describe, it, expect } from 'vitest';
import { radialLine } from '../static/canvas-draw.js';
import { dashedLine } from '../static/canvas-draw.js';
function recordingCtx() {
    const calls = [];
    return {
        calls,
        beginPath() { calls.push(['beginPath']); },
        moveTo(x, y) { calls.push(['moveTo', x, y]); },
        lineTo(x, y) { calls.push(['lineTo', x, y]); },
        stroke() { calls.push(['stroke']); },
        setLineDash(d) { calls.push(['setLineDash', d.join(',')]); },
        set lineWidth(v) { calls.push(['lineWidth', v]); },
    };
}

describe('radialLine', () => {
    it('strokes from r0 to r1 at bearing 0 (north, up)', () => {
        const ctx = recordingCtx();
        radialLine(ctx, 100, 100, 0, 50, 0, 2);
        expect(ctx.calls).toEqual([
            ['beginPath'],
            ['moveTo', 100, 100],
            ['lineTo', 100, 50],
            ['lineWidth', 2],
            ['stroke'],
        ]);
    });

    it('bearing 90 points east (+x)', () => {
        const ctx = recordingCtx();
        radialLine(ctx, 100, 100, 0, 50, 90);
        const lineTo = ctx.calls.find((c) => c[0] === 'lineTo');
        expect(lineTo[1]).toBeCloseTo(150);
        expect(lineTo[2]).toBeCloseTo(100);
    });
});

describe('dashedLine', () => {
    it('sets dash, strokes the segment, then resets dash', () => {
        const ctx = recordingCtx();
        dashedLine(ctx, 10, 20, 30, 40, [4, 3], 1.5);
        expect(ctx.calls).toEqual([
            ['lineWidth', 1.5],
            ['setLineDash', '4,3'],
            ['beginPath'],
            ['moveTo', 10, 20],
            ['lineTo', 30, 40],
            ['stroke'],
            ['setLineDash', ''],
        ]);
    });
});
