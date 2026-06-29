import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, getMinSnrMode, getSelectedBand, getEnabledBands, bandColors, locatorToBounds } from './utils.js';
import { endPerfTimer, incrementPerfCounter, isPerfProfilingEnabled, startPerfTimer } from './perf.js';

let lastRenderFingerprint = '';

// Add a single isolated-spot circle marker to the heat layer (shared by the
// clustered and non-clustered render paths so the style lives in one place).
function addSpotMarker(p, color) {
    L.circleMarker([p.geometry.coordinates[1], p.geometry.coordinates[0]], {
        color, fillColor: color, radius: 4.5, weight: 2,
        opacity: 0.65, fillOpacity: 0.5, interactive: false
    }).addTo(state.heatLayer);
}

function buildFilterCtx() {
    return {
        minSnrMode: getMinSnrMode(),
        ssbMinDb: parseInt(document.getElementById('ssb-min-db')?.value || '0', 10),
        cwMinDb: parseInt(document.getElementById('cw-min-db')?.value || '-15', 10),
        selectedBand: getSelectedBand(),
        enabledBands: getEnabledBands(),
    };
}

function buildRenderFingerprint(spots, filterCtx, style) {
    const spotKey = `${spots.length}:${spots[0]?.T ?? ''}:${spots[spots.length - 1]?.T ?? ''}`;
    const filterKey = `${filterCtx.minSnrMode}:${filterCtx.ssbMinDb}:${filterCtx.cwMinDb}:${filterCtx.selectedBand}:${[...filterCtx.enabledBands].sort().join(',')}:${style}`;
    return `${spotKey}|${filterKey}`;
}

function escapeHtml(value) {
    return String(value ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

function buildDxClusterHoverHtml(spot) {
    const sender = escapeHtml(spot?.sender || '—');
    const receiver = escapeHtml(spot?.receiver || '—');
    const band = escapeHtml(spot?.band || '—');
    const locator = escapeHtml(spot?.locator || '—');
    const reporterLocator = escapeHtml(spot?.reporterLocator || '—');
    const snr = Number.isFinite(Number(spot?.snr)) ? `${Number(spot.snr)} dB` : '—';

    return [
        '<strong>DXCluster Spot</strong>',
        `DX: ${receiver}`,
        `Spotter: ${sender}`,
        `Band: ${band}`,
        `SNR: ${snr}`,
        `DX Loc: ${locator}`,
        `Spotter Loc: ${reporterLocator}`
    ].join('<br>');
}

function splitSpotSources(spots) {
    const regularSpots = [];
    const dxClusterSpots = [];
    spots.forEach((spot) => {
        if (String(spot?.sourceType || '').toLowerCase() === 'dxcluster') {
            dxClusterSpots.push(spot);
        } else {
            regularSpots.push(spot);
        }
    });
    return { regularSpots, dxClusterSpots };
}

function renderDxClusterMarkers(dxClusterSpots) {
    if (!Array.isArray(dxClusterSpots) || dxClusterSpots.length === 0 || !state.heatLayer) return;

    // Item 4: single circleMarker per spot instead of two
    dxClusterSpots.forEach((spot) => {
        if (!Number.isFinite(spot.lat) || !Number.isFinite(spot.lng)) return;
        const color = bandColors[spot.band] || bandColors.all;
        const marker = L.circleMarker([spot.lat, spot.lng], {
            color,
            fillColor: color,
            radius: 4,
            weight: 2.2,
            opacity: 0.95,
            fillOpacity: 0.55,
            interactive: true,
            bubblingMouseEvents: false
        }).addTo(state.heatLayer);

        marker.bindTooltip(buildDxClusterHoverHtml(spot), {
            direction: 'top',
            offset: [0, -6],
            opacity: 0.95,
            sticky: true
        });
        marker.on('mouseover', () => {
            state.dxClusterHoverActive = true;
            const tooltip = document.getElementById('tooltip');
            if (tooltip) tooltip.style.display = 'none';
        });
        marker.on('mouseout', () => {
            state.dxClusterHoverActive = false;
        });
    });
}

export function updateMapVisualization(spots, maxMinutes) {
    if (!map) return;

    const perfEnabled = isPerfProfilingEnabled();
    const renderTimer = startPerfTimer();

    state.dxClusterHoverActive = false;

    // Item 3: read all filter/style state once
    const checkedStyleRadio = document.querySelector('input[name="style-select"]:checked');
    const style = checkedStyleRadio ? checkedStyleRadio.value : 'grid-snr';
    const filterCtx = buildFilterCtx();

    // Item 1: skip layer rebuild when spots and filters are unchanged
    const fingerprint = buildRenderFingerprint(spots, filterCtx, style);
    const skipRebuild = fingerprint === lastRenderFingerprint && state.heatLayer !== null;

    if (!skipRebuild) {
        lastRenderFingerprint = fingerprint;

        if (state.heatLayer) {
            map.removeLayer(state.heatLayer);
            incrementPerfCounter('mercator.layers.removed', 1);
        }

        let activeBands;
        switch (style) {
            case 'grid-snr':
                incrementPerfCounter('mercator.render.style.grid_snr', 1);
                activeBands = renderGridSnr(spots, maxMinutes, filterCtx);
                break;
            case 'active-area':
                incrementPerfCounter('mercator.render.style.active_area', 1);
                activeBands = renderActiveArea(spots, maxMinutes, filterCtx);
                break;
            default:
                incrementPerfCounter('mercator.render.style.grid_snr', 1);
                activeBands = renderGridSnr(spots, maxMinutes, filterCtx);
        }

        // Item 6: pass pre-computed activeBands to avoid re-scanning spots
        const bandLabelTimer = startPerfTimer();
        updateBandLabels(spots, filterCtx, activeBands);
        endPerfTimer('mercator.band_labels.total_ms', bandLabelTimer);
    }

    if (document.getElementById('auto-zoom')?.checked) {
        const now = Date.now();
        const interactionCooldownMs = 12000;
        const autoZoomMinIntervalMs = 5000;
        if ((now - (state.lastMercatorInteractionAt || 0)) < interactionCooldownMs) {
            if (perfEnabled) {
                endPerfTimer('mercator.render.total_ms', renderTimer);
            }
            return;
        }
        if ((now - (state.lastMercatorAutoZoomAt || 0)) < autoZoomMinIntervalMs) {
            if (perfEnabled) {
                endPerfTimer('mercator.render.total_ms', renderTimer);
            }
            return;
        }

        const autoZoomCalcTimer = startPerfTimer();
        // Item 3: use filterCtx instead of re-reading DOM
        const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;

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
        endPerfTimer('mercator.autozoom.bounds_calc_ms', autoZoomCalcTimer);

        if (found) {
            const fitBoundsTimer = startPerfTimer();
            const bounds = L.latLngBounds([minLat, minLng], [maxLat, maxLng]);
            const center = bounds.getCenter();
            const minBounds = center.toBounds(2000000); // minimum 2000km
            bounds.extend(minBounds);
            map.fitBounds(bounds, { padding: [20, 20], maxZoom: 5 });
            endPerfTimer('mercator.autozoom.fit_bounds_ms', fitBoundsTimer);
            incrementPerfCounter('mercator.autozoom.applied', 1);
            state.lastMercatorAutoZoomAt = now;
        }

        if (perfEnabled) {
            endPerfTimer('mercator.render.total_ms', renderTimer);
        }
        return;
    }

    if (perfEnabled) {
        endPerfTimer('mercator.render.total_ms', renderTimer);
    }
}

export function updateBandLabels(spots, filterCtx = null, activeBands = null) {
    // Item 3: use provided filterCtx or read from DOM once
    const ctx = filterCtx || buildFilterCtx();

    // Item 6: use pre-computed activeBands or scan spots once
    let bands = activeBands;
    if (!bands) {
        bands = new Set();
        spots.forEach(s => {
            if (ctx.minSnrMode === 'ssb' && s.snr < ctx.ssbMinDb) return;
            if (ctx.minSnrMode === 'cw' && s.snr < ctx.cwMinDb) return;
            if (!ctx.enabledBands.has(s.band)) return;
            bands.add(s.band);
        });
    }

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

        const isEnabled = ctx.enabledBands.has(radio.value);
        if (!isEnabled) {
            wrapper.style.opacity = '0.2';
            wrapper.style.filter = 'grayscale(100%)';
            return;
        }

        if (bands.has(radio.value)) {
            wrapper.style.opacity = '1';
            wrapper.style.filter = 'none';
        } else {
            wrapper.style.opacity = '0.4';
            wrapper.style.filter = 'grayscale(100%)';
        }
    });
}

function renderGridSnr(spots, maxMinutes, filterCtx) {
    const timer = startPerfTimer();
    state.heatLayer = L.layerGroup().addTo(map);
    incrementPerfCounter('mercator.layers.added', 1);

    // Item 3: use passed filterCtx instead of re-reading DOM
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;
    const { regularSpots, dxClusterSpots } = splitSpotSources(spots);
    const squareData = {};
    const res = getGridResolution();
    const aggregateTimer = startPerfTimer();

    regularSpots.forEach(spot => {
        let loc = spot.locator.substring(0, res);
        if (loc.length < res) loc = spot.locator.substring(0, 4); // Fallback if data is sparse

        if (loc.length >= 4) {
            if (!squareData[loc]) {
                squareData[loc] = { snrSum: 0, count: 0, maxSnr: -Infinity, visibleCount: 0, bands: {} };
            }
            squareData[loc].snrSum += spot.snr;
            squareData[loc].count++;
            squareData[loc].maxSnr = Math.max(squareData[loc].maxSnr, Number(spot.snr));
        }

        if (minSnrMode === 'ssb' && spot.snr < ssbMinDb) return;
        if (minSnrMode === 'cw' && spot.snr < cwMinDb) return;
        if (!enabledBands.has(spot.band)) return;
        if (selectedBand !== 'all' && spot.band !== selectedBand) return;

        if (loc.length >= 4) {
            squareData[loc].visibleCount++;
            squareData[loc].bands[spot.band] = (squareData[loc].bands[spot.band] || 0) + 1;
        }
    });
    endPerfTimer('mercator.grid.aggregate_ms', aggregateTimer);

    const drawTimer = startPerfTimer();
    // Item 6: collect activeBands during draw to avoid re-scanning in updateBandLabels
    const activeBands = new Set();
    // Item 2: accumulate GeoJSON features, then add as a single layer call
    const gridFeatures = [];

    for (let loc in squareData) {
        if (squareData[loc].visibleCount <= 0 || squareData[loc].count <= 0) continue;
        const maxSnr = Number.isFinite(squareData[loc].maxSnr)
            ? squareData[loc].maxSnr
            : (squareData[loc].snrSum / squareData[loc].count);

        let dominantBand = 'all';
        let maxCount = 0;
        for (let b in squareData[loc].bands) {
            if (squareData[loc].bands[b] > maxCount) {
                maxCount = squareData[loc].bands[b];
                dominantBand = b;
            }
            activeBands.add(b);
        }

        let color = bandColors[dominantBand] || bandColors['all'];

        let fillOpacity = 0.22; // Low intensity (< 0 dB)
        if (maxSnr >= 0 && maxSnr < 10) fillOpacity = 0.45; // Medium intensity (0 - 9 dB)
        else if (maxSnr >= 10) fillOpacity = 0.72; // High intensity (>= 10 dB)

        const bounds = locatorToBounds(loc);
        if (bounds) {
            const [[south, west], [north, east]] = bounds;
            gridFeatures.push({
                type: 'Feature',
                properties: { color, fillOpacity },
                geometry: {
                    type: 'Polygon',
                    coordinates: [[[west, south], [east, south], [east, north], [west, north], [west, south]]]
                }
            });
        }
    }

    // Item 2: single L.geoJSON call replaces N individual L.rectangle().addTo() calls
    if (gridFeatures.length) {
        L.geoJSON({ type: 'FeatureCollection', features: gridFeatures }, {
            style: f => ({
                color: f.properties.color,
                weight: 1,
                fillColor: f.properties.color,
                fillOpacity: f.properties.fillOpacity
            }),
            interactive: false
        }).addTo(state.heatLayer);
    }

    renderDxClusterMarkers(dxClusterSpots);

    endPerfTimer('mercator.grid.draw_ms', drawTimer);
    endPerfTimer('mercator.grid.total_ms', timer);
    incrementPerfCounter('mercator.grid.rectangles_added', gridFeatures.length);
    return activeBands;
}

function renderActiveArea(spots, maxMinutes, filterCtx) {
    const timer = startPerfTimer();
    state.heatLayer = L.layerGroup().addTo(map);
    incrementPerfCounter('mercator.layers.added', 1);

    // Item 3: use passed filterCtx instead of re-reading DOM
    const { minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands } = filterCtx;
    const { regularSpots, dxClusterSpots } = splitSpotSources(spots);
    let maxClusterDist = parseInt(document.getElementById('cluster-distance')?.value, 10);
    if (isNaN(maxClusterDist) || maxClusterDist < 100) maxClusterDist = 500;

    const pointsByBand = {};
    const seenCoordsByBand = {};
    const aggregateTimer = startPerfTimer();

    regularSpots.forEach(spot => {
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
    endPerfTimer('mercator.active_area.aggregate_ms', aggregateTimer);

    let polygonsAdded = 0;
    let markersAdded = 0;
    const clusterTimer = startPerfTimer();

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
                            style: { color: color, weight: 1, opacity: 0.9, fillColor: color, fillOpacity: 0.18 },
                            interactive: false
                        }).addTo(state.heatLayer);
                        polygonsAdded += 1;
                    }
                } else {
                    clusterPts.forEach(p => isolatedPts.push(p));
                }
            }

            isolatedPts.forEach(p => {
                addSpotMarker(p, color);
                markersAdded += 1;
            });
        } else {
            pts.forEach(p => {
                addSpotMarker(p, color);
                markersAdded += 1;
            });
        }
    }

    renderDxClusterMarkers(dxClusterSpots);

    endPerfTimer('mercator.active_area.cluster_draw_ms', clusterTimer);
    endPerfTimer('mercator.active_area.total_ms', timer);
    incrementPerfCounter('mercator.active_area.polygons_added', polygonsAdded);
    incrementPerfCounter('mercator.active_area.markers_added', markersAdded);
    // Item 6: return bands with data for updateBandLabels
    return new Set(Object.keys(pointsByBand));
}