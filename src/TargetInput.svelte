<script>
  import { uiStore } from './store.js';

  // Target input, store-bound but DOM-readable. Keeps id="target" so all
  // getElementById('target').value readers + the form-submit/keydown listeners
  // are unchanged. External writers call window.__horstSetTarget to keep the
  // store in sync. Persists the legacy 'target' key for autostart.
  function onInput() {
    try { localStorage.setItem('target', $uiStore.target); } catch (_) {}
    if (typeof window.__horstTargetInput === 'function') window.__horstTargetInput();
  }
</script>

<input type="text" id="target" class="form-control" bind:value={$uiStore.target} on:input={onInput}
       placeholder="Callsign or Locator (e.g. W1AW, FN31, JO32WE)"
       title="You can use squares, subsquares or callsigns" />
