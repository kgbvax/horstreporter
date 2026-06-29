<script>
  import { uiStore } from './store.js';

  // Reusable store-bound range slider. Keeps the original input id and value
  // label id so vanilla render-path readers stay unchanged. Fires the render
  // hook on input.
  export let id;
  export let valId;
  export let label;
  export let unit = '';
  export let key;
  export let min;
  export let max;
  export let step = 1;

  function render() {
    if (typeof window.__horstScheduleRender === 'function') window.__horstScheduleRender();
  }
</script>

<div class="d-flex flex-column w-100">
  <label class="d-flex align-items-center justify-content-between mb-0" for={id}>
    <span>{label}</span> <span><span id={valId}>{$uiStore[key]}</span> {unit}</span>
  </label>
  <input type="range" {id} class="form-range" {min} {max} {step}
         bind:value={$uiStore[key]} on:input={render} />
</div>
