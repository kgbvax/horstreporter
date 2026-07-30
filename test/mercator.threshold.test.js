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
import { updateMapVisualization, clearDxClusterMarkers } from '../static/renderers.js';

let geoJsonCalls;
function setupDom({ minSnr = 'ssb', ssbMinDb = 0, cwMinDb = -15, focusBand = '', enabled = ['20m', '15m'] } = {}) {
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
    `;
}

let circleMarkerCalls;
function installLeafletMock() {
    geoJsonCalls = [];
    circleMarkerCalls = [];
    const tooltipCalls = [];
    globalThis.L = {
        layerGroup: () => ({ addTo: () => ({ __kind: 'layerGroup' }) }),
        geoJSON: vi.fn((fc) => {
            geoJsonCalls.push(fc);
            return { addTo: () => ({ __kind: 'geojson' }) };
        }),
        canvas: () => ({ addTo: () => ({ __kind: 'canvas' }) }),
        circleMarker: (latlng, opts) => {
            circleMarkerCalls.push({ latlng, opts });
            const marker = {
                bindTooltip: vi.fn((html) => { tooltipCalls.push(html); return marker; }),
                on() { return marker; }
            };
            marker.addTo = () => marker;
            marker.tooltipCalls = tooltipCalls;
            return marker;
        },
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
    clearDxClusterMarkers();
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
        // -5 dB -> ramp 0.45 - 0.03*5 = 0.30 (low-ish but honest: the spot is weak).
        expect(feats[0].fillOpacity).toBeCloseTo(0.30, 2);
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
        // must grade from the +1 dB 20m spot alone (ramp 0.465), not from the
        // hidden +25 dB 15m spot (which would score near the ramp cap).
        setupDom({ minSnr: 'ssb', ssbMinDb: 0, focusBand: '20m' });
        const spots = [spot('JO32', 1, '20m'), spot('JO32', 25, '15m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].color).toBe('#008000');
        expect(feats[0].fillOpacity).toBeCloseTo(0.465, 3);
    });

    it('does not let a sub-threshold spot inflate intensity', () => {
        // SSB threshold 0. The -9 dB spot is filtered out; the square grades
        // from the +12 dB spot alone: ramp 0.45 + 0.015*12 = 0.63.
        setupDom({ minSnr: 'ssb', ssbMinDb: 0 });
        const spots = [spot('JO32', 12, '20m'), spot('JO32', -9, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].fillOpacity).toBeCloseTo(0.63, 2);
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

// Grading model (top-quartile mean -> continuous opacity ramp). The core
// property: one lucky strong decode among many weak reports must NOT light
// up the square; corroborated strong paths should.
describe('renderGridSnr grading', () => {
    it('does not highlight a square on a single strong outlier among weak spots', () => {
        setupDom({ minSnr: 'none' });
        const spots = [
            spot('JO32', 12, '20m'),
            ...Array.from({ length: 20 }, () => spot('JO32', -8, '20m'))
        ];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // top quartile = best 6 -> mean(12, -8 x5) ~= -4.67 dB -> ramp ~0.31.
        expect(feats[0].fillOpacity).toBeLessThan(0.35);
    });

    it('reads bright when strong reports are corroborated', () => {
        setupDom({ minSnr: 'none' });
        const spots = [10, 11, 12, 13, 14, 15].map((s) => spot('JO32', s, '20m'));
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // top quartile = best 2 -> mean 14.5 -> ramp ~0.67.
        expect(feats[0].fillOpacity).toBeGreaterThan(0.6);
    });

    it('still gives a lone strong spot visual credit', () => {
        setupDom({ minSnr: 'none' });
        const spots = [spot('JO32', 12, '20m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        // mean of 1 = 12 dB -> 0.45 + 0.015*12 = 0.63.
        expect(feats[0].fillOpacity).toBeCloseTo(0.63, 2);
    });

    it('subdues a lone weak spot on the ramp', () => {
        setupDom({ minSnr: 'none' });
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
        setupDom({ minSnr: 'none', focusBand: '20m' });
        const spots = [spot('JO32', -5, '20m'), spot('JO32', 25, '15m')];
        updateMapVisualization(spots, 15);
        const feats = drawFeatures();
        expect(feats).toHaveLength(1);
        expect(feats[0].fillOpacity).toBeCloseTo(0.30, 2);
    });
});

// DX cluster markers: separate persistent layer (rebuilt only when the
// cluster set changes, so hover tooltips don't flicker on every grid
// rebuild) and tooltips carry the DX/spotter callsigns.
describe('renderGridSnr DX cluster markers', () => {
    function dxSpot() {
        return {
            locator: 'IM59', snr: 0, band: '20m', sourceType: 'dxcluster',
            lat: 38.5, lng: -8.5,
            sender: 'EA1AAA', receiver: 'JA1BBB', reporterLocator: 'JO43'
        };
    }

    it('renders cluster spots as circle markers with callsigns in the tooltip', () => {
        setupDom({ minSnr: 'none' });
        updateMapVisualization([dxSpot()], 15);
        expect(circleMarkerCalls).toHaveLength(1);
        const marker = globalThis.L.circleMarker(circleMarkerCalls[0].latlng, circleMarkerCalls[0].opts);
        expect(marker.tooltipCalls[0]).toContain('DX: JA1BBB');
        expect(marker.tooltipCalls[0]).toContain('Spotter: EA1AAA');
        expect(marker.tooltipCalls[0]).toContain('Spotter Loc: JO43');
        expect(marker.tooltipCalls[0]).toContain('DX Loc: IM59');
    });

    it('does not rebuild the marker layer when only regular spots change', () => {
        setupDom({ minSnr: 'none' });
        updateMapVisualization([dxSpot()], 15);
        const firstLayer = state.dxClusterLayer;
        expect(firstLayer).toBeTruthy();
        updateMapVisualization([dxSpot(), spot('JO32', 5, '20m')], 15);
        expect(state.dxClusterLayer).toBe(firstLayer);
        expect(circleMarkerCalls).toHaveLength(1);
    });
});