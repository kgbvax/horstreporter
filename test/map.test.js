import { beforeEach, describe, expect, it, vi } from 'vitest';

let mapModule;
let mockMap;
let mockLayerGroup;

function setupDom() {
    document.body.innerHTML = '<div id="map"></div>';
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
        eachLayer: vi.fn(),
        getContainer: vi.fn(() => ({ style: {} }))
    };

    mockLayerGroup = {
        addTo: vi.fn(function addTo() { return this; }),
        clearLayers: vi.fn()
    };

    globalThis.L = {
        map: vi.fn(() => mockMap),
        control: {
            zoom: vi.fn(() => ({ addTo: vi.fn() }))
        },
        tileLayer: vi.fn(() => ({ addTo: vi.fn() })),
        layerGroup: vi.fn(() => mockLayerGroup),
        marker: vi.fn(() => ({ addTo: vi.fn() })),
        polygon: vi.fn(() => ({ addTo: vi.fn() })),
        polyline: vi.fn(() => ({ addTo: vi.fn() })),
        divIcon: vi.fn((options) => options),
        geoJSON: vi.fn(() => ({ addTo: vi.fn() })),
        imageOverlay: vi.fn(() => ({ addTo: vi.fn() })),
        CRS: { EPSG3857: {} }
    };
}

async function importFreshMapModule() {
    vi.resetModules();
    vi.doMock('../static/utils.js', () => ({
        getCountryColoringEnabled: vi.fn(() => false),
        getCountryFillForFeature: vi.fn(() => '#000000'),
        getGraylineEnabled: vi.fn(() => false),
        getGraylineOverlayOpacities: vi.fn(() => ({ graylineOpacity: 0, nightOpacity: 0 })),
        getMercatorDxccLabelsEnabled: vi.fn(() => true),
        getSubsolarPoint: vi.fn(() => ({ lat: 0, lng: 0 })),
        greatCirclePoints: vi.fn(() => [[52, 7], [40, -74]]),
        destinationPoint: vi.fn((lat, lng, bearing, dist) => [lat + dist / 111, lng + bearing / 100]),
        hexToRgb: vi.fn(() => [0, 0, 0]),
        blendOverlayColors: vi.fn((base) => base)
    }));
    vi.doMock('../static/azimuth-runtime.js', () => ({
        selectProminentDxccLabels: vi.fn(() => ([{ lat: 52, lng: 7, prefix: 'DL' }]))
    }));

    mapModule = await import('../static/map.js');
}

describe('map.js mercator dxcc labels', () => {
    beforeEach(() => {
        vi.clearAllMocks();
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

describe('map.js mercator antenna overlay', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        setupDom();
        installLeafletMock();
        document.body.setAttribute('data-theme', 'light');
    });

    it('draws one cone (polygon + two side edges) for a forward beam', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorAntennaOverlay({
            enabled: true,
            stationLat: 52,
            stationLng: 7,
            azimuthDeg: 90,
            beamwidth3dBDeg: 60,
            mode: 'forward'
        });

        expect(globalThis.L.polygon).toHaveBeenCalledTimes(1);
        expect(globalThis.L.polyline).toHaveBeenCalledTimes(2); // left + right edge
    });

    it('draws two cones for bidirectional', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorAntennaOverlay({
            enabled: true,
            stationLat: 52,
            stationLng: 7,
            azimuthDeg: 90,
            beamwidth3dBDeg: 60,
            mode: 'bidirectional'
        });

        expect(globalThis.L.polygon).toHaveBeenCalledTimes(2);
        expect(globalThis.L.polyline).toHaveBeenCalledTimes(4);
    });

    it('clears the layer and draws nothing when disabled', async () => {
        await importFreshMapModule();
        mapModule.initMap([52, 7], 2);

        mapModule.setMercatorAntennaOverlay({ enabled: false });

        expect(globalThis.L.polygon).not.toHaveBeenCalled();
        expect(mockLayerGroup.clearLayers).toHaveBeenCalled();
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
