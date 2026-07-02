export let map;
export let currentTileLayer = null;
export let currentGeoJsonLayer = null;
export let currentCountryLayer = null;
export let currentGraylineLayer = null;
export let currentDxccLabelLayer = null;

import { getCountryColoringEnabled, getCountryFillForFeature, getGraylineEnabled, getGraylineOverlayOpacities, getMercatorDxccLabelsEnabled, getSubsolarPoint, greatCirclePoints, destinationPoint, hexToRgb, blendOverlayColors } from './utils.js';
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
const MERCATOR_TILE_SIZE_PX = 256;
// Effectively unbounded longitude so east/west panning + worldCopyJump keep
// working, while maxBounds still clamps latitude to the projection's poles.
const MERCATOR_HORIZONTAL_PAN_LIMIT_DEG = 360 * 1000;
// Replicate the grayline overlay across this many world copies on each side so
// the terminator is never interrupted when panning across the antimeridian.
const GRAYLINE_WORLD_COPIES = 2;
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
        worldCopyJump: true,
        maxBoundsViscosity: 1.0
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
    // Chase Queue spot highlight (path + marker), above everything else.
    if (!map.getPane('dx-highlight-pane')) {
        const pane = map.createPane('dx-highlight-pane');
        pane.style.zIndex = '650';
        pane.style.pointerEvents = 'none';
    }
    // Operator antenna beam lobe (opmode), below the chase-queue highlight.
    if (!map.getPane('antenna-pane')) {
        const pane = map.createPane('antenna-pane');
        pane.style.zIndex = '500';
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

    // Re-derive the minimum zoom whenever the container is resized (window
    // resize, sidebar toggle, invalidateSize) so the world always fills the
    // viewport vertically.
    map.on('resize', applyMercatorViewConstraints);
    applyMercatorViewConstraints();

    return map;
}

// applyMercatorViewConstraints pins the view so the user can never zoom or pan
// to reveal empty space above/below the map: the minimum zoom is set so the
// Web-Mercator world (MERCATOR_TILE_SIZE_PX · 2^zoom px tall) is at least as
// tall as the viewport, and maxBounds clamps latitude to the poles. Longitude
// is left effectively unbounded so east/west panning still works.
function applyMercatorViewConstraints() {
    if (!map || typeof map.getSize !== 'function' || typeof map.setMinZoom !== 'function') {
        return;
    }

    const size = map.getSize();
    const height = size && Number.isFinite(size.y) ? size.y : 0;
    if (height <= 0) {
        return;
    }

    // +epsilon guards against sub-pixel rounding leaving a 1px gap at the limit.
    const minZoom = Math.log2(height / MERCATOR_TILE_SIZE_PX) + 1e-3;
    if (Number.isFinite(minZoom)) {
        map.setMinZoom(minZoom);
    }

    if (typeof map.setMaxBounds === 'function') {
        map.setMaxBounds([
            [-WEB_MERCATOR_MAX_LAT, -MERCATOR_HORIZONTAL_PAN_LIMIT_DEG],
            [WEB_MERCATOR_MAX_LAT, MERCATOR_HORIZONTAL_PAN_LIMIT_DEG]
        ]);
    }
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

    // Replicate the overlay across adjacent world copies so the terminator is
    // continuous when panning east/west across the antimeridian (the single
    // [-180,180] copy used to vanish in neighbouring worlds).
    const graylineGroup = L.layerGroup([], { pane: 'grayline-pane' });
    for (let copy = -GRAYLINE_WORLD_COPIES; copy <= GRAYLINE_WORLD_COPIES; copy += 1) {
        const offset = copy * 360;
        L.imageOverlay(dataUrl, [[-WEB_MERCATOR_MAX_LAT, -180 + offset], [WEB_MERCATOR_MAX_LAT, 180 + offset]], {
            pane: 'grayline-pane',
            interactive: false,
            opacity: 1
        }).addTo(graylineGroup);
    }
    graylineGroup.addTo(map);
    currentGraylineLayer = graylineGroup;
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

let dxHighlightLayer = null;

function ensureDxHighlightLayer() {
    if (!map) return null;
    if (!dxHighlightLayer) {
        dxHighlightLayer = L.layerGroup([], { pane: 'dx-highlight-pane' }).addTo(map);
    }
    return dxHighlightLayer;
}

// setMercatorDxHighlight draws the Chase Queue spot highlight on the Leaflet map:
// a segmented great-circle path from origin → spot (dashed = hover, solid =
// pinned) plus a marker + callsign label. Updated directly (no full re-render)
// so hovering stays snappy. Pass nothing / null spot to clear.
export function setMercatorDxHighlight({ originLat, originLng, spotLat, spotLng, label = '', pinned = false } = {}) {
    const layer = ensureDxHighlightLayer();
    if (!layer) return;
    layer.clearLayers();
    if (!Number.isFinite(spotLat) || !Number.isFinite(spotLng)) return;

    const dark = document.body.getAttribute('data-theme') === 'dark';
    const color = dark ? '#ffd166' : '#d97706';

    if (Number.isFinite(originLat) && Number.isFinite(originLng)) {
        const pts = greatCirclePoints(originLat, originLng, spotLat, spotLng, 64);
        L.polyline(pts, {
            pane: 'dx-highlight-pane',
            color,
            weight: pinned ? 3 : 2,
            opacity: pinned ? 0.95 : 0.8,
            dashArray: pinned ? null : '3 6',
            interactive: false
        }).addTo(layer);
    }

    L.circleMarker([spotLat, spotLng], {
        pane: 'dx-highlight-pane',
        radius: pinned ? 7 : 6,
        color,
        weight: 2.6,
        fillColor: color,
        fillOpacity: 0.5,
        interactive: false
    }).addTo(layer);

    if (label) {
        L.marker([spotLat, spotLng], {
            pane: 'dx-highlight-pane',
            interactive: false,
            icon: L.divIcon({
                className: 'dx-highlight-label',
                html: `<span>${label}</span>`,
                iconSize: null,
                iconAnchor: [-10, 8]
            })
        }).addTo(layer);
    }
}

export function clearMercatorDxHighlight() {
    if (dxHighlightLayer) dxHighlightLayer.clearLayers();
}

// --- Operator antenna beam overlay (Mercator) ---------------------------------
// Mirrors the azimuthal beam lobe (azimuth-runtime.js drawAntennaDirectionalLobe)
// on the Leaflet map: a great-circle cone from the station along the beam
// bearing, filled and edged, with the outer arc left open (no end line) to match
// the azimuth look. Updated imperatively (like setMercatorDxHighlight), so it
// stays in sync without a full re-render.

// Beam reach on the Mercator map (km). Kept short enough that a due-north beam
// from a typical mid-latitude station does not cross the pole (which would flip
// longitude by ~180° and smear the polygon across the map); longitude unwrapping
// (below) handles the remaining antimeridian cases.
const MERCATOR_BEAM_RADIUS_KM = 4000;

let antennaLayer = null;
let lastAntennaOverlay = null; // cached payload so setTheme can recolor + redraw

function ensureAntennaLayer() {
    if (!map) return null;
    if (!antennaLayer) {
        antennaLayer = L.layerGroup([], { pane: 'antenna-pane' }).addTo(map);
    }
    return antennaLayer;
}

const _normBearing = (deg) => ((deg % 360) + 360) % 360;

// unwrapLngSeq rewrites a [[lat,lng],…] sequence so consecutive longitudes never
// jump more than 180° — i.e. it lets longitude run past ±180 continuously. Leaflet
// (worldCopyJump) renders such coordinates correctly and, crucially, will NOT draw
// a stray line straight across the map when a great-circle path crosses the
// antimeridian or sweeps a wide longitude range near the pole. Exported for tests.
export function unwrapLngSeq(points) {
    const out = [];
    let prev = null;
    for (const [lat, lng] of points) {
        let l = lng;
        if (prev !== null) {
            while (l - prev > 180) l -= 360;
            while (l - prev < -180) l += 360;
        }
        out.push([lat, l]);
        prev = l;
    }
    return out;
}

// sampleBeamEdge walks the station outward along a fixed bearing, returning
// [[lat,lng],…] (station first). Multiple samples keep the great-circle curve on
// the Mercator projection instead of a straight screen line.
function sampleBeamEdge(lat, lng, bearingDeg, radiusKm, segments = 24) {
    const pts = [];
    for (let i = 0; i <= segments; i += 1) {
        pts.push(destinationPoint(lat, lng, bearingDeg, (radiusKm * i) / segments));
    }
    return pts;
}

// sampleBeamArc samples the constant-distance outer arc across the beamwidth.
function sampleBeamArc(lat, lng, startBearingDeg, sweepDeg, radiusKm) {
    const steps = Math.max(12, Math.ceil(sweepDeg / 2));
    const pts = [];
    for (let i = 0; i <= steps; i += 1) {
        const b = _normBearing(startBearingDeg + (sweepDeg * i) / steps);
        pts.push(destinationPoint(lat, lng, b, radiusKm));
    }
    return pts;
}

function drawBeamLobe(layer, lat, lng, centerBearingDeg, beamwidthDeg, radiusKm, fillColor, fillOpacity, lineColor) {
    const half = Math.max(2, beamwidthDeg / 2);
    const bw = half * 2;
    const leftBearing = _normBearing(centerBearingDeg - half);
    const leftEdge = sampleBeamEdge(lat, lng, leftBearing, radiusKm);
    const rightEdge = sampleBeamEdge(lat, lng, _normBearing(centerBearingDeg + half), radiusKm);
    const arc = sampleBeamArc(lat, lng, leftBearing, bw, radiusKm);

    // Filled cone: station → left edge → outer arc → right edge (auto-closed).
    // Unwrap the whole ring so it never jumps across the antimeridian.
    const poly = unwrapLngSeq([...leftEdge, ...arc, ...rightEdge.slice().reverse()]);
    L.polygon(poly, {
        pane: 'antenna-pane',
        stroke: false,
        fill: true,
        fillColor,
        fillOpacity,
        interactive: false
    }).addTo(layer);

    // Stroke only the two side edges — the rounded outer end stays open.
    for (const edge of [leftEdge, rightEdge]) {
        L.polyline(unwrapLngSeq(edge), {
            pane: 'antenna-pane',
            color: lineColor,
            weight: 1.7,
            opacity: 0.9,
            interactive: false
        }).addTo(layer);
    }
}

// setMercatorAntennaOverlay draws the operator antenna beam on the Leaflet map.
// Accepts the same payload shape as setAzimuthAntennaOverlay so opmode.js can call
// both together. Pass { enabled: false } (or omit) to clear.
export function setMercatorAntennaOverlay(overlay = {}) {
    const layer = ensureAntennaLayer();
    if (!layer) return;
    layer.clearLayers();

    if (!overlay || overlay.enabled !== true) {
        lastAntennaOverlay = null;
        return;
    }
    const lat = Number(overlay.stationLat);
    const lng = Number(overlay.stationLng);
    const heading = Number(overlay.azimuthDeg);
    if (!Number.isFinite(lat) || !Number.isFinite(lng) || !Number.isFinite(heading)) {
        lastAntennaOverlay = null;
        return;
    }
    lastAntennaOverlay = overlay;

    const beamwidth = Number.isFinite(Number(overlay.beamwidth3dBDeg)) ? Number(overlay.beamwidth3dBDeg) : 60;
    const mode = overlay.mode || 'forward';
    const dark = document.body.getAttribute('data-theme') === 'dark';

    const forwardFill = dark ? '#ffc454' : '#ffa140';
    const forwardLine = dark ? '#ffde9b' : '#b85200';
    const backwardFill = dark ? '#6fb9ff' : '#3f89ff';
    const backwardLine = dark ? '#9bd1ff' : '#145bbb';
    const fillOpacity = dark ? 0.14 : 0.16;

    if (mode === 'forward' || mode === 'bidirectional') {
        drawBeamLobe(layer, lat, lng, heading, beamwidth, MERCATOR_BEAM_RADIUS_KM, forwardFill, fillOpacity, forwardLine);
    }
    if (mode === 'backward' || mode === 'bidirectional') {
        drawBeamLobe(layer, lat, lng, heading + 180, beamwidth, MERCATOR_BEAM_RADIUS_KM, backwardFill, fillOpacity, backwardLine);
    }
}

export function clearMercatorAntennaOverlay() {
    lastAntennaOverlay = null;
    if (antennaLayer) antennaLayer.clearLayers();
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

    // Beam colors are theme-dependent; redraw from the cached payload.
    if (lastAntennaOverlay) {
        setMercatorAntennaOverlay(lastAntennaOverlay);
    }

    const toggleBtn = document.getElementById('theme-toggle');
    if (toggleBtn) {
        toggleBtn.innerHTML = theme === 'dark' ? '<i class="fas fa-sun"></i>' : '<i class="fas fa-moon"></i>';
        toggleBtn.title = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
    }
}
