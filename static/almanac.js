import { state } from './state.js';
import { WSPR_REGIONS, bandColors, getMinSnrMode, regionForLocator, regionLabel } from './utils.js';
import { setPanelToggleState } from './panel-toggle.js';
import { escapeHtml } from './ui-helpers.js';

// almanac.js — the Almanac panel (plan 2026-09-28-001, U7): which regions are
// usually reachable from the operator's own area, per band and 30-min UTC
// slot, over the last 30 complete days ("opened N of M days"). Reads
// GET /api/almanac?qth= (docs/api.md). From-here-only by design: the area is
// always the operator's QTH (widened to neighbouring squares when sparse).
//
// Layout: header (area grid4 + radius, "approximate location" for a DXCC
// fallback), the schedule (usual openings now / within the next 3 h, as bars
// on a 3 h axis), then one
// block per far-end region (the operator's own region last) holding a thin
// lane per band. A lane's opacity follows n/m in the band's canonical colour;
// a slot with m < m_min is "not enough data" (neutral hatch, never "closed").
// A vertical line marks the current time.
//
// Local time: the API speaks UTC (48 slots from 00:00 UTC, start_slot,
// data-slot); the panel shows the browser's local time. Each lane is drawn
// rotated by the UTC offset (offset / 30 min slots, rounded to the nearest
// slot for zones like UTC+5:45, which the header then notes), so the axis
// 00/06/12/18/24 is local midnight; titles, the now line and the schedule read
// local time (a bar's title also gives UTC). A
// drill-down month row uses that month's own offset (mid-month), so summer
// and winter months each sit right across a DST change.
//
// Preliminary SNR views: with a floor and fewer than 10 SNR days the server
// judges cells against a smaller effective m_min and sets `preliminary`; the
// header then says how many SNR days the view rests on.
//
// Fetch discipline mirrors wspr-matrix.js: enable key in localStorage, #qth
// change listener, AbortController, plus a request token so a late response
// for an old QTH can never overwrite the current one.
//
// SNR floor (plan KTD13): the page's Min SNR control picks the floor — CW /
// SSB send their slider value as min_snr (none sends nothing = any SNR); the
// server snaps it to the nearest 5 dB tier (−20 … 0) and reports the applied
// tier, the SNR collection start and, per slot, the share of spots at or
// above the floor. A mode switch or slider move refetches (debounced).
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

const TICK_MS = 60_000;         // now-line refresh
const REFETCH_TICKS = 5;        // re-fetch every 5 ticks (server caches 60 s / 120 s)
const SCHEDULE_MIN = 180;       // schedule axis: now .. now + 3 h
const SNR_DEBOUNCE_MS = 400;    // slider input → refetch
const SLOTS = 48;
const DEFAULT_SLOT_MINUTES = 30;
// Full 30-day M_min (almanacMinActiveDays30): a preliminary view becomes a
// full one once this many days carry SNR data.
const FULL_M_MIN = 10;

const BAND_ORDER = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];

const TOGGLE_LABELS = { show: 'Show typical openings', hide: 'Hide typical openings' };

const runtime = {
    enabled: false,
    timer: null,
    ticks: 0,
    abortController: null,
    token: 0,
    loading: false,
    cache: null,        // last good /api/almanac payload (for the current QTH)
    error: null,        // { kind: 'invalid' | 'notfound' | 'unavailable', qth }
    refreshFailed: false, // last quiet refresh failed; cache shown as-is
    lastQth: '',
    lastMinSnr: null,   // min_snr of the last fetch (null = any SNR)
    snrTimer: null,     // debounce timer of a Min SNR change
    snrListener: null,  // delegated document listener (replaced on re-init)
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
    if (!panel) return;

    // Standalone mode: the panel has its own toggle. Inside the Conditions
    // dock there is none; cond-dock.js calls setAlmanacVisible instead.
    if (toggle) {
        if (localStorage.getItem(ENABLE_KEY) === 'true') {
            setAlmanacVisible(true);
        } else {
            setPanelToggleState(toggle, false, TOGGLE_LABELS);
        }
        toggle.addEventListener('click', () => setAlmanacVisible(!runtime.enabled));
    }

    // Refetch when the QTH changed: on #qth change, and on #fetch-form submit
    // (Enter in the field can submit before a change event fires). The form's
    // own submit handling is left alone (no preventDefault).
    const onQthMaybeChanged = () => {
        if (!runtime.enabled) return;
        if (currentQth() !== runtime.lastQth) fetchAlmanac();
    };
    document.getElementById('qth')?.addEventListener('change', onQthMaybeChanged);
    document.getElementById('fetch-form')?.addEventListener('submit', onQthMaybeChanged);

    // Min SNR mode radios and threshold sliders (both re-created by Svelte,
    // so the listener is delegated). Replace, don't stack, on re-init.
    if (runtime.snrListener) {
        document.removeEventListener('change', runtime.snrListener);
        document.removeEventListener('input', runtime.snrListener);
    }
    runtime.snrListener = (e) => {
        const t = e.target;
        if (t?.name === 'min-snr' || t?.id === 'cw-min-db' || t?.id === 'ssb-min-db') scheduleSnrRefetch();
    };
    document.addEventListener('change', runtime.snrListener);
    document.addEventListener('input', runtime.snrListener);
}

export function setAlmanacVisible(visible) {
    const panel = document.getElementById(PANEL_ID);
    const toggle = document.getElementById(TOGGLE_ID);
    if (!panel) return;
    if (runtime.enabled === visible && panel.classList.contains('is-hidden') === !visible) return;
    runtime.enabled = visible;
    panel.classList.toggle('is-hidden', !visible);
    if (toggle) setPanelToggleState(toggle, visible, TOGGLE_LABELS);
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
    if (runtime.snrTimer) {
        clearTimeout(runtime.snrTimer);
        runtime.snrTimer = null;
    }
    // Any response still in flight is now stale.
    runtime.token += 1;
    runtime.loading = false;
    if (runtime.drilldown) {
        // Put the lanes back so no drill-down with a dead Back button is
        // left in the (hidden) body for the next show.
        closeDrilldownState();
        render();
    } else {
        closeDrilldownState();
    }
}

// The page's current SNR floor in dB (null = any SNR): the slider value of
// the active Min SNR mode.
export function currentMinSnr() {
    const mode = getMinSnrMode();
    const id = mode === 'cw' ? 'cw-min-db' : mode === 'ssb' ? 'ssb-min-db' : null;
    if (!id) return null;
    const fallback = mode === 'cw' ? '-15' : '0';
    const v = Number.parseInt(document.getElementById(id)?.value ?? fallback, 10);
    return Number.isFinite(v) ? v : null;
}

// Debounced refetch after a Min SNR mode / slider change.
function scheduleSnrRefetch() {
    if (!runtime.enabled) return;
    if (runtime.snrTimer) clearTimeout(runtime.snrTimer);
    runtime.snrTimer = setTimeout(() => {
        runtime.snrTimer = null;
        if (!runtime.enabled) return;
        const cur = currentMinSnr();
        if (cur !== runtime.lastMinSnr) fetchAlmanac();
        const dd = runtime.drilldown;
        // Same band × region, new floor.
        if (dd && dd.minSnr !== cur) openDrilldown(dd.band, dd.region);
    }, SNR_DEBOUNCE_MS);
}

function minSnrParam(v) {
    return v === null || v === undefined ? '' : `&min_snr=${encodeURIComponent(v)}`;
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
        runtime.refreshFailed = false;
        runtime.loading = false;
        render();
        return;
    }

    const minSnr = currentMinSnr();
    const qthChanged = qth !== runtime.lastQth;
    const snrChanged = minSnr !== runtime.lastMinSnr;
    // A background refresh of the same area with data on screen: a failure
    // keeps that data (and any open drill-down) instead of wiping it.
    const keepStale = quiet && !qthChanged && !snrChanged && !!runtime.cache;
    runtime.lastQth = qth;
    runtime.lastMinSnr = minSnr;
    runtime.loading = true;
    if (qthChanged) {
        runtime.refreshFailed = false;
        // The seasonal view belongs to the old area: close it and put the
        // (about to be dimmed) lanes back.
        if (runtime.drilldown) {
            closeDrilldownState();
            render();
        }
    }
    // A background refresh of the same QTH keeps the lanes steady; anything
    // else dims them (or shows "Loading" on a first load).
    if (!quiet || qthChanged || snrChanged) showLoading();

    const controller = new AbortController();
    runtime.abortController = controller;
    let outcome;
    try {
        const resp = await fetch(`/api/almanac?qth=${encodeURIComponent(qth)}${minSnrParam(minSnr)}`, { signal: controller.signal });
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
        runtime.refreshFailed = false;
    } else if (keepStale) {
        runtime.refreshFailed = true;
    } else {
        // Never leave a previous area's lanes on screen next to an error.
        closeDrilldownState();
        runtime.cache = null;
        runtime.refreshFailed = false;
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
        body.innerHTML = loadingHtml();
    }
    body.setAttribute('aria-busy', 'true');
}

// --- Formatting helpers ------------------------------------------------------

function pad2(v) {
    return String(v).padStart(2, '0');
}

// Minutes after midnight → "HH:MM" (1440 → "24:00", used as a range end).
function hhmm(minutes) {
    return `${pad2(Math.floor(minutes / 60))}:${pad2(minutes % 60)}`;
}

function mod(v, n) {
    return ((v % n) + n) % n;
}

// --- Local time ---------------------------------------------------------------

// Browser UTC offset in minutes EAST of UTC (+120 = CEST) at `date`.
function tzOffsetMinutes(date = new Date()) {
    const v = -date.getTimezoneOffset();
    return v === 0 ? 0 : v; // no -0
}

// Offset of a calendar month (mid-month, midday), so a month row follows
// its own DST state. `month` is 1–12.
function monthOffsetMinutes(year, month) {
    const y = Number(year) || new Date().getFullYear();
    return tzOffsetMinutes(new Date(y, Number(month) - 1, 15, 12));
}

// Whole slots to rotate UTC data by (nearest slot for non-30-min offsets).
function slotShift(offsetMin, slotMinutes = DEFAULT_SLOT_MINUTES) {
    const v = Math.round(offsetMin / slotMinutes);
    return v === 0 ? 0 : v;
}

// UTC-ordered slots → local display order: out[i] = arr[i − shift] (wrapping).
function rotateSlots(arr, shift) {
    if (!Array.isArray(arr)) return arr;
    const out = new Array(SLOTS);
    for (let i = 0; i < SLOTS; i += 1) out[i] = arr[mod(i - shift, SLOTS)];
    return out;
}

// "UTC+2", "UTC−5", "UTC+5:45", "UTC".
function offsetLabel(offsetMin) {
    if (!offsetMin) return 'UTC';
    const a = Math.abs(offsetMin);
    const h = Math.floor(a / 60);
    const m = a % 60;
    return `UTC${offsetMin < 0 ? '\u2212' : '+'}${h}${m ? `:${pad2(m)}` : ''}`;
}

// Note for zones whose offset is not a whole number of slots (UTC+5:45): the
// lanes are drawn rounded to the nearest slot; '' otherwise.
function roundingNote(date = new Date(), slotMinutes = DEFAULT_SLOT_MINUTES) {
    const off = tzOffsetMinutes(date);
    return off % slotMinutes === 0
        ? ''
        : `Lanes rounded to the nearest ${slotMinutes} min (${offsetLabel(off)}).`;
}

// Local "HH:MM" of `utcMinutes` after 00:00 UTC.
function localHHMM(utcMinutes, offsetMin) {
    return hhmm(mod(utcMinutes + offsetMin, 1440));
}

// "16:00 local" for one UTC slot, "13:00–14:30 local" for a run (end
// exclusive; a run ending at local midnight reads "24:00").
function slotRangeLabel(start, len, slotMinutes = DEFAULT_SLOT_MINUTES, offsetMin = 0) {
    const a = mod(start * slotMinutes + offsetMin, 1440);
    if (len <= 1) return `${hhmm(a)} local`;
    let b = a + len * slotMinutes;
    if (b > 1440) b -= 1440;
    return `${hhmm(a)}–${hhmm(b)} local`;
}

// "−10 dB" (typographic minus).
function dbLabel(v) {
    const n = Number(v);
    return `${n < 0 ? '\u2212' : ''}${Math.abs(n)} dB`;
}

// "64% of spots ≥ −10 dB" (share 0..1 at the applied tier).
function shareLabel(share, tier) {
    return `${Math.round(Number(share) * 100)}% of spots \u2265 ${dbLabel(tier)}`;
}

// Accessible name and title of one slot (or run of equal slots).
// `where` is "20m to NA" (lanes) or "20m to NA, Dec 2025" (drill-down).
// With an SNR floor, `share` (0..1 or null) and `tier` add the SNR share.
// `start` is the run's first UTC slot; offsetMin the local offset used.
function slotLabel(where, start, len, n, m, mMin, slotMinutes, share = null, tier = null, offsetMin = 0) {
    const at = `${where}, ${slotRangeLabel(start, len, slotMinutes, offsetMin)}`;
    if (m < mMin) return `${at}: not enough data (${m} ${m === 1 ? 'day' : 'days'})`;
    const snr = share !== null && share !== undefined && tier !== null && tier !== undefined
        ? ` \u00b7 ${shareLabel(share, tier)}` : '';
    return `${at}: opened ${n} of ${m} days${snr}`;
}

// Header line for the SNR floor: "FT8/FT4 openings — any SNR" or
// "— spots ≥ −10 dB (slider −12 → −10 dB tier)", plus the SNR start.
function snrHeaderText(data) {
    const tier = data?.snr_tier;
    if (tier === null || tier === undefined) return 'FT8/FT4 openings \u2014 any SNR';
    let text = `FT8/FT4 openings \u2014 spots \u2265 ${dbLabel(tier)}`;
    const req = data.min_snr;
    if (req !== null && req !== undefined && Number(req) !== Number(tier)) {
        text += ` (slider ${dbLabel(req).replace(' dB', '')} \u2192 ${dbLabel(tier)} tier)`;
    }
    text += data.snr_available && data.snr_since
        ? `; SNR data since ${data.snr_since}`
        : '; no SNR data collected yet';
    return text;
}

function startsInLabel(min) {
    const v = Math.max(0, Math.round(Number(min) || 0));
    if (v < 60) return `in ${v} min`;
    const h = Math.floor(v / 60);
    const rest = v % 60;
    return rest ? `in ${h} h ${rest} min` : `in ${h} h`;
}

// Local "20:00–22:00" of an agenda window (UTC start_slot / len_slots);
// a window across local midnight reads "23:00–01:00".
function localRangeLabel(entry, slotMinutes = DEFAULT_SLOT_MINUTES, offsetMin = tzOffsetMinutes()) {
    const start = Number(entry.start_slot) * slotMinutes;
    const end = start + Number(entry.len_slots) * slotMinutes;
    return `${localHHMM(start, offsetMin)}–${localHHMM(end, offsetMin)}`;
}

// Now as a fraction of the local day on the drawn (slot-rotated) grid: the
// exact local time for whole-slot offsets.
function nowFraction(date = new Date(), slotMinutes = DEFAULT_SLOT_MINUTES) {
    const utc = date.getUTCHours() * 60 + date.getUTCMinutes();
    const shiftMin = slotShift(tzOffsetMinutes(date), slotMinutes) * slotMinutes;
    return mod(utc + shiftMin, 1440) / 1440;
}

function nowLeft() {
    return `${(nowFraction() * 100).toFixed(4)}%`;
}

// Header note of a preliminary SNR view: "preliminary — based on 3 days of
// SNR data (full view from 2026-10-05)" ('' otherwise).
function preliminaryText(data) {
    if (!data?.preliminary) return '';
    const days = Number(data.snr_days) || 0;
    let text = `preliminary \u2014 based on ${days} ${days === 1 ? 'day' : 'days'} of SNR data`;
    const since = Date.parse(`${data.snr_since}T00:00:00Z`);
    if (Number.isFinite(since)) {
        const full = new Date(since + FULL_M_MIN * 86_400_000).toISOString().slice(0, 10);
        text += ` (full view from ${full})`;
    }
    return text;
}

// Canonical band position; unknown bands sort after all known ones.
const BAND_RANK = new Map(BAND_ORDER.map((b, i) => [b, i]));
function bandRank(band) {
    return BAND_RANK.get(band) ?? BAND_ORDER.length;
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
        body.innerHTML = message('Enter your locator to see typical openings.');
        return;
    }
    if (runtime.error) {
        body.innerHTML = message(errorText(runtime.error));
        return;
    }
    const data = runtime.cache;
    if (!data) {
        body.innerHTML = runtime.loading
            ? loadingHtml()
            : '';
        return;
    }

    let html = headerHtml(data);
    html += `<div class="almanac-status text-muted small" role="status">${statusText()}</div>`;
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
    html += scheduleHtml(data);
    html += lanesHtml(data, lanes);
    html += legendHtml();
    body.innerHTML = html;
    attachHandlers(body);
    restoreFocus(body, focus);
}

// Status line above the lanes / drill-down: in-flight fetch, or a quiet
// refresh that failed while the last good data stays on screen.
function statusText() {
    if (runtime.loading) return 'Loading…';
    if (runtime.refreshFailed) return 'Couldn\'t refresh; showing last data';
    return '';
}

// Status line shown while a fetch is in flight (lanes body or drill-down).
function loadingHtml(classes = 'almanac-status almanac-message') {
    return `<div class="${classes} text-muted small" role="status">Loading…</div>`;
}

function message(text) {
    return `<div class="almanac-message text-muted small">${escapeHtml(text)}</div>`;
}

function errorText(err) {
    switch (err.kind) {
        case 'invalid': return 'Invalid QTH. Enter a Maidenhead locator (for example JO32) or a callsign.';
        case 'notfound': return `Could not locate ${err.qth}. Enter a locator instead.`;
        default: return 'Typical openings are temporarily unavailable. Try again in a minute.';
    }
}

function headerHtml(data) {
    const area = data.area || {};
    const grid4 = area.grid4 || data.qth || '';
    const radius = Number(area.radius) || 0;
    const squares = Array.isArray(area.squares) ? area.squares.length : 1;
    const approx = area.approximate
        ? '<div class="almanac-approx">Approximate location: taken from the country centre. Enter a locator for your own square.</div>'
        : '';
    const sq = `${squares} ${squares === 1 ? 'square' : 'squares'}`;
    const prelim = preliminaryText(data);
    const rounding = roundingNote(new Date(), data.slot_minutes || DEFAULT_SLOT_MINUTES);
    return `<div class="almanac-area small"><span class="almanac-area-main"><strong>${escapeHtml(grid4)}</strong>, radius ${radius} (${sq})</span>${approx}` +
        `<div class="almanac-snr text-muted">${escapeHtml(snrHeaderText(data))}</div>` +
        (rounding ? `<div class="text-muted">${escapeHtml(rounding)}</div>` : '') +
        (prelim ? `<div class="almanac-preliminary">${escapeHtml(prelim)}</div>` : '') +
        '</div>';
}

// Schedule: the usual openings now and within the next 3 h as bars on a 3 h
// axis starting at `now`. Opacity follows n/m like the lanes; an outlined bar
// is open now; a window that runs past the axis fades out at the right edge.
export function scheduleHtml(data, now = new Date()) {
    const slotMinutes = data.slot_minutes || DEFAULT_SLOT_MINUTES;
    const offsetMin = tzOffsetMinutes(now);
    const nowUtcMin = now.getUTCHours() * 60 + now.getUTCMinutes();
    const items = [];
    for (const e of sortAgenda(Array.isArray(data.agenda) ? data.agenda : [], Number(data.now_slot) || 0)) {
        const ongoing = e.status === 'ongoing';
        const startOff = ongoing ? 0 : Number(e.starts_in_min) || 0;
        if (startOff >= SCHEDULE_MIN) continue;
        const endOff = ongoing
            ? (e.all_day ? SCHEDULE_MIN : mod((Number(e.start_slot) + Number(e.len_slots)) * slotMinutes - nowUtcMin, 1440))
            : startOff + Number(e.len_slots) * slotMinutes;
        items.push({ e, ongoing, startOff, endOff });
    }
    let html = '<section class="almanac-schedule" aria-label="Usual openings in the next 3 hours">';
    if (items.length === 0) {
        return `${html}<div class="text-muted small">No usual openings in the next 3 h</div></section>`;
    }
    html += scheduleAxisHtml(nowUtcMin, offsetMin);
    const groups = [
        ['Open now, usually', items.filter((i) => i.ongoing)],
        ['Opens within 3 h', items.filter((i) => !i.ongoing)],
    ];
    for (const [title, rows] of groups) {
        if (rows.length === 0) continue;
        html += `<div class="almanac-subhead">${title}</div><ul class="cond-sched-list">`;
        for (const it of rows) html += scheduleRowHtml(it, slotMinutes, data.snr_tier, offsetMin);
        html += '</ul>';
    }
    return `${html}</section>`;
}

// Hour labels (local time) and 30-minute ticks along the 3 h axis.
function scheduleAxisHtml(nowUtcMin, offsetMin) {
    const localNow = nowUtcMin + offsetMin;
    let marks = '';
    for (let t = Math.ceil(localNow / 30) * 30; t <= localNow + SCHEDULE_MIN; t += 30) {
        const left = ((t - localNow) / SCHEDULE_MIN) * 100;
        const label = t % 60 === 0 ? `<span class="cond-sched-hour">${pad2(mod(t, 1440) / 60)}</span>` : '';
        marks += `<span class="cond-sched-tick" style="left: ${left.toFixed(2)}%">${label}</span>`;
    }
    return `<div class="cond-sched-axis" aria-hidden="true"><span></span><span class="cond-sched-track">${marks}</span><span></span></div>`;
}

function scheduleRowHtml({ e, ongoing, startOff, endOff }, slotMinutes, tier, offsetMin) {
    const color = bandColor(e.band);
    const band = escapeHtml(e.band);
    const region = escapeHtml(e.region);
    const regionText = escapeHtml(regionLabel(e.region));
    const n = Number(e.peak_n) || 0;
    const m = Number(e.peak_m) || 0;
    const clipped = endOff > SCHEDULE_MIN;
    const left = (startOff / SCHEDULE_MIN) * 100;
    const width = Math.max(1, ((Math.min(endOff, SCHEDULE_MIN) - startOff) / SCHEDULE_MIN) * 100);
    const opacity = Math.round((0.15 + 0.85 * (m > 0 ? Math.min(1, n / m) : 0)) * 100) / 100;
    const range = e.all_day ? 'all day' : localRangeLabel(e, slotMinutes, offsetMin);
    const share = e.peak_share !== null && e.peak_share !== undefined && tier !== null && tier !== undefined
        ? `, ${shareLabel(e.peak_share, tier)}` : '';
    const title = escapeHtml(`${e.band} to ${regionLabel(e.region)}: usually ${range}` +
        `${e.all_day ? '' : ` (${e.start}\u2013${e.end} UTC)`}, ${ongoing ? 'open now' : startsInLabel(e.starts_in_min)}, ` +
        `opened ${n} of ${m} days${share}${e.open_today ? ', open today' : ''}`);
    return `<li class="cond-sched-row" data-status="${escapeHtml(e.status)}" data-band="${band}" data-region="${region}" title="${title}">` +
        `<span class="cond-sched-what"><span class="almanac-band-dot" style="background: ${color}" aria-hidden="true"></span><strong>${band}</strong> ${regionText}</span>` +
        '<span class="cond-sched-track">' +
        `<span class="cond-sched-bar${ongoing ? ' is-now' : ''}${clipped ? ' is-cut' : ''}" style="left: ${left.toFixed(2)}%; width: ${width.toFixed(2)}%; --band: ${color}">` +
        `<span class="almanac-run" style="background: ${color}; opacity: ${opacity}"></span></span></span>` +
        `<span class="cond-sched-n">${n} of ${m}</span></li>`;
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
    // Safeguard against payload order: O(1) rank lookups; the stable sort keeps
    // unknown bands in arrival order after the known ones.
    const bands = Array.from(bandM.keys()).sort((a, b) => bandRank(a) - bandRank(b));
    const own = regionForLocator(data.area?.grid4 || '');
    const regions = orderRegions(own, extraRegions);
    const left = nowLeft();
    const offset = tzOffsetMinutes();

    let html = `<div class="almanac-lanes${runtime.loading ? ' is-loading' : ''}" role="group" aria-label="Openings by region and band, 24 hours local time">`;
    html += axisHtml();
    for (const region of regions) {
        const isOwn = region === own;
        html += `<div class="almanac-region${isOwn ? ' is-own' : ''}" data-region="${escapeHtml(region)}">` +
            `<div class="almanac-region-label">${escapeHtml(regionLabel(region))}${isOwn ? ' <span class="text-muted">(your region)</span>' : ''}</div>` +
            '<div class="almanac-tracks">';
        for (const band of bands) {
            const lane = byKey.get(`${band}|${region}`) || { band, region, n: new Array(SLOTS).fill(0), m: bandM.get(band) || [] };
            html += laneHtml(lane, mMin, slotMinutes, data.snr_tier, offset);
        }
        html += `<div class="almanac-now-layer" aria-hidden="true"><div class="almanac-now" style="left: ${left}"></div></div>`;
        html += '</div></div>';
    }
    return `${html}</div>`;
}

// 00–24 h tick row shared by the lanes and the seasonal drill-down.
function axisHtml() {
    return '<div class="almanac-axis" aria-hidden="true"><span></span><span class="almanac-axis-ticks">' +
        '<span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></span></div>';
}

function bandColor(band) {
    return bandColors[band] || bandColors.all || '#6c757d';
}

// 48 UTC slots → runs of equal (n, m, share) in local display order (the
// arrays rotated by `offsetMin`): opacity from n/m in the band colour,
// neutral hatch when m < m_min, empty track when closed. `where` prefixes
// each run's title/aria-label (e.g. "20m to NA" or "20m to NA, Dec 2025");
// with an SNR floor (`tier`) the label carries the slot's share. data-slot
// is the run's first UTC slot.
function runsHtml(nIn, mIn, mMin, slotMinutes, color, where, shareIn = null, tier = null, offsetMin = 0) {
    const shift = slotShift(offsetMin, slotMinutes);
    const n = Array.isArray(nIn) ? rotateSlots(nIn, shift) : [];
    const m = Array.isArray(mIn) ? rotateSlots(mIn, shift) : [];
    const sh = Array.isArray(shareIn) ? rotateSlots(shareIn, shift) : [];
    const shareAt = (i) => (sh[i] === null || sh[i] === undefined ? null : Number(sh[i]));
    let runs = '';
    let s = 0;
    while (s < SLOTS) {
        const ms = Number(m[s]) || 0;
        const ns = Number(n[s]) || 0;
        const ss = shareAt(s);
        let len = 1;
        while (s + len < SLOTS && (Number(m[s + len]) || 0) === ms && (Number(n[s + len]) || 0) === ns &&
            shareAt(s + len) === ss) len += 1;
        const utcStart = mod(s - shift, SLOTS);
        const label = escapeHtml(slotLabel(where, utcStart, len, ns, ms, mMin, slotMinutes, ss, tier, offsetMin));
        const common = `title="${label}" aria-label="${label}" role="img" data-slot="${utcStart}"`;
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

function laneHtml(lane, mMin, slotMinutes, tier = null, offsetMin = 0) {
    const { band, region } = lane;
    const color = bandColor(band);
    const runs = runsHtml(lane.n, lane.m, mMin, slotMinutes, color, `${band} to ${regionLabel(region)}`, lane.share, tier, offsetMin);
    const b = escapeHtml(band);
    const r = escapeHtml(region);
    return `<div class="almanac-lane" role="button" tabindex="-1" data-band="${b}" data-region="${r}" aria-label="${b} to ${escapeHtml(regionLabel(lane.region))}: open the seasonal view">` +
        `<span class="almanac-lane-label" style="--band: ${color}">${b}</span><span class="almanac-lane-track">${runs}</span></div>`;
}

function legendHtml(note = '') {
    return '<div class="almanac-legend small text-muted">' +
        '<span class="almanac-legend-item"><span class="almanac-legend-ramp" aria-hidden="true"></span>opened on few &rarr; most days</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-unknown" aria-hidden="true"></span>not enough data</span>' +
        '<span class="almanac-legend-item"><span class="almanac-legend-now" aria-hidden="true"></span>now</span>' +
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
    const title = `${dd.band} to ${regionLabel(dd.region)}`;
    let html = `<section class="almanac-drill" aria-label="${escapeHtml(title)} by month">` +
        '<div class="almanac-drill-head">' +
        '<button type="button" class="almanac-drill-back" aria-label="Back to all regions">&larr; Back</button>' +
        `<h3 class="almanac-drill-title">${escapeHtml(title)}</h3></div>`;
    if (dd.loading) {
        html += loadingHtml('almanac-message');
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
    let html = `<div class="almanac-months" role="group" aria-label="${escapeHtml(dd.band)} to ${escapeHtml(regionLabel(dd.region))}, typical openings per month, 24 hours local time">`;
    html += axisHtml();
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
        // A preliminary (SNR floor) month is judged against its own m_min.
        const moMin = Number(mo.m_min) > 0 ? Number(mo.m_min) : mMin;
        const offset = monthOffsetMinutes(mo.year, mo.month || i + 1);
        const runs = runsHtml(mo.n, mo.m, moMin, slotMinutes, color, `${dd.band} to ${regionLabel(dd.region)}, ${when}`, mo.share, data.snr_tier, offset);
        const prelim = mo.preliminary ? ' · preliminary' : '';
        html += `<div class="${cls}" data-month="${i + 1}" data-layer="${escapeHtml(mo.layer || '')}"${cur}>` +
            `<span class="almanac-month-label">${escapeHtml(`${when} · ${layer}${prelim}`)}</span>` +
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
    runtime.drilldown = { band, region, qth: runtime.lastQth, minSnr: runtime.lastMinSnr, loading: true, data: null, error: null };
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
    const target = findLane(body, dd.band, dd.region)
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
        `&band=${encodeURIComponent(dd.band)}&region=${encodeURIComponent(dd.region)}${minSnrParam(dd.minSnr)}`;
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

// The rendered lane for band × region, or null.
function findLane(body, band, region) {
    return Array.from(body.querySelectorAll('.almanac-lane'))
        .find((l) => l.dataset.band === band && l.dataset.region === region) || null;
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
    if (el.classList.contains('almanac-drill-back')) return { back: true };
    return null;
}

function restoreFocus(body, focus) {
    if (!focus) return;
    let target = null;
    if (focus.back) {
        target = body.querySelector('.almanac-drill-back');
    } else {
        target = findLane(body, focus.band, focus.region);
        if (target) setRovingLane(target);
    }
    target?.focus();
}

// Test hooks.
export const __test = {
    runtime,
    openDrilldown,
    slotLabel,
    snrHeaderText,
    preliminaryText,
    slotShift,
    rotateSlots,
    offsetLabel,
    localRangeLabel,
    nowFraction,
    currentMinSnr,
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
        runtime.refreshFailed = false;
        runtime.loading = false;
        runtime.lastQth = '';
        runtime.lastMinSnr = null;
        closeDrilldownState();
        runtime.onLayoutChange = null;
    },
    PANEL_ID,
    TOGGLE_ID,
    BODY_ID,
    ENABLE_KEY,
    SNR_DEBOUNCE_MS,
};
