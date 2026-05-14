let initialCenter = [20, 0];
let initialZoom = 2;

try {
    const savedCenter = localStorage.getItem('mapCenter');
    const savedZoom = localStorage.getItem('mapZoom');
    if (savedCenter) initialCenter = JSON.parse(savedCenter);
    if (savedZoom) initialZoom = parseFloat(savedZoom);
} catch (e) {
    console.error("Error parsing saved map state", e);
}

try {
    document.querySelectorAll('.band-enable').forEach(cb => {
        const savedEnable = localStorage.getItem(`enable-${cb.value}`);
        if (savedEnable !== null) {
            cb.checked = savedEnable === 'true';
            const radio = document.querySelector(`input[name="band"][value="${cb.value}"]`);
            if (radio) radio.disabled = !cb.checked;
        }
    });

    const savedTarget = localStorage.getItem('target');
    if (savedTarget) document.getElementById('target').value = savedTarget;

    const savedMinutes = localStorage.getItem('minutes');
    if (savedMinutes) document.getElementById('minutes').value = savedMinutes;

    const savedMinSnr = localStorage.getItem('minSnrSelect');
    if (savedMinSnr) {
        const radio = document.querySelector(`input[name="min-snr"][value="${savedMinSnr}"]`);
        if (radio) radio.checked = true;
    }

    const savedStyle = localStorage.getItem('mapStyle');
    if (savedStyle) {
        const radio = document.querySelector(`input[name="style-select"][value="${savedStyle}"]`);
        if (radio) radio.checked = true;
    }

    const savedBand = localStorage.getItem('selectedBand');
    if (savedBand) {
        const radio = document.querySelector(`input[name="band"][value="${savedBand}"]`);
        if (radio) radio.checked = true;
    }

    const savedSsbMinDb = localStorage.getItem('ssbMinDb');
    if (savedSsbMinDb !== null && document.getElementById('ssb-min-db')) {
        document.getElementById('ssb-min-db').value = savedSsbMinDb;
    }

    const savedCwMinDb = localStorage.getItem('cwMinDb');
    if (savedCwMinDb !== null && document.getElementById('cw-min-db')) {
        document.getElementById('cw-min-db').value = savedCwMinDb;
    }

    const savedClusterDist = localStorage.getItem('clusterDistance');
    if (savedClusterDist !== null && document.getElementById('cluster-distance')) {
        document.getElementById('cluster-distance').value = savedClusterDist;
        document.getElementById('cluster-dist-val').textContent = savedClusterDist;
    }

    const savedSmoothEdges = localStorage.getItem('smoothEdges');
    if (savedSmoothEdges !== null && document.getElementById('smooth-edges')) {
        document.getElementById('smooth-edges').checked = savedSmoothEdges === 'true';
    }

    const savedAutoZoom = localStorage.getItem('autoZoom');
    if (savedAutoZoom !== null && document.getElementById('auto-zoom')) {
        document.getElementById('auto-zoom').checked = savedAutoZoom === 'true';
    }

    const savedSurroundings = localStorage.getItem('surroundings');
    if (savedSurroundings !== null && document.getElementById('surroundings')) {
        document.getElementById('surroundings').checked = savedSurroundings === 'true';
    }
} catch (e) {
    console.error("Error parsing saved form state", e);
}

const map = L.map('map', {
    zoomSnap: 0.25,
    zoomDelta: 0.25
}).setView(initialCenter, initialZoom);

const lightTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

const darkTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

let currentTileLayer = null;
let heatLayer = null;
let targetLayer = null;
let eventSource = null;
let liveSpots = [];
let renderInterval = null;
let cycleInterval = null;

// Debounce rendering to prevent UI lockups during heavy spot bursts
let renderPending = false;
function scheduleRender() {
    if (!renderPending) {
        renderPending = true;
        requestAnimationFrame(() => {
            const minutes = document.getElementById('minutes').value || 15;
            updateMapVisualization(liveSpots, parseInt(minutes));
            renderPending = false;
        });
    }
}

map.on('moveend', () => {
    const center = map.getCenter();
    localStorage.setItem('mapCenter', JSON.stringify([center.lat, center.lng]));
});

map.on('zoomend', () => {
    localStorage.setItem('mapZoom', map.getZoom());
});

function setTheme(theme) {
    document.body.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);

    if (currentTileLayer) {
        map.removeLayer(currentTileLayer);
    }

    if (theme === 'dark') {
        currentTileLayer = darkTileLayer;
        document.getElementById('theme-toggle').innerHTML = '<i class="fas fa-sun"></i>';
        document.getElementById('theme-toggle').title = 'Switch to light theme';
    } else {
        currentTileLayer = lightTileLayer;
        document.getElementById('theme-toggle').innerHTML = '<i class="fas fa-moon"></i>';
        document.getElementById('theme-toggle').title = 'Switch to dark theme';
    }
    currentTileLayer.addTo(map);
}

function formatNumber(num) {
    if (num == null) return '';
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

// --- Theme Initializer ---
const savedTheme = localStorage.getItem('theme') || 'dark'; // Default to dark theme
setTheme(savedTheme);

function getGridResolution() {
    const target = document.getElementById('target')?.value.trim() || '';
    if (/^[A-Za-z]{2}[0-9]{2}[A-Za-z]{2}/.test(target)) {
        return 6;
    }
    return 4;
}

function latLngToLocator(lat, lng, precision = 4) {
    lng = Math.max(-180, Math.min(180, lng));
    lat = Math.max(-90, Math.min(90, lat));
    let _lon = lng + 180;
    let _lat = lat + 90;
    let char1 = String.fromCharCode(65 + Math.floor(_lon / 20));
    let char2 = String.fromCharCode(65 + Math.floor(_lat / 10));
    _lon = _lon % 20;
    _lat = _lat % 10;
    let char3 = String.fromCharCode(48 + Math.floor(_lon / 2));
    let char4 = String.fromCharCode(48 + Math.floor(_lat / 1));
    
    if (precision < 6) return char1 + char2 + char3 + char4;
    
    _lon = (_lon % 2) * 60;
    _lat = (_lat % 1) * 60;
    let char5 = String.fromCharCode(65 + Math.floor(_lon / 5));
    let char6 = String.fromCharCode(65 + Math.floor(_lat / 2.5));
    return char1 + char2 + char3 + char4 + char5 + char6;
}

function getMinSnrMode() {
    const checkedRadio = document.querySelector('input[name="min-snr"]:checked');
    return checkedRadio ? checkedRadio.value : 'none';
}

function setFaviconColor(color) {
    const favicon = document.getElementById('favicon');
    if (favicon) {
        const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="8" fill="${color}"/></svg>`;
        favicon.href = `data:image/svg+xml,${encodeURIComponent(svg)}`;
    }
}

function getSelectedBand() {
    const checkedRadio = document.querySelector('input[name="band"]:checked');
    return checkedRadio ? checkedRadio.value : 'all';
}

function getEnabledBands() {
    const enabled = new Set();
    document.querySelectorAll('.band-enable').forEach(cb => {
        if (cb.checked) enabled.add(cb.value);
    });
    return enabled;
}

const tooltip = document.getElementById('tooltip');

map.on('mousemove', function(e) {
    const res = getGridResolution();
    const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, res);
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    
    let baseDb = 0;
    if (minSnrMode === 'ssb') baseDb = ssbMinDb;
    else if (minSnrMode === 'cw') baseDb = cwMinDb;

    const squareSpots = liveSpots.filter(s => {
        if (!s.locator || !s.locator.startsWith(loc)) return false;
        if (minSnrMode === 'ssb' && s.snr < ssbMinDb) return false;
        if (minSnrMode === 'cw' && s.snr < cwMinDb) return false;
        if (!enabledBands.has(s.band)) return false;
        if (selectedBand !== 'all' && s.band !== selectedBand) return false;
        return true;
    });

    if (squareSpots.length > 0) {
        let min = Math.min(...squareSpots.map(s => s.snr));
        let max = Math.max(...squareSpots.map(s => s.snr));
        let avg = Math.round(squareSpots.reduce((sum, s) => sum + s.snr, 0) / squareSpots.length);
        
        let bestBand = 'N/A';
        let bestSnr = -999;
        squareSpots.forEach(s => {
            if (s.snr > bestSnr) {
                bestSnr = s.snr;
                bestBand = s.band;
            }
        });
        
        squareSpots.sort((a, b) => b.snr - a.snr);
        
        let uniqueSpots = [];
        let seenPairs = new Set();
        for (let s of squareSpots) {
            let pairKey = `${s.sender}-${s.receiver}`;
            if (!seenPairs.has(pairKey)) {
                seenPairs.add(pairKey);
                uniqueSpots.push(s);
                if (uniqueSpots.length >= 10) break;
            }
        }
        let topSpots = uniqueSpots;

        let reportsHtml = `<hr style="margin: 5px 0; border: 0; border-top: 1px solid var(--tooltip-border);">` +
                          `<span style="font-size: 11px;"><b>Top Reports:</b><br>`;
        topSpots.forEach(s => {
            reportsHtml += `${s.sender} / ${s.receiver} / ${s.snr}dB<br>`;
        });
        reportsHtml += `</span>`;

        tooltip.innerHTML = `<strong>${loc}</strong><br>Min: ${min}dB<br>Max: ${max}dB<br>Avg: ${avg}dB<br>Best Band: ${bestBand}<br>Spots: ${formatNumber(squareSpots.length)}${reportsHtml}`;
        tooltip.style.display = 'block';
        tooltip.style.left = (e.originalEvent.pageX + 15) + 'px';
        tooltip.style.top = (e.originalEvent.pageY + 15) + 'px';
    } else {
        tooltip.style.display = 'none';
    }
});

map.on('mouseout', function() {
    tooltip.style.display = 'none';
});

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

// --- Info Overlay Initialization ---
const themeToggleBtn = document.getElementById('theme-toggle');
if (themeToggleBtn && themeToggleBtn.parentNode) {
    // Group the new info button and the theme toggle together
    const headerActions = document.createElement('div');
    headerActions.style.display = 'flex';
    headerActions.style.gap = '8px';
    
    const infoBtn = document.createElement('button');
    infoBtn.id = 'info-toggle';
    infoBtn.innerHTML = '<i class="fas fa-info-circle"></i>';
    infoBtn.title = 'Information';
    infoBtn.style.background = 'none';
    infoBtn.style.border = '1px solid var(--border-color)';
    infoBtn.style.borderRadius = '5px';
    infoBtn.style.cursor = 'pointer';
    infoBtn.style.fontSize = '18px';
    infoBtn.style.padding = '4px 8px';
    infoBtn.style.lineHeight = '1';

    themeToggleBtn.parentNode.insertBefore(headerActions, themeToggleBtn);
    headerActions.appendChild(infoBtn);
    headerActions.appendChild(themeToggleBtn);

    // Create overlay
    const overlay = document.createElement('div');
    overlay.id = 'info-overlay';
    overlay.innerHTML = `
        <div class="info-content">
            <button id="close-info" title="Close">&times;</button>
            <iframe src="info.html" frameborder="0"></iframe>
        </div>
    `;
    document.body.appendChild(overlay);

    infoBtn.addEventListener('click', () => overlay.style.display = 'flex');
    document.getElementById('close-info').addEventListener('click', () => overlay.style.display = 'none');
    overlay.addEventListener('click', (e) => { if (e.target === overlay) overlay.style.display = 'none'; });
}

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

document.getElementById('cluster-distance')?.addEventListener('input', (e) => {
    document.getElementById('cluster-dist-val').textContent = e.target.value;
    localStorage.setItem('clusterDistance', e.target.value);
    scheduleRender();
});

document.getElementById('smooth-edges')?.addEventListener('change', (e) => {
    localStorage.setItem('smoothEdges', e.target.checked);
    scheduleRender();
});

document.getElementById('band-container').addEventListener('change', (e) => {
    if (e && e.isTrusted && cycleInterval && e.target && e.target.name === 'band') {
        clearInterval(cycleInterval);
        cycleInterval = null;
        const btn = document.getElementById('btn-cycle');
        if (btn) {
            btn.innerHTML = '<i class="fas fa-play"></i>';
            btn.title = 'Cycle Active Bands';
        }
    }
    localStorage.setItem('selectedBand', getSelectedBand());
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
    if (cycleInterval) {
        clearInterval(cycleInterval);
        cycleInterval = null;
        btn.innerHTML = '<i class="fas fa-play"></i>';
        btn.title = 'Cycle Active Bands';
    } else {
        btn.innerHTML = '<i class="fas fa-pause"></i>';
        btn.title = 'Stop Cycling';
        cycleInterval = setInterval(() => {
            const minSnrMode = getMinSnrMode();
            const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
            const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
            const activeBands = new Set();
            const enabledBands = getEnabledBands();

            liveSpots.forEach(s => {
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
        }, 3000);
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
        if (eventSource) {
            eventSource.close();
            eventSource = null;
        }
        if (renderInterval) {
            clearInterval(renderInterval);
            renderInterval = null;
        }
        liveSpots = [];
        if (heatLayer) {
            map.removeLayer(heatLayer);
            heatLayer = null;
        }
        if (targetLayer) {
            map.removeLayer(targetLayer);
            targetLayer = null;
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

    if (eventSource) {
        eventSource.close();
    }
    if (renderInterval) {
        clearInterval(renderInterval);
    }
    
    liveSpots = [];
    if (heatLayer) {
        map.removeLayer(heatLayer);
        heatLayer = null;
    }

    if (targetLayer) {
        map.removeLayer(targetLayer);
        targetLayer = null;
    }

    const isLocator = /^[A-Z]{2}[0-9]{2}([A-Z]{2})?$/.test(target);
    if (isLocator) {
        if (target.length === 4) {
            const bounds = locatorToBounds(target);
            if (bounds) {
                targetLayer = L.rectangle(bounds, { color: '#ff0000', weight: 3, fillOpacity: 0.1, interactive: false }).addTo(map);
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
                targetLayer = L.marker([lat, lng], { icon: crossIcon, interactive: false }).addTo(map);
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

    eventSource = new EventSource(`/api/stream?${params.toString()}`);
    setFaviconColor('#ffa500'); // Orange for connecting/waiting
    
    let historyLoading = true;

    eventSource.onopen = () => {
        console.log("Connected to live MQTT stream");
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong></br><span style="color: orange;">(Fetching history...)</span> <div class="spinner"></div>`;
        setFaviconColor('#ffa500'); // Orange until data arrives
    };

    eventSource.addEventListener('history_end', () => {
        historyLoading = false;
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong> </br> <span style="color: green;">Receiving data (Spots: ${formatNumber(totalReceived)})</span>`;
        lastStatusUpdate = Date.now();
    });

    eventSource.onmessage = (e) => {
        totalReceived++;
        
        const spot = JSON.parse(e.data);
        liveSpots.push(spot);
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

        scheduleRender();
    };

    eventSource.onerror = (e) => {
        console.error("Stream error:", e);
        statusEl.innerHTML = `Status: <span style="color: red;">Connection error / Disconnected</span>`;
        setFaviconColor('#dc3545'); // Red for error
    };

    renderInterval = setInterval(() => {
        if (liveSpots.length > 0) {
            const maxAge = parseInt(minutes) * 60;
            liveSpots.forEach(s => s.ageSeconds += 5); 
            liveSpots = liveSpots.filter(s => s.ageSeconds <= maxAge);
            scheduleRender();
        }
    }, 5000);
});

const bandColors = {
    '80m': '#800080',
    '40m': '#0000FF',
    '20m': '#008000',
    '17m': '#808000',
    '15m': '#FFA500',
    '12m': '#00FFFF',
    '10m': '#FF0000',
    '6m':  '#FF00FF',
    'all': '#555555'
};

function locatorToBounds(locator) {
    if (!locator || locator.length < 4) return null;
    locator = locator.toUpperCase();
    let lng = (locator.charCodeAt(0) - 65) * 20 - 180;
    let lat = (locator.charCodeAt(1) - 65) * 10 - 90;
    lng += (locator.charCodeAt(2) - 48) * 2;
    lat += (locator.charCodeAt(3) - 48) * 1;
    
    if (locator.length >= 6) {
        lng += (locator.charCodeAt(4) - 65) * (5/60);
        lat += (locator.charCodeAt(5) - 65) * (2.5/60);
        return [[lat, lng], [lat + (2.5/60), lng + (5/60)]];
    }
    return [[lat, lng], [lat + 1, lng + 2]];
}

function updateBandLabels(spots) {
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const activeBands = new Set();
    const enabledBands = getEnabledBands();

    spots.forEach(s => {
        if (minSnrMode === 'ssb' && s.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && s.snr < cwMinDb) return;
        if (!enabledBands.has(s.band)) return;
        activeBands.add(s.band);
    });

    const radios = document.querySelectorAll('input[name="band"]');
    radios.forEach(radio => {
        if (radio.value === 'all') return;
        const label = radio.parentElement;
        const isEnabled = enabledBands.has(radio.value);
        
        if (!isEnabled) {
            label.style.opacity = '0.2';
            label.style.filter = 'grayscale(100%)';
            return;
        }

        if (activeBands.has(radio.value)) {
            label.style.opacity = '1';
            label.style.filter = 'none';
        } else {
            label.style.opacity = '0.4';
            label.style.filter = 'grayscale(100%)';
        }
    });
}

function updateMapVisualization(spots, maxMinutes) {
    if (heatLayer) map.removeLayer(heatLayer);
    
    updateBandLabels(spots);

    const checkedStyleRadio = document.querySelector('input[name="style-select"]:checked');
    const style = checkedStyleRadio ? checkedStyleRadio.value : 'grid-snr';

    switch (style) {
        case 'grid-snr':
            renderGridSnr(spots, maxMinutes);
            break;
        case 'heatmap':
            renderLiveHeatmap(spots, maxMinutes);
            break;
        case 'active-area':
            renderActiveArea(spots, maxMinutes);
            break;
        default:
            renderGridSnr(spots, maxMinutes);
    }

    if (document.getElementById('auto-zoom')?.checked) {
        const minSnrMode = getMinSnrMode();
        const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
        const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
        const selectedBand = getSelectedBand();
        const enabledBands = getEnabledBands();

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

        if (found) {
            const bounds = L.latLngBounds([minLat, minLng], [maxLat, maxLng]);
            map.fitBounds(bounds, { padding: [20, 20], maxZoom: 8 });
        }
    }
}
function renderGridSnr(spots, maxMinutes) {
    heatLayer = L.layerGroup().addTo(map);

    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    const squareData = {};
    const res = getGridResolution();

    spots.forEach(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        let loc = spot.locator.substring(0, res);
        if (loc.length < res) loc = spot.locator.substring(0, 4); // Fallback if data is sparse

        if (loc.length >= 4) {
            if (!squareData[loc]) {
                squareData[loc] = { snrSum: 0, count: 0, bands: {} };
            }
            squareData[loc].snrSum += spot.snr;
            squareData[loc].count++;
            squareData[loc].bands[spot.band] = (squareData[loc].bands[spot.band] || 0) + 1;
        }
    });

    for (let loc in squareData) {
        let avgSnr = squareData[loc].snrSum / squareData[loc].count;
        
        let dominantBand = 'all';
        let maxCount = 0;
        for (let b in squareData[loc].bands) {
            if (squareData[loc].bands[b] > maxCount) {
                maxCount = squareData[loc].bands[b];
                dominantBand = b;
            }
        }

        let color = bandColors[dominantBand] || bandColors['all'];
        
        let opacity = 0.3; // Low intensity (< 0 dB)
        if (avgSnr >= 0 && avgSnr < 10) opacity = 0.6; // Medium intensity (0 - 9 dB)
        else if (avgSnr >= 10) opacity = 0.9; // High intensity (>= 10 dB)
        
        let bounds = locatorToBounds(loc);
        if (bounds) {
            L.rectangle(bounds, {
                color: color,
                weight: 1,
                fillColor: color,
                fillOpacity: opacity
            }).addTo(heatLayer);
        }
    }
}

function renderActiveArea(spots, maxMinutes) {
    heatLayer = L.layerGroup().addTo(map);

    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    let maxClusterDist = parseInt(document.getElementById('cluster-distance')?.value, 10);
    if (isNaN(maxClusterDist) || maxClusterDist < 100) maxClusterDist = 500;
    const smoothEdges = document.getElementById('smooth-edges')?.checked;

    const pointsByBand = {};

    spots.forEach(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        if (!pointsByBand[spot.band]) {
            pointsByBand[spot.band] = [];
        }
        // Turf expects coordinates as [longitude, latitude]
        pointsByBand[spot.band].push(turf.point([spot.lng, spot.lat]));
    });

    for (const band in pointsByBand) {
        const pts = pointsByBand[band];
        const color = bandColors[band] || bandColors['all'];
        
        if (pts.length >= 3) {
            const fc = turf.featureCollection(pts);
            
            // Group points that are within the configured distance of each other into clusters
            // to prevent drawing massive polygons across oceans with no activity.
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
                    const hull = turf.convex(clusterFc);
                    if (hull) {
                        let finalShape = hull;
                        if (smoothEdges) {
                            try {
                                finalShape = turf.polygonSmooth(hull, { iterations: 2 });
                            } catch (e) {
                                console.error("Error smoothing polygon", e);
                            }
                        }
                        L.geoJSON(finalShape, {
                            style: { color: color, weight: 2, opacity: 0.8, fillColor: color, fillOpacity: 0.2 },
                            interactive: false
                        }).addTo(heatLayer);
                    }
                } else {
                    clusterPts.forEach(p => isolatedPts.push(p));
                }
            }

            // Draw isolated points that couldn't form a polygon
            isolatedPts.forEach(p => {
                L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
                    color: color, radius: 5, weight: 2, fillOpacity: 0.5, interactive: false
                }).addTo(heatLayer);
            });
        } else {
            // For 1 or 2 isolated spots that cannot form a geometric polygon, fallback to simple markers
            pts.forEach(p => {
                L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
                    color: color, radius: 5, weight: 2, fillOpacity: 0.5, interactive: false
                }).addTo(heatLayer);
            });
        }
    }
}

function renderLiveHeatmap(spots, maxMinutes) {
    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();

    const heatPoints = spots
        .filter(spot => {
            if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
            if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
            if (!enabledBands.has(spot.band)) return false;
            if (selectedBand !== 'all' && spot.band !== selectedBand) return false;
            return true;
        })
        .map(spot => {
            const intensity = Math.max(0, Math.min(1, (spot.snr + 20) / 40));
            return [spot.lat, spot.lng, intensity];
        });

    if (heatPoints.length > 0) {
        heatLayer = L.heatLayer(heatPoints, {
            radius: 25,
            blur: 15,
            maxZoom: 10,
            max: 1.0,
            gradient: {0.4: 'blue', 0.6: 'lime', 0.8: 'yellow', 1.0: 'red'}
        }).addTo(map);
    } else {
        heatLayer = L.layerGroup().addTo(map);
    }
}

function updateServerStats() {
    const statsEl = document.getElementById('server-stats');
    if (!statsEl) return;

    fetch('/api/stats')
        .then(response => response.json())
        .then(stats => {
            statsEl.innerHTML = `Connections: ${formatNumber(stats.active_connections)} History: ${formatNumber(stats.history_size)} spots (${formatNumber(stats.history_minutes)} mins)`;
        })
        .catch(error => {
            console.error('Error fetching server stats:', error);
            statsEl.innerHTML = 'Server stats unavailable.';
        });
}

updateServerStats();
setInterval(updateServerStats, 10000); // Update every 10 seconds

// Automatically trigger analysis on load if a target is saved
if (localStorage.getItem('target')) {
    document.getElementById('fetch-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
}