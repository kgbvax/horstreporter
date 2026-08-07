import App from './App.svelte';
import SnrThresholds from './SnrThresholds.svelte';
import Range from './Range.svelte';
import Toggle from './Toggle.svelte';
import MinSnr from './MinSnr.svelte';
import MapStyle from './MapStyle.svelte';
import Projection from './Projection.svelte';
import TargetInput from './TargetInput.svelte';
import { uiStore } from './store.js';
// Mount the Svelte UI shell into a stable host node if present. The node does
// not exist yet (U2 migrates panels into it); guard so the bundle is inert
// until then. Expose the store for vanilla modules to interop during migration.
const host = document.getElementById('svelte-root');
let app = null;
if (host) app = new App({ target: host });

// If app.js was loaded with ?capture=1, it parsed the URL into a capture config
// and exposed it here. Seed the Svelte store from those values BEFORE mounting
// the control components so URL-driven projection/style/qth/etc. are not
// clobbered by store defaults or persisted localStorage state.
const captureConfig = typeof window !== 'undefined' ? window.__horstCaptureConfig : null;
if (captureConfig?.enabled) {
    const cfg = captureConfig;
    uiStore.update((s) => {
        const next = { ...s };
        if (cfg.target != null) next.qth = String(cfg.target);
        if (Number.isFinite(cfg.minutes) && cfg.minutes >= 1) next.minutes = Math.min(60, cfg.minutes);
        if (cfg.minSnrMode === 'ssb' || cfg.minSnrMode === 'cw' || cfg.minSnrMode === 'none') next.minSnr = cfg.minSnrMode;
        if (Number.isFinite(cfg.ssbMinDb)) next.ssbMinDb = cfg.ssbMinDb;
        if (Number.isFinite(cfg.cwMinDb)) next.cwMinDb = cfg.cwMinDb;
        if (cfg.projection === 'mercator' || cfg.projection === 'azimuthal') next.projection = cfg.projection;
        if (cfg.style === 'grid-snr' || cfg.style === 'active-area') next.mapStyle = cfg.style;
        if (typeof cfg.surroundings === 'boolean') next.surroundings = cfg.surroundings;
        if (typeof cfg.countryColoring === 'boolean') next.countryColoring = cfg.countryColoring;
        if (typeof cfg.includeDxcluster === 'boolean') next.showDxcluster = cfg.includeDxcluster;
        return next;
    });
}

// U2 slice: SNR threshold sliders mount into the controls form placeholder.
const snrHost = document.getElementById('snr-sliders-root');
if (snrHost) new SnrThresholds({ target: snrHost });

const minutesHost = document.getElementById('minutes-root');
if (minutesHost) new Range({ target: minutesHost, props: { id: 'minutes', valId: 'minutes-val', label: 'Max Spot Age:', unit: 'min', key: 'minutes', min: 1, max: 60 } });

const clusterHost = document.getElementById('cluster-root');
if (clusterHost) new Range({ target: clusterHost, props: { id: 'cluster-distance', valId: 'cluster-dist-val', label: 'Max Cluster Dist:', unit: 'km', key: 'clusterDistance', min: 100, max: 2000, step: 50 } });

const autoZoomHost = document.getElementById('auto-zoom-root');
if (autoZoomHost) new Toggle({ target: autoZoomHost, props: { id: 'auto-zoom', label: 'Auto-zoom', key: 'autoZoom' } });

const surroundingsHost = document.getElementById('surroundings-root');
if (surroundingsHost) new Toggle({ target: surroundingsHost, props: { id: 'surroundings', label: 'Adj. Squares', key: 'surroundings', render: false, hook: '__horstSurroundingsChanged' } });

const countryHost = document.getElementById('country-coloring-root');
if (countryHost) new Toggle({ target: countryHost, props: { id: 'show-country-coloring', label: 'Country Color', key: 'countryColoring', render: false, hook: '__horstCountryColoringChanged' } });

const dxclusterHost = document.getElementById('dxcluster-root');
if (dxclusterHost) new Toggle({ target: dxclusterHost, props: { id: 'show-dxcluster-spots', label: 'DX Cluster', key: 'showDxcluster' } });
window.__horstSetDxcluster = (on) => uiStore.update((s) => ({ ...s, showDxcluster: !!on }));

const minSnrHost = document.getElementById('min-snr-group');
if (minSnrHost) new MinSnr({ target: minSnrHost });

const styleHost = document.getElementById('style-group');
if (styleHost) new MapStyle({ target: styleHost });

// Legacy localStorage seeding: only applied when there is no capture config so
// normal reloads keep the user's last UI state, while capture URLs win.
// One-time migration: the legacy 'target' key is now 'qth'.
if (!captureConfig?.enabled) {
    const savedProjection = localStorage.getItem('mapProjection');
    if (savedProjection === 'mercator' || savedProjection === 'azimuthal') {
        uiStore.update((s) => ({ ...s, projection: savedProjection }));
    }
    const savedSurroundings = localStorage.getItem('surroundings');
    if (savedSurroundings !== null) {
        uiStore.update((s) => ({ ...s, surroundings: savedSurroundings === 'true' }));
    }
    const savedCountry = localStorage.getItem('countryColoringEnabled');
    if (savedCountry !== null) {
        uiStore.update((s) => ({ ...s, countryColoring: savedCountry === 'true' }));
    }
    // Migrate from the legacy 'target' key to 'qth' on first load; otherwise
    // use the current 'qth' key.
    const legacyTarget = localStorage.getItem('target');
    if (legacyTarget) {
        localStorage.setItem('qth', legacyTarget);
        localStorage.removeItem('target');
    }
    const savedQth = localStorage.getItem('qth');
    if (savedQth) uiStore.update((s) => ({ ...s, qth: savedQth }));
    const savedDx = localStorage.getItem('showDXClusterSpots');
    if (savedDx !== null) uiStore.update((s) => ({ ...s, showDxcluster: savedDx === 'true' }));
}

// External vanilla writers set the qth via the store so the input stays in
// sync; readers still read #qth.value directly.
window.__horstSetQTH = (v) => uiStore.update((s) => ({ ...s, qth: v == null ? '' : String(v) }));

const qthHost = document.getElementById('qth-root');
if (qthHost) new TargetInput({ target: qthHost });
const projHost = document.getElementById('projection-group');
if (projHost) new Projection({ target: projHost });

window.__horstUiStore = uiStore;

export { app, uiStore };
