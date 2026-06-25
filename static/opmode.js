import { setAzimuthAntennaOverlay } from './azimuth-runtime.js';
import { locatorToBounds } from './utils.js';

const opModeState = {
    enabled: false,
    runtimeViaLocalAgent: false,
    controlPermittedByUser: false,
    controlPermittedByServer: false,
    controlPermittedByAgent: null,
    transport: 'unknown',
    directBaseUrl: '',
    lastProxyStatus: null,
    pollingTimer: null,
    requestRender: () => {},
    station: null,
    antenna: null,
    statusText: 'disabled',
    transportFailureSticky: false,
    commandInFlight: false,
    pendingTargetBearingDeg: null,
    pendingTargetLabel: '',
    rigCapabilities: null
};

const BASE_DOCUMENT_TITLE = typeof document !== 'undefined'
    ? (document.title || 'horstreporter - HF Propagation Maps')
    : 'horstreporter - HF Propagation Maps';

function toNumber(v) {
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
}

function normalizeMode(raw) {
    const mode = String(raw || '').trim().toLowerCase();
    if (mode === 'backward' || mode === 'reverse' || mode === 'back') return 'backward';
    if (mode === 'bi' || mode === 'bi-directional' || mode === 'bidirectional') return 'bidirectional';
    return 'forward';
}

function getCurrentOriginBaseUrl() {
    return String(window?.location?.origin || '').trim().replace(/\/$/, '');
}

function opModeEndpoint(path) {
    const cleanPath = String(path || '').replace(/^\/+/, '');
    if (opModeState.transport === 'direct' && opModeState.directBaseUrl) {
        return `${opModeState.directBaseUrl}/v1/${cleanPath}`;
    }

    throw new Error('opmode direct endpoint not available');
}

async function probeLocalAgentStatus() {
    try {
        const payload = await fetchJson('/v1/status');
        return payload;
    } catch {
        return null;
    }
}

async function detectTransport() {
    const localAgentStatus = await probeLocalAgentStatus();
    if (localAgentStatus) {
        opModeState.runtimeViaLocalAgent = true;
        opModeState.transport = 'direct';
        opModeState.directBaseUrl = getCurrentOriginBaseUrl();
        return;
    }

    opModeState.runtimeViaLocalAgent = false;
    opModeState.transport = 'unknown';
    throw new Error('opmode inactive: app is running directly on backend (no local agent origin detected)');
}

function canSendControl() {
    if (!opModeState.enabled) return [false, 'opmode disabled'];
    if (!opModeState.controlPermittedByServer) return [false, 'control disabled on server'];
    if (opModeState.controlPermittedByAgent === false) return [false, 'control disabled by agent'];
    if (!opModeState.controlPermittedByUser) return [false, 'control not permitted in UI'];
    if (opModeState.commandInFlight) return [false, 'command already in progress'];
    return [true, ''];
}

function getOperatorDocumentTitle() {
    return BASE_DOCUMENT_TITLE.replace(/horstreporter/gi, 'horstoperator');
}

function updateBranding(isOperatorModeActive) {
    if (typeof document === 'undefined') return;
    document.title = isOperatorModeActive ? getOperatorDocumentTitle() : BASE_DOCUMENT_TITLE;

    const heading = document.getElementById('app-title');
    if (heading) {
        heading.textContent = isOperatorModeActive ? 'horstoperator' : 'horstreporter';
    }
}

function extractStation(payload) {
    const station = payload?.station || payload?.operator_station || payload?.site || {};
    let lat = toNumber(station.lat ?? station.latitude);
    let lng = toNumber(station.lng ?? station.lon ?? station.longitude);

    const locator = String(station.locator || station.grid || '').trim().toUpperCase();
    let locatorCenter = null;
    if (locator.length >= 4) {
        const bounds = locatorToBounds(locator);
        if (bounds) {
            locatorCenter = {
                lat: (bounds[0][0] + bounds[1][0]) / 2,
                lng: (bounds[0][1] + bounds[1][1]) / 2
            };
        }
    }

    if ((lat === null || lng === null) && locatorCenter) {
        lat = locatorCenter.lat;
        lng = locatorCenter.lng;
    }

    // If locator and coordinates disagree strongly, prefer locator center for map consistency.
    if (lat !== null && lng !== null && locatorCenter) {
        const mismatchKm = calculateGreatCircleDistanceKm(lat, lng, locatorCenter.lat, locatorCenter.lng);
        const mismatchThresholdKm = locator.length >= 6 ? 20 : 200;
        if (Number.isFinite(mismatchKm) && mismatchKm > mismatchThresholdKm) {
            lat = locatorCenter.lat;
            lng = locatorCenter.lng;
        }
    }

    if (lat === null || lng === null) return null;

    return {
        lat,
        lng,
        locator,
        name: String(station.name || station.callsign || station.call || '').trim()
    };
}

function extractAntenna(payload) {
    const src = payload?.antenna || payload?.rotator || payload || {};
    const azimuthDeg = toNumber(src.azimuth_deg ?? src.azimuth ?? src.heading_deg ?? src.heading);
    if (azimuthDeg === null) return null;

    const mode = normalizeMode(src.mode ?? src.pattern_mode ?? src.direction_mode);
    const configuredBeamwidth = toNumber(src.beamwidth_3db_deg ?? src.beamwidth_deg ?? src.beamwidth ?? src.wedge_deg) ?? 60;
    const beamwidth3dBDeg = mode === 'bidirectional' ? 80 : configuredBeamwidth;

    return {
        azimuthDeg,
        beamwidth3dBDeg,
        mode,
        availableModes: Array.isArray(src.available_modes) ? src.available_modes : []
    };
}

function setStatus(text, isError = false) {
    opModeState.statusText = text;
    const statusEl = document.getElementById('opmode-status');
    if (!statusEl) return;
    statusEl.textContent = text;
    statusEl.style.color = isError ? '#b02a37' : '';
}

function formatStationText(station) {
    if (!station) return 'n/a';

    const name = station.name || 'station';
    const locator = station.locator ? ` (${station.locator})` : '';
    const lat = Number.isFinite(station.lat) ? station.lat.toFixed(4) : '?';
    const lng = Number.isFinite(station.lng) ? station.lng.toFixed(4) : '?';
    return `${name}${locator} @ ${lat}, ${lng}`;
}

function updateStationUi(station) {
    const stationEl = document.getElementById('opmode-station');
    if (!stationEl) return;
    stationEl.textContent = formatStationText(station);
}

function normalizeAzimuthDeg(deg) {
    return ((deg % 360) + 360) % 360;
}

function angularDeltaDeg(a, b) {
    const azA = normalizeAzimuthDeg(Number(a) || 0);
    const azB = normalizeAzimuthDeg(Number(b) || 0);
    return Math.abs((((azA - azB + 540) % 360) - 180));
}

function calculateBearingDegrees(fromLat, fromLng, toLat, toLng) {
    const toRad = (v) => (v * Math.PI) / 180;
    const toDeg = (v) => (v * 180) / Math.PI;

    const lat1 = toRad(fromLat);
    const lat2 = toRad(toLat);
    const dLon = toRad(toLng - fromLng);
    const y = Math.sin(dLon) * Math.cos(lat2);
    const x = Math.cos(lat1) * Math.sin(lat2) - Math.sin(lat1) * Math.cos(lat2) * Math.cos(dLon);
    return normalizeAzimuthDeg(toDeg(Math.atan2(y, x)));
}

function calculateGreatCircleDistanceKm(fromLat, fromLng, toLat, toLng) {
    const toRad = (v) => (v * Math.PI) / 180;
    const dLat = toRad(toLat - fromLat);
    const dLng = toRad(toLng - fromLng);
    const lat1 = toRad(fromLat);
    const lat2 = toRad(toLat);

    const a = (Math.sin(dLat / 2) ** 2)
        + (Math.cos(lat1) * Math.cos(lat2) * (Math.sin(dLng / 2) ** 2));
    const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
    return 6371 * c;
}

function syncControlWidgets() {
    const allowControlEl = document.getElementById('opmode-allow-control');
    const modeSelect = document.getElementById('opmode-antenna-mode');
    const modeBtn = document.getElementById('opmode-antenna-mode-btn');

    const agentAllowsControl = opModeState.controlPermittedByAgent === null ? true : opModeState.controlPermittedByAgent;
    const canControl = opModeState.enabled
        && opModeState.controlPermittedByServer
        && agentAllowsControl
        && opModeState.controlPermittedByUser
        && !opModeState.commandInFlight;

    if (allowControlEl) {
        allowControlEl.disabled = !opModeState.enabled || !opModeState.controlPermittedByServer || !agentAllowsControl;
    }
    if (modeSelect) modeSelect.disabled = !canControl;
    if (modeBtn) modeBtn.disabled = !canControl;
}

function syncModeUiFromAntenna(antenna) {
    const modeSelect = document.getElementById('opmode-antenna-mode');
    const modeBtn = document.getElementById('opmode-antenna-mode-btn');
    if (!modeSelect || !modeBtn) return;

    const allModes = ['forward', 'backward', 'bidirectional'];
    const available = Array.isArray(antenna?.availableModes)
        ? antenna.availableModes.map(normalizeMode).filter((m, i, arr) => arr.indexOf(m) === i)
        : [];

    const effectiveAvailable = available.length > 0 ? available : allModes;
    for (const option of modeSelect.options) {
        option.disabled = !effectiveAvailable.includes(option.value);
    }

    const currentMode = normalizeMode(antenna?.mode || modeSelect.value || 'forward');
    if (effectiveAvailable.includes(currentMode)) {
        modeSelect.value = currentMode;
    } else {
        modeSelect.value = effectiveAvailable[0] || 'forward';
    }

    modeBtn.disabled = modeSelect.disabled || modeSelect.options[modeSelect.selectedIndex]?.disabled === true;
}

function syncAntennaOverlay() {
    if (!opModeState.enabled || !opModeState.station || !opModeState.antenna) {
        setAzimuthAntennaOverlay({ enabled: false });
        opModeState.requestRender();
        return;
    }

    setAzimuthAntennaOverlay({
        enabled: true,
        stationLat: opModeState.station.lat,
        stationLng: opModeState.station.lng,
        stationLocator: opModeState.station.locator,
        stationName: opModeState.station.name,
        azimuthDeg: opModeState.antenna.azimuthDeg,
        beamwidth3dBDeg: opModeState.antenna.beamwidth3dBDeg,
        mode: opModeState.antenna.mode,
        pendingTargetBearingDeg: opModeState.pendingTargetBearingDeg,
        pendingTargetLabel: opModeState.pendingTargetLabel
    });
    opModeState.requestRender();
}

function clearPendingTargetPreview() {
    opModeState.pendingTargetBearingDeg = null;
    opModeState.pendingTargetLabel = '';
}

function setPendingTargetPreview(bearingDeg, label) {
    opModeState.pendingTargetBearingDeg = Number.isFinite(Number(bearingDeg))
        ? normalizeAzimuthDeg(Number(bearingDeg))
        : null;
    opModeState.pendingTargetLabel = String(label || '').trim();
}

async function runControlAction(actionName, fn, options = {}) {
    const preserveTargetPreview = options.preserveTargetPreview === true;
    opModeState.commandInFlight = true;
    syncControlWidgets();

    try {
        setStatus(`${actionName}...`);
        await fn();
    } finally {
        opModeState.commandInFlight = false;
        if (!preserveTargetPreview) {
            clearPendingTargetPreview();
        }
        syncControlWidgets();
        syncAntennaOverlay();
    }
}

async function fetchJson(url, options = {}) {
    const response = await fetch(url, {
        cache: 'no-store',
        ...options
    });

    let payload = null;
    try {
        payload = await response.json();
    } catch {
        payload = null;
    }

    if (!response.ok) {
        const message = payload?.error || `HTTP ${response.status}`;
        throw new Error(message);
    }

    return payload;
}

async function refreshOpModeStatus() {
    if (opModeState.transport === 'unknown') {
        await detectTransport();
    }

    const status = await fetchJson(opModeEndpoint('status'));
    const directControl = typeof status?.control_permitted === 'boolean'
        ? status.control_permitted
        : (typeof status?.capabilities?.control === 'boolean' ? status.capabilities.control : true);

    opModeState.controlPermittedByServer = Boolean(directControl);
    opModeState.controlPermittedByAgent = Boolean(directControl);

    const rigCaps = status?.capabilities?.rig;
    opModeState.rigCapabilities = (rigCaps && typeof rigCaps === 'object')
        ? {
            tune: rigCaps.tune === true,
            preview: rigCaps.preview === true,
            split: rigCaps.split === true
        }
        : null;

    syncControlWidgets();

    setStatus('online (direct)');
}

async function refreshStationState() {
    const payload = await fetchJson(opModeEndpoint('station'));
    const station = extractStation(payload);
    if (station) {
        opModeState.station = station;
    }
    updateStationUi(opModeState.station);
}

async function refreshAntennaState() {
    const payload = await fetchJson(opModeEndpoint('antenna/state'));

    const station = extractStation(payload);
    const antenna = extractAntenna(payload);

    opModeState.station = station;
    opModeState.antenna = antenna;
    if (antenna && Number.isFinite(opModeState.pendingTargetBearingDeg)) {
        // Keep dotted target line alive across status polling until the heading has
        // reasonably converged to the pending target.
        const delta = angularDeltaDeg(antenna.azimuthDeg, opModeState.pendingTargetBearingDeg);
        if (delta <= 20) {
            clearPendingTargetPreview();
        }
    } else if (antenna) {
        clearPendingTargetPreview();
    }
    updateStationUi(opModeState.station);
    syncModeUiFromAntenna(antenna);

    const headingEl = document.getElementById('opmode-heading');
    if (headingEl) {
        if (antenna) {
            headingEl.textContent = `${Math.round(antenna.azimuthDeg)}° ${antenna.mode}`;
        } else {
            headingEl.textContent = 'n/a';
        }
    }

    syncAntennaOverlay();
}

async function pollTick() {
    if (!opModeState.enabled || document.hidden || opModeState.transportFailureSticky) return;

    try {
        await refreshOpModeStatus();
        await refreshStationState();
        await refreshAntennaState();
    } catch (err) {
        try {
            opModeState.transport = 'unknown';
            await refreshOpModeStatus();
            await refreshStationState();
            await refreshAntennaState();
        } catch (retryErr) {
            const message = retryErr?.message || err?.message || 'unavailable';
            setStatus(`error: ${message}`, true);
            if (String(message).includes('opmode inactive: app is running directly on backend')) {
                opModeState.transportFailureSticky = true;
                stopPolling();
            }
            opModeState.station = null;
            opModeState.antenna = null;
            syncAntennaOverlay();
        }
    }
}

function stopPolling() {
    if (opModeState.pollingTimer) {
        clearInterval(opModeState.pollingTimer);
        opModeState.pollingTimer = null;
    }
}

function startPolling() {
    stopPolling();
    opModeState.pollingTimer = setInterval(() => {
        void pollTick();
    }, 3000);
}

function syncEnabledStateFromUi() {
    opModeState.enabled = opModeState.runtimeViaLocalAgent === true;
    updateBranding(opModeState.enabled);

    if (!opModeState.enabled) {
        stopPolling();
        opModeState.transport = 'unknown';
        opModeState.transportFailureSticky = false;
        setStatus('inactive (direct backend mode)');
        opModeState.station = null;
        opModeState.antenna = null;
        clearPendingTargetPreview();
        syncAntennaOverlay();
        syncControlWidgets();
        return;
    }

    setStatus('connecting...');
    opModeState.transport = 'unknown';
    opModeState.transportFailureSticky = false;
    startPolling();
    void pollTick();
}

function syncControlPermissionFromUi() {
    const allowEl = document.getElementById('opmode-allow-control');
    opModeState.controlPermittedByUser = allowEl?.checked === true;
    localStorage.setItem('opModeControlPermitted', opModeState.controlPermittedByUser ? 'true' : 'false');
    syncControlWidgets();
}

export async function setAntennaMode(modeValue) {
    const [allowed, reason] = canSendControl();
    if (!allowed) throw new Error(reason);

    const mode = normalizeMode(modeValue);

    await runControlAction('setting mode', async () => {
        await fetchJson(opModeEndpoint('antenna/mode'), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                permit_control: true,
                mode,
                station_lat: opModeState.station?.lat ?? null,
                station_lng: opModeState.station?.lng ?? null
            })
        });
        await refreshAntennaState();
    });
}

export function isOpModeActive() {
    return opModeState.enabled === true;
}

// canControlRig reports whether the agent has a rig backend that can tune, AND
// the operator/server/UI permission gate is satisfied. The Chase Queue uses this
// to decide whether to expose tune affordances.
export function canControlRig() {
    if (opModeState.rigCapabilities?.tune !== true) return false;
    const [allowed] = canSendControl();
    return allowed;
}

export function getRigCapabilities() {
    return opModeState.rigCapabilities;
}

// rigTune QSYs the rig to freqHz (Hz) with the given mode. Mode mapping
// (CW/FT8/SSB → cw/data/usb/lsb) happens agent-side.
export async function rigTune(freqHz, mode) {
    const [allowed, reason] = canSendControl();
    if (!allowed) throw new Error(reason);
    if (opModeState.rigCapabilities?.tune !== true) throw new Error('rig tune not available');

    const hz = Math.round(Number(freqHz));
    if (!Number.isFinite(hz) || hz <= 0) throw new Error('invalid frequency');

    await runControlAction('tuning rig', async () => {
        await fetchJson(opModeEndpoint('rig/tune'), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                permit_control: true,
                freq_hz: hz,
                mode: String(mode || '').trim()
            })
        });
    });
}

// operate is the composite "Tune + Turn": QSY the rig AND rotate the beam to
// azimuthDeg. The agent returns per-leg status so a partial failure (tuned,
// rotor failed) is still reported.
export async function operate(freqHz, mode, azimuthDeg, label = '') {
    const [allowed, reason] = canSendControl();
    if (!allowed) throw new Error(reason);

    const hz = Math.round(Number(freqHz));
    if (!Number.isFinite(hz) || hz <= 0) throw new Error('invalid frequency');

    const az = toNumber(azimuthDeg);
    const body = {
        permit_control: true,
        freq_hz: hz,
        mode: String(mode || '').trim(),
        station_lat: opModeState.station?.lat ?? null,
        station_lng: opModeState.station?.lng ?? null
    };
    if (az !== null) {
        body.azimuth_deg = normalizeAzimuthDeg(az);
        setPendingTargetPreview(az, label);
        syncAntennaOverlay();
    }

    await runControlAction('tune + turn', async () => {
        await fetchJson(opModeEndpoint('operate'), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
        await refreshAntennaState();
    }, { preserveTargetPreview: az !== null });
}

export async function setBeamTargetFromMapClick({ lat, lng, label = '' } = {}) {
    const [allowed, reason] = canSendControl();
    if (!allowed) {
        setStatus(`beam target failed: ${reason}`, true);
        return false;
    }

    const targetLat = toNumber(lat);
    const targetLng = toNumber(lng);
    if (targetLat === null || targetLng === null) {
        setStatus('beam target failed: invalid map location', true);
        return false;
    }
    if (!opModeState.station) {
        setStatus('beam target failed: station location unavailable', true);
        return false;
    }

    const targetDistanceKm = calculateGreatCircleDistanceKm(
        opModeState.station.lat,
        opModeState.station.lng,
        targetLat,
        targetLng
    );
    if (!Number.isFinite(targetDistanceKm) || targetDistanceKm < 2) {
        clearPendingTargetPreview();
        syncAntennaOverlay();
        setStatus('beam target failed: selected point too close to station', true);
        return false;
    }

    const targetLabel = String(label || '').trim().toUpperCase();
    const azimuthDeg = calculateBearingDegrees(
        opModeState.station.lat,
        opModeState.station.lng,
        targetLat,
        targetLng
    );

    setPendingTargetPreview(azimuthDeg, targetLabel);
    syncAntennaOverlay();

    try {
        await runControlAction('setting beam target', async () => {
            await fetchJson(opModeEndpoint('antenna/rotate'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    permit_control: true,
                    azimuth_deg: azimuthDeg,
                    target_locator: targetLabel || null,
                    target_lat: targetLat,
                    target_lng: targetLng,
                    station_lat: opModeState.station?.lat ?? null,
                    station_lng: opModeState.station?.lng ?? null
                })
            });
            await refreshAntennaState();
        }, { preserveTargetPreview: true });

        const suffix = targetLabel ? ` (${targetLabel})` : '';
        setStatus(`beam target set to ${Math.round(azimuthDeg)}°${suffix}`);
        return true;
    } catch (err) {
        setStatus(`beam target failed: ${err?.message || 'unknown error'}`, true);
        return false;
    }
}

export function initOpMode({ requestRender } = {}) {
    opModeState.requestRender = typeof requestRender === 'function' ? requestRender : () => {};

    const allowControlEl = document.getElementById('opmode-allow-control');
    const modeSelect = document.getElementById('opmode-antenna-mode');
    const modeBtn = document.getElementById('opmode-antenna-mode-btn');

    if (allowControlEl) {
        allowControlEl.checked = localStorage.getItem('opModeControlPermitted') === 'true';
        allowControlEl.addEventListener('change', syncControlPermissionFromUi);
    }

    if (modeBtn) {
        modeBtn.addEventListener('click', async () => {
            try {
                const mode = normalizeMode(modeSelect?.value || 'forward');
                await setAntennaMode(mode);
                setStatus(`mode set to ${mode}`);
            } catch (err) {
                setStatus(`mode change failed: ${err?.message || 'unknown error'}`, true);
            }
        });
    }

    if (modeSelect) {
        modeSelect.addEventListener('keydown', async (e) => {
            if (e.key !== 'Enter') return;
            e.preventDefault();
            try {
                const mode = normalizeMode(modeSelect.value || 'forward');
                await setAntennaMode(mode);
                setStatus(`mode set to ${mode}`);
            } catch (err) {
                setStatus(`mode change failed: ${err?.message || 'unknown error'}`, true);
            }
        });
    }

    document.addEventListener('visibilitychange', () => {
        if (document.hidden) return;
        if (opModeState.enabled) {
            void pollTick();
        }
    });

    updateBranding(false);
    syncControlPermissionFromUi();
    void (async () => {
        try {
            await detectTransport();
            syncEnabledStateFromUi();
        } catch {
            opModeState.runtimeViaLocalAgent = false;
            syncEnabledStateFromUi();
        }
    })();
}
