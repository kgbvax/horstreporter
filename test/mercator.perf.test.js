import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mockMap } = vi.hoisted(() => ({
    mockMap: {
        removeLayer: vi.fn(),
        fitBounds: vi.fn(),
        hasLayer: vi.fn(() => false)
    }
}));

vi.mock('../static/map.js', () => ({
    map: mockMap
}));

import { state } from '../static/state.js';
import { getPerfSnapshot, resetPerfMetrics } from '../static/perf.js';
import { updateMapVisualization } from '../static/renderers.js';
import { writePerfScenarioReport } from './perf-report.js';

function setupDom(style = 'grid-snr') {
    document.body.innerHTML = `
        <input id="qth" value="JO32" />
        <input id="ssb-min-db" value="0" />
        <input id="cw-min-db" value="-15" />
        <input id="auto-zoom" type="checkbox" />

        <div class="band-wrapper"><input type="radio" name="band" value="all" checked /></div>
        <div class="band-wrapper"><input type="radio" name="band" value="20m" /></div>
        <div class="band-wrapper"><input type="radio" name="band" value="40m" /></div>
        <input type="checkbox" class="band-enable" value="20m" checked />
        <input type="checkbox" class="band-enable" value="40m" checked />

        <input type="radio" name="min-snr" value="none" checked />
        <input type="radio" name="style-select" value="grid-snr" ${style === 'grid-snr' ? 'checked' : ''} />
        <input type="radio" name="style-select" value="active-area" ${style === 'active-area' ? 'checked' : ''} />
    `;
}

function createSpots(count) {
    const spots = [];
    const locators = ['JO32', 'JN58', 'IO91', 'FN20', 'EM73', 'PM95'];
    for (let i = 0; i < count; i += 1) {
        spots.push({
            locator: locators[i % locators.length],
            snr: (i % 35) - 10,
            band: i % 2 === 0 ? '20m' : '40m',
            lat: -60 + (i % 120),
            lng: -170 + (i % 340)
        });
    }
    return spots;
}

beforeEach(() => {
    globalThis.__HORST_PERF_TEST__ = true;
    resetPerfMetrics();
    mockMap.removeLayer.mockReset();
    mockMap.fitBounds.mockReset();
    state.heatLayer = null;

    globalThis.L = {
        layerGroup: () => ({
            addTo: () => ({
                __kind: 'layerGroup'
            })
        }),
        rectangle: () => ({
            addTo: () => ({
                __kind: 'rect'
            })
        }),
        heatLayer: () => ({
            addTo: () => ({
                __kind: 'heat'
            })
        }),
            circleMarker: () => ({
                addTo: () => ({
                    __kind: 'circle'
                })
            }),
            geoJSON: () => ({
                addTo: () => ({
                    __kind: 'geojson'
                })
            }),
            canvas: () => ({
                addTo: () => ({ __kind: 'canvas' })
            }),
        latLngBounds: () => ({
            getCenter: () => ({
                toBounds: () => ({})
            }),
            extend: () => {}
        })
    };

    globalThis.turf = {
        point: (coords) => ({ geometry: { coordinates: coords } }),
        featureCollection: (features) => ({ features }),
        clustersDbscan: (fc) => ({ features: fc.features.map((f) => ({ ...f, properties: {} })) }),
        featureEach: (fc, cb) => {
            (fc.features || []).forEach((f) => cb(f));
        },
        concave: () => null,
        convex: () => null,
        polygonSmooth: (poly) => poly
    };
});

describe('mercator perf harness', () => {
    it('profiles grid draw path and stays inside baseline budget', () => {
        setupDom('grid-snr');
        const spots = createSpots(7000);

        for (let i = 0; i < 2; i += 1) {
            updateMapVisualization(spots, 15);
        }

        const snapshot = getPerfSnapshot();
        writePerfScenarioReport('mercator-grid-medium', {
            metrics: snapshot.metrics,
            counters: snapshot.counters,
            metadata: {
                spots: 7000,
                style: 'grid-snr',
                iterations: 2
            }
        });
        expect(snapshot.metrics['mercator.grid.total_ms']).toBeDefined();
        expect(snapshot.metrics['mercator.render.total_ms']).toBeDefined();
        expect(snapshot.metrics['mercator.grid.total_ms'].p95Ms).toBeLessThan(900);
        expect(snapshot.metrics['mercator.render.total_ms'].p95Ms).toBeLessThan(1200);
        expect(snapshot.counters['mercator.grid.rectangles_added']).toBeGreaterThan(0);
    });

    it('profiles style switching burst and tracks mutation counters', async () => {
        const spots = createSpots(4500);

        setupDom('grid-snr');
        for (let i = 0; i < 6; i += 1) {
            const style = i % 2 === 0 ? 'grid-snr' : 'active-area';
            document.querySelectorAll('input[name="style-select"]').forEach((radio) => {
                radio.checked = radio.value === style;
            });
            updateMapVisualization(spots, 15);
            // The active-area draw runs in an ensureTurf().then() continuation
            // (turf is lazy-loaded); drain the macrotask so its metrics and
            // counters land before the snapshot below. The turf global is
            // pre-seeded above, so this is one microtask drain.
            await new Promise((resolve) => setTimeout(resolve, 0));
        }

        const snapshot = getPerfSnapshot();
        writePerfScenarioReport('mercator-style-burst', {
            metrics: snapshot.metrics,
            counters: snapshot.counters,
            metadata: {
                spots: 4500,
                styles: ['grid-snr', 'active-area'],
                iterations: 6
            }
        });
        expect(snapshot.metrics['mercator.render.total_ms'].medianMs).toBeLessThan(900);
        // The rendered-state fingerprint (grid-snr) + active-area rebuild
        // debounce skip redundant rebuilds: the same spot set only builds each
        // style's layer once, so a style-switch burst no longer tears down and
        // recreates the layer on every iteration.
        expect(snapshot.counters['mercator.layers.added']).toBeGreaterThanOrEqual(2);
        expect(snapshot.counters['mercator.layers.removed']).toBeGreaterThanOrEqual(1);
        expect(snapshot.counters['mercator.render.style.grid_snr']).toBeGreaterThanOrEqual(1);
        expect(snapshot.counters['mercator.render.style.active_area']).toBeGreaterThanOrEqual(1);
        // The async continuation is part of the measured window: the drain
        // above must have let the hull draw actually run (the stubbed turf
        // returns no hulls, so the points land as markers instead).
        expect(snapshot.metrics['mercator.active_area.total_ms']).toBeDefined();
        expect(snapshot.metrics['mercator.active_area.cluster_draw_ms']).toBeDefined();
        expect(snapshot.counters['mercator.active_area.markers_added']).toBeGreaterThan(0);
    });
});
