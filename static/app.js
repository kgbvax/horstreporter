import { state } from './state.js';
import { loadConfig } from './config.js';
import { isChaseQueueEnabled } from './cq-flag.js';
import { initMap, setTheme, map, syncMercatorCountryLayer, syncMercatorGraylineLayer, syncMercatorDxccLabelLayer, setMercatorDxHighlight, clearMercatorDxHighlight } from './map.js';
import { initAzimuthCanvas, isAzimuthEnabled, loadAzimuthWorldGeoJson, renderAzimuthScene, setAzimuthCenter, getAzimuthCenter, setAzimuthEnabled, setAzimuthDragging, setAzimuthTheme, setAzimuthZoom, clampAzimuthZoom, setAzimuthHorizonKm, clampAzimuthHorizonKm, setAzimuthNs6tIndicatorEnabled, setAzimuthDxccLabelDensity, setAzimuthDxccLabelsEnabled, getAzimuthLatLngFromClientPoint, getAzimuthHiddenGridSquaresCount, setAzimuthDxSpotHighlight } from './azimuth-runtime.js';
import { initUI, attachUITooltipEvents } from './ui.js';
import { getBandLabLookbackMinutes, initBandLab, updateBandLab } from './band-lab.js';
import { initWsprMatrix, updateWsprMatrix, clearDrillDown, updateDrillDownButton } from './wspr-matrix.js';
import { initHotBandIndicator } from './hot-band-indicator.js';
import { initHorstKevin } from './horst-kevin.js';
import { initPushUI } from './push.js';
import { updateMapVisualization, updateBandLabels, clearDxClusterMarkers, clearWsprMarkers, resetRenderFingerprint, whenActiveAreaRendered } from './renderers.js';
import { latLngToLocator, locatorToBounds, normalizeLongitude, setFaviconColor, getMinSnrMode, getEnabledBands, getSelectedBand, formatNumber, bandColors, getCountryColoringEnabled, pillTextColor, setSubmitMode, isStreaming, icon } from './utils.js';
import { endPerfTimer, incrementPerfCounter, installPerfDebugApi, perfNow, startPerfTimer } from './perf.js';
import { initOpMode, isOpModeActive, setBeamTargetFromMapClick, getOpModeStation } from './opmode.js';
import { isTimelineActive, enterTimeline, exitTimeline, seek, play as timelinePlay, pause as timelinePause, onMoment as onTimelineMoment, onExit as onTimelineExit, syncTimelineURL, readTimelineURL, invalidateBundles as invalidateTimelineBundles, refreshMoment as refreshTimelineMoment } from './timeline.js';
import { updateAfterglow, notifyMapMoved, hideAfterglow } from './afterglow.js';
import { GRAYLINE_BUCKET_MS, setDataNowMs, clearDataNowOverride } from './data-now.js';
import { sessionRing } from './session-ring.js';
import { STREAM_STATUS_TEXT, setStreamStatus, spotCountText, liveForText, connectingText, serverErrorText, timeTravelErrorText } from './stream-status.js';

// --- Azimuth Zoom State ---
const AZIMUTH_MAX_HORIZON_KM = 20015;
const AZIMUTH_MIN_ZOOM = 1.0;
const AZIMUTH_MAX_ZOOM = 5.0;
// Cap on state.liveSpots to bound memory between 5s age-prune ticks. A hot
// band can push thousands of spots/sec; without this the array grows
// unbounded until the prune runs. Drop oldest-arrived in a batch when over.
const MAX_LIVE_SPOTS = 20000;
let suppressAzimuthClickUntil = 0;
let hotBandIndicator = null;
let horstKevin = null;
// Horst-Kevin mascot temporarily disabled (to be revised). Set true to re-enable;
// the #horst-kevin element in index.html is also hidden via inline display:none.
const HORST_KEVIN_ENABLED = false;

// Tab favicon status dot. Same semantics as the --status-* tokens in style.css
// (live = brand teal, waiting = amber, error = danger fill); the tab bar is
// outside the page theme, so these are fixed mid-tones that read on light and
// dark browser chrome.
const FAVICON = { idle: '#6c757d', waiting: '#e0a000', live: '#1f9e8f', error: '#c62828' };

// --- Band selector (pills) ---------------------------------------------------
// Band order matches the panel layout. Focus = solo band ('all' = no solo),
// stored on #band-container[data-focus-band]; enabled = checkbox set. The two
// are independent (see redesign spec).

function setBandFocus(band) {
    const c = document.getElementById('band-container');
    if (c) c.dataset.focusBand = (!band || band === 'all') ? '' : band;
}

// Re-style pills immediately on interaction (the render loop also calls
// updateBandLabels, but this gives instant feedback before the throttled render).
// Timeline gate (KTD-10): mid-replay this must not paint the live array over
// the moment; the pills restyle via the moment re-render in scheduleRender.
function refreshBandPills() {
    if (isTimelineActive()) return;
    try { updateBandLabels(getRenderableMapSpots(state.liveSpots)); } catch (_) { /* pre-init */ }
}

// Shared side effects for any focus/enable change (mirrors the old
// band-container change handler).
function applyBandChange() {
    localStorage.setItem('selectedBand', getSelectedBand());
    updateCurrentBandDisplay();
    updateBandLab({ force: true });
    updateWsprMatrix();
    scheduleRender();
    hotBandIndicator?.rerender();
    hotBandIndicator?.refresh();
    horstKevin?.refresh();
    refreshBandPills();
}

// Returns true if the current control settings (enabled bands + SNR filter)
// differ from what the active SSE connection is already fetching. When they
// match, a filter change can be handled purely client-side without discarding
// data or hitting the server.
function streamFilterNeedsReconnect() {
    const enabled = getEnabledBands();
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = minSnrMode === 'ssb' ? (document.getElementById('ssb-min-db')?.value || '0') : null;
    const cwMinDb = minSnrMode === 'cw' ? (document.getElementById('cw-min-db')?.value || '-15') : null;
    const current = state.streamedFilter;
    if (!current) return true;
    for (const band of enabled) {
        if (!current.bands.has(band)) return true;
    }
    if (minSnrMode !== current.minSnrMode) return true;
    if (ssbMinDb !== current.ssbMinDb) return true;
    if (cwMinDb !== current.cwMinDb) return true;
    return false;
}

// Re-connect the live stream when band/SNR filters change ONLY if the new
// filter requires data we aren't already receiving. Disabling bands is a
// client-side render filter; enabling a previously-unseen band or changing the
// SNR threshold restarts the stream but preserves existing spots so the map
// never flashes empty.
function restartStreamIfSubscribed() {
    const btnSubmit = document.getElementById('btn-submit');
    if (!btnSubmit || !isStreaming(btnSubmit)) return;

    if (isTimelineActive()) {
        // KTD-12: mid-timeline band/SNR changes never restart the stream. The
        // bundle LRU is dropped so the next moment re-synthesizes against the
        // new filter, and the current moment is re-emitted: a narrowing
        // re-slices from the ring (band-disable / SNR-raise), a widening falls
        // through to the archive path for that chunk (AE2). Either way the
        // stream is untouched; a widening is reconciled with the stream filter
        // on exit (stopTimelineMode -> restartStreamIfSubscribed).
        invalidateTimelineBundles();
        void refreshTimelineMoment().catch((err) => console.warn('timeline moment refresh failed', err));
        return;
    }

    if (!streamFilterNeedsReconnect()) {
        // Server already sends everything matching the current filter; just
        // re-apply it client-side.
        updateBandLab({ force: true });
        updateWsprMatrix();
        scheduleRender();
        return;
    }

    // Filter changed in a way the server needs to know about. Restart while
    // keeping current data visible. The ring must not survive the restart:
    // its windows were delivered under the old narrower filter, so keeping it
    // would let the cohort guard vouch those windows with the new wider
    // filter and ring-served chunks would silently miss the newly enabled
    // band's spots. The new connection's dump re-seeds the recent window;
    // older windows fall through to /api/history.
    sessionRing.clear();
    startLiveStream(true);
}

// Focus a band programmatically (hot-band indicator / horst-kevin onBandSwitch).
function switchToBand(band) {
    if (!getEnabledBands().has(band)) return;
    setBandFocus(band);
    applyBandChange();
}

function parseBoolParam(raw, fallback = false) {
    if (raw == null || raw === '') return fallback;
    const normalized = String(raw).trim().toLowerCase();
    return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
}

function parseCaptureConfig() {
    const params = new URLSearchParams(window.location.search || '');
    const enabled = parseBoolParam(params.get('capture'), false) || params.get('mode') === 'capture';
    if (!enabled) return null;

    const qth = (params.get('qth') || 'JO32').trim().toUpperCase();
    const minutes = Math.max(1, Math.min(720, Number.parseInt(params.get('minutes') || '15', 10) || 15));
    const snapshotAt = Number.parseInt(params.get('snapshot_at') || '', 10);

    const projection = (params.get('projection') || 'mercator').trim().toLowerCase() === 'azimuthal' ? 'azimuthal' : 'mercator';
    const style = (params.get('style') || 'active-area').trim().toLowerCase() === 'grid-snr' ? 'grid-snr' : 'active-area';
    const minSnrMode = (params.get('min_snr_mode') || 'ssb').trim().toLowerCase();
    const selectedBand = (params.get('selected_band') || 'all').trim().toLowerCase();
    const enabledBandsCsv = (params.get('enabled_bands') || '').trim();
    const includeDxcluster = parseBoolParam(params.get('include_dxcluster'), false);

    return {
        enabled: true,
        qth,
        minutes,
        snapshotAt: Number.isFinite(snapshotAt) ? snapshotAt : null,
        surroundings: parseBoolParam(params.get('surroundings'), false),
        projection,
        style,
        minSnrMode: minSnrMode === 'cw' || minSnrMode === 'ssb' ? minSnrMode : 'none',
        ssbMinDb: Number.parseInt(params.get('ssb_min_db') || '0', 10),
        cwMinDb: Number.parseInt(params.get('cw_min_db') || '-15', 10),
        selectedBand,
        enabledBandsCsv,
        includeDxcluster,
        countryColoring: parseBoolParam(params.get('country_coloring'), true),
        dk3jfMode: parseBoolParam(params.get('dk3jf_mode'), true)
    };
}

const captureConfig = parseCaptureConfig();
if (captureConfig?.enabled) {
    window.__horstCaptureReady = false;
}
// Expose the parsed capture config so the Svelte UI bundle can seed its store
// before mounting controls, preventing a race where Svelte defaults overwrite
// URL-driven projection/style/target/etc.
window.__horstCaptureConfig = captureConfig;

const storedAzimuthZoomRaw = localStorage.getItem('azimuthZoom');
const storedAzimuthHorizonKmRaw = localStorage.getItem('azimuthHorizonKm');
const storedAzimuthZoom = storedAzimuthZoomRaw === null ? Number.NaN : Number(storedAzimuthZoomRaw);
const storedAzimuthHorizonKm = storedAzimuthHorizonKmRaw === null ? Number.NaN : Number(storedAzimuthHorizonKmRaw);
let azimuthZoom = clampAzimuthZoom(
    Number.isFinite(storedAzimuthZoom)
        ? storedAzimuthZoom
        : Number.isFinite(storedAzimuthHorizonKm)
            ? (AZIMUTH_MAX_HORIZON_KM / clampAzimuthHorizonKm(storedAzimuthHorizonKm))
            : 1.5
);

function getAzimuthHorizonKmForZoom(zoom) {
    const safeZoom = Math.max(AZIMUTH_MIN_ZOOM, Number(zoom) || 1.5);
    return clampAzimuthHorizonKm(AZIMUTH_MAX_HORIZON_KM / safeZoom);
}

function toRadians(value) {
    return (Number(value) || 0) * (Math.PI / 180);
}

function angularDistanceRad(aLat, aLng, bLat, bLng) {
    const lat1 = toRadians(aLat);
    const lat2 = toRadians(bLat);
    const dLat = lat2 - lat1;
    const dLng = toRadians((Number(bLng) || 0) - (Number(aLng) || 0));

    const sinDLat = Math.sin(dLat / 2);
    const sinDLng = Math.sin(dLng / 2);
    const h = (sinDLat * sinDLat) + (Math.cos(lat1) * Math.cos(lat2) * sinDLng * sinDLng);
    return 2 * Math.atan2(Math.sqrt(h), Math.sqrt(Math.max(0, 1 - h)));
}

function getAzimuthFilteredRenderableSpots() {
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();

    return getRenderableMapSpots(state.liveSpots).filter((spot) => {
        if (!Number.isFinite(Number(spot?.lat)) || !Number.isFinite(Number(spot?.lng))) return false;
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
        if (!enabledBands.has(spot.band)) return false;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
        return true;
    });
}

function fitAzimuthToAllRenderableSpots() {
    const spots = getAzimuthFilteredRenderableSpots();
    if (!spots.length) return;

    const avgLat = spots.reduce((acc, s) => acc + Number(s.lat), 0) / spots.length;
    const meanSin = spots.reduce((acc, s) => acc + Math.sin(toRadians(Number(s.lng))), 0) / spots.length;
    const meanCos = spots.reduce((acc, s) => acc + Math.cos(toRadians(Number(s.lng))), 0) / spots.length;
    const avgLng = normalizeLongitude(Math.atan2(meanSin, meanCos) * (180 / Math.PI));
    const center = [Math.max(-89.5, Math.min(89.5, avgLat)), avgLng];

    let maxAngularDistance = 0;
    for (const spot of spots) {
        const d = angularDistanceRad(center[0], center[1], Number(spot.lat), Number(spot.lng));
        if (Number.isFinite(d) && d > maxAngularDistance) {
            maxAngularDistance = d;
        }
    }

    // Apply a small margin so outermost spots are not clipped at the horizon edge.
    const fitAngular = Math.max(0.01, maxAngularDistance * 1.08);
    const targetZoom = clampAzimuthZoom(Math.PI / fitAngular);

    setAzimuthCenter(center);
    updateAzimuthZoom(targetZoom);
}

function syncAzimuthZoomUi() {
    const horizonInput = document.getElementById('azimuth-horizon-km');
    if (horizonInput) {
        const horizonKm = getAzimuthHorizonKmForZoom(azimuthZoom);
        const horizonRounded = Math.round(horizonKm);
        if (Number(horizonInput.value) !== horizonRounded) {
            horizonInput.value = String(horizonRounded);
        }
    }

    const zoomOutButton = document.getElementById('azimuth-zoom-out');
    if (zoomOutButton) {
        zoomOutButton.disabled = azimuthZoom <= AZIMUTH_MIN_ZOOM + 1e-6;
    }

    const zoomInButton = document.getElementById('azimuth-zoom-in');
    if (zoomInButton) {
        zoomInButton.disabled = azimuthZoom >= AZIMUTH_MAX_ZOOM - 1e-6;
    }

    syncAzimuthZoomOutHint();
}

function syncAzimuthZoomOutHint() {
    const zoomOutButton = document.getElementById('azimuth-zoom-out');
    if (!zoomOutButton) return;

    const hasHiddenSquares = isAzimuthEnabled() && getAzimuthHiddenGridSquaresCount() > 0;
    zoomOutButton.classList.toggle('azimuth-zoom-hint', hasHiddenSquares);
    if (hasHiddenSquares) {
        zoomOutButton.title = 'Zoom out to reveal additional squares currently outside the viewport';
    } else {
        zoomOutButton.title = 'Zoom out';
    }
}

function updateAzimuthZoom(newZoom) {
    azimuthZoom = clampAzimuthZoom(newZoom);
    localStorage.setItem('azimuthZoom', azimuthZoom);
    setAzimuthZoom(azimuthZoom);
    const horizonKm = getAzimuthHorizonKmForZoom(azimuthZoom);
    localStorage.setItem('azimuthHorizonKm', horizonKm);
    setAzimuthHorizonKm(horizonKm);
    syncAzimuthZoomUi();
    if (isAzimuthEnabled()) scheduleRender();
}

function updateAzimuthHorizonKm(newHorizonKm) {
    const horizonKm = clampAzimuthHorizonKm(newHorizonKm);
    const derivedZoom = clampAzimuthZoom(AZIMUTH_MAX_HORIZON_KM / horizonKm);
    updateAzimuthZoom(derivedZoom);
}

function attachProjectionGestureZoomEvents() {
    const azimuthCanvas = document.getElementById('azimuth-canvas');
    const canvases = [azimuthCanvas].filter(Boolean);
    if (!canvases.length) return;

    const AZIMUTH_DRAG_CLICK_SUPPRESS_MS = 220;
    const AZIMUTH_DRAG_MOVE_THRESHOLD_PX = 4;

    const touchZoomState = {
        active: false,
        startDistance: 0,
        startZoom: 1
    };

    const dragState = {
        active: false,
        startClientX: 0,
        startClientY: 0,
        startCenterLat: 0,
        startCenterLng: 0,
        moved: false
    };

    const setDraggingCursor = (dragging) => {
        if (!azimuthCanvas) return;
        azimuthCanvas.style.cursor = 'default';
    };

    const beginDrag = (clientX, clientY) => {
        if (!isAzimuthEnabled()) return;

        const canvasCenter = getAzimuthCenter();
        setAzimuthDragging(true);

        dragState.active = true;
        dragState.startClientX = clientX;
        dragState.startClientY = clientY;
        dragState.startCenterLat = Number(canvasCenter[0]) || 0;
        dragState.startCenterLng = Number(canvasCenter[1]) || 0;
        dragState.moved = false;
        setDraggingCursor(true);
    };

    const updateDrag = (clientX, clientY) => {
        if (!dragState.active || !isAzimuthEnabled()) return;

        const movedPx = Math.hypot(clientX - dragState.startClientX, clientY - dragState.startClientY);
        if (movedPx >= AZIMUTH_DRAG_MOVE_THRESHOLD_PX) {
            dragState.moved = true;
        }

        const dx = clientX - dragState.startClientX;
        const dy = clientY - dragState.startClientY;

        const canvas = azimuthCanvas;
        const width = canvas?.clientWidth || canvas?.offsetWidth || 1;
        const height = canvas?.clientHeight || canvas?.offsetHeight || 1;
        const radiusBase = Math.min(width, height) * 0.47;
        const radPerPx = radiusBase > 0 ? (Math.PI / (radiusBase * azimuthZoom)) : 0;

        const deltaLatDeg = (dy * radPerPx) * (180 / Math.PI);
        const baseLatRad = (dragState.startCenterLat * Math.PI) / 180;
        const cosBaseLat = Math.max(0.15, Math.abs(Math.cos(baseLatRad)));
        const deltaLngDeg = ((dx * radPerPx) * (180 / Math.PI)) / cosBaseLat;

        const nextLat = Math.max(-89.5, Math.min(89.5, dragState.startCenterLat + deltaLatDeg));
        const nextLng = normalizeLongitude(dragState.startCenterLng - deltaLngDeg);
        setAzimuthCenter([nextLat, nextLng]);
        scheduleRender();
    };

    const endDrag = () => {
        if (!dragState.active) return;

        setAzimuthDragging(false);

        if (dragState.moved) {
            suppressAzimuthClickUntil = Date.now() + AZIMUTH_DRAG_CLICK_SUPPRESS_MS;
        }

        dragState.active = false;
        dragState.startClientX = 0;
        dragState.startClientY = 0;
        dragState.startCenterLat = 0;
        dragState.startCenterLng = 0;
        dragState.moved = false;
        setDraggingCursor(false);
    };

    const touchDistance = (touches) => {
        if (!touches || touches.length < 2) return 0;
        const dx = touches[0].clientX - touches[1].clientX;
        const dy = touches[0].clientY - touches[1].clientY;
        return Math.hypot(dx, dy);
    };

    const onWheel = (e) => {
        if (!isAzimuthEnabled()) return;

        e.preventDefault();

        const baseStep = 0.16;
        const magnitude = Math.max(0.5, Math.min(3.0, Math.abs(e.deltaY) / 120));
        const step = baseStep * magnitude;

        if (e.deltaY < 0) {
            updateAzimuthZoom(azimuthZoom + step);
        } else if (e.deltaY > 0) {
            updateAzimuthZoom(azimuthZoom - step);
        }
    };

    const onTouchStart = (e) => {
        if (!isAzimuthEnabled()) {
            touchZoomState.active = false;
            endDrag();
            return;
        }

        if (e.touches.length === 1) {
            touchZoomState.active = false;
            beginDrag(e.touches[0].clientX, e.touches[0].clientY);
            e.preventDefault();
            return;
        }

        if (e.touches.length !== 2) {
            touchZoomState.active = false;
            endDrag();
            return;
        }

        endDrag();

        const distance = touchDistance(e.touches);
        if (!distance) {
            touchZoomState.active = false;
            return;
        }

        touchZoomState.active = true;
        touchZoomState.startDistance = distance;
        touchZoomState.startZoom = azimuthZoom;
        e.preventDefault();
    };

    const onTouchMove = (e) => {
        if (dragState.active && e.touches.length === 1) {
            updateDrag(e.touches[0].clientX, e.touches[0].clientY);
            e.preventDefault();
            return;
        }

        if (!touchZoomState.active || e.touches.length !== 2) return;

        const distance = touchDistance(e.touches);
        if (!distance || !touchZoomState.startDistance) return;

        const pinchScale = distance / touchZoomState.startDistance;
        const nextZoom = touchZoomState.startZoom * pinchScale;
        updateAzimuthZoom(nextZoom);

        e.preventDefault();
    };

    const onTouchEndOrCancel = () => {
        if (dragState.active) {
            endDrag();
        }
        touchZoomState.active = false;
        touchZoomState.startDistance = 0;
    };

    const onMouseDown = (e) => {
        if (e.button !== 0) return;
        beginDrag(e.clientX, e.clientY);
        if (dragState.active) e.preventDefault();
    };

    const onMouseMove = (e) => {
        if (!dragState.active) return;
        updateDrag(e.clientX, e.clientY);
        e.preventDefault();
    };

    const onMouseUp = () => {
        endDrag();
    };

    canvases.forEach((canvas) => {
        canvas.style.touchAction = 'none';
        canvas.addEventListener('wheel', onWheel, { passive: false });
        canvas.addEventListener('mousedown', onMouseDown);
        canvas.addEventListener('touchstart', onTouchStart, { passive: false });
        canvas.addEventListener('touchmove', onTouchMove, { passive: false });
        canvas.addEventListener('touchend', onTouchEndOrCancel, { passive: false });
        canvas.addEventListener('touchcancel', onTouchEndOrCancel, { passive: false });
    });

    window.addEventListener('mousemove', onMouseMove);
    window.addEventListener('mouseup', onMouseUp);
}

function enableAzimuthNs6tIndicatorAlways() {
    setAzimuthNs6tIndicatorEnabled(true);
}

let dxccLabelDensity = Number(localStorage.getItem('dxccLabelDensity') || 1.0);
function updateDxccLabelDensity(density) {
    dxccLabelDensity = Math.max(0, Math.min(5.0, Number(density) || 1.0));
    localStorage.setItem('dxccLabelDensity', dxccLabelDensity);
    setAzimuthDxccLabelDensity(dxccLabelDensity);
    const densityInput = document.getElementById('dxcc-label-density');
    if (densityInput && Number(densityInput.value) !== dxccLabelDensity) {
        densityInput.value = String(dxccLabelDensity);
    }
    const densityVal = document.getElementById('dxcc-label-density-val');
    if (densityVal) densityVal.textContent = dxccLabelDensity.toFixed(1);
    if (isAzimuthEnabled()) scheduleRender();
}

const storedUnifiedDxcc = localStorage.getItem('dxccLabelsEnabled');
const storedMercatorDxcc = localStorage.getItem('mercatorDxccLabelsEnabled');
const storedAzimuthDxcc = localStorage.getItem('azimuthDxccLabelsEnabled');
let dxccLabelsEnabled = storedUnifiedDxcc !== null
    ? storedUnifiedDxcc !== 'false'
    : storedMercatorDxcc !== null
        ? storedMercatorDxcc !== 'false'
        : storedAzimuthDxcc !== null
            ? storedAzimuthDxcc !== 'false'
            : false;

function updateDxccLabelsEnabled(enabled) {
    dxccLabelsEnabled = Boolean(enabled);
    const serialized = dxccLabelsEnabled ? 'true' : 'false';

    // Keep unified key and legacy keys in sync for backward compatibility.
    localStorage.setItem('dxccLabelsEnabled', serialized);
    localStorage.setItem('mercatorDxccLabelsEnabled', serialized);
    localStorage.setItem('azimuthDxccLabelsEnabled', serialized);

    setAzimuthDxccLabelsEnabled(dxccLabelsEnabled);

    const dxccToggle = document.getElementById('show-dxcc-labels');
    if (dxccToggle) dxccToggle.checked = dxccLabelsEnabled;

    const densityRow = document.getElementById('dxcc-density-row');
    if (densityRow) densityRow.style.display = dxccLabelsEnabled ? '' : 'none';

    if (isAzimuthEnabled()) {
        scheduleRender();
    } else {
        void syncMercatorDxccLabelLayer({ force: true });
    }
}

const storedDk3jfMode = localStorage.getItem('dk3jfModeEnabled');
const legacyStoredDk3jxMode = localStorage.getItem('dk3jxModeEnabled');
let dk3jfModeEnabled = storedDk3jfMode !== null
    ? storedDk3jfMode === 'true'
    : legacyStoredDk3jxMode === 'true';

function setElementVisibility(el, visible) {
    if (!el) return;
    if (visible) {
        el.style.removeProperty('display');
    } else {
        el.style.setProperty('display', 'none', 'important');
    }
}

async function updateDk3jfMode(enabled) {
    dk3jfModeEnabled = Boolean(enabled);
    localStorage.setItem('dk3jfModeEnabled', dk3jfModeEnabled ? 'true' : 'false');
    localStorage.removeItem('dk3jxModeEnabled');

    const dk3jfToggle = document.getElementById('dk3jf-mode');
    if (dk3jfToggle) dk3jfToggle.checked = dk3jfModeEnabled;

    const band2mWrapper = document.getElementById('band-wrapper-2m');
    setElementVisibility(band2mWrapper, dk3jfModeEnabled);

    if (!dk3jfModeEnabled) {
        const band2mEnable = document.querySelector('.band-enable[value="2m"]');

        if (band2mEnable) {
            band2mEnable.checked = false;
            localStorage.setItem('enable-2m', 'false');
        }
        // If 2m was the focused band, drop focus back to "all".
        if (getSelectedBand() === '2m') {
            setBandFocus('all');
            localStorage.setItem('selectedBand', 'all');
            updateCurrentBandDisplay();
        }

    }

    scheduleRender();
}

// --- Init Configuration & Map ---
const { initialCenter, initialZoom } = loadConfig();
const savedTheme = localStorage.getItem('theme') || 'light';
const savedProjection = localStorage.getItem('mapProjection') || 'mercator';
let lastRenderTime = 0;
let renderTimeoutId = null;
const MIN_RENDER_INTERVAL_MS = 40;
let appReadyForAutoStart = false;
let autoStartTriggered = false;
let softPauseStartedAtMs = 0;

function currentProjection() {
    return document.querySelector('input[name="projection-select"]:checked')?.value || 'mercator';
}

function getRenderableMapSpots(spots) {
    const showDXClusterSpots = document.getElementById('show-dxcluster-spots')?.checked !== false;
    const showRbnSpots = document.getElementById('show-rbn-spots')?.checked !== false;
    const showWsprSpots = document.getElementById('show-wspr-spots')?.checked !== false;
    const srcFilter = !(showDXClusterSpots && showRbnSpots && showWsprSpots);

    // Render-time age gate using TRUE wall-clock age, not the prune's 5s-stepped
    // ageSeconds. The prune (state.renderInterval) already reaps over-age spots
    // out of state.liveSpots, so a same-threshold filter on the stepped value
    // would be a no-op and the boundary cohort (age within one prune step of
    // maxAge) would still vanish in a 5s batch at each tick — the "shown then
    // hidden" flash on reload. By tracking each spot's receive wall-clock time
    // (__recvMs) and its server-stamped age at receipt (__recvAge), the render
    // computes a continuous true age and hides spots smoothly as their real
    // age crosses the cutoff (at whatever render runs between ticks), instead
    // of a discrete batch drop. The prune keeps reaping memory on its 5s tick.
    const maxAge = getCurrentMaxSpotAgeSeconds();
    const now = Date.now();

    return spots.filter((spot) => {
        const recvMs = spot.__recvMs;
        if (recvMs) {
            const trueAge = (spot.__recvAge ?? spot.ageSeconds) + (now - recvMs) / 1000;
            if (trueAge > maxAge) return false;
        } else if (spot.ageSeconds > maxAge) {
            // Fallback for spots lacking a receive stamp (e.g. capture-mode
            // snapshots loaded directly into liveSpots): use the stepped age.
            return false;
        }
        if (!srcFilter) return true;
        const src = String(spot?.sourceType || '').toLowerCase();
        if (!showDXClusterSpots && src === 'dxcluster') return false;
        if (!showRbnSpots && src === 'rbn') return false;
        if (!showWsprSpots && src === 'wspr') return false;
        return true;
    });
}

function getCurrentMaxSpotAgeSeconds() {
    const streamMaxAge = (parseInt(document.getElementById('minutes')?.value || '15', 10) || 15) * 60;
    const bandLabMaxAge = getBandLabLookbackMinutes() * 60;
    return Math.max(streamMaxAge, bandLabMaxAge);
}

function applySoftPause() {
    if (state.softPaused) return;

    state.softPaused = true;
    softPauseStartedAtMs = Date.now();

    if (renderTimeoutId) {
        clearTimeout(renderTimeoutId);
        renderTimeoutId = null;
    }
    state.renderPending = false;
}

function resumeFromSoftPause() {
    if (!state.softPaused) return;

    state.softPaused = false;

    const pausedForSec = Math.max(0, Math.floor((Date.now() - softPauseStartedAtMs) / 1000));
    softPauseStartedAtMs = 0;

    if (pausedForSec > 0 && state.liveSpots.length > 0) {
        const maxAge = getCurrentMaxSpotAgeSeconds();
        state.liveSpots.forEach((s) => {
            s.ageSeconds += pausedForSec;
        });
        state.liveSpots = state.liveSpots.filter((s) => s.ageSeconds <= maxAge);
    }

    scheduleRender();
    updateBandLab({ force: true });
    updateWsprMatrix();
}

function syncSoftPauseWithVisibility() {
    // Timeline gate (KTD-10): the visibility machinery owns state.softPaused
    // only in live mode. Mid-replay, hiding/refocusing the tab must not pause
    // or bulk-age anything and must not kick live rendering back on — the
    // scheduleRender gate keeps rendering on the current moment regardless.
    if (isTimelineActive()) return;
    if (document.hidden) {
        applySoftPause();
    } else {
        resumeFromSoftPause();
    }
}

function getActiveQthCenter() {
    const rawQth = document.getElementById('qth')?.value?.trim()?.toUpperCase() || '';
    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(rawQth);
    if (!isLocator) return null;

    const bounds = locatorToBounds(rawQth);
    if (!bounds) return null;

    const lat = (bounds[0][0] + bounds[1][0]) / 2;
    const lng = (bounds[0][1] + bounds[1][1]) / 2;
    return [lat, lng];
}

// setChaseQueueHighlight drives the map highlight for a Chase Queue spot. Pass a
// spot {dx_call, dx_locator, pinned} to show it (great-circle line from the
// operator origin + marker), or null to clear. Origin = operator station when in
// operator mode (matches the antenna beam), else the active target center.
export function setChaseQueueHighlight(spot) {
    if (!spot || !spot.dx_locator) {
        clearChaseQueueHighlight();
        return;
    }
    const bounds = locatorToBounds(String(spot.dx_locator).toUpperCase());
    if (!bounds) {
        clearChaseQueueHighlight();
        return;
    }
    const spotLat = (bounds[0][0] + bounds[1][0]) / 2;
    const spotLng = (bounds[0][1] + bounds[1][1]) / 2;

    const station = (typeof getOpModeStation === 'function' && isOpModeActive()) ? getOpModeStation() : null;
    const origin = station || (() => {
        const c = getActiveQthCenter();
        return c ? { lat: c[0], lng: c[1] } : null;
    })();

    const payload = {
        enabled: true,
        originLat: origin ? origin.lat : null,
        originLng: origin ? origin.lng : null,
        spotLat,
        spotLng,
        label: String(spot.dx_call || ''),
        pinned: Boolean(spot.pinned)
    };

    setAzimuthDxSpotHighlight(payload);
    setMercatorDxHighlight(payload);

    // On an explicit selection (click), with Auto-zoom on, pull the Mercator view
    // in so the highlighted DX is visible. Centre on the DX with a ~2000km region
    // (matching the periodic auto-zoom's minimum). Mark it as an interaction so the
    // periodic auto-zoom doesn't immediately yank the view back to all spots.
    if (spot.select && currentProjection() === 'mercator' &&
        document.getElementById('auto-zoom')?.checked) {
        const bounds = L.latLng(spotLat, spotLng).toBounds(2000000);
        map.fitBounds(bounds, { padding: [40, 40], maxZoom: 6 });
        state.lastMercatorInteractionAt = Date.now();
    }

    if (isAzimuthEnabled()) scheduleRender();
}

export function clearChaseQueueHighlight() {
    setAzimuthDxSpotHighlight({ enabled: false });
    clearMercatorDxHighlight();
    if (isAzimuthEnabled()) scheduleRender();
}

function syncProjectionCenterToActiveQth() {
    const targetCenter = getActiveQthCenter();
    if (!targetCenter) return;

    const projection = currentProjection();
    if (projection === 'azimuthal') {
        setAzimuthCenter(targetCenter);
    } else {
        return;
    }

    scheduleRender();
}

function maybeAutoStartSavedQth() {
    if (captureConfig?.enabled) {
        return;
    }
    if (autoStartTriggered || !appReadyForAutoStart || !map) {
        return;
    }

    // Read the saved QTH. Fall back to the legacy 'target' key (pre-QTH-rename)
    // and migrate it inline, because the Svelte-bundle migration in main.js
    // runs AFTER app.js (module load order) — without this fallback, existing
    // users with a saved 'target' locator would see an empty field and no
    // autostart.
    let savedQth = localStorage.getItem('qth')?.trim()?.toUpperCase();
    if (!savedQth) {
        const legacy = localStorage.getItem('target')?.trim()?.toUpperCase();
        if (legacy) {
            savedQth = legacy;
            localStorage.setItem('qth', legacy);
            localStorage.removeItem('target');
        }
    }
    if (!savedQth) {
        return;
    }

    // The #qth input element is created by the Svelte bundle (dist/horst-ui.js),
    // which loads AFTER app.js. If it hasn't mounted yet, retry shortly.
    const qthInput = document.getElementById('qth');
    if (!qthInput) {
        setTimeout(() => { maybeAutoStartSavedQth(); }, 50);
        return;
    }

    // Ensure the input is populated from storage even if another init step missed it.
    if (!qthInput.value?.trim()) {
        window.__horstSetQTH?.(savedQth);
        qthInput.value = savedQth;
    }

    const qthValue = qthInput.value?.trim()?.toUpperCase();
    if (!qthValue) {
        return;
    }

    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit) setSubmitMode(btnSubmit, 'go');

    const form = document.getElementById('fetch-form');
    if (!form) {
        return;
    }

    form.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));

    const streamStarted = Boolean(state.eventSource) || isStreaming(document.getElementById('btn-submit'));
    if (streamStarted) {
        autoStartTriggered = true;
        return;
    }

    // If submit wiring was not yet active at this moment, retry shortly.
    setTimeout(() => {
        maybeAutoStartSavedQth();
    }, 50);
}

async function syncMercatorOverlays(force = false) {
    await syncMercatorCountryLayer({ force, enabled: getCountryColoringEnabled() });
    await syncMercatorGraylineLayer({ force });
    await syncMercatorDxccLabelLayer({ force });
}

function syncStyleAvailabilityForProjection(_projection) {
    const activeAreaRadio = document.getElementById('style-area');
    if (!activeAreaRadio) return;
    activeAreaRadio.disabled = false;
    const label = document.querySelector(`label[for="${activeAreaRadio.id}"]`);
    if (label) {
        label.style.opacity = '';
        label.style.pointerEvents = '';
    }
}

function syncProjectionOptionVisibility(projection) {
    const azimuthCtrls = document.getElementById('azimuth-zoom-controls');
    if (azimuthCtrls) azimuthCtrls.style.display = projection === 'azimuthal' ? 'block' : 'none';
}

// Monotonic sequence token for applyProjectionMode: each call bumps it, and
// every call re-checks it after its awaits so a superseded call (a newer
// projection toggle, or a click during initial load) can't flip the runtime
// state to a projection the user no longer selected.
let projectionApplySeq = 0;

async function applyProjectionMode(projection) {
    const seq = ++projectionApplySeq;

    // A mercator zoom/pan interaction may still be in flight when the user
    // switches projection. Its zoomend/moveend handler early-returns on the
    // projection change (attachMapEvents), so without this reset the
    // mercatorInteractionActive flag would stay stuck true and freeze every
    // mercator render until the next pan/zoom. Clear both flags here so the
    // deferred-render path can never be left armed across a projection switch.
    state.mercatorInteractionActive = false;
    state.mercatorRenderDeferred = false;

    syncStyleAvailabilityForProjection(projection);
    syncProjectionOptionVisibility(projection);
    const targetCenter = getActiveQthCenter();

    if (projection === 'azimuthal') {
        await loadAzimuthWorldGeoJson();
        if (seq !== projectionApplySeq) return; // superseded by a newer toggle
        setAzimuthEnabled(true);
        setAzimuthTheme(document.body.getAttribute('data-theme') || 'light');
        setAzimuthZoom(azimuthZoom);
        if (targetCenter) {
            setAzimuthCenter(targetCenter);
        }
        scheduleRender();
        return;
    }

    setAzimuthEnabled(false);
    resetRenderFingerprint();
    syncAzimuthZoomOutHint();
    if (map) map.invalidateSize();
    await syncMercatorOverlays(true);
    if (seq !== projectionApplySeq) return; // superseded by a newer toggle
    scheduleRender();
}

function applyCaptureConfigToControls(config) {
    if (!config?.enabled) return;

    const qthEl = document.getElementById('qth');
    if (qthEl) window.__horstSetQTH?.(config.qth);

    const minutesEl = document.getElementById('minutes');
    if (minutesEl) {
        minutesEl.value = String(config.minutes);
        const minutesVal = document.getElementById('minutes-val');
        if (minutesVal) minutesVal.textContent = config.minutes;
    }

    const surroundingsEl = document.getElementById('surroundings');
    if (surroundingsEl) surroundingsEl.checked = Boolean(config.surroundings);

    const ssbEl = document.getElementById('ssb-min-db');
    if (ssbEl && Number.isFinite(config.ssbMinDb)) {
        ssbEl.value = String(config.ssbMinDb);
        const ssbVal = document.getElementById('ssb-min-db-val');
        if (ssbVal) ssbVal.textContent = config.ssbMinDb;
    }

    const cwEl = document.getElementById('cw-min-db');
    if (cwEl && Number.isFinite(config.cwMinDb)) {
        cwEl.value = String(config.cwMinDb);
        const cwVal = document.getElementById('cw-min-db-val');
        if (cwVal) cwVal.textContent = config.cwMinDb;
    }

    const minSnrRadio = document.querySelector(`input[name="min-snr"][value="${config.minSnrMode}"]`);
    if (minSnrRadio) minSnrRadio.checked = true;

    const styleRadio = document.querySelector(`input[name="style-select"][value="${config.style}"]`);
    if (styleRadio) styleRadio.checked = true;

    const projectionRadio = document.querySelector(`input[name="projection-select"][value="${config.projection}"]`);
    if (projectionRadio) projectionRadio.checked = true;

    setBandFocus(config.selectedBand && config.selectedBand !== 'all' ? config.selectedBand : 'all');

    const enabledBandsSet = new Set(
        config.enabledBandsCsv
            .split(',')
            .map((part) => part.trim().toLowerCase())
            .filter(Boolean)
    );

    document.querySelectorAll('.band-enable').forEach((cb) => {
        const band = String(cb?.value || '').toLowerCase();
        if (!band) return;
        if (enabledBandsSet.size === 0) {
            cb.checked = true;
        } else {
            cb.checked = enabledBandsSet.has(band);
        }
    });

    const showDxclusterEl = document.getElementById('show-dxcluster-spots');
    if (showDxclusterEl) showDxclusterEl.checked = config.includeDxcluster;

    const countryColoringEl = document.getElementById('show-country-coloring');
    if (countryColoringEl) countryColoringEl.checked = Boolean(config.countryColoring);

    updateCurrentBandDisplay();
}

async function runCaptureBootstrap(config) {
    if (!config?.enabled) return;

    if (state.eventSource) {
        state.eventSource.close();
        state.eventSource = null;
    }
    if (state.renderInterval) {
        clearInterval(state.renderInterval);
        state.renderInterval = null;
    }

    const params = new URLSearchParams();
    params.set('qth', config.qth);
    params.set('minutes', String(config.minutes));
    params.set('surroundings', config.surroundings ? 'true' : 'false');
    params.set('min_snr_mode', config.minSnrMode);
    params.set('ssb_min_db', String(config.ssbMinDb));
    params.set('cw_min_db', String(config.cwMinDb));
    params.set('selected_band', config.selectedBand);
    if (config.enabledBandsCsv) params.set('enabled_bands', config.enabledBandsCsv);
    params.set('include_dxcluster', config.includeDxcluster ? 'true' : 'false');
    if (Number.isFinite(config.snapshotAt)) {
        params.set('snapshot_at', String(config.snapshotAt));
    }

    setStreamStatus({ message: STREAM_STATUS_TEXT.snapshotLoading, tone: 'warn', spinner: true });

    const response = await fetch(`/api/capture_snapshot?${params.toString()}`);
    if (!response.ok) {
        const msg = await response.text();
        throw new Error(`capture snapshot request failed (${response.status}): ${msg}`);
    }

    const payload = await response.json();
    state.liveSpots = Array.isArray(payload?.spots) ? payload.spots : [];

    scheduleRender();

    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    // The active-area hull draw completes asynchronously (turf is lazy-loaded);
    // hold the capture-ready handshake until it settles so frames captured off
    // __horstCaptureReady always contain the hulls. Resolves immediately when
    // no active-area rebuild is pending (grid-snr or cached turf).
    await whenActiveAreaRendered();
    setStreamStatus({ title: config.qth ? `Snapshot for ${config.qth}` : 'Snapshot', message: spotCountText(state.liveSpots.length), tone: 'ok' });
    window.__horstCaptureReady = {
        ready: true,
        count: state.liveSpots.length,
        snapshotAt: payload?.snapshot_at ?? null,
        generatedAt: payload?.generated_at ?? null
    };
}

export function attachMapEvents() {
    let zoomStartedAt = 0;
    let panStartedAt = 0;
    let mercatorInteractionSettleTimerId = null;
    const MERCATOR_INTERACTION_SETTLE_MS = 90;

    const startMercatorInteraction = () => {
        if (currentProjection() !== 'mercator') return;
        if (mercatorInteractionSettleTimerId) {
            clearTimeout(mercatorInteractionSettleTimerId);
            mercatorInteractionSettleTimerId = null;
        }
        state.mercatorInteractionActive = true;
    };

    const finishMercatorInteraction = () => {
        if (currentProjection() !== 'mercator') {
            // Projection changed mid-interaction: the zoomend/moveend that would
            // normally reset the flag can't arm the settle timer, so reset the
            // interaction state directly. (applyProjectionMode also does this;
            // this covers any path that changes the projection without it.)
            state.mercatorInteractionActive = false;
            if (state.mercatorRenderDeferred) {
                state.mercatorRenderDeferred = false;
                scheduleRender();
            }
            return;
        }

        if (mercatorInteractionSettleTimerId) {
            clearTimeout(mercatorInteractionSettleTimerId);
        }

        mercatorInteractionSettleTimerId = setTimeout(() => {
            mercatorInteractionSettleTimerId = null;
            state.mercatorInteractionActive = false;
            if (state.mercatorRenderDeferred) {
                state.mercatorRenderDeferred = false;
                scheduleRender();
            }
        }, MERCATOR_INTERACTION_SETTLE_MS);
    };

    const markMercatorInteraction = () => {
        if (currentProjection() !== 'mercator') return;
        state.lastMercatorInteractionAt = Date.now();
    };

    map.on('zoomstart', () => {
        if (currentProjection() !== 'mercator') return;
        zoomStartedAt = startPerfTimer();
        incrementPerfCounter('mercator.interaction.zoom_start', 1);
        startMercatorInteraction();
        markMercatorInteraction();
    });
    map.on('zoomend', () => {
        if (currentProjection() !== 'mercator') return;
        endPerfTimer('mercator.interaction.zoom_duration_ms', zoomStartedAt);
        zoomStartedAt = 0;
        incrementPerfCounter('mercator.interaction.zoom_end', 1);
        finishMercatorInteraction();
        if (isTimelineActive()) notifyMapMoved();
    });

    map.on('movestart', () => {
        if (currentProjection() !== 'mercator') return;
        panStartedAt = startPerfTimer();
        incrementPerfCounter('mercator.interaction.pan_start', 1);
        startMercatorInteraction();
        markMercatorInteraction();
    });
    map.on('moveend', () => {
        if (currentProjection() !== 'mercator') return;
        endPerfTimer('mercator.interaction.pan_duration_ms', panStartedAt);
        panStartedAt = 0;
        incrementPerfCounter('mercator.interaction.pan_end', 1);
        finishMercatorInteraction();
        if (isTimelineActive()) notifyMapMoved();
    });

	const setQthAndRestart = (locator) => {
		if (!locator) return;
		const qthInput = document.getElementById('qth');
		if (!qthInput) return;

		window.__horstSetQTH?.(locator);
		const btnSubmit = document.getElementById('btn-submit');
		if (btnSubmit) setSubmitMode(btnSubmit, 'go'); // Force a clean restart
		document.getElementById('fetch-form')?.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
	};

    const CLICK_TO_DBLCLICK_DELAY_MS = 260;
    let pendingMercatorSingleClickTimer = null;

    const clearPendingMercatorSingleClick = () => {
        if (pendingMercatorSingleClickTimer) {
            clearTimeout(pendingMercatorSingleClickTimer);
            pendingMercatorSingleClickTimer = null;
        }
    };

    const submitBeamTargetFromLatLng = (lat, lng, locatorLabel = '') => {
        if (!Number.isFinite(Number(lat)) || !Number.isFinite(Number(lng))) return;
        void setBeamTargetFromMapClick({
            lat,
            lng,
            label: locatorLabel
        });
    };

    if (map?.doubleClickZoom && typeof map.doubleClickZoom.disable === 'function') {
        map.doubleClickZoom.disable();
    }

    map.on('click', function(e) {
        if (currentProjection() === 'azimuthal') {
            return;
        }

        clearPendingMercatorSingleClick();
        if (!isOpModeActive()) {
            return;
        }

        const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, 4);
        pendingMercatorSingleClickTimer = setTimeout(() => {
            pendingMercatorSingleClickTimer = null;
            submitBeamTargetFromLatLng(e.latlng.lat, e.latlng.lng, loc);
        }, CLICK_TO_DBLCLICK_DELAY_MS);
    });

    map.on('dblclick', function(e) {
        if (currentProjection() === 'azimuthal') {
            return;
        }

        clearPendingMercatorSingleClick();
        const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, 4);
        setQthAndRestart(loc);
    });

    const azimuthCanvas = document.getElementById('azimuth-canvas');
    if (!azimuthCanvas || azimuthCanvas.dataset.targetingBound === 'true') return;

    let pendingAzimuthSingleClickTimer = null;
    const clearPendingAzimuthSingleClick = () => {
        if (pendingAzimuthSingleClickTimer) {
            clearTimeout(pendingAzimuthSingleClickTimer);
            pendingAzimuthSingleClickTimer = null;
        }
    };

    azimuthCanvas.dataset.targetingBound = 'true';
    azimuthCanvas.addEventListener('click', (e) => {
        if (!isAzimuthEnabled()) return;
        if (Date.now() < suppressAzimuthClickUntil) return;

        clearPendingAzimuthSingleClick();
        const point = getAzimuthLatLngFromClientPoint(e.clientX, e.clientY);
        if (!point) return;

        if (!isOpModeActive()) return;

        const loc = latLngToLocator(point.lat, point.lng, 4);
        pendingAzimuthSingleClickTimer = setTimeout(() => {
            pendingAzimuthSingleClickTimer = null;
            submitBeamTargetFromLatLng(point.lat, point.lng, loc);
        }, CLICK_TO_DBLCLICK_DELAY_MS);
    });

    azimuthCanvas.addEventListener('dblclick', (e) => {
        if (!isAzimuthEnabled()) return;
        if (Date.now() < suppressAzimuthClickUntil) return;

        clearPendingAzimuthSingleClick();
        const point = getAzimuthLatLngFromClientPoint(e.clientX, e.clientY);
        if (!point) return;

        const loc = latLngToLocator(point.lat, point.lng, 4);
        setQthAndRestart(loc);
    });
}

// --- Init UI ---
initUI();

if (captureConfig?.enabled) {
    applyCaptureConfigToControls(captureConfig);
}

(async () => {
    installPerfDebugApi();
    // Render hook for migrated Svelte controls (SNR sliders) to trigger renders.
    window.__horstScheduleRender = scheduleRender;
    window.__horstApplyProjection = applyProjectionMode;
    window.__horstQthInput = syncProjectionCenterToActiveQth;
    initMap(initialCenter, initialZoom);
    initAzimuthCanvas();
    attachProjectionGestureZoomEvents();
    setTheme(savedTheme);
    setAzimuthTheme(savedTheme);
    setAzimuthCenter(initialCenter);
    updateAzimuthZoom(azimuthZoom);
    enableAzimuthNs6tIndicatorAlways();
    updateDxccLabelDensity(dxccLabelDensity);
    updateDxccLabelsEnabled(dxccLabelsEnabled);
    await updateDk3jfMode(dk3jfModeEnabled);
    initRbnSpotsToggle();
    initWsprSpotsToggle();

    const initialProjection = captureConfig?.enabled ? captureConfig.projection : savedProjection;
    const projRadio = document.querySelector(`input[name="projection-select"][value="${initialProjection}"]`);
    if (projRadio) projRadio.checked = true;
    await applyProjectionMode(initialProjection);
    syncProjectionCenterToActiveQth();

    const dxccToggle = document.getElementById('show-dxcc-labels');
    if (dxccToggle) dxccToggle.checked = dxccLabelsEnabled;

    await syncMercatorOverlays(true);

    attachMapEvents();
    attachUITooltipEvents();
    initOpMode({ requestRender: scheduleRender });
    initBandLab({
        onLayoutChange: () => {
            // Docked Band Stats panel changed the map container width; re-fit Leaflet
            // and the azimuth canvas so tiles/centering stay correct (no overlap).
            if (map) map.invalidateSize();
            if (isAzimuthEnabled()) scheduleRender();
        },
    });
    initWsprMatrix({
        onLayoutChange: () => {
            // The WSPR matrix is a floating overlay inside #map-stack; toggling
            // it no longer changes the map container's box, so invalidateSize is
            // not needed (unlike band-lab, which is docked in-flow). The azimuth
            // canvas display-swaps with #map and may need a re-render.
            if (isAzimuthEnabled()) scheduleRender();
        },
    });
    // U4: wire up the "clear filter" overlay button for grid-square drill-down.
    const drillDownClearBtn = document.getElementById('drill-down-clear');
    if (drillDownClearBtn) {
        drillDownClearBtn.addEventListener('click', clearDrillDown);
    }
    updateDrillDownButton();
    hotBandIndicator = initHotBandIndicator({
        getQth: () => document.getElementById('qth')?.value?.trim()?.toUpperCase() || '',
        getSurroundings: () => Boolean(document.getElementById('surroundings')?.checked),
        getCurrentBand: () => getSelectedBand(),
        onBandSwitch: switchToBand,
    });
    // U5: Web Push UI — wires up the push settings panel, Service Worker
    // registration, and re-subscription-after-restart reconciliation.
    // Returns null when push is unsupported (UI stays hidden).
    initPushUI().catch((err) => { console.warn('push UI init failed:', err); });
    if (HORST_KEVIN_ENABLED) {
        horstKevin = initHorstKevin({
            getQth: () => document.getElementById('qth')?.value?.trim()?.toUpperCase() || '',
            getSurroundings: () => Boolean(document.getElementById('surroundings')?.checked),
            getCurrentBand: () => getSelectedBand(),
            onBandSwitch: switchToBand,
        });
    }

    // Force an initial render to sync visual band states (colors/opacity) loaded from localStorage
    scheduleRender();
    updateCurrentBandDisplay();

    if (captureConfig?.enabled) {
        await updateDk3jfMode(captureConfig.dk3jfMode);
        try {
            await runCaptureBootstrap(captureConfig);
        } catch (err) {
            console.error('Snapshot mode bootstrap failed:', err);
            const errorText = String(err?.message || err);
            window.__horstCaptureReady = { ready: false, error: errorText };
            setStreamStatus({ message: `Could not load snapshot: ${errorText}`, tone: 'danger' });
        }
    }

    appReadyForAutoStart = true;
    maybeAutoStartSavedQth();
})();

// Solo chip on the map (#band-solo-clear): shown only while one band is
// soloed, in that band's color; clicking it shows all enabled bands again.
// "All bands" needs no chip: the band rail already shows the enabled set.
function updateCurrentBandDisplay() {
    const band = getSelectedBand();
    const soloed = band !== 'all' && getEnabledBands().has(band);
    const chip = document.getElementById('band-solo-clear');
    if (chip) {
        chip.style.display = soloed ? '' : 'none';
        if (soloed) {
            const color = bandColors[band] || bandColors['all'];
            chip.querySelector('.band-solo-name').textContent = `${band} only`;
            chip.style.backgroundColor = color;
            chip.style.borderColor = color;
            chip.style.color = pillTextColor(color);
            chip.setAttribute('aria-label', `Showing ${band} only. Show all bands`);
        }
    }
}

export function scheduleRender() {
    if (state.softPaused) return;

    if (!isAzimuthEnabled() && currentProjection() === 'mercator' && state.mercatorInteractionActive) {
        state.mercatorRenderDeferred = true;
        return;
    }

    const scheduleStart = perfNow();
    const now = Date.now();
    const timeSinceLastRender = now - lastRenderTime;
    const delay = Math.max(0, MIN_RENDER_INTERVAL_MS - timeSinceLastRender);

    if (renderTimeoutId) {
        clearTimeout(renderTimeoutId);
        renderTimeoutId = null;
    }

    state.renderPending = true;
    renderTimeoutId = setTimeout(() => {
        renderTimeoutId = null;
        requestAnimationFrame(() => {
            if (state.softPaused) {
                state.renderPending = false;
                return;
            }

            const rafStart = perfNow();
            incrementPerfCounter('render.schedule.calls', 1);
            endPerfTimer('render.schedule.queue_delay_ms', scheduleStart);

            // Timeline gate (plan 2026-09-11-002 U4, KTD-10): while the timeline
            // is active the live array is never painted over the moment, no
            // matter which caller fired this render (SSE frame, age-prune tick,
            // theme/projection toggle, band pill). Instead the
            // retained moment is re-rendered with the current styles.
            if (isTimelineActive()) {
                renderTimelineMomentFrame();
                lastRenderTime = Date.now();
                state.renderPending = false;
                endPerfTimer('render.frame.total_ms', rafStart);
                return;
            }

            const minutes = document.getElementById('minutes')?.value || 15;
            const renderSpots = getRenderableMapSpots(state.liveSpots);
            if (isAzimuthEnabled()) {
                updateBandLabels(renderSpots);
                renderAzimuthScene({ spots: renderSpots }); // grayline clock defaults to dataNow() (U6, KTD-9)
                syncAzimuthZoomOutHint();
                endPerfTimer('render.azimuth.frame_ms', rafStart);
            } else {
                const mercatorTimer = startPerfTimer();
                updateMapVisualization(renderSpots, parseInt(minutes));
                endPerfTimer('render.mercator.frame_ms', mercatorTimer);
            }

            endPerfTimer('render.frame.total_ms', rafStart);
            lastRenderTime = Date.now();
            state.renderPending = false;
            updateBandLab();
            updateWsprMatrix();
        });
    }, delay);
}

document.getElementById('theme-toggle')?.addEventListener('click', () => {
    const currentTheme = document.body.getAttribute('data-theme');
    const newTheme = currentTheme === 'dark' ? 'light' : 'dark';
    setTheme(newTheme);
    setAzimuthTheme(newTheme);
    if (!isAzimuthEnabled()) {
        void syncMercatorOverlays(true);
    }
    if (isAzimuthEnabled()) scheduleRender();
});

document.getElementById('hide-sidebar')?.addEventListener('click', () => {
    const controls = document.getElementById('controls');
    if (controls) controls.style.marginLeft = '-' + controls.offsetWidth + 'px';
    const show = document.getElementById('show-sidebar');
    if (show) show.style.display = 'block';
});

document.getElementById('show-sidebar')?.addEventListener('click', () => {
    const controls = document.getElementById('controls');
    if (controls) controls.style.marginLeft = '0px';
    const show = document.getElementById('show-sidebar');
    if (show) show.style.display = 'none';
});

document.getElementById('controls')?.addEventListener('transitionend', (e) => {
    if (e.propertyName === 'margin-left' && map) {
        map.invalidateSize(); // Fixes distorted tile layers and centering after map container is stretched
        if (isAzimuthEnabled()) scheduleRender();
    }
});

// Azimuth zoom button events
document.getElementById('azimuth-zoom-in')?.addEventListener('click', () => {
    updateAzimuthZoom(azimuthZoom + 0.2);
});
document.getElementById('azimuth-zoom-out')?.addEventListener('click', () => {
    updateAzimuthZoom(azimuthZoom - 0.2);
});
document.getElementById('azimuth-zoom-out')?.addEventListener('dblclick', (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (!isAzimuthEnabled()) return;
    fitAzimuthToAllRenderableSpots();
});
document.getElementById('azimuth-zoom-in')?.addEventListener('dblclick', (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (!isAzimuthEnabled()) return;
    updateAzimuthZoom(AZIMUTH_MAX_ZOOM);
});

// Keyboard +/- for azimuth zoom
window.addEventListener('keydown', (e) => {
    const targetTag = String(e.target?.tagName || '').toUpperCase();
    if (targetTag === 'INPUT' || targetTag === 'TEXTAREA' || targetTag === 'SELECT' || e.target?.isContentEditable) {
        return;
    }

    if (!isAzimuthEnabled()) return;
    if (e.key === '+' || e.key === '=') {
        updateAzimuthZoom(azimuthZoom + 0.2);
        e.preventDefault();
    } else if (e.key === '-' || e.key === '_') {
        updateAzimuthZoom(azimuthZoom - 0.2);
        e.preventDefault();
    }
});

document.getElementById('qth')?.addEventListener('input', () => {
    state.qth = document.getElementById('qth')?.value?.trim()?.toUpperCase() || '';
    syncProjectionCenterToActiveQth();
});
document.getElementById('azimuth-horizon-km')?.addEventListener('change', (e) => {
    // Keep backward compatibility: if edited manually, convert horizon request into zoom.
    updateAzimuthHorizonKm(e.target.value);
});

document.getElementById('dxcc-label-density')?.addEventListener('input', (e) => {
    const val = document.getElementById('dxcc-label-density-val');
    if (val) val.textContent = e.target.value;
});

document.getElementById('dxcc-label-density')?.addEventListener('change', (e) => {
    updateDxccLabelDensity(e.target.value);
});

document.getElementById('dk3jf-mode')?.addEventListener('change', async (e) => {
    await updateDk3jfMode(e.target.checked);
});

// --- RBN (Reverse Beacon Network) live-map toggle ----------------------------
// Filters sourceType==='rbn' spots out of the live map (both projections, via
// getRenderableMapSpots) when unchecked. Default: show. Persisted to localStorage
// and the include_rbn URL param so the choice survives reload. RBN only reaches
// the live map when QRZ resolves a locator, so this is a no-op until RBN is
// enabled upstream (-rbn-enable) and produces locators.
function initRbnSpotsToggle() {
    const el = document.getElementById('show-rbn-spots');
    if (!el) return;
    const fromUrl = new URLSearchParams(location.search).get('include_rbn');
    if (fromUrl === 'true' || fromUrl === '1') el.checked = true;
    else if (fromUrl === 'false' || fromUrl === '0') el.checked = false;
    else {
        const stored = localStorage.getItem('rbnSpotsVisible');
        if (stored === 'false') el.checked = false;
    }
}

document.getElementById('show-rbn-spots')?.addEventListener('change', (e) => {
    const visible = e.target.checked;
    localStorage.setItem('rbnSpotsVisible', visible ? 'true' : 'false');
    try {
        const url = new URL(location.href);
        url.searchParams.set('include_rbn', visible ? 'true' : 'false');
        history.replaceState(null, '', url.toString());
    } catch (_) { /* location not available */ }
    scheduleRender();
});

// --- WSPR live-map toggle ----------------------------------------------------
// Filters sourceType==='wspr' spots out of the live map when unchecked.
// Default: show. Persisted to localStorage and the include_wspr URL param.
// WSPR only reaches the live map when the receiver locator is valid, so this
// is a no-op until WSPR is enabled upstream (-wspr-enable).
function initWsprSpotsToggle() {
    const el = document.getElementById('show-wspr-spots');
    if (!el) return;
    const fromUrl = new URLSearchParams(location.search).get('include_wspr');
    if (fromUrl === 'true' || fromUrl === '1') el.checked = true;
    else if (fromUrl === 'false' || fromUrl === '0') el.checked = false;
    else {
        const stored = localStorage.getItem('wsprSpotsVisible');
        if (stored === 'false') el.checked = false;
    }
}

document.getElementById('show-wspr-spots')?.addEventListener('change', (e) => {
    const visible = e.target.checked;
    localStorage.setItem('wsprSpotsVisible', visible ? 'true' : 'false');
    try {
        const url = new URL(location.href);
        url.searchParams.set('include_wspr', visible ? 'true' : 'false');
        history.replaceState(null, '', url.toString());
    } catch (_) { /* location not available */ }
    scheduleRender();
});

// --- Band pill interactions (event-delegated on #band-container) ------------
function activateBandPill(pill) {
    const band = pill?.dataset?.band;
    if (!band) return;
    // A disabled band can't be soloed (its .band-solo button is disabled too;
    // this guards clicks on the row's padding).
    if (!getEnabledBands().has(band)) return;
    if (getSelectedBand() === band) {
        setBandFocus('all'); // release focus -> show all enabled
    } else {
        setBandFocus(band);
    }
    applyBandChange();
}

const bandContainerEl = document.getElementById('band-container');
// Click anywhere on a row (incl. its .band-solo button, which also covers
// keyboard Enter/Space natively) solos that band; the checkbox is separate.
bandContainerEl?.addEventListener('click', (e) => {
    if (e.target.closest('.band-enable')) return; // checkbox handled separately
    const pill = e.target.closest('.band-pill');
    if (pill) activateBandPill(pill);
});
// Enter on a band checkbox would submit #fetch-form (implicit submission) and
// stop the stream; make it toggle like Space instead.
bandContainerEl?.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    const cb = e.target.closest('.band-enable');
    if (!cb) return;
    e.preventDefault();
    cb.click();
});
bandContainerEl?.addEventListener('change', (e) => {
    const cb = e.target.closest('.band-enable');
    if (!cb) return;
    // Keep the row's solo button in step right away (updateBandLabels also
    // sets it, but can be deferred, e.g. while Time Travel is paused).
    const solo = cb.closest('.band-row')?.querySelector('.band-solo');
    if (solo) solo.disabled = !cb.checked;
    // Toggling enabled must NOT change focus; just persist the set and re-render.
    localStorage.setItem(`enable-${cb.value}`, cb.checked);
    applyBandChange();
    restartStreamIfSubscribed();
});

function showAllBands() {
    setBandFocus('all');
    applyBandChange();
}
document.getElementById('band-solo-clear')?.addEventListener('click', showAllBands);

window.__horstSurroundingsChanged = () => {
    // KTD-13 keys the ring on qth AND surroundings: when the surroundings
    // toggle changes the delivered cohort, the ring's contents are foreign
    // (startLiveStream's qth hook alone does not catch a surroundings-only
    // change). Clear it unconditionally, before any re-stream.
    sessionRing.clear();
    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit && isStreaming(btnSubmit)) {
        setSubmitMode(btnSubmit, 'go');
        document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
    hotBandIndicator?.refresh();
    horstKevin?.refresh();
};

window.__horstCountryColoringChanged = () => {
    const enabled = document.getElementById('show-country-coloring')?.checked;
    if (!isAzimuthEnabled()) {
        void syncMercatorCountryLayer({ force: true, enabled });
    } else {
        scheduleRender();
    }
};

// Forecast overlay toggle (azimuthal advancing gray-line + rising-activity halo).
// Local-only, default on.
const forecastEl = document.getElementById('show-forecast');
if (forecastEl) {
    const savedForecast = localStorage.getItem('forecastEnabled');
    forecastEl.checked = savedForecast === null ? true : savedForecast === 'true';
    forecastEl.addEventListener('change', (e) => {
        localStorage.setItem('forecastEnabled', e.target.checked ? 'true' : 'false');
        scheduleRender();
    });
}

document.getElementById('show-dxcc-labels')?.addEventListener('change', (e) => {
    updateDxccLabelsEnabled(e.target.checked);
});

// Restart the live stream when SNR filters change so the server-side filter can
// take effect. The min-snr radios and threshold sliders are created by the
// Svelte bundle, so attach listeners after DOM mount.
document.getElementById('min-snr-group')?.addEventListener('change', (e) => {
    if (e.target?.name === 'min-snr') restartStreamIfSubscribed();
});
document.getElementById('ssb-min-db')?.addEventListener('change', restartStreamIfSubscribed);
document.getElementById('cw-min-db')?.addEventListener('change', restartStreamIfSubscribed);

document.getElementById('btn-geo')?.addEventListener('click', () => {
    if (!navigator.geolocation) {
        alert('Geolocation is not supported by your browser.');
        return;
    }

    const btn = document.getElementById('btn-geo');
    if (!btn) return;
    const originalText = btn.innerHTML;
    btn.innerHTML = icon('spinner', true);
    btn.disabled = true;

    navigator.geolocation.getCurrentPosition(
        (position) => {
            // Pre-fill with a 4-character locator (square)
            const loc = latLngToLocator(position.coords.latitude, position.coords.longitude, 4);
            const qthEl = document.getElementById('qth');
            if (qthEl) window.__horstSetQTH?.(loc);
            localStorage.setItem('qth', loc);
            btn.innerHTML = originalText;
            btn.disabled = false;
            
            const btnSubmit = document.getElementById('btn-submit');
            if (btnSubmit) setSubmitMode(btnSubmit, 'go');
            document.getElementById('fetch-form')?.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        },
        (error) => {
            console.error('Geolocation error:', error);
            alert(`Could not get location: ${error.message}`);
            btn.innerHTML = originalText;
            btn.disabled = false;
        },
        { enableHighAccuracy: false, timeout: 10000, maximumAge: 60000 }
    );
});

document.getElementById('qth')?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
        e.preventDefault();
        const btnSubmit = document.getElementById('btn-submit');
        if (btnSubmit) setSubmitMode(btnSubmit, 'go');
        document.getElementById('fetch-form')?.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
});

// Surface an invalid-target error in the stream status line (same place the
// stream's connection status renders, so the feedback lands where users look).
// Message text is assigned via textContent — the message embeds raw user input.
function showStreamError(message) {
    const statusEl = document.getElementById('stream-status');
    if (statusEl) {
        setStreamStatus({ message, tone: 'danger' });
    } else {
        alert(message);
    }
}

// The qth whose cohort the session ring currently holds. Tracked separately
// from state.qth (which the qth input listener updates on every keystroke) so
// the ring's invalidation compares the value the stream actually last fed.
let lastRingQth = null;

// Start (or restart) the live SSE stream. When preserveData is true, existing
// spots and overlays are kept on screen while the new connection's history dump
// is merged in, so band changes never create an empty-map flash.
function startLiveStream(preserveData = false) {
    const minutes = document.getElementById('minutes')?.value || 15;

    const rawQth = document.getElementById('qth')?.value.trim().toUpperCase() || '';
    if (!rawQth) {
        alert('Enter your locator or callsign.');
        return;
    }

    // Normalize/validate the target: accept Maidenhead squares/subsquares and
    // callsigns (the backend matches sender/receiver callsigns incl. modifiers).
    // A subsquare (JO32WE) is shortened to its square (JO32) — spot locators are
    // typically 4-char, so an exact 6-char prefix match would find nothing.
    const locatorRe = /^[A-R]{2}[0-9]{2}(?:[A-R]{2})?$/;
    const callsignRe = /^[A-Z0-9]{3,}(?:\/[A-Z0-9]+)*$/;
    let qth = rawQth;
    if (locatorRe.test(qth)) {
        if (qth.length > 4) {
            qth = qth.slice(0, 4);
            const qthInput = document.getElementById('qth');
            if (qthInput) qthInput.value = qth;
            window.__horstSetQTH?.(qth);
        }
    } else if (!callsignRe.test(qth) || !/[0-9]/.test(qth) || !/[A-Z]/.test(qth)) {
        showStreamError(`"${rawQth}" is not a valid locator (e.g. JO32) or callsign.`);
        return;
    }

    state.qth = qth;
    localStorage.setItem('qth', qth);
    localStorage.setItem('minutes', minutes);

    // The session ring only holds frames the server filtered for the current
    // qth, so a qth change invalidates the whole ring (the incoming cohort is
    // foreign). Compared against the qth the ring actually fed, not state.qth:
    // the qth input listener updates state.qth on every keystroke, so it can
    // already hold the new value when startLiveStream runs. A resubmit with
    // an unchanged qth keeps the ring (and the preserveData band change too).
    if (lastRingQth !== qth) {
        sessionRing.clear();
        lastRingQth = qth;
    }

    if (state.eventSource) state.eventSource.close();
    if (state.renderInterval) clearInterval(state.renderInterval);

    if (!preserveData) {
        state.liveSpots = [];
        if (state.heatLayer) {
            map.removeLayer(state.heatLayer);
            state.heatLayer = null;
        }
        clearDxClusterMarkers();
        resetRenderFingerprint();

        if (state.qthLayer) {
            map.removeLayer(state.qthLayer);
            state.qthLayer = null;
        }
    }

    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(qth);
    if (isLocator) {
        let lat = null, lng = null;
        let bounds = locatorToBounds(qth);

        if (bounds) {
            lat = (bounds[0][0] + bounds[1][0]) / 2;
            lng = (bounds[0][1] + bounds[1][1]) / 2;
            if (currentProjection() === 'azimuthal') {
                setAzimuthCenter([lat, lng]);
            }
        }

        if (currentProjection() !== 'azimuthal') {
            if (qth.length === 4 && bounds) {
                state.qthLayer = L.rectangle(bounds, { color: '#ff0000', weight: 3, fillOpacity: 0.1, interactive: false }).addTo(map);
            } else if (qth.length >= 6 && bounds) {
                const crossIcon = L.divIcon({
                    html: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" stroke="red" stroke-width="4" fill="none" stroke-linecap="round"><line x1="4" y1="4" x2="20" y2="20"></line><line x1="20" y1="4" x2="4" y2="20"></line></svg>',
                    className: 'qth-cross',
                    iconSize: [24, 24],
                    iconAnchor: [12, 12]
                });
                state.qthLayer = L.marker([lat, lng], { icon: crossIcon, interactive: false }).addTo(map);
            }
        }
    }

    const params = new URLSearchParams();
    params.append('qth', qth);
    if (minutes) params.append('minutes', minutes);
    if (document.getElementById('surroundings')?.checked) {
        params.append('surroundings', 'true');
    }

    // Server-side band/SNR filter: tell the backend which spots the client will
    // actually display so it can avoid sending the rest over the wire.
    const enabledBands = Array.from(getEnabledBands()).sort();
    if (enabledBands.length) {
        params.append('enabled_bands', enabledBands.join(','));
    }

    const minSnrMode = getMinSnrMode();
    let ssbMinDb = null;
    let cwMinDb = null;
    if (minSnrMode && minSnrMode !== 'none') {
        params.append('min_snr_mode', minSnrMode);
        if (minSnrMode === 'ssb') {
            ssbMinDb = document.getElementById('ssb-min-db')?.value || '0';
            params.append('ssb_min_db', ssbMinDb);
        } else if (minSnrMode === 'cw') {
            cwMinDb = document.getElementById('cw-min-db')?.value || '-15';
            params.append('cw_min_db', cwMinDb);
        }
    }
    // Remember the exact filter this connection is fetching, so later band/SNR
    // toggles can be handled client-side when the server already sends everything
    // we need.
    state.streamedFilter = {
        bands: new Set(enabledBands),
        minSnrMode,
        ssbMinDb,
        cwMinDb,
    };

    const liveTitle = liveForText(qth);
    let totalReceived = 0;
    let lastStatusUpdate = 0;
    setStreamStatus({ message: connectingText(qth), tone: 'warn', spinner: true });

    // Status line states for this connection: loading the history dump (or,
    // on a band change, the new selection) vs receiving live spots.
    const showLoadingStatus = (withCount) => setStreamStatus({
        title: liveTitle,
        message: preserveData
            ? STREAM_STATUS_TEXT.updating
            : (withCount ? `${STREAM_STATUS_TEXT.loading} (${formatNumber(totalReceived)})` : STREAM_STATUS_TEXT.loading),
        tone: 'warn',
        spinner: true,
    });
    const showLiveStatus = () => setStreamStatus({ title: liveTitle, message: spotCountText(totalReceived), tone: 'ok' });

    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit) setSubmitMode(btnSubmit, 'stop');

    // On mobile, hide sidebar after submitting so the map is immediately visible
    if (window.innerWidth <= 575) {
        const controls = document.getElementById('controls');
        const showBtn = document.getElementById('show-sidebar');
        if (controls) controls.style.marginLeft = '-' + controls.offsetWidth + 'px';
        if (showBtn) showBtn.style.display = 'block';
    }

    // When preserving data, seed a deduplication set from the existing spots so
    // the new connection's history dump doesn't create stacked duplicate markers.
    function spotKey(spot) {
        const band = String(spot.band || '').toLowerCase();
        const src = String(spot.sourceType || '').toLowerCase();
        const sender = String(spot.sender || '').toUpperCase();
        const receiver = String(spot.receiver || '').toUpperCase();
        // Bucket age to ~5 s to tolerate timestamp drift between history dumps.
        // Prefer the server-stamped receive age if already stamped; the prune
        // mutates ageSeconds, so __recvAge is the stable value.
        const age = spot.__recvAge ?? spot.ageSeconds ?? 0;
        const ageBucket = Math.floor(age / 5);
        return `${src}|${band}|${sender}|${receiver}|${ageBucket}|${spot.snr ?? ''}`;
    }
    const seenKeys = preserveData ? new Set(state.liveSpots.map(spotKey)) : null;

    state.eventSource = new EventSource(`/api/stream?${params.toString()}`);
    setFaviconColor(FAVICON.waiting);
    hotBandIndicator?.refresh();
    horstKevin?.refresh();

    let historyLoading = true;

    // Shared tail for the two mid-timeline fatal-drop paths (server_error and
    // a CLOSED onerror after the restart attempt): the stream is gone but the
    // timeline stays active — the ring still serves every covered moment
    // (KTD-7), only uncovered windows fall through to /api/history.
    function surfaceTimelineCoverageEnded() {
        setStreamStatus({ message: STREAM_STATUS_TEXT.ended, tone: 'danger' });
        setFaviconColor(FAVICON.error);
        if (btnSubmit) setSubmitMode(btnSubmit, 'go');
    }

    let everOpened = false; // per EventSource: reconnect wording only after a real connection
    state.eventSource.onopen = () => {
        everOpened = true;
        // On auto-reconnect EventSource re-sends a history dump before live
        // frames. Reset the loading flag so that dump is also suppressed from
        // rendering (and the 5s prune is gated) until history_end fires —
        // otherwise a reconnect paints the dump in chunks mid-stream. Harmless
        // on the initial connect (historyLoading is already true).
        historyLoading = true;
        showLoadingStatus(false);
        setFaviconColor(FAVICON.waiting); // until data arrives
    };

    state.eventSource.addEventListener('server_error', (e) => {
        state.eventSource.close();
        state.eventSource = null;
        if (state.renderInterval) {
            clearInterval(state.renderInterval);
            state.renderInterval = null;
        }
        historyLoading = false;
        // Mid-timeline the stream is torn down but the timeline stays active:
        // the ring still serves every covered moment (the reject was
        // deterministic, so no restart attempt), only uncovered windows fall
        // through to /api/history. The exit path starts a fresh stream.
        if (isTimelineActive()) {
            streamRestartedDuringTimeline = false;
            surfaceTimelineCoverageEnded();
            return;
        }
        state.streamedFilter = null;
        setStreamStatus({ message: serverErrorText(e.data), tone: 'danger' });
        setFaviconColor(FAVICON.error);
        if (btnSubmit) setSubmitMode(btnSubmit, 'go');
    });

    state.eventSource.addEventListener('history_end', () => {
        historyLoading = false;
        showLiveStatus();
        lastStatusUpdate = Date.now();
        scheduleRender();
    });

    state.eventSource.onmessage = (e) => {
        totalReceived++;

        let spot;
        try {
            spot = JSON.parse(e.data);
        } catch (err) {
            console.warn('Malformed spot frame, skipping:', err, e.data);
            return;
        }

        if (seenKeys) {
            const key = spotKey(spot);
            if (seenKeys.has(key)) return;
            seenKeys.add(key);
        }

        // Cap liveSpots: drop oldest-arrived in a batch when over the limit so a
        // hot-band burst between 5s prunes can't grow memory unbounded.
        if (state.liveSpots.length >= MAX_LIVE_SPOTS) {
            state.liveSpots.splice(0, state.liveSpots.length - MAX_LIVE_SPOTS + 1);
        }
        // Stamp receive time for the render-time true-age gate (see
        // getRenderableMapSpots). __recvAge is the server-stamped age at
        // receive; __recvMs is the client wall-clock at receive. The prune
        // mutates ageSeconds (+5/tick) but these stay fixed, so the render
        // filter computes a continuous true age.
        spot.__recvMs = Date.now();
        spot.__recvAge = spot.ageSeconds;
        // Session ring (plan 2026-09-11-002, U2): every received frame feeds
        // the ring — connect-time and reconnect history dumps included, since
        // they flow through this same handler. Unconditional: the feed must
        // not depend on any render gate (the timeline relies on the ring's
        // coverage even while live rendering is suppressed). The ring itself
        // rejects wspr spots (reference-only; time travel skips them).
        sessionRing.push(spot);
        state.liveSpots.push(spot);
        setFaviconColor(FAVICON.live);

        // Throttle DOM text updates to max ~4 times a second
        const now = Date.now();
        if (now - lastStatusUpdate > 250) {
            if (historyLoading && !preserveData) {
                showLoadingStatus(true);
            } else {
                showLiveStatus();
            }
            lastStatusUpdate = now;
        }

        // While preserving data we keep the map live (no empty flash); for fresh
        // starts we still suppress the chunky history dump until history_end.
        if (!historyLoading || preserveData) {
            scheduleRender();
        }
    };

    state.eventSource.onerror = (e) => {
        const es = e.currentTarget || state.eventSource;
        // EventSource auto-reconnects on transient errors (readyState CONNECTING);
        // only treat a CLOSED connection as fatal so a brief blip doesn't flap
        // the status red and tear down a stream that will recover on its own.
        if (es && es.readyState === EventSource.CLOSED) {
            // KTD-11: fatal drop during the timeline — one silent restart
            // attempt (the ring absorbs the reconnect dump and the render gate
            // keeps the moment on screen). A second failure ends live coverage:
            // surface the ring-staleness state instead of the generic error,
            // but leave the timeline active — the ring still legitimately
            // serves every covered moment (KTD-7); only windows past the ring's
            // last received t fall through to /api/history.
            if (isTimelineActive()) {
                if (state.eventSource) {
                    state.eventSource.close();
                    state.eventSource = null;
                }
                if (!streamRestartedDuringTimeline) {
                    streamRestartedDuringTimeline = true;
                    console.warn("Stream closed during timeline — one silent restart attempt");
                    startLiveStream(true);
                    return;
                }
                streamRestartedDuringTimeline = false;
                if (state.renderInterval) {
                    clearInterval(state.renderInterval);
                    state.renderInterval = null;
                }
                surfaceTimelineCoverageEnded();
                return;
            }
            console.error("Stream closed (fatal):", e);
            if (state.eventSource) {
                state.eventSource.close();
                state.eventSource = null;
            }
            if (state.renderInterval) {
                clearInterval(state.renderInterval);
                state.renderInterval = null;
            }
            historyLoading = false;
            state.streamedFilter = null;
            setStreamStatus({ message: STREAM_STATUS_TEXT.disconnected, tone: 'danger' });
            setFaviconColor(FAVICON.error);
            if (btnSubmit) setSubmitMode(btnSubmit, 'go');
            return;
        }
        // Transient — reconnecting. Surface it but don't tear down.
        console.warn("Stream error (reconnecting):", e);
        setStreamStatus(everOpened
            ? { title: liveTitle, message: STREAM_STATUS_TEXT.reconnecting, tone: 'warn', spinner: true }
            : { message: STREAM_STATUS_TEXT.unreachable, tone: 'warn', spinner: true });
        setFaviconColor(FAVICON.waiting);
    };

    state.renderInterval = setInterval(() => {
        if (state.softPaused) return;
        // Prune/age continuously when preserving data so existing spots don't
        // freeze on screen while the new band's history loads. For fresh starts,
        // keep suppressing during the history dump to avoid a half-loaded grid.
        if (historyLoading && !preserveData) return;

        if (state.liveSpots.length > 0) {
            // Read the live Max Spot Age slider value rather than the `minutes`
            // captured at stream start: the slider only triggers a re-render
            // (Range.svelte), it does not restart the stream, so the prune must
            // follow the current control. This also keeps the prune consistent
            // with resumeFromSoftPause, which reads the live value.
            const maxAge = getCurrentMaxSpotAgeSeconds();
            state.liveSpots.forEach(s => s.ageSeconds += 5);
            state.liveSpots = state.liveSpots.filter(s => s.ageSeconds <= maxAge);
            scheduleRender();
        }
    }, 5000);
}

// teardownTimelineFrame is the shared timeline-exit prologue: the flags both
// exit paths reset, the timeline teardown, and the retained-moment/clock
// reset. Callers add their own restore (ring rebuild + stream restart, or the
// plain programmatic-exit handoff to a fresh stream).
function teardownTimelineFrame() {
    streamRestartedDuringTimeline = false;
    exitTimeline();
    hideAfterglow();
    syncTimelineURL();
    resetRenderFingerprint();
    // Drop the retained moment so no stale frame survives the gate, and
    // restore the grayline clock to the wall (live mode).
    lastMomentSpots = null;
    lastMomentPlayhead = 0;
    clearDataNowOverride();
}

// exitTimelineForRestart is the plain programmatic exit used when the timeline
// must end because the stream cohort is about to change (qth / surroundings /
// submit): no ring rebuild — the caller's full re-stream (or teardown) follows.
// The ring itself is cleared by the callers that change the cohort (startLive
// Stream's qth hook, the surroundings handler).
function exitTimelineForRestart() {
    if (!isTimelineActive()) return;
    teardownTimelineFrame();
}

document.getElementById('fetch-form')?.addEventListener('submit', (e) => {
    e.preventDefault();
    if (!map) return;

    const btnSubmit = document.getElementById('btn-submit');

    if (btnSubmit && isStreaming(btnSubmit)) {
        // Submit (stop) click mid-timeline (KTD-12): exit the timeline first,
        // then perform the original stop teardown. The ring survives — a later
        // re-stream with the same qth keeps its coverage (U2 contract).
        if (isTimelineActive()) exitTimelineForRestart();
        if (state.eventSource) {
            state.eventSource.close();
            state.eventSource = null;
        }
        if (state.renderInterval) {
            clearInterval(state.renderInterval);
            state.renderInterval = null;
        }
        state.liveSpots = [];
        if (state.heatLayer) {
            map.removeLayer(state.heatLayer);
            state.heatLayer = null;
        }
        clearDxClusterMarkers();
        clearWsprMarkers();
        resetRenderFingerprint();
        if (state.qthLayer) {
            map.removeLayer(state.qthLayer);
            state.qthLayer = null;
        }
        state.streamedFilter = null;

        setSubmitMode(btnSubmit, 'go');
        setStreamStatus({ message: STREAM_STATUS_TEXT.idle });
        setFaviconColor(FAVICON.idle);
        updateBandLab({ force: true });
        updateWsprMatrix();
        return;
    }

    // Starting a stream (go) mid-timeline — the single point all qth-change
    // paths funnel through (they set the submit button to 'go' first): exit
    // the timeline, then the normal fresh re-stream runs. startLiveStream's
    // qth hook clears the ring when the qth actually differs.
    if (isTimelineActive()) exitTimelineForRestart();
    startLiveStream(false);
});

document.addEventListener('visibilitychange', syncSoftPauseWithVisibility);
window.addEventListener('pageshow', syncSoftPauseWithVisibility);
window.addEventListener('focus', syncSoftPauseWithVisibility);

// --- Time Travel (timeline.js integration) ---------------------------------
// While the timeline is active the SSE stream STAYS OPEN (KTD-3): rendering is
// gated instead of the stream, so liveSpots and the session ring keep
// accumulating while the map is driven by timeline moments. Exiting rebuilds
// state.liveSpots from the ring's tail — no server dump (KTD-11).

// The retained current moment (last emitted spots + playhead). The
// scheduleRender gate re-renders it with current styles when style-affecting
// controls change mid-replay (theme, projection, band focus, ...).
let lastMomentPlayhead = 0;
let lastMomentSpots = null;

// Render the moment spots through the normal pipeline (same renderers) —
// bypassing the wall-clock age gate, since timeline ages are data-time
// (playhead-relative) and always small. Shared by the moment listener and the
// timeline-gated scheduleRender.
function renderTimelineMomentFrame(moved = false) {
    if (!lastMomentSpots) return;
    if (isAzimuthEnabled()) {
        updateBandLabels(lastMomentSpots);
        // Pass the playhead the moment carries (U6): the azimuth grayline cache
        // key buckets on it, so scrub snaps to 5-minute buckets and playback
        // rebuilds only per bucket crossing (KTD-8). Same value on the gate
        // re-render path — this function is shared.
        renderAzimuthScene({ spots: lastMomentSpots, dataNowMs: lastMomentPlayhead * 1000 });
        syncAzimuthZoomOutHint();
    } else {
        updateMapVisualization(lastMomentSpots, parseInt(document.getElementById('minutes')?.value || '15', 10));
        // Afterglow overlay (Mercator only; azimuth renders its own scene).
        // moved marks a playhead move (decay active); a gate re-render is a
        // re-style, not a move, so no re-fade.
        updateAfterglow(lastMomentSpots, lastMomentPlayhead, moved);
        // Moment-driven grayline sync (plan 2026-09-11-002 U5, R8): the clock
        // is pinned to the playhead by the moment listener, and the layer's
        // 5-minute bucket/key check in map.js dedupes within a bucket — so
        // every moment (or gate re-render) syncs cheaply and only a playhead
        // bucket crossing rebuilds the overlay. No separate force rule: the
        // cache key always reflects the last built bucket, so a mismatch (e.g.
        // theme change mid-replay) rebuilds on its own.
        void syncMercatorGraylineLayer();
    }
}

onTimelineMoment((spots, playhead) => {
    if (!isTimelineActive()) return;
    lastMomentPlayhead = playhead;
    lastMomentSpots = spots;
    // Pin the shared grayline clock (KTD-9) to the playhead so map.js's
    // bucket/key derivation (and azimuth's in U6) follows data time.
    setDataNowMs(playhead * 1000);
    renderTimelineMomentFrame(true);
    updateBandLab();
    updateWsprMatrix();
    syncTimelineURL();
});

// One-shot flag for the KTD-11 mid-timeline fatal-drop restart attempt.
let streamRestartedDuringTimeline = false;

// KTD-11: rebuild state.liveSpots from the ring's last live window — deduped,
// t-sorted, no server dump. The window comes from the current Max-Spot-Age
// controls (the same semantics the render age gate uses). Ring entries are raw
// historySpot-shaped copies without age fields, so ageSeconds is reconstructed
// from the absolute t and the receive stamps are re-cohered: __recvAge is the
// age at rebuild, __recvMs is now, so the render's true-age computation
// (trueAge = __recvAge + (now - __recvMs)/1000) starts at exactly ageSeconds
// and keeps aging smoothly — no bulk-aging, no over-aged empty map.
function rebuildLiveSpotsFromRing() {
    const nowMs = Date.now();
    const nowSec = Math.floor(nowMs / 1000);
    const windowSec = getCurrentMaxSpotAgeSeconds();
    const raw = sessionRing.slice(Math.max(0, nowSec - windowSec), nowSec);
    state.liveSpots = raw.map((s) => {
        const ageSeconds = Math.max(0, nowSec - s.t);
        return {
            lat: s.lat,
            lng: s.lng,
            snr: s.snr,
            locator: s.locator,
            reporterLocator: s.reporterLocator,
            sourceType: s.sourceType,
            band: s.band,
            sender: s.sender,
            receiver: s.receiver,
            ageSeconds,
            __recvMs: nowMs,
            __recvAge: ageSeconds,
        };
    });
}

async function startTimelineMode(rangeSeconds) {
    if (isTimelineActive()) return;
    // KTD-3: the SSE stream STAYS OPEN. Rendering is gated in scheduleRender
    // instead, so liveSpots and the ring keep accumulating during the replay
    // and the age-prune interval keeps bounding liveSpots memory. Only the
    // timeline-incompatible overlays are cleared.
    streamRestartedDuringTimeline = false;
    if (state.heatLayer) { map.removeLayer(state.heatLayer); state.heatLayer = null; }
    clearDxClusterMarkers();
    clearWsprMarkers();
    resetRenderFingerprint();
    // The submit button keeps reflecting the (still running) stream: a stop
    // click mid-timeline must remain a stop action.
    setStreamStatus({ message: STREAM_STATUS_TEXT.timeTravel });

    try {
        await enterTimeline(rangeSeconds);
    } catch (err) {
        // A superseded/aborted fetch (rapid re-entry, mode exit during load) is
        // benign — only a real failure tears the mode down.
        if ((err?.name || '') === 'AbortError') return;
        console.warn('timeline enter failed', err);
        setStreamStatus({ message: timeTravelErrorText(err?.message || err), tone: 'danger' });
        // KTD-12: entry failure must NOT stack a second EventSource on the
        // still-open stream. The stream keeps running; the status line carries
        // the error. (With no stream running, e.g. a shared-URL restore, the
        // error is the only surface — the user can still submit manually.)
        return;
    }
    if (!isTimelineActive()) return; // enterTimeline bailed (no qth)
    timelinePlay();
}

function stopTimelineMode() {
    teardownTimelineFrame();

    if (state.eventSource) {
        // KTD-11: rebuild the live list from the ring's tail — the SSE has been
        // open the whole time, so no reconnect and no server dump. A filter
        // change made during the replay (queued widening) is reconciled here
        // through the existing restart semantics; when nothing changed this is
        // a pure client-side refresh.
        rebuildLiveSpotsFromRing();
        scheduleRender();
        restartStreamIfSubscribed();
    } else if (localStorage.getItem('qth')) {
        // The stream was not running at entry (shared-URL restore) or died
        // mid-replay: a fresh stream is the only live source. One dump accepted.
        startLiveStream(false);
    }
}

// The bar's Live button exits through timeline.js; hook the live restore here.
onTimelineExit(stopTimelineMode);

// Live-mode grayline refresh (plan 2026-09-11-002 U5, R8): without it the
// terminator only rebuilt on boot/theme/projection syncs, so a 5-minute wall
// bucket could pass unrefreshed for minutes. Wall-clock cadence (KTD-9): the
// check below reads Date.now() only, and only fires once per 5-minute wall
// bucket (the same bucket size map.js keys on); when the layer is disabled or
// the azimuth projection is active (U6 owns that leg) the sync is skipped.
const GRAYLINE_LIVE_REFRESH_MS = 60 * 1000;
let lastGraylineLiveBucket = Math.floor(Date.now() / GRAYLINE_BUCKET_MS);
setInterval(() => {
    if (isTimelineActive() || isAzimuthEnabled()) return;
    const bucket = Math.floor(Date.now() / GRAYLINE_BUCKET_MS);
    if (bucket === lastGraylineLiveBucket) return;
    lastGraylineLiveBucket = bucket;
    void syncMercatorGraylineLayer();
}, GRAYLINE_LIVE_REFRESH_MS);

// Auto-enter from a shared URL (?tl=1&t0=&t1=&spd=). Runs once the app is
// otherwise booted; a missing live stream (not subscribed) is fine — the
// timeline bundle renders the past and stopTimelineMode re-streams on exit.
function maybeRestoreTimelineFromURL() {
    const tl = readTimelineURL();
    if (!tl) return;
    const span = Math.max(60, Math.min(24 * 60 * 60, tl.t1 - tl.t0));
    void startTimelineMode(span).then(() => {
        if (isTimelineActive()) void seek(tl.t1);
    });
}
setTimeout(maybeRestoreTimelineFromURL, 1200);

// Sidebar entry point: Time Travel button (1h default; presets inside the bar).
document.getElementById('btn-timeline')?.addEventListener('click', () => {
    void startTimelineMode(60 * 60);
});

// Fallback pass in case autostart check happened before submit wiring was ready.
maybeAutoStartSavedQth();
syncSoftPauseWithVisibility();

// Chase Queue (dxcluster.js) is an opt-in operator surface hidden by default.
// It is no longer a static <script> in index.html; inject the module on demand
// so the payload is only downloaded when opted in. The predicate lives in
// cq-flag.js, shared with dxcluster.js's CQ_ENABLED.
// Note: a createElement script tag, not import(). The offline payload
// analyzer's static module-graph walk does not count these bytes in
// wire_gzip_kb; the harness reports them separately in lazy_gzip_kb.
if (isChaseQueueEnabled()) {
    const cqScript = document.createElement('script');
    cqScript.type = 'module';
    cqScript.src = 'dxcluster.js';
    document.head.appendChild(cqScript);
}

// Sync opmode controls visibility (initOpMode may have set checkbox from localStorage)
requestAnimationFrame(() => {
    // Auto-hide sidebar on mobile at startup
    if (window.innerWidth <= 575) {
        const controls = document.getElementById('controls');
        const showBtn = document.getElementById('show-sidebar');
        if (controls) controls.style.marginLeft = '-' + controls.offsetWidth + 'px';
        if (showBtn) showBtn.style.display = 'block';
    }
});