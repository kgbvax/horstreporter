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
import { state } from '../static/state.js';

// Build a minimal filterCtx matching the shape buildFilterCtx() produces.
function ctx(overrides = {}) {
    return {
        minSnrMode: 'ssb',
        ssbMinDb: -100,
        cwMinDb: -100,
        selectedBand: 'all',
        enabledBands: new Set(['20m', '40m', '10m']),
        filterBand: '',
        filterRegion: '',
        ...overrides,
    };
}

// Spot factory. locator → region is resolved by regionForLocatorCached.
function spot(band, locator, snr = 10) {
    return { band, locator, snr, sender: 'a', receiver: 'b', ageSeconds: 0 };
}

describe('aggregateGridSquares — drill-down filter (U4)', () => {
    it('returns all matching squares when no drill-down filter is set', () => {
        const spots = [
            spot('20m', 'JO62'),   // EU
            spot('40m', 'FN31'),   // NA
            spot('10m', 'QF22'),   // VK
        ];
        const { squareData } = aggregateGridSquares(spots, ctx());
        expect(Object.keys(squareData).sort()).toEqual(['FN31', 'JO62', 'QF22']);
    });

    it('AE3: 20m × EU returns only EU 20m squares', () => {
        const spots = [
            spot('20m', 'JO62'),   // EU 20m ✓
            spot('20m', 'FN31'),   // NA 20m ✗
            spot('40m', 'JO62'),   // EU 40m ✗ (wrong band)
            spot('10m', 'QF22'),   // VK 10m ✗
            spot('20m', 'JN58'),   // EU 20m ✓
        ];
        const { squareData } = aggregateGridSquares(spots, ctx({
            filterBand: '20m',
            filterRegion: 'EU',
        }));
        const locs = Object.keys(squareData).sort();
        expect(locs).toEqual(['JN58', 'JO62']);
        // Every retained square must be EU 20m.
        for (const loc of locs) {
            expect(regionForLocatorCached(loc)).toBe('EU');
            for (const b in squareData[loc].bands) {
                expect(b).toBe('20m');
            }
        }
    });

    it('clear filter restores all squares (no region/band filter)', () => {
        const spots = [
            spot('20m', 'JO62'),
            spot('40m', 'FN31'),
            spot('10m', 'QF22'),
        ];
        const { squareData } = aggregateGridSquares(spots, ctx());
        expect(Object.keys(squareData).sort()).toEqual(['FN31', 'JO62', 'QF22']);
    });

    it('non-matching region → no grid squares rendered (empty plot, not an error)', () => {
        // AN (Antarctica) has no spots in the input set.
        const spots = [
            spot('20m', 'JO62'),
            spot('40m', 'FN31'),
        ];
        const { squareData, activeBands } = aggregateGridSquares(spots, ctx({
            filterBand: '20m',
            filterRegion: 'AN',
        }));
        expect(Object.keys(squareData)).toHaveLength(0);
        expect(activeBands.size).toBe(0);
    });

    it('filterBand alone (no region) restricts to that band', () => {
        const spots = [
            spot('20m', 'JO62'),
            spot('40m', 'FN31'),
            spot('20m', 'FN31'),
        ];
        const { squareData } = aggregateGridSquares(spots, ctx({ filterBand: '20m' }));
        expect(Object.keys(squareData).sort()).toEqual(['FN31', 'JO62']);
        // No 40m squares.
        for (const loc in squareData) {
            expect(squareData[loc].bands['40m'] || 0).toBe(0);
        }
    });

    it('filterRegion alone (no band) restricts to that region', () => {
        const spots = [
            spot('20m', 'JO62'),   // EU
            spot('40m', 'FN31'),   // NA
            spot('10m', 'PM96'),   // JA
        ];
        const { squareData } = aggregateGridSquares(spots, ctx({ filterRegion: 'NA' }));
        expect(Object.keys(squareData)).toEqual(['FN31']);
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

describe('state drill-down fields', () => {
    it('defaults to empty strings (no drill-down)', () => {
        expect(state.drillDownBand).toBe('');
        expect(state.drillDownRegion).toBe('');
    });
});