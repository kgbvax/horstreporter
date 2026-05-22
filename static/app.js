import { state } from './state.js';
import { loadConfig } from './config.js';
import { initMap, setTheme, map, syncMercatorCountryLayer, syncMercatorGraylineLayer, syncMercatorDxccLabelLayer } from './map.js';
import { initAzimuthCanvas, isAzimuthEnabled, loadAzimuthWorldGeoJson, renderAzimuthScene, setAzimuthCenter, setAzimuthEnabled, setAzimuthTheme, setAzimuthZoom, clampAzimuthZoom, setAzimuthHorizonKm, clampAzimuthHorizonKm, setAzimuthNs6tIndicatorEnabled, setAzimuthDxccLabelDensity, setAzimuthDxccLabelsEnabled } from './azimuth-runtime.js';
import { initUI, attachUITooltipEvents } from './ui.js';
import { updateMapVisualization } from './renderers.js';
import { initDxConditionsUI, setDxConditionsVisible, resetDxConditions, startDxPolling, stopDxPolling } from './dx-conditions.js';
import { latLngToLocator, locatorToBounds, setFaviconColor, getMinSnrMode, getEnabledBands, getSelectedBand, formatNumber, bandColors } from './utils.js';

// --- Azimuth Zoom State ---
let azimuthZoom = Number(localStorage.getItem('azimuthZoom') || 1.5);
function updateAzimuthZoom(newZoom) {
    azimuthZoom = clampAzimuthZoom(newZoom);
    localStorage.setItem('azimuthZoom', azimuthZoom);
    setAzimuthZoom(azimuthZoom);
    if (isAzimuthEnabled()) scheduleRender();
}

let azimuthHorizonKm = Number(localStorage.getItem('azimuthHorizonKm') || 16000);
function updateAzimuthHorizonKm(newHorizonKm) {
    azimuthHorizonKm = clampAzimuthHorizonKm(newHorizonKm);
    localStorage.setItem('azimuthHorizonKm', azimuthHorizonKm);
    setAzimuthHorizonKm(azimuthHorizonKm);
    const horizonInput = document.getElementById('azimuth-horizon-km');
    if (horizonInput && Number(horizonInput.value) !== azimuthHorizonKm) {
        horizonInput.value = String(azimuthHorizonKm);
    }
    if (isAzimuthEnabled()) scheduleRender();
}

let azimuthNs6tIndicator = localStorage.getItem('azimuthNs6tIndicator') !== 'false';
function updateAzimuthNs6tIndicator(enabled) {
    azimuthNs6tIndicator = Boolean(enabled);
    localStorage.setItem('azimuthNs6tIndicator', azimuthNs6tIndicator ? 'true' : 'false');
    setAzimuthNs6tIndicatorEnabled(azimuthNs6tIndicator);
    const indicatorInput = document.getElementById('azimuth-ns6t-indicator');
    if (indicatorInput) indicatorInput.checked = azimuthNs6tIndicator;
    if (isAzimuthEnabled()) scheduleRender();
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
    document.getElementById('dxcc-label-density-val').textContent = dxccLabelDensity.toFixed(1);
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

function getCurrentDxRequestParams() {
    const target = document.getElementById('target')?.value?.trim()?.toUpperCase();
    const minutesRaw = parseInt(document.getElementById('minutes')?.value || '15', 10);
    const minutes = Number.isFinite(minutesRaw) ? minutesRaw : 15;
    const surroundings = document.getElementById('surroundings')?.checked === true;
    return { target, minutes, surroundings };
}

async function updateDk3jfMode(enabled) {
    dk3jfModeEnabled = Boolean(enabled);
    localStorage.setItem('dk3jfModeEnabled', dk3jfModeEnabled ? 'true' : 'false');
    localStorage.removeItem('dk3jxModeEnabled');

    const dk3jfToggle = document.getElementById('dk3jf-mode');
    if (dk3jfToggle) dk3jfToggle.checked = dk3jfModeEnabled;

    const band2mWrapper = document.getElementById('band-wrapper-2m');
    setElementVisibility(band2mWrapper, dk3jfModeEnabled);

    const projectionRow = document.getElementById('projection-switch-row');
    setElementVisibility(projectionRow, dk3jfModeEnabled);

    const azimuthOptionsGroup = document.getElementById('azimuth-options-group');
    setElementVisibility(azimuthOptionsGroup, dk3jfModeEnabled);

    setDxConditionsVisible(dk3jfModeEnabled);

    if (!dk3jfModeEnabled) {
        stopDxPolling();
        resetDxConditions();

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

        const mercatorRadio = document.querySelector('input[name="projection-select"][value="mercator"]');
        localStorage.setItem('mapProjection', 'mercator');
        if (mercatorRadio && !mercatorRadio.checked) {
            mercatorRadio.checked = true;
            await applyProjectionMode('mercator');
        }
    } else if (state.eventSource) {
        startDxPolling(getCurrentDxRequestParams);
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

function currentProjection() {
    return document.querySelector('input[name="projection-select"]:checked')?.value || 'mercator';
}

function maybeAutoStartSavedTarget() {
    if (autoStartTriggered || !appReadyForAutoStart || !map) {
        return;
    }

    const savedTarget = localStorage.getItem('target');
    const targetInput = document.getElementById('target');
    const targetValue = targetInput?.value?.trim();
    if (!savedTarget || !targetValue) {
        return;
    }

    autoStartTriggered = true;
    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit) btnSubmit.textContent = 'Go';

    document.getElementById('fetch-form')?.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
}

async function syncMercatorOverlays(force = false) {
    await syncMercatorCountryLayer({ force });
    await syncMercatorGraylineLayer({ force });
    await syncMercatorDxccLabelLayer({ force });
}

async function applyProjectionMode(projection) {
    if (projection === 'azimuthal') {
        await loadAzimuthWorldGeoJson();
        setAzimuthEnabled(true);
        setAzimuthTheme(document.body.getAttribute('data-theme') || 'light');
        setAzimuthZoom(azimuthZoom);
        document.getElementById('azimuth-zoom-controls').style.display = 'block';
        scheduleRender();
        return;
    }

    setAzimuthEnabled(false);
    document.getElementById('azimuth-zoom-controls').style.display = 'none';
    if (map) map.invalidateSize();
    await syncMercatorOverlays(true);
    scheduleRender();
}

export function attachMapEvents() {
    map.on('click', function(e) {
        if (currentProjection() === 'azimuthal') {
            return;
        }

        const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, 4);
        const targetInput = document.getElementById('target');
        if (targetInput) {
            targetInput.value = loc;
            const btnSubmit = document.getElementById('btn-submit');
            if (btnSubmit) btnSubmit.textContent = 'Go'; // Force a clean restart
            document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        }
    });
}

// --- Init UI ---
initUI();
initDxConditionsUI();

(async () => {
    initMap(initialCenter, initialZoom);
    initAzimuthCanvas();
    setTheme(savedTheme);
    setAzimuthTheme(savedTheme);
    setAzimuthCenter(initialCenter);
    updateAzimuthHorizonKm(azimuthHorizonKm);
    updateAzimuthNs6tIndicator(azimuthNs6tIndicator);
    updateDxccLabelDensity(dxccLabelDensity);
    updateDxccLabelsEnabled(dxccLabelsEnabled);
    resetDxConditions();
    await updateDk3jfMode(dk3jfModeEnabled);

    const initialProjection = dk3jfModeEnabled ? savedProjection : 'mercator';
    const projRadio = document.querySelector(`input[name="projection-select"][value="${initialProjection}"]`);
    if (projRadio) projRadio.checked = true;
    await applyProjectionMode(initialProjection);

    const showGrayline = localStorage.getItem('showGrayline') === 'true';
    const colorCountries = localStorage.getItem('colorCountries') === 'true';

    const graylineToggle = document.getElementById('show-grayline');
    if (graylineToggle) graylineToggle.checked = showGrayline;

    const countriesToggle = document.getElementById('color-countries');
    if (countriesToggle) countriesToggle.checked = colorCountries;

    const dxccToggle = document.getElementById('show-dxcc-labels');
    if (dxccToggle) dxccToggle.checked = dxccLabelsEnabled;

    await syncMercatorOverlays(true);

    attachMapEvents();
    attachUITooltipEvents();
    
    // Force an initial render to sync visual band states (colors/opacity) loaded from localStorage
    updateMapVisualization(state.liveSpots, parseInt(document.getElementById('minutes')?.value || 15));
    updateCurrentBandDisplay();

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
            const minutes = document.getElementById('minutes').value || 15;
            if (isAzimuthEnabled()) {
                renderAzimuthScene({ spots: state.liveSpots });
            } else {
                updateMapVisualization(state.liveSpots, parseInt(minutes));
            }
            lastRenderTime = Date.now();
            state.renderPending = false;
        });
    }, delay);
}

document.getElementById('theme-toggle').addEventListener('click', () => {
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
    controls.style.marginLeft = '-320px';
    document.getElementById('show-sidebar').style.display = 'block';
});

document.getElementById('show-sidebar')?.addEventListener('click', () => {
    const controls = document.getElementById('controls');
    controls.style.marginLeft = '0px';
    document.getElementById('show-sidebar').style.display = 'none';
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

// Keyboard +/- for azimuth zoom
window.addEventListener('keydown', (e) => {
    if (!isAzimuthEnabled()) return;
    if (e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA')) return;
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

document.getElementById('ssb-min-db')?.addEventListener('change', () => {
    localStorage.setItem('ssbMinDb', document.getElementById('ssb-min-db').value);
    scheduleRender();
});

document.getElementById('cw-min-db')?.addEventListener('change', () => {
    localStorage.setItem('cwMinDb', document.getElementById('cw-min-db').value);
    scheduleRender();
});

document.getElementById('cycle-time')?.addEventListener('change', (e) => {
    localStorage.setItem('cycleTime', e.target.value);
    if (state.cycleInterval) {
        // Restart cycle to pick up the new time
        document.getElementById('btn-cycle').click();
        document.getElementById('btn-cycle').click();
    }
});

document.getElementById('azimuth-horizon-km')?.addEventListener('change', (e) => {
    updateAzimuthHorizonKm(e.target.value);
});

document.getElementById('azimuth-ns6t-indicator')?.addEventListener('change', (e) => {
    updateAzimuthNs6tIndicator(e.target.checked);
});

document.getElementById('dxcc-label-density')?.addEventListener('input', (e) => {
    document.getElementById('dxcc-label-density-val').textContent = e.target.value;
});

document.getElementById('dxcc-label-density')?.addEventListener('change', (e) => {
    updateDxccLabelDensity(e.target.value);
});

document.getElementById('dk3jf-mode')?.addEventListener('change', async (e) => {
    await updateDk3jfMode(e.target.checked);
});

document.getElementById('cluster-distance')?.addEventListener('input', (e) => {
    document.getElementById('cluster-dist-val').textContent = e.target.value;
    localStorage.setItem('clusterDistance', e.target.value);
    scheduleRender();
});

document.getElementById('band-container').addEventListener('change', (e) => {
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
    scheduleRender();
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
        scheduleRender();
    });
});

document.querySelectorAll('.band-preset').forEach(btn => {
    btn.addEventListener('click', () => {
        const preset = btn.getAttribute('data-preset');
        const highBands = ['20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];
        const lowBands = ['160m', '80m', '60m', '40m', '30m'];
        const ssbBands = ['160m', '80m', '40m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];
        
        document.querySelectorAll('.band-enable').forEach(cb => {
            let enable = false;
            if (preset === 'all') enable = true;
            else if (preset === 'high') enable = highBands.includes(cb.value);
            else if (preset === 'low') enable = lowBands.includes(cb.value);
            else if (preset === 'ssb') enable = ssbBands.includes(cb.value);
            
            if (cb.checked !== enable) {
                cb.checked = enable;
                cb.dispatchEvent(new Event('change', { bubbles: true })); // Triggers map re-render and localStorage save
            }
        });
    });
});

document.getElementById('auto-zoom')?.addEventListener('change', (e) => {
    localStorage.setItem('autoZoom', e.target.checked);
    if (e.target.checked) scheduleRender();
});

document.getElementById('surroundings')?.addEventListener('change', (e) => {
    localStorage.setItem('surroundings', e.target.checked);
    const btnSubmit = document.getElementById('btn-submit');
    if (btnSubmit && btnSubmit.textContent === 'Stop') {
        btnSubmit.textContent = 'Go';
        document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
});

document.getElementById('show-grayline')?.addEventListener('change', (e) => {
    localStorage.setItem('showGrayline', e.target.checked ? 'true' : 'false');
    if (!isAzimuthEnabled()) {
        void syncMercatorGraylineLayer({ force: true });
    }
});

document.getElementById('color-countries')?.addEventListener('change', (e) => {
    localStorage.setItem('colorCountries', e.target.checked ? 'true' : 'false');
    if (!isAzimuthEnabled()) {
        void syncMercatorCountryLayer({ force: true });
    }
});

document.getElementById('show-dxcc-labels')?.addEventListener('change', (e) => {
    updateDxccLabelsEnabled(e.target.checked);
});

document.getElementById('btn-geo').addEventListener('click', () => {
    if (!navigator.geolocation) {
        alert('Geolocation is not supported by your browser.');
        return;
    }

    const btn = document.getElementById('btn-geo');
    const originalText = btn.innerHTML;
    btn.innerHTML = '<i class="fas fa-spinner fa-spin"></i>';
    btn.disabled = true;

    navigator.geolocation.getCurrentPosition(
        (position) => {
            // Pre-fill with a 4-character locator (square)
            const loc = latLngToLocator(position.coords.latitude, position.coords.longitude, 4);
            document.getElementById('target').value = loc;
            localStorage.setItem('target', loc);
            btn.innerHTML = originalText;
            btn.disabled = false;
            
            const btnSubmit = document.getElementById('btn-submit');
            if (btnSubmit) btnSubmit.textContent = 'Go';
            document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
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

    const target = document.getElementById('target').value.trim().toUpperCase();
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

document.getElementById('btn-cycle').addEventListener('click', () => {
    const btn = document.getElementById('btn-cycle');
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
            document.getElementById('band-container').dispatchEvent(new Event('change'));
        }, cycleTimeMs);
    }
});

document.getElementById('target').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
        e.preventDefault();
        const btnSubmit = document.getElementById('btn-submit');
        if (btnSubmit) btnSubmit.textContent = 'Go';
        document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
});

document.getElementById('fetch-form').addEventListener('submit', (e) => {
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

        stopDxPolling();
        resetDxConditions();

        btnSubmit.textContent = 'Go';
        document.getElementById('stream-status').innerHTML = 'Status: Not subscribed';
        setFaviconColor('#6c757d');
        return;
    }

    const target = document.getElementById('target').value.trim().toUpperCase();
    const minutes = document.getElementById('minutes').value || 15;

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

    state.eventSource = new EventSource(`/api/stream?${params.toString()}`);
    setFaviconColor('#ffa500'); // Orange for connecting/waiting

    if (dk3jfModeEnabled) {
        startDxPolling(getCurrentDxRequestParams);
    }
    
    let historyLoading = true;

    state.eventSource.onopen = () => {
        console.log("Connected to live MQTT stream");
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong></br><span style="color: orange;">(Fetching history...)</span> <div class="spinner"></div>`;
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
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong> </br> <span style="color: green;">Receiving data (Spots: ${formatNumber(totalReceived)})</span>`;
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
                statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong> </br> <span style="color: orange;">Fetching history (Spots: ${formatNumber(totalReceived)})</span> <div class="spinner"></div>`;
            } else {
                statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong> </br> <span style="color: green;">Receiving data (Spots: ${formatNumber(totalReceived)})</span>`;
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
        if (state.liveSpots.length > 0) {
            const maxAge = parseInt(minutes) * 60;
            state.liveSpots.forEach(s => s.ageSeconds += 5); 
            state.liveSpots = state.liveSpots.filter(s => s.ageSeconds <= maxAge);
            scheduleRender();
        }
    }, 5000);
});