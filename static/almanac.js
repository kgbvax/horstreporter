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
// Clicking a lane (or Enter/Space on the focused lane) calls openDrilldown
// (U8): the seasonal month × hour view for that band and region replaces the
// agenda and lanes inside the panel (GET /api/almanac/season). One row per
// calendar month Jan–Dec, each labelled with the year and layer it comes from
// (PSKR, or the backfilled WSPR layer). Back, Escape or a QTH change close it.

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
    // Open seasonal drill-down: { band, region, qth, loading, data, error }.
    drilldown: null,
    drillAbort: null,   // AbortController of the in-flight season fetch
    drillToken: 0,      // request token: a late season response is dropped
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
    closeDrilldownState();
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
        closeDrilldownState();
        runtime.lastQth = '';
        runtime.cache = null;
        runtime.error = null;
        runtime.loading = false;
        render();
        return;
    }

    const qthChanged = qth !== runtime.lastQth;
    runtime.lastQth = qth;
    runtime.loading = true;
    if (qthChanged) {
        runtime.agendaExpanded = false;
        // The seasonal view belongs to the old area: close it and put the
        // (about to be dimmed) lanes back.
        if (runtime.drilldown) {
            closeDrilldownState();
            render();
        }
    }
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
        closeDrilldownState();
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
    const view = body.querySelector('.almanac-lanes, .almanac-drill');
    if (view && runtime.cache) {
        if (view.classList.contains('almanac-lanes')) view.classList.add('is-loading');
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
// `where` is "20m to NA" (lanes) or "20m to NA, Dec 2025" (drill-down).
function slotLabel(where, start, len, n, m, mMin, slotMinutes) {
    const at = `${where}, ${slotRangeLabel(start, len, slotMinutes)}`;
    if (m < mMin) return `${at}: not enough data (${m} ${m === 1 ? 'day' : 'days'})`;
    return `${at}: opened ${n} of ${m} days`;
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
    if (runtime.drilldown) {
        html += drilldownHtml(runtime.drilldown);
        body.innerHTML = html;
        attachDrilldownHandlers(body);
        restoreFocus(body, focus);
        return;
    }
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
    const color = bandColor(e.band);
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

function bandColor(band) {
    return bandColors[band] || bandColors.all || '#6c757d';
}

// 48 slots → runs of equal (n, m): opacity from n/m in the band colour,
// neutral hatch when m < m_min, empty track when closed. `where` prefixes
// each run's title/aria-label (e.g. "20m to NA" or "20m to NA, Dec 2025").
function runsHtml(nIn, mIn, mMin, slotMinutes, color, where) {
    const n = Array.isArray(nIn) ? nIn : [];
    const m = Array.isArray(mIn) ? mIn : [];
    let runs = '';
    let s = 0;
    while (s < SLOTS) {
        const ms = Number(m[s]) || 0;
        const ns = Number(n[s]) || 0;
        let len = 1;
        while (s + len < SLOTS && (Number(m[s + len]) || 0) === ms && (Number(n[s + len]) || 0) === ns) len += 1;
        const label = escapeHtml(slotLabel(where, s, len, ns, ms, mMin, slotMinutes));
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
    return runs;
}

function laneHtml(lane, mMin, slotMinutes) {
    const { band, region } = lane;
    const color = bandColor(band);
    const runs = runsHtml(lane.n, lane.m, mMin, slotMinutes, color, `${band} to ${region}`);
    const b = escapeHtml(band);
    const r = escapeHtml(region);
    return `<div class="almanac-lane" role="button" tabindex="-1" data-band="${b}" data-region="${r}" aria-label="${b} to ${r}: open the seasonal view">` +
        `<span class="almanac-lane-label" style="--band: ${color}">${b}</span><span class="almanac-lane-track">${runs}</span></div>`;
}

function legendHtml(note = '') {
    return '<div class="almanac-legend small text-muted">' +
        '<span class="almanac-legend-item"><span class="almanac-legend-ramp" aria-hidden="true"></span>opened on few &rarr; most days</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-unknown" aria-hidden="true"></span>not enough data</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-now" aria-hidden="true"></span>now (UTC)</span>' +
        (note ? `<span class="almanac-legend-note">${escapeHtml(note)}</span>` : '') +
        '</div>';
}

// --- Seasonal drill-down (U8) ---------------------------------------------------

const MONTH_NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const LAYER_LABELS = { pskr: 'PSKR', wspr: 'WSPR' };
const LAYER_NOTE = 'WSPR and PSKR months come from different networks and are not directly comparable.';

function drillErrorText(kind) {
    switch (kind) {
        case 'unavailable': return 'The seasonal view is temporarily unavailable. Try again in a minute.';
        case 'invalid': return 'Could not load the seasonal view: invalid QTH, band or region.';
        case 'notfound': return 'Could not load the seasonal view: this QTH could not be located.';
        default: return 'Could not load the seasonal view. Try again later.';
    }
}

function drilldownHtml(dd) {
    const title = `${dd.band} to ${dd.region}`;
    let html = `<section class="almanac-drill" aria-label="${escapeHtml(title)} by month">` +
        '<div class="almanac-drill-head">' +
        '<button type="button" class="almanac-drill-back" aria-label="Back to all regions">&larr; Back</button>' +
        `<h3 class="almanac-drill-title">${escapeHtml(title)}</h3></div>`;
    if (dd.loading) {
        html += '<div class="almanac-message text-muted small" role="status">Loading…</div>';
    } else if (dd.error) {
        html += `<div class="almanac-message text-muted small" role="alert">${escapeHtml(drillErrorText(dd.error))}</div>`;
    } else if (dd.data) {
        html += monthsHtml(dd);
        html += legendHtml(LAYER_NOTE);
    }
    return `${html}</section>`;
}

function monthsHtml(dd) {
    const data = dd.data;
    const mMin = Number(data.m_min) || 0;
    const slotMinutes = data.slot_minutes || DEFAULT_SLOT_MINUTES;
    const color = bandColor(dd.band);
    const months = Array.isArray(data.months) ? data.months : [];
    const current = new Date().getUTCMonth();
    let html = `<div class="almanac-months" role="group" aria-label="${escapeHtml(dd.band)} to ${escapeHtml(dd.region)}, typical openings per month, 24 hours UTC">`;
    html += '<div class="almanac-axis" aria-hidden="true"><span></span><span class="almanac-axis-ticks">' +
        '<span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></span></div>';
    html += '<div class="almanac-month-rows">';
    for (let i = 0; i < 12; i += 1) {
        const mo = months[i] || {};
        const name = mo.name || MONTH_NAMES[i];
        const ok = mo.status === 'ok';
        const isCurrent = i === current;
        const cls = `almanac-month${isCurrent ? ' is-current' : ''}${ok ? '' : ' is-empty'}`;
        const cur = isCurrent ? ' aria-current="date"' : '';
        if (!ok) {
            html += `<div class="${cls}" data-month="${i + 1}"${cur}>` +
                `<span class="almanac-month-label">${escapeHtml(name)}</span>` +
                '<span class="almanac-month-track almanac-month-empty">not collected yet</span></div>';
            continue;
        }
        const layer = LAYER_LABELS[mo.layer] || String(mo.layer || '').toUpperCase();
        const when = `${name} ${mo.year}`;
        const runs = runsHtml(mo.n, mo.m, mMin, slotMinutes, color, `${dd.band} to ${dd.region}, ${when}`);
        html += `<div class="${cls}" data-month="${i + 1}" data-layer="${escapeHtml(mo.layer || '')}"${cur}>` +
            `<span class="almanac-month-label">${escapeHtml(`${when} · ${layer}`)}</span>` +
            `<span class="almanac-month-track">${runs}</span></div>`;
    }
    html += `<div class="almanac-now-layer" aria-hidden="true"><div class="almanac-now" style="left: ${nowLeft()}"></div></div>`;
    return `${html}</div></div>`;
}

function updateNowLines() {
    const body = bodyEl();
    if (!body) return;
    const left = nowLeft();
    body.querySelectorAll('.almanac-now').forEach((el) => { el.style.left = left; });
}

// --- Interaction ----------------------------------------------------------------

// Opens the seasonal month × hour view for one band × region lane in place
// of the agenda and lanes, and fetches /api/almanac/season for it.
export function openDrilldown(band, region) {
    if (!band || !region || !runtime.lastQth) return;
    closeDrilldownState();
    runtime.drilldown = { band, region, qth: runtime.lastQth, loading: true, data: null, error: null };
    render();
    bodyEl()?.querySelector('.almanac-drill-back')?.focus();
    fetchSeason();
}

// Drops the drill-down state and invalidates any season fetch in flight.
function closeDrilldownState() {
    if (runtime.drillAbort) {
        runtime.drillAbort.abort();
        runtime.drillAbort = null;
    }
    runtime.drillToken += 1;
    runtime.drilldown = null;
}

// Back / Escape: restore the lanes and return focus to the lane that opened
// the view (or the lanes' tab stop when that lane is gone).
function closeDrilldown() {
    const dd = runtime.drilldown;
    if (!dd) return;
    closeDrilldownState();
    render();
    const body = bodyEl();
    if (!body) return;
    const target = Array.from(body.querySelectorAll('.almanac-lane'))
        .find((l) => l.dataset.band === dd.band && l.dataset.region === dd.region)
        || body.querySelector('.almanac-lane[tabindex="0"]');
    if (target) {
        setRovingLane(target);
        target.focus();
    }
}

async function fetchSeason() {
    const dd = runtime.drilldown;
    if (!dd) return;
    const token = ++runtime.drillToken;
    const controller = new AbortController();
    runtime.drillAbort = controller;
    const url = `/api/almanac/season?qth=${encodeURIComponent(dd.qth)}` +
        `&band=${encodeURIComponent(dd.band)}&region=${encodeURIComponent(dd.region)}`;
    let outcome;
    try {
        const resp = await fetch(url, { signal: controller.signal });
        if (token !== runtime.drillToken) return;
        if (resp.ok) outcome = { data: await resp.json() };
        else if (resp.status === 503) outcome = { error: 'unavailable' };
        else if (resp.status === 400) outcome = { error: 'invalid' };
        else if (resp.status === 404) outcome = { error: 'notfound' };
        else outcome = { error: 'error' };
    } catch (err) {
        if (err?.name === 'AbortError' || token !== runtime.drillToken) return;
        console.warn('almanac season fetch failed:', err);
        outcome = { error: 'error' };
    } finally {
        if (runtime.drillAbort === controller) runtime.drillAbort = null;
    }
    if (token !== runtime.drillToken || runtime.drilldown !== dd) return;
    dd.loading = false;
    dd.data = outcome.data || null;
    dd.error = outcome.error || null;
    render();
}

function attachDrilldownHandlers(body) {
    const drill = body.querySelector('.almanac-drill');
    if (!drill) return;
    drill.querySelector('.almanac-drill-back')?.addEventListener('click', () => closeDrilldown());
    drill.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') return;
        e.preventDefault();
        e.stopPropagation();
        closeDrilldown();
    });
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
    if (el.classList.contains('almanac-drill-back')) return { back: true };
    return null;
}

function restoreFocus(body, focus) {
    if (!focus) return;
    let target = null;
    if (focus.more) {
        target = body.querySelector('.almanac-agenda-more');
    } else if (focus.back) {
        target = body.querySelector('.almanac-drill-back');
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
    closeDrilldown,
    reset() {
        stopTimer();
        runtime.enabled = false;
        runtime.cache = null;
        runtime.error = null;
        runtime.loading = false;
        runtime.lastQth = '';
        runtime.agendaExpanded = false;
        closeDrilldownState();
        runtime.onLayoutChange = null;
    },
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
};
