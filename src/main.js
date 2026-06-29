import App from './App.svelte';
import SnrThresholds from './SnrThresholds.svelte';
import Range from './Range.svelte';
import { uiStore } from './store.js';

// Mount the Svelte UI shell into a stable host node if present. The node does
// not exist yet (U2 migrates panels into it); guard so the bundle is inert
// until then. Expose the store for vanilla modules to interop during migration.
const host = document.getElementById('svelte-root');
let app = null;
if (host) app = new App({ target: host });

// U2 slice: SNR threshold sliders mount into the controls form placeholder.
const snrHost = document.getElementById('snr-sliders-root');
if (snrHost) new SnrThresholds({ target: snrHost });

const minutesHost = document.getElementById('minutes-root');
if (minutesHost) new Range({ target: minutesHost, props: { id: 'minutes', valId: 'minutes-val', label: 'Max Spot Age:', unit: 'min', key: 'minutes', min: 1, max: 60 } });

const clusterHost = document.getElementById('cluster-root');
if (clusterHost) new Range({ target: clusterHost, props: { id: 'cluster-distance', valId: 'cluster-dist-val', label: 'Max Cluster Dist:', unit: 'km', key: 'clusterDistance', min: 100, max: 2000, step: 50 } });

window.__horstUiStore = uiStore;

export { app, uiStore };
