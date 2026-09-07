// video-stage.js — the clean render surface horstvideo captures frame by
// frame. Deliberately a SEPARATE boot from app.js (which is not import-safe:
// importing it runs the full UI boot + SSE). This page mounts the minimum
// shared machinery — map, Mercator spot renderer, Band Stats panel, the
// replay-state flag — and exposes a tiny driver API:
//
//   window.__horstVideo.ready       true once tiles/layout/panel are up
//   window.__horstVideo.stepTo(t)   install bucket ending at unix t, rerender,
//                                   flip frameDone when the paint settled
//
// All spot data comes from /api/replay/spots (QTH-prefiltered archive reads);
// the Band Stats panel evaluates /api/dx_conditions with bucket_end because
// state.timeTravel.active is kept true for the whole session.
import { state } from './state.js';
import { initMap, setTheme, syncMercatorGraylineLayer, syncMercatorCountryLayer, currentTileLayer, map } from './map.js';
import { updateMapVisualization } from './renderers.js';
import { initBandLab, updateBandLab } from './band-lab.js';
import { bandColors, locatorToBounds } from './utils.js';

const params = new URLSearchParams(location.search);
const cfg = {
    qth: (params.get('qth') || '').toUpperCase(),
    start: parseInt(params.get('start') || '0', 10),
    end: parseInt(params.get('end') || '0', 10),
    step: parseInt(params.get('step') || '120', 10),
    zoom: parseFloat(params.get('zoom') || '5'),
    lat: parseFloat(params.get('lat') || '52.5'),
    lng: parseFloat(params.get('lng') || '13.4'),
    bands: (params.get('bands') || '').split(',').map((b) => b.trim()).filter(Boolean),
    surroundings: params.get('surroundings') === 'true',
};

// Replay mode from the very first frame: band-lab passes bucket_end to
// /api/dx_conditions and overlayNowMs() (grayline cache key) follows the
// simulated clock via currentBucketEnd.
state.timeTravel.active = true;
state.timeTravel.bucketSeconds = cfg.step;
state.timeTravel.currentBucketEnd = cfg.start || Math.floor(Date.now() / 1000);

const theme = localStorage.getItem('theme') || 'dark';
document.body.dataset.theme = theme;

// --- filter inputs the shared modules read from the DOM ---------------------

document.getElementById('qth').value = cfg.qth;
if (cfg.surroundings) document.getElementById('surroundings').checked = true;

// Band-enable checkboxes: no .band-enable checkboxes means getEnabledBands()
// returns an empty Set and NOTHING renders, so mount the full canonical list
// checked according to the driver config.
const bandContainer = document.getElementById('band-container');
for (const band of Object.keys(bandColors)) {
    if (band === 'all') continue;
    const label = document.createElement('label');
    label.style.display = 'block';
    label.textContent = band;
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.className = 'band-enable';
    cb.value = band;
    cb.checked = cfg.bands.length === 0 || cfg.bands.includes(band);
    label.prepend(cb);
    bandContainer.appendChild(label);
}

// --- map + panel boot --------------------------------------------------------

// No explicit camera ("current map view" unchecked) => center on the QTH
// grid square instead of the driver's fallback (0,0 = Gulf of Guinea).
if (cfg.lat === 0 && cfg.lng === 0 && cfg.qth) {
    const b = locatorToBounds(cfg.qth);
    if (b) {
        cfg.lat = (b[0][0] + b[1][0]) / 2;
        cfg.lng = (b[0][1] + b[1][1]) / 2;
    }
}

initMap([cfg.lat, cfg.lng], cfg.zoom);
// initMap hardcodes a zoom control; the stage is a render surface.
map.zoomControl?.remove();

const themeApplied = setTheme(theme);
syncMercatorCountryLayer({ enabled: true });
syncMercatorGraylineLayer({ force: true });

// The Band Stats module gates on this localStorage flag before init.
localStorage.setItem('bandLabEnabled', 'true');
initBandLab({});

// --- driver API --------------------------------------------------------------

function waitTiles(timeoutMs = 20000) {
    return new Promise((resolve) => {
        // currentTileLayer is a live binding reassigned by setTheme.
        const layer = currentTileLayer;
        if (!layer) return resolve();
        const done = () => { layer.off('load', done); resolve(); };
        layer.on('load', done);
        setTimeout(done, timeoutMs);
    });
}

async function installBucket(bucketEnd) {
    const params2 = new URLSearchParams();
    params2.set('qth', cfg.qth);
    if (cfg.surroundings) params2.set('surroundings', 'true');
    params2.set('bucket_end', String(bucketEnd));
    params2.set('bucket_seconds', String(cfg.step));
    if (cfg.bands.length) params2.set('enabled_bands', cfg.bands.join(','));
    const res = await fetch(`/api/replay/spots?${params2.toString()}`);
    if (!res.ok) throw new Error(`replay/spots ${res.status}`);
    const json = await res.json();
    state.liveSpots = (json.spots || []).map((spot) => ({
        ...spot,
        __replay: true,
        __bucketEnd: bucketEnd,
    }));
    state.timeTravel.currentBucketEnd = bucketEnd;
}

async function renderFrame() {
    updateMapVisualization(state.liveSpots, 15);
    // Band Stats re-summarizes after an async /api/dx_conditions fetch — the
    // frame is only done once that promise landed, else clips capture the
    // "Updating baseline and trend…" placeholder.
    await updateBandLab({ force: true });
    // The grayline layer is key-cached on the 5-min overlay clock; during
    // replay that clock is the current bucket, so this only rebuilds when the
    // terminator actually moved.
    void syncMercatorGraylineLayer();
}

async function boot() {
    await themeApplied;
    if (!cfg.qth) {
        document.body.innerHTML = '<p style="padding:2em">video-stage requires a qth parameter</p>';
        return;
    }
    await new Promise((resolve) => map.whenReady(resolve));
    await waitTiles();
    await installBucket(state.timeTravel.currentBucketEnd);
    await renderFrame();
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    window.__horstVideo.ready = true;
}

const driver = {
    ready: false,
    frameDone: true,
    async stepTo(bucketEnd) {
        driver.frameDone = false;
        try {
            await installBucket(bucketEnd);
            await renderFrame();
            // Two rAFs = both map (Leaflet) and canvas (Band Stats) paints
            // have hit the compositor.
            await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
        } catch (err) {
            console.error('stepTo failed', err);
        } finally {
            driver.frameDone = true;
        }
    },
};
window.__horstVideo = driver;

boot();