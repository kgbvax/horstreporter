import { writable } from 'svelte/store';

// Central reactive store for non-canvas UI state. Single source of truth for
// the controls/sidebar/panels, replacing scattered DOM + localStorage reads.
// Vanilla canvas modules can subscribe via uiStore.subscribe(...) without
// importing Svelte. Values persist to localStorage so reloads keep config.

const PERSIST_KEY = 'horst-ui-state';

const defaults = {
    qth: '',
    minutes: 15,
    minSnr: 'ssb',
    ssbMinDb: 0,
    cwMinDb: -15,
    clusterDistance: 500,
    autoZoom: false,
    mapStyle: 'grid-snr',
    projection: 'mercator',
    countryColoring: true,
    showDxcluster: true,
    surroundings: false,
};

function loadInitial() {
    try {
        const raw = localStorage.getItem(PERSIST_KEY);
        if (raw) {
            const saved = { ...defaults, ...JSON.parse(raw) };
            // One-time migration: the legacy 'target' field name is now 'qth'.
            if (saved.target !== undefined && saved.qth === undefined) {
                saved.qth = saved.target;
                delete saved.target;
                localStorage.setItem(PERSIST_KEY, JSON.stringify(saved));
            }
            return saved;
        }
    } catch (_) { /* corrupt/absent -> defaults */ }
    return { ...defaults };
}

export const uiStore = writable(loadInitial());

uiStore.subscribe((v) => {
    try { localStorage.setItem(PERSIST_KEY, JSON.stringify(v)); } catch (_) { /* private mode */ }
});
