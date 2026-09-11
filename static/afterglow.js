// afterglow.js — Time Travel's decaying spot overlay. A single canvas over
// the map (Mercator: absolute-positioned sibling of the Leaflet panes;
// Azimuthal: not used — the azimuth runtime renders its own scene, and the
// timeline moments feed it through the normal path).
//
// Visual model (the spec's decay = data-time decision):
//   - Each spot's alpha = exp(-(playhead - t) / TAU) while inside the moment
//     window, hard-cut at the 15-min window edge.
//   - While the playhead MOVES (playing or scrubbing), alpha decays in
//     data-time: a band opening floods the map, a closing band's glow dies.
//   - While the playhead STANDS STILL, alpha is pure age-graded (no decay) —
//     identical to the live map's look, so a paused moment reads as "the
//     situation at T", not "a decaying memory".
//   - Fade-in: newly-entered spots ramp to full alpha over FADE_IN_MS of
//     wall-clock, so animation doesn't pop.

import { MOMENT_WINDOW_SECONDS } from './timeline.js';

// Decay time constant in data-time seconds. 5 min per the spec: a spot at the
// window edge (15 min old) is at e^-3 ≈ 5% — visually gone before the cut.
const TAU_SECONDS = 5 * 60;

// Fade-in ramp (wall-clock ms) for spots newly entering the window.
const FADE_IN_MS = 300;

// How long a spot keeps "entering" state (its fade-in window in data-time):
// a spot enters the fade when its data-age drops below this many seconds.
const FADE_IN_DATA_SECONDS = 60;

// Playhead movement threshold (data-seconds per wall-second) above which the
// playhead counts as "moving" (decay active). Stationary = pure age alpha.
const MOVE_THRESHOLD = 0.01;

let canvas = null;
let ctx = null;

function ensureCanvas() {
    if (canvas && document.body.contains(canvas)) return canvas;
    canvas = document.createElement('canvas');
    canvas.className = 'afterglow-canvas';
    // The map container (Mercator: #map; its panes carry the tiles).
    const host = document.getElementById('map');
    if (!host) return null;
    host.appendChild(canvas);
    ctx = canvas.getContext('2d');
    return canvas;
}

function resizeCanvas() {
    if (!ensureCanvas() || !canvas) return false;
    const host = canvas.parentElement;
    const w = host?.clientWidth || 0;
    const h = host?.clientHeight || 0;
    const dpr = window.devicePixelRatio || 1;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
        canvas.width = Math.round(w * dpr);
        canvas.height = Math.round(h * dpr);
        canvas.style.width = `${w}px`;
        canvas.style.height = `${h}px`;
    }
    return true;
}

// bandColors: the live map's band palette (utils.bandColors is an object —
// band → color). Imported lazily to keep afterglow.js free of eager imports.
let bandColorsMap = null;
async function loadBandColors() {
    if (bandColorsMap) return bandColorsMap;
    try {
        const mod = await import('./utils.js');
        bandColorsMap = mod.bandColors || {};
    } catch (_) {
        bandColorsMap = {};
    }
    return bandColorsMap;
}

// alphaFor computes a spot's alpha at playhead T given movement state.
// (spot.t is data-time; T the playhead. moving=true → exponential decay.)
export function alphaFor(spot, T, moving, nowMs) {
    const age = Math.max(0, T - spot.t);
    if (age > MOMENT_WINDOW_SECONDS) return 0; // hard cut at the window edge
    let base;
    if (moving) {
        base = Math.exp(-age / TAU_SECONDS);
        // Fade-in ramp for freshly arrived spots (data-age within the ramp).
        if (age < FADE_IN_DATA_SECONDS) {
            base *= Math.max(0.15, age / FADE_IN_DATA_SECONDS);
        }
    } else {
        // Stationary: pure age-graded, matching the live map's look.
        base = 1 - age / MOMENT_WINDOW_SECONDS;
        base = Math.max(0, Math.min(1, 0.35 + 0.65 * base));
    }
    return base;
}

// drawAfterglow paints the overlay for the current moment. Called from the
// timeline moment subscription with the LIVE-shaped spots (ageSeconds relative
// to the playhead) plus the raw playhead.
let lastSpots = [];
let lastPlayhead = 0;
let lastMoveAt = 0;
let rafPending = 0;

function scheduleRedraw() {
    if (rafPending) return;
    rafPending = requestAnimationFrame(() => {
        rafPending = 0;
        draw();
    });
}

function draw() {
    if (!isMercatorActive()) return;
    if (!resizeCanvas() || !ctx) return;
    const host = canvas.parentElement;
    const dpr = window.devicePixelRatio || 1;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, host.clientWidth, host.clientHeight);

    const T = lastPlayhead;
    const nowMs = performance.now();
    const moving = (nowMs - lastMoveAt) < 400; // decay while recently moved
    const colors = bandColorsMap || {};

    const project = mapPointProjector();
    if (!project) return;

    for (const s of lastSpots) {
        const a = alphaFor({ t: T - (s.ageSeconds || 0) }, T, moving, nowMs);
        if (a <= 0.01) continue;
        const p = project(s.lat, s.lng);
        if (!p) continue;
        const color = colors[String(s.band || '').toLowerCase()] || colors.all || '#4aa3ff';
        ctx.beginPath();
        ctx.arc(p.x, p.y, 5.5, 0, Math.PI * 2);
        ctx.fillStyle = color;
        ctx.globalAlpha = a * 0.55;
        ctx.fill();
        // Hot core for fresh spots (data-age < 2 min) while moving — the
        // "just happened" cue.
        if ((s.ageSeconds || 0) < 120 && moving) {
            ctx.globalAlpha = a * 0.9;
            ctx.beginPath();
            ctx.arc(p.x, p.y, 2.5, 0, Math.PI * 2);
            ctx.fillStyle = '#ffffff';
            ctx.fill();
        }
    }
    ctx.globalAlpha = 1;
}

// mapPointProjector returns (lat, lng) -> {x, y} in CSS pixels for the active
// Mercator map, re-computed per draw (map may have moved).
function mapPointProjector() {
    try {
        // map is imported from map.js (same singleton the app uses).
        const m = getMapInstance();
        if (!m) return null;
        return (lat, lng) => {
            const p = m.latLngToContainerPoint([lat, lng]);
            return { x: p.x, y: p.y };
        };
    } catch (_) {
        return null;
    }
}

// getMapInstance lazy-imports the Leaflet map singleton (avoids a circular
// import with map.js at module load; map.js imports renderers.js which may
// import this module in later phases).
let mapInstance = null;
async function loadMapInstance() {
    if (mapInstance) return mapInstance;
    try {
        const mod = await import('./map.js');
        mapInstance = mod.map;
    } catch (_) {
        mapInstance = null;
    }
    return mapInstance;
}
function getMapInstance() {
    return mapInstance;
}

function isMercatorActive() {
    const proj = document.querySelector('input[name="projection-select"]:checked')?.value || 'mercator';
    return proj === 'mercator';
}

// --- Public API ----------------------------------------------------------

// updateAfterglow(spotArray, playhead, moved) — the timeline moment listener.
// moved=true marks the playhead as having just moved (decay active).
export function updateAfterglow(spots, playhead, moved = false) {
    lastSpots = spots || [];
    lastPlayhead = playhead;
    if (moved) lastMoveAt = performance.now();
    // Kick the async band-color + map loads once; every call after is sync.
    if (!bandColorsMap) { void loadBandColors().then(scheduleRedraw); return; }
    if (!mapInstance) { void loadMapInstance().then(scheduleRedraw); return; }
    scheduleRedraw();
}

// notifyMapMoved forces a redraw after pan/zoom (positions are projected per
// draw, so a plain schedule is enough; this also clears the cached key).
export function notifyMapMoved() {
    if (!isMercatorActive()) return;
    scheduleRedraw();
}

// hideAfterglow clears the overlay (mode exit).
export function hideAfterglow() {
    lastSpots = [];
    if (canvas) {
        const c = ctx;
        if (c) {
            const host = canvas.parentElement;
            c.setTransform(1, 0, 0, 1, 0, 0);
            c.clearRect(0, 0, canvas.width, canvas.height || (host?.clientHeight || 0));
        }
    }
}

// Exported for tests.
export const __internals = {
    alphaFor,
    TAU_SECONDS,
    FADE_IN_MS,
    ensureCanvas,
    resizeCanvas,
};

export { FADE_IN_MS, TAU_SECONDS };