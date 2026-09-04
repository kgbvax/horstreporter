import { describe, expect, it, vi } from 'vitest';

// Mock perf.js so aggregateGridSquares doesn't touch real perf counters.
vi.mock('../static/perf.js', () => ({
    startPerfTimer: () => 0,
    endPerfTimer: () => {},
    incrementPerfCounter: () => {},
    isPerfProfilingEnabled: () => false,
}));

// Mock map.js so renderers.js loads without a real Leaflet map.
vi.mock('../static/map.js', () => ({ map: {} }));

import { aggregateGridSquares } from '../static/renderers.js';
import { regionForLocatorCached, regionForLocator } from '../static/utils.js';

// Build a minimal filterCtx matching the shape buildFilterCtx() produces.
function ctx(overrides = {}) {
    return {
        minSnrMode: 'ssb',
        ssbMinDb: -100,
        cwMinDb: -100,
        selectedBand: 'all',
        enabledBands: new Set(['20m', '40m', '10m']),
        ...overrides,
    };
}

// Spot factory. locator → region is resolved by regionForLocatorCached.
function spot(band, locator, snr = 10) {
    return { band, locator, snr, sender: 'a', receiver: 'b', ageSeconds: 0 };
}

describe('aggregateGridSquares — base aggregation', () => {
    it('aggregates all matching spots into their grid squares', () => {
        const spots = [
            spot('20m', 'JO62'),   // EU
            spot('40m', 'FN31'),   // NA
            spot('10m', 'QF22'),   // VK
        ];
        const { squareData } = aggregateGridSquares(spots, ctx());
        expect(Object.keys(squareData).sort()).toEqual(['FN31', 'JO62', 'QF22']);
    });

    it('disabled bands and soloed bands filter the aggregate', () => {
        const spots = [
            spot('20m', 'JO62'),
            spot('40m', 'FN31'),
            spot('20m', 'FN31'),
        ];
        const { squareData } = aggregateGridSquares(spots, ctx({
            enabledBands: new Set(['20m']),
        }));
        expect(Object.keys(squareData).sort()).toEqual(['FN31', 'JO62']);
        // No 40m squares.
        for (const loc in squareData) {
            expect(squareData[loc].bands['40m'] || 0).toBe(0);
        }
    });
});

describe('regionForLocatorCached — consistency with regionForLocator', () => {
    // The JS classifier must mirror Go's dxPulseRegionForLocator (dx_regions.go:48).
    // We cross-check the memoized and non-memoized JS variants agree, and that
    // the canonical locators map to the same regions the Go classifier produces.
    const cases = [
        ['JO62', 'EU'],    // Berlin
        ['FN31', 'NA'],    // New York
        ['QF22', 'VK'],    // Australia
        ['PM96', 'JA'],    // Japan
        ['GF15', 'SA'],    // Brazil
        ['KJ76', 'AF'],    // South Africa
        ['GB45', 'AN'],    // Antarctica
        ['BL11', 'KH6'],   // Hawaii
        ['FK83', 'CAR'],   // Caribbean
    ];

    it('regionForLocatorCached matches regionForLocator for all canonical locators', () => {
        for (const [loc, expected] of cases) {
            expect(regionForLocator(loc), `non-cached ${loc}`).toBe(expected);
            expect(regionForLocatorCached(loc), `cached ${loc}`).toBe(expected);
        }
    });

    it('cached and non-cached produce identical results across a broader sweep', () => {
        const sweep = ['JO62', 'JN58', 'FN31', 'EM95', 'QF22', 'PM96', 'GF15', 'KJ76'];
        for (const loc of sweep) {
            expect(regionForLocatorCached(loc)).toBe(regionForLocator(loc));
        }
    });
});