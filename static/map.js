export let map;
export let currentTileLayer = null;
export let currentGeoJsonLayer = null;
export let currentCountryLayer = null;
export let currentGraylineLayer = null;
export let currentDxccLabelLayer = null;

import { getCountryColoringEnabled, getCountryFillForFeature, getGraylineEnabled, getGraylineOverlayOpacities, getMercatorDxccLabelsEnabled, getSubsolarPoint } from './utils.js';
import { selectProminentDxccLabels } from './azimuth-runtime.js';
import { endPerfTimer, incrementPerfCounter, startPerfTimer } from './perf.js';

let worldGeoJsonData = null;
let worldGeoJsonPromise = null;
let currentCountryLayerTheme = null;
let currentGraylineLayerKey = null;
let currentDxccLabelLayerKey = null;
let dxccSyncRevision = 0;
const WEB_MERCATOR_MAX_LAT = 85.05112878;
const GRAYLINE_BUCKET_MS = 5 * 60 * 1000;
const DXCC_SHOW_ALL_ZOOM_THRESHOLD = 5.0;
const graylineOverlayCache = {
    key: '',
    dataUrl: null
};

// Tile layers are created lazily so this module can be parsed even if the
// Leaflet global `L` is not yet available at module-evaluation time.
let lightTileLayer = null;
let darkTileLayer = null;

function getLightTileLayer() {
    if (!lightTileLayer) {
        lightTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
            maxZoom: 18,
            attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
        });
    }
    return lightTileLayer;
}

function getDarkTileLayer() {
    if (!darkTileLayer) {
        darkTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
            maxZoom: 18,
            attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
        });
    }
    return darkTileLayer;
}

export function initMap(initialCenter, initialZoom) {
    if (typeof L === 'undefined' || !L || typeof L.map !== 'function') {
        const msg = 'Leaflet (window.L) is not available. Verify vendor/js/leaflet.js loads before app.js.';
        console.error(msg);
        const container = document.getElementById('map');
        if (container) {
            container.textContent = msg;
            container.style.padding = '1em';
            container.style.color = '#c00';
        }
        throw new Error(msg);
    }

    if (map) {
        map.remove();
        map = null;
    }

    map = L.map('map', {
        zoomSnap: 0.25,
        zoomDelta: 0.25,
        zoomControl: false,
        crs: L.CRS.EPSG3857,
        worldCopyJump: true
    }).setView(initialCenter, initialZoom);

    if (!map.getPane('country-fill-pane')) {
        const pane = map.createPane('country-fill-pane');
        pane.style.zIndex = '350';
        pane.style.pointerEvents = 'none';
    }
    if (!map.getPane('grayline-pane')) {
        const pane = map.createPane('grayline-pane');
        pane.style.zIndex = '360';
        pane.style.pointerEvents = 'none';
    }
    if (!map.getPane('dxcc-label-pane')) {
        const pane = map.createPane('dxcc-label-pane');
        pane.style.zIndex = '370';
        pane.style.pointerEvents = 'none';
    }

    L.control.zoom({ position: 'bottomright' }).addTo(map);

    map.on('moveend', () => {
        const center = map.getCenter();
        localStorage.setItem('mapCenter', JSON.stringify([center.lat, center.lng]));
        void syncMercatorDxccLabelLayer();
    });

    map.on('zoomend', () => {
        localStorage.setItem('mapZoom', map.getZoom());
        void syncMercatorDxccLabelLayer();
    });

    return map;
}

async function loadWorldGeoJson() {
    if (worldGeoJsonData) return worldGeoJsonData;
    if (!worldGeoJsonPromise) {
        worldGeoJsonPromise = fetch('vendor/world.geojson')
            .then(resp => {
                if (!resp.ok) {
                    throw new Error(`Failed to load world.geojson: ${resp.status}`);
                }
                return resp.json();
            })
            .then(data => {
                worldGeoJsonData = data;
                return data;
            });
    }
    return worldGeoJsonPromise;
}

function removeCountryLayer() {
    if (currentCountryLayer && map) {
        map.removeLayer(currentCountryLayer);
        currentCountryLayer = null;
    }
    currentCountryLayerTheme = null;
}

function removeGraylineLayer() {
    if (currentGraylineLayer && map) {
        map.removeLayer(currentGraylineLayer);
        currentGraylineLayer = null;
    }
    currentGraylineLayerKey = null;
}

function removeDxccLabelLayer() {
    if (currentDxccLabelLayer && map) {
        map.removeLayer(currentDxccLabelLayer);
        currentDxccLabelLayer = null;
    }

    // Defensive cleanup for any orphaned DXCC markers/layers created by overlapping async sync calls.
    if (map) {
        map.eachLayer((layer) => {
            const pane = layer?.options?.pane;
            if (pane === 'dxcc-label-pane' && layer !== currentDxccLabelLayer) {
                map.removeLayer(layer);
            }
        });

        const dxccPane = map.getPane('dxcc-label-pane');
        if (dxccPane) {
            dxccPane.replaceChildren();
        }
    }

    currentDxccLabelLayerKey = null;
}

function hexToRgb(hex) {
    const value = String(hex || '').replace('#', '');
    if (value.length !== 6) return [0, 0, 0];
    return [
        Number.parseInt(value.slice(0, 2), 16),
        Number.parseInt(value.slice(2, 4), 16),
        Number.parseInt(value.slice(4, 6), 16)
    ];
}

function blendOverlayColors(base, color, alpha) {
    if (alpha <= 0) return base;
    const nextAlpha = base.a + (alpha * (1 - base.a));
    if (nextAlpha <= 0) return base;
    return {
        r: ((base.r * base.a) + (color[0] * alpha * (1 - base.a))) / nextAlpha,
        g: ((base.g * base.a) + (color[1] * alpha * (1 - base.a))) / nextAlpha,
        b: ((base.b * base.a) + (color[2] * alpha * (1 - base.a))) / nextAlpha,
        a: nextAlpha
    };
}

function mercatorYToLat(yRatio) {
    return (Math.atan(Math.sinh(Math.PI * (1 - (2 * yRatio))))) * 180 / Math.PI;
}

function buildMercatorGraylineDataUrl(theme, subsolarPoint) {
    const width = 1024;
    const height = 512;
    const canvas = document.createElement('canvas');
    canvas.width = width;
    canvas.height = height;

    const ctx = canvas.getContext('2d');
    if (!ctx) return null;

    const twilightFill = hexToRgb(theme === 'dark' ? '#9a8371' : '#b08b72');
    const nightFill = hexToRgb(theme === 'dark' ? '#01050a' : '#182534');
    const imageData = ctx.createImageData(width, height);
    const data = imageData.data;

    for (let y = 0; y < height; y += 1) {
        const lat = mercatorYToLat(y / (height - 1));
        for (let x = 0; x < width; x += 1) {
            const lng = -180 + ((x / (width - 1)) * 360);
            const { graylineOpacity, nightOpacity } = getGraylineOverlayOpacities(lat, lng, subsolarPoint);
            if (graylineOpacity <= 0 && nightOpacity <= 0) continue;

            let pixel = { r: 0, g: 0, b: 0, a: 0 };
            pixel = blendOverlayColors(pixel, twilightFill, graylineOpacity);
            pixel = blendOverlayColors(pixel, nightFill, nightOpacity);

            const offset = (y * width * 4) + (x * 4);
            data[offset] = Math.round(pixel.r);
            data[offset + 1] = Math.round(pixel.g);
            data[offset + 2] = Math.round(pixel.b);
            data[offset + 3] = Math.round(pixel.a * 255);
        }
    }

    ctx.putImageData(imageData, 0, 0);
    return canvas.toDataURL('image/png');
}

export async function syncMercatorCountryLayer(options = {}) {
    if (!map) return;

    const enabled = options.enabled ?? getCountryColoringEnabled();
    const force = options.force === true;
    const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';

    if (!enabled) {
        removeCountryLayer();
        return;
    }

    if (!force && currentCountryLayer && currentCountryLayerTheme === theme) {
        return;
    }

    removeCountryLayer();

    const geoJson = await loadWorldGeoJson();
    currentCountryLayer = L.geoJSON(geoJson, {
        pane: 'country-fill-pane',
        interactive: false,
        style: (feature) => ({
            color: theme === 'dark' ? '#2a3845' : '#58636d',
            weight: 0.7,
            opacity: theme === 'dark' ? 0.7 : 0.5,
            fillColor: getCountryFillForFeature(feature, theme),
            fillOpacity: theme === 'dark' ? 0.42 : 0.30
        })
    }).addTo(map);
    currentCountryLayerTheme = theme;
}

export async function syncMercatorGraylineLayer(options = {}) {
    if (!map) return;

    const enabled = options.enabled ?? getGraylineEnabled();
    const force = options.force === true;
    const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';

    if (!enabled) {
        removeGraylineLayer();
        return;
    }

    const bucket = Math.floor(Date.now() / GRAYLINE_BUCKET_MS);
    const key = `${theme}:${bucket}`;
    if (!force && currentGraylineLayer && currentGraylineLayerKey === key) {
        return;
    }

    removeGraylineLayer();

    let dataUrl = graylineOverlayCache.dataUrl;
    if (graylineOverlayCache.key !== key || !dataUrl) {
        const subsolarPoint = getSubsolarPoint(new Date(bucket * GRAYLINE_BUCKET_MS));
        dataUrl = buildMercatorGraylineDataUrl(theme, subsolarPoint);
        graylineOverlayCache.key = key;
        graylineOverlayCache.dataUrl = dataUrl;
    }

    if (!dataUrl) return;

    currentGraylineLayer = L.imageOverlay(dataUrl, [[-WEB_MERCATOR_MAX_LAT, -180], [WEB_MERCATOR_MAX_LAT, 180]], {
        pane: 'grayline-pane',
        interactive: false,
        opacity: 1
    }).addTo(map);
    currentGraylineLayerKey = key;
}

function currentProjection() {
    return document.querySelector('input[name="projection-select"]:checked')?.value || 'mercator';
}

function buildDxccLabelIcon(label, theme) {
    const textColor = theme === 'dark' ? '#f3f6fb' : '#263745';
    const bgColor = theme === 'dark' ? 'rgba(18, 28, 38, 0.84)' : 'rgba(255, 255, 255, 0.84)';
    const borderColor = theme === 'dark' ? 'rgba(216, 226, 236, 0.28)' : 'rgba(70, 86, 98, 0.30)';

    return L.divIcon({
        className: 'dxcc-entity-marker',
        html: `<span class="dxcc-entity-label" style="color:${textColor};background:${bgColor};border-color:${borderColor};">${label.prefix}</span>`,
        iconSize: [0, 0],
        iconAnchor: [0, 0]
    });
}

function mercatorDxccLabelLimits(zoom) {
    if (zoom >= DXCC_SHOW_ALL_ZOOM_THRESHOLD) {
        return { maxLabels: 2000, minDistanceKm: 0 };
    }
    const rel_scale = 0.75;
    if (zoom <= 2) return { maxLabels: 28, minDistanceKm: Math.round(1500 * rel_scale) };
    if (zoom <= 3) return { maxLabels: 44, minDistanceKm: Math.round(1100 * rel_scale) };
    if (zoom <= 4) return { maxLabels: 62, minDistanceKm: Math.round(800 * rel_scale) };
    if (zoom <= 5) return { maxLabels: 84, minDistanceKm: Math.round(560 * rel_scale) };
    return { maxLabels: 110, minDistanceKm: Math.round(200 * rel_scale) };
}

export async function syncMercatorDxccLabelLayer(options = {}) {
    if (!map) return;

    const syncTimer = startPerfTimer();

    const revision = ++dxccSyncRevision;

    const enabled = options.enabled ?? getMercatorDxccLabelsEnabled();
    const force = options.force === true;
    const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    const projection = currentProjection();

    if (!enabled || projection !== 'mercator') {
        removeDxccLabelLayer();
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }

    const center = map.getCenter();
    const zoom = map.getZoom();
    const bounds = map.getBounds();
    const key = `${theme}:${zoom.toFixed(2)}:${center.lat.toFixed(2)}:${center.lng.toFixed(2)}`;

    if (!force && currentDxccLabelLayer && currentDxccLabelLayerKey === key) {
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }

    removeDxccLabelLayer();

    const loadTimer = startPerfTimer();
    const geoJson = await loadWorldGeoJson();
    endPerfTimer('mercator.dxcc.geojson_load_ms', loadTimer);
    if (revision !== dxccSyncRevision) {
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }
    if (!geoJson?.features?.length) {
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }

    const { maxLabels, minDistanceKm } = mercatorDxccLabelLimits(zoom);
    const selectionTimer = startPerfTimer();
    const labels = selectProminentDxccLabels(geoJson, [center.lat, center.lng], { maxLabels, minDistanceKm, includeSupplemental: true })
        .filter(label => bounds.contains([label.lat, label.lng]));
    endPerfTimer('mercator.dxcc.select_labels_ms', selectionTimer);

    if (!labels.length) {
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }

    currentDxccLabelLayer = L.layerGroup([], { pane: 'dxcc-label-pane' });
    labels.forEach(label => {
        L.marker([label.lat, label.lng], {
            pane: 'dxcc-label-pane',
            interactive: false,
            keyboard: false,
            icon: buildDxccLabelIcon(label, theme)
        }).addTo(currentDxccLabelLayer);
    });
    incrementPerfCounter('mercator.dxcc.labels_added', labels.length);

    if (revision !== dxccSyncRevision) {
        endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
        return;
    }

    currentDxccLabelLayer.addTo(map);
    currentDxccLabelLayerKey = key;
    endPerfTimer('mercator.dxcc.sync_total_ms', syncTimer);
}

export function setTheme(theme) {
    document.body.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);

    if (currentTileLayer && map) {
        map.removeLayer(currentTileLayer);
        currentTileLayer = null;
    }

    if (currentGeoJsonLayer && map) {
        map.removeLayer(currentGeoJsonLayer);
        currentGeoJsonLayer = null;
    }

    if (currentCountryLayer && map) {
        map.removeLayer(currentCountryLayer);
        currentCountryLayer = null;
    }
    if (currentGraylineLayer && map) {
        map.removeLayer(currentGraylineLayer);
        currentGraylineLayer = null;
    }
    if (currentDxccLabelLayer && map) {
        map.removeLayer(currentDxccLabelLayer);
        currentDxccLabelLayer = null;
    }
    currentCountryLayerTheme = null;
    currentGraylineLayerKey = null;
    currentDxccLabelLayerKey = null;

    currentTileLayer = theme === 'dark' ? getDarkTileLayer() : getLightTileLayer();
    if (map) {
        currentTileLayer.addTo(map);
        map.getContainer().style.background = '';
    }

    const toggleBtn = document.getElementById('theme-toggle');
    if (toggleBtn) {
        toggleBtn.innerHTML = theme === 'dark' ? '<i class="fas fa-sun"></i>' : '<i class="fas fa-moon"></i>';
        toggleBtn.title = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
    }
}
