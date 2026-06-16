import { describe, it, expect } from 'vitest';
import { computeActivityChartData, utcSlotOfDayFromMs } from '../static/band-lab.js';

describe('utcSlotOfDayFromMs', () => {
    it('returns 0 at UTC midnight', () => {
        // 1970-01-01 00:00:00 UTC
        expect(utcSlotOfDayFromMs(0)).toBe(0);
    });
    it('returns 1 at 00:30 UTC', () => {
        expect(utcSlotOfDayFromMs(30 * 60 * 1000)).toBe(1);
    });
    it('returns 47 at 23:30 UTC', () => {
        expect(utcSlotOfDayFromMs((23 * 60 + 30) * 60 * 1000)).toBe(47);
    });
    it('returns hour*2 on the hour and hour*2+1 at half past', () => {
        // 14:00 UTC → slot 28
        expect(utcSlotOfDayFromMs(14 * 60 * 60 * 1000)).toBe(28);
        // 14:45 UTC → slot 29
        expect(utcSlotOfDayFromMs((14 * 60 + 45) * 60 * 1000)).toBe(29);
    });
});

describe('computeActivityChartData', () => {
    const nowMidnightMs = 0; // 1970-01-01 00:00 UTC, slot 0

    it('produces zero rates and 0.5 yMax when there are no points and no baseline', () => {
        const data = computeActivityChartData([], {}, 15, nowMidnightMs);
        expect(data.binRates).toHaveLength(12);
        expect(data.binRates.every((r) => r === 0)).toBe(true);
        expect(data.baselineRatesPerBin.every((r) => r === 0)).toBe(true);
        expect(data.yMax).toBe(0.5);
    });

    it('converts bin counts to spots/min using the configured window', () => {
        // 15-minute window → binMinutes = 1.25.
        // Two spots in the newest bin (age ≈ 0). Rate per bin = 2 / 1.25 = 1.6/min.
        const points = [
            { ageSeconds: 1 },
            { ageSeconds: 2 },
        ];
        const data = computeActivityChartData(points, {}, 15, nowMidnightMs);
        expect(data.binMinutes).toBeCloseTo(1.25, 6);
        expect(data.binRates[11]).toBeCloseTo(1.6, 6);
        // No spots in earlier bins.
        expect(data.binRates.slice(0, 11).every((r) => r === 0)).toBe(true);
        // yMax has headroom (binRates max × 1.1).
        expect(data.yMax).toBeCloseTo(1.76, 6);
    });

    it('drops points older than the window', () => {
        // 15m window = 900 s. A point at age 1200 must not be counted.
        const points = [{ ageSeconds: 1200 }];
        const data = computeActivityChartData(points, {}, 15, nowMidnightMs);
        expect(data.binRates.every((r) => r === 0)).toBe(true);
    });

    it('places each bin in its correct slot for a 120m window straddling 4 slots', () => {
        // 120m window starting at midnight UTC → bins cover the previous 2h
        // (22:00..00:00). 3 bins per 30-min slot → slot indices 44,45,46,47
        // for bins [0..2], [3..5], [6..8], [9..11].
        const baselineBySlot = new Array(48).fill(0);
        baselineBySlot[44] = 1.0;
        baselineBySlot[45] = 2.0;
        baselineBySlot[46] = 3.0;
        baselineBySlot[47] = 4.0;
        const used = new Array(48).fill(true);
        const data = computeActivityChartData(
            [],
            { baseline_activity_by_slot: baselineBySlot, baseline_slot_used_by_target: used },
            120,
            nowMidnightMs,
        );

        const expected = [1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 4];
        for (let i = 0; i < 12; i++) {
            expect(data.baselineRatesPerBin[i]).toBeCloseTo(expected[i], 6);
            expect(data.baselineTargetUsedPerBin[i]).toBe(true);
        }
        expect(data.slotChanges).toEqual([3, 6, 9]);
        // yMax driven by the largest baseline (4.0) × 1.1.
        expect(data.yMax).toBeCloseTo(4.4, 6);
    });

    it('marks a per-slot fallback when baseline_slot_used_by_target[slot] is false', () => {
        // nowMs = 00:16 UTC → a 15m window covers 00:01..00:16, entirely in slot 0.
        const nowSlot0Ms = 16 * 60 * 1000;
        const baselineBySlot = new Array(48).fill(0);
        baselineBySlot[0] = 0.5;
        const used = new Array(48).fill(false);
        const data = computeActivityChartData(
            [],
            { baseline_activity_by_slot: baselineBySlot, baseline_slot_used_by_target: used },
            15,
            nowSlot0Ms,
        );
        expect(data.baselineRatesPerBin.every((r) => r === 0.5)).toBe(true);
        expect(data.baselineTargetUsedPerBin.every((u) => u === false)).toBe(true);
    });

    it('falls back to baseline_activity when by_slot array is absent (older backend)', () => {
        const data = computeActivityChartData(
            [],
            { baseline_activity: 1.5, target_baseline_used: true },
            15,
            nowMidnightMs,
        );
        expect(data.baselineRatesPerBin.every((r) => r === 1.5)).toBe(true);
        expect(data.baselineTargetUsedPerBin.every((u) => u === true)).toBe(true);
    });
});
