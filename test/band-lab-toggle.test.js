import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { initBandLab } from '../static/band-lab.js';
import { setPanelToggleState } from '../static/panel-toggle.js';
import { state } from '../static/state.js';

// The Band stats toggle behaves like the Propagation toggle: it stays visible
// while the panel is open, shows the on state (.is-active), exposes
// aria-pressed and titles the next action.

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

describe('setPanelToggleState', () => {
    it('sets the on look, aria-pressed and the next-action title', () => {
        const btn = document.createElement('button');
        const labels = { show: 'Show x', hide: 'Hide x' };
        setPanelToggleState(btn, true, labels);
        expect(btn.classList.contains('is-active')).toBe(true);
        expect(btn.getAttribute('aria-pressed')).toBe('true');
        expect(btn.title).toBe('Hide x');
        setPanelToggleState(btn, false, labels);
        expect(btn.classList.contains('is-active')).toBe(false);
        expect(btn.getAttribute('aria-pressed')).toBe('false');
        expect(btn.title).toBe('Show x');
    });

    it('ignores a missing button', () => {
        expect(() => setPanelToggleState(null, true, { show: 'a', hide: 'b' })).not.toThrow();
    });
});

describe('Band stats toggle', () => {
    beforeEach(() => {
        installLocalStorageMock();
        state.liveSpots = [];
        vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
        vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ bands: [] }) })));
        document.body.innerHTML = `
            <div id="band-lab-window" class="band-lab-window is-hidden">
                <button id="band-lab-window-close" type="button">×</button>
                <div id="band-lab-content">
                    <div id="band-lab-summary"></div>
                    <div id="band-lab-cards"></div>
                </div>
            </div>
            <button id="band-stats-toggle" type="button" class="panel-toggle" title="Show band stats" aria-pressed="false">Band stats</button>
            <input id="qth" value="JO62">
        `;
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('stays visible and reports its state through open and close', () => {
        initBandLab();
        const toggle = document.getElementById('band-stats-toggle');
        const windowEl = document.getElementById('band-lab-window');
        const expectState = (on) => {
            expect(windowEl.classList.contains('is-hidden')).toBe(!on);
            expect(toggle.style.display).toBe('');
            expect(toggle.classList.contains('is-active')).toBe(on);
            expect(toggle.getAttribute('aria-pressed')).toBe(String(on));
            expect(toggle.title).toBe(on ? 'Hide band stats' : 'Show band stats');
        };

        expectState(false);
        toggle.click();
        expectState(true);
        expect(localStorage.getItem('bandLabEnabled')).toBe('true');
        toggle.click();
        expectState(false);

        // The panel's own close button resets the toggle the same way.
        toggle.click();
        expectState(true);
        document.getElementById('band-lab-window-close').click();
        expectState(false);
        expect(localStorage.getItem('bandLabEnabled')).toBe('false');
    });
});
