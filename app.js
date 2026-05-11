const map = L.map('map').setView([20, 0], 2);

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
let eventSource = null;
let liveSpots = [];
let renderInterval = null;

function setTheme(theme) {
    document.body.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);

    if (currentTileLayer) {
        map.removeLayer(currentTileLayer);
    }

    if (theme === 'dark') {
        currentTileLayer = darkTileLayer;
        document.getElementById('theme-toggle').innerText = '☀️';
        document.getElementById('theme-toggle').title = 'Switch to light theme';
    } else {
        currentTileLayer = lightTileLayer;
        document.getElementById('theme-toggle').innerText = '🌙';
        document.getElementById('theme-toggle').title = 'Switch to dark theme';
    }
    currentTileLayer.addTo(map);
}

// --- Theme Initializer ---
const savedTheme = localStorage.getItem('theme') || 'dark'; // Default to dark theme
setTheme(savedTheme);

function latLngToLocator(lat, lng) {
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
    return char1 + char2 + char3 + char4;
}

function getSelectedBand() {
    const checkedRadio = document.querySelector('input[name="band"]:checked');
    return checkedRadio ? checkedRadio.value : 'all';
}

const tooltip = document.getElementById('tooltip');

map.on('mousemove', function(e) {
    const loc = latLngToLocator(e.latlng.lat, e.latlng.lng);
    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const selectedBand = getSelectedBand();
    
    const squareSpots = liveSpots.filter(s => {
        if (!s.locator || !s.locator.startsWith(loc)) return false;
        if (filter0dbEnabled && s.snr <= 0) return false;
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
        
        tooltip.innerHTML = `<strong>${loc}</strong><br>Min: ${min}dB<br>Max: ${max}dB<br>Avg: ${avg}dB<br>Best Band: ${bestBand}<br>Spots: ${squareSpots.length}`;
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

document.getElementById('theme-toggle').addEventListener('click', () => {
    const currentTheme = document.body.getAttribute('data-theme');
    const newTheme = currentTheme === 'dark' ? 'light' : 'dark';
    setTheme(newTheme);
});

document.getElementById('filter-0db').addEventListener('change', () => {
    const minutes = document.getElementById('minutes').value || 15;
    updateMapVisualization(liveSpots, parseInt(minutes));
});

document.getElementById('band-container').addEventListener('change', () => {
    const minutes = document.getElementById('minutes').value || 15;
    updateMapVisualization(liveSpots, parseInt(minutes));
});

document.getElementById('style-select')?.addEventListener('change', () => {
    const minutes = document.getElementById('minutes').value || 15;
    updateMapVisualization(liveSpots, parseInt(minutes));
});

document.getElementById('btn-geo').addEventListener('click', () => {
    if (!navigator.geolocation) {
        alert('Geolocation is not supported by your browser.');
        return;
    }

    const btn = document.getElementById('btn-geo');
    const originalText = btn.innerHTML;
    btn.innerHTML = '⏳';
    btn.disabled = true;

    navigator.geolocation.getCurrentPosition(
        (position) => {
            const loc = latLngToLocator(position.coords.latitude, position.coords.longitude);
            document.getElementById('target').value = loc;
            btn.innerHTML = originalText;
            btn.disabled = false;
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

let cycleInterval = null;

document.getElementById('btn-cycle').addEventListener('click', () => {
    const btn = document.getElementById('btn-cycle');
    if (cycleInterval) {
        clearInterval(cycleInterval);
        cycleInterval = null;
        btn.innerHTML = '▶️';
        btn.title = 'Cycle Active Bands';
    } else {
        btn.innerHTML = '⏸️';
        btn.title = 'Stop Cycling';
        cycleInterval = setInterval(() => {
            const filter0dbEnabled = document.getElementById('filter-0db').checked;
            const activeBands = new Set();
            liveSpots.forEach(s => {
                if (filter0dbEnabled && s.snr <= 0) return;
                activeBands.add(s.band);
            });

            const radios = Array.from(document.querySelectorAll('input[name="band"]'))
                .filter(r => r.value !== 'all' && activeBands.has(r.value));
            
            if (radios.length === 0) return;

            const currentBand = getSelectedBand();
            let currentIndex = radios.findIndex(r => r.value === currentBand);
            let nextIndex = (currentIndex + 1) % radios.length;
            
            radios[nextIndex].checked = true;
            document.getElementById('band-container').dispatchEvent(new Event('change'));
        }, 3000);
    }
});

document.getElementById('fetch-form').addEventListener('submit', (e) => {
    e.preventDefault();
    const target = document.getElementById('target').value.trim();
    const minutes = document.getElementById('minutes').value || 15;

    if (!target) {
        alert('Please provide a Callsign or Locator.');
        return;
    }

    const isLocator = /^[A-Za-z]{2}[0-9]{2}([A-Za-z]{2})?$/.test(target);
    const callsign = !isLocator ? target : '';
    const locator = isLocator ? target : '';

    console.log(`Starting live stream for callsign: '${callsign}', locator: '${locator}'`);

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

    const params = new URLSearchParams();
    if (callsign) params.append('callsign', callsign);
    if (locator) params.append('locator', locator);

    const statusEl = document.getElementById('stream-status');
    const currentSub = callsign ? `Callsign: ${callsign}` : `Locator: ${locator}`;
    let totalReceived = 0;
    statusEl.innerHTML = `Status: Connecting to ${currentSub}...`;

    eventSource = new EventSource(`/api/stream?${params.toString()}`);
    
    eventSource.onopen = () => {
        console.log("Connected to live MQTT stream");
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong></br><span style="color: orange;">(Waiting for data...)</span>`;
    };

    eventSource.onmessage = (e) => {
        totalReceived++;
        statusEl.innerHTML = `Status: Subscribed to <strong>${currentSub}</strong> </br> <span style="color: green;">Receiving data (Spots: ${totalReceived})</span>`;
        const spot = JSON.parse(e.data);
        liveSpots.push(spot);

        updateMapVisualization(liveSpots, parseInt(minutes));
    };

    eventSource.onerror = (e) => {
        console.error("Stream error:", e);
        statusEl.innerHTML = `Status: <span style="color: red;">Connection error / Disconnected</span>`;
    };

    renderInterval = setInterval(() => {
        if (liveSpots.length > 0) {
            const maxAge = parseInt(minutes) * 60;
            liveSpots.forEach(s => s.ageSeconds += 5); 
            liveSpots = liveSpots.filter(s => s.ageSeconds <= maxAge);
            updateMapVisualization(liveSpots, parseInt(minutes));
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
    return [[lat, lng], [lat + 1, lng + 2]];
}

function updateBandLabels(spots) {
    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const activeBands = new Set();
    spots.forEach(s => {
        if (filter0dbEnabled && s.snr <= 0) return;
        activeBands.add(s.band);
    });

    const radios = document.querySelectorAll('input[name="band"]');
    radios.forEach(radio => {
        if (radio.value === 'all') return;
        const label = radio.parentElement;
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

    const style = document.getElementById('style-select')?.value || 'grid-snr';

    switch (style) {
        case 'grid-snr':
            renderGridSnr(spots, maxMinutes);
            break;
        case 'grid-age':
            renderGridAge(spots, maxMinutes);
            break;
        case 'aggregated':
            renderAggregatedFields(spots, maxMinutes);
            break;
        case 'heatmap':
            renderLiveHeatmap(spots, maxMinutes);
            break;
        default:
            renderGridSnr(spots, maxMinutes);
    }
}
function renderGridSnr(spots, maxMinutes) {
    heatLayer = L.layerGroup().addTo(map);

    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const selectedBand = getSelectedBand();
    const squareData = {};

    spots.forEach(spot => {
        if (filter0dbEnabled && spot.snr <= 0) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        let loc4 = spot.locator.substring(0, 4);
        if (loc4.length === 4) {
            if (!squareData[loc4]) {
                squareData[loc4] = { snrSum: 0, count: 0, bands: {} };
            }
            squareData[loc4].snrSum += spot.snr;
            squareData[loc4].count++;
            squareData[loc4].bands[spot.band] = (squareData[loc4].bands[spot.band] || 0) + 1;
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

function renderGridAge(spots, maxMinutes) {
    heatLayer = L.layerGroup().addTo(map);

    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const selectedBand = getSelectedBand();
    const squareData = {};

    spots.forEach(spot => {
        if (filter0dbEnabled && spot.snr <= 0) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        let loc4 = spot.locator.substring(0, 4);
        if (loc4.length === 4) {
            if (!squareData[loc4]) {
                squareData[loc4] = { bands: {}, minAge: Infinity };
            }
            squareData[loc4].bands[spot.band] = (squareData[loc4].bands[spot.band] || 0) + 1;
            squareData[loc4].minAge = Math.min(squareData[loc4].minAge, spot.ageSeconds);
        }
    });

    const maxAgeSeconds = maxMinutes * 60;

    for (let loc in squareData) {
        let dominantBand = 'all';
        let maxCount = 0;
        for (let b in squareData[loc].bands) {
            if (squareData[loc].bands[b] > maxCount) {
                maxCount = squareData[loc].bands[b];
                dominantBand = b;
            }
        }

        let color = bandColors[dominantBand] || bandColors['all'];
        
        const opacity = 0.3 + (0.6 * (1 - (squareData[loc].minAge / maxAgeSeconds)));
        
        let bounds = locatorToBounds(loc);
        if (bounds) {
            L.rectangle(bounds, {
                color: color,
                weight: 1,
                fillColor: color,
                fillOpacity: Math.max(0.2, Math.min(0.9, opacity))
            }).addTo(heatLayer);
        }
    }
}

function fieldToBounds(field) {
    if (!field || field.length < 2) return null;
    field = field.toUpperCase();
    const lng = (field.charCodeAt(0) - 65) * 20 - 180;
    const lat = (field.charCodeAt(1) - 65) * 10 - 90;
    return [[lat, lng], [lat + 10, lng + 20]];
}

function renderAggregatedFields(spots, maxMinutes) {
    heatLayer = L.layerGroup().addTo(map);

    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const selectedBand = getSelectedBand();
    
    const squareData = {};
    spots.forEach(spot => {
        if (filter0dbEnabled && spot.snr <= 0) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        let loc4 = spot.locator.substring(0, 4);
        if (loc4.length === 4) {
            if (!squareData[loc4]) {
                squareData[loc4] = { snrSum: 0, count: 0, bands: {} };
            }
            squareData[loc4].snrSum += spot.snr;
            squareData[loc4].count++;
            squareData[loc4].bands[spot.band] = (squareData[loc4].bands[spot.band] || 0) + 1;
        }
    });

    const fieldData = {};
    for (const loc4 in squareData) {
        const loc2 = loc4.substring(0, 2);
        if (!fieldData[loc2]) {
            fieldData[loc2] = [];
        }
        fieldData[loc2].push({ loc4, ...squareData[loc4] });
    }

    const AGGREGATION_THRESHOLD = 3;

    for (const loc2 in fieldData) {
        const squaresInField = fieldData[loc2];
        
        if (squaresInField.length > AGGREGATION_THRESHOLD) {
            let totalSnrSum = 0, totalCount = 0;
            const totalBands = {};
            squaresInField.forEach(sq => {
                totalSnrSum += sq.snrSum;
                totalCount += sq.count;
                for (const band in sq.bands) {
                    totalBands[band] = (totalBands[band] || 0) + sq.bands[band];
                }
            });

            const avgSnr = totalSnrSum / totalCount;
            let dominantBand = Object.keys(totalBands).reduce((a, b) => totalBands[a] > totalBands[b] ? a : b, 'all');
            const color = bandColors[dominantBand] || bandColors['all'];
            let opacity = (avgSnr >= 10) ? 0.9 : (avgSnr >= 0) ? 0.6 : 0.3;

            const bounds = fieldToBounds(loc2);
            if (bounds) L.rectangle(bounds, { color, weight: 1, fillColor: color, fillOpacity: opacity }).addTo(heatLayer);

        } else {
            squaresInField.forEach(sq => {
                const avgSnr = sq.snrSum / sq.count;
                let dominantBand = Object.keys(sq.bands).reduce((a, b) => sq.bands[a] > sq.bands[b] ? a : b, 'all');
                const color = bandColors[dominantBand] || bandColors['all'];
                let opacity = (avgSnr >= 10) ? 0.9 : (avgSnr >= 0) ? 0.6 : 0.3;
                const bounds = locatorToBounds(sq.loc4);
                if (bounds) L.rectangle(bounds, { color, weight: 1, fillColor: color, fillOpacity: opacity }).addTo(heatLayer);
            });
        }
    }
}

function renderLiveHeatmap(spots, maxMinutes) {
    const filter0dbEnabled = document.getElementById('filter-0db').checked;
    const selectedBand = getSelectedBand();

    const heatPoints = spots
        .filter(spot => {
            if (filter0dbEnabled && spot.snr <= 0) return false;
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