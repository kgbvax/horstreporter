<script>
  import { uiStore } from './store.js';

  // Reusable store-bound bootstrap btn-check toggle. Keeps the original input
  // id so vanilla readers (getElementById(id).checked) stay unchanged. Fires
  // the render hook on change.
  export let id;
  export let label;
  export let key;

  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

<input type="checkbox" class="btn-check" {id} autocomplete="off" bind:checked={$uiStore[key]} on:change={render} />
<label class="btn btn-outline-primary btn-sm w-100" for={id}>{label}</label>
