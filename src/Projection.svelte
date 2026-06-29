<script>
  import { uiStore } from './store.js';

  // Projection radio group, store-bound. Keeps ids/name/values so the
  // currentProjection() reader stays unchanged. Persists the legacy
  // mapProjection key and invokes the async projection-mode hook on change.
  function onChange(e) {
    localStorage.setItem('mapProjection', e.target.value);
    if (typeof window.__horstApplyProjection === 'function') window.__horstApplyProjection(e.target.value);
  }
</script>

<input type="radio" class="btn-check" id="proj-mercator" name="projection-select" value="mercator" autocomplete="off" bind:group={$uiStore.projection} on:change={onChange} />
<label class="btn btn-outline-primary btn-sm" for="proj-mercator">Mercator</label>
<input type="radio" class="btn-check" id="proj-azimuthal" name="projection-select" value="azimuthal" autocomplete="off" bind:group={$uiStore.projection} on:change={onChange} />
<label class="btn btn-outline-primary btn-sm" for="proj-azimuthal">Azimuthal</label>
