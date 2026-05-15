export let map;
export let currentTileLayer = null;
export let currentGeoJsonLayer = null;
export let worldGeoJsonData = null;

const lightTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

const darkTileLayer = L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 18,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>'
});

export async function loadWorldGeoJson() {
    if (!worldGeoJsonData) {
        try {
            const response = await fetch('vendor/world.geojson');
            worldGeoJsonData = await response.json();
        } catch (e) {
            console.error("Failed to load world.geojson", e);
        }
    }
    return worldGeoJsonData;
}

export function initMap(initialCenter, initialZoom, projection = 'mercator') {
    if (map) {
        map.remove();
        map = null;
    }

    let crs = L.CRS.EPSG3857;
    if (projection === 'azimuthal') {
        const proj4String = `+proj=aeqd +lat_0=${initialCenter[0]} +lon_0=${initialCenter[1]} +x_0=0 +y_0=0 +ellps=WGS84 +datum=WGS84 +units=m +no_defs`;
        crs = new L.Proj.CRS('EPSG:9999', proj4String, {
            resolutions: [
                156543.03392804097, 78271.51696402048, 39135.75848201024,
                19567.87924100512, 9783.93962050256, 4891.96981025128,
                2445.98490512564, 1222.99245256282, 611.49622628141,
                305.748113140705, 152.8740565703525, 76.43702828517625,
                38.21851414258813, 19.109257071294063, 9.554628535647032,
                4.777314267823516, 2.388657133911758, 1.194328566955879,
                0.5971642834779395
            ]
        });
    }

    map = L.map('map', {
        zoomSnap: 0.25,
        zoomDelta: 0.25,
        zoomControl: false,
        crs: crs,
        worldCopyJump: projection === 'mercator'
    }).setView(initialCenter, initialZoom);

    L.control.zoom({ position: 'bottomright' }).addTo(map);

    map.on('moveend', () => {
        const center = map.getCenter();
        localStorage.setItem('mapCenter', JSON.stringify([center.lat, center.lng]));
    });

    map.on('zoomend', () => {
        localStorage.setItem('mapZoom', map.getZoom());
    });

    return map;
}

export function setTheme(theme, projection = 'mercator') {
    document.body.setAttribute('data-theme', theme);
    localStorage.setItem('theme', theme);

    if (currentTileLayer && map) {
        map.removeLayer(currentTileLayer);
        currentTileLayer = null;
    }
    if (currentGeoJsonLayer && map) {
        map.removeLayer(currentGeoJsonLayer);
        currentGeoJsonLayer = null;
    }

    if (projection === 'mercator') {
        currentTileLayer = theme === 'dark' ? darkTileLayer : lightTileLayer;
        if (map) {
            currentTileLayer.addTo(map);
            map.getContainer().style.background = ''; // Revert to CSS default
        }
    } else if (projection === 'azimuthal' && worldGeoJsonData) {
        const borderColor = theme === 'dark' ? '#444444' : '#aaaaaa';
        const fillColor = theme === 'dark' ? '#222222' : '#ffffff';
        const bgColor = theme === 'dark' ? '#111111' : '#e2e8f0'; // Blue-grey ocean
        
        let processedGeoJson = worldGeoJsonData;
        if (map && typeof turf !== 'undefined') {
            const center = map.getCenter();
            let lng = center.lng % 360;
            if (lng > 180) lng -= 360;
            if (lng < -180) lng += 360;
            
            let antLng = lng > 0 ? lng - 180 : lng + 180;
            const EPSILON = 0.00001; // Avoid strict intersection float glitches
            
            // Slice polygons dynamically at the projection's anti-meridian to prevent streaking
            if (antLng > -179.99 && antLng < 179.99) {
                const box1 = [-180, -90, antLng - EPSILON, 90];
                const box2 = [antLng + EPSILON, -90, 180, 90];
                const clippedFeatures = [];
                
                worldGeoJsonData.features.forEach(f => {
                    if (!f.geometry || !f.geometry.coordinates) return clippedFeatures.push(f);
                    const bbox = turf.bbox(f);
                    if (bbox[0] < antLng && bbox[2] > antLng) { // Only cut countries that physically cross the tear line
                        try {
                            const c1 = turf.bboxClip(f, box1);
                            if (c1 && c1.geometry && c1.geometry.coordinates.length > 0) clippedFeatures.push(c1);
                            const c2 = turf.bboxClip(f, box2);
                            if (c2 && c2.geometry && c2.geometry.coordinates.length > 0) clippedFeatures.push(c2);
                        } catch (e) {
                            clippedFeatures.push(f);
                        }
                    } else {
                        clippedFeatures.push(f);
                    }
                });
                processedGeoJson = { type: 'FeatureCollection', features: clippedFeatures };
            }
        }

        currentGeoJsonLayer = L.geoJSON(processedGeoJson, {
            style: { color: borderColor, weight: 1, fill: true, fillColor: fillColor, fillOpacity: 1 },
            interactive: false,
            noClip: true,
            smoothFactor: 0
        });
        if (map) {
            currentGeoJsonLayer.addTo(map);
            map.getContainer().style.background = bgColor;
        }
    }
    
    const toggleBtn = document.getElementById('theme-toggle');
    if (toggleBtn) {
        toggleBtn.innerHTML = theme === 'dark' ? '<i class="fas fa-sun"></i>' : '<i class="fas fa-moon"></i>';
        toggleBtn.title = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
    }
}