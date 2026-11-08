import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, getMinSnrMode, getSelectedBand, getEnabledBands, gridSnrOpacity, topQuartileMean, bandColors, locatorToBounds, pillTextColor, regionForLocatorCached } from './utils.js';
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
// events) is safe.
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

// The spots the map's own layers would show under the current filters: Min
// SNR (WSPR exempt), enabled bands, band focus and the matrix
// drill-down. For overlays that draw spots outside the grid/marker layers
// (the timeline afterglow), so they cannot show bands the map hides.
export function visibleMapSpots(spots, filterCtx = buildFilterCtx()) {
    if (!Array.isArray(spots) || spots.length === 0) return [];
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
    return spots.filter((spot) => {
        const src = String(spot?.sourceType || '').toLowerCase();
        const snrExempt = src === 'wspr';
        if (!snrExempt && minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (!snrExempt && minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
        if (enabledBands && !enabledBands.has(spot.band)) return false;
        if (selectedBand && selectedBand !== 'all' && spot.band !== selectedBand) return false;
        if (filterBand && spot.band !== filterBand) return false;
        if (filterRegion && regionForLocatorCached(spot.locator) !== filterRegion) return false;
        return true;
    });
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

function splitSpotSources(spots) {
    const regularSpots = [];
    const wsprSpots = [];
    spots.forEach((spot) => {
        const src = String(spot?.sourceType || '').toLowerCase();
        if (src === 'wspr') {
            wsprSpots.push(spot);
        } else {
            regularSpots.push(spot);
        }
    });
    return { regularSpots, wsprSpots };
}

// --- WSPR beacon markers (region-scoped, distinct style) --------------------
// WSPR spots are a global propagation reference. They're drawn as a separate
// layer with a distinct visual style (smaller, teal/cyan
// band-colored fill with dashed border) so they're clearly separable from regular spots. Region-scoped: only WSPR paths where either end is
// in the operator's DXPulse region are shown, keeping the map relevant.
let wsprMarkerFingerprint = '';

export function clearWsprMarkers() {
    wsprMarkerFingerprint = '';
    if (state.wsprLayer && map) {
        map.removeLayer(state.wsprLayer);
    }
    state.wsprLayer = null;
}

// WSPR markers follow the same band filters as the grid (enabled bands, band
// focus, matrix drill-down) but not the FT8 SNR floors: WSPR SNR is on its own
// scale, and the stream and session ring exempt it from them too.
export function filterWsprMarkerSpots(wsprSpots, filterCtx) {
    if (!filterCtx || wsprSpots.length === 0) return wsprSpots;
    const { selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
    return wsprSpots.filter((spot) => {
        if (enabledBands && !enabledBands.has(spot.band)) return false;
        if (selectedBand && selectedBand !== 'all' && spot.band !== selectedBand) return false;
        if (filterBand && spot.band !== filterBand) return false;
        if (filterRegion && regionForLocatorCached(spot.locator) !== filterRegion) return false;
        return true;
    });
}

function syncWsprMarkers(wsprSpots) {
    if (wsprSpots.length === 0) {
        if (state.wsprLayer) clearWsprMarkers();
        return;
    }

    // The server already scopes WSPR to the operator's area (from-your-QTH
    // only), so no client-side region filter is needed.
    const scopedSpots = wsprSpots;

    // O(1) fingerprint: spots are appended at the end and pruned from the
    // front (never mutated in place), so the first/last spots capture every
    // set change without building a giant join string every frame.
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

// locator is the far end from the operator's side. When it equals the
// reporter (receiver) locator the operator's area holds the transmitter.
function wsprTxLabel(spot) {
    if (spot.locator && spot.locator === spot.reporterLocator) return 'your area';
    return spot.locator || '—';
}

function renderWsprMarkers(wsprSpots) {
    if (!Array.isArray(wsprSpots) || wsprSpots.length === 0 || !state.wsprLayer) return;

    wsprSpots.forEach((spot) => {
        if (!Number.isFinite(spot.lat) || !Number.isFinite(spot.lng)) return;
        // Distinct style: small band-colored dots with a thin dashed border.
        const color = bandColors[spot.band] || bandColors.all;
        const marker = L.circleMarker([spot.lat, spot.lng], {
            color,
            fillColor: color,
            radius: 3,
            weight: 1,
            opacity: 0.8,
            fillOpacity: 0.65,
            dashArray: '3,2',
            interactive: true,
            bubblingMouseEvents: false
        }).addTo(state.wsprLayer);

        marker.bindTooltip(
            `<strong>WSPR beacon</strong><br>Band: ${escapeHtml(spot.band || '—')}<br>SNR: ${Number.isFinite(Number(spot.snr)) ? spot.snr + ' dB' : '—'}<br>TX: ${escapeHtml(wsprTxLabel(spot))}<br>RX: ${escapeHtml(spot.reporterLocator || '—')}`,
            { direction: 'top', offset: [0, -4], opacity: 0.9, sticky: true }
        );
    });
}

// Auto-zoom fits the spread of the visible spots, not their full extent: a
// handful of long-haul spots (one ZL, a few VK/W) would otherwise stretch the
// box to the whole world while nearly all activity sits in one region, and the
// toggle would appear to do nothing. With enough spots, drop the outer
// AUTO_ZOOM_TRIM of each axis; small sets keep every spot.
const AUTO_ZOOM_TRIM = 0.05;
const AUTO_ZOOM_TRIM_MIN_SPOTS = 20;

export function computeAutoZoomBox(spots, filterCtx) {
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;
    const lats = [];
    const lngs = [];
    for (const spot of spots) {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) continue;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) continue;
        if (!enabledBands.has(spot.band)) continue;
        if (selectedBand !== 'all' && spot.band !== selectedBand) continue;
        if (!Number.isFinite(spot.lat) || !Number.isFinite(spot.lng)) continue;
        lats.push(spot.lat);
        lngs.push(spot.lng);
    }
    if (lats.length === 0) return null;

    lats.sort((a, b) => a - b);
    lngs.sort((a, b) => a - b);
    const n = lats.length;
    const cut = n >= AUTO_ZOOM_TRIM_MIN_SPOTS ? Math.floor(n * AUTO_ZOOM_TRIM) : 0;
    return {
        minLat: lats[cut],
        maxLat: lats[n - 1 - cut],
        minLng: lngs[cut],
        maxLng: lngs[n - 1 - cut]
    };
}

export function updateMapVisualization(spots, maxMinutes) {
    if (!map) return;

    const perfEnabled = isPerfProfilingEnabled();
    const renderTimer = startPerfTimer();

    // Item 3: read all filter/style state once
    const checkedStyleRadio = document.querySelector('input[name="style-select"]:checked');
    const style = checkedStyleRadio ? checkedStyleRadio.value : 'grid-snr';
    const filterCtx = buildFilterCtx();

    // Split spot sources once per render (the grid/area pass and
    // syncWsprMarkers both consume the split).
    const { regularSpots, wsprSpots } = splitSpotSources(spots);

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

    syncWsprMarkers(filterWsprMarkerSpots(wsprSpots, filterCtx));

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
        const box = computeAutoZoomBox(spots, filterCtx);
        endPerfTimer('mercator.autozoom.bounds_calc_ms', autoZoomCalcTimer);

        if (box) {
            const fitBoundsTimer = startPerfTimer();
            const bounds = L.latLngBounds([box.minLat, box.minLng], [box.maxLat, box.maxLng]);
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
    lastLabelArgs = [spots, filterCtx, activeBands];
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
    for (const a of activity.values()) for (const v of a.line) if (v > sparkPeak) sparkPeak = v;
    // The dashed "normal for this hour" line, in spots per bin like the trend.
    // It joins the shared peak so it stays on the chart when activity is below it.
    const binMinutes = railSpanMinutes() / SPARK_BINS;
    const normals = new Map();
    for (const band of enabledBands) {
        const perMin = bandNormalRate ? bandNormalRate(band) : null;
        if (Number.isFinite(perMin) && perMin > 0) {
            // Rescale from the -15 dB population to the user's Min SNR filter,
            // using this window's own share of reports that pass the filter.
            const act = activity.get(band);
            const filterShare = act && act.baselineFloorCount > 0 ? act.count / act.baselineFloorCount : 1;
            const perBin = perMin * filterShare * binMinutes;
            normals.set(band, perBin);
            if (perBin > sparkPeak) sparkPeak = perBin;
        }
    }

    updateRailTimeAnchor();

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

        const color = bandColors[band] || '#6c757d';
        pill.style.setProperty('--band', color);
        pill.style.setProperty('--band-on', pillTextColor(color));
        pill.dataset.state = !enabled ? 'off' : (hasData ? 'live' : 'quiet');
        pill.classList.toggle('is-focused', isFocused);
        pill.classList.toggle('is-dimmed', soloing && !shown);

        const count = pill.querySelector('.band-count');
        if (count) count.textContent = enabled ? (act?.count ? String(act.count) : '\u2013') : '';
        const line = pill.querySelector('.band-spark polyline');
        if (line) line.setAttribute('points', enabled && act ? sparkPoints(act.line, sparkPeak) : '');
        const normalLine = pill.querySelector('.band-spark .spark-normal');
        if (normalLine) {
            const perBin = enabled ? normals.get(band) : undefined;
            if (perBin === undefined) {
                normalLine.setAttribute('data-off', '');
            } else {
                const y = sparkY(perBin, sparkPeak).toFixed(1);
                normalLine.setAttribute('y1', y);
                normalLine.setAttribute('y2', y);
                normalLine.removeAttribute('data-off');
            }
        }

        // The solo button (sibling of the enable checkbox, not its parent).
        // Its name stays "Show only <band>"; the live count goes into the
        // aria-describedby text, which screen readers don't re-announce on
        // every update the way they do a changing name.
        const solo = pill.querySelector('.band-solo');
        if (solo) {
            solo.setAttribute('aria-pressed', isFocused ? 'true' : 'false');
            solo.disabled = !enabled; // soloing a disabled band has no effect
        }
        const desc = pill.querySelector(`#band-desc-${band}`);
        if (desc) {
            const n = act?.count || 0;
            desc.textContent = !enabled ? 'band disabled'
                : n === 0 ? 'no spots' : `${n} ${n === 1 ? 'spot' : 'spots'}`;
        }
    });
}

const SPARK_BINS = 15;

// Span of the rail sparklines in minutes: the Max Spot Age slider, or a fixed
// span while a replay feeds the rail (the timeline's moments always cover
// MOMENT_WINDOW_SECONDS, so a 60 min slider left 3/4 of every line empty).
let railSpanOverrideMinutes = null;
export function setRailSpanOverride(minutes) {
    railSpanOverrideMinutes = Number.isFinite(minutes) && minutes > 0 ? minutes : null;
    updateRailTimeAnchor();
}
function railSpanMinutes() {
    if (railSpanOverrideMinutes) return railSpanOverrideMinutes;
    return Number(typeof document !== 'undefined' ? document.getElementById('minutes')?.value : NaN) || 15;
}

// "15 min", "1 h": the span of the sparklines (max spot age).
export function sparkSpanLabel(minutes) {
    const m = Math.max(1, Math.round(Number(minutes) || 15));
    return m >= 60 && m % 60 === 0 ? `${m / 60} h` : `${m} min`;
}

// The rail's time scale: the label at the foot of the hairline that marks
// where the sparklines start.
function updateRailTimeAnchor() {
    const span = sparkSpanLabel(railSpanMinutes());
    const label = document.querySelector('.band-rail-time-span');
    if (label) label.textContent = span;
    const row = document.querySelector('.band-rail-time');
    if (row) row.title = `Sparklines: the last ${span}`;
}

// The label also follows the slider directly (the next render may be a while
// off, or never come without spots) and is set once the controls exist.
if (typeof document !== 'undefined') {
    document.addEventListener('input', (e) => {
        if (e.target?.id === 'minutes') updateRailTimeAnchor();
    });
    document.addEventListener('DOMContentLoaded', updateRailTimeAnchor);
}

// Per-band spot count + a SPARK_BINS histogram over the max-spot-age window,
// anchored on the newest spot so it also works for a timeline moment.
// Applies the SNR floor but not focus.
// SNR floor dx_conditions counts baseline_local_scale at (its default cw_min_db;
// band-lab does not send one).
const BASELINE_LOCAL_FLOOR_DB = -15;

export function bandActivity(spots, ctx) {
    const out = new Map();
    if (!Array.isArray(spots) || spots.length === 0) return out;
    let tMax = -Infinity;
    for (const s of spots) {
        const t = spotEpochSeconds(s);
        if (t > tMax) tMax = t;
    }
    const span = 60 * railSpanMinutes();
    const t0 = tMax - span;
    for (const s of spots) {
        let a = out.get(s.band);
        if (!a) { a = { count: 0, baselineFloorCount: 0, bins: new Array(SPARK_BINS).fill(0) }; out.set(s.band, a); }
        // The band's normal rate (dx_conditions baseline_local_scale) is counted
        // at the backend's default -15 dB floor; tally that population too so
        // the dashed line can be put on the same SNR filter as the live line.
        if (s.snr >= BASELINE_LOCAL_FLOOR_DB) a.baselineFloorCount += 1;
        if (ctx.minSnrMode === 'ssb' && s.snr < ctx.ssbMinDb) continue;
        if (ctx.minSnrMode === 'cw' && s.snr < ctx.cwMinDb) continue;
        a.count += 1;
        const i = Math.min(SPARK_BINS - 1, Math.max(0, Math.floor(((spotEpochSeconds(s) - t0) / span) * SPARK_BINS)));
        a.bins[i] += 1;
    }
    // Bands whose reports all fail the Min SNR filter stay absent, as before;
    // their -15 dB tally is only needed for bands that do have a live line.
    for (const [band, a] of out) if (a.count === 0) out.delete(band);
    // Reporters upload in ~2-minute batches, so raw 1-minute bins zig-zag
    // (58 8 50 0 ...). The drawn line is a centred 3-bin moving average.
    for (const a of out.values()) {
        a.line = a.bins.map((_, i) => {
            const lo = Math.max(0, i - 1);
            const hi = Math.min(a.bins.length - 1, i + 1);
            let sum = 0;
            for (let j = lo; j <= hi; j += 1) sum += a.bins[j];
            return sum / (hi - lo + 1);
        });
    }
    return out;
}

// Spot time in epoch seconds. Timeline spots carry an absolute `t`; live
// stream spots only carry their server age at receipt (__recvAge) and the
// client clock at receipt (__recvMs), see app.js.
function spotEpochSeconds(s) {
    if (Number.isFinite(s.t)) return s.t;
    const age = s.__recvAge ?? s.ageSeconds ?? 0;
    const recvMs = Number.isFinite(s.__recvMs) ? s.__recvMs : Date.now();
    return recvMs / 1000 - age;
}

// Polyline points for a 60x16 viewBox. Square-root scale against the busiest
// bin across all bands keeps quiet bands visible without exaggerating them.
function sparkY(v, peak) {
    return 15 - Math.sqrt(v / peak) * 14;
}

function sparkPoints(bins, peak) {
    const step = 60 / (bins.length - 1);
    return bins.map((v, i) => `${(i * step).toFixed(1)},${sparkY(v, peak).toFixed(1)}`).join(' ');
}

// Source of each band's normal rate (spots/min for the current time of day),
// or null when unknown. Wired by app.js from band-lab.js; kept injectable so
// this module does not depend on the Conditions dock.
let bandNormalRate = null;
export function setBandNormalRateProvider(fn) {
    bandNormalRate = typeof fn === 'function' ? fn : null;
}

// Redraw the rail with the last inputs; used when only the normal rates changed.
let lastLabelArgs = null;
export function refreshBandLabels() {
    if (lastLabelArgs) updateBandLabels(...lastLabelArgs);
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

    endPerfTimer('mercator.grid.draw_ms', drawTimer);
    endPerfTimer('mercator.grid.total_ms', timer);
    incrementPerfCounter('mercator.grid.rectangles_added', gridFeatures.length);
}

// Promise for the most recent async active-area draw.
let activeAreaRebuildPromise = Promise.resolve();

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
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
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
        // Matrix drill-down (band x region), as in the grid style.
        if (filterBand && spot.band !== filterBand) return;
        if (filterRegion && regionForLocatorCached(spot.locator) !== filterRegion) return;

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

    endPerfTimer('mercator.active_area.cluster_draw_ms', clusterTimer);
    endPerfTimer('mercator.active_area.total_ms', timer);
    incrementPerfCounter('mercator.active_area.polygons_added', polygonsAdded);
    incrementPerfCounter('mercator.active_area.markers_added', markersAdded);
    // Item 6: return bands with data for updateBandLabels
    return new Set(Object.keys(pointsByBand));
}