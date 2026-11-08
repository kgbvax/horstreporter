export const state = {
    liveSpots: [],
    heatLayer: null,
    dxClusterLayer: null,
    wsprLayer: null,
    qthLayer: null,
    qth: '',
    // The qth the live stream was last started for (normalised, e.g. JO32).
    // state.qth follows the input on every keystroke; panels that sit next to
    // the stream's spots read this instead (panelQth), so they don't fetch
    // for a half-typed or not-yet-submitted locator.
    streamQth: '',
    eventSource: null,
    renderInterval: null,
    renderPending: false,
    dxClusterHoverActive: false,
    lastMercatorInteractionAt: 0,
    lastMercatorAutoZoomAt: 0,
    mercatorInteractionActive: false,
    mercatorRenderDeferred: false,
    softPaused: false,
    drillDownBand: '',
    drillDownRegion: '',
    // Filter the current SSE connection is actually fetching from the server.
    // Used to avoid tearing down the stream when the user only disables bands
    // (client-side filter is enough) or re-enables bands already in the stream.
    // When the SNR threshold or band set changes, we still restart but preserve
    // existing data so the map doesn't flash empty. null = no active stream.
    streamedFilter: null,
    // Live area the current stream delivers (live-area.js parseAreaPayload):
    // set by the stream's `area` event; null before the first one.
    liveArea: null,
    // Resume state (stream v2): id of the last event the stream delivered
    // (`<epoch>-<seq>`), the qth/minutes/surroundings that stream was opened
    // for, and whether the stream was closed on purpose while the tab was
    // hidden (it reopens with `since=streamLastId` when the tab returns).
    streamLastId: null,
    streamedParams: null,
    streamSuspended: false,
    // Server answer to the last resume request ("delta" | "full" | null).
    lastResumeMode: null,
};
// The qth the Conditions panels describe: the one the live stream runs for,
// or the input's value before any stream has started.
export function panelQth() {
    if (state.streamQth) return state.streamQth;
    const el = typeof document !== 'undefined' ? document.getElementById('qth') : null;
    return String(el?.value || state.qth || '').trim().toUpperCase();
}

// Fired on window when the stream starts for a different qth than before.
export const STREAM_QTH_EVENT = 'horst:stream-qth';
