import { bandColors, getEnabledBands } from './utils.js';

const POLL_INTERVAL_MS = 30_000;
const STALE_TIMEOUT_MS = 2 * 60_000;

const GLYPH = {
    surprise: '★',
    dx_surge: '↗',
    rising: '↑',
};

function hexToRgba(hex, alpha) {
    if (typeof hex !== 'string' || hex.length !== 7 || hex[0] !== '#') {
        return `rgba(85,85,85,${alpha})`;
    }
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) {
        return `rgba(85,85,85,${alpha})`;
    }
    return `rgba(${r},${g},${b},${alpha})`;
}

export function initHotBandIndicator({ getQth, getSurroundings, getCurrentBand, onBandSwitch }) {
    const container = document.getElementById('hot-band-indicator');
    if (!container) return null;

    let pollTimer = null;
    let abortCtl = null;
    let lastGoodTs = 0;
    let lastRecs = [];

    function clearStale() {
        if (lastGoodTs > 0 && Date.now() - lastGoodTs > STALE_TIMEOUT_MS) {
            lastRecs = [];
            render();
        }
    }

    function render() {
        const current = (getCurrentBand?.() || '').toLowerCase();
        // The backend recommends bands without knowing the client's enabled-band
        // checkboxes, so a pill for a disabled band would render but its click
        // would no-op in switchToBand. Filter those out so every visible pill
        // is actionable.
        const enabled = getEnabledBands();
        const visible = lastRecs.filter(r => r && r.band && r.band !== current && enabled.has(r.band));
        if (!visible.length) {
            container.classList.add('is-hidden');
            container.replaceChildren();
            return;
        }
        container.classList.remove('is-hidden');
        const frag = document.createDocumentFragment();
        for (const r of visible) frag.appendChild(buildPill(r));
        container.replaceChildren(frag);
    }

    function buildPill(rec) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'hot-band-pill';
        if (rec.priority === 'high') btn.classList.add('is-priority-high');

        const color = bandColors[rec.band] || bandColors.all;
        btn.style.setProperty('--hot-band-bg', hexToRgba(color, 0.18));
        btn.style.setProperty('--hot-band-bg-strong', hexToRgba(color, 0.28));
        btn.style.setProperty('--hot-band-border', hexToRgba(color, 0.85));

        btn.title = tooltipFor(rec);
        btn.dataset.band = rec.band;
        btn.dataset.kind = rec.kind || '';
        btn.setAttribute('aria-label', `Switch to ${rec.band}: ${rec.reason || rec.kind || ''}`);

        const glyph = document.createElement('span');
        glyph.className = 'hot-band-glyph';
        glyph.textContent = GLYPH[rec.kind] || '·';
        btn.appendChild(glyph);

        const name = document.createElement('span');
        name.className = 'hot-band-name';
        name.textContent = rec.band;
        btn.appendChild(name);

        if (rec.reason) {
            const reason = document.createElement('span');
            reason.className = 'hot-band-reason';
            reason.textContent = `· ${rec.reason}`;
            btn.appendChild(reason);
        }

        btn.addEventListener('click', () => {
            if (typeof onBandSwitch === 'function') onBandSwitch(rec.band);
        });
        return btn;
    }

    function tooltipFor(r) {
        const lines = [`${r.band} · ${r.reason || r.kind || ''}`];
        if (typeof r.spots_per_minute === 'number') {
            const live = r.spots_per_minute.toFixed(2);
            if (typeof r.baseline_activity === 'number' && r.baseline_activity > 0) {
                lines.push(`rate: ${live}/min (baseline ${r.baseline_activity.toFixed(2)})`);
            } else {
                lines.push(`rate: ${live}/min`);
            }
        }
        if (r.kind === 'dx_surge' && r.baseline_p90_distance_km) {
            lines.push(`p90 distance: ${Math.round(r.p90_distance_km)} km vs baseline ${Math.round(r.baseline_p90_distance_km)} km`);
        }
        if (r.kind === 'rising' && typeof r.trend_delta === 'number') {
            lines.push(`trend Δ ${r.trend_delta.toFixed(2)}`);
        }
        return lines.join('\n');
    }

    async function fetchOnce() {
        const qth = (getQth?.() || '').trim();
        if (!qth) {
            lastRecs = [];
            render();
            return;
        }
        if (abortCtl) abortCtl.abort();
        abortCtl = new AbortController();

        const params = new URLSearchParams();
        params.set('qth', qth);
        if (getSurroundings?.()) params.set('surroundings', 'true');
        const current = (getCurrentBand?.() || '').toLowerCase();
        if (current && current !== 'all') params.set('current_band', current);

        try {
            const resp = await fetch(`/api/hot_bands?${params.toString()}`, { signal: abortCtl.signal });
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            lastRecs = Array.isArray(data?.recommendations) ? data.recommendations : [];
            lastGoodTs = Date.now();
            render();
        } catch (err) {
            if (err.name === 'AbortError') return;
            console.warn('hot_bands fetch failed:', err);
            clearStale();
        }
    }

    function start() {
        stop();
        fetchOnce();
        pollTimer = setInterval(() => {
            if (document.visibilityState !== 'visible') return;
            fetchOnce();
        }, POLL_INTERVAL_MS);
    }

    function stop() {
        if (pollTimer) {
            clearInterval(pollTimer);
            pollTimer = null;
        }
        if (abortCtl) {
            abortCtl.abort();
            abortCtl = null;
        }
    }

    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') fetchOnce();
    });

    start();

    return {
        refresh: () => { if (document.visibilityState === 'visible') fetchOnce(); },
        rerender: render,
        stop,
    };
}
