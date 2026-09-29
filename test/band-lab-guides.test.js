import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
    computeScatterData,
    formatWindowMinutes,
    getSnrThresholdsDb,
    initBandLab,
    setBandLabVisible,
    snrGuideSpecs,
    subscribeBandRows,
    drawBandMiniPlot,
    getBandNormalRate,
    getBandRow,
    miniSnrAxis,
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

// Drawing through the real module with a recording canvas context: every
// dashed stroke is recorded with the y it was drawn at.
function makeRecordingContext() {
    const target = { dashedYs: [], dots: 0, dotFills: [], dash: [], lastY: 0 };
    return new Proxy(target, {
        get(obj, prop) {
            if (prop === 'setLineDash') return (d) => { obj.dash = d; };
            if (prop === 'moveTo') return (x, y) => { obj.lastY = y; };
            if (prop === 'stroke') {
                return () => { if (obj.dash.length) obj.dashedYs.push(obj.lastY); };
            }
            if (prop === 'arc') return () => { obj.dots += 1; obj.dotFills.push(obj.fillStyle); };
            if (prop === 'clearRect') return () => { obj.dashedYs.length = 0; obj.dots = 0; obj.dotFills.length = 0; };
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
        <select id="band-lab-time-range"><option value="15">15 min</option></select>
        <div id="band-lab-content">
            <div id="band-lab-summary"></div>
        </div>
        <canvas id="mini" class="cond-mini" data-band="20m" width="96" height="44"></canvas>
        <input id="qth" value="JO62">
        <div id="band-container">
            <input type="checkbox" class="band-enable" value="20m" checked>
        </div>
        <input type="radio" name="min-snr" value="none" checked>
        <input type="range" id="ssb-min-db" min="-30" max="10" value="${ssb}">
        <input type="range" id="cw-min-db" min="-30" max="10" value="${cw}">
    `;
}

// Mini-plot geometry (default 96x44 canvas, pad t=3 b=3): fixed -25..+10 dB.
const yOfSnr = (snr) => 3 + 38 - ((snr + 25) / 35) * 38;

function guideYs() {
    return document.getElementById('mini').__ctx.dashedYs;
}

describe('miniSnrAxis', () => {
    const grouped = (...snrs) => new Map([['20m', snrs.map((snr) => ({ snr }))]]);

    it('spans -25..+10 dB whatever the filter', () => {
        expect(miniSnrAxis(grouped(-20, 4))).toEqual({ lo: -25, hi: 10 });
        expect(miniSnrAxis(new Map())).toEqual({ lo: -25, hi: 10 });
    });

    it('raises the top to the strongest report, rounded to 5 dB and capped at 30', () => {
        expect(miniSnrAxis(grouped(17)).hi).toBe(20);
        expect(miniSnrAxis(grouped(44)).hi).toBe(30);
    });
});

describe('Mini plot guides', () => {
    let unsubscribe;

    beforeEach(() => {
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
        vi.setSystemTime(new Date('2026-09-25T12:00:00Z'));
        installLocalStorageMock();
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
        // Let the dx_conditions fetch settle while the canvas mock is installed.
        for (let i = 0; i < 20; i += 1) await Promise.resolve();
        unsubscribe?.();
        setBandLabVisible(false);
        vi.useRealTimers();
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    function start(thresholds) {
        mountFixture(thresholds);
        initBandLab();
        unsubscribe = subscribeBandRows(() => drawBandMiniPlot(document.getElementById('mini'), '20m'));
        setBandLabVisible(true);
    }

    it('draws the SSB and CW thresholds set under Display on the fixed axis', () => {
        start({ ssb: -5, cw: -20 });
        const ys = guideYs();
        expect(ys).toHaveLength(2);
        expect(ys[0]).toBeCloseTo(yOfSnr(-5), 5);
        expect(ys[1]).toBeCloseTo(yOfSnr(-20), 5);
    });

    it('omits a threshold that lies below the fixed axis', () => {
        start({ ssb: 10, cw: -30 });
        const ys = guideYs();
        expect(ys).toHaveLength(1);
        expect(ys[0]).toBeCloseTo(yOfSnr(10), 5);
    });

    it('redraws the guides when a threshold slider changes', async () => {
        start({ ssb: 0, cw: -15 });
        expect(guideYs()[0]).toBeCloseTo(yOfSnr(0), 5);

        const slider = document.getElementById('ssb-min-db');
        slider.value = '-7';
        slider.dispatchEvent(new Event('input', { bubbles: true }));
        // Within the update throttle the change is deferred, not dropped.
        await vi.advanceTimersByTimeAsync(300);

        const ys = guideYs();
        expect(ys[0]).toBeCloseTo(yOfSnr(-7), 5);
        expect(ys[1]).toBeCloseTo(yOfSnr(-15), 5);
    });

    it('plots in-range reports as dots and pins the p95 outlier to a chevron', () => {
        start({ ssb: 0, cw: -15 });
        // New York is beyond the p95 distance cap of the three reports; Paris
        // and Moscow are dots.
        expect(document.getElementById('mini').__ctx.dots).toBe(2);
    });

    it('exposes the band normal rate and row model once dx_conditions has landed', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => ({
            ok: true,
            json: async () => ({
                status: 'green',
                bands: [{ band: '20m', activity_level: 'above', baseline_activity: 2, baseline_local_scale: 0.5, regional_spots: 30, regional_expected: 12 }],
            }),
        })));
        // A qth of its own: the module keeps the last dx_conditions response per key.
        mountFixture({ ssb: 0, cw: -15 });
        document.getElementById('qth').value = 'JO64';
        initBandLab();
        unsubscribe = subscribeBandRows(() => {});
        setBandLabVisible(true);
        // Before dx arrives: no normal, and the row says dx is not ready.
        expect(getBandNormalRate('20m')).toBe(null);
        expect(getBandRow('20m')).toMatchObject({ dxReady: false, reports: 3, metrics: null });
        await vi.advanceTimersByTimeAsync(0);
        // The region baseline scaled to your squares' share.
        expect(getBandNormalRate('20m')).toBeCloseTo(1, 5);
        expect(getBandRow('20m')).toMatchObject({ dxReady: true, metrics: { activity_level: 'above', regional_spots: 30 } });
        expect(getBandNormalRate('40m')).toBe(null);
    });

    it('drops the snapshot when the Now view stops', async () => {
        start({ ssb: 0, cw: -15 });
        expect(getBandRow('20m')).not.toBe(null);
        setBandLabVisible(false);
        expect(getBandRow('20m')).toBe(null);
        expect(getBandNormalRate('20m')).toBe(null);
    });

    it('draws the same reports whatever the filter and grays those below the floor', async () => {
        // No floor: every report in the band color.
        start({ ssb: 0, cw: -15 });
        const fills = () => document.getElementById('mini').__ctx.dotFills;
        expect(fills()).toHaveLength(2);
        expect(new Set(fills()).size).toBe(1);
        const bandColored = fills()[0];

        // SSB floor at 0 dB: the -8 dB and -16 dB reports fall below it. Same
        // dots, same places, but the ones below the floor are gray.
        document.querySelector('input[name="min-snr"]').insertAdjacentHTML('afterend', '<input type="radio" name="min-snr" value="ssb" checked>');
        document.querySelector('input[value="none"]').checked = false;
        const slider = document.getElementById('ssb-min-db');
        slider.dispatchEvent(new Event('input', { bubbles: true }));
        await vi.advanceTimersByTimeAsync(300);
        expect(fills()).toHaveLength(2);
        // Paris (+4 dB) stays in the band color, Moscow (-16 dB) is gray.
        expect(fills().filter((f) => f === bandColored)).toHaveLength(1);
        expect(fills().filter((f) => f !== bandColored)).toHaveLength(1);
    });
});
