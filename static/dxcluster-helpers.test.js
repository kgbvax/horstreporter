// dxcluster-helpers.test.js — unit tests for the Chase Queue's pure helpers:
// comment trimming, HTML escaping, grade→decision, formatting, band-plan /
// mode classification, and POTA ref parsing. These live in
// dxcluster-helpers.js precisely because dxcluster.js itself auto-runs
// init() at import time (plus DOM/localStorage at module top level), so the
// auto-init path stays untested here (it runs under the browser).

import { describe, it, expect } from 'vitest';

import {
    bandColor,
    gradeToDecision,
    trimComment,
    escapeHtml,
    parseMode,
    modeCatFromFreq,
    modeCat,
    guessMode,
    parsePotaRef,
    enrichSpot,
    fmtFreq,
    fmtAge,
    degToCardinal,
    meterPct,
} from './dxcluster-helpers.js';

describe('escapeHtml', () => {
    it('escapes all five HTML metacharacters', () => {
        expect(escapeHtml('<a href="x">&\'')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;&#39;');
        expect(escapeHtml('<script>alert(1)</script>')).toBe('&lt;script&gt;alert(1)&lt;/script&gt;');
    });

    it('passes safe text through unchanged', () => {
        expect(escapeHtml('UP 5-10 listening EU')).toBe('UP 5-10 listening EU');
        expect(escapeHtml('')).toBe('');
    });

    it('stringifies non-strings and tolerates null/undefined', () => {
        expect(escapeHtml(null)).toBe('');
        expect(escapeHtml(undefined)).toBe('');
        expect(escapeHtml(14205)).toBe('14205');
    });

    it('does not double-escape already-escaped entities beyond the & pass', () => {
        // & is escaped first, so a literal "&lt;" becomes "&amp;lt;"
        expect(escapeHtml('&lt;')).toBe('&amp;lt;');
    });
});

describe('trimComment', () => {
    it('strips control chars, the trailing spot time, and padding', () => {
        // DXSpider comments end with the spot time + BEL control chars
        expect(trimComment('UP 5-10  1015Z')).toBe('UP 5-10');
        expect(trimComment('599 915Z')).toBe('599'); // 3-digit time also stripped
        expect(trimComment('CQ DX  UP10 1015Z')).toBe('CQ DX UP10');
    });

    it('keeps comments that end in non-time text', () => {
        expect(trimComment('no trailing time here')).toBe('no trailing time here');
        expect(trimComment('de LA7GIA 1234Z')).toBe('de LA7GIA'); // 4-digit time still stripped
    });

    it('collapses runs of whitespace', () => {
        expect(trimComment('  hello   world  ')).toBe('hello world');
        expect(trimComment('   ')).toBe('');
    });

    it('handles empty/missing comments', () => {
        expect(trimComment('')).toBe('');
        expect(trimComment(null)).toBe('');
        expect(trimComment(undefined)).toBe('');
    });
});

describe('gradeToDecision', () => {
    it('maps every grade class', () => {
        // A/B → go, C → watch, D → wait — the decision buckets the score bar color
        expect(gradeToDecision('A')).toBe('go');
        expect(gradeToDecision('B')).toBe('go');
        expect(gradeToDecision('C')).toBe('watch');
        expect(gradeToDecision('D')).toBe('wait');
    });

    it('maps the adjacent-grade boundary C→watch vs D→wait and unknown grades', () => {
        expect(gradeToDecision('E')).toBe('unknown');
        expect(gradeToDecision('B-')).toBe('unknown'); // no prefix matching
        expect(gradeToDecision('')).toBe('unknown');
        expect(gradeToDecision(undefined)).toBe('unknown');
    });
});

describe('fmtFreq', () => {
    it('shows kHz under 1000 and MHz with 3 decimals at/above', () => {
        expect(fmtFreq(999)).toBe('999');
        expect(fmtFreq(1000)).toBe('1.000'); // boundary: 1000 kHz switches to MHz
        expect(fmtFreq(14205)).toBe('14.205');
        expect(fmtFreq(50313)).toBe('50.313');
    });

    it('handles boundary values', () => {
        expect(fmtFreq(0)).toBe('0');
        expect(fmtFreq(-500)).toBe('-500'); // negative falls through to the kHz branch
    });
});

describe('fmtAge', () => {
    it('formats seconds, minutes, and hours', () => {
        expect(fmtAge(0)).toBe('0s');
        expect(fmtAge(59)).toBe('59s');
        expect(fmtAge(60)).toBe('1m');
        expect(fmtAge(90)).toBe('2m'); // rounds
        expect(fmtAge(3599)).toBe('60m');
        expect(fmtAge(3600)).toBe('1h');
        expect(fmtAge(7230)).toBe('2h');
    });

    it('degrades on negative input', () => {
        expect(fmtAge(-30)).toBe('-30s'); // < 60 branch, interpolated raw
    });
});

describe('degToCardinal', () => {
    it('maps bearings to 16-point compass abbreviations', () => {
        expect(degToCardinal(0)).toBe('N');
        expect(degToCardinal(45)).toBe('NE');
        expect(degToCardinal(90)).toBe('E');
        expect(degToCardinal(270)).toBe('W');
    });

    it('rounds to the nearest compass point at the 11.25° boundary', () => {
        expect(degToCardinal(11.24)).toBe('N');
        expect(degToCardinal(11.25)).toBe('NNE'); // Math.round(0.5) rounds up
        expect(degToCardinal(348.75)).toBe('N'); // round(15.5)=16 → wraps to index 0
    });

    it('normalizes out-of-range and negative bearings', () => {
        expect(degToCardinal(360)).toBe('N');
        expect(degToCardinal(719.9)).toBe('N');
        expect(degToCardinal(-11.25)).toBe('N'); // 348.75 wraps, then rounds to N
    });
});

describe('meterPct', () => {
    it('clamps to [6, 100]', () => {
        expect(meterPct(0)).toBe(6);
        expect(meterPct(5)).toBe(6);
        expect(meterPct(6)).toBe(6);
        expect(meterPct(6.5)).toBe(6.5);
        expect(meterPct(50)).toBe(50);
        expect(meterPct(100)).toBe(100);
        expect(meterPct(150)).toBe(100);
        expect(meterPct(-10)).toBe(6);
    });

    it('treats missing/NaN scores as the 6% floor', () => {
        expect(meterPct(null)).toBe(6);
        expect(meterPct(undefined)).toBe(6);
        expect(meterPct(NaN)).toBe(6); // NaN || 0 → 0 → clamped to 6
    });
});

describe('parseMode', () => {
    it('reads the mode from the spotter comment, uppercased', () => {
        expect(parseMode('FT8 -12dB')).toBe('FT8');
        expect(parseMode('599 SSB')).toBe('SSB');
        expect(parseMode('CW UP')).toBe('CW');
        expect(parseMode('cw')).toBe('CW'); // case-insensitive
        expect(parseMode('RTTY 14074')).toBe('RTTY');
        expect(parseMode('PSK125')).toBe('PSK125');
    });

    it('folds sideband names into SSB', () => {
        expect(parseMode('LSB')).toBe('SSB');
        expect(parseMode('USB')).toBe('SSB');
    });

    it('returns empty when the comment names no mode', () => {
        expect(parseMode('up 5-10 listening EU')).toBe('');
        expect(parseMode('')).toBe('');
        expect(parseMode(undefined)).toBe('');
    });
});

describe('modeCatFromFreq', () => {
    it('classifies just-inside band edges per the band plan', () => {
        expect(modeCatFromFreq(1810)).toBe('cw'); // 160m band bottom
        expect(modeCatFromFreq(2000)).toBe('phone'); // 160m band top
        expect(modeCatFromFreq(5351.5)).toBe('cw'); // 60m band bottom
        expect(modeCatFromFreq(5366.5)).toBe('phone'); // 60m band top
        expect(modeCatFromFreq(10150)).toBe('digi'); // 30m: digi segment ends at the band top
        expect(modeCatFromFreq(14350)).toBe('phone'); // 20m band top
        expect(modeCatFromFreq(50000)).toBe('cw'); // 6m band bottom
        expect(modeCatFromFreq(52000)).toBe('phone'); // 6m band top
        expect(modeCatFromFreq(144000)).toBe('cw'); // 2m band bottom
        expect(modeCatFromFreq(146000)).toBe('phone'); // 2m band top
        expect(modeCatFromFreq(24900)).toBe('cw'); // 12m, inside the CW segment
    });

    it('returns empty just outside each band edge', () => {
        expect(modeCatFromFreq(1809.9)).toBe('');
        expect(modeCatFromFreq(2000.1)).toBe('');
        expect(modeCatFromFreq(10150.1)).toBe('');
        expect(modeCatFromFreq(14350.1)).toBe('');
        expect(modeCatFromFreq(52000.1)).toBe('');
        expect(modeCatFromFreq(29700.1)).toBe('');
    });

    it('digi dials override the surrounding phone segment', () => {
        // 7074 sits in the 40m phone segment but is the FT4 dial → digi
        expect(modeCatFromFreq(7074)).toBe('digi');
        // dial tolerance is ±1.5 kHz
        expect(modeCatFromFreq(14075.4)).toBe('digi');
        expect(modeCatFromFreq(14075.6)).toBe('digi'); // also digi via the 14099 digi segment
        expect(modeCatFromFreq(7072.4)).toBe('phone'); // 1.6 kHz off the dial → segment wins
        expect(modeCatFromFreq(7075.6)).toBe('phone');
    });

    it('degrades on zero, negative, and non-numeric frequencies', () => {
        expect(modeCatFromFreq(0)).toBe('');
        expect(modeCatFromFreq(-1)).toBe('');
        expect(modeCatFromFreq('abc')).toBe('');
    });
});

describe('modeCat', () => {
    it('prefers the spotter-reported mode over the band plan', () => {
        expect(modeCat({ comment: 'CW UP', freq_khz: 14074 })).toBe('cw');
        expect(modeCat({ comment: '599 SSB', freq_khz: 14074 })).toBe('phone');
        expect(modeCat({ comment: 'AM', freq_khz: 0 })).toBe('phone');
        expect(modeCat({ comment: 'FM', freq_khz: 0 })).toBe('phone');
        expect(modeCat({ comment: 'FT8 -12dB', freq_khz: 0 })).toBe('digi');
    });

    it('falls back to the band plan when the comment names no mode', () => {
        expect(modeCat({ comment: '', freq_khz: 14074 })).toBe('digi');
        expect(modeCat({ comment: 'up 5-10', freq_khz: 14018 })).toBe('cw'); // below the 14070 CW edge
        expect(modeCat({ comment: '', freq_khz: 0 })).toBe(''); // out of band → unknown
    });
});

describe('guessMode', () => {
    it('returns FT8 within ±1.5 kHz of an FT8 dial', () => {
        expect(guessMode(14074)).toBe('FT8');
        expect(guessMode(14075.4)).toBe('FT8');
        expect(guessMode(50313)).toBe('FT8');
        expect(guessMode(14075.6)).toBe(''); // 1.6 kHz off → nothing
        expect(guessMode(14071)).toBe('');
    });

    it('returns CW within 100 kHz below a CW segment edge', () => {
        expect(guessMode(14070)).toBe('CW'); // at the edge
        expect(guessMode(14069)).toBe('CW');
        expect(guessMode(3580)).toBe('CW'); // 80m CW edge
        expect(guessMode(3581)).toBe(''); // past the edge
        expect(guessMode(13969)).toBe(''); // 101 kHz below the edge → outside the window
        expect(guessMode(10150)).toBe('CW'); // 30m CW edge per CW_EDGE_KHZ
    });

    it('degrades on zero, negative, and non-numeric frequencies', () => {
        expect(guessMode(0)).toBe('');
        expect(guessMode(-1)).toBe('');
        expect(guessMode('abc')).toBe('');
    });
});

describe('parsePotaRef', () => {
    it('extracts a letter-led park reference and uppercases it', () => {
        expect(parsePotaRef('POTA K-0817')).toBe('K-0817');
        expect(parsePotaRef('DL-0123')).toBe('DL-0123');
        expect(parsePotaRef('KH6-0123')).toBe('KH6-0123'); // multi-char prefix
        expect(parsePotaRef('k-0817')).toBe('K-0817'); // case-insensitive match
        expect(parsePotaRef('WSPR K-1234 599')).toBe('K-1234');
        expect(parsePotaRef('VK-123456')).toBe('VK-123456'); // 6 digits allowed
    });

    it('does not match digit-led ranges, wrong shapes, or out-of-range digit counts', () => {
        expect(parsePotaRef('5-10')).toBe('');
        expect(parsePotaRef('up 5-10 listening')).toBe('');
        expect(parsePotaRef('K-F123')).toBe(''); // letter in the digit tail
        expect(parsePotaRef('K-081')).toBe(''); // too few digits
        expect(parsePotaRef('K-08171234')).toBe(''); // too many digits → no boundary
        expect(parsePotaRef('')).toBe('');
        expect(parsePotaRef(undefined)).toBe('');
    });
});

describe('enrichSpot', () => {
    it('builds the enrich id from call, band, and reported mode', () => {
        expect(enrichSpot({ dx_call: 'JA3XYZ', band: '20m', freq_khz: 14074, comment: 'FT8 -12dB' }))
            .toEqual({ id: 'JA3XYZ|20m|FT8', call: 'JA3XYZ', band: '20m', mode: 'FT8', pota_ref: '' });
    });

    it('falls back to guessMode when the comment names no mode', () => {
        expect(enrichSpot({ dx_call: 'K1ABC', band: '20m', freq_khz: 14018, comment: '' }).mode).toBe('CW');
        expect(enrichSpot({ dx_call: 'K1ABC', band: '20m', freq_khz: 14205, comment: 'booming in' }).mode).toBe('');
    });

    it('carries the POTA ref from the comment', () => {
        const out = enrichSpot({ dx_call: 'K1ABC', band: '20m', freq_khz: 14205, comment: 'POTA K-0817' });
        expect(out.pota_ref).toBe('K-0817');
        expect(out.id).toBe('K1ABC|20m|'); // no mode named, frequency not on a dial/edge
    });
});

describe('bandColor', () => {
    it('uses the canonical band palette', () => {
        expect(bandColor('20m')).toBe('#008000');
        expect(bandColor('17m')).toBe('#808000');
        expect(bandColor('all')).toBe('#555555');
    });

    it('falls back to the "all" grey for unknown bands', () => {
        expect(bandColor('23cm')).toBe('#555555');
        expect(bandColor(undefined)).toBe('#555555');
    });
});