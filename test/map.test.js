import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

let mapModule;
let mockMap;
let mockLayerGroup;
// Captured from the doMock factories by importFreshMapModule.
let mockSelectProminentDxccLabels;

// Mutable knobs read by the utils.js mock factory; reset per test.
const utilsMockState = {
    countryColoring: false,
    grayline: false
};

// Two-country fixture used by the country-layer and label-layer sync tests.
function makeWorldGeoJson() {
    return {
        type: 'FeatureCollection',
        features: [
            {
                type: 'Feature',
                properties: { ISO_A2: 'DE', NAME: 'Germany', ADM0_A3: 'DEU' },
                geometry: { type: 'Polygon', coordinates: [[[5, 47], [15, 47], [15, 55], [5, 55], [5, 47]]] }
            },
            {
                type: 'Feature',
                properties: { ISO_A2: 'IN', NAME: 'India', ADM0_A3: 'IND' },
                geometry: { type: 'Polygon', coordinates: [[[68, 8], [97, 8], [97, 37], [68, 37], [68, 8]]] }
            }
        ]
    };
}

function setupDom() {
    document.body.innerHTML = '<div id="map"></div>';
}

// jsdom here does not expose localStorage; other suites stub it the same way.
function installLocalStorageStub() {
    const store = new Map();
    const stub = {
        getItem: (k) => (store.has(k) ? store.get(k) : null),
        setItem: (k, v) => { store.set(k, String(v)); },
        removeItem: (k) => { store.delete(k); },
        clear: () => { store.clear(); },
        key: (i) => Array.from(store.keys())[i] ?? null,
        get length() { return store.size; }
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: stub });
    return stub;
}

function installLeafletMock() {
    mockMap = {
        remove: vi.fn(),
        setView: vi.fn(function setView() { return this; }),
        getPane: vi.fn(() => null),
        createPane: vi.fn((name) => ({ name, style: {} })),
        on: vi.fn(),
        getCenter: vi.fn(() => ({ lat: 52, lng: 7 })),
        getZoom: vi.fn(() => 2),
        getBounds: vi.fn(() => ({ contains: vi.fn(() => true) })),
        getSize: vi.fn(() => ({ x: 1200, y: 885 })),
        setMinZoom: vi.fn(function setMinZoom() { return this; }),
        setMaxBounds: vi.fn(function setMaxBounds() { return this; }),
        removeLayer: vi.fn(),
        hasLayer: vi.fn(() => false),
        eachLayer: vi.fn(),
        getContainer: vi.fn(() => ({ style: {} }))
    };

    mockLayerGroup = {
        layers: [],
        addTo: vi.fn(function addTo() { return this; }),
        clearLayers: vi.fn(function clearLayers() { this.layers = []; return this; })
    };

    // Leaflet's layer.addTo(map) returns the layer itself — map.js relies on
    // this (e.g. `currentCountryLayer = L.geoJSON(...).addTo(map)`), so the
    // factory mocks must be self-returning too.
    const selfReturningLayer = () => {
        const layer = { addTo: vi.fn(() => layer) };
        return layer;
    };

    globalThis.L = {
        map: vi.fn(() => mockMap),
        control: {
            zoom: vi.fn(() => ({ addTo: vi.fn() }))
        },
        tileLayer: vi.fn(selfReturningLayer),
        layerGroup: vi.fn(() => mockLayerGroup),
        marker: vi.fn(selfReturningLayer),
        divIcon: vi.fn((options) => options),
        geoJSON: vi.fn(selfReturningLayer),
        imageOverlay: vi.fn(selfReturningLayer),
        polyline: vi.fn(selfReturningLayer),
        circleMarker: vi.fn(selfReturningLayer),
        canvas: vi.fn(() => ({ type: 'canvas' })),
        CRS: { EPSG3857: {} }
    };
}

async function importFreshMapModule() {
    vi.resetModules();
    vi.doMock('../static/utils.js', () => ({
        getCountryColoringEnabled: vi.fn(() => utilsMockState.countryColoring),
        getCountryFillForFeature: vi.fn(() => '#000000'),
        getGraylineEnabled: vi.fn(() => utilsMockState.grayline),
        getGraylineOverlayOpacities: vi.fn(() => ({ graylineOpacity: 0, nightOpacity: 0 })),
        getMercatorDxccLabelsEnabled: vi.fn(() => true),
        getSubsolarPoint: vi.fn(() => ({ lat: 0, lng: 0 })),
        greatCirclePoints: vi.fn((aLat, aLng, bLat, bLng, segments = 48) => (
            Array.from({ length: Math.max(1, Math.floor(segments)) + 1 },
                (_, i) => [aLat + ((bLat - aLat) * i) / segments, aLng + ((bLng - aLng) * i) / segments])
        )),
        hexToRgb: vi.fn(() => [1, 2, 3]),
        blendOverlayColors: vi.fn(() => ({ r: 0, g: 0, b: 0, a: 0 })),
        icon: vi.fn(() => 'ICON')
    }));
    vi.doMock('../static/azimuth-runtime.js', () => ({
        selectProminentDxccLabels: vi.fn(() => ([{ lat: 52, lng: 7, prefix: 'DL' }]))
    }));

    mapModule = await import('../static/map.js');
    // The doMock factories run at import time; grab the created mocks.
    mockSelectProminentDxccLabels = vi.mocked((await import('../static/azimuth-runtime.js')).selectProminentDxccLabels);
    return mapModule;
}

describe('map.js mercator dxcc labels', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLeafletMock();
        globalThis.fetch = vi.fn(async () => ({
            ok: true,
            json: async () => ({ type: 'FeatureCollection', features: [] })
        }));
        document.body.setAttribute('data-theme', 'light');
    });

    it('loads mercator dxcc labels without throwing at low zoom levels', async () => {
        await importFreshMapModule();

        mapModule.initMap([52, 7], 2);

        await expect(mapModule.syncMercatorDxccLabelLayer({ force: true, enabled: true })).resolves.toBeUndefined();
        expect(globalThis.fetch).toHaveBeenCalledWith('vendor/world.geojson');
        expect(mockMap.getZoom).toHaveBeenCalled();
    });

    it('constrains min zoom to viewport height and clamps latitude so no empty space can appear', async () => {
        await importFreshMapModule();

        mapModule.initMap([52, 7], 2);

        // minZoom must make the Web-Mercator world (256·2^zoom px) at least as
        // tall as the 885px viewport, so zooming out can never reveal gaps.
        expect(mockMap.setMinZoom).toHaveBeenCalled();
        const minZoom = mockMap.setMinZoom.mock.calls[0][0];
        expect(minZoom).toBeCloseTo(Math.log2(885 / 256) + 1e-3, 5);

        // Latitude is clamped to the projection poles; longitude is left
        // effectively unbounded so east/west panning keeps working.
        expect(mockMap.setMaxBounds).toHaveBeenCalled();
        const bounds = mockMap.setMaxBounds.mock.calls[0][0];
        expect(bounds[0][0]).toBeCloseTo(-85.05112878, 5);
        expect(bounds[1][0]).toBeCloseTo(85.05112878, 5);
        expect(Math.abs(bounds[0][1])).toBeGreaterThan(360);
        expect(Math.abs(bounds[1][1])).toBeGreaterThan(360);

        // The resize hook re-derives the constraint when the container changes.
        expect(mockMap.on).toHaveBeenCalledWith('resize', expect.any(Function));
    });
});

describe('map.js initMap without Leaflet', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        document.body.innerHTML = '<div id="map"></div>';
        delete globalThis.L;
    });

    it('throws a clear error and renders a fallback message when L is missing', async () => {
        const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        await importFreshMapModule();

        expect(() => mapModule.initMap([52, 7], 2)).toThrowError(/Leaflet/);
        const mapEl = document.getElementById('map');
        expect(mapEl.textContent).toMatch(/Leaflet/);
        expect(errSpy).toHaveBeenCalledWith(expect.stringMatching(/Leaflet/));
        errSpy.mockRestore();
    });
});

// --- U6: overlay layers, highlight path, event handlers, theme ---

describe('map.js mercator country layer', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLeafletMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('renders fixture GeoJSON with a themed style and registers the layer on the map', async () => {
        utilsMockState.countryColoring = true;
        globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => makeWorldGeoJson() }));
        await importFreshMapModule();

        mapModule.initMap([52, 7], 2);
        await mapModule.syncMercatorCountryLayer({ enabled: true, force: true });

        expect(globalThis.fetch).toHaveBeenCalledWith('vendor/world.geojson');
        expect(globalThis.L.geoJSON).toHaveBeenCalledTimes(1);
        const [geoJson, opts] = globalThis.L.geoJSON.mock.calls[0];
        expect(geoJson.type).toBe('FeatureCollection');
        expect(geoJson.features).toHaveLength(2);
        expect(opts.pane).toBe('country-fill-pane');
        expect(opts.interactive).toBe(false);
        expect(globalThis.L.canvas).toHaveBeenCalledWith({ pane: 'country-fill-pane' });

        // The style callback is per-feature and theme-dependent.
        const style = opts.style(makeWorldGeoJson().features[0]);
        expect(style.fillColor).toBe('#000000'); // from the getCountryFillForFeature mock
        expect(style.weight).toBe(0.7);
        expect(style.fillOpacity).toBe(0.3); // light theme
        // Stroke is the shared --map-border token (jsdom has no stylesheet, so
        // map-tokens.js serves its light fallback), drawn at full opacity.
        expect(style.color).toBe('rgba(88, 99, 109, 0.5)');
        expect(style.opacity).toBe(1);

        expect(globalThis.L.geoJSON.mock.results[0].value.addTo).toHaveBeenCalledWith(mockMap);
    });

    it('removes the country layer and skips the fetch when disabled', async () => {
        utilsMockState.countryColoring = true;
        globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => makeWorldGeoJson() }));
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        await mapModule.syncMercatorCountryLayer({ enabled: true, force: true });
        const layer = globalThis.L.geoJSON.mock.results[0].value;

        utilsMockState.countryColoring = false;
        await mapModule.syncMercatorCountryLayer({ enabled: false, force: true });

        expect(mockMap.removeLayer).toHaveBeenCalledWith(layer);
        expect(globalThis.fetch).toHaveBeenCalledTimes(1); // no re-fetch for the removal
    });

    it('drops the in-flight layer when a newer sync supersedes the fetch', async () => {
        utilsMockState.countryColoring = true;
        let resolveFetch;
        globalThis.fetch = vi.fn(() => new Promise((resolve) => { resolveFetch = resolve; }));
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        const superseded = mapModule.syncMercatorCountryLayer({ enabled: true, force: true });
        const newer = mapModule.syncMercatorCountryLayer({ enabled: false });
        resolveFetch({ ok: true, json: async () => makeWorldGeoJson() });
        await Promise.all([superseded, newer]);

        // The stale sync must not add a layer the user toggled off meanwhile.
        expect(globalThis.L.geoJSON).not.toHaveBeenCalled();
        expect(mockMap.removeLayer).not.toHaveBeenCalled();
    });
});

describe('map.js chase queue dx highlight overlay', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLeafletMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('draws a pinned great-circle path, marker and label for a finite spot', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorDxHighlight({ originLat: 52, originLng: 7, spotLat: 40, spotLng: -74, label: 'W1AW', pinned: true });

        const group = mockLayerGroup;
        expect(group.addTo).toHaveBeenCalledWith(mockMap);
        expect(group.clearLayers).toHaveBeenCalled();

        // Path: 64 segments over the mocked greatCirclePoints sampler.
        expect(globalThis.L.polyline).toHaveBeenCalledTimes(1);
        const [pts, pathOpts] = globalThis.L.polyline.mock.calls[0];
        expect(pts).toHaveLength(65);
        expect(pts[0]).toEqual([52, 7]);
        expect(pts[pts.length - 1]).toEqual([40, -74]);
        expect(pathOpts.pane).toBe('dx-highlight-pane');
        expect(pathOpts.weight).toBe(3); // pinned
        expect(pathOpts.dashArray).toBeNull(); // solid when pinned

        expect(globalThis.L.circleMarker).toHaveBeenCalledTimes(1);
        const [markerLatLng, markerOpts] = globalThis.L.circleMarker.mock.calls[0];
        expect(markerLatLng).toEqual([40, -74]);
        expect(markerOpts.radius).toBe(7); // pinned radius
        expect(markerOpts.pane).toBe('dx-highlight-pane');

        expect(globalThis.L.marker).toHaveBeenCalledTimes(1);
        const [, labelOpts] = globalThis.L.marker.mock.calls[0];
        expect(labelOpts.icon.html).toContain('W1AW');
    });

    it('draws a dashed thinner path when not pinned', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorDxHighlight({ originLat: 52, originLng: 7, spotLat: 40, spotLng: -74, pinned: false });

        const [, pathOpts] = globalThis.L.polyline.mock.calls[0];
        expect(pathOpts.weight).toBe(2);
        expect(pathOpts.dashArray).toBe('3 6');
        const [, markerOpts] = globalThis.L.circleMarker.mock.calls[0];
        expect(markerOpts.radius).toBe(6);
        // No label when the callsign label is omitted.
        expect(globalThis.L.marker).not.toHaveBeenCalled();
    });

    it('clears the layer and adds nothing for missing spot coordinates', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorDxHighlight({});

        expect(mockLayerGroup.clearLayers).toHaveBeenCalled();
        expect(globalThis.L.polyline).not.toHaveBeenCalled();
        expect(globalThis.L.circleMarker).not.toHaveBeenCalled();
        expect(globalThis.L.marker).not.toHaveBeenCalled();
    });

    it('skips the path but still marks the spot when the origin is not finite', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorDxHighlight({ originLat: null, spotLat: 40, spotLng: -74 });

        expect(globalThis.L.polyline).not.toHaveBeenCalled();
        expect(globalThis.L.circleMarker).toHaveBeenCalledTimes(1);
    });

    it('clearMercatorDxHighlight clears layers without recreating the group', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorDxHighlight({ spotLat: 40, spotLng: -74 });
        mapModule.clearMercatorDxHighlight();

        expect(mockLayerGroup.clearLayers).toHaveBeenCalledTimes(2);
        expect(globalThis.L.polyline).not.toHaveBeenCalled();
    });
});

describe('map.js dxcc label layer gating and markers', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLeafletMock();
        globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => makeWorldGeoJson() }));
        document.body.setAttribute('data-theme', 'light');
    });

    it('creates one pane-scoped marker per selected label inside the bounds', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        await mapModule.syncMercatorDxccLabelLayer({ force: true, enabled: true });

        expect(mockSelectProminentDxccLabels).toHaveBeenCalledTimes(1);
        expect(globalThis.L.marker).toHaveBeenCalledTimes(1);
        const [latlng, opts] = globalThis.L.marker.mock.calls[0];
        expect(latlng).toEqual([52, 7]);
        expect(opts.pane).toBe('dxcc-label-pane');
        expect(opts.interactive).toBe(false);
        expect(opts.keyboard).toBe(false);
        expect(opts.icon.html).toContain('DL');
        // Colors come from the --dxcc-label-* tokens via the class, not inline.
        expect(opts.icon.html).toBe('<span class="dxcc-entity-label">DL</span>');
        expect(mockLayerGroup.addTo).toHaveBeenCalledWith(mockMap);
    });

    it('adds no markers when the label falls outside the visible bounds', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);
        mockMap.getBounds = vi.fn(() => ({ contains: vi.fn(() => false) }));

        await mapModule.syncMercatorDxccLabelLayer({ force: true, enabled: true });

        expect(globalThis.L.marker).not.toHaveBeenCalled();
        expect(mockLayerGroup.addTo).not.toHaveBeenCalled();
    });

    it('passes show-all limits at zoom >= 5', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);
        mockMap.getZoom = vi.fn(() => 5.2);

        await mapModule.syncMercatorDxccLabelLayer({ force: true, enabled: true });

        const [, , options] = mockSelectProminentDxccLabels.mock.calls[0];
        expect(options.maxLabels).toBe(2000);
        expect(options.minDistanceKm).toBe(0);
    });

    it('removes the label layer without fetching when disabled', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        await mapModule.syncMercatorDxccLabelLayer({ enabled: false });

        expect(globalThis.fetch).not.toHaveBeenCalled();
        expect(globalThis.L.marker).not.toHaveBeenCalled();
        // Defensive orphan cleanup still runs against the map.
        expect(mockMap.eachLayer).toHaveBeenCalled();
    });

    it('removes the label layer when the active projection is not mercator', async () => {
        const input = document.createElement('input');
        input.type = 'radio';
        input.name = 'projection-select';
        input.value = 'azimuthal';
        input.checked = true;
        document.body.appendChild(input);

        try {
            await importFreshMapModule();
            mapModule.initMap([52, 7], 2);

            await mapModule.syncMercatorDxccLabelLayer({ force: true, enabled: true });

            expect(globalThis.fetch).not.toHaveBeenCalled();
            expect(globalThis.L.marker).not.toHaveBeenCalled();
        } finally {
            input.remove();
        }
    });
});

describe('map.js event handlers and view constraints', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLocalStorageStub();
        installLeafletMock();
        globalThis.fetch = vi.fn(async () => ({ ok: true, json: async () => makeWorldGeoJson() }));
        document.body.setAttribute('data-theme', 'light');
    });

    it('persists the view on moveend/zoomend and re-derives constraints on resize', async () => {
        vi.useFakeTimers();
        try {
            await importFreshMapModule();
            mapModule.initMap([52, 7], 2);

            const handlers = Object.fromEntries(mockMap.on.mock.calls.map(([event, fn]) => [event, fn]));
            expect(Object.keys(handlers)).toEqual(expect.arrayContaining(['moveend', 'zoomend', 'resize']));

            handlers.moveend();
            handlers.zoomend();
            expect(JSON.parse(localStorage.getItem('mapCenter'))).toEqual([52, 7]);
            expect(localStorage.getItem('mapZoom')).toBe('2');

            // The resize hook re-derives the min zoom from the current size.
            const callsBefore = mockMap.setMinZoom.mock.calls.length;
            handlers.resize();
            expect(mockMap.setMinZoom.mock.calls.length).toBeGreaterThan(callsBefore);
            expect(mockMap.setMaxBounds).toHaveBeenCalled();
        } finally {
            vi.useRealTimers();
        }
    });

    it('re-initializing the map removes the previous map instance', async () => {
        await importFreshMapModule();

        mapModule.initMap([52, 7], 2);
        mapModule.initMap([48, 11], 3);

        expect(globalThis.L.map).toHaveBeenCalledTimes(2);
        expect(mockMap.remove).toHaveBeenCalledTimes(1);
        expect(mockMap.setView).toHaveBeenLastCalledWith([48, 11], 3);
    });
});

describe('map.js theme switch', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLocalStorageStub();
        installLeafletMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('swaps the tile layer, persists the choice, and updates the toggle button', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        const toggle = document.createElement('button');
        toggle.id = 'theme-toggle';
        document.body.appendChild(toggle);
        try {
            mapModule.setTheme('dark');

            expect(document.body.getAttribute('data-theme')).toBe('dark');
            expect(localStorage.getItem('theme')).toBe('dark');
            expect(globalThis.L.tileLayer).toHaveBeenCalledWith(
                expect.stringContaining('dark_all'),
                expect.any(Object)
            );
            expect(mockMap.removeLayer).not.toHaveBeenCalled(); // no layers existed yet
            expect(globalThis.L.tileLayer.mock.results[0].value.addTo).toHaveBeenCalledWith(mockMap);
            expect(toggle.innerHTML).toBe('ICON'); // mocked utils icon
        } finally {
            toggle.remove();
        }
    });

    it('removes existing overlay layers when switching theme', async () => {
        utilsMockState.countryColoring = true;
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        await mapModule.syncMercatorCountryLayer({ enabled: true, force: true });
        const layer = globalThis.L.geoJSON.mock.results[0].value;

        mapModule.setTheme('dark');

        expect(mockMap.removeLayer).toHaveBeenCalledWith(layer);
    });
});

describe('map.js grayline overlay degradation', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = false;
        setupDom();
        installLeafletMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('adds no overlay and does not throw when the 2D context is unavailable', async () => {
        utilsMockState.grayline = true;
        const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        // jsdom has no canvas 2D implementation: buildMercatorGraylineDataUrl
        // must degrade to null and the sync must bail out quietly.
        await expect(mapModule.syncMercatorGraylineLayer({ enabled: true, force: true })).resolves.toBeUndefined();

        expect(globalThis.L.layerGroup).not.toHaveBeenCalled();
        expect(globalThis.L.imageOverlay).not.toHaveBeenCalled();
        expect(mockMap.removeLayer).not.toHaveBeenCalled();
        errSpy.mockRestore();
    });

    it('removes an existing grayline layer when disabled', async () => {
        utilsMockState.grayline = true;
        // Provide a 2D-context-less canvas; the overlay cannot be built, so set
        // up the layer bookkeeping by disabling right after an attempted sync.
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        utilsMockState.grayline = false;
        await expect(mapModule.syncMercatorGraylineLayer({ enabled: false })).resolves.toBeUndefined();

        expect(globalThis.L.layerGroup).not.toHaveBeenCalled();
        expect(globalThis.L.imageOverlay).not.toHaveBeenCalled();
    });
});

describe('map.js grayline clock basis (plan 2026-09-11-002 U5, KTD-9)', () => {
    // jsdom has no canvas 2D context; a minimal stub lets
    // buildMercatorGraylineDataUrl succeed so the bucket/cache-key derivation
    // is observable through getSubsolarPoint + L.imageOverlay.
    function installCanvasStub() {
        const fakeCtx = {
            createImageData: (w, h) => ({ data: new Uint8ClampedArray(w * h * 4) }),
            putImageData: vi.fn()
        };
        vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(fakeCtx);
        vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,GRAYLINE');
    }

    const WALL_MS = 1_730_000_000_000;       // wall bucket floor(…/300000) = 5766666
    const PLAYHEAD_MS = 1_700_000_123_000;   // override bucket floor(…/300000) = 5666666
    const BUCKET_MS = 5 * 60 * 1000;

    beforeEach(() => {
        vi.clearAllMocks();
        utilsMockState.countryColoring = false;
        utilsMockState.grayline = true;
        setupDom();
        installLeafletMock();
        installCanvasStub();
        document.body.setAttribute('data-theme', 'light');
    });

    afterEach(async () => {
        const { clearDataNowOverride } = await import('../static/data-now.js');
        clearDataNowOverride();
        vi.useRealTimers();
    });

    it('derives the bucket from the dataNow() clock, not Date.now()', async () => {
        vi.useFakeTimers({ now: WALL_MS });
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);
        const { setDataNowMs } = await import('../static/data-now.js');
        const mockGetSubsolarPoint = vi.mocked((await import('../static/utils.js')).getSubsolarPoint);

        setDataNowMs(PLAYHEAD_MS);
        await mapModule.syncMercatorGraylineLayer({ enabled: true, force: true });

        // The subsolar point must come from the overridden (playhead) bucket;
        // a Date.now()-keyed bucket would differ here.
        const expectedBucketStart = new Date(Math.floor(PLAYHEAD_MS / BUCKET_MS) * BUCKET_MS);
        expect(mockGetSubsolarPoint).toHaveBeenCalledWith(expectedBucketStart);
    });

    it('falls back to the wall-clock bucket when no override is set (live mode)', async () => {
        vi.useFakeTimers({ now: WALL_MS });
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);
        const mockGetSubsolarPoint = vi.mocked((await import('../static/utils.js')).getSubsolarPoint);

        await mapModule.syncMercatorGraylineLayer({ enabled: true, force: true });

        const expectedBucketStart = new Date(Math.floor(WALL_MS / BUCKET_MS) * BUCKET_MS);
        expect(mockGetSubsolarPoint).toHaveBeenCalledWith(expectedBucketStart);
    });

    it('does not rebuild the overlay within a clock bucket, rebuilds on crossing', async () => {
        vi.useFakeTimers({ now: WALL_MS });
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);
        const { setDataNowMs } = await import('../static/data-now.js');

        setDataNowMs(PLAYHEAD_MS);
        await mapModule.syncMercatorGraylineLayer({ enabled: true, force: true });
        const built = globalThis.L.imageOverlay.mock.calls.length;

        // Within the same 5-minute bucket: key dedupe, no rebuild.
        setDataNowMs(PLAYHEAD_MS + 60_000);
        await mapModule.syncMercatorGraylineLayer();
        expect(globalThis.L.imageOverlay.mock.calls.length).toBe(built);

        // Crossing into the next bucket: rebuild happens.
        setDataNowMs(PLAYHEAD_MS + BUCKET_MS + 60_000);
        await mapModule.syncMercatorGraylineLayer();
        expect(globalThis.L.imageOverlay.mock.calls.length).toBeGreaterThan(built);
    });
});

afterEach(() => {
    delete globalThis.L;
    vi.restoreAllMocks();
});