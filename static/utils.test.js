import { describe, it, expect, beforeEach } from 'vitest';
import {
    formatNumber,
    latLngToLocator,
    locatorToBounds,
    setFaviconColor,
    getGridResolution,
    getMinSnrMode,
    getSelectedBand,
    getEnabledBands
} from './utils.js';

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

        it('latLngToLocator clamps out-of-range coordinates and supports 6-char precision', () => {
            const high = latLngToLocator(999, 999, 6);
            const low = latLngToLocator(-999, -999, 6);

            expect(high).toHaveLength(6);
            expect(low).toHaveLength(6);
            expect(high).toMatch(/^[A-S]{2}[0-9]{2}[A-X]{2}$/);
            expect(low).toMatch(/^[A-S]{2}[0-9]{2}[A-X]{2}$/);
        });

        it('locatorToBounds converts locator strings into lat/lng bounds', () => {
            const bounds4 = locatorToBounds('FN31');
            expect(bounds4).toEqual([[41, -74], [42, -72]]);

            const bounds6 = locatorToBounds('FN31AB');
            expect(bounds6[0][0]).toBeCloseTo(41 + (2.5 / 60)); // lat min
            expect(bounds6[0][1]).toBeCloseTo(-74.0); // lng min
        });

        it('setFaviconColor updates favicon href when favicon element exists', () => {
            document.body.innerHTML = '<link id="favicon" rel="icon" href="about:blank">';
            setFaviconColor('#123456');
            const href = document.getElementById('favicon')?.getAttribute('href') || '';
            expect(href).toContain('data:image/svg+xml');
            expect(decodeURIComponent(href)).toContain('#123456');
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

        it('getMinSnrMode returns checked value and falls back to none', () => {
            document.body.innerHTML = '';
            expect(getMinSnrMode()).toBe('none');

            document.body.innerHTML = `
                <input type="radio" name="min-snr" value="none" />
                <input type="radio" name="min-snr" value="cw" checked />
                <input type="radio" name="min-snr" value="ssb" />
            `;
            expect(getMinSnrMode()).toBe('cw');
        });

        it('getEnabledBands returns only checked .band-enable values', () => {
            document.body.innerHTML = `
                <input type="checkbox" class="band-enable" value="20m" checked />
                <input type="checkbox" class="band-enable" value="40m" />
                <input type="checkbox" class="band-enable" value="6m" checked />
            `;

            const enabled = getEnabledBands();
            expect(enabled.has('20m')).toBe(true);
            expect(enabled.has('6m')).toBe(true);
            expect(enabled.has('40m')).toBe(false);
            expect(enabled.size).toBe(2);
        });
    });
});