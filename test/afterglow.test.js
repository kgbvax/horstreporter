// afterglow.test.js — decay/alpha math for the Time Travel overlay.

import { describe, it, expect } from 'vitest';
import { alphaFor, TAU_SECONDS, FADE_IN_MS } from '../static/afterglow.js';
import { MOMENT_WINDOW_SECONDS } from '../static/timeline.js';

// alphaFor(spot {t}, T, moving, nowMs) — re-exported for tests via __internals.

describe('alphaFor (moving playhead)', () => {
    it('decays exponentially in data-time (tau = 5 min)', () => {
        const T = 10000;
        // Ages beyond the 60s fade-in ramp: pure exponential.
        const at60 = alphaFor({ t: T - 60 }, T, true, 0);
        const atTau = alphaFor({ t: T - TAU_SECONDS }, T, true, 0);
        expect(at60).toBeGreaterThan(atTau);
        // e^-1 ≈ 0.368 at one tau (300s age is past the ramp, no ramp factor).
        expect(atTau).toBeCloseTo(Math.exp(-1), 1);
    });

    it('hard-cuts at the 15-min window edge', () => {
        const T = 10000;
        expect(alphaFor({ t: T - MOMENT_WINDOW_SECONDS }, T, true, 0)).toBeGreaterThan(0);
        expect(alphaFor({ t: T - MOMENT_WINDOW_SECONDS - 1 }, T, true, 0)).toBe(0);
    });

    it('fades in freshly arrived spots (ramp below 60s data-age)', () => {
        const T = 10000;
        const ramped = alphaFor({ t: T - 30 }, T, true, 0);
        const full = alphaFor({ t: T - 120 }, T, true, 0);
        // 30s-old spot: exp(-30/300) * (30/60) ≈ 0.905 * 0.5 ≈ 0.45 < full exp.
        expect(ramped).toBeLessThan(full);
        expect(ramped).toBeGreaterThan(0.2);
    });
});

describe('alphaFor (stationary playhead)', () => {
    it('is pure age-graded, no exponential decay', () => {
        const T = 10000;
        const fresh = alphaFor({ t: T }, T, false, 0);
        const old = alphaFor({ t: T - MOMENT_WINDOW_SECONDS }, T, false, 0);
        // Stationary: 0.35 + 0.65 * (1 - age/W) — fresh ≈ 1.0, edge ≈ 0.35.
        expect(fresh).toBeCloseTo(1.0, 1);
        expect(old).toBeCloseTo(0.35, 1);
    });

    it('stationary alpha is higher than moving alpha for old spots (no decay when paused)', () => {
        const T = 10000;
        const age = 10 * 60;
        expect(alphaFor({ t: T - age }, T, false, 0))
            .toBeGreaterThan(alphaFor({ t: T - age }, T, true, 0));
    });
});

describe('constants', () => {
    it('tau is 5 minutes', () => {
        expect(TAU_SECONDS).toBe(300);
    });
    it('fade-in is 300ms wall-clock', () => {
        expect(FADE_IN_MS).toBe(300);
    });
});