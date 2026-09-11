import { afterEach, describe, expect, it, vi } from 'vitest';

// Unit U5 (plan 2026-09-11-002): the shared grayline time basis. KTD-9 keeps
// this clock narrow — only the grayline terminator time reads it; every
// cadence guard stays on wall-clock Date.now().
import { clearDataNowOverride, dataNow, getDataNowMs, setDataNowMs } from './data-now.js';

afterEach(() => {
    clearDataNowOverride();
    vi.useRealTimers();
});

describe('data-now.js shared clock', () => {
    it('returns the wall clock when no override is set (live mode)', () => {
        vi.useFakeTimers({ now: 1_730_000_123_000 });
        expect(dataNow()).toBe(1_730_000_123_000);
        expect(getDataNowMs()).toBeNull();
    });

    it('returns the playhead override during timeline mode', () => {
        vi.useFakeTimers({ now: 1_730_000_000_000 });
        setDataNowMs(1_700_000_456_000);
        expect(dataNow()).toBe(1_700_000_456_000);
        expect(getDataNowMs()).toBe(1_700_000_456_000);
    });

    it('falls back to the wall clock after the override is cleared (timeline exit)', () => {
        vi.useFakeTimers({ now: 1_730_000_000_000 });
        setDataNowMs(1_700_000_456_000);
        clearDataNowOverride();
        expect(getDataNowMs()).toBeNull();
        expect(dataNow()).toBe(1_730_000_000_000);
    });

    it('ignores non-finite overrides so live mode keeps the wall clock', () => {
        vi.useFakeTimers({ now: 1_730_000_123_000 });
        setDataNowMs(Number.NaN);
        expect(dataNow()).toBe(1_730_000_123_000);
        expect(getDataNowMs()).toBeNull();
        setDataNowMs(1_700_000_456_000);
        setDataNowMs(Number.POSITIVE_INFINITY);
        expect(getDataNowMs()).toBeNull();
    });
});