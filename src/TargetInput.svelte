<script>
  import { uiStore } from './store.js';

  // QTH input, store-bound but DOM-readable. Keeps id="qth" so all
  // getElementById('qth').value readers + the form-submit/keydown listeners
  // are unchanged. External writers call window.__horstSetQTH to keep the
  // store in sync. Persists the 'qth' key for autostart (migrates legacy 'target').
  function onInput() {
    try { localStorage.setItem('qth', $uiStore.qth); } catch (_) {}
    if (typeof window.__horstQthInput === 'function') window.__horstQthInput();
  }
</script>

<input type="text" id="qth" class="form-control" bind:value={$uiStore.qth} on:input={onInput}
       placeholder="Locator or Callsign"
       title="You can use squares, subsquares or callsigns" />
