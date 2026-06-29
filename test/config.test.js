import { beforeEach, describe, expect, it, vi } from 'vitest';
import { loadConfig } from '../static/config.js';

function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => { store.set(String(key), String(value)); },
        removeItem: (key) => { store.delete(String(key)); },
        clear: () => { store.clear(); }
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    if (typeof window !== 'undefined') {
        Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    }
}

// Regression coverage for the production console errors:
//   - "Error parsing saved form state" caused by null .value access
//   - TypeError: null is not an object (#target / #minutes)
// loadConfig must tolerate missing DOM elements silently.

describe('loadConfig defensive behavior', () => {
    let errSpy;

    beforeEach(() => {
        document.body.innerHTML = '';
        installLocalStorageMock();
        localStorage.clear();
        errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    });

    it('does not throw on a completely empty DOM and returns defaults', () => {
        const result = loadConfig();
        expect(result).toEqual({ initialCenter: [20, 0], initialZoom: 2 });
        expect(errSpy).not.toHaveBeenCalled();
    });

    it('does not log an error when #target is missing but a saved target exists', () => {
        localStorage.setItem('target', 'JO32');
        document.body.innerHTML = '<input id="minutes" />';
        expect(() => loadConfig()).not.toThrow();
        expect(errSpy).not.toHaveBeenCalled();
    });

    it('does not log an error when #minutes is missing but a saved value exists', () => {
        localStorage.setItem('minutes', '30');
        document.body.innerHTML = '<input id="target" />';
        expect(() => loadConfig()).not.toThrow();
        expect(errSpy).not.toHaveBeenCalled();
    });

    it('does not throw or log when form elements are present (target/minutes now store-owned)', () => {
        localStorage.setItem('target', 'JO32');
        document.body.innerHTML = '<input id="target" /><input id="minutes" />';
        expect(() => loadConfig()).not.toThrow();
        expect(errSpy).not.toHaveBeenCalled();
    });

    it('parses saved map state from localStorage', () => {
        localStorage.setItem('mapCenter', JSON.stringify([10, 20]));
        localStorage.setItem('mapZoom', '5');
        const { initialCenter, initialZoom } = loadConfig();
        expect(initialCenter).toEqual([10, 20]);
        expect(initialZoom).toBe(5);
    });

    it('logs a single error on corrupt mapCenter but still continues', () => {
        localStorage.setItem('mapCenter', '{not-json');
        const { initialCenter, initialZoom } = loadConfig();
        expect(initialCenter).toEqual([20, 0]);
        expect(initialZoom).toBe(2);
        expect(errSpy).toHaveBeenCalledWith('Error parsing saved map state', expect.anything());
    });
});
