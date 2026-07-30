import { beforeEach, describe, expect, it, vi } from 'vitest';

// Verifies the Mercator Grid-SNR threshold + intensity contract:
//   * A grid square is only drawn when at least one spot in it passes BOTH
//     the SNR threshold (active min-SNR mode) AND the band filter.
//   * A square's intensity (opacity) reflects only the filter-passing spots,
//     not band/SNR-filtered-out spots that happen to share the square.
//
// Regression guard for the "green 20m squares with no spots above the set
// threshold" symptom: in SSB mode the gate at renderGridSnr must filter
// below-threshold spots, and the active slider is the only one consulted.

const { mockMap } = vi.hoisted(() => ({
    mockMap: {
        removeLayer: vi.fn(),
        fitBounds: vi.fn(),
        hasLayer: vi.fn(() => false)
    }
}));

vi.mock('../static/map.js', () => ({ map: mockMap }));

import { state } from '../static/state.js';
import { updateMapVisualization } from '../static/renderers.js';

let geoJsonCalls;
function setupDom({ minSnr = 'ssb', ssbMinDb = 0, cwMinDb = -15, focusBand = '', enabled = ['20m', '15m'], highlightModel = 'classic' } = {}) {
    const enabledHtml = enabled.map(b => `<input type="checkbox" class="band-enable" value="${b}" checked />`).join('');
    document.body.innerHTML = `
        <input id="target" value="JO32" />
        <input id="ssb-min-db" value="${ssbMinDb}" />
        <input id="cw-min-db" value="${cwMinDb}" />
        <input id="auto-zoom" type="checkbox" />
        <div id="band-container" data-focus-band="${focusBand}"></div>
        ${enabledHtml}
        <input type="radio" name="min-snr" value="none" ${minSnr === 'none' ? 'checked' : ''} />
        <input type="radio" name="min-snr" value="cw" ${minSnr === 'cw' ? 'checked' : ''} />
        <input type="radio" name="min-snr" value="ssb" ${minSnr === 'ssb' ? 'checked' : ''} />
        <input type="radio" name="style-select" value="grid-snr" checked />
        <input type="radio" name="style-select" value="active-area" />
        <input type="radio" name="grid-highlight-model" value="classic" ${highlightModel === 'classic' ? 'checked' : ''} />
        <input type="radio" name="grid-highlight-model" value="reachability" ${highlightModel === 'reachability' ? 'checked' : ''} />
    `;
}

function installLeafletMock() {
    geoJsonCalls = [];
    globalThis.L = {
        layerGroup: () => ({ addTo: () => ({ __kind: 'layerGroup' }) }),
        geoJSON: vi.fn((fc) => {
            geoJsonCalls.push(fc);
            return { addTo: () => ({ __kind: 'geojson' }) };
        }),
        canvas: () => ({ addTo: () => ({ __kind: 'canvas' }) }),
        circleMarker: () => ({ addTo: () => ({}), bindTooltip() { return this; }, on() { return this; } }),
        rectangle: () => ({ addTo: () => ({}) }),
        latLngBounds: () => ({ getCenter: () => ({ toBounds: () => ({}), extend: () => {} }) })
    };
}

function drawFeatures() {
    // L.geoJSON is called once per (re)build with the full FeatureCollection.
    const all = geoJsonCalls.flatMap((fc) => (fc && fc.features) ? fc.features : []);
    return all.map((f) => ({ color: f.properties.color, fillOpacity: f.properties.fillOpacity }));
}

function spot(locator, snr, band, sourceType = '') {
    return { locator, snr, band, sourceType, lat: 50, lng: 10 };
}

beforeEach(() => {
    globalThis.__HORST_PERF_TEST__ = true;
    if (typeof localStorage !== 'undefined' && localStorage) localStorage.clear();
    setupDom();
    installLeafletMock();
    mockMap.removeLayer.mockReset();
    state.heatLayer = null;
});

describe('renderGridSnr threshold gate', () => {
    it('does not draw a square when every spot in it is below the SSB threshold', () => {
        setupDom({ minSnr: 'ssb', ssbMinDb: 0 });
        const spots = [
            spot('JO32', -5, '20m'),
            spot('JO32', -12, '20m')
        ];
        updateMapVisualization(spots, 15);
        expect(drawFeatures()).toEqual([]);
    });

    it('draws a 20m (green) square when a spot passes the SSB threshold', () => {
        setupDom({ minSnr: 'ssb', ssbMinDb: 0 });
        const spots = [spot('JO32', 3, '20m'), spot('JO32', -8, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // bandColors['20m'] is green (#008000).
        expect(feats[0].color).toBe('#008000');
    });

    it('in All (none) mode draws a square from below-threshold spots (no filter)', () => {
        setupDom({ minSnr: 'none', ssbMinDb: 0 });
        const spots = [spot('JO32', -5, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].color).toBe('#008000');
        // maxSnr < 0 -> low intensity (the only honest signal that the spot is weak).
        expect(feats[0].fillOpacity).toBeCloseTo(0.22);
    });

    it('in CW mode filters by the CW threshold, not the (dead) SSB slider', () => {
        setupDom({ minSnr: 'cw', cwMinDb: -15, ssbMinDb: 0 });
        // -10 dB passes the CW threshold (-10 >= -15) even though it is below
        // the SSB slider's 0 dB — exactly the footgun scenario.
        const spots = [spot('JO32', -10, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].color).toBe('#008000');
    });

    it('in CW mode drops spots below the CW threshold', () => {
        setupDom({ minSnr: 'cw', cwMinDb: -15 });
        const spots = [spot('JO32', -20, '20m')];
        updateMapVisualization(spots, 15);
        expect(drawFeatures()).toEqual([]);
    });
});

describe('renderGridSnr intensity reflects only filter-passing spots', () => {
    it('does not let a band-filtered-out spot inflate a soloed-band square', () => {
        // Solo 20m. The square has a weak passing 20m spot (+1 dB) and a strong
        // 15m spot (+25 dB) that is filtered out by the band filter. The square
        // must render at MEDIUM intensity (0.45, from the +1 dB 20m spot), not
        // HIGH (0.72 from the hidden +25 dB 15m spot).
        setupDom({ minSnr: 'ssb', ssbMinDb: 0, focusBand: '20m' });
        const spots = [spot('JO32', 1, '20m'), spot('JO32', 25, '15m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].color).toBe('#008000');
        expect(feats[0].fillOpacity).toBeCloseTo(0.45);
    });

    it('does not let a sub-threshold spot inflate intensity', () => {
        // SSB threshold 0. Passing 20m spot at +2 dB; a sub-threshold 20m spot
        // at +30 dB is impossible by definition (sub-threshold means < 0), so
        // instead verify a passing +12 dB spot yields HIGH intensity on its own.
        setupDom({ minSnr: 'ssb', ssbMinDb: 0 });
        const spots = [spot('JO32', 12, '20m'), spot('JO32', -9, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].fillOpacity).toBeCloseTo(0.72);
    });

    it('colors by the dominant band among passing spots only', () => {
        // Two 20m + one 15m, all passing; 20m dominates -> green.
        setupDom({ minSnr: 'ssb', ssbMinDb: 0 });
        const spots = [
            spot('JO32', 5, '20m'),
            spot('JO32', 4, '20m'),
            spot('JO32', 9, '15m')
        ];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].color).toBe('#008000');
    });
});

// Reachability model (top-quartile mean -> continuous opacity ramp). The core
// fix: one lucky strong decode among many weak reports must NOT light up the
// square; corroborated strong paths should.
describe('renderGridSnr reachability model', () => {
    it('does not highlight a square on a single strong outlier among weak spots', () => {
        setupDom({ minSnr: 'none', highlightModel: 'reachability' });
        const spots = [
            spot('JO32', 12, '20m'),
            ...Array.from({ length: 20 }, () => spot('JO32', -8, '20m'))
        ];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // top quartile = best 6 -> mean(12, -8 x5) ~= -4.67 dB -> ramp ~0.31,
        // far below the classic HIGH tier the +12 outlier would have forced.
        expect(feats[0].fillOpacity).toBeLessThan(0.35);
    });

    it('reads bright when strong reports are corroborated', () => {
        setupDom({ minSnr: 'none', highlightModel: 'reachability' });
        const spots = [10, 11, 12, 13, 14, 15].map((s) => spot('JO32', s, '20m'));
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // top quartile = best 2 -> mean 14.5 -> ramp ~0.67.
        expect(feats[0].fillOpacity).toBeGreaterThan(0.6);
    });

    it('still gives a lone strong spot visual credit', () => {
        setupDom({ minSnr: 'none', highlightModel: 'reachability' });
        const spots = [spot('JO32', 12, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // mean of 1 = 12 dB -> 0.45 + 0.015*12 = 0.63.
        expect(feats[0].fillOpacity).toBeCloseTo(0.63, 2);
    });

    it('subdues a lone weak spot on the ramp', () => {
        setupDom({ minSnr: 'none', highlightModel: 'reachability' });
        const spots = [spot('JO32', -5, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // -5 dB -> 0.45 - 0.15 = 0.30.
        expect(feats[0].fillOpacity).toBeCloseTo(0.30, 2);
    });

    it('keeps the filter-first contract: filtered-out spots never feed the score', () => {
        // Solo 20m; a strong 15m spot is band-filtered out and must not lift
        // the weak passing 20m spot's square.
        setupDom({ minSnr: 'none', focusBand: '20m', highlightModel: 'reachability' });
        const spots = [spot('JO32', -5, '20m'), spot('JO32', 25, '15m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].fillOpacity).toBeCloseTo(0.30, 2);
    });
});