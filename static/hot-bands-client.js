// Shared /api/hot_bands fetch. The hot-band pills and the Horst-Kevin mood
// module poll the same URL every 30 s; sharing the in-flight request and a
// short TTL cache halves the traffic without coupling the two modules.

const DEFAULT_TTL_MS = 10000;

const inflight = new Map();
const cache = new Map();

/** Query string for /api/hot_bands, identical for every caller. */
export function hotBandsParams({ qth, surroundings, currentBand }) {
    const params = new URLSearchParams();
    params.set('qth', qth);
    if (surroundings) params.set('surroundings', 'true');
    params.set('rings', 'auto');
    const current = String(currentBand || '').toLowerCase();
    if (current && current !== 'all') params.set('current_band', current);
    return params;
}

/**
 * Fetch (or reuse) the hot_bands response for `params`. A fetch already in
 * flight is shared; a successful response is reused for `ttlMs`. Failures are
 * not cached. There is deliberately no abort signal: callers keep their own
 * sequence guard and simply ignore results they no longer want.
 */
export function fetchHotBands(params, { ttlMs = DEFAULT_TTL_MS, now = Date.now } = {}) {
    const key = params.toString();
    const hit = cache.get(key);
    if (hit && now() - hit.at < ttlMs) return Promise.resolve(hit.data);
    const pending = inflight.get(key);
    if (pending) return pending;

    const p = (async () => {
        try {
            const resp = await fetch(`/api/hot_bands?${key}`);
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            cache.set(key, { at: now(), data });
            return data;
        } finally {
            inflight.delete(key);
        }
    })();
    inflight.set(key, p);
    return p;
}

/** Test hook: drop all shared state. */
export function resetHotBandsClient() {
    inflight.clear();
    cache.clear();
}
