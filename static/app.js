import { state } from './state.js';
import { loadConfig } from './config.js';
import { initMap, setTheme, map, syncMercatorCountryLayer, syncMercatorGraylineLayer, syncMercatorDxccLabelLayer, setMercatorDxHighlight, clearMercatorDxHighlight } from './map.js';
import { initAzimuthCanvas, isAzimuthEnabled, loadAzimuthWorldGeoJson, renderAzimuthScene, setAzimuthCenter, getAzimuthCenter, setAzimuthEnabled, setAzimuthDragging, setAzimuthTheme, setAzimuthZoom, clampAzimuthZoom, setAzimuthHorizonKm, clampAzimuthHorizonKm, setAzimuthNs6tIndicatorEnabled, setAzimuthDxccLabelDensity, setAzimuthDxccLabelsEnabled, getAzimuthLatLngFromClientPoint, getAzimuthHiddenGridSquaresCount, setAzimuthDxSpotHighlight } from './azimuth-runtime.js';
import { initUI, attachUITooltipEvents } from './ui.js';
import { getBandLabLookbackMinutes, initBandLab, updateBandLab } from './band-lab.js';
import { initHotBandIndicator } from './hot-band-indicator.js';
import { initHorstKevin } from './horst-kevin.js';
import { updateMapVisualization, updateBandLabels } from './renderers.js';
import { latLngToLocator, locatorToBounds, normalizeLongitude, setFaviconColor, getMinSnrMode, getEnabledBands, getSelectedBand, formatNumber, bandColors, getCountryColoringEnabled } from './utils.js';
import { endPerfTimer, incrementPerfCounter, installPerfDebugApi, perfNow, startPerfTimer } from './perf.js';
import { initOpMode, isOpModeActive, setBeamTargetFromMapClick, getOpModeStation } from './opmode.js';

// --- Azimuth Zoom State ---
const AZIMUTH_MAX_HORIZON_KM = 20015;
const AZIMUTH_MIN_ZOOM = 1.0;
const AZIMUTH_MAX_ZOOM = 5.0;
let suppressAzimuthClickUntil = 0;
let hotBandIndicator = null;
let horstKevin = null;
// Horst-Kevin mascot temporarily disabled (to be revised). Set true to re-enable;
// the #horst-kevin element in index.html is also hidden via inline display:none.
const HORST_KEVIN_ENABLED = false;

function switchToBand(band) {
    const radio = document.querySelector(`input[name="band"][value="${band}"]`);
    if (!radio || radio.disabled) return;
    radio.checked = true;
    radio.dispatchEvent(new Event('change', { bubbles: true }));
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

    const target = (params.get('target') || 'JO32').trim().toUpperCase();
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
        target,
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
        const band2mRadio = document.querySelector('input[name="band"][value="2m"]');
        const allBandRadio = document.querySelector('input[name="band"][value="all"]');

        if (band2mEnable) {
            band2mEnable.checked = false;
            localStorage.setItem('enable-2m', 'false');
        }
        if (band2mRadio) {
            band2mRadio.disabled = true;
            if (band2mRadio.checked && allBandRadio) {
                allBandRadio.checked = true;
                localStorage.setItem('selectedBand', 'all');
                updateCurrentBandDisplay();
            }
        }

    }

    scheduleRender();
}

async function fetchSnapshotFrame(config, snapshotAt) {
    if (!config?.enabled) return { spots: [] };

    const params = new URLSearchParams();
    params.set('target', config.target);
    params.set('minutes', String(config.minutes));
    params.set('surroundings', config.surroundings ? 'true' : 'false');
    params.set('min_snr_mode', config.minSnrMode);
    params.set('ssb_min_db', String(config.ssbMinDb));
    params.set('cw_min_db', String(config.cwMinDb));
    params.set('selected_band', config.selectedBand);
    if (config.enabledBandsCsv) params.set('enabled_bands', config.enabledBandsCsv);
    params.set('include_dxcluster', config.includeDxcluster ? 'true' : 'false');
    if (Number.isFinite(snapshotAt)) {
        params.set('snapshot_at', String(snapshotAt));
    }

    const response = await fetch(`/api/capture_snapshot?${params.toString()}`);
    if (!response.ok) {
        const msg = await response.text();
        throw new Error(`snapshot request failed (${response.status}): ${msg}`);
    }

    return response.json();
}

async function loadSnapshotFrame(config, snapshotAt, readyKey = 'capture') {
    const payload = await fetchSnapshotFrame(config, snapshotAt);
    state.liveSpots = Array.isArray(payload?.spots) ? payload.spots : [];

    scheduleRender();
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));

    const stamp = payload?.snapshot_at ?? snapshotAt ?? null;
    const statusEl = document.getElementById('stream-status');
    const spotCountLabel = formatNumber(state.liveSpots.length);
    if (statusEl) {
        statusEl.innerHTML = `Status: Capture Snapshot ready (Spots: ${spotCountLabel})`;
    }
    window.__horstCaptureReady = {
        ready: true,
        count: state.liveSpots.length,
        snapshotAt: stamp,
        generatedAt: payload?.generated_at ?? null
    };

    return payload;
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
    if (showDXClusterSpots) return spots;

    return spots.filter((spot) => String(spot?.sourceType || '').toLowerCase() !== 'dxcluster');
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
}

function syncSoftPauseWithVisibility() {
    if (document.hidden) {
        applySoftPause();
    } else {
        resumeFromSoftPause();
    }
}

function getActiveTargetCenter() {
    const rawTarget = document.getElementById('target')?.value?.trim()?.toUpperCase() || '';
    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(rawTarget);
    if (!isLocator) return null;

    const bounds = locatorToBounds(rawTarget);
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
        const c = getActiveTargetCenter();
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

function syncProjectionCenterToActiveTarget() {
    const targetCenter = getActiveTargetCenter();
    if (!targetCenter) return;

    const projection = currentProjection();
    if (projection === 'azimuthal') {
        setAzimuthCenter(targetCenter);
    } else {
        return;
    }

    scheduleRender();
}

function maybeAutoStartSavedTarget() {
    if (captureConfig?.enabled) {
        return;
    }
    if (autoStartTriggered || !appReadyForAutoStart || !map) {
        return;
    }

    const savedTarget = localStorage.getItem('target')?.trim()?.toUpperCase();
    const targetInput = document.getElementById('target');
    if (!savedTarget || !targetInput) {
        return;
    }

    // Ensure the input is populated from storage even if another init step missed it.
    if (!targetInput.value?.trim()) {
        targetInput.value = savedTarget;
    }

    const targetValue = targetInput.value?.trim()?.toUpperCase();
    if (!targetValue) {
        return;
    }

    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit) btnSubmit.textContent = 'Go';

    const form = document.getElementById('fetch-form');
    if (!form) {
        return;
    }

    form.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));

    const streamStarted = Boolean(state.eventSource) || document.getElementById('btn-submit')?.textContent === 'Stop';
    if (streamStarted) {
        autoStartTriggered = true;
        return;
    }

    // If submit wiring was not yet active at this moment, retry shortly.
    setTimeout(() => {
        maybeAutoStartSavedTarget();
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

async function applyProjectionMode(projection) {
    syncStyleAvailabilityForProjection(projection);
    syncProjectionOptionVisibility(projection);
    const targetCenter = getActiveTargetCenter();

    if (projection === 'azimuthal') {
        await loadAzimuthWorldGeoJson();
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
    syncAzimuthZoomOutHint();
    if (map) map.invalidateSize();
    await syncMercatorOverlays(true);
    scheduleRender();
}

function applyCaptureConfigToControls(config) {
    if (!config?.enabled) return;

    const targetEl = document.getElementById('target');
    if (targetEl) targetEl.value = config.target;

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

    const selectedBandRadio = document.querySelector(`input[name="band"][value="${config.selectedBand}"]`)
        || document.querySelector('input[name="band"][value="all"]');
    if (selectedBandRadio) selectedBandRadio.checked = true;

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
        const radio = document.querySelector(`input[name="band"][value="${band}"]`);
        if (radio) radio.disabled = !cb.checked;
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
    params.set('target', config.target);
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

    const statusEl = document.getElementById('stream-status');
    if (statusEl) statusEl.innerHTML = 'Status: Capture snapshot loading...';

    const response = await fetch(`/api/capture_snapshot?${params.toString()}`);
    if (!response.ok) {
        const msg = await response.text();
        throw new Error(`capture snapshot request failed (${response.status}): ${msg}`);
    }

    const payload = await response.json();
    state.liveSpots = Array.isArray(payload?.spots) ? payload.spots : [];

    scheduleRender();

    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    if (statusEl) {
        statusEl.innerHTML = `Status: Capture snapshot ready (Spots: ${formatNumber(state.liveSpots.length)})`;
    }
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
        if (currentProjection() !== 'mercator') return;

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
    });

    const setTargetAndRestart = (locator) => {
        if (!locator) return;
        const targetInput = document.getElementById('target');
        if (!targetInput) return;

        targetInput.value = locator;
        const btnSubmit = document.getElementById('btn-submit');
        if (btnSubmit) btnSubmit.textContent = 'Go'; // Force a clean restart
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
        setTargetAndRestart(loc);
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
        setTargetAndRestart(loc);
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

    const initialProjection = captureConfig?.enabled ? captureConfig.projection : savedProjection;
    const projRadio = document.querySelector(`input[name="projection-select"][value="${initialProjection}"]`);
    if (projRadio) projRadio.checked = true;
    await applyProjectionMode(initialProjection);
    syncProjectionCenterToActiveTarget();

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
    hotBandIndicator = initHotBandIndicator({
        getTarget: () => document.getElementById('target')?.value?.trim()?.toUpperCase() || '',
        getSurroundings: () => Boolean(document.getElementById('surroundings')?.checked),
        getCurrentBand: () => getSelectedBand(),
        onBandSwitch: switchToBand,
    });
    if (HORST_KEVIN_ENABLED) {
        horstKevin = initHorstKevin({
            getTarget: () => document.getElementById('target')?.value?.trim()?.toUpperCase() || '',
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
            const statusEl = document.getElementById('stream-status');
            const errorText = String(err?.message || err);
            window.__horstCaptureReady = { ready: false, error: errorText };
            if (statusEl) statusEl.innerHTML = `Status: Capture snapshot failed (${errorText})`;
        }
    }

    appReadyForAutoStart = true;
    maybeAutoStartSavedTarget();
})();

function updateCurrentBandDisplay() {
    const band = getSelectedBand();
    const display = document.getElementById('current-band-display');
    if (display) {
        display.textContent = band === 'all' ? 'All Bands' : band;
        display.style.backgroundColor = bandColors[band] || bandColors['all'];
        display.style.color = (band === '15m' || band === '12m') ? '#212529' : '#fff';
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

            const minutes = document.getElementById('minutes')?.value || 15;
            const renderSpots = getRenderableMapSpots(state.liveSpots);
            if (isAzimuthEnabled()) {
                updateBandLabels(renderSpots);
                renderAzimuthScene({ spots: renderSpots });
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

document.getElementById('style-group')?.addEventListener('change', (e) => {
    if (e.target.name === 'style-select') {
        localStorage.setItem('mapStyle', e.target.value);
        scheduleRender();
    }
});

document.getElementById('projection-group')?.addEventListener('change', async (e) => {
    if (e.target?.name !== 'projection-select') return;
    localStorage.setItem('mapProjection', e.target.value);
    await applyProjectionMode(e.target.value);
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

document.getElementById('min-snr-group')?.addEventListener('change', (e) => {
    if (e.target.name === 'min-snr') {
        localStorage.setItem('minSnrSelect', e.target.value);
        scheduleRender();
    }
});

document.getElementById('target')?.addEventListener('input', () => {
    syncProjectionCenterToActiveTarget();
});

document.getElementById('cycle-time')?.addEventListener('input', (e) => {
    const val = document.getElementById('cycle-time-val');
    if (val) val.textContent = e.target.value;
    localStorage.setItem('cycleTime', e.target.value);
});

document.getElementById('cycle-time')?.addEventListener('change', (e) => {
    if (state.cycleInterval) {
        // Restart cycle to pick up the new time
        const btn = document.getElementById('btn-cycle');
        btn?.click();
        btn?.click();
    }
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

document.getElementById('band-container')?.addEventListener('change', (e) => {
    if (e && e.isTrusted && state.cycleInterval && e.target && e.target.name === 'band') {
        clearInterval(state.cycleInterval);
        state.cycleInterval = null;
        const btn = document.getElementById('btn-cycle');
        if (btn) {
            btn.innerHTML = '<i class="fas fa-play"></i>';
            btn.title = 'Cycle Active Bands';
            btn.classList.remove('active');
        }
    }
    localStorage.setItem('selectedBand', getSelectedBand());
    updateCurrentBandDisplay();
    updateBandLab({ force: true });
    scheduleRender();
    hotBandIndicator?.rerender();
    hotBandIndicator?.refresh();
    horstKevin?.refresh();
});

document.querySelectorAll('.band-enable').forEach(cb => {
    cb.addEventListener('change', (e) => {
        const band = e.target.value;
        const radio = document.querySelector(`input[name="band"][value="${band}"]`);
        if (radio) {
            radio.disabled = !e.target.checked;
            if (!e.target.checked && radio.checked) {
                document.querySelector('input[name="band"][value="all"]').checked = true;
                localStorage.setItem('selectedBand', 'all');
            }
        }
        localStorage.setItem(`enable-${band}`, e.target.checked);
        updateBandLab({ force: true });
        scheduleRender();
    });
});

document.getElementById('surroundings')?.addEventListener('change', (e) => {
    localStorage.setItem('surroundings', e.target.checked);
    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit && btnSubmit.textContent === 'Stop') {
        btnSubmit.textContent = 'Go';
        document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
    hotBandIndicator?.refresh();
    horstKevin?.refresh();
});

document.getElementById('show-dxcluster-spots')?.addEventListener('change', (e) => {
    localStorage.setItem('showDXClusterSpots', e.target.checked);
    scheduleRender();
});

document.getElementById('show-country-coloring')?.addEventListener('change', (e) => {
    localStorage.setItem('countryColoringEnabled', e.target.checked ? 'true' : 'false');
    if (!isAzimuthEnabled()) {
        void syncMercatorCountryLayer({ force: true, enabled: e.target.checked });
    } else {
        scheduleRender();
    }
});

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

document.getElementById('opmode-allow-control')?.addEventListener('change', (e) => {
    const group = document.getElementById('opmode-controls-group');
    if (group) group.style.display = e.target.checked ? '' : 'none';
});

document.getElementById('btn-geo')?.addEventListener('click', () => {
    if (!navigator.geolocation) {
        alert('Geolocation is not supported by your browser.');
        return;
    }

    const btn = document.getElementById('btn-geo');
    if (!btn) return;
    const originalText = btn.innerHTML;
    btn.innerHTML = '<i class="fas fa-spinner fa-spin"></i>';
    btn.disabled = true;

    navigator.geolocation.getCurrentPosition(
        (position) => {
            // Pre-fill with a 4-character locator (square)
            const loc = latLngToLocator(position.coords.latitude, position.coords.longitude, 4);
            const targetEl = document.getElementById('target');
            if (targetEl) targetEl.value = loc;
            localStorage.setItem('target', loc);
            btn.innerHTML = originalText;
            btn.disabled = false;
            
            const btnSubmit = document.getElementById('btn-submit');
            if (btnSubmit) btnSubmit.textContent = 'Go';
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

document.getElementById('btn-center')?.addEventListener('click', () => {
    if (!map) return;

    const target = document.getElementById('target')?.value.trim().toUpperCase() || '';
    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(target);
    if (isLocator) {
        const bounds = locatorToBounds(target);
        if (bounds) {
            const lat = (bounds[0][0] + bounds[1][0]) / 2;
            const lng = (bounds[0][1] + bounds[1][1]) / 2;
            if (currentProjection() === 'azimuthal') {
                setAzimuthCenter([lat, lng]);
                scheduleRender();
            } else {
                map.setView([lat, lng], map.getZoom());
            }
        }
    } else if (target) {
        alert('Cannot center: Please provide a valid Maidenhead locator.');
    }
});

document.getElementById('btn-cycle')?.addEventListener('click', () => {
    const btn = document.getElementById('btn-cycle');
    if (!btn) return;
    if (state.cycleInterval) {
        clearInterval(state.cycleInterval);
        state.cycleInterval = null;
        btn.innerHTML = '<i class="fas fa-play"></i>';
        btn.title = 'Cycle Active Bands';
        btn.classList.remove('active');
    } else {
        btn.innerHTML = '<i class="fas fa-pause"></i>';
        btn.title = 'Stop Cycling';
        btn.classList.add('active');
        const cycleTimeMs = parseInt(document.getElementById('cycle-time')?.value || '3', 10) * 1000;
        state.cycleInterval = setInterval(() => {
            const minSnrMode = getMinSnrMode();
            const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
            const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
            const activeBands = new Set();
            const enabledBands = getEnabledBands();

            state.liveSpots.forEach(s => {
                if (minSnrMode === 'ssb' && s.snr < ssbMinDb) return;
                if (minSnrMode === 'cw' && s.snr < cwMinDb) return;
                if (!enabledBands.has(s.band)) return;
                activeBands.add(s.band);
            });

            const radios = Array.from(document.querySelectorAll('input[name="band"]'))
                .filter(r => {
                    if (r.disabled) return false;
                    if (r.value === 'all') return true;
                    return activeBands.has(r.value);
                });
            
            if (radios.length === 0) return;

            const currentBand = getSelectedBand();
            let currentIndex = radios.findIndex(r => r.value === currentBand);
            let nextIndex = (currentIndex + 1) % radios.length;
            
            radios[nextIndex].checked = true;
            document.getElementById('band-container')?.dispatchEvent(new Event('change'));
        }, cycleTimeMs);
    }
});

document.getElementById('target')?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
        e.preventDefault();
        const btnSubmit = document.getElementById('btn-submit');
        if (btnSubmit) btnSubmit.textContent = 'Go';
        document.getElementById('fetch-form')?.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
});

document.getElementById('fetch-form')?.addEventListener('submit', (e) => {
    e.preventDefault();
    if (!map) return;

    const btnSubmit = document.getElementById('btn-submit');

    if (btnSubmit && btnSubmit.textContent === 'Stop') {
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
        if (state.targetLayer) {
            map.removeLayer(state.targetLayer);
            state.targetLayer = null;
        }

        btnSubmit.textContent = 'Go';
        const status = document.getElementById('stream-status');
        if (status) status.innerHTML = 'Status: Not subscribed';
        setFaviconColor('#6c757d');
        updateBandLab({ force: true });
        return;
    }

    const target = document.getElementById('target')?.value.trim().toUpperCase() || '';
    const minutes = document.getElementById('minutes')?.value || 15;

    if (!target) {
        alert('Please provide a Callsign or Locator.');
        return;
    }

    localStorage.setItem('target', target);
    localStorage.setItem('minutes', minutes);

    console.log(`Starting live stream for target: '${target}'`);

    if (state.eventSource) state.eventSource.close();
    if (state.renderInterval) clearInterval(state.renderInterval);
    
    state.liveSpots = [];
    if (state.heatLayer) {
        map.removeLayer(state.heatLayer);
        state.heatLayer = null;
    }

    if (state.targetLayer) {
        map.removeLayer(state.targetLayer);
        state.targetLayer = null;
    }

    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(target);
    if (isLocator) {
        let lat = null, lng = null;
        let bounds = locatorToBounds(target);
        
        if (bounds) {
            lat = (bounds[0][0] + bounds[1][0]) / 2;
            lng = (bounds[0][1] + bounds[1][1]) / 2;
            if (currentProjection() === 'azimuthal') {
                setAzimuthCenter([lat, lng]);
            }
        }

        if (currentProjection() !== 'azimuthal') {
            if (target.length === 4 && bounds) {
                state.targetLayer = L.rectangle(bounds, { color: '#ff0000', weight: 3, fillOpacity: 0.1, interactive: false }).addTo(map);
            } else if (target.length >= 6 && bounds) {
                const crossIcon = L.divIcon({
                    html: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" stroke="red" stroke-width="4" fill="none" stroke-linecap="round"><line x1="4" y1="4" x2="20" y2="20"></line><line x1="20" y1="4" x2="4" y2="20"></line></svg>',
                    className: 'target-cross',
                    iconSize: [24, 24],
                    iconAnchor: [12, 12]
                });
                state.targetLayer = L.marker([lat, lng], { icon: crossIcon, interactive: false }).addTo(map);
            }
        }
    }

    const params = new URLSearchParams();
    params.append('target', target);
    if (minutes) params.append('minutes', minutes);
    if (document.getElementById('surroundings')?.checked) {
        params.append('surroundings', 'true');
    }

    const statusEl = document.getElementById('stream-status');
    const currentSub = `Target: ${target}`;
    let totalReceived = 0;
    let lastStatusUpdate = 0;
    statusEl.innerHTML = `Status: Connecting to ${currentSub}...`;

    if (btnSubmit) btnSubmit.textContent = 'Stop';

    // On mobile, hide sidebar after submitting so the map is immediately visible
    if (window.innerWidth <= 575) {
        const controls = document.getElementById('controls');
        const showBtn = document.getElementById('show-sidebar');
        if (controls) controls.style.marginLeft = '-' + controls.offsetWidth + 'px';
        if (showBtn) showBtn.style.display = 'block';
    }

    state.eventSource = new EventSource(`/api/stream?${params.toString()}`);
    setFaviconColor('#ffa500'); // Orange for connecting/waiting
    hotBandIndicator?.refresh();
    horstKevin?.refresh();
    
    let historyLoading = true;

    state.eventSource.onopen = () => {
        console.log("Connected to live MQTT stream");
        statusEl.innerHTML = `Status: Subscribed to ${currentSub}<br><span style="color: orange;">(Fetching history...)</span> <div class="spinner"></div>`;
        setFaviconColor('#ffa500'); // Orange until data arrives
    };

    state.eventSource.addEventListener('server_error', (e) => {
        state.eventSource.close();
        statusEl.innerHTML = `Status: <span style="color: red;">${e.data}</span>`;
        setFaviconColor('#dc3545'); // Red for error
        if (btnSubmit) btnSubmit.textContent = 'Go';
    });

    state.eventSource.addEventListener('history_end', () => {
        historyLoading = false;
        statusEl.innerHTML = `Status: Subscribed to ${currentSub}<br><span style="color: green;">Receiving data (Spots: ${formatNumber(totalReceived)})</span>`;
        lastStatusUpdate = Date.now();
        scheduleRender();
    });

    state.eventSource.onmessage = (e) => {
        totalReceived++;
        
        const spot = JSON.parse(e.data);
        state.liveSpots.push(spot);
        setFaviconColor('#28a745'); // Green for active receiving

        // Throttle DOM text updates to max ~4 times a second
        const now = Date.now();
        if (now - lastStatusUpdate > 250) {
            if (historyLoading) {
                statusEl.innerHTML = `Status: Subscribed to ${currentSub}<br><span style="color: orange;">Fetching history (Spots: ${formatNumber(totalReceived)})</span> <div class="spinner"></div>`;
            } else {
                statusEl.innerHTML = `Status: Subscribed to ${currentSub}<br><span style="color: green;">Receiving data (Spots: ${formatNumber(totalReceived)})</span>`;
            }
            lastStatusUpdate = now;
        }

        if (!historyLoading) {
            scheduleRender();
        }
    };

    state.eventSource.onerror = (e) => {
        console.error("Stream error:", e);
        statusEl.innerHTML = `Status: <span style="color: red;">Connection error / Disconnected</span>`;
        setFaviconColor('#dc3545'); // Red for error
    };

    state.renderInterval = setInterval(() => {
        if (state.softPaused) return;

        if (state.liveSpots.length > 0) {
            const streamMaxAge = (parseInt(minutes, 10) || 15) * 60;
            const bandLabMaxAge = getBandLabLookbackMinutes() * 60;
            const maxAge = Math.max(streamMaxAge, bandLabMaxAge);
            state.liveSpots.forEach(s => s.ageSeconds += 5); 
            state.liveSpots = state.liveSpots.filter(s => s.ageSeconds <= maxAge);
            scheduleRender();
        }
    }, 5000);
});

document.addEventListener('visibilitychange', syncSoftPauseWithVisibility);
window.addEventListener('pageshow', syncSoftPauseWithVisibility);
window.addEventListener('focus', syncSoftPauseWithVisibility);

// Fallback pass in case autostart check happened before submit wiring was ready.
maybeAutoStartSavedTarget();
syncSoftPauseWithVisibility();

// Sync opmode controls visibility (initOpMode may have set checkbox from localStorage)
requestAnimationFrame(() => {
    const opGroup = document.getElementById('opmode-controls-group');
    if (opGroup) {
        opGroup.style.display = document.getElementById('opmode-allow-control')?.checked ? '' : 'none';
    }

    // Auto-hide sidebar on mobile at startup
    if (window.innerWidth <= 575) {
        const controls = document.getElementById('controls');
        const showBtn = document.getElementById('show-sidebar');
        if (controls) controls.style.marginLeft = '-' + controls.offsetWidth + 'px';
        if (showBtn) showBtn.style.display = 'block';
    }
});