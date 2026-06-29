<script>
  import { uiStore } from './store.js';

  // SNR threshold sliders, migrated from index.html + app.js listeners.
  // Keeps the ssb-min-db / cw-min-db ids so the 5 render-path readers
  // (renderers, azimuth-runtime, band-lab, ui) read these unchanged.
  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

<div class="d-flex flex-column w-100">
  <label class="d-flex align-items-center justify-content-between mb-0" for="ssb-min-db">
    <span>SSB Min dB:</span> <span><span id="ssb-min-db-val">{$uiStore.ssbMinDb}</span> dB</span>
  </label>
  <input type="range" id="ssb-min-db" class="form-range" min="-30" max="10" step="1"
         bind:value={$uiStore.ssbMinDb} on:input={render} />
</div>
<div class="d-flex flex-column w-100">
  <label class="d-flex align-items-center justify-content-between mb-0" for="cw-min-db">
    <span>CW Min dB:</span> <span><span id="cw-min-db-val">{$uiStore.cwMinDb}</span> dB</span>
  </label>
  <input type="range" id="cw-min-db" class="form-range" min="-30" max="10" step="1"
         bind:value={$uiStore.cwMinDb} on:input={render} />
</div>
