import { describe, it, expect } from 'vitest';
import { regionForLocator, regionForLatLng, locatorClusterAnchorJS, locatorToLatLngJS, WSPR_REGIONS } from '../static/utils.js';

describe('regionForLocator', () => {
    it('classifies known locators to DXPulse regions', () => {
        expect(regionForLocator('JO62')).toBe('EU');    // Berlin
        expect(regionForLocator('FN31')).toBe('NA');    // New York
        expect(regionForLocator('QF22')).toBe('VK');    // Australia (VK/ZL)
        expect(regionForLocator('PM96')).toBe('JA');    // Japan
        expect(regionForLocator('GF15')).toBe('SA');    // Brazil
        expect(regionForLocator('KJ76')).toBe('AF');    // South Africa
    });

    it('returns empty for non-locators', () => {
        expect(regionForLocator('W1AW')).toBe('');
        expect(regionForLocator('')).toBe('');
        expect(regionForLocator(null)).toBe('');
    });
});

describe('regionForLatLng', () => {
    it('classifies Antarctica', () => {
        expect(regionForLatLng(-70, 0)).toBe('AN');
    });

    it('classifies Hawaii', () => {
        expect(regionForLatLng(21, -157)).toBe('KH6');
    });

    it('classifies Caribbean', () => {
        expect(regionForLatLng(18, -70)).toBe('CAR');
    });
});

describe('locatorClusterAnchorJS', () => {
    it('floors to 6×6 cluster anchor', () => {
        expect(locatorClusterAnchorJS('JO62')).toBe('JN68');
        expect(locatorClusterAnchorJS('FN31')).toBe('EM86');
        expect(locatorClusterAnchorJS('CM87')).toBe('CM46');
    });

    it('returns null for non-locators', () => {
        expect(locatorClusterAnchorJS('W1AW')).toBe(null);
        expect(locatorClusterAnchorJS('')).toBe(null);
        expect(locatorClusterAnchorJS(null)).toBe(null);
    });
});

describe('WSPR_REGIONS', () => {
    it('has 11 regions in display order', () => {
        expect(WSPR_REGIONS).toHaveLength(11);
        expect(WSPR_REGIONS[0]).toBe('EU');
        expect(WSPR_REGIONS[10]).toBe('AN');
    });
});