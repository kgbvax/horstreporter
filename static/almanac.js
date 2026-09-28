import { state } from './state.js';
import { WSPR_REGIONS, bandColors, regionForLocator } from './utils.js';
import { makeDraggable } from './panel-drag.js';
import { setPanelToggleState } from './panel-toggle.js';
import { escapeHtml } from './ui-helpers.js';

// almanac.js — the Almanac panel (plan 2026-09-28-001, U7): which regions are
// usually reachable from the operator's own area, per band and 30-min UTC
// slot, over the last 30 complete days ("opened N of M days"). Reads
// GET /api/almanac?qth= (docs/api.md). From-here-only by design: the area is
// always the operator's QTH (widened to neighbouring squares when sparse).
//
// Layout: header (area grid4 + radius, "approximate location" for a DXCC
// fallback), the agenda (usual openings now / in the next 12 h), then one
// block per far-end region (the operator's own region last) holding a thin
// lane per band. A lane's opacity follows n/m in the band's canonical colour;
// a slot with m < m_min is "not enough data" (neutral hatch, never "closed").
// A vertical line marks the current UTC time.
//
// Fetch discipline mirrors wspr-matrix.js: enable key in localStorage, #qth
// change listener, AbortController, plus a request token so a late response
// for an old QTH can never overwrite the current one.
//
// Clicking a lane (or Enter/Space on the focused lane) calls openDrilldown —
// the seasonal month × hour view arrives with U8.

const PANEL_ID = 'almanac-window';
const TOGGLE_ID = 'almanac-toggle';
const BODY_ID = 'almanac-body';
const ENABLE_KEY = 'almanacEnabled';
const POS_KEY = 'almanacPos';

const TICK_MS = 60_000;         // now-line refresh
const REFETCH_TICKS = 5;        // re-fetch every 5 ticks (server caches 60 s / 120 s)
const AGENDA_CAP = 6;
const SLOTS = 48;
const DEFAULT_SLOT_MINUTES = 30;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

const TOGGLE_LABELS = { show: 'Show almanac', hide: 'Hide almanac' };

const runtime = {
    enabled: false,
    timer: null,
    ticks: 0,
    abortController: null,
    token: 0,
    loading: false,
    cache: null,        // last good /api/almanac payload (for the current QTH)
    error: null,        // { kind: 'invalid' | 'notfound' | 'unavailable', qth }
    lastQth: '',
    agendaExpanded: false,
    drilldown: null,    // last lane handed to openDrilldown (U8 renders it)
    onLayoutChange: null,
};

export function initAlmanac({ onLayoutChange } = {}) {
    runtime.onLayoutChange = onLayoutChange || null;
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel || !toggle) return;

    if (localStorage.getItem(ENABLE_KEY) === 'true') {
        setAlmanacVisible(true);
    } else {
        setPanelToggleState(toggle, false, TOGGLE_LABELS);
    }

    toggle.addEventListener('click', () => setAlmanacVisible(!runtime.enabled));

    const qthInput = document.getElementById('qth');
    if (qthInput) {
        qthInput.addEventListener('change', () => {
            if (!runtime.enabled) return;
            if (currentQth() !== runtime.lastQth) fetchAlmanac();
        });
    }

    makeDraggable(panel, panel.querySelector('.almanac-window-header'), POS_KEY);
}

function setAlmanacVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    setPanelToggleState(toggle, visible, TOGGLE_LABELS);
    localStorage.setItem(ENABLE_KEY, visible ? 'true' : 'false');
    if (runtime.onLayoutChange) runtime.onLayoutChange();
    if (visible) startTimer(); else stopTimer();
}

function startTimer() {
    stopTimer();
    fetchAlmanac();
    runtime.ticks = 0;
    runtime.timer = setInterval(() => {
        if (document.visibilityState !== 'visible') return;
        runtime.ticks += 1;
        if (runtime.ticks % REFETCH_TICKS === 0) fetchAlmanac({ quiet: true });
        else updateNowLines();
    }, TICK_MS);
}

function stopTimer() {
    if (runtime.timer) {
        clearInterval(runtime.timer);
        runtime.timer = null;
    }
    if (runtime.abortController) {
        runtime.abortController.abort();
        runtime.abortController = null;
    }
    // Any response still in flight is now stale.
    runtime.token += 1;
    runtime.loading = false;
}

function currentQth() {
    return String(document.getElementById('qth')?.value || state.qth || '').trim().toUpperCase();
}

async function fetchAlmanac({ quiet = false } = {}) {
    if (!runtime.enabled) return;
    const qth = currentQth();
    const token = ++runtime.token;
    if (runtime.abortController) runtime.abortController.abort();
    runtime.abortController = null;

    if (!qth) {
        runtime.lastQth = '';
        runtime.cache = null;
        runtime.error = null;
        runtime.loading = false;
        render();
        return;
    }

    const qthChanged = qth !== runtime.lastQth;
    runtime.lastQth = qth;
    if (qthChanged) runtime.agendaExpanded = false;
    runtime.loading = true;
    // A background refresh of the same QTH keeps the lanes steady; anything
    // else dims them (or shows "Loading" on a first load).
    if (!quiet || qthChanged) showLoading();

    const controller = new AbortController();
    runtime.abortController = controller;
    let outcome;
    try {
        const resp = await fetch(`/api/almanac?qth=${encodeURIComponent(qth)}`, { signal: controller.signal });
        if (token !== runtime.token) return;
        if (resp.ok) {
            outcome = { payload: await resp.json() };
        } else if (resp.status === 400) {
            outcome = { error: 'invalid' };
        } else if (resp.status === 404) {
            outcome = { error: 'notfound' };
        } else {
            outcome = { error: 'unavailable' };
        }
    } catch (err) {
        if (err?.name === 'AbortError' || token !== runtime.token) return;
        console.warn('almanac fetch failed:', err);
        outcome = { error: 'unavailable' };
    } finally {
        if (runtime.abortController === controller) runtime.abortController = null;
    }
    if (token !== runtime.token) return;

    runtime.loading = false;
    if (outcome.payload) {
        runtime.cache = outcome.payload;
        runtime.error = null;
    } else {
        // Never leave a previous area's lanes on screen next to an error.
        runtime.cache = null;
        runtime.error = { kind: outcome.error, qth };
    }
    render();
}

function bodyEl() {
    return document.getElementById(BODY_ID);
}

function showLoading() {
    const body = bodyEl();
    if (!body) return;
    const lanes = body.querySelector('.almanac-lanes');
    if (lanes && runtime.cache) {
        lanes.classList.add('is-loading');
        const status = body.querySelector('.almanac-status');
        if (status) status.textContent = 'Loading…';
    } else {
        body.innerHTML = '<div class="almanac-status almanac-message text-muted small" role="status">Loading…</div>';
    }
    body.setAttribute('aria-busy', 'true');
}

// --- Formatting helpers ------------------------------------------------------

function pad2(v) {
    return String(v).padStart(2, '0');
}

// Minutes after 00:00 UTC → "HH:MM" (1440 → "24:00", used as a range end).
function hhmm(minutes) {
    return `${pad2(Math.floor(minutes / 60))}:${pad2(minutes % 60)}`;
}

// "14:00 UTC" for one slot, "13:00–14:30 UTC" for a run (end exclusive).
function slotRangeLabel(start, len, slotMinutes = DEFAULT_SLOT_MINUTES) {
    const a = hhmm(start * slotMinutes);
    if (len <= 1) return `${a} UTC`;
    return `${a}–${hhmm((start + len) * slotMinutes)} UTC`;
}

// Accessible name and title of one slot (or run of equal slots).
function slotLabel(band, region, start, len, n, m, mMin, slotMinutes) {
    const where = `${band} to ${region}, ${slotRangeLabel(start, len, slotMinutes)}`;
    if (m < mMin) return `${where}: not enough data (${m} ${m === 1 ? 'day' : 'days'})`;
    return `${where}: opened ${n} of ${m} days`;
}

function startsInLabel(min) {
    const v = Math.max(0, Math.round(Number(min) || 0));
    if (v < 60) return `in ${v} min`;
    const h = Math.floor(v / 60);
    const rest = v % 60;
    return rest ? `in ${h} h ${rest} min` : `in ${h} h`;
}

function localTime(ms) {
    return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
}

// Secondary local-time label for an agenda window (browser time zone).
function localRangeLabel(entry, slotMinutes = DEFAULT_SLOT_MINUTES) {
    const now = new Date();
    const midnight = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
    const startMs = midnight + entry.start_slot * slotMinutes * 60_000;
    const endMs = startMs + entry.len_slots * slotMinutes * 60_000;
    return `${localTime(startMs)}–${localTime(endMs)} local`;
}

function nowFraction(date = new Date()) {
    return (date.getUTCHours() * 60 + date.getUTCMinutes()) / 1440;
}

function nowLeft() {
    return `${(nowFraction() * 100).toFixed(4)}%`;
}

function bandRank(band) {
    const i = BAND_ORDER.indexOf(band);
    return i < 0 ? BAND_ORDER.length : i;
}

// Region rows: canonical order with the operator's own region last.
function orderRegions(ownRegion, extra = []) {
    const all = [...WSPR_REGIONS];
    for (const r of extra) if (r && !all.includes(r)) all.push(r);
    if (!ownRegion || !all.includes(ownRegion)) return all;
    return [...all.filter((r) => r !== ownRegion), ownRegion];
}

// Agenda: usually-open-now first (longest running first), then upcoming by
// how soon they start.
function sortAgenda(agenda, nowSlot) {
    const ago = (e) => ((nowSlot - e.start_slot) % SLOTS + SLOTS) % SLOTS;
    return [...agenda].sort((a, b) => {
        const ao = a.status === 'ongoing' ? 0 : 1;
        const bo = b.status === 'ongoing' ? 0 : 1;
        if (ao !== bo) return ao - bo;
        if (ao === 0) return ago(b) - ago(a);
        return (a.starts_in_min ?? 0) - (b.starts_in_min ?? 0);
    });
}

// --- Rendering ----------------------------------------------------------------

function render() {
    const body = bodyEl();
    if (!body) return;
    const focus = captureFocus(body);
    body.setAttribute('aria-busy', runtime.loading ? 'true' : 'false');

    if (!runtime.lastQth) {
        body.innerHTML = message('Enter your locator to see the Almanac.');
        return;
    }
    if (runtime.error) {
        body.innerHTML = message(errorText(runtime.error));
        return;
    }
    const data = runtime.cache;
    if (!data) {
        body.innerHTML = runtime.loading
            ? '<div class="almanac-status almanac-message text-muted small" role="status">Loading…</div>'
            : '';
        return;
    }

    let html = headerHtml(data);
    html += `<div class="almanac-status text-muted small" role="status">${runtime.loading ? 'Loading…' : ''}</div>`;
    const lanes = Array.isArray(data.lanes) ? data.lanes : [];
    if (lanes.length === 0) {
        html += message('No data from this area yet.');
        body.innerHTML = html;
        return;
    }
    html += agendaHtml(data);
    html += lanesHtml(data, lanes);
    html += legendHtml();
    body.innerHTML = html;
    attachHandlers(body);
    restoreFocus(body, focus);
}

function message(text) {
    return `<div class="almanac-message text-muted small">${escapeHtml(text)}</div>`;
}

function errorText(err) {
    switch (err.kind) {
        case 'invalid': return 'Invalid QTH. Enter a Maidenhead locator (for example JO32) or a callsign.';
        case 'notfound': return `Could not locate ${err.qth}. Enter a locator instead.`;
        default: return 'The Almanac is temporarily unavailable. Try again in a minute.';
    }
}

function headerHtml(data) {
    const area = data.area || {};
    const grid4 = area.grid4 || data.qth || '';
    const radius = Number(area.radius) || 0;
    const squares = Array.isArray(area.squares) ? area.squares.length : 1;
    const days = data.window?.days || 30;
    const approx = area.approximate
        ? '<div class="almanac-approx">Approximate location: taken from the country centre. Enter a locator for your own square.</div>'
        : '';
    const sq = `${squares} ${squares === 1 ? 'square' : 'squares'}`;
    return `<div class="almanac-area small"><span class="almanac-area-main"><strong>${escapeHtml(grid4)}</strong>, radius ${radius} (${sq})</span>` +
        ` <span class="text-muted">last ${days} days, times UTC</span>${approx}</div>`;
}

function agendaHtml(data) {
    const slotMinutes = data.slot_minutes || DEFAULT_SLOT_MINUTES;
    const all = sortAgenda(Array.isArray(data.agenda) ? data.agenda : [], Number(data.now_slot) || 0);
    let html = '<section class="almanac-agenda" aria-label="Usual openings in the next 12 hours">' +
        '<div class="almanac-subhead">Usual openings, next 12 h</div>';
    if (all.length === 0) {
        return `${html}<div class="text-muted small">No usual openings in the next 12 h</div></section>`;
    }
    const shown = runtime.agendaExpanded ? all : all.slice(0, AGENDA_CAP);
    html += '<ul class="almanac-agenda-list">';
    for (const e of shown) html += agendaRowHtml(e, slotMinutes);
    html += '</ul>';
    if (all.length > AGENDA_CAP) {
        const label = runtime.agendaExpanded ? 'Show fewer' : `Show all (${all.length})`;
        html += `<button type="button" class="almanac-agenda-more" aria-expanded="${runtime.agendaExpanded}">${label}</button>`;
    }
    return `${html}</section>`;
}

function agendaRowHtml(e, slotMinutes) {
    const color = bandColors[e.band] || bandColors.all || '#6c757d';
    const band = escapeHtml(e.band);
    const region = escapeHtml(e.region);
    const time = e.all_day ? 'all day' : `${escapeHtml(e.start)}–${escapeHtml(e.end)} UTC`;
    const when = e.status === 'ongoing'
        ? '<span class="almanac-when is-now">usually open now</span>'
        : `<span class="almanac-when">${startsInLabel(e.starts_in_min)}</span>`;
    const today = e.open_today ? '<span class="almanac-today">open today</span>' : '';
    const local = e.all_day ? '' : `<span class="almanac-local">${escapeHtml(localRangeLabel(e, slotMinutes))}</span>`;
    return `<li class="almanac-agenda-row" data-status="${escapeHtml(e.status)}" data-band="${band}" data-region="${region}">` +
        `<span class="almanac-agenda-what"><span class="almanac-band-dot" style="background: ${color}" aria-hidden="true"></span>` +
        `<strong>${band}</strong> to ${region}: usually <span class="almanac-agenda-time">${time}</span> ` +
        `<span class="text-muted">(${e.peak_n}/${e.peak_m} days)</span></span>` +
        `<span class="almanac-agenda-meta">${when}${today}${local}</span></li>`;
}

function lanesHtml(data, lanes) {
    const mMin = Number(data.m_min) || 0;
    const slotMinutes = data.slot_minutes || DEFAULT_SLOT_MINUTES;
    const byKey = new Map();
    const bandM = new Map();
    const extraRegions = [];
    for (const lane of lanes) {
        byKey.set(`${lane.band}|${lane.region}`, lane);
        if (!bandM.has(lane.band)) bandM.set(lane.band, lane.m);
        if (!WSPR_REGIONS.includes(lane.region) && !extraRegions.includes(lane.region)) extraRegions.push(lane.region);
    }
    const bands = Array.from(bandM.keys()).sort((a, b) => bandRank(a) - bandRank(b));
    const own = regionForLocator(data.area?.grid4 || '');
    const regions = orderRegions(own, extraRegions);
    const left = nowLeft();

    let html = `<div class="almanac-lanes${runtime.loading ? ' is-loading' : ''}" role="group" aria-label="Openings by region and band, 24 hours UTC">`;
    html += '<div class="almanac-axis" aria-hidden="true"><span></span><span class="almanac-axis-ticks">' +
        '<span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></span></div>';
    for (const region of regions) {
        const isOwn = region === own;
        html += `<div class="almanac-region${isOwn ? ' is-own' : ''}" data-region="${escapeHtml(region)}">` +
            `<div class="almanac-region-label">${escapeHtml(region)}${isOwn ? ' <span class="text-muted">(your region)</span>' : ''}</div>` +
            '<div class="almanac-tracks">';
        for (const band of bands) {
            const lane = byKey.get(`${band}|${region}`) || { band, region, n: new Array(SLOTS).fill(0), m: bandM.get(band) || [] };
            html += laneHtml(lane, mMin, slotMinutes);
        }
        html += `<div class="almanac-now-layer" aria-hidden="true"><div class="almanac-now" style="left: ${left}"></div></div>`;
        html += '</div></div>';
    }
    return `${html}</div>`;
}

function laneHtml(lane, mMin, slotMinutes) {
    const { band, region } = lane;
    const color = bandColors[band] || bandColors.all || '#6c757d';
    const n = Array.isArray(lane.n) ? lane.n : [];
    const m = Array.isArray(lane.m) ? lane.m : [];
    let runs = '';
    let s = 0;
    while (s < SLOTS) {
        const ms = Number(m[s]) || 0;
        const ns = Number(n[s]) || 0;
        let len = 1;
        while (s + len < SLOTS && (Number(m[s + len]) || 0) === ms && (Number(n[s + len]) || 0) === ns) len += 1;
        const label = escapeHtml(slotLabel(band, region, s, len, ns, ms, mMin, slotMinutes));
        const common = `title="${label}" aria-label="${label}" role="img" data-slot="${s}"`;
        if (ms < mMin) {
            runs += `<span class="almanac-run is-unknown" style="flex-grow: ${len}" ${common}></span>`;
        } else if (ns === 0 || ms === 0) {
            runs += `<span class="almanac-run is-closed" style="flex-grow: ${len}" ${common}></span>`;
        } else {
            const opacity = Math.round((0.15 + 0.85 * Math.min(1, ns / ms)) * 100) / 100;
            runs += `<span class="almanac-run" style="flex-grow: ${len}; background: ${color}; opacity: ${opacity}" ${common}></span>`;
        }
        s += len;
    }
    const b = escapeHtml(band);
    const r = escapeHtml(region);
    return `<div class="almanac-lane" role="button" tabindex="-1" data-band="${b}" data-region="${r}" aria-label="${b} to ${r}: open the seasonal view">` +
        `<span class="almanac-lane-label" style="--band: ${color}">${b}</span><span class="almanac-lane-track">${runs}</span></div>`;
}

function legendHtml() {
    return '<div class="almanac-legend small text-muted">' +
        '<span class="almanac-legend-item"><span class="almanac-legend-ramp" aria-hidden="true"></span>opened on few &rarr; most days</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-unknown" aria-hidden="true"></span>not enough data</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-now" aria-hidden="true"></span>now (UTC)</span>' +
        '</div>';
}

function updateNowLines() {
    const body = bodyEl();
    if (!body) return;
    const left = nowLeft();
    body.querySelectorAll('.almanac-now').forEach((el) => { el.style.left = left; });
}

// --- Interaction ----------------------------------------------------------------

// Drill-down hook (U8 replaces the body): the seasonal month × hour view for
// one band × region lane. For now it only records the request.
export function openDrilldown(band, region) {
    if (!band || !region) return;
    runtime.drilldown = { band, region };
}

function setRovingLane(lane) {
    const root = lane.closest('.almanac-lanes');
    if (!root) return;
    root.querySelectorAll('.almanac-lane[tabindex="0"]').forEach((l) => {
        if (l !== lane) l.setAttribute('tabindex', '-1');
    });
    lane.setAttribute('tabindex', '0');
}

function onLanesKeydown(e) {
    const lane = e.target.closest?.('.almanac-lane');
    if (!lane) return;
    const all = Array.from(e.currentTarget.querySelectorAll('.almanac-lane'));
    const i = all.indexOf(lane);
    let next = null;
    switch (e.key) {
        case 'ArrowDown': next = all[i + 1] || null; break;
        case 'ArrowUp': next = all[i - 1] || null; break;
        case 'Home': next = all[0] || null; break;
        case 'End': next = all[all.length - 1] || null; break;
        case 'Enter':
        case ' ':
            e.preventDefault();
            openDrilldown(lane.dataset.band, lane.dataset.region);
            return;
        default:
            return;
    }
    e.preventDefault();
    if (next) {
        setRovingLane(next);
        next.focus();
    }
}

function attachHandlers(body) {
    const more = body.querySelector('.almanac-agenda-more');
    if (more) {
        more.addEventListener('click', () => {
            runtime.agendaExpanded = !runtime.agendaExpanded;
            render();
        });
    }
    const lanes = body.querySelector('.almanac-lanes');
    if (!lanes) return;
    const first = lanes.querySelector('.almanac-lane');
    if (first) first.setAttribute('tabindex', '0');
    lanes.addEventListener('click', (e) => {
        const lane = e.target.closest?.('.almanac-lane');
        if (!lane) return;
        setRovingLane(lane);
        openDrilldown(lane.dataset.band, lane.dataset.region);
    });
    lanes.addEventListener('keydown', onLanesKeydown);
    lanes.addEventListener('focusin', (e) => {
        const lane = e.target.closest?.('.almanac-lane');
        if (lane) setRovingLane(lane);
    });
}

// Re-renders rebuild the body; keep focus on the same lane / the agenda toggle.
function captureFocus(body) {
    const el = document.activeElement;
    if (!el || !body.contains(el)) return null;
    if (el.classList.contains('almanac-lane')) return { band: el.dataset.band, region: el.dataset.region };
    if (el.classList.contains('almanac-agenda-more')) return { more: true };
    return null;
}

function restoreFocus(body, focus) {
    if (!focus) return;
    let target = null;
    if (focus.more) {
        target = body.querySelector('.almanac-agenda-more');
    } else {
        target = Array.from(body.querySelectorAll('.almanac-lane'))
            .find((l) => l.dataset.band === focus.band && l.dataset.region === focus.region) || null;
        if (target) setRovingLane(target);
    }
    target?.focus();
}

// Test hooks.
export const __test = {
    runtime,
    openDrilldown,
    slotLabel,
    slotRangeLabel,
    startsInLabel,
    orderRegions,
    sortAgenda,
    render,
    fetchAlmanac,
    reset() {
        stopTimer();
        runtime.enabled = false;
        runtime.cache = null;
        runtime.error = null;
        runtime.loading = false;
        runtime.lastQth = '';
        runtime.agendaExpanded = false;
        runtime.drilldown = null;
        runtime.onLayoutChange = null;
    },
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
};
