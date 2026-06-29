import { writable } from 'svelte/store';

// Central reactive store for non-canvas UI state. Single source of truth for
// the controls/sidebar/panels, replacing scattered DOM + localStorage reads.
// Vanilla canvas modules can subscribe via uiStore.subscribe(...) without
// importing Svelte. Values persist to localStorage so reloads keep config.

const PERSIST_KEY = 'horst-ui-state';

const defaults = {
    target: '',
    minutes: 15,
    ssbMinDb: 0,
    cwMinDb: -15,
    clusterDistance: 500,
    surroundings: false,
};

function loadInitial() {
    try {
        const raw = localStorage.getItem(PERSIST_KEY);
        if (raw) return { ...defaults, ...JSON.parse(raw) };
    } catch (_) { /* corrupt/absent -> defaults */ }
    return { ...defaults };
}

export const uiStore = writable(loadInitial());

uiStore.subscribe((v) => {
    try { localStorage.setItem(PERSIST_KEY, JSON.stringify(v)); } catch (_) { /* private mode */ }
});
