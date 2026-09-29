import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../static/band-lab.js', () => ({ setBandLabVisible: vi.fn() }));
vi.mock('../static/wspr-matrix.js', () => ({ setWsprMatrixVisible: vi.fn() }));
vi.mock('../static/almanac.js', () => ({ setAlmanacVisible: vi.fn() }));
vi.mock('../static/cq-flag.js', () => ({ isChaseQueueEnabled: vi.fn(() => false) }));

import { initCondDock, migrateLegacyState, __test } from '../static/cond-dock.js';
import { setBandLabVisible } from '../static/band-lab.js';
import { setWsprMatrixVisible } from '../static/wspr-matrix.js';
import { setAlmanacVisible } from '../static/almanac.js';
import { isChaseQueueEnabled } from '../static/cq-flag.js';

// Node's own localStorage global shadows jsdom's; use a Map-backed one.
function installLocalStorageMock(initial = {}) {
    const store = new Map(Object.entries(initial));
    const mock = {
        getItem: (k) => (store.has(String(k)) ? store.get(String(k)) : null),
        setItem: (k, v) => { store.set(String(k), String(v)); },
        removeItem: (k) => { store.delete(String(k)); },
        clear: () => store.clear(),
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    return mock;
}

function mountDom() {
    document.body.innerHTML = `
        <input id="qth" value="jo32">
        <div id="cond-collapsed"><button id="cond-open" class="panel-toggle"></button></div>
        <aside id="cond-dock" class="cond-dock is-hidden">
            <div id="cond-dock-resize"></div>
            <button id="cond-tab-conditions"><span id="cond-qth"></span></button>
            <button id="cond-tab-chase" hidden></button>
            <button id="cond-close"></button>
            <div id="cond-conditions">
                <div id="cond-horizon">
                    <input type="radio" name="cond-horizon" value="now" checked>
                    <input type="radio" name="cond-horizon" value="usual">
                </div>
                <div id="cond-now"></div>
                <div id="cond-typical" class="is-hidden"></div>
            </div>
            <div id="cond-chase" class="is-hidden"></div>
        </aside>`;
}

const click = (id) => document.getElementById(id).click();
const hidden = (id) => document.getElementById(id).classList.contains('is-hidden');

describe('migrateLegacyState', () => {
    it('opens the dock on Now when a Now-side panel was on, then drops the old keys', () => {
        const ls = installLocalStorageMock({ bandLabEnabled: 'true', almanacEnabled: 'true', wsprMatrixPos: '{}', almanacPos: '{}', bandLabWindowSize: '{}' });
        migrateLegacyState(ls);
        expect(ls.getItem('condDockOpen')).toBe('true');
        expect(ls.getItem('condHorizon')).toBe('now');
        for (const k of ['bandLabEnabled', 'wsprMatrixEnabled', 'almanacEnabled', 'wsprMatrixPos', 'almanacPos', 'bandLabWindowSize']) {
            expect(ls.getItem(k)).toBe(null);
        }
    });

    it('picks Typical when only the almanac was on', () => {
        const ls = installLocalStorageMock({ almanacEnabled: 'true' });
        migrateLegacyState(ls);
        expect(ls.getItem('condDockOpen')).toBe('true');
        expect(ls.getItem('condHorizon')).toBe('usual');
    });

    it('leaves the dock closed when nothing was on, and never overrides a saved choice', () => {
        const none = installLocalStorageMock({ bandLabEnabled: 'false' });
        migrateLegacyState(none);
        expect(none.getItem('condDockOpen')).toBe(null);
        const saved = installLocalStorageMock({ condDockOpen: 'false', wsprMatrixEnabled: 'true' });
        migrateLegacyState(saved);
        expect(saved.getItem('condDockOpen')).toBe('false');
    });
});

describe('cond dock', () => {
    let ls;
    beforeEach(() => {
        ls = installLocalStorageMock();
        mountDom();
        __test.runtime.initialized = false; // fresh DOM needs fresh listeners
        vi.clearAllMocks();
        isChaseQueueEnabled.mockReturnValue(false);
    });
    afterEach(() => {
        delete window.__horstChaseQueue;
    });

    it('starts closed with the collapsed row showing and no data pipeline running', () => {
        const onLayoutChange = vi.fn();
        initCondDock({ onLayoutChange });
        expect(hidden('cond-dock')).toBe(true);
        expect(hidden('cond-collapsed')).toBe(false);
        expect(setBandLabVisible).toHaveBeenLastCalledWith(false);
        expect(setWsprMatrixVisible).toHaveBeenLastCalledWith(false);
        expect(setAlmanacVisible).toHaveBeenLastCalledWith(false);
        expect(onLayoutChange).toHaveBeenCalled();
        expect(document.getElementById('cond-qth').textContent).toBe('JO32');
    });

    it('opens on Now: band stats and matrix run, almanac stays off; persists the choice', () => {
        const onLayoutChange = vi.fn();
        initCondDock({ onLayoutChange });
        onLayoutChange.mockClear();
        click('cond-open');
        expect(hidden('cond-dock')).toBe(false);
        expect(hidden('cond-collapsed')).toBe(true);
        expect(document.getElementById('cond-open').getAttribute('aria-pressed')).toBe('true');
        expect(setBandLabVisible).toHaveBeenLastCalledWith(true);
        expect(setWsprMatrixVisible).toHaveBeenLastCalledWith(true);
        expect(setAlmanacVisible).toHaveBeenLastCalledWith(false);
        expect(ls.getItem(__test.OPEN_KEY)).toBe('true');
        expect(onLayoutChange).toHaveBeenCalledTimes(1);
    });

    it('switches horizon: Typical starts the almanac and stops Now; persists the horizon', () => {
        initCondDock();
        click('cond-open');
        const usual = document.querySelector('input[value="usual"]');
        usual.checked = true;
        usual.dispatchEvent(new Event('change', { bubbles: true }));
        expect(hidden('cond-now')).toBe(true);
        expect(hidden('cond-typical')).toBe(false);
        expect(setBandLabVisible).toHaveBeenLastCalledWith(false);
        expect(setWsprMatrixVisible).toHaveBeenLastCalledWith(false);
        expect(setAlmanacVisible).toHaveBeenLastCalledWith(true);
        expect(ls.getItem(__test.HORIZON_KEY)).toBe('usual');
    });

    it('restores the saved state on the next load', () => {
        ls.setItem('condDockOpen', 'true');
        ls.setItem('condHorizon', 'usual');
        initCondDock();
        expect(hidden('cond-dock')).toBe(false);
        expect(document.querySelector('input[value="usual"]').checked).toBe(true);
        expect(setAlmanacVisible).toHaveBeenLastCalledWith(true);
    });

    it('closing stops every pipeline and shows the collapsed row again', () => {
        initCondDock();
        click('cond-open');
        click('cond-close');
        expect(hidden('cond-dock')).toBe(true);
        expect(hidden('cond-collapsed')).toBe(false);
        expect(setBandLabVisible).toHaveBeenLastCalledWith(false);
        expect(setWsprMatrixVisible).toHaveBeenLastCalledWith(false);
        expect(setAlmanacVisible).toHaveBeenLastCalledWith(false);
    });

    it('moves focus into the dock on open and back to the toggle on close', () => {
        initCondDock();
        click('cond-open');
        expect(document.activeElement.id).toBe('cond-tab-conditions');
        click('cond-close');
        expect(document.activeElement.id).toBe('cond-open');
    });

    it('keeps the locator label current, also after a programmatic change and submit', () => {
        document.body.insertAdjacentHTML('beforeend', '<form id="fetch-form"></form>');
        vi.useFakeTimers();
        initCondDock();
        document.getElementById('qth').value = 'jo62wa';
        document.getElementById('fetch-form').dispatchEvent(new Event('submit'));
        vi.runAllTimers();
        vi.useRealTimers();
        expect(document.getElementById('cond-qth').textContent).toBe('JO62WA');
    });

    it('hides the Chase queue tab unless it is enabled', () => {
        initCondDock();
        expect(document.getElementById('cond-tab-chase').hidden).toBe(true);
    });

    it('with the Chase queue enabled its tab swaps the pane and drives the queue', () => {
        isChaseQueueEnabled.mockReturnValue(true);
        const setVisible = vi.fn();
        window.__horstChaseQueue = { setVisible };
        initCondDock();
        expect(document.getElementById('cond-tab-chase').hidden).toBe(false);
        click('cond-open');
        click('cond-tab-chase');
        expect(hidden('cond-conditions')).toBe(true);
        expect(hidden('cond-chase')).toBe(false);
        expect(setVisible).toHaveBeenLastCalledWith(true);
        expect(setBandLabVisible).toHaveBeenLastCalledWith(false);
        click('cond-tab-conditions');
        expect(setVisible).toHaveBeenLastCalledWith(false);
        expect(setBandLabVisible).toHaveBeenLastCalledWith(true);
    });
});
