<script>
  import { uiStore } from './store.js';

  // Min-SNR radio group, store-bound. Keeps input ids/name and values so the
  // getMinSnrMode() reader (input[name="min-snr"]:checked) is unchanged.
  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

<input type="radio" class="btn-check" id="snr-none" name="min-snr" value="none" autocomplete="off" bind:group={$uiStore.minSnr} on:change={render} />
<label class="btn btn-outline-primary btn-sm" for="snr-none" title="Show every spot, regardless of signal strength">All spots</label>
<input type="radio" class="btn-check" id="snr-cw" name="min-snr" value="cw" autocomplete="off" bind:group={$uiStore.minSnr} on:change={render} />
<label class="btn btn-outline-primary btn-sm" for="snr-cw" title="Only spots strong enough for a CW contact (threshold under Display)">CW workable</label>
<input type="radio" class="btn-check" id="snr-ssb" name="min-snr" value="ssb" autocomplete="off" bind:group={$uiStore.minSnr} on:change={render} />
<label class="btn btn-outline-primary btn-sm" for="snr-ssb" title="Only spots strong enough for an SSB contact (threshold under Display)">SSB workable</label>
