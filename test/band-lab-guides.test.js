import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
    computeScatterData,
    formatWindowMinutes,
    getSnrThresholdsDb,
    initBandLab,
    snrGuideSpecs,
} from '../static/band-lab.js';
import { state } from '../static/state.js';

describe('snrGuideSpecs', () => {
    it('labels the guides with the SSB and CW thresholds', () => {
        const guides = snrGuideSpecs({ ssbMinDb: -3, cwMinDb: -18 });
        expect(guides.map((g) => [g.snr, g.label])).toEqual([
            [-3, 'SSB -3 dB'],
            [-18, 'CW -18 dB'],
        ]);
    });

    it('falls back to the slider defaults', () => {
        expect(snrGuideSpecs().map((g) => g.label)).toEqual(['SSB 0 dB', 'CW -15 dB']);
        expect(snrGuideSpecs({ ssbMinDb: Number.NaN }).map((g) => g.label)).toEqual(['SSB 0 dB', 'CW -15 dB']);
    });
});

describe('computeScatterData guide range', () => {
    const center = { lat: 50, lng: 0 };
    const points = [{ lat: 51, lng: 1, snr: 5 }];

    it('widens the SNR axis to keep thresholds outside -20..20 dB visible', () => {
        const out = computeScatterData(points, center, 0, null, [8, -28]);
        expect(out.minSnr).toBe(-28);
        expect(out.maxSnr).toBe(20);
        const high = computeScatterData(points, center, 0, null, [26, -15]);
        expect(high.maxSnr).toBe(26);
    });

    it('keeps the default floors for thresholds inside the range', () => {
        const out = computeScatterData(points, center, 0, null, [0, -15]);
        expect(out.minSnr).toBe(-20);
        expect(out.maxSnr).toBe(20);
    });
});

describe('formatWindowMinutes', () => {
    it('uses min below an hour and h for whole hours', () => {
        expect(formatWindowMinutes(15)).toBe('15 min');
        expect(formatWindowMinutes(30)).toBe('30 min');
        expect(formatWindowMinutes(60)).toBe('1 h');
        expect(formatWindowMinutes(120)).toBe('2 h');
        expect(formatWindowMinutes(90)).toBe('90 min');
    });
});

describe('getSnrThresholdsDb', () => {
    afterEach(() => {
        delete window.__horstUiStore;
        document.body.innerHTML = '';
    });

    it('uses the slider defaults when nothing is mounted', () => {
        document.body.innerHTML = '';
        expect(getSnrThresholdsDb()).toEqual({ ssbMinDb: 0, cwMinDb: -15 });
    });

    it('reads a mounted slider first and the store for the unmounted one', () => {
        // Only the active mode's slider is mounted (src/SnrThresholds.svelte).
        document.body.innerHTML = '<input type="range" id="cw-min-db" min="-30" max="10" value="-12">';
        window.__horstUiStore = {
            subscribe(fn) {
                fn({ ssbMinDb: -4, cwMinDb: -20 });
                return () => {};
            },
        };
        expect(getSnrThresholdsDb()).toEqual({ ssbMinDb: -4, cwMinDb: -12 });
    });
});

// Node's own (flag-gated) localStorage global shadows jsdom's here, so install
// a Map-backed one like the app.* tests do.
function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(String(key)) ? store.get(String(key)) : null),
        setItem: (key, value) => { store.set(String(key), String(value)); },
        removeItem: (key) => { store.delete(String(key)); },
        clear: () => { store.clear(); },
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
}

// Drawing through the real module with a recording canvas context.
function makeRecordingContext() {
    const target = { texts: [] };
    return new Proxy(target, {
        get(obj, prop) {
            if (prop === 'fillText') {
                return (text, x, y) => obj.texts.push({ text: String(text), x, y, textAlign: obj.textAlign || 'left' });
            }
            if (prop === 'clearRect') {
                return () => { obj.texts.length = 0; };
            }
            if (prop in obj) return obj[prop];
            return () => {};
        },
        set(obj, prop, value) {
            obj[prop] = value;
            return true;
        },
    });
}

function mountFixture({ ssb, cw }) {
    document.body.innerHTML = `
        <div id="band-lab-window" class="band-lab-window is-hidden">
            <select id="band-lab-time-range"><option value="15">15 min</option></select>
            <div id="band-lab-content">
                <div id="band-lab-summary"></div>
                <div id="band-lab-cards"></div>
            </div>
        </div>
        <input id="qth" value="JO62">
        <div id="band-container">
            <input type="checkbox" class="band-enable" value="20m" checked>
        </div>
        <input type="radio" name="min-snr" value="none" checked>
        <input type="range" id="ssb-min-db" min="-30" max="10" value="${ssb}">
        <input type="range" id="cw-min-db" min="-30" max="10" value="${cw}">
    `;
}

function scatterLabels() {
    const canvas = document.getElementById('band-lab-scatter-20m');
    return canvas.__ctx.texts;
}

describe('Distance vs SNR guides', () => {
    beforeEach(() => {
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
        vi.setSystemTime(new Date('2026-09-25T12:00:00Z'));
        installLocalStorageMock();
        localStorage.setItem('bandLabEnabled', 'true');
        state.liveSpots = [
            { band: '20m', lat: 40, lng: -74, snr: -8, ageSeconds: 30 },
            { band: '20m', lat: 48, lng: 2, snr: 4, ageSeconds: 60 },
            { band: '20m', lat: 55, lng: 37, snr: -16, ageSeconds: 90 },
        ];
        vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function getContext() {
            if (!this.__ctx) this.__ctx = makeRecordingContext();
            return this.__ctx;
        });
        vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ status: 'grey', bands: [] }) })));
    });

    afterEach(async () => {
        // Let the dx_conditions fetch settle (it redraws the cards) while the
        // canvas mock is still installed.
        for (let i = 0; i < 20; i += 1) await Promise.resolve();
        vi.useRealTimers();
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('draws the SSB and CW thresholds set under Display', () => {
        mountFixture({ ssb: -5, cw: -20 });
        initBandLab();

        const labels = scatterLabels().map((t) => t.text);
        expect(labels).toContain('SSB -5 dB');
        expect(labels).toContain('CW -20 dB');
        expect(labels.some((t) => /phone|cw -15/.test(t))).toBe(false);
    });

    it('moves a colliding label to the right edge', () => {
        mountFixture({ ssb: -10, cw: -10 });
        initBandLab();

        const guides = scatterLabels().filter((t) => t.text === 'SSB -10 dB' || t.text === 'CW -10 dB');
        expect(guides.map((t) => t.textAlign)).toEqual(['left', 'right']);
    });

    it('redraws the guides when a threshold slider changes', async () => {
        mountFixture({ ssb: 0, cw: -15 });
        initBandLab();
        expect(scatterLabels().map((t) => t.text)).toContain('SSB 0 dB');

        const slider = document.getElementById('ssb-min-db');
        slider.value = '-7';
        slider.dispatchEvent(new Event('input', { bubbles: true }));
        // Within the update throttle the change is deferred, not dropped.
        await vi.advanceTimersByTimeAsync(300);

        const labels = scatterLabels().map((t) => t.text);
        expect(labels).toContain('SSB -7 dB');
        expect(labels).not.toContain('SSB 0 dB');
        expect(labels).toContain('CW -15 dB');
    });

    it('labels the activity window in minutes', () => {
        mountFixture({ ssb: 0, cw: -15 });
        initBandLab();
        const canvas = document.getElementById('band-lab-activity-20m');
        expect(canvas.__ctx.texts.map((t) => t.text)).toContain('Last 15 min');
    });
});
