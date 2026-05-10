const map = L.map('map').setView([20, 0], 2);
L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
}).addTo(map);

let heatLayer = null;
let eventSource = null;
let liveSpots = [];
let renderInterval = null;

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

document.getElementById('filter-0db').addEventListener('change', () => {
    const minutes = document.getElementById('minutes').value || 15;
    renderHeatmap(liveSpots, parseInt(minutes));
});

document.getElementById('band-container').addEventListener('change', () => {
    const minutes = document.getElementById('minutes').value || 15;
    renderHeatmap(liveSpots, parseInt(minutes));
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
        btn.innerHTML = 'Cycle Active';
    } else {
        btn.innerHTML = 'Stop Cycling';
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

        renderHeatmap(liveSpots, parseInt(minutes));
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
            renderHeatmap(liveSpots, parseInt(minutes));
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

function renderHeatmap(spots, maxMinutes) {
    if (heatLayer) map.removeLayer(heatLayer);
    heatLayer = L.layerGroup().addTo(map);

    updateBandLabels(spots);

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