import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, getMinSnrMode, getSelectedBand, getEnabledBands, bandColors, locatorToBounds } from './utils.js';

export function updateMapVisualization(spots, maxMinutes) {
    if (!map) return;

    if (state.heatLayer) map.removeLayer(state.heatLayer);
    
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
            const center = bounds.getCenter();
            const minBounds = center.toBounds(2000000); // minimum 2000km
            bounds.extend(minBounds);
            map.fitBounds(bounds, { padding: [20, 20], maxZoom: 5 });
        }
    }
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
        const wrapper = radio.closest('.band-wrapper');
        if (!wrapper) return;
        
        if (radio.checked) {
            wrapper.style.borderColor = 'var(--text-color)';
        } else {
            wrapper.style.borderColor = 'transparent';
        }

        if (radio.value === 'all') return;
        
        const isEnabled = enabledBands.has(radio.value);
        if (!isEnabled) {
            wrapper.style.opacity = '0.2';
            wrapper.style.filter = 'grayscale(100%)';
            return;
        }

        if (activeBands.has(radio.value)) {
            wrapper.style.opacity = '1';
            wrapper.style.filter = 'none';
        } else {
            wrapper.style.opacity = '0.4';
            wrapper.style.filter = 'grayscale(100%)';
        }
    });
}

function renderGridSnr(spots, maxMinutes) {
    state.heatLayer = L.layerGroup().addTo(map);

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
            }).addTo(state.heatLayer);
        }
    }
}

function renderActiveArea(spots, maxMinutes) {
    state.heatLayer = L.layerGroup().addTo(map);

    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();
    let maxClusterDist = parseInt(document.getElementById('cluster-distance')?.value, 10);
    if (isNaN(maxClusterDist) || maxClusterDist < 100) maxClusterDist = 500;

    const pointsByBand = {};
    const seenCoordsByBand = {};

    spots.forEach(spot => {
        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        if (!pointsByBand[spot.band]) {
            pointsByBand[spot.band] = [];
            seenCoordsByBand[spot.band] = new Set();
        }
        
        const coordKey = `${spot.lng},${spot.lat}`;
        if (!seenCoordsByBand[spot.band].has(coordKey)) {
            seenCoordsByBand[spot.band].add(coordKey);
            pointsByBand[spot.band].push(turf.point([spot.lng, spot.lat]));
        }
    });

    for (const band in pointsByBand) {
        const pts = pointsByBand[band];
        const color = bandColors[band] || bandColors['all'];
        
        if (pts.length >= 3) {
            const fc = turf.featureCollection(pts);
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
                    let hull;
                    try {
                        // Attempt to create a concave hull (which supports indentations and holes).
                        // If the points are too sparse to form a valid concave hull, fallback to convex.
                        hull = turf.concave(clusterFc, { maxEdge: maxClusterDist * 1.5, units: 'kilometers' }) || turf.convex(clusterFc);
                    } catch (e) {
                        hull = turf.convex(clusterFc);
                    }
                    if (hull) {
                        let finalShape = hull;
                        try {
                            finalShape = turf.polygonSmooth(hull, { iterations: 2 });
                        } catch (e) {
                            console.error("Error smoothing polygon", e);
                        }
                        L.geoJSON(finalShape, {
                            style: { color: color, weight: 1, opacity: 0.8, fillColor: color, fillOpacity: 0.2 },
                            interactive: false
                        }).addTo(state.heatLayer);
                    }
                } else {
                    clusterPts.forEach(p => isolatedPts.push(p));
                }
            }

            isolatedPts.forEach(p => {
                L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
                    color: color, radius: 5, weight: 2, fillOpacity: 0.5, interactive: false
                }).addTo(state.heatLayer);
            });
        } else {
            pts.forEach(p => {
                L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
                    color: color, radius: 5, weight: 2, fillOpacity: 0.5, interactive: false
                }).addTo(state.heatLayer);
            });
        }
    }
}

function renderLiveHeatmap(spots, maxMinutes) {
    state.heatLayer = L.layerGroup().addTo(map);

    const minSnrMode = getMinSnrMode();
    const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
    const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
    const selectedBand = getSelectedBand();
    const enabledBands = getEnabledBands();

    const bandsToRender = selectedBand === 'all' ? Array.from(enabledBands) : [selectedBand];

    bandsToRender.forEach(band => {
        const heatPoints = spots
            .filter(spot => {
                if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return false;
                if (minSnrMode === 'cw' && spot.snr < cwMinDb) return false;
                if (!enabledBands.has(spot.band)) return false;
                if (spot.band !== band) return false;
                return true;
            })
            .map(spot => {
                const intensity = Math.max(0, Math.min(1, (spot.snr + 20) / 40));
                return [spot.lat, spot.lng, intensity];
            });

        if (heatPoints.length > 0 && typeof L.heatLayer === 'function') {
            const color = bandColors[band] || bandColors['all'];
            const gradient = { 0.4: color, 0.8: color, 1.0: 'white' };
            
            L.heatLayer(heatPoints, {
                radius: 25,
                blur: 15,
                maxZoom: 10,
                max: 1.0,
                minOpacity: 0.2,
                gradient: gradient
            }).addTo(state.heatLayer);
        }
    });
}