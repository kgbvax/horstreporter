import { state } from './state.js';
import { loadConfig } from './config.js';
import { initMap, setTheme, map } from './map.js';
import { initUI } from './ui.js';
import { updateMapVisualization } from './renderers.js';
import { latLngToLocator, locatorToBounds, setFaviconColor, getMinSnrMode, getEnabledBands, getSelectedBand, formatNumber, bandColors } from './utils.js';

// --- Init Configuration & Map ---
const { initialCenter, initialZoom } = loadConfig();
initMap(initialCenter, initialZoom);
const savedTheme = localStorage.getItem('theme') || 'light';
setTheme(savedTheme);

// --- Init UI ---
initUI();

function updateCurrentBandDisplay() {
    const band = getSelectedBand();
    const display = document.getElementById('current-band-display');
    if (display) {
        display.textContent = band === 'all' ? 'All Bands' : band;
        display.style.backgroundColor = bandColors[band] || bandColors['all'];
        display.style.color = (band === '15m' || band === '12m') ? '#212529' : '#fff';
    }
}

// Force an initial render to sync visual band states (colors/opacity) loaded from localStorage
updateMapVisualization(state.liveSpots, parseInt(document.getElementById('minutes')?.value || 15));
updateCurrentBandDisplay();

let lastRenderTime = 0;

export function scheduleRender() {
    if (state.renderPending) return;

    const now = Date.now();
    const timeSinceLastRender = now - lastRenderTime;

    const doRender = () => {
        requestAnimationFrame(() => {
            const minutes = document.getElementById('minutes').value || 15;
            updateMapVisualization(state.liveSpots, parseInt(minutes));
            lastRenderTime = Date.now();
            state.renderPending = false;
        });
    };

    if (timeSinceLastRender >= 1000) {
        state.renderPending = true;
        doRender();
    } else {
        state.renderPending = true;
        setTimeout(doRender, 1000 - timeSinceLastRender);
    }
}

map.on('click', function(e) {
    const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, 4);
    const targetInput = document.getElementById('target');
    if (targetInput) {
        targetInput.value = loc;
        const btnSubmit = document.getElementById('btn-submit');
        if (btnSubmit) btnSubmit.textContent = 'Go'; // Force a clean restart
        document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
});

document.getElementById('theme-toggle').addEventListener('click', () => {
    const currentTheme = document.body.getAttribute('data-theme');
    const newTheme = currentTheme === 'dark' ? 'light' : 'dark';
    setTheme(newTheme);
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
    if (e.propertyName === 'margin-left') {
        map.invalidateSize(); // Fixes distorted tile layers and centering after map container is stretched
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
        const highBands = ['20m', '17m', '15m', '12m', '10m', '6m', '4m'];
        const lowBands = ['160m', '80m', '60m', '40m', '30m'];
        const ssbBands = ['160m', '80m', '40m', '20m', '17m', '15m', '12m', '10m', '6m', '4m'];
        
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

document.getElementById('style-group')?.addEventListener('change', (e) => {
    if (e.target.name === 'style-select') {
        localStorage.setItem('mapStyle', e.target.value);
        scheduleRender();
    }
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
        if (target.length === 4) {
            const bounds = locatorToBounds(target);
            if (bounds) {
                state.targetLayer = L.rectangle(bounds, { color: '#ff0000', weight: 3, fillOpacity: 0.1, interactive: false }).addTo(map);
            }
        } else if (target.length >= 6) {
            const bounds = locatorToBounds(target);
            if (bounds) {
                const lat = (bounds[0][0] + bounds[1][0]) / 2;
                const lng = (bounds[0][1] + bounds[1][1]) / 2;
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

// Automatically trigger analysis on load if a target is saved
if (localStorage.getItem('target')) {
    document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
}