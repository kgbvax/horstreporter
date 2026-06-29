import App from './App.svelte';
import { uiStore } from './store.js';

// Mount the Svelte UI shell into a stable host node if present. The node does
// not exist yet (U2 migrates panels into it); guard so the bundle is inert
// until then. Expose the store for vanilla modules to interop during migration.
const host = document.getElementById('svelte-root');
let app = null;
if (host) app = new App({ target: host });

window.__horstUiStore = uiStore;

export { app, uiStore };
