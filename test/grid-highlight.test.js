import { afterEach, describe, expect, it, vi } from 'vitest';
import {
    getGridHighlightModel,
    gridSnrOpacity,
    gridSnrOpacityClassic,
    topQuartileMean
} from '../static/utils.js';

// jsdom in this repo provides no localStorage; stub a minimal one for the
// pref-fallback tests.
function stubLocalStorage() {
    const store = new Map();
    vi.stubGlobal('localStorage', {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => { store.set(k, String(v)); },
        removeItem: (k) => { store.delete(k); },
        clear: () => store.clear()
    });
}

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('topQuartileMean', () => {
    it('uses the single best spot for tiny squares (k = ceil(n/4))', () => {
        expect(topQuartileMean([12])).toBe(12);
        expect(topQuartileMean([10, 4])).toBe(10);
    });

    it('takes the mean of the strongest quarter once there are 8+ spots', () => {
        // best 1 of 4
        expect(topQuartileMean([10, 0, 0, 0])).toBe(10);
        // best 2 of 8: 12, 8 -> 10
        expect(topQuartileMean([12, 8, -5, -5, -5, -5, -5, -5])).toBe(10);
    });

    it('keeps a single strong outlier from dominating a weak square', () => {
        const snrs = [12, ...Array(20).fill(-8)];
        // best 6: 12, -8 x5 -> ~-4.67, still below 0 dB
        const score = topQuartileMean(snrs);
        expect(score).toBeCloseTo(-28 / 6, 5);
        expect(score).toBeLessThan(0);
    });

    it('ignores non-finite values and returns NaN for empty input', () => {
        expect(topQuartileMean([])).toBeNaN();
        expect(topQuartileMean(undefined)).toBeNaN();
        expect(topQuartileMean([NaN, Infinity, 5])).toBe(5);
    });
});

describe('gridSnrOpacity', () => {
    it('hits the calibrated anchors', () => {
        expect(gridSnrOpacity(0)).toBeCloseTo(0.45, 5); // old "medium" tier
        expect(gridSnrOpacity(-10)).toBeCloseTo(0.15, 5);
        expect(gridSnrOpacity(10)).toBeCloseTo(0.60, 5);
        expect(gridSnrOpacity(20)).toBeCloseTo(0.75, 5);
    });

    it('clamps at the floor and cap', () => {
        expect(gridSnrOpacity(-30)).toBeCloseTo(0.10, 5);
        expect(gridSnrOpacity(50)).toBeCloseTo(0.75, 5);
    });

    it('is continuous across the 0 dB anchor', () => {
        expect(gridSnrOpacity(-0.1)).toBeCloseTo(0.447, 3);
        expect(gridSnrOpacity(0.1)).toBeGreaterThan(0.45);
        expect(gridSnrOpacity(0.1)).toBeLessThan(0.46);
    });

    it('maps non-finite scores to the floor', () => {
        expect(gridSnrOpacity(NaN)).toBeCloseTo(0.10, 5);
    });
});

// REMOVE-WITH-CLASSIC-MODEL
describe('gridSnrOpacityClassic', () => {
    it('keeps the legacy 3-tier contract', () => {
        expect(gridSnrOpacityClassic(-1)).toBeCloseTo(0.22, 5);
        expect(gridSnrOpacityClassic(0)).toBeCloseTo(0.45, 5);
        expect(gridSnrOpacityClassic(9)).toBeCloseTo(0.45, 5);
        expect(gridSnrOpacityClassic(10)).toBeCloseTo(0.72, 5);
    });
});

describe('getGridHighlightModel', () => {
    it('defaults to classic with no radio and no saved pref', () => {
        stubLocalStorage();
        document.body.innerHTML = '';
        expect(getGridHighlightModel()).toBe('classic');
    });

    it('prefers the checked radio over localStorage', () => {
        stubLocalStorage();
        localStorage.setItem('gridHighlightModel', 'classic');
        document.body.innerHTML = '<input type="radio" name="grid-highlight-model" value="reachability" checked />';
        expect(getGridHighlightModel()).toBe('reachability');
    });

    it('falls back to the localStorage pref when no radio exists', () => {
        stubLocalStorage();
        document.body.innerHTML = '';
        localStorage.setItem('gridHighlightModel', 'reachability');
        expect(getGridHighlightModel()).toBe('reachability');
        localStorage.setItem('gridHighlightModel', 'classic');
        expect(getGridHighlightModel()).toBe('classic');
    });
});
