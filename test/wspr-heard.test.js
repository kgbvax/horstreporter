import { describe, it, expect } from 'vitest';
import {
    wsprSnrToMode,
    wsprHeardConfidence,
    isMyWsprSpot,
    matchCallJS,
    isLocatorJS,
    deviationColor,
} from '../static/utils.js';

describe('wsprSnrToMode', () => {
    it('maps SNR to the most demanding viable mode (no power offset)', () => {
        expect(wsprSnrToMode(15)).toBe('ssb');
        expect(wsprSnrToMode(10)).toBe('ssb');   // boundary: SSB threshold
        expect(wsprSnrToMode(0)).toBe('cw');
        expect(wsprSnrToMode(-15)).toBe('cw');   // boundary: CW threshold
        expect(wsprSnrToMode(-18)).toBe('ft8');
        expect(wsprSnrToMode(-21)).toBe('ft8');  // boundary: FT8 threshold
        expect(wsprSnrToMode(-25)).toBe('wspr-only');
        expect(wsprSnrToMode(-31)).toBe('wspr-only');
    });

    it('applies the power offset before thresholding', () => {
        // +13 dB offset (5 W WSPR → 100 W QSO) flips the viable mode.
        expect(wsprSnrToMode(-18, 13)).toBe('cw');   // -5 dB effective
        expect(wsprSnrToMode(-25, 13)).toBe('cw');   // -12 dB effective
        expect(wsprSnrToMode(-30, 13)).toBe('ft8');  // -17 dB effective
        expect(wsprSnrToMode(-35, 13)).toBe('wspr-only'); // -22 dB effective
    });

    it('defaults the offset to 0 and handles non-numeric input', () => {
        expect(wsprSnrToMode(15)).toBe('ssb');
        expect(wsprSnrToMode(NaN)).toBe('wspr-only');
        expect(wsprSnrToMode(undefined)).toBe('wspr-only');
    });
});

describe('wsprHeardConfidence', () => {
    it('maps report count to confidence tiers', () => {
        expect(wsprHeardConfidence(1)).toBe('low');
        expect(wsprHeardConfidence(2)).toBe('low');
        expect(wsprHeardConfidence(3)).toBe('medium');
        expect(wsprHeardConfidence(9)).toBe('medium');
        expect(wsprHeardConfidence(10)).toBe('high');
        expect(wsprHeardConfidence(100)).toBe('high');
    });

    it('treats missing/zero counts as low', () => {
        expect(wsprHeardConfidence(0)).toBe('low');
        expect(wsprHeardConfidence(undefined)).toBe('low');
        expect(wsprHeardConfidence(NaN)).toBe('low');
    });
});

describe('isMyWsprSpot', () => {
    it('matches a callsign QTH against the transmitter callsign (spot.receiver)', () => {
        const spot = { sourceType: 'wspr', receiver: 'W1AW', locator: 'JO62' };
        expect(isMyWsprSpot(spot, 'W1AW')).toBe(true);
        expect(isMyWsprSpot(spot, 'w1aw')).toBe(true); // case-insensitive
        expect(isMyWsprSpot(spot, 'K1ABC')).toBe(false);
    });

    it('matches callsign prefix/suffix modifiers', () => {
        expect(isMyWsprSpot({ sourceType: 'wspr', receiver: 'W1AW/P' }, 'W1AW')).toBe(true);
        expect(isMyWsprSpot({ sourceType: 'wspr', receiver: 'DL/W1AW' }, 'W1AW')).toBe(true);
        expect(isMyWsprSpot({ sourceType: 'wspr', receiver: 'DL/W1AW/P' }, 'W1AW')).toBe(true);
    });

    it('matches a locator QTH against the transmitter locator (spot.locator)', () => {
        expect(isMyWsprSpot({ sourceType: 'wspr', locator: 'JO62qm' }, 'JO62')).toBe(true);
        expect(isMyWsprSpot({ sourceType: 'wspr', locator: 'JO62' }, 'JO62')).toBe(true);
        expect(isMyWsprSpot({ sourceType: 'wspr', locator: 'FN31' }, 'JO62')).toBe(false);
    });

    it('rejects non-WSPR spots and empty QTH', () => {
        expect(isMyWsprSpot({ sourceType: 'ft8', receiver: 'W1AW' }, 'W1AW')).toBe(false);
        expect(isMyWsprSpot({ sourceType: 'wspr', receiver: 'W1AW' }, '')).toBe(false);
        expect(isMyWsprSpot(null, 'W1AW')).toBe(false);
    });
});

describe('matchCallJS', () => {
    it('mirrors Go spot.go:matchCall', () => {
        expect(matchCallJS('W1AW', 'W1AW')).toBe(true);
        expect(matchCallJS('W1AW/P', 'W1AW')).toBe(true);
        expect(matchCallJS('DL/W1AW', 'W1AW')).toBe(true);
        expect(matchCallJS('DL/W1AW/P', 'W1AW')).toBe(true);
        expect(matchCallJS('K1ABC', 'W1AW')).toBe(false);
        expect(matchCallJS('', 'W1AW')).toBe(false);
    });
});

describe('isLocatorJS', () => {
    it('recognizes 4- and 6-char Maidenhead locators', () => {
        expect(isLocatorJS('JO62')).toBe(true);
        expect(isLocatorJS('JO62qm')).toBe(true);
        expect(isLocatorJS('W1AW')).toBe(false);
        expect(isLocatorJS('')).toBe(false);
    });
});

describe('deviationColor', () => {
    it('returns gray at ratio 1 (normal)', () => {
        expect(deviationColor(1)).toBe('#6c757d');
    });

    it('returns green above average and red below average', () => {
        expect(deviationColor(4)).toBe('#28a745');    // clamped to full green
        expect(deviationColor(0.25)).toBe('#dc3545'); // clamped to full red
    });

    it('is symmetric on a log2 scale (2× and 0.5× are equidistant from gray)', () => {
        expect(deviationColor(2)).toBe('#4a8e61');
        expect(deviationColor(0.5)).toBe('#a45561');
    });

    it('returns gray for non-positive or non-finite ratios', () => {
        expect(deviationColor(0)).toBe('#6c757d');
        expect(deviationColor(-1)).toBe('#6c757d');
        expect(deviationColor(NaN)).toBe('#6c757d');
        expect(deviationColor(undefined)).toBe('#6c757d');
    });
});
