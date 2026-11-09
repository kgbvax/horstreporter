import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { attachSmoothWheelZoom } from '../static/wheel-zoom.js';

// Minimal point with the methods the handler uses.
function pt(x, y) {
    return {
        x, y,
        subtract: (o) => pt(x - o.x, y - o.y),
        divideBy: (n) => pt(x / n, y / n),
    };
}

function makeMap({ zoom = 3, min = 1, max = 10 } = {}) {
    const listeners = {};
    const handlers = {};
    const el = {
        addEventListener: vi.fn((type, fn) => { listeners[type] = fn; }),
        removeEventListener: vi.fn((type) => { delete listeners[type]; }),
    };
    const events = [];
    const map = {
        _zoom: zoom,
        _animatingZoom: false,
        options: { maxBounds: null },
        getContainer: () => el,
        getZoom: () => map._zoom,
        getSize: () => pt(800, 600),
        mouseEventToContainerPoint: (e) => pt(e.clientX, e.clientY),
        containerPointToLatLng: (p) => ({ p }),
        project: () => pt(0, 0),
        unproject: () => ({ lat: 0, lng: 0 }),
        _limitZoom: (z) => Math.max(min, Math.min(max, z)),
        _limitCenter: (c) => c,
        _stop: vi.fn(),
        _moveStart: vi.fn(() => events.push('start')),
        _moveEnd: vi.fn(() => events.push('end')),
        _move: vi.fn((c, z) => { map._zoom = z; events.push('move'); }),
        on: vi.fn((types, fn) => types.split(' ').forEach(t => { handlers[t] = fn; })),
        off: vi.fn(),
    };
    return { map, el, events, handlers, wheel: (e) => listeners.wheel(e) };
}

function wheelEvent(deltaY, extra = {}) {
    return {
        deltaY, deltaMode: 0, clientX: 100, clientY: 100, ctrlKey: false,
        preventDefault: vi.fn(), stopPropagation: vi.fn(), ...extra,
    };
}

describe('attachSmoothWheelZoom', () => {
    let now;
    let frames;

    beforeEach(() => {
        now = 0;
        frames = [];
        vi.spyOn(performance, 'now').mockImplementation(() => now);
        vi.stubGlobal('requestAnimationFrame', (fn) => { frames.push(fn); return frames.length; });
        vi.stubGlobal('cancelAnimationFrame', () => { frames = []; });
    });
    afterEach(() => {
        vi.restoreAllMocks();
        vi.unstubAllGlobals();
    });

    // Run animation frames 16 ms apart until none are queued.
    function runFrames(limit = 500) {
        for (let i = 0; i < limit && frames.length; i++) {
            now += 16;
            const queued = frames;
            frames = [];
            queued.forEach(fn => fn(now));
        }
    }

    it('returns a no-op when the container cannot listen', () => {
        const detach = attachSmoothWheelZoom({ getContainer: () => ({ style: {} }) });
        expect(typeof detach).toBe('function');
        expect(() => detach()).not.toThrow();
    });

    it('fires one zoomstart and one zoomend for a stream of wheel events', () => {
        const { map, events, wheel } = makeMap({ zoom: 3 });
        attachSmoothWheelZoom(map, { pxPerLevel: 100 });
        for (let i = 0; i < 20; i++) {
            wheel(wheelEvent(-10));
            now += 16;
            const queued = frames; frames = []; queued.forEach(fn => fn(now));
        }
        runFrames();
        expect(map._moveStart).toHaveBeenCalledTimes(1);
        expect(map._moveEnd).toHaveBeenCalledTimes(1);
        expect(events[0]).toBe('start');
        expect(events[events.length - 1]).toBe('end');
        // 20 events x 10 px at 100 px/level = +2 levels, no delta lost.
        expect(map._zoom).toBeCloseTo(5, 6);
    });

    it('moves the zoom continuously, with no frame standing still mid-gesture', () => {
        const { map, wheel } = makeMap({ zoom: 3 });
        attachSmoothWheelZoom(map, { pxPerLevel: 100 });
        wheel(wheelEvent(-100));
        const zooms = [];
        for (let i = 0; i < 10; i++) {
            now += 16;
            const queued = frames; frames = []; queued.forEach(fn => fn(now));
            zooms.push(map._zoom);
        }
        for (let i = 1; i < zooms.length; i++) expect(zooms[i]).toBeGreaterThan(zooms[i - 1]);
    });

    it('clamps the target zoom to the map limits', () => {
        const { map, wheel } = makeMap({ zoom: 3, min: 2 });
        attachSmoothWheelZoom(map, { pxPerLevel: 10 });
        wheel(wheelEvent(150));
        runFrames();
        expect(map._zoom).toBe(2);
    });

    it('ends the gesture when a drag or an animated zoom takes over', () => {
        const { map, handlers, wheel } = makeMap();
        attachSmoothWheelZoom(map);
        wheel(wheelEvent(-50));
        handlers.zoomanim();
        expect(map._moveEnd).toHaveBeenCalledTimes(1);
        expect(frames.length).toBe(0);
    });

    it('ignores the wheel while a +/- zoom animation runs', () => {
        const { map, wheel } = makeMap();
        attachSmoothWheelZoom(map);
        map._animatingZoom = true;
        const e = wheelEvent(-50);
        wheel(e);
        expect(e.preventDefault).toHaveBeenCalled();
        expect(map._moveStart).not.toHaveBeenCalled();
    });

    it('detach removes the listener and ends a running gesture', () => {
        const { map, el, wheel } = makeMap();
        const detach = attachSmoothWheelZoom(map);
        wheel(wheelEvent(-50));
        detach();
        expect(el.removeEventListener).toHaveBeenCalledWith('wheel', expect.any(Function), { passive: false });
        expect(map._moveEnd).toHaveBeenCalledTimes(1);
    });
});
