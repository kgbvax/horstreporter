import { panelQth, STREAM_QTH_EVENT } from './state.js';
import { setBandLabVisible } from './band-lab.js';
import { setWsprMatrixVisible } from './wspr-matrix.js';
import { setAlmanacVisible } from './almanac.js';
import { setPanelToggleState } from './panel-toggle.js';

// cond-dock.js — the Conditions dock: one in-flow column right of the map that
// replaces the Band stats, Propagation and Typical openings panels. Tabs:
// Conditions (horizon Now / Typical). The dock never overlays the map; closed,
// it collapses to the hot-band pills plus a Conditions toggle (#cond-collapsed).
//
// The dock owns visibility. It switches the data pipelines of the panels it
// hosts (band-lab.js, wspr-matrix.js, almanac.js) on only while
// their view is showing, so a closed dock costs no polling.

const OPEN_KEY = 'condDockOpen';
const HORIZON_KEY = 'condHorizon';
const WIDTH_KEY = 'condDockWidth';
// Enable flags of the three panels this dock replaced (migrated once).
const LEGACY_ENABLE_KEYS = ['bandLabEnabled', 'wsprMatrixEnabled', 'almanacEnabled'];
// Placement storage of the old floating / docked panels; dead now.
const LEGACY_LAYOUT_KEYS = ['wsprMatrixPos', 'almanacPos', 'bandLabWindowSize'];

const MIN_W = 360;
const TOGGLE_LABELS = { show: 'Show conditions', hide: 'Hide conditions' };

const runtime = {
    open: false,
    horizon: 'now',
    tab: 'conditions',
    onLayoutChange: null,
    initialized: false,
};

// One-time move from the three per-panel enable flags to condDockOpen /
// condHorizon: open if any panel was on; Typical only when it was the only one.
export function migrateLegacyState(storage = localStorage) {
    if (storage.getItem(OPEN_KEY) === null) {
        const on = (k) => storage.getItem(k) === 'true';
        const now = on('bandLabEnabled') || on('wsprMatrixEnabled');
        const usual = on('almanacEnabled');
        if (now || usual) {
            storage.setItem(OPEN_KEY, 'true');
            storage.setItem(HORIZON_KEY, now ? 'now' : 'usual');
        }
    }
    for (const k of [...LEGACY_ENABLE_KEYS, ...LEGACY_LAYOUT_KEYS]) storage.removeItem(k);
}

export function initCondDock({ onLayoutChange } = {}) {
    runtime.onLayoutChange = onLayoutChange || null;
    const dock = document.getElementById('cond-dock');
    if (!dock) return null;

    migrateLegacyState();
    runtime.open = localStorage.getItem(OPEN_KEY) === 'true';
    runtime.horizon = localStorage.getItem(HORIZON_KEY) === 'usual' ? 'usual' : 'now';
    runtime.tab = 'conditions';

    restoreWidth(dock);
    syncQthLabel();
    const horizonRadio = document.querySelector(`input[name="cond-horizon"][value="${runtime.horizon}"]`);
    if (horizonRadio) horizonRadio.checked = true;

    if (!runtime.initialized) {
        document.getElementById('cond-open')?.addEventListener('click', () => setOpen(true));
        document.getElementById('cond-close')?.addEventListener('click', () => setOpen(false));
        document.getElementById('cond-tab-conditions')?.addEventListener('click', () => setTab('conditions'));
        document.getElementById('cond-horizon')?.addEventListener('change', (e) => {
            const value = e.target?.value;
            if (value === 'now' || value === 'usual') setHorizon(value);
        });
        // The label names the qth the panels describe (panelQth: the one the
        // stream runs for), so it changes when the stream does, not per keystroke.
        window.addEventListener(STREAM_QTH_EVENT, syncQthLabel);
        setupResize(dock);
        runtime.initialized = true;
    }

    apply();
    return { setOpen, setHorizon, setTab };
}

function currentQth() {
    return panelQth();
}

function syncQthLabel() {
    const el = document.getElementById('cond-qth');
    if (el) el.textContent = currentQth();
}

export function setOpen(open) {
    if (runtime.open === open) return;
    runtime.open = open;
    localStorage.setItem(OPEN_KEY, open ? 'true' : 'false');
    apply();
    // The control that was pressed is now hidden; keep keyboard focus useful.
    document.getElementById(open ? 'cond-tab-conditions' : 'cond-open')?.focus();
}

export function setHorizon(horizon) {
    if (runtime.horizon === horizon) return;
    runtime.horizon = horizon;
    localStorage.setItem(HORIZON_KEY, horizon);
    apply();
}

function setTab(tab) {
    if (runtime.tab === tab) return;
    runtime.tab = tab;
    apply();
}

// Push the state into the DOM and onto the hosted panels' data pipelines.
function apply() {
    const dock = document.getElementById('cond-dock');
    if (!dock) return;
    const { open, horizon, tab } = runtime;

    if (open) syncQthLabel();
    dock.classList.toggle('is-hidden', !open);
    document.getElementById('cond-collapsed')?.classList.toggle('is-hidden', open);
    setPanelToggleState(document.getElementById('cond-open'), open, TOGGLE_LABELS);

    const el = document.getElementById('cond-tab-conditions');
    el?.classList.toggle('is-active', tab === 'conditions');
    el?.setAttribute('aria-selected', tab === 'conditions' ? 'true' : 'false');
    document.getElementById('cond-conditions')?.classList.toggle('is-hidden', tab !== 'conditions');
    document.getElementById('cond-now')?.classList.toggle('is-hidden', horizon !== 'now');
    document.getElementById('cond-typical')?.classList.toggle('is-hidden', horizon !== 'usual');

    const showConditions = open && tab === 'conditions';
    setBandLabVisible(showConditions && horizon === 'now');
    setWsprMatrixVisible(showConditions && horizon === 'now');
    setAlmanacVisible(showConditions && horizon === 'usual');

    // The dock is in-flow: the map's box changed, so Leaflet and the azimuth
    // canvas must re-fit.
    runtime.onLayoutChange?.();
}

// Left-edge splitter: drag to resize the dock. The width lives in --cond-w so
// the flex column re-bases instantly; the map is re-fitted live.
function setupResize(dock) {
    const handle = document.getElementById('cond-dock-resize');
    if (!handle) return;
    const maxWidth = () => Math.max(MIN_W, Math.round(window.innerWidth * 0.7));
    let resizing = false;
    let startX = 0;
    let startWidth = 0;
    let rafPending = false;

    const onMove = (e) => {
        if (!resizing) return;
        const next = Math.max(MIN_W, Math.min(maxWidth(), startWidth + (startX - e.clientX)));
        dock.style.setProperty('--cond-w', `${Math.round(next)}px`);
        if (!rafPending) {
            rafPending = true;
            requestAnimationFrame(() => {
                rafPending = false;
                runtime.onLayoutChange?.();
            });
        }
    };
    const stop = () => {
        if (!resizing) return;
        resizing = false;
        document.body.style.cursor = '';
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', stop);
        localStorage.setItem(WIDTH_KEY, JSON.stringify({ width: dock.offsetWidth }));
        runtime.onLayoutChange?.();
    };
    handle.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        resizing = true;
        startX = e.clientX;
        startWidth = dock.offsetWidth;
        document.body.style.cursor = 'col-resize';
        document.addEventListener('mousemove', onMove);
        document.addEventListener('mouseup', stop);
        e.preventDefault();
        e.stopPropagation();
    });
}

function restoreWidth(dock) {
    try {
        const width = Number(JSON.parse(localStorage.getItem(WIDTH_KEY) || 'null')?.width);
        if (Number.isFinite(width) && width >= MIN_W) dock.style.setProperty('--cond-w', `${Math.round(width)}px`);
    } catch {
        // ignore invalid persisted values
    }
}

export const __test = { runtime, OPEN_KEY, HORIZON_KEY, WIDTH_KEY, apply };
