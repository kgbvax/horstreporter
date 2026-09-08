// Shared Chase Queue opt-in predicate. app.js (module injection gate) and
// dxcluster.js (CQ_ENABLED) both import it so the two opt-in checks cannot
// drift apart.
export function isChaseQueueEnabled() {
    return new URLSearchParams(location.search).has('cq') ||
        localStorage.getItem('showChaseQueue') === '1';
}