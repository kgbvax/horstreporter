export const state = {
    liveSpots: [],
    heatLayer: null,
    dxClusterLayer: null,
    wsprLayer: null,
    qthLayer: null,
    qth: '',
    eventSource: null,
    renderInterval: null,
    cycleInterval: null,
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
};