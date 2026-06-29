<script>
  import { uiStore } from './store.js';

  // Map style radio group, store-bound. Keeps input ids/name/values so the
  // style readers (input[name="style-select"]:checked) stay unchanged.
  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

<input type="radio" class="btn-check" id="style-grid" name="style-select" value="grid-snr" autocomplete="off" bind:group={$uiStore.mapStyle} on:change={render} />
<label class="btn btn-outline-primary btn-sm" for="style-grid">Grid</label>
<input type="radio" class="btn-check" id="style-area" name="style-select" value="active-area" autocomplete="off" bind:group={$uiStore.mapStyle} on:change={render} />
<label class="btn btn-outline-primary btn-sm" for="style-area">Active Area</label>
