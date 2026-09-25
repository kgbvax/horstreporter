<script>
  import { uiStore } from './store.js';

  // SNR threshold sliders, migrated from index.html + app.js listeners.
  // Keeps the ssb-min-db / cw-min-db ids so the 5 render-path readers
  // (renderers, azimuth-runtime, band-lab, ui) read these unchanged.
  //
  // Only the slider for the ACTIVE min-SNR mode is mounted. The "All"
  // (none) mode applies no SNR filter, and "≥ CW" uses only the CW slider
  // while "≥ SSB" uses only the SSB slider — so the value shown is always
  // the value the render path actually consults. The inactive sliders are
  // unmounted (not hidden), but their values live in the store and
  // reappear when their mode is selected, so nothing is lost. Each reader
  // only consults the slider that matches the active mode, so an
  // unmounted slider's absence is harmless (it would fall back to its
  // default, which is unused in that mode).
  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

{#if $uiStore.minSnr === 'ssb'}
  <div class="d-flex flex-column w-100">
    <label class="d-flex align-items-center justify-content-between mb-0" for="ssb-min-db">
      <span>SSB min dB</span> <span><span id="ssb-min-db-val">{$uiStore.ssbMinDb}</span> dB</span>
    </label>
    <input type="range" id="ssb-min-db" class="form-range" min="-30" max="10" step="1"
           bind:value={$uiStore.ssbMinDb} on:input={render} />
  </div>
{:else if $uiStore.minSnr === 'cw'}
  <div class="d-flex flex-column w-100">
    <label class="d-flex align-items-center justify-content-between mb-0" for="cw-min-db">
      <span>CW min dB</span> <span><span id="cw-min-db-val">{$uiStore.cwMinDb}</span> dB</span>
    </label>
    <input type="range" id="cw-min-db" class="form-range" min="-30" max="10" step="1"
           bind:value={$uiStore.cwMinDb} on:input={render} />
  </div>
{:else}
  <div class="d-flex align-items-center w-100 small text-muted" style="min-height: 2.1em;">
    No SNR filter
  </div>
{/if}