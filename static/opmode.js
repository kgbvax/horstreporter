import { setAzimuthAntennaOverlay } from './azimuth-runtime.js';
import { locatorToBounds, freqHzToBand } from './utils.js';

const opModeState = {
    enabled: false,
    runtimeViaLocalAgent: false,
    controlPermittedByUser: true,
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
    rigCapabilities: null,
    lookupCapabilities: null,
    ultrabeamCapabilities: null,
    reverseSince: null,
    // Last frequency/mode the operator commanded from the browser (tune / operate).
    // Fallback for the status line when no live rig readback is available.
    lastTuned: null, // { freqHz, mode }
    // Live rig state from WaveLogGate's WebSocket broadcast (via the agent's
    // /v1/antenna/state `rig` block): actual frequency/mode + split RX.
    liveRig: null // { freqHz, mode, freqRxHz, modeRx, split, online }
};

const BASE_DOCUMENT_TITLE = typeof document !== 'undefined'
    ? (document.title || 'horstreporter - HF Propagation Maps')
    : 'horstreporter - HF Propagation Maps';

function toNumber(v) {
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
}

export function normalizeMode(raw) {
    const mode = String(raw || '').trim().toLowerCase();
    // 'reverse' is first-class for the UltraBeam (forward | reverse | bidirectional).
    // Legacy 'backward'/'back' map onto it so older payloads still resolve.
    if (mode === 'reverse' || mode === '180' || mode === '180°' || mode === 'back' || mode === 'backward') return 'reverse';
    if (mode === 'bi' || mode === 'bidir' || mode === 'bi-dir' || mode === 'bi-directional' || mode === 'bidirectional') return 'bidirectional';
    return 'forward';
}

// overlayModeFor maps the canonical beam direction onto the azimuth runtime's
// internal vocabulary (which understands forward | backward | bidirectional).
// The runtime draws the 180° lobe on 'backward', so 'reverse' translates to it.
export function overlayModeFor(mode) {
    return mode === 'reverse' ? 'backward' : mode;
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
    // Agent/station/antenna readouts mean nothing without the local agent.
    const statusRows = document.getElementById('opmode-status-rows');
    if (statusRows) statusRows.hidden = !isOperatorModeActive;
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
    const hasMode = src.mode != null || src.pattern_mode != null || src.direction_mode != null;

    // Return an antenna object whenever there is a beam direction OR an azimuth.
    // A missing azimuth (rotator outage) must NOT drop the object — the beam
    // buttons and the reverse alarm keep working off the UltraBeam mode alone.
    if (azimuthDeg === null && !hasMode) return null;

    const mode = normalizeMode(src.mode ?? src.pattern_mode ?? src.direction_mode);
    const configuredBeamwidth = toNumber(src.beamwidth_3db_deg ?? src.beamwidth_deg ?? src.beamwidth ?? src.wedge_deg) ?? 60;
    const beamwidth3dBDeg = mode === 'bidirectional' ? 80 : configuredBeamwidth;

    return {
        azimuthDeg,
        beamwidth3dBDeg,
        mode,
        // Default true when the flag is absent (legacy PSTrotator payloads) so
        // existing overlay behavior is unchanged when UltraBeam is disabled.
        beamOnline: src.beam_online !== false,
        azimuthOnline: src.azimuth_online !== false,
        availableModes: Array.isArray(src.available_modes) ? src.available_modes : []
    };
}

// extractRig reads the live rig block (WaveLogGate WebSocket readback) from an
// /v1/antenna/state payload. Returns null for tune-only backends that omit it.
function extractRig(payload) {
    const src = payload?.rig;
    if (!src || typeof src !== 'object') return null;
    const freqHz = toNumber(src.freq_hz);
    if (freqHz === null || freqHz <= 0) return null;
    const freqRxHz = toNumber(src.freq_rx_hz);
    return {
        freqHz,
        mode: String(src.mode || '').trim().toUpperCase(),
        split: src.split === true,
        online: src.online !== false,
        freqRxHz: freqRxHz !== null && freqRxHz > 0 ? freqRxHz : null,
        modeRx: String(src.mode_rx || '').trim().toUpperCase()
    };
}

function setStatus(text, isError = false) {
    opModeState.statusText = text;
    const statusEl = document.getElementById('opmode-status');
    if (!statusEl) return;
    statusEl.textContent = text;
    statusEl.classList.toggle('status-danger', Boolean(isError));
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

export function syncControlWidgets() {
    const buttons = document.querySelectorAll('.opmode-beam-btn');
    const unavailableEl = document.getElementById('opmode-beam-unavailable');
    const opGroup = document.getElementById('opmode-controls-group');

    const agentAllowsControl = opModeState.controlPermittedByAgent === null ? true : opModeState.controlPermittedByAgent;
    const ub = opModeState.ultrabeamCapabilities;
    const ubConfigured = ub != null;
    const ubOnline = ub?.online === true;

    const permitted = opModeState.enabled
        && opModeState.controlPermittedByServer
        && agentAllowsControl
        && opModeState.controlPermittedByUser
        && !opModeState.commandInFlight;
    const canControl = permitted && ubConfigured && ubOnline;

    buttons.forEach((btn) => { btn.disabled = !canControl; });

    if (opGroup) {
        const showGroup = opModeState.enabled && opModeState.ultrabeamCapabilities != null;
        opGroup.style.display = showGroup ? 'flex' : 'none';
    }

    // Cause-specific unavailable copy so the operator knows the remedy rather
    // than facing three identically greyed-out buttons. Permission is named
    // first so the "not permitted + not configured" combination still gets a
    // message instead of an ambiguous blank.
    if (unavailableEl) {
        let msg = '';
        if (opModeState.enabled) {
            if (!opModeState.controlPermittedByUser) msg = 'Antenna control not permitted';
            else if (!ubConfigured) msg = 'UltraBeam not configured';
            else if (!ubOnline) msg = 'UltraBeam offline';
        }
        unavailableEl.textContent = msg;
        unavailableEl.style.display = msg ? '' : 'none';
    }
}

// canSendBeamControl extends the base control gate with the UltraBeam-online
// requirement, used before publishing a beam-direction command.
function canSendBeamControl() {
    const [allowed, reason] = canSendControl();
    if (!allowed) return [false, reason];
    if (opModeState.ultrabeamCapabilities == null) return [false, 'ultrabeam not configured'];
    if (opModeState.ultrabeamCapabilities.online !== true) return [false, 'ultrabeam offline'];
    return [true, ''];
}

export function syncBeamButtonsFromAntenna(antenna) {
    const buttons = document.querySelectorAll('.opmode-beam-btn');
    if (!buttons.length) return;
    // A null antenna (outage) clears all active highlights rather than defaulting
    // to forward — there is no known direction to indicate.
    const current = antenna ? normalizeMode(antenna.mode || 'forward') : null;
    buttons.forEach((btn) => {
        const isActive = current != null && btn.dataset.mode === current;
        btn.classList.toggle('active', isActive);
        btn.setAttribute('aria-pressed', isActive ? 'true' : 'false');
    });
}

// REVERSE_ALARM_RAMP_MS is the window over which the 180° reverse alarm escalates
// from minimum to maximum prominence.
export const REVERSE_ALARM_RAMP_MS = 90000;

// reverseAlarmIntensity maps elapsed time in reverse to a 0..1 intensity, clamped.
export function reverseAlarmIntensity(elapsedMs) {
    const n = Number(elapsedMs);
    if (!Number.isFinite(n) || n <= 0) return 0;
    return Math.min(1, n / REVERSE_ALARM_RAMP_MS);
}

// updateReverseAlarm drives the escalating 180° alarm off observed status. When
// reverse is the live direction, the 180° button pulses red with intensity
// ramping over REVERSE_ALARM_RAMP_MS, and a non-color REVERSE text cue (an
// aria-live region) is shown so the alarm reads without color or motion. Any
// non-reverse state (or an offline UltraBeam) clears it and resets the ramp.
export function updateReverseAlarm(antenna) {
    const btn = document.getElementById('opmode-beam-180');
    const badge = document.getElementById('opmode-reverse-badge');
    const isReverse = normalizeMode(antenna?.mode) === 'reverse' && antenna?.beamOnline !== false;

    if (!isReverse) {
        opModeState.reverseSince = null;
        if (btn) {
            btn.classList.remove('opmode-reverse-alarm');
            btn.style.removeProperty('--reverse-alarm-intensity');
        }
        if (badge) {
            // Clear the text (not just hide) so the live region re-announces on
            // the next entry into reverse.
            badge.textContent = '';
            badge.style.display = 'none';
        }
        return;
    }

    if (opModeState.reverseSince == null) {
        opModeState.reverseSince = Date.now();
    }
    const intensity = reverseAlarmIntensity(Date.now() - opModeState.reverseSince);
    if (btn) {
        btn.classList.add('opmode-reverse-alarm');
        btn.style.setProperty('--reverse-alarm-intensity', intensity.toFixed(3));
    }
    if (badge) {
        // Set the content at the transition so the aria-live region fires an
        // assertive announcement (screen readers may not announce a region whose
        // text was already present and merely un-hidden).
        if (badge.textContent === '') {
            badge.textContent = '\u26A0 REVERSE (180\u00B0)';
        }
        badge.style.display = '';
    }
}

export function syncAntennaOverlay() {
    const a = opModeState.antenna;
    // Suppress the directional overlay when there is no station, no antenna,
    // no usable azimuth, or the UltraBeam is offline — never draw the 'forward'
    // fallback as if it were authoritative (R7).
    const canDraw = opModeState.enabled
        && opModeState.station
        && a
        && Number.isFinite(a.azimuthDeg)
        && a.beamOnline !== false;

    if (!canDraw) {
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
        azimuthDeg: a.azimuthDeg,
        beamwidth3dBDeg: a.beamwidth3dBDeg,
        mode: overlayModeFor(a.mode),
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
    } catch (err) {
        // A failed command must not leave a phantom pending-target line on the
        // map. The preview is only meaningful while the beam is actually
        // turning toward it; on failure it would persist until a poll happened
        // to see the antenna within 20° of a target that was never commanded.
        clearPendingTargetPreview();
        throw err;
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

    const lookupCaps = status?.capabilities?.lookup;
    opModeState.lookupCapabilities = (lookupCaps && typeof lookupCaps === 'object')
        ? { wavelog: lookupCaps.wavelog === true, awards: lookupCaps.awards === true }
        : null;

    const ubCaps = status?.capabilities?.ultrabeam;
    opModeState.ultrabeamCapabilities = (ubCaps && typeof ubCaps === 'object')
        ? { control: ubCaps.control === true, online: ubCaps.online === true }
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
    opModeState.liveRig = extractRig(payload);
    if (antenna && Number.isFinite(antenna.azimuthDeg) && Number.isFinite(opModeState.pendingTargetBearingDeg)) {
        // Keep dotted target line alive across status polling until the heading has
        // reasonably converged to the pending target.
        const delta = angularDeltaDeg(antenna.azimuthDeg, opModeState.pendingTargetBearingDeg);
        if (delta <= 20) {
            clearPendingTargetPreview();
        }
    } else if (antenna && Number.isFinite(antenna.azimuthDeg)) {
        clearPendingTargetPreview();
    }
    updateStationUi(opModeState.station);
    syncBeamButtonsFromAntenna(antenna);
    updateReverseAlarm(antenna);

    const headingEl = document.getElementById('opmode-heading');
    if (headingEl) {
        if (antenna) {
            const az = Number.isFinite(antenna.azimuthDeg) ? `${Math.round(antenna.azimuthDeg)}°` : '—';
            headingEl.textContent = `${az} ${beamModeLabel(antenna.mode)}`;
        } else {
            headingEl.textContent = 'n/a';
        }
    }

    syncAntennaOverlay();
    updateOpModeStatusLine();
}

// beamModeLabel renders a canonical beam direction with the operator-facing
// label used on the buttons (reverse shows as "180°").
export function beamModeLabel(mode) {
    if (mode === 'reverse') return '180°';
    if (mode === 'bidirectional') return 'bi-dir';
    return 'forward';
}

// formatQrg renders a frequency in Hz as MHz with 3 decimals (kHz precision),
// e.g. 14074000 → "14.074".
function formatQrg(freqHz) {
    const hz = Number(freqHz);
    if (!Number.isFinite(hz) || hz <= 0) return '';
    return (hz / 1_000_000).toFixed(3);
}

// updateOpModeStatusLine renders the on-map "Band | Mode | QRG | Antenna" line.
// Shown only in opmode. Band/Mode/QRG prefer WaveLogGate's live WebSocket
// readback (incl. split RX), falling back to the last browser-issued tune when no
// live rig source is available. Antenna direction is always live.
export function updateOpModeStatusLine() {
    const line = document.getElementById('opmode-status-line');
    if (!line) return;

    const hide = () => { line.style.display = 'none'; };

    if (!opModeState.enabled) {
        hide();
        return;
    }

    // Prefer live rig readback; fall back to the last commanded tune.
    const live = opModeState.liveRig;
    const useLive = live && live.online && live.freqHz > 0;
    const src = useLive
        ? { freqHz: live.freqHz, mode: live.mode }
        : (opModeState.lastTuned || null);

    // No frequency source at all (no live rig, no last tune) → hide the line
    // entirely rather than showing an all-empty row.
    if (!src) {
        hide();
        return;
    }

    const band = freqHzToBand(src.freqHz);
    const mode = src.mode ? String(src.mode).toUpperCase() : '';

    // QRG: TX frequency, plus the split RX frequency when the rig reports split.
    let qrg = formatQrg(src.freqHz);
    if (qrg) {
        qrg = `${qrg} MHz`;
        if (useLive && live.split && live.freqRxHz) {
            qrg = `TX ${formatQrg(live.freqHz)} / RX ${formatQrg(live.freqRxHz)} MHz`;
        }
    }
    // Mode: append RX mode when split and it differs from TX.
    let modeTxt = mode;
    if (useLive && live.split && live.modeRx && live.modeRx !== mode) {
        modeTxt = `${mode} / ${live.modeRx}`;
    }
    if (useLive && live.split) {
        modeTxt = modeTxt ? `${modeTxt} (split)` : 'split';
    }

    const antenna = opModeState.antenna;
    let antennaTxt = '—';
    if (antenna && Number.isFinite(antenna.azimuthDeg) && antenna.azimuthOnline !== false) {
        antennaTxt = `${Math.round(antenna.azimuthDeg)}° ${beamModeLabel(antenna.mode)}`;
    } else if (antenna) {
        antennaTxt = beamModeLabel(antenna.mode);
    }

    const setVal = (id, val) => {
        const el = document.getElementById(id);
        if (el) el.textContent = val && val.length ? val : '—';
    };
    setVal('opmode-sl-band', band);
    setVal('opmode-sl-mode', modeTxt);
    setVal('opmode-sl-qrg', qrg);
    setVal('opmode-sl-antenna', antennaTxt);

    line.style.display = 'flex';
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
            opModeState.liveRig = null;
            // A total outage means there is no UltraBeam truth either — the beam
            // UI must go neutral, not leave the reverse alarm latched or a stale
            // button active/enabled (which would let a click hit a dead agent).
            opModeState.ultrabeamCapabilities = null;
            updateReverseAlarm(null);
            syncBeamButtonsFromAntenna(null);
            syncControlWidgets();
            syncAntennaOverlay();
            updateOpModeStatusLine();
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
        opModeState.liveRig = null;
        clearPendingTargetPreview();
        updateReverseAlarm(null);
        syncAntennaOverlay();
        syncControlWidgets();
        updateOpModeStatusLine();
        return;
    }

    setStatus('connecting...');
    opModeState.transport = 'unknown';
    opModeState.transportFailureSticky = false;
    startPolling();
    void pollTick();
}

export async function setAntennaMode(modeValue) {
    const [allowed, reason] = canSendBeamControl();
    if (!allowed) throw new Error(reason);

    const mode = normalizeMode(modeValue);

    await runControlAction('setting beam', async () => {
        await fetchJson(opModeEndpoint('antenna/beam'), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                permit_control: true,
                mode
            })
        });
        await refreshAntennaState();
    });
}

export function isOpModeActive() {
    return opModeState.enabled === true;
}

// __setOpModeStateForTest patches internal opmode state. Test-only seam so the
// gating/command paths can be exercised in unit tests without a live agent.
export function __setOpModeStateForTest(patch) {
    Object.assign(opModeState, patch);
}

// getOpModeStation returns the operator station {lat,lng} when operator mode is
// active and a station is known, else null. Used as the origin for the Chase
// Queue path line so it matches the antenna-beam origin.
export function getOpModeStation() {
    if (!opModeState.enabled || !opModeState.station) return null;
    const { lat, lng } = opModeState.station;
    if (!Number.isFinite(lat) || !Number.isFinite(lng)) return null;
    return { lat, lng };
}

// canControlRig reports whether the agent has a rig backend that can tune, AND
// the operator/server/UI permission gate is satisfied. The Chase Queue uses this
// to decide whether to expose tune affordances.
export function canControlRig() {
    if (opModeState.rigCapabilities?.tune !== true) return false;
    const [allowed] = canSendControl();
    return allowed;
}

// canLookup reports whether the agent has a configured Wavelog backend. Lookup
// is a read (no hardware), so it is gated only on the agent + capability, not
// on the control-permission checkbox.
export function canLookup() {
    return opModeState.enabled === true
        && opModeState.transport === 'direct'
        && opModeState.lookupCapabilities?.wavelog === true;
}

// enrichSpots batches [{id, call, band, mode}] to the agent, which answers
// needed[] + worked_before per spot from Wavelog (private_lookup). Returns the
// raw envelope { ok, degraded, results } or null when lookup is unavailable.
export async function enrichSpots(spots) {
    if (!canLookup()) return null;
    const list = (spots || [])
        .filter((s) => s && s.call)
        .map((s) => ({ id: String(s.id), call: s.call, band: s.band || '', mode: s.mode || '', pota_ref: s.pota_ref || '' }));
    if (!list.length) return null;

    return fetchJson(opModeEndpoint('operate/enrich'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ permit_lookup: true, spots: list })
    });
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
    opModeState.lastTuned = { freqHz: hz, mode: String(mode || '').trim() };
    updateOpModeStatusLine();
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
    opModeState.lastTuned = { freqHz: hz, mode: String(mode || '').trim() };
    updateOpModeStatusLine();
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

    const beamButtons = document.querySelectorAll('.opmode-beam-btn');

    beamButtons.forEach((btn) => {
        btn.addEventListener('click', async () => {
            const mode = normalizeMode(btn.dataset.mode || 'forward');
            try {
                await setAntennaMode(mode);
                setStatus(`beam set to ${beamModeLabel(mode)}`);
            } catch (err) {
                setStatus(`beam change failed: ${err?.message || 'unknown error'}`, true);
            }
        });
    });

    document.addEventListener('visibilitychange', () => {
        if (document.hidden) return;
        if (opModeState.enabled) {
            void pollTick();
        }
    });

    updateBranding(false);
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
