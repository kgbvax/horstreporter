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
    // Time-travel replay runtime (static/timetravel.js). While `active`, the
    // replay bucket array REPLACES liveSpots (the real live list is parked in
    // liveSpotsBackup) — scheduleRender, band-lab and both projections read
    // state.liveSpots dynamically each call, so they follow the swap with no
    // changes. SSE arrivals and the 5s prune are suspended until exit, so the
    // backup stays valid.
    timeTravel: {
        active: false,
        liveSpotsBackup: null,
        start: 0,
        end: 0,
        bucketSeconds: 1800,
        currentBucketEnd: 0,
        playing: false,
        speedMs: 3000,
        histogram: null,
        bucketCache: new Map(),
        bucketCacheLimit: 24,
        timer: null,
        abort: null
    }
};