import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { buildGlobalDecision, initBandLab, updateBandLab } from '../static/band-lab.js';
import { state } from '../static/state.js';

const GOOD = { label: 'Good: worth turning the radio on', className: 'is-go' };
const FAIR = { label: 'Fair: worth monitoring', className: 'is-watch' };
const POOR = { label: 'Poor: low payoff now', className: 'is-wait' };
const NO_DATA = { label: 'Not enough data yet', className: 'is-wait' };

describe('buildGlobalDecision (Band stats verdict)', () => {
    it('maps the backend overall status to one verdict', () => {
        expect(buildGlobalDecision('green', 0.8, 2)).toEqual(GOOD);
        expect(buildGlobalDecision('yellow', 0.8, 2)).toEqual(FAIR);
        expect(buildGlobalDecision('red', 0.8, 2)).toEqual(POOR);
    });

    it('needs a recommended enabled band for the go verdict', () => {
        expect(buildGlobalDecision('green', 0.9, 0)).toEqual(FAIR);
        // Recommended bands do not lift yellow or red.
        expect(buildGlobalDecision('yellow', 0.9, 0)).toEqual(FAIR);
        expect(buildGlobalDecision('red', 0.9, 0)).toEqual(POOR);
    });

    it('says there is not enough data below 40% confidence, whatever the status', () => {
        for (const status of ['green', 'yellow', 'red', 'grey']) {
            expect(buildGlobalDecision(status, 0.39, 3)).toEqual(NO_DATA);
            expect(buildGlobalDecision(status, 0, 3)).toEqual(NO_DATA);
        }
        expect(buildGlobalDecision('green', Number.NaN, 3)).toEqual(NO_DATA);
        // The gate is inclusive at 40%.
        expect(buildGlobalDecision('green', 0.4, 1)).toEqual(GOOD);
    });

    it('treats grey, missing or unknown status as not enough data', () => {
        expect(buildGlobalDecision('grey', 0.9, 3)).toEqual(NO_DATA);
        expect(buildGlobalDecision(undefined, 0.9, 3)).toEqual(NO_DATA);
        expect(buildGlobalDecision('purple', 0.9, 3)).toEqual(NO_DATA);
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

// Rendering the summary through the real module: DOM fixture + mocked fetch.
function mountFixture(qth) {
    document.body.innerHTML = `
        <div id="band-lab-window" class="band-lab-window is-hidden">
            <select id="band-lab-time-range"><option value="15">15 min</option></select>
            <div id="band-lab-content">
                <div id="band-lab-summary"></div>
                <div id="band-lab-cards"></div>
            </div>
        </div>
        <button id="band-stats-toggle" type="button">Band stats</button>
        <input id="qth" value="${qth}">
        <div id="band-container">
            <input type="checkbox" class="band-enable" value="20m" checked>
            <input type="checkbox" class="band-enable" value="15m" checked>
            <input type="checkbox" class="band-enable" value="10m">
        </div>
        <input type="radio" name="min-snr" value="none" checked>
    `;
}

async function flushMicrotasks() {
    for (let i = 0; i < 20; i += 1) await Promise.resolve();
}

describe('Band stats summary', () => {
    let payload;

    beforeEach(() => {
        installLocalStorageMock();
        localStorage.setItem('bandLabEnabled', 'true');
        state.liveSpots = [];
        vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
        vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => payload })));
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('shows one verdict from the backend status, without a separate condition line', async () => {
        payload = {
            status: 'yellow',
            condition: 'Fair',
            overall_score: 48.2,
            confidence: 72,
            bands: [
                { band: '10m', status: 'green' },
                { band: '20m', status: 'yellow' },
                { band: '15m', status: 'red' },
            ],
        };
        mountFixture('JO62');
        initBandLab();
        await flushMicrotasks();

        const summary = document.getElementById('band-lab-summary');
        const badge = summary.querySelector('.band-lab-decision-badge');
        expect(badge.textContent).toBe('Fair: worth monitoring');
        expect(badge.classList.contains('is-watch')).toBe(true);
        expect(summary.querySelector('.band-lab-confidence').textContent).toBe('Confidence 72%');
        expect(summary.textContent).toContain('Score: 48.2');
        expect(summary.textContent).not.toContain('Condition');
        expect(summary.textContent).not.toContain('Decision');
        // 10m is disabled, so it is not offered as a best band.
        expect(summary.querySelector('.band-lab-summary-reco').textContent).toBe('Best now: 20m');
    });

    it('says there is not enough data at low confidence and when no band stands out', async () => {
        payload = {
            status: 'red',
            condition: 'Poor',
            overall_score: 12,
            confidence: 20,
            bands: [],
        };
        mountFixture('JO63');
        initBandLab();
        await flushMicrotasks();

        const summary = document.getElementById('band-lab-summary');
        const badge = summary.querySelector('.band-lab-decision-badge');
        expect(badge.textContent).toBe('Not enough data yet');
        expect(badge.classList.contains('is-wait')).toBe(true);
        expect(summary.querySelector('.band-lab-summary-reco').textContent).toBe('No clear best band yet');
    });

    it('asks for a locator when none is set', () => {
        mountFixture('');
        updateBandLab({ force: true });
        expect(document.getElementById('band-lab-summary').textContent).toBe('Enter your locator to see band conditions.');
    });
});
