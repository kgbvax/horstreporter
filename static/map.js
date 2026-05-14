export let map;
export let currentTileLayer = null;

const lightTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

const darkTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

export function initMap(initialCenter, initialZoom) {
    map = L.map('map', {
        zoomSnap: 0.25,
        zoomDelta: 0.25
    }).setView(initialCenter, initialZoom);

    map.on('moveend', () => {
        const center = map.getCenter();
        localStorage.setItem('mapCenter', JSON.stringify([center.lat, center.lng]));
    });

    map.on('zoomend', () => {
        localStorage.setItem('mapZoom', map.getZoom());
    });

    return map;
}

export function setTheme(theme) {
    document.body.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);

    if (currentTileLayer && map) map.removeLayer(currentTileLayer);
    currentTileLayer = theme === 'dark' ? darkTileLayer : lightTileLayer;
    
    const toggleBtn = document.getElementById('theme-toggle');
    if (toggleBtn) {
        toggleBtn.innerHTML = theme === 'dark' ? '<i class="fas fa-sun"></i>' : '<i class="fas fa-moon"></i>';
        toggleBtn.title = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
    }
    
    if (map) currentTileLayer.addTo(map);
}