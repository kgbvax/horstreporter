import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, getMinSnrMode, getSelectedBand, getEnabledBands, gridSnrOpacity, topQuartileMean, bandColors, locatorToBounds, regionForLocatorCached } from './utils.js';
import { endPerfTimer, incrementPerfCounter, isPerfProfilingEnabled, startPerfTimer } from './perf.js';

// Rendered-state fingerprint for the grid-snr heat layer. Unlike the old
// spot-list fingerprint (spots.length + first/last ageSeconds — which changes
// on every arriving spot and every 5s prune), this keys on the actual squares
// and their appearance, so a new spot that doesn't change any square's
// dominant band or quantized opacity doesn't tear down and rebuild the layer.
let lastGridFingerprint = '';
// Lazy loader for vendor/js/turf.min.js (~605KB raw). The script is not part
// of the initial index.html payload; it is injected on demand the first time
// the active-area (hull/cluster) render path actually runs. An already-loaded
// global (e.g. the perf-gate stub) is reused as-is instead of re-injecting.
let turfLoadPromise = null;
// Backoff state for failed injections: without it the 2s rebuild debounce
// would re-append the ~605KB script (and a dead <script> tag) forever while
// the vendor file is unreachable.
let turfFailures = 0;
let lastTurfFailureAt = 0;
export function ensureTurf() {
    const existing = (typeof window !== 'undefined' ? window : globalThis).turf;
    if (existing) return Promise.resolve(existing);
    // Hoisted so ensureTurf always returns a Promise (the rejection flows into
    // the caller's .catch instead of crashing on a null .then).
    if (typeof document === 'undefined' || !document.createElement) {
        return Promise.reject(new Error('turf: no DOM available to load vendor/js/turf.min.js'));
    }
    if (turfFailures && Date.now() - lastTurfFailureAt < Math.min(2000 * 2 ** turfFailures, 60000)) {
        // Back off exponentially (2s -> 60s cap) after failures; reset on success.
        return Promise.reject(new Error('turf: backing off after a failed load'));
    }
    if (!turfLoadPromise) {
        turfLoadPromise = new Promise((resolve, reject) => {
            const script = document.createElement('script');
            script.src = 'vendor/js/turf.min.js';
            script.async = true;
            script.onload = () => {
                const loaded = (typeof window !== 'undefined' ? window : globalThis).turf;
                if (loaded) {
                    turfFailures = 0;
                    resolve(loaded);
                } else {
                    turfFailures += 1;
                    lastTurfFailureAt = Date.now();
                    script.remove();
                    turfLoadPromise = null;
                    reject(new Error('turf: vendor/js/turf.min.js loaded but the turf global is missing'));
                }
            };
            script.onerror = () => {
                // Remove the dead tag and allow a later frame to retry.
                turfFailures += 1;
                lastTurfFailureAt = Date.now();
                script.remove();
                turfLoadPromise = null;
                reject(new Error('turf: failed to load vendor/js/turf.min.js'));
            };
            document.head.appendChild(script);
        });
    }
    return turfLoadPromise;
}

// Debounce for the active-area rebuild: turf.clustersDbscan is O(n²), so the
// layer is rebuilt at most this often even when spots are streaming in.
const ACTIVE_AREA_REBUILD_INTERVAL_MS = 2000;
// Cap on the per-band point count fed to the O(n²) DBSCAN. Strongest-SNR
// points are kept for clustering; the rest are drawn as dots.
const MAX_ACTIVE_AREA_CLUSTER_POINTS = 300;

// Canvas renderer for the Mercator Grid-SNR / Active-Area non-interactive
// polygon overlays. SVG re-projects every path on each zoomend; with many
// grid squares (and zoomSnap:0 firing several fractional zoomends per net
// zoom level) that is the dominant zoom-time cost. Canvas redraws all paths
// in one batched paint instead. Grid squares and active-area polygons are
// already interactive:false, so canvas (which can't do per-path mouse
// events) is safe; DX-cluster circleMarkers keep the default SVG renderer so
// their tooltips/hover are unaffected.
//
// Singleton, reused across heatLayer rebuilds: map.removeLayer(heatLayer)
// removes the geoJSON paths from the renderer but leaves the renderer on
// the map (no orphan canvas containers piling up per rebuild). Recreated
// lazily if the Leaflet map itself was recreated (initMap calls map.remove()
// on a projection switch), detected via hasLayer.
let gridCanvasRenderer = null;
function getGridCanvasRenderer() {
    if (!gridCanvasRenderer || !map.hasLayer(gridCanvasRenderer)) {
        gridCanvasRenderer = L.canvas();
    }
    return gridCanvasRenderer;
}

// Add a single isolated-spot circle marker to the given layer (shared by the
// clustered and non-clustered render paths so the style lives in one place).
function addSpotMarker(p, color, layer) {
    L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
        color, fillColor: color, radius: 4.5, weight: 2,
        opacity: 0.65, fillOpacity: 0.5, interactive: false
    }).addTo(layer);
}

function buildFilterCtx() {
    return {
        minSnrMode: getMinSnrMode(),
        ssbMinDb: parseInt(document.getElementById('ssb-min-db')?.value || '0', 10),
        cwMinDb: parseInt(document.getElementById('cw-min-db')?.value || '-15', 10),
        selectedBand: getSelectedBand(),
        enabledBands: getEnabledBands(),
        filterBand: state.drillDownBand || '',
        filterRegion: state.drillDownRegion || '',
    };
}

// resetRenderFingerprint forces the next render to rebuild the heat layer even
// if the spot set looks identical. Called on stream stop and projection switch
// so a restarted/cleared stream always redraws.
export function resetRenderFingerprint() {
    lastGridFingerprint = '';
    state.lastActiveAreaRebuildAt = 0;
}

function escapeHtml(value) {
    return String(value ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

function buildDxClusterHoverHtml(spot) {
    const sender = escapeHtml(spot?.sender || '—');
    const receiver = escapeHtml(spot?.receiver || '—');
    const band = escapeHtml(spot?.band || '—');
    const locator = escapeHtml(spot?.locator || '—');
    const reporterLocator = escapeHtml(spot?.reporterLocator || '—');
    const snr = Number.isFinite(Number(spot?.snr)) ? `${Number(spot.snr)} dB` : '—';

    return [
        '<strong>DXCluster Spot</strong>',
        `DX: ${receiver}`,
        `Spotter: ${sender}`,
        `Band: ${band}`,
        `SNR: ${snr}`,
        `DX Loc: ${locator}`,
        `Spotter Loc: ${reporterLocator}`
    ].join('<br>');
}

function splitSpotSources(spots) {
    const regularSpots = [];
    const dxClusterSpots = [];
    const wsprSpots = [];
    spots.forEach((spot) => {
        const src = String(spot?.sourceType || '').toLowerCase();
        if (src === 'dxcluster') {
            dxClusterSpots.push(spot);
        } else if (src === 'wspr') {
            wsprSpots.push(spot);
        } else {
            regularSpots.push(spot);
        }
    });
    return { regularSpots, dxClusterSpots, wsprSpots };
}

// DX cluster markers live in their own layer (state.dxClusterLayer), NOT in
// the heatLayer. The heatLayer is torn down and rebuilt on every render, and
// a rebuilt marker's bound tooltip closes and re-opens — hovering a cluster
// marker during an active FT8 period made the tooltip flicker with no
// pointer movement. This layer only rebuilds when the DX cluster spot set
// itself changes.
let dxClusterMarkerFingerprint = '';

export function clearDxClusterMarkers() {
    dxClusterMarkerFingerprint = '';
    // The layer's markers are being torn down; their mouseout won't fire, so
    // clear the hover-suppression flag here or the grid tooltip would stay
    // suppressed after the markers are gone.
    state.dxClusterHoverActive = false;
    if (state.dxClusterLayer && map) {
        map.removeLayer(state.dxClusterLayer);
    }
    state.dxClusterLayer = null;
}

function syncDxClusterMarkers(dxClusterSpots) {
    // O(1) fingerprint: spots are appended at the end and pruned from the
    // front (never mutated in place), so the first/last spots capture every
    // set change without building a giant join string every frame.
    const first = dxClusterSpots[0];
    const last = dxClusterSpots[dxClusterSpots.length - 1];
    const key = (s) => s ? `${s.locator}|${s.band}|${s.sender}|${s.receiver}|${s.reporterLocator}|${s.lat}|${s.lng}` : '';
    const fingerprint = `${dxClusterSpots.length}:${key(first)}:${key(last)}`;
    if (fingerprint === dxClusterMarkerFingerprint && state.dxClusterLayer) return;
    clearDxClusterMarkers();
    dxClusterMarkerFingerprint = fingerprint;
    if (dxClusterSpots.length === 0) return;
    state.dxClusterLayer = L.layerGroup().addTo(map);
    incrementPerfCounter('mercator.layers.added', 1);
    renderDxClusterMarkers(dxClusterSpots);
}

function renderDxClusterMarkers(dxClusterSpots) {
    if (!Array.isArray(dxClusterSpots) || dxClusterSpots.length === 0 || !state.dxClusterLayer) return;

    // Item 4: single circleMarker per spot instead of two
    // Subtle visual distinction: white border around the band-colored fill
    // so DX Cluster spots are clearly identifiable as individual points in
    // Grid-SNR mode, rather than looking like small grid rectangles.
    dxClusterSpots.forEach((spot) => {
        if (!Number.isFinite(spot.lat) || !Number.isFinite(spot.lng)) return;
        const color = bandColors[spot.band] || bandColors.all;
        const marker = L.circleMarker([spot.lat, spot.lng], {
            color: '#ffffff',
            fillColor: color,
            radius: 4.5,
            weight: 1.5,
            opacity: 0.9,
            fillOpacity: 0.65,
            interactive: true,
            bubblingMouseEvents: false
        }).addTo(state.dxClusterLayer);

        marker.bindTooltip(buildDxClusterHoverHtml(spot), {
            direction: 'top',
            offset: [0, -6],
            opacity: 0.95,
            sticky: true
        });
        marker.on('mouseover', () => {
            state.dxClusterHoverActive = true;
            const tooltip = document.getElementById('tooltip');
            if (tooltip) tooltip.style.display = 'none';
        });
        marker.on('mouseout', () => {
            state.dxClusterHoverActive = false;
        });
    });
}

// --- WSPR beacon markers (region-scoped, distinct style) --------------------
// WSPR spots are a global propagation reference. They're drawn as a separate
// layer (like DX-cluster) with a distinct visual style (smaller, teal/cyan
// fill with dashed border) so they're clearly separable from regular spots
// and DX-cluster markers. Region-scoped: only WSPR paths where either end is
// in the operator's DXPulse region are shown, keeping the map relevant.
let wsprMarkerFingerprint = '';

export function clearWsprMarkers() {
    wsprMarkerFingerprint = '';
    if (state.wsprLayer && map) {
        map.removeLayer(state.wsprLayer);
    }
    state.wsprLayer = null;
}

function syncWsprMarkers(wsprSpots) {
    if (wsprSpots.length === 0) {
        if (state.wsprLayer) clearWsprMarkers();
        return;
    }

    // Region-scope: only show WSPR paths where either end is in the
    // operator's region (derived from the QTH locator). If the QTH is a
    // callsign (no locator), skip the region filter and show all WSPR.
    // regionForLocatorCached memoizes per locator (locators repeat heavily).
    const qthEl = document.getElementById('qth');
    const qthVal = qthEl?.value?.trim()?.toUpperCase() || '';
    const operatorRegion = regionForLocatorCached(qthVal);
    const scopedSpots = operatorRegion
        ? wsprSpots.filter((s) =>
            regionForLocatorCached(s.locator) === operatorRegion ||
            regionForLocatorCached(s.reporterLocator) === operatorRegion)
        : wsprSpots;

    // O(1) fingerprint (first/last spot) — see syncDxClusterMarkers.
    const first = scopedSpots[0];
    const last = scopedSpots[scopedSpots.length - 1];
    const key = (s) => s ? `${s.locator}|${s.reporterLocator}|${s.band}|${s.snr}` : '';
    const fingerprint = `${scopedSpots.length}:${key(first)}:${key(last)}`;
    if (fingerprint === wsprMarkerFingerprint && state.wsprLayer) return;
    clearWsprMarkers();
    wsprMarkerFingerprint = fingerprint;
    if (scopedSpots.length === 0) return;
    state.wsprLayer = L.layerGroup().addTo(map);
    incrementPerfCounter('mercator.layers.added', 1);
    renderWsprMarkers(scopedSpots);
}

function renderWsprMarkers(wsprSpots) {
    if (!Array.isArray(wsprSpots) || wsprSpots.length === 0 || !state.wsprLayer) return;

    wsprSpots.forEach((spot) => {
        if (!Number.isFinite(spot.lat) || !Number.isFinite(spot.lng)) return;
        // Distinct style: small teal/cyan dots with a thin dashed border.
        const marker = L.circleMarker([spot.lat, spot.lng], {
            color: '#0d6efd',
            fillColor: '#17a2b8',
            radius: 3,
            weight: 1,
            opacity: 0.7,
            fillOpacity: 0.5,
            dashArray: '3,2',
            interactive: true,
            bubblingMouseEvents: false
        }).addTo(state.wsprLayer);

        marker.bindTooltip(
            `<strong>WSPR beacon</strong><br>Band: ${escapeHtml(spot.band || '—')}<br>SNR: ${Number.isFinite(Number(spot.snr)) ? spot.snr + ' dB' : '—'}<br>TX: ${escapeHtml(spot.locator || '—')}<br>RX: ${escapeHtml(spot.reporterLocator || '—')}`,
            { direction: 'top', offset: [0, -4], opacity: 0.9, sticky: true }
        );
    });
}

export function updateMapVisualization(spots, maxMinutes) {
    if (!map) return;

    const perfEnabled = isPerfProfilingEnabled();
    const renderTimer = startPerfTimer();

    // NOTE: do NOT reset state.dxClusterHoverActive here. It is set by the
    // DX-cluster marker's mouseover and cleared by its mouseout (and by
    // clearDxClusterMarkers when the layer is torn down). Resetting it on every
    // render re-enabled the grid-square hover tooltip while the pointer was
    // still over a cluster marker, causing tooltip flicker/overlap.

    // Item 3: read all filter/style state once
    const checkedStyleRadio = document.querySelector('input[name="style-select"]:checked');
    const style = checkedStyleRadio ? checkedStyleRadio.value : 'grid-snr';
    const filterCtx = buildFilterCtx();

    // Split spot sources once per render (was 3x: renderGridSnr,
    // syncDxClusterMarkers, syncWsprMarkers each re-split the full array).
    const { regularSpots, dxClusterSpots, wsprSpots } = splitSpotSources(spots);

    if (style === 'active-area') {
        // turf.clustersDbscan is O(n²), so bound the rebuild with a debounce
        // (the layer is kept as-is between rebuilds) and a per-band point cap
        // inside renderActiveArea. The marker layers still update every render.
        const now = Date.now();
        if (!state.heatLayer || (now - (state.lastActiveAreaRebuildAt || 0)) >= ACTIVE_AREA_REBUILD_INTERVAL_MS) {
            state.lastActiveAreaRebuildAt = now;
            if (state.heatLayer) {
                map.removeLayer(state.heatLayer);
                incrementPerfCounter('mercator.layers.removed', 1);
            }
            incrementPerfCounter('mercator.render.style.active_area', 1);
            // Create the layer synchronously so it is on the map even while
            // turf is still loading; the hull/draw pass fills it in via the
            // async continuation (its `layer` argument pins the draw target so
            // a later style switch can't capture this draw).
            const layer = L.layerGroup().addTo(map);
            state.heatLayer = layer;
            incrementPerfCounter('mercator.layers.added', 1);
            rebuildActiveArea(layer, regularSpots, maxMinutes, filterCtx, spots);
        }
    } else {
        // grid-snr: the aggregate is O(n) but cheap; the L.geoJSON layer
        // creation is the expensive part. Fingerprint the RENDERED state
        // (squares + dominant band + quantized opacity) so a new spot that
        // doesn't change any square's appearance doesn't tear down and rebuild
        // the layer on every 40ms render.
        const gridState = aggregateGridSquares(regularSpots, filterCtx);
        const gridFingerprint = buildGridFingerprint(gridState.squareData, filterCtx);
        if (gridFingerprint !== lastGridFingerprint || !state.heatLayer) {
            lastGridFingerprint = gridFingerprint;
            if (state.heatLayer) {
                map.removeLayer(state.heatLayer);
                incrementPerfCounter('mercator.layers.removed', 1);
            }
            incrementPerfCounter('mercator.render.style.grid_snr', 1);
            renderGridSquares(gridState.squareData, filterCtx);
        }
        // Every frame, not just on a grid change: the band rail's counts and
        // sparklines move even when no square's appearance does.
        const bandLabelTimer = startPerfTimer();
        updateBandLabels(spots, filterCtx, gridState.activeBands);
        endPerfTimer('mercator.band_labels.total_ms', bandLabelTimer);
    }

    // DX cluster markers: persistent layer, rebuilt only when the cluster
    // spot set changes — hovering must not flicker on every heatLayer rebuild.
    syncDxClusterMarkers(dxClusterSpots);
    syncWsprMarkers(wsprSpots);

    if (document.getElementById('auto-zoom')?.checked) {
        const now = Date.now();
        const interactionCooldownMs = 12000;
        const autoZoomMinIntervalMs = 5000;
        if ((now - (state.lastMercatorInteractionAt || 0)) < interactionCooldownMs) {
            if (perfEnabled) {
                endPerfTimer('mercator.render.total_ms', renderTimer);
            }
            return;
        }
        if ((now - (state.lastMercatorAutoZoomAt || 0)) < autoZoomMinIntervalMs) {
            if (perfEnabled) {
                endPerfTimer('mercator.render.total_ms', renderTimer);
            }
            return;
        }

        const autoZoomCalcTimer = startPerfTimer();
        // Item 3: use filterCtx instead of re-reading DOM
        const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;

        let minLat = 90, maxLat = -90, minLng = 180, maxLng = -180;
        let found = false;

        spots.forEach(spot => {
            if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
            if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
            if (!enabledBands.has(spot.band)) return;
            if (selectedBand !== 'all' && spot.band !== selectedBand) return;

            if (spot.lat < minLat) minLat = spot.lat;
            if (spot.lat > maxLat) maxLat = spot.lat;
            if (spot.lng < minLng) minLng = spot.lng;
            if (spot.lng > maxLng) maxLng = spot.lng;
            found = true;
        });
        endPerfTimer('mercator.autozoom.bounds_calc_ms', autoZoomCalcTimer);

        if (found) {
            const fitBoundsTimer = startPerfTimer();
            const bounds = L.latLngBounds([minLat, minLng], [maxLat, maxLng]);
            const center = bounds.getCenter();
            const minBounds = center.toBounds(2000000); // minimum 2000km
            bounds.extend(minBounds);
            map.fitBounds(bounds, { padding: [20, 20], maxZoom: 5 });
            endPerfTimer('mercator.autozoom.fit_bounds_ms', fitBoundsTimer);
            incrementPerfCounter('mercator.autozoom.applied', 1);
            state.lastMercatorAutoZoomAt = now;
        }

        if (perfEnabled) {
            endPerfTimer('mercator.render.total_ms', renderTimer);
        }
        return;
    }

    if (perfEnabled) {
        endPerfTimer('mercator.render.total_ms', renderTimer);
    }
}

export function updateBandLabels(spots, filterCtx = null, activeBands = null) {
    // Item 3: use provided filterCtx or read from DOM once
    const ctx = filterCtx || buildFilterCtx();

    // Item 6: use pre-computed activeBands or scan spots once
    let bands = activeBands;
    if (!bands) {
        bands = new Set();
        spots.forEach(s => {
            if (ctx.minSnrMode === 'ssb' && s.snr < ctx.ssbMinDb) return;
            if (ctx.minSnrMode === 'cw' && s.snr < ctx.cwMinDb) return;
            if (!ctx.enabledBands.has(s.band)) return;
            bands.add(s.band);
        });
    }

    const enabledBands = ctx.enabledBands;
    const focus = getSelectedBand();
    // Focus only applies while its band is still enabled; otherwise fall back to
    // "show all enabled" (focus is ignored until set again).
    const effectiveFocus = (focus !== 'all' && enabledBands.has(focus)) ? focus : 'all';
    const soloing = effectiveFocus !== 'all';
    const activity = bandActivity(spots, ctx);
    // One shared scale so a 2-spot band doesn't look as busy as a 200-spot one.
    let sparkPeak = 1;
    for (const a of activity.values()) for (const v of a.bins) if (v > sparkPeak) sparkPeak = v;

    document.querySelectorAll('.band-pill').forEach(pill => {
        const band = pill.dataset.band;
        if (!band) return;
        const enabled = enabledBands.has(band);
        const act = activity.get(band);
        // Counts ignore focus, so a band dimmed by solo still reports its
        // activity; fall back to the render's band set when no spots were given.
        const hasData = act ? act.count > 0 : bands.has(band);
        const isFocused = enabled && band === effectiveFocus;
        const shown = enabled && (effectiveFocus === 'all' || isFocused);

        pill.style.setProperty('--band', bandColors[band] || '#6c757d');
        pill.dataset.state = !enabled ? 'off' : (hasData ? 'live' : 'quiet');
        pill.classList.toggle('is-focused', isFocused);
        pill.classList.toggle('is-dimmed', soloing && !shown);

        const count = pill.querySelector('.band-count');
        if (count) count.textContent = enabled ? (act?.count ? String(act.count) : '\u2013') : '';
        const line = pill.querySelector('.band-spark polyline');
        if (line) line.setAttribute('points', enabled && act ? sparkPoints(act.bins, sparkPeak) : '');

        pill.setAttribute('aria-pressed', isFocused ? 'true' : 'false');
    });
}

const SPARK_BINS = 15;

// Per-band spot count + a SPARK_BINS histogram over the max-spot-age window,
// anchored on the newest spot so it also works for a timeline moment.
// Applies the SNR floor but not focus.
function bandActivity(spots, ctx) {
    const out = new Map();
    if (!Array.isArray(spots) || spots.length === 0) return out;
    let tMax = -Infinity;
    for (const s of spots) if (s.t > tMax) tMax = s.t;
    const span = 60 * (Number(document.getElementById('minutes')?.value) || 15);
    const t0 = tMax - span;
    for (const s of spots) {
        if (ctx.minSnrMode === 'ssb' && s.snr < ctx.ssbMinDb) continue;
        if (ctx.minSnrMode === 'cw' && s.snr < ctx.cwMinDb) continue;
        let a = out.get(s.band);
        if (!a) { a = { count: 0, bins: new Array(SPARK_BINS).fill(0) }; out.set(s.band, a); }
        a.count += 1;
        const i = Math.min(SPARK_BINS - 1, Math.max(0, Math.floor(((s.t - t0) / span) * SPARK_BINS)));
        a.bins[i] += 1;
    }
    return out;
}

// Polyline points for a 60x16 viewBox. Square-root scale against the busiest
// bin across all bands keeps quiet bands visible without exaggerating them.
function sparkPoints(bins, peak) {
    const step = 60 / (bins.length - 1);
    return bins.map((v, i) => `${(i * step).toFixed(1)},${(15 - Math.sqrt(v / peak) * 14).toFixed(1)}`).join(' ');
}

// Aggregate filtered spots into grid squares. O(n) but cheap; the expensive
// part is the L.geoJSON layer creation, which is gated separately by
// buildGridFingerprint so a new spot that doesn't change any square's
// appearance doesn't tear down and rebuild the layer.
export function aggregateGridSquares(regularSpots, filterCtx) {
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
    const squareData = {};
    const res = getGridResolution();
    const activeBands = new Set();
    const aggregateTimer = startPerfTimer();

    // Filter first, then aggregate: the snrs list (which drives a square's
    // brightness via topQuartileMean) must reflect only the spots the user is
    // actually allowed to see. Aggregating before the band+SNR filter
    // previously let a band-filtered-out spot (e.g. a +25 dB 15m spot while
    // soloing 20m) inflate a square's opacity, and let an RBN 0-40 dB CW-scale
    // spot set intensity against an FT8-calibrated threshold. visibleCount
    // and bands (which gate drawing and pick the color) were already
    // post-filter, so only the intensity was wrong — but wrong intensity
    // is what made a square read as "active" when its visible spots were weak.
    regularSpots.forEach(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;
        // Drill-down filter (U4): when a matrix cell was clicked, restrict to
        // that band × region. Empty string means no drill-down filter.
        if (filterBand && spot.band !== filterBand) return;
        if (filterRegion && regionForLocatorCached(spot.locator) !== filterRegion) return;

        let loc = spot.locator.substring(0, res);
        if (loc.length < res) loc = spot.locator.substring(0, 4); // Fallback if data is sparse
        if (loc.length < 4) return;

        if (!squareData[loc]) {
            squareData[loc] = { count: 0, visibleCount: 0, bands: {}, snrs: [] };
        }
        squareData[loc].count++;
        squareData[loc].visibleCount++;
        squareData[loc].bands[spot.band] = (squareData[loc].bands[spot.band] || 0) + 1;
        squareData[loc].snrs.push(Number(spot.snr));
        activeBands.add(spot.band);
    });
    endPerfTimer('mercator.grid.aggregate_ms', aggregateTimer);
    return { squareData, activeBands };
}

// Fingerprint of the grid's RENDERED state: the filter key plus, per square,
// its locator, dominant band, and quantized opacity. Stable across new spots
// that don't change any square's appearance, so the heat layer is only rebuilt
// when what's on screen actually changes.
function buildGridFingerprint(squareData, filterCtx) {
    const filterKey = `${filterCtx.minSnrMode}:${filterCtx.ssbMinDb}:${filterCtx.cwMinDb}:${filterCtx.selectedBand}:${[...filterCtx.enabledBands].sort().join(',')}:${filterCtx.filterBand || ''}:${filterCtx.filterRegion || ''}`;
    const parts = [];
    for (const loc in squareData) {
        const entry = squareData[loc];
        if (entry.visibleCount <= 0 || entry.count <= 0) continue;
        let dominantBand = 'all', maxCount = 0;
        for (const b in entry.bands) {
            if (entry.bands[b] > maxCount) { maxCount = entry.bands[b]; dominantBand = b; }
        }
        const opacity = gridSnrOpacity(topQuartileMean(entry.snrs)).toFixed(2);
        parts.push(`${loc}:${dominantBand}:${opacity}`);
    }
    parts.sort();
    return `${filterKey}|${parts.join('|')}`;
}

// Create the grid-snr heat layer from a pre-computed aggregate. The caller
// (updateMapVisualization) gates this on buildGridFingerprint, so it only runs
// when the rendered state actually changed.
function renderGridSquares(squareData, filterCtx) {
    const timer = startPerfTimer();
    state.heatLayer = L.layerGroup().addTo(map);
    incrementPerfCounter('mercator.layers.added', 1);

    const drawTimer = startPerfTimer();
    // Item 2: accumulate GeoJSON features, then add as a single layer call
    const gridFeatures = [];

    for (let loc in squareData) {
        if (squareData[loc].visibleCount <= 0 || squareData[loc].count <= 0) continue;

        // Brightness = mean of the strongest quarter of reports, mapped
        // through a continuous ramp. One lucky decode in a sea of weak
        // spots no longer lights up the square.
        const fillOpacity = gridSnrOpacity(topQuartileMean(squareData[loc].snrs));

        let dominantBand = 'all';
        let maxCount = 0;
        for (let b in squareData[loc].bands) {
            if (squareData[loc].bands[b] > maxCount) {
                maxCount = squareData[loc].bands[b];
                dominantBand = b;
            }
        }

        let color = bandColors[dominantBand] || bandColors['all'];

        const bounds = locatorToBounds(loc);
        if (bounds) {
            const [[south, west], [north, east]] = bounds;
            gridFeatures.push({
                type: 'Feature',
                properties: { color, fillOpacity },
                geometry: {
                    type: 'Polygon',
                    coordinates: [[[west, south], [east, south], [east, north], [west, north], [west, south]]]
                }
            });
        }
    }

    // Item 2: single L.geoJSON call replaces N individual L.rectangle().addTo() calls
    if (gridFeatures.length) {
        L.geoJSON({ type: 'FeatureCollection', features: gridFeatures }, {
            renderer: getGridCanvasRenderer(),
            style: f => ({
                color: f.properties.color,
                weight: 1,
                fillColor: f.properties.color,
                fillOpacity: f.properties.fillOpacity
            }),
            interactive: false
        }).addTo(state.heatLayer);
    }

    // DX cluster markers are managed separately (syncDxClusterMarkers) so
    // heatLayer rebuilds don't churn their tooltips.

    endPerfTimer('mercator.grid.draw_ms', drawTimer);
    endPerfTimer('mercator.grid.total_ms', timer);
    incrementPerfCounter('mercator.grid.rectangles_added', gridFeatures.length);
}

// Promise for the most recent async active-area draw; runCaptureBootstrap
// awaits it so the capture-ready handshake covers the hull render (which now
// completes only after the turf fetch).
let activeAreaRebuildPromise = Promise.resolve();
export function whenActiveAreaRendered() {
    return activeAreaRebuildPromise;
}

// Async wrapper around renderActiveArea: load turf on demand (the vendor
// script is no longer part of the initial index.html payload), then draw the
// hulls into the already-created layer and update the band labels. On a turf
// load failure the layer is left empty for this frame instead of crashing the
// render; band pills still recompute from the spot set (they are data, not
// decoration), and the rebuild debounce + backoff let a later frame retry.
function rebuildActiveArea(layer, regularSpots, maxMinutes, filterCtx, spots) {
    activeAreaRebuildPromise = ensureTurf()
        .then(() => {
            const activeBands = renderActiveArea(regularSpots, maxMinutes, filterCtx, layer);
            const bandLabelTimer = startPerfTimer();
            updateBandLabels(spots, filterCtx, activeBands);
            endPerfTimer('mercator.band_labels.total_ms', bandLabelTimer);
        })
        .catch((err) => {
            console.error('Active-area render skipped: turf is unavailable.', err);
            updateBandLabels(spots, filterCtx, null);
        });
    return activeAreaRebuildPromise;
}

function renderActiveArea(regularSpots, maxMinutes, filterCtx, layer) {
    const timer = startPerfTimer();

    // Item 3: use passed filterCtx instead of re-reading DOM
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;
    let maxClusterDist = parseInt(document.getElementById('cluster-distance')?.value, 10);
    if (isNaN(maxClusterDist) || maxClusterDist < 100) maxClusterDist = 500;

    const pointsByBand = {};
    const seenCoordsByBand = {};
    const aggregateTimer = startPerfTimer();

    regularSpots.forEach(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        if (!pointsByBand[spot.band]) {
            pointsByBand[spot.band] = [];
            seenCoordsByBand[spot.band] = new Set();
        }

        const coordKey = `${spot.lng},${spot.lat}`;
        if (!seenCoordsByBand[spot.band].has(coordKey)) {
            seenCoordsByBand[spot.band].add(coordKey);
            // Carry the SNR so the DBSCAN input can be capped to the strongest
            // points (see MAX_ACTIVE_AREA_CLUSTER_POINTS).
            pointsByBand[spot.band].push(turf.point([spot.lng, spot.lat], { snr: Number(spot.snr) || 0 }));
        }
    });
    endPerfTimer('mercator.active_area.aggregate_ms', aggregateTimer);

    let polygonsAdded = 0;
    let markersAdded = 0;
    const clusterTimer = startPerfTimer();

    for (const band in pointsByBand) {
        const allPts = pointsByBand[band];
        const color = bandColors[band] || bandColors['all'];

        // Cap the DBSCAN input: turf.clustersDbscan is O(n²), so a band with
        // thousands of points would take hundreds of ms. Keep the strongest-SNR
        // points for clustering; the capped-out points are drawn as dots below
        // so no spots are lost.
        let pts = allPts;
        let cappedOut = [];
        if (allPts.length > MAX_ACTIVE_AREA_CLUSTER_POINTS) {
            pts = allPts.slice()
                .sort((a, b) => (b.properties?.snr || 0) - (a.properties?.snr || 0))
                .slice(0, MAX_ACTIVE_AREA_CLUSTER_POINTS);
            const cappedSet = new Set(pts);
            cappedOut = allPts.filter((p) => !cappedSet.has(p));
        }

        if (pts.length >= 3) {
            const fc = turf.featureCollection(pts);
            const clustered = turf.clustersDbscan(fc, maxClusterDist, { units: 'kilometers', minPoints: 3 });

            const clusters = {};
            const isolatedPts = [];

            turf.featureEach(clustered, function (point) {
                if (point.properties && point.properties.cluster !== undefined && point.properties.cluster !== null) {
                    const clusterId = point.properties.cluster;
                    if (!clusters[clusterId]) clusters[clusterId] = [];
                    clusters[clusterId].push(point);
                } else {
                    isolatedPts.push(point);
                }
            });

            for (const clusterId in clusters) {
                const clusterPts = clusters[clusterId];
                if (clusterPts.length >= 3) {
                    const clusterFc = turf.featureCollection(clusterPts);
                    let hull;
                    try {
                        // Attempt to create a concave hull (which supports indentations and holes).
                        // If the points are too sparse to form a valid concave hull, fallback to convex.
                        hull = turf.concave(clusterFc, { maxEdge: maxClusterDist * 1.5, units: 'kilometers' }) || turf.convex(clusterFc);
                    } catch (e) {
                        hull = turf.convex(clusterFc);
                    }
                    if (hull) {
                        let finalShape = hull;
                        try {
                            finalShape = turf.polygonSmooth(hull, { iterations: 2 });
                        } catch (e) {
                            console.error("Error smoothing polygon", e);
                        }
                        L.geoJSON(finalShape, {
                            renderer: getGridCanvasRenderer(),
                            style: { color: color, weight: 1, opacity: 0.9, fillColor: color, fillOpacity: 0.18 },
                            interactive: false
                        }).addTo(layer);
                        polygonsAdded += 1;
                    }
                } else {
                    clusterPts.forEach(p => isolatedPts.push(p));
                }
            }

            isolatedPts.forEach(p => {
                addSpotMarker(p, color, layer);
                markersAdded += 1;
            });
        } else {
            pts.forEach(p => {
                addSpotMarker(p, color, layer);
                markersAdded += 1;
            });
        }

        // Capped-out points (weaker SNR) are drawn as dots so the view still
        // shows them, just not as cluster regions.
        cappedOut.forEach(p => {
            addSpotMarker(p, color, layer);
            markersAdded += 1;
        });
    }

    // DX cluster markers are managed separately (syncDxClusterMarkers) so
    // heatLayer rebuilds don't churn their tooltips.

    endPerfTimer('mercator.active_area.cluster_draw_ms', clusterTimer);
    endPerfTimer('mercator.active_area.total_ms', timer);
    incrementPerfCounter('mercator.active_area.polygons_added', polygonsAdded);
    incrementPerfCounter('mercator.active_area.markers_added', markersAdded);
    // Item 6: return bands with data for updateBandLabels
    return new Set(Object.keys(pointsByBand));
}