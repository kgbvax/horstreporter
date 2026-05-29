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
        removeLayer: vi.fn(),
        eachLayer: vi.fn(),
        getContainer: vi.fn(() => ({ style: {} }))
    };

    mockLayerGroup = {
        addTo: vi.fn(function addTo() { return this; })
    };

    globalThis.L = {
        map: vi.fn(() => mockMap),
        control: {
            zoom: vi.fn(() => ({ addTo: vi.fn() }))
        },
        tileLayer: vi.fn(() => ({ addTo: vi.fn() })),
        layerGroup: vi.fn(() => mockLayerGroup),
        marker: vi.fn(() => ({ addTo: vi.fn() })),
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
        getSubsolarPoint: vi.fn(() => ({ lat: 0, lng: 0 }))
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
