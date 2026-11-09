import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, getMinSnrMode, getSelectedBand, getEnabledBands, gridSnrOpacity, topQuartileMean, bandColors, locatorToBounds, pillTextColor, regionForLocatorCached } from './utils.js';
import { endPerfTimer, incrementPerfCounter, isPerfProfilingEnabled, startPerfTimer } from './perf.js';
import { aggregateSpots, buildGeoBandField, buildTerritoryShapes, drawDots, drawTerritoryShapes, loneDotPoints } from './active-area.js';

// Rendered-state fingerprint for the grid-snr heat layer. Unlike the old
// spot-list fingerprint (spots.length + first/last ageSeconds — which changes
// on every arriving spot and every 5s prune), this keys on the actual squares
// and their appearance, so a new spot that doesn't change any square's
// dominant band or quantized opacity doesn't tear down and rebuild the layer.
let lastGridFingerprint = '';
// The active-area layer (density field + contour rings, see active-area.js)
// is rebuilt at most this often while spots stream in. A change of filters,
// bands or the area slider rebuilds at once (see activeAreaKey).
const ACTIVE_AREA_REBUILD_INTERVAL_MS = 2000;

// Canvas renderer for the Mercator Grid-SNR non-interactive polygon
// overlay. SVG re-projects every path on each zoomend; with many
// grid squares (and zoomSnap:0 firing several fractional zoomends per net
// zoom level) that is the dominant zoom-time cost. Canvas redraws all paths
// in one batched paint instead. Grid squares are already interactive:false, so canvas (which can't do per-path mouse
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
        // The field + ring build is the expensive part (tens of ms on a
        // contest-sized feed), so new spots rebuild at most every
        // ACTIVE_AREA_REBUILD_INTERVAL_MS; the layer itself redraws cheaply on
        // pan/zoom from its cached rings. The marker layers still update
        // every render.
        const now = Date.now();
        const radiusKm = activeAreaRadiusKm();
        const key = activeAreaKey(filterCtx, radiusKm);
        if (!state.heatLayer || key !== state.lastActiveAreaKey || (now - (state.lastActiveAreaRebuildAt || 0)) >= ACTIVE_AREA_REBUILD_INTERVAL_MS) {
            state.lastActiveAreaRebuildAt = now;
            state.lastActiveAreaKey = key;
            if (state.heatLayer) {
                map.removeLayer(state.heatLayer);
                incrementPerfCounter('mercator.layers.removed', 1);
            }
            incrementPerfCounter('mercator.render.style.active_area', 1);
            const filtered = filterActiveAreaSpots(regularSpots, filterCtx);
            state.heatLayer = createTerritoryLayer(filtered, radiusKm).addTo(map);
            incrementPerfCounter('mercator.layers.added', 1);
            updateBandLabels(spots, filterCtx, new Set(filtered.map((s) => s.band)));
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

// The spots the active-area layer draws: the same filters as the grid style.
function filterActiveAreaSpots(regularSpots, filterCtx) {
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
    return regularSpots.filter((spot) => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
        if (!enabledBands.has(spot.band)) return false;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
        if (filterBand && spot.band !== filterBand) return false;
        if (filterRegion && regionForLocatorCached(spot.locator) !== filterRegion) return false;
        return true;
    });
}

// Kernel radius of the active-area density field: 0.8 x the "Area smoothing"
// slider (id cluster-distance, kept for stored settings; default 500 km).
function activeAreaRadiusKm() {
    let v = parseInt(document.getElementById('cluster-distance')?.value, 10);
    if (isNaN(v) || v < 100) v = 500;
    return Math.round(v * 0.8);
}

// Everything that changes WHICH spots or HOW they are smoothed: a change
// rebuilds the layer at once instead of waiting for the rebuild interval.
function activeAreaKey(filterCtx, radiusKm) {
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, filterBand, filterRegion } = filterCtx;
    return `${radiusKm}|${minSnrMode}|${ssbMinDb}|${cwMinDb}|${selectedBand}|${filterBand}|${filterRegion}|${[...enabledBands].sort().join(',')}`;
}

// Active-area territories (see active-area.js) as a non-interactive canvas in
// the overlay pane, redrawn on moveend/zoomend/resize from the spot set
// captured at rebuild time. Contour rings and the lone-dot list are cached
// per grid size in lat/lng, so a pan only re-projects them and a zoom
// re-samples only when the grid size changes.
function createTerritoryLayer(filteredSpots, radiusKm) {
    const aggregateTimer = startPerfTimer();
    const pts = aggregateSpots(filteredSpots);
    endPerfTimer('mercator.active_area.aggregate_ms', aggregateTimer);
    const shapesByCell = new Map();
    // A fine grid over the whole data extent would be millions of cells
    // (world-wide feed, zoomed in), so above MAX_FIELD_CELLS the field covers
    // only the view plus a margin, and is rebuilt when the view leaves the
    // covered area.
    const MAX_FIELD_CELLS = 250000;
    const padLatDeg = radiusKm / 110.57;
    let extent = null;
    for (const p of pts) {
        if (!extent) extent = { s: p.lat, n: p.lat, w: p.lng, e: p.lng };
        else {
            if (p.lat < extent.s) extent.s = p.lat;
            if (p.lat > extent.n) extent.n = p.lat;
            if (p.lng < extent.w) extent.w = p.lng;
            if (p.lng > extent.e) extent.e = p.lng;
        }
    }
    const build = (cellDeg, subset) => {
        const timer = startPerfTimer();
        const field = buildGeoBandField(subset, { cellDeg, radiusKm });
        const entry = { shapes: buildTerritoryShapes(field), lone: loneDotPoints(field, subset) };
        endPerfTimer('mercator.active_area.build_ms', timer);
        incrementPerfCounter('mercator.active_area.builds', 1);
        return entry;
    };
    const shapesFor = (cellDeg, view) => {
        const hit = shapesByCell.get(cellDeg);
        // Rough size of the full-extent grid (longitude padding taken as
        // twice the latitude padding, the value near 60 deg N).
        const fullCells = extent
            ? ((extent.e - extent.w + 4 * padLatDeg) / cellDeg) * ((extent.n - extent.s + 2 * padLatDeg) / cellDeg)
            : 0;
        if (fullCells <= MAX_FIELD_CELLS) {
            if (hit) return hit;
            const entry = build(cellDeg, pts);
            shapesByCell.set(cellDeg, entry);
            return entry;
        }
        const vs = view.getSouth(), vn = view.getNorth(), vw = view.getWest(), ve = view.getEast();
        if (hit && hit.cover && vs >= hit.cover.s && vn <= hit.cover.n && vw >= hit.cover.w && ve <= hit.cover.e) return hit;
        // Cover the view plus half a view on each side; take points one
        // kernel radius further out so densities inside the cover are exact.
        const dLat = (vn - vs) / 2, dLng = (ve - vw) / 2;
        const cover = { s: vs - dLat, n: vn + dLat, w: vw - dLng, e: ve + dLng };
        const padLng = padLatDeg / Math.max(0.15, Math.cos(Math.min(80, Math.max(Math.abs(cover.s), Math.abs(cover.n))) * Math.PI / 180));
        const subset = pts.filter((p) => p.lat >= cover.s - padLatDeg && p.lat <= cover.n + padLatDeg
            && p.lng >= cover.w - padLng && p.lng <= cover.e + padLng);
        const entry = { ...build(cellDeg, subset), cover };
        shapesByCell.set(cellDeg, entry);
        return entry;
    };
    const TerritoryLayer = L.Layer.extend({
        onAdd(m) {
            this._m = m;
            this._c = L.DomUtil.create('canvas', 'leaflet-zoom-hide active-area-layer');
            this._c.style.pointerEvents = 'none';
            m.getPanes().overlayPane.appendChild(this._c);
            m.on('moveend zoomend resize', this._redraw, this);
            this._redraw();
        },
        onRemove(m) {
            m.off('moveend zoomend resize', this._redraw, this);
            this._c.remove();
        },
        _redraw() {
            const timer = startPerfTimer();
            const m = this._m;
            // ~7 px per grid cell at the current zoom, snapped to a few sizes
            // so small zoom changes reuse the cached shapes.
            const target = 7 * 360 / (256 * Math.pow(2, m.getZoom()));
            const cellDeg = [0.125, 0.25, 0.5, 1, 2].find((d) => d >= target) || 2;
            const entry = shapesFor(cellDeg, m.getBounds());
            const size = m.getSize();
            const dpr = window.devicePixelRatio || 1;
            const c = this._c;
            c.width = Math.round(size.x * dpr);
            c.height = Math.round(size.y * dpr);
            c.style.width = `${size.x}px`;
            c.style.height = `${size.y}px`;
            L.DomUtil.setPosition(c, m.containerPointToLayerPoint([0, 0]));
            const ctx = c.getContext('2d');
            if (!ctx) return;
            ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
            ctx.clearRect(0, 0, size.x, size.y);
            const project = (lat, lng) => {
                const p = m.latLngToContainerPoint([lat, lng]);
                return { x: p.x, y: p.y };
            };
            drawTerritoryShapes(ctx, entry.shapes, project, { pixelRatio: 1 });
            drawDots(ctx, entry.lone, project, 3.5);
            endPerfTimer('mercator.active_area.draw_ms', timer);
        },
    });
    return new TerritoryLayer();
}
