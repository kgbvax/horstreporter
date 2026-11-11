// feed-lag.js — the PSKReporter feed delay (server side: feed_lag.go).
//
// While the PSKReporter feed runs late, dx_conditions, hot_bands and
// prop_intel/v2 end their "now" window where the feed is instead of at the
// wall clock. dx_conditions reports the measured delay (pskr_feed_lag_seconds)
// and how far its window moved (feed_lag_seconds); a long time range can move
// less or not at all. This module words that for the dock; it touches no DOM.

// { lagMin, shiftMin } in whole minutes from a dx_conditions response, or null
// while the feed is on time or the response has no figures.
export function parseFeedLag(dx) {
    const raw = Number(dx?.pskr_feed_lag_seconds);
    if (!Number.isFinite(raw) || raw <= 0) return null;
    const shift = Number(dx?.feed_lag_seconds);
    return {
        lagMin: Math.max(1, Math.round(raw / 60)),
        shiftMin: Number.isFinite(shift) && shift > 0 ? Math.round(shift / 60) : 0,
    };
}

// One line for the dock header, empty while the feed is on time. minutes is
// the selected time range of the band rows.
export function feedLagSummary(lag, minutes) {
    if (!lag) return '';
    const text = `PSKReporter reports arrive about ${lag.lagMin} min late, so Now ends ${lag.lagMin} min ago.`;
    if (lag.shiftMin >= lag.lagMin) return text;
    const range = Number.isFinite(Number(minutes)) && Number(minutes) > 0 ? `${Number(minutes)}-min ` : '';
    return `${text} The band figures for the ${range}time range cannot move back that far and read low.`;
}
