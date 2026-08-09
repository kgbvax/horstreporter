export const state = {
    liveSpots: [],
    heatLayer: null,
    dxClusterLayer: null,
    wsprLayer: null,
    qthLayer: null,
    eventSource: null,
    renderInterval: null,
    cycleInterval: null,
    renderPending: false,
    dxClusterHoverActive: false,
    lastMercatorInteractionAt: 0,
    lastMercatorAutoZoomAt: 0,
    mercatorInteractionActive: false,
    mercatorRenderDeferred: false,
    softPaused: false
};