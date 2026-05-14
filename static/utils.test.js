import { describe, it, expect, beforeEach } from 'vitest';
import { formatNumber, latLngToLocator, locatorToBounds, getGridResolution, getSelectedBand } from './utils.js';

describe('utils.js', () => {
    describe('Pure Mathematical & Formatting Functions', () => {
        it('formatNumber formats numbers with space separators', () => {
            expect(formatNumber(1000)).toBe('1 000');
            expect(formatNumber(1234567)).toBe('1 234 567');
            expect(formatNumber(null)).toBe('');
            expect(formatNumber(0)).toBe('0');
        });

        it('latLngToLocator converts coordinates to a Maidenhead locator', () => {
            // Test known lat/lng mapping to 4-character Maidenhead locators
            expect(latLngToLocator(41.5, -73.0, 4)).toBe('FN31');
            expect(latLngToLocator(52.5, 7.0, 4)).toBe('JO32');
        });

        it('locatorToBounds converts locator strings into lat/lng bounds', () => {
            const bounds4 = locatorToBounds('FN31');
            expect(bounds4).toEqual([[41, -74], [42, -72]]);

            const bounds6 = locatorToBounds('FN31AB');
            expect(bounds6[0][0]).toBeCloseTo(41.0); // lat min
            expect(bounds6[0][1]).toBeCloseTo(-74.0); // lng min
        });
    });

    describe('DOM Reading Functions', () => {
        beforeEach(() => {
            // Clear the virtual DOM before each test
            document.body.innerHTML = '';
        });

        it('getGridResolution returns 6 for valid 6-character locators, 4 otherwise', () => {
            document.body.innerHTML = '<input id="target" value="FN31AB" />';
            expect(getGridResolution()).toBe(6);
            
            document.body.innerHTML = '<input id="target" value="W1AW" />';
            expect(getGridResolution()).toBe(4);
        });

        it('getSelectedBand returns the value of the checked band radio input', () => {
            document.body.innerHTML = `
                <input type="radio" name="band" value="20m" checked />
                <input type="radio" name="band" value="40m" />
            `;
            expect(getSelectedBand()).toBe('20m');
        });
    });
});