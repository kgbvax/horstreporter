// Continuous wheel / trackpad zoom for the Mercator (Leaflet) map.
//
// Why this exists: Leaflet 1.9.4's ScrollWheelZoom batches wheel events
// (wheelDebounceTime) and plays each batch as its own 250 ms CSS zoom
// animation with an ease-out curve. A batch that arrives while an animation
// runs is dropped (_tryAnimatedZoom returns early while _animatingZoom is
// set). A trackpad sends a continuous stream, so the user saw: ease out,
// stop, start the next hop, with about 9 of 10 batches lost. Option tuning
// (zoomSnap, wheelPxPerZoomLevel, wheelDebounceTime) cannot fix that.
//
// This handler uses the same private calls as Leaflet's own TouchZoom
// (pinch): _moveStart once, _move(..., {pinch: true}) every animation frame,
// _moveEnd once. Every wheel event moves a target zoom, and the view
// approaches that target exponentially, so no input is lost and the speed
// never drops to zero between events. zoomstart / zoomend fire once per
// gesture, so the app's interaction gate, the canvas redraws and the DXCC
// label rebuild run once instead of once per 25 ms batch.
//
// Depends on Leaflet privates (_move, _moveStart, _moveEnd, _limitZoom,
// _limitCenter, _stop, _animatingZoom). Re-check them on a Leaflet upgrade.

// Pixel scale per deltaMode (0 = pixels, 1 = lines, 2 = pages).
const DELTA_MODE_PX = [1, 20, 60];

export function attachSmoothWheelZoom(map, {
    pxPerLevel = 300,      // wheel / trackpad scroll pixels per zoom level
    pinchPxPerLevel = 100, // ctrlKey wheel events (macOS trackpad pinch) are smaller
    maxDeltaPx = 150,      // tame accelerated or high-resolution wheels
    tauMs = 70,            // time constant of the approach to the target zoom
    idleMs = 120,          // gesture ends this long after the last wheel event
} = {}) {
    const el = typeof map?.getContainer === 'function' ? map.getContainer() : null;
    if (!el || typeof el.addEventListener !== 'function') return () => {};

    let active = false;
    let raf = 0;
    let target = 0;
    let anchorPoint = null;  // container point under the cursor
    let anchorLatLng = null; // the lat/lng that must stay under it
    let lastFrame = 0;
    let lastInput = 0;

    function containerPoint(e) {
        if (typeof map.mouseEventToContainerPoint === 'function') {
            return map.mouseEventToContainerPoint(e);
        }
        const r = el.getBoundingClientRect();
        return L.point(e.clientX - r.left - el.clientLeft, e.clientY - r.top - el.clientTop);
    }

    function onWheel(e) {
        e.preventDefault();
        e.stopPropagation();
        const raw = e.deltaY * (DELTA_MODE_PX[e.deltaMode] || 1);
        const dy = Math.max(-maxDeltaPx, Math.min(maxDeltaPx, raw));
        if (!dy) return;
        if (!active) {
            // Let a +/- button or keyboard zoom animation finish first.
            if (map._animatingZoom) return;
            active = true;
            map._stop();
            target = map.getZoom();
            lastFrame = performance.now();
            map._moveStart(true, false); // one zoomstart + movestart per gesture
        }
        target = map._limitZoom(target - dy / (e.ctrlKey ? pinchPxPerLevel : pxPerLevel));
        anchorPoint = containerPoint(e);
        anchorLatLng = map.containerPointToLatLng(anchorPoint);
        lastInput = performance.now();
        if (!raf) raf = requestAnimationFrame(step);
    }

    function step(now) {
        raf = 0;
        if (!active) return;
        const dt = Math.min(64, Math.max(0, now - lastFrame));
        lastFrame = now;
        const zoom = map.getZoom();
        const diff = target - zoom;
        const next = Math.abs(diff) < 1e-3 ? target : zoom + diff * (1 - Math.exp(-dt / tauMs));
        if (next !== zoom) {
            const half = map.getSize().divideBy(2);
            let center = map.unproject(
                map.project(anchorLatLng, next).subtract(anchorPoint.subtract(half)),
                next
            );
            center = map._limitCenter(center, next, map.options.maxBounds);
            map._move(center, next, { pinch: true, round: false });
        }
        if (next !== target || now - lastInput < idleMs) {
            raf = requestAnimationFrame(step);
        } else {
            finish();
        }
    }

    function finish() {
        if (raf) {
            cancelAnimationFrame(raf);
            raf = 0;
        }
        if (!active) return;
        active = false;
        map._moveEnd(true); // one zoomend + moveend per gesture
    }

    el.addEventListener('wheel', onWheel, { passive: false });
    // A drag, or a +/- / keyboard zoom animation, takes over the view.
    map.on('dragstart zoomanim', finish);

    return () => {
        el.removeEventListener('wheel', onWheel, { passive: false });
        map.off('dragstart zoomanim', finish);
        finish();
    };
}
