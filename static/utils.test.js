import { describe, it, expect, beforeEach } from 'vitest';
import {
    formatNumber,
    getCountryColoringEnabled,
    getMercatorDxccLabelsEnabled,
    getClosestSunEvent,
    getCountryFillForFeature,
    getCountryFillForKey,
    getGraylineEnabled,
    getGraylineOverlayCells,
    getGraylineOverlayOpacities,
    getGraylineSegments,
    latLngToLocator,
    locatorToBounds,
    normalizeLongitude,
    GRAYLINE_TWILIGHT_WIDTH_DEGREES,
    setFaviconColor,
    getSubsolarPoint,
    getSolarZenithAngle,
    getGridResolution,
    normalizeDxPulseTarget,
    buildDxPulseUrl,
    getMinSnrMode,
    getSelectedBand,
    getEnabledBands,
    pillTextColor,
    greatCircleDistanceKm,
    initialBearingDeg,
    greatCirclePoints,
    setSubmitMode,
    isStreaming
} from './utils.js';

describe('utils.js', () => {
    describe('Great-circle geometry', () => {
        // Berlin (52.52, 13.40) → Tokyo (35.68, 139.69)
        it('greatCircleDistanceKm matches the known ~8900 km Berlin→Tokyo path', () => {
            const d = greatCircleDistanceKm(52.52, 13.40, 35.68, 139.69);
            expect(d).toBeGreaterThan(8800);
            expect(d).toBeLessThan(9000);
        });

        it('initialBearingDeg from Berlin to Tokyo is roughly NE (~40°)', () => {
            const b = initialBearingDeg(52.52, 13.40, 35.68, 139.69);
            expect(b).toBeGreaterThan(25);
            expect(b).toBeLessThan(55);
        });

        it('greatCirclePoints returns segments+1 points, endpoints exact', () => {
            const pts = greatCirclePoints(52.52, 13.40, 35.68, 139.69, 16);
            expect(pts.length).toBe(17);
            expect(pts[0][0]).toBeCloseTo(52.52, 3);
            expect(pts[0][1]).toBeCloseTo(13.40, 3);
            expect(pts[16][0]).toBeCloseTo(35.68, 3);
            expect(pts[16][1]).toBeCloseTo(139.69, 3);
        });

        it('greatCirclePoints handles identical endpoints without NaN', () => {
            const pts = greatCirclePoints(10, 20, 10, 20, 8);
            expect(pts.every(([la, lo]) => Number.isFinite(la) && Number.isFinite(lo))).toBe(true);
        });
    });

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

        it('normalizeDxPulseTarget keeps squares and trims subsquares to 4 characters', () => {
            expect(normalizeDxPulseTarget('fn31')).toBe('FN31');
            expect(normalizeDxPulseTarget('fn31ab')).toBe('FN31');
            expect(normalizeDxPulseTarget('w1aw')).toBe('');
        });

        it('buildDxPulseUrl builds a DXPulse page URL for locator targets', () => {
            expect(buildDxPulseUrl({ target: 'fn31ab', minutes: 15, surroundings: true, mode: 'anomaly', lookbackDays: 60 }))
                .toBe('/dxpulse/?target=FN31&mode=anomaly&minutes=15&lookback_days=60&surroundings=true');
            expect(buildDxPulseUrl({ target: 'W1AW', minutes: 15 })).toBe('');
        });

        it('setFaviconColor updates favicon href when favicon element exists', () => {
            document.body.innerHTML = '<link id="favicon" rel="icon" href="about:blank">';
            setFaviconColor('#123456');
            const href = document.getElementById('favicon')?.getAttribute('href') || '';
            expect(href).toContain('data:image/svg+xml');
            expect(decodeURIComponent(href)).toContain('#123456');
        });

        it('normalizeLongitude wraps values into the expected range', () => {
            expect(normalizeLongitude(190)).toBe(-170);
            expect(normalizeLongitude(-190)).toBe(170);
            expect(normalizeLongitude(45)).toBe(45);
        });

        it('getCountryFillForKey is deterministic for a given key and theme', () => {
            expect(getCountryFillForKey('DEU', 'light')).toBe(getCountryFillForKey('DEU', 'light'));
            expect(getCountryFillForKey('DEU', 'dark')).toBe(getCountryFillForKey('DEU', 'dark'));
            expect(getCountryFillForKey('DEU', 'light')).not.toBe(getCountryFillForKey('DEU', 'dark'));
        });

        it('getCountryFillForFeature prefers map color indexes like the azimuth map', () => {
            const feature = { properties: { MAPCOLOR13: 2, ADM0_A3: 'DEU' } };
            expect(getCountryFillForFeature(feature, 'light')).toBe('#FBD3D1');
            expect(getCountryFillForFeature(feature, 'dark')).toBe('#5a3c3b');
        });

        it('getSubsolarPoint returns a plausible solar position', () => {
            const subsolar = getSubsolarPoint(new Date('2026-03-20T12:00:00Z'));

            expect(subsolar.lat).toBeGreaterThan(-2);
            expect(subsolar.lat).toBeLessThan(2);
            expect(subsolar.lng).toBeGreaterThanOrEqual(-180);
            expect(subsolar.lng).toBeLessThanOrEqual(180);
        });

        it('getGraylineSegments returns usable terminator segments', () => {
            const segments = getGraylineSegments(new Date('2026-03-20T12:00:00Z'), 10);

            expect(segments.length).toBeGreaterThan(0);
            expect(segments.some(segment => segment.length >= 10)).toBe(true);

            segments.flat().forEach(([lat, lng]) => {
                expect(lat).toBeGreaterThanOrEqual(-90);
                expect(lat).toBeLessThanOrEqual(90);
                expect(lng).toBeGreaterThanOrEqual(-180);
                expect(lng).toBeLessThanOrEqual(180);
            });
        });

        it('getSolarZenithAngle is lower near the subsolar point than on the night side', () => {
            const source = { lat: 0, lng: 0 };

            expect(getSolarZenithAngle(0, 0, source)).toBeCloseTo(0, 6);
            expect(getSolarZenithAngle(0, 180, source)).toBeCloseTo(180, 6);
        });

        it('getGraylineOverlayOpacities creates a twilight band about one hour wide', () => {
            const source = { lat: 0, lng: 0 };
            const dayEdge = 90 - (GRAYLINE_TWILIGHT_WIDTH_DEGREES / 2);
            const nightEdge = 90 + (GRAYLINE_TWILIGHT_WIDTH_DEGREES / 2);

            const daySide = getGraylineOverlayOpacities(0, dayEdge - 5, source);
            const twilight = getGraylineOverlayOpacities(0, 90, source);
            const nightSide = getGraylineOverlayOpacities(0, nightEdge + 10, source);

            expect(daySide.graylineOpacity).toBe(0);
            expect(daySide.nightOpacity).toBe(0);
            expect(twilight.graylineOpacity).toBeGreaterThan(0);
            expect(nightSide.nightOpacity).toBeGreaterThan(0);
        });

        it('getGraylineOverlayCells yields shaded cells only for twilight and night regions', () => {
            const cells = getGraylineOverlayCells({ lat: 0, lng: 0 }, { latStep: 30, lngStep: 30 });

            expect(cells.length).toBeGreaterThan(0);
            expect(cells.every(cell => cell.graylineOpacity > 0 || cell.nightOpacity > 0)).toBe(true);
        });

        it('getClosestSunEvent returns a nearby sunrise/sunset with signed minutes when within range', () => {
            const equinoxNoon = new Date('2026-03-20T12:00:00Z');
            const closest = getClosestSunEvent(0, 0, equinoxNoon, 500);

            expect(closest).not.toBeNull();
            expect(['sunrise', 'sunset']).toContain(closest.type);
            expect(Number.isInteger(closest.deltaMinutes)).toBe(true);
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

        it('getSelectedBand returns the focus band from #band-container, else all', () => {
            document.body.innerHTML = '<div id="band-container" data-focus-band="20m"></div>';
            expect(getSelectedBand()).toBe('20m');
            document.getElementById('band-container').dataset.focusBand = '';
            expect(getSelectedBand()).toBe('all');
            document.body.innerHTML = '';
            expect(getSelectedBand()).toBe('all');
        });

        it('pillTextColor picks dark text on light band colors and white on dark', () => {
            expect(pillTextColor('#FFA500')).toBe('#212529'); // orange (15m) -> dark
            expect(pillTextColor('#00FFFF')).toBe('#212529'); // cyan (12m) -> dark
            expect(pillTextColor('#0000FF')).toBe('#ffffff'); // blue (40m) -> white
            expect(pillTextColor('bad')).toBe('#ffffff');
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

        it('getGraylineEnabled is always enabled', () => {
            document.body.innerHTML = '<input type="checkbox" id="show-grayline" checked />';
            expect(getGraylineEnabled()).toBe(true);

            document.body.innerHTML = '<input type="checkbox" id="show-grayline" />';
            expect(getGraylineEnabled()).toBe(true);
        });

        it('getCountryColoringEnabled reads checkbox when present and falls back to localStorage', () => {
            document.body.innerHTML = '';
            expect(getCountryColoringEnabled()).toBe(true);

            Object.defineProperty(globalThis, 'localStorage', {
                configurable: true,
                value: {
                    _store: new Map(),
                    getItem(key) { return this._store.has(key) ? this._store.get(key) : null; },
                    setItem(key, value) { this._store.set(String(key), String(value)); },
                    removeItem(key) { this._store.delete(String(key)); }
                }
            });

            localStorage.setItem('countryColoringEnabled', 'false');
            expect(getCountryColoringEnabled()).toBe(false);

            document.body.innerHTML = '<input type="checkbox" id="show-country-coloring" />';
            expect(getCountryColoringEnabled()).toBe(false);

            document.body.innerHTML = '<input type="checkbox" id="show-country-coloring" checked />';
            expect(getCountryColoringEnabled()).toBe(true);
        });

        it('getMercatorDxccLabelsEnabled reads from the checkbox when present', () => {
            document.body.innerHTML = '<input type="checkbox" id="show-dxcc-labels" checked />';
            expect(getMercatorDxccLabelsEnabled()).toBe(true);

            document.body.innerHTML = '<input type="checkbox" id="show-dxcc-labels" />';
            expect(getMercatorDxccLabelsEnabled()).toBe(false);
        });
    });

    describe('Go/Stop submit button state', () => {
        beforeEach(() => {
            document.body.innerHTML = '<button id="btn-submit" data-mode="go" title="Go" aria-label="Go"><i class="fas fa-play"></i></button>';
        });

        it('setSubmitMode("stop") sets streaming state, stop icon, and label', () => {
            const btn = document.getElementById('btn-submit');
            setSubmitMode(btn, 'stop');
            expect(btn.dataset.mode).toBe('stop');
            expect(btn.innerHTML).toContain('fa-stop');
            expect(btn.innerHTML).not.toContain('fa-play');
            expect(btn.title).toBe('Stop');
            expect(btn.getAttribute('aria-label')).toBe('Stop');
        });

        it('setSubmitMode("go") sets idle state, play icon, and label', () => {
            const btn = document.getElementById('btn-submit');
            setSubmitMode(btn, 'stop');
            setSubmitMode(btn, 'go');
            expect(btn.dataset.mode).toBe('go');
            expect(btn.innerHTML).toContain('fa-play');
            expect(btn.innerHTML).not.toContain('fa-stop');
            expect(btn.title).toBe('Go');
            expect(btn.getAttribute('aria-label')).toBe('Go');
        });

        it('isStreaming reflects the data-mode attribute', () => {
            const btn = document.getElementById('btn-submit');
            expect(isStreaming(btn)).toBe(false);
            setSubmitMode(btn, 'stop');
            expect(isStreaming(btn)).toBe(true);
            setSubmitMode(btn, 'go');
            expect(isStreaming(btn)).toBe(false);
        });

        it('helpers are null-safe when the button is missing', () => {
            expect(() => setSubmitMode(null, 'stop')).not.toThrow();
            expect(isStreaming(null)).toBe(false);
            expect(isStreaming(undefined)).toBe(false);
        });
    });
});