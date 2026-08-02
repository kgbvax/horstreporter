import { bandColors, hexToRgba } from '../utils.js';

const API = {
    params: '/api/proplab/v1/params',
    ladder: '/api/proplab/v1/ladder',
    fusion: '/api/proplab/v1/fusion',
    baseline: '/api/dx_conditions'
};

const B_PARAMS = [
    { key: 'min_links', label: 'Min links', min: 1, max: 10, step: 1 },
    { key: 'snr_floor_ft8', label: 'SNR floor FT8 (dB)', min: -30, max: 0, step: 1 },
    { key: 'snr_floor_rbn', label: 'SNR floor RBN (dB)', min: 0, max: 40, step: 1 },
    { key: 'witness_min', label: 'Witness min', min: 1, max: 10, step: 1 },
    { key: 'es_min_km', label: 'Es min km', min: 0, max: 2000, step: 50 },
    { key: 'es_max_km', label: 'Es max km', min: 0, max: 5000, step: 50 },
    { key: 'coherence_min_bands', label: 'Coherence min bands', min: 1, max: 7, step: 1 },
    { key: 'cusum_drift', label: 'CUSUM drift', min: 0, max: 2, step: 0.1 },
    { key: 'cusum_threshold', label: 'CUSUM threshold', min: 0, max: 10, step: 0.5 },
    { key: 'ewma_alpha', label: 'EWMA alpha', min: 0, max: 1, step: 0.05 },
    { key: 'expected_lookback_days', label: 'Lookback days', min: 1, max: 90, step: 1 },
    { key: 'term_min_east_deg', label: 'Term min east deg', min: 0, max: 90, step: 1 },
    { key: 'term_max_east_deg', label: 'Term max east deg', min: 0, max: 90, step: 1 },
    { key: 'term_deg_per_hour', label: 'Term deg/h', min: 0, max: 60, step: 1 }
];

const C_PARAMS = [
    { key: 'lookback_days', label: 'Lookback days', min: 1, max: 90, step: 1 },
    { key: 'quantile_lo', label: 'Quantile lo', min: 0, max: 0.5, step: 0.05 },
    { key: 'quantile_hi', label: 'Quantile hi', min: 0.5, max: 1, step: 0.05 },
    { key: 'guardband_sigma', label: 'Guardband sigma', min: 0, max: 6, step: 0.5 },
    { key: 'guardband_weight', label: 'Guardband weight', min: 0, max: 1, step: 0.05 },
    { key: 'witness_per_day_min', label: 'Witness/day min', min: 0, max: 50, step: 1 },
    { key: 'open_ratio', label: 'Open ratio', min: 0.5, max: 5, step: 0.1 },
    { key: 'closed_ratio', label: 'Closed ratio', min: 0, max: 1, step: 0.05 },
    { key: 'kp_absorb', label: 'Kp absorb', min: 0, max: 9, step: 0.5 },
    { key: 'sfi_low', label: 'SFI low', min: 0, max: 200, step: 1 },
    { key: 'xray_min_class', label: 'X-ray min class', text: true }
];

let defaults = { b: {}, c: {} };
let refreshTimer = null;

const CANON_BANDS = ['160m','80m','60m','40m','30m','20m','17m','15m','12m','10m','6m','4m','2m'];
const HISTORY_MAX = 80;
const HISTORY_TICKS_SHOWN = 40;
const FUSION_PRECEDENCE = ['open_confirmed','open_unconfirmed','closed_but_active','closed_with_cause','closed','insufficient_data'];

let lastNorm = null;
let history = [];
let expandedBand = null;

function el(id) { return document.getElementById(id); }

async function getJson(url) {
    const res = await fetch(url);
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    return res.json();
}

function formatDate(ts) {
    if (!ts) return '-';
    return new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function bandStyle(band) {
    const color = bandColors[band] || '#999';
    return `border-left-color: ${color}; background-color: ${hexToRgba(color, 0.06)};`;
}

// Region classifier for midpoint cells. Mirrors internal/proplab/region.go.
// Keep these bounding boxes in sync with that file.
function locatorToLatLng(loc) {
    if (!loc || loc.length < 4) return { lat: 0, lng: 0 };
    const latField = loc.toUpperCase().charCodeAt(1) - 'A'.charCodeAt(0);
    const lonField = loc.toUpperCase().charCodeAt(0) - 'A'.charCodeAt(0);
    const latSq = parseInt(loc[3], 10);
    const lonSq = parseInt(loc[2], 10);
    const lat = -90 + latField * 10 + latSq * 1 + 0.5;
    const lng = -180 + lonField * 20 + lonSq * 2 + 1;
    return { lat, lng };
}

function regionFromLocator(loc) {
    if (!loc) return '';
    const { lat, lng } = locatorToLatLng(loc);
    if (lat <= -60) return 'AN';
    if (lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146) return 'JA';
    if (lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154) return 'KH6';
    if (lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60) return 'CAR';
    if (lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180) return 'VK';
    if (lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45) return 'EU';
    if (lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55) return 'AF';
    if (lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50) return 'NA';
    if (lat >= -60 && lat < 15 && lng >= -90 && lng <= -30) return 'SA';
    if (lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180) return 'AS';
    if (lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130)) return 'OC';
    return '';
}

function regionCountsFromCells(cells) {
    const out = {};
    (cells || []).forEach(c => {
        const r = regionFromLocator(c);
        if (r) out[r] = (out[r] || 0) + 1;
    });
    return out;
}

function stateClass(state) {
    if (!state) return '';
    const s = String(state).toLowerCase();
    if (s.includes('open')) return 'proplab-state-open';
    if (s.includes('active') || s.includes('rising') || s.includes('spike')) return 'proplab-state-active';
    if (s.includes('insufficient')) return 'proplab-state-insufficient';
    return 'proplab-state-closed';
}

function buildParamControls(containerId, params, values, prefix) {
    const container = el(containerId);
    container.innerHTML = '';
    params.forEach(p => {
        const wrap = document.createElement('div');
        wrap.className = 'proplab-param';
        wrap.title = p.label;
        const id = `${prefix}-${p.key}`;

        if (p.text) {
            wrap.innerHTML = `
                <label for="${id}">${p.label}</label>
                <input type="text" id="${id}" class="form-control form-control-sm" value="${values[p.key] || ''}">
                <span></span>
            `;
        } else {
            const val = values[p.key];
            wrap.innerHTML = `
                <label for="${id}">${p.label}</label>
                <input type="range" id="${id}-range" min="${p.min}" max="${p.max}" step="${p.step}" value="${val}">
                <input type="number" id="${id}" class="form-control form-control-sm" min="${p.min}" max="${p.max}" step="${p.step}" value="${val}">
            `;
            const range = wrap.querySelector(`#${id}-range`);
            const num = wrap.querySelector(`#${id}`);
            range.addEventListener('input', () => { num.value = range.value; });
            num.addEventListener('input', () => { range.value = num.value; });
        }
        container.appendChild(wrap);
    });
}

function collectParams(params, prefix) {
    const out = {};
    params.forEach(p => {
        const id = `${prefix}-${p.key}`;
        const v = el(id).value;
        if (v === '') return;
        if (p.text) {
            out[p.key] = v;
        } else {
            const n = parseFloat(v);
            if (!Number.isNaN(n)) out[p.key] = n;
        }
    });
    return out;
}

function queryString(params) {
    return Object.entries(params)
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`)
        .join('&');
}

function normConf(v) {
    if (v === undefined || v === null || Number.isNaN(v)) return null;
    const n = Number(v);
    if (n > 1) return Math.min(n / 100, 1); // A confidence is 0-99
    return Math.max(0, Math.min(1, n));
}

function normalizeA(data) {
    if (data.error) return { unavailable: true, byBand: {} };
    const byBand = {};
    (data.bands || []).forEach(b => {
        const status = String(b.status || '').toLowerCase();
        const hasData = (b.unique_links || 0) > 0;
        let state, label;
        if (status === 'green') { state = 'open'; label = b.condition || 'open'; }
        else if (status === 'yellow') { state = 'maybe'; label = b.condition || 'watch'; }
        else if (status === 'red') { state = 'closed'; label = b.condition || 'poor'; }
        else { // grey
            state = hasData ? 'maybe' : 'nodata';
            label = hasData ? (b.condition || 'unclear') : 'no data';
        }
        const conf = normConf(b.confidence);
        const title = [
            `A baseline: ${b.status || '-'} (${b.condition || '-'}), score ${fmt(b.score)}`,
            `conf ${fmt(conf != null ? conf * 100 : null)} · ${b.spots_per_minute || 0} spots/m`,
            `trend ${b.trend || '-'}, rec: ${b.recommendation || '-'}`,
            ...Object.entries(b.region_counts || {}).map(([r, c]) => `${r}: ${c} link${c === 1 ? '' : 's'}`).sort()
        ].join('\n');
        byBand[b.band] = { state, label, conf, rateText: `${fmt(b.spots_per_minute)} spots/m`, title, regions: b.region_counts || {} };
    });
    return { unavailable: false, byBand };
}

function normalizeB(data) {
    if (data.error) return { unavailable: true, byBand: {} };
    const byBand = {};
    (data.bands || []).forEach(b => {
        const s = String(b.state || '').toLowerCase();
        let state, label;
        if (s === 'open' || s === 'rising') { state = 'open'; label = s === 'rising' ? 'rising' : 'open'; }
        else if (s === 'activity_spike') { state = 'maybe'; label = 'spike'; }
        else if (s === 'unconfirmed') { state = 'maybe'; label = 'unconfirmed'; }
        else { state = 'closed'; label = b.state || 'closed'; }
        const conf = normConf(b.confidence);
        const lines = [`B ladder: ${b.state || '-'} (${b.reason || 'no reason'})`, `conf ${fmt(conf != null ? conf * 100 : null)} · ${fmt(b.links_per_minute)} links/m`];
        if (b.onset_min_ago >= 0) lines.push(`onset ${b.onset_min_ago} min ago`);
        if (b.forecast_hints?.length) lines.push(`hints: ${b.forecast_hints.join('; ')}`);
        const mufRegions = regionCountsFromCells(b.muf_cells);
        const esRegions = regionCountsFromCells(b.es_cells);
        const regions = {};
        Object.entries(mufRegions).forEach(([r, c]) => { regions[r] = (regions[r] || 0) + c; });
        Object.entries(esRegions).forEach(([r, c]) => { regions[r] = (regions[r] || 0) + c; });
        if (Object.keys(regions).length) {
            lines.push('open midpoint cells by region (path midpoint):');
            Object.entries(regions).sort().forEach(([r, c]) => lines.push(`  ${r}: ${c}`));
        }
        byBand[b.band] = { state, label, conf, rateText: `${fmt(b.links_per_minute)} links/m`, title: lines.join('\n'), regions };
    });
    return { unavailable: false, byBand, dataThin: !!data.data_thin, muf: data.empirical_muf };
}

function normalizeC(data) {
    if (data.error) return { unavailable: true, byBand: {} };
    const grouped = {};
    (data.bands || []).forEach(row => {
        if (!grouped[row.band]) grouped[row.band] = [];
        grouped[row.band].push(row);
    });
    const byBand = {};
    CANON_BANDS.forEach(band => {
        const rows = grouped[band];
        if (!rows || !rows.length) {
            byBand[band] = { state: 'nodata', label: 'no data', conf: null, rateText: '-', title: 'C fusion: no live spots', regions: [] };
            return;
        }
        rows.sort((a, b) => FUSION_PRECEDENCE.indexOf(a.state) - FUSION_PRECEDENCE.indexOf(b.state));
        const best = rows[0];
        const openRows = rows.filter(r => r.state === 'open_confirmed' || r.state === 'open_unconfirmed');
        const stateMap = {
            open_confirmed: 'open', open_unconfirmed: 'open',
            closed_but_active: 'maybe', closed_with_cause: 'closed',
            closed: 'closed', insufficient_data: 'nodata'
        };
        const state = stateMap[best.state] || 'closed';
        let label = best.label || best.state;
        if (state === 'open' && openRows.length > 1) label += ` ×${openRows.length}`;
        const conf = normConf(best.confidence);
        const maxRate = Math.max(...rows.map(r => r.links_per_minute || 0));
        const lines = [`C fusion: ${best.state} (${best.label || '-'})`, `conf ${fmt(conf != null ? conf * 100 : null)} · up to ${fmt(maxRate)} links/m`];
        if (best.closure_type) lines.push(`cause: ${best.closure_type}`);
        lines.push('per region (path midpoint):');
        rows.forEach(r => {
            const parts = [`  ${r.region}: ${r.label || r.state} (conf ${fmt(r.confidence)})`];
            if (r.activity_ratio != null) parts.push(`ratio ${fmt(r.activity_ratio)}`);
            if (r.closure_type) parts.push(`cause ${r.closure_type}`);
            if (r.reason) parts.push(`- ${r.reason}`);
            lines.push(parts.join(' · '));
        });
        byBand[band] = { state, label, conf, rateText: `${fmt(maxRate)} links/m`, title: lines.join('\n'), regions: rows };
    });
    return { unavailable: false, byBand, dataThin: !!data.data_thin, sw: data.sw_available, drap: data.has_drap, events: data.events_active };
}

function computeAgreement(aRec, bRec, cRec) {
    const present = [];
    if (aRec && !aRec.unavailable) present.push(aRec);
    if (bRec && !bRec.unavailable) present.push(bRec);
    if (cRec && !cRec.unavailable) present.push(cRec);
    if (!present.length) return { cls: 'agree-na', text: '-', title: 'No engines available' };

    const states = present.map(r => r.state);
    const unique = new Set(states).size;
    const total = present.length;
    const parts = ['A: ' + (aRec?.unavailable ? 'n/a' : (aRec?.state || '-')),
                   'B: ' + (bRec?.unavailable ? 'n/a' : (bRec?.state || '-')),
                   'C: ' + (cRec?.unavailable ? 'n/a' : (cRec?.state || '-'))];
    const title = parts.join(' · ');
    if (unique === 1) return { cls: `agree-3 ${total === 2 ? 'agree-2' : ''}`, text: `${total}/${total}`, title };
    if (unique === 2) return { cls: 'agree-2', text: '2/1', title };
    return { cls: 'agree-3way', text: '1/1/1', title };
}

function buildTargetQuery() {
    const target = el('target-input').value.trim();
    const surroundings = el('surroundings-check').checked;
    const q = new URLSearchParams();
    if (target) q.set('target', target);
    if (surroundings) q.set('surroundings', 'true');
    return q;
}

async function runAll() {
    const targetQ = buildTargetQuery();
    const bParams = collectParams(B_PARAMS, 'b');
    const cParams = collectParams(C_PARAMS, 'c');

    const ladderQ = new URLSearchParams(targetQ);
    Object.entries(bParams).forEach(([k, v]) => ladderQ.set(k, v));

    const fusionQ = new URLSearchParams();
    Object.entries(cParams).forEach(([k, v]) => fusionQ.set(k, v));

    const baselineUrl = `${API.baseline}?${targetQ.toString()}`;
    const ladderUrl = `${API.ladder}?${ladderQ.toString()}`;
    const fusionUrl = `${API.fusion}?${fusionQ.toString()}`;

    try {
        const [baseline, ladder, fusion] = await Promise.all([
            getJson(baselineUrl).catch(e => ({ error: e.message })),
            getJson(ladderUrl).catch(e => ({ error: e.message })),
            getJson(fusionUrl).catch(e => ({ error: e.message }))
        ]);
        const nA = normalizeA(baseline);
        const nB = normalizeB(ladder);
        const nC = normalizeC(fusion);
        lastNorm = [nA, nB, nC];
        pushHistory(nA, nB, nC);
        renderCompare(nA, nB, nC);
        renderBaseline(baseline);
        renderLadder(ladder);
        renderFusion(fusion);
    } catch (e) {
        console.error(e);
    }
}

function stateOfNorm(n, band) {
    if (n.unavailable) return 'unavail';
    const rec = n.byBand[band];
    return rec ? rec.state : 'nodata';
}

function pushHistory(nA, nB, nC) {
    history.push({
        ts: Date.now() / 1000,
        a: Object.fromEntries(CANON_BANDS.map(b => [b, stateOfNorm(nA, b)])),
        b: Object.fromEntries(CANON_BANDS.map(b => [b, stateOfNorm(nB, b)])),
        c: Object.fromEntries(CANON_BANDS.map(b => [b, stateOfNorm(nC, b)]))
    });
    if (history.length > HISTORY_MAX) history.shift();
}

function renderCompare(nA, nB, nC) {
    renderLegend(nA, nB, nC);
    renderMatrix(nA, nB, nC);
    renderHistory();
}

function renderLegend(nA, nB, nC) {
    const elLegend = el('compare-legend');
    const chips = [];
    if (nA.unavailable) chips.push('<span class="badge text-bg-warning">A skipped: no target</span>');
    if (nB.dataThin) chips.push('<span class="badge text-bg-light">B thin</span>');
    if (nC.dataThin) chips.push('<span class="badge text-bg-light">C thin</span>');
    if (nC.sw && nC.drap) chips.push('<span class="badge text-bg-light">D-RAP</span>');
    if (nC.events) chips.push(`<span class="badge text-bg-light">${nC.events} event${nC.events === 1 ? '' : 's'}</span>`);
    const meta = [
        nB.unavailable ? '' : 'B: 30 min window',
        nC.unavailable ? '' : 'C: 20 min window',
        nA.unavailable ? '' : 'A: target window'
    ].filter(Boolean).join(' · ');
    elLegend.innerHTML = chips.join(' ') + (chips.length && meta ? ' · ' : '') + meta;
}

function bandIsInteresting(band, nA, nB, nC) {
    const states = [stateOfNorm(nA, band), stateOfNorm(nB, band), stateOfNorm(nC, band)].filter(s => s !== 'unavail');
    if (states.some(s => s === 'open' || s === 'maybe')) return true;
    if (new Set(states).size > 1) return true;
    return expandedBand === band;
}

function renderMatrix(nA, nB, nC) {
    const activeOnly = el('compare-active-only').checked;
    const matrix = el('compare-matrix');
    const rows = [];
    rows.push(`
        <div class="pl-row">
            <div class="pl-colhead"></div>
            <div class="pl-colhead">A baseline</div>
            <div class="pl-colhead">B ladder</div>
            <div class="pl-colhead">C fusion</div>
            <div class="pl-colhead">=</div>
        </div>`);

    CANON_BANDS.forEach(band => {
        if (activeOnly && !bandIsInteresting(band, nA, nB, nC)) return;
        const a = nA.byBand[band] || { state: 'nodata', label: 'no data', conf: null, title: 'A: no data', regions: {} };
        const b = nB.byBand[band] || { state: 'nodata', label: 'no data', conf: null, title: 'B: no data', regions: {} };
        const c = nC.byBand[band] || { state: 'nodata', label: 'no data', conf: null, title: 'C: no data', regions: [] };
        if (nA.unavailable) a.state = 'unavail';
        if (nB.unavailable) b.state = 'unavail';
        if (nC.unavailable) c.state = 'unavail';
        const agr = computeAgreement(a, b, c);
        const isExpanded = expandedBand === band;
        rows.push(`
            <div class="pl-row" data-band="${escapeHtml(band)}">
                <div class="pl-band" style="${bandStyle(band)}">${escapeHtml(band)}</div>
                ${renderCell(a, 'a')}
                ${renderCell(b, 'b')}
                ${renderCell(c, 'c')}
                <div class="pl-agree ${agr.cls}" title="${escapeHtml(agr.title)}">${agr.text}</div>
            </div>`);
        if (isExpanded) {
            rows.push(`
                <div class="pl-row">
                    <div class="pl-drill" data-band="${escapeHtml(band)}">${renderDrillDown(band, nA, nB, nC)}</div>
                </div>`);
        }
    });

    if (rows.length === 1) rows.push('<div class="pl-row"><div class="text-muted small" style="grid-column:1/-1">No open or disagreeing bands.</div></div>');
    matrix.innerHTML = rows.join('');

    matrix.querySelectorAll('.pl-cell').forEach(cell => {
        cell.addEventListener('click', () => {
            const band = cell.closest('.pl-row')?.dataset?.band;
            if (!band) return;
            expandedBand = expandedBand === band ? null : band;
            renderMatrix(nA, nB, nC);
        });
    });
}

function renderCell(rec, engine) {
    const confHtml = rec.conf != null ? `<span class="pl-confbar"><span style="width:${Math.round(rec.conf * 100)}%"></span></span>` : '';
    const rateHtml = rec.rateText ? `<span class="pl-rate">${escapeHtml(rec.rateText)}</span>` : '';
    return `<div class="pl-cell st-${rec.state}" title="${escapeHtml(rec.title || '')}" data-engine="${engine}">
        <span class="pl-chip-label">${escapeHtml(rec.label || '')}</span>
        ${rateHtml}
        ${confHtml}
    </div>`;
}

function renderDrillDown(band, nA, nB, nC) {
    const a = nA.byBand[band];
    const b = nB.byBand[band];
    const c = nC.byBand[band];

    const aTotal = a ? Object.values(a.regions || {}).reduce((s, v) => s + v, 0) : 0;
    const aRows = nA.unavailable
        ? '<tr><td colspan="3" class="text-muted">A requires a target</td></tr>'
        : Object.entries(a?.regions || {}).length
            ? Object.entries(a.regions).sort().map(([r, cnt]) => `
                <tr><td>${escapeHtml(r)}</td><td class="text-end">${cnt}</td><td class="text-end">${aTotal ? Math.round(cnt / aTotal * 100) : 0}%</td></tr>`).join('')
            : '<tr><td colspan="3" class="text-muted">no regional counts</td></tr>';

    const bRows = nB.unavailable
        ? '<tr><td colspan="2" class="text-muted">B unavailable</td></tr>'
        : Object.entries(b?.regions || {}).length
            ? Object.entries(b.regions).sort().map(([r, cnt]) => `
                <tr><td>${escapeHtml(r)}</td><td class="text-end">${cnt} cell${cnt === 1 ? '' : 's'}</td></tr>`).join('')
            : '<tr><td colspan="2" class="text-muted">no open midpoint cells</td></tr>';

    const cRows = nC.unavailable
        ? '<tr><td colspan="6" class="text-muted">C unavailable</td></tr>'
        : (c?.regions || []).length
            ? c.regions.map(r => `
                <tr>
                    <td>${escapeHtml(r.region)}</td>
                    <td>${renderCell({ state: ({open_confirmed:'open',open_unconfirmed:'open',closed_but_active:'maybe',closed_with_cause:'closed',closed:'closed',insufficient_data:'nodata'}[r.state]||'closed'), label: r.label || r.state, conf: normConf(r.confidence), title: `${r.state}: ${r.reason || ''}` }, 'c')}</td>
                    <td class="text-end proplab-mono">${fmt(r.confidence)}</td>
                    <td class="text-end proplab-mono">${fmt(r.links_per_minute)}</td>
                    <td class="text-end proplab-mono">${fmt(r.activity_ratio)}</td>
                    <td class="text-muted">${escapeHtml(r.closure_type || '-')}</td>
                </tr>`).join('')
            : '<tr><td colspan="6" class="text-muted">no live spots</td></tr>';

    return `
        <div class="pl-drill-tables">
            <table class="table table-sm">
                <caption>A baseline · remote station</caption>
                <thead><tr><th>region</th><th class="text-end">links</th><th class="text-end">share</th></tr></thead>
                <tbody>${aRows}</tbody>
            </table>
            <table class="table table-sm">
                <caption>B ladder · path midpoint</caption>
                <thead><tr><th>region</th><th class="text-end">cells</th></tr></thead>
                <tbody>${bRows}</tbody>
            </table>
            <table class="table table-sm">
                <caption>C fusion · path midpoint</caption>
                <thead><tr><th>region</th><th>state</th><th class="text-end">conf</th><th class="text-end">links/m</th><th class="text-end">ratio</th><th>cause</th></tr></thead>
                <tbody>${cRows}</tbody>
            </table>
        </div>`;
}

function renderHistory() {
    const hist = el('compare-history');
    if (!history.length) {
        hist.innerHTML = 'No refresh history yet.';
        return;
    }
    const shown = history.slice(-HISTORY_TICKS_SHOWN);
    const rows = CANON_BANDS.map(band => {
        const activeOnly = el('compare-active-only').checked;
        if (activeOnly && lastNorm && !bandIsInteresting(band, lastNorm[0], lastNorm[1], lastNorm[2])) return '';
        const aStrips = shown.map((h, i) => tickHtml(h.a[band] || 'nodata', `A · ${formatDate(h.ts)} · ${h.a[band] || 'nodata'}`)).join('');
        const bStrips = shown.map((h, i) => tickHtml(h.b[band] || 'nodata', `B · ${formatDate(h.ts)} · ${h.b[band] || 'nodata'}`)).join('');
        const cStrips = shown.map((h, i) => tickHtml(h.c[band] || 'nodata', `C · ${formatDate(h.ts)} · ${h.c[band] || 'nodata'}`)).join('');
        return `
            <div class="pl-hrow">
                <div class="pl-hlabel" style="${bandStyle(band)}">${escapeHtml(band)}</div>
                <div class="pl-strip">${aStrips}</div>
                <div class="pl-strip">${bStrips}</div>
                <div class="pl-strip">${cStrips}</div>
            </div>`;
    }).filter(Boolean).join('');
    hist.innerHTML = rows || '<div class="text-muted small">No open or disagreeing bands.</div>';
}

function tickHtml(state, title) {
    return `<div class="pl-tick t-${state}" title="${escapeHtml(title)}"></div>`;
}

function renderBaseline(data) {
    const out = el('result-a');
    if (data.error) {
        out.innerHTML = `<div class="alert alert-danger">${escapeHtml(data.error)}</div>`;
        return;
    }
    const header = `
        <div class="d-flex justify-content-between mb-2">
            <span>Overall: <strong>${data.condition || '?'}</strong></span>
            <span class="text-muted">score ${fmt(data.overall_score)} / conf ${fmt(data.confidence)}</span>
        </div>`;
    const rows = (data.bands || []).map(b => `
        <tr class="proplab-band-row" style="${bandStyle(b.band)}">
            <td>${b.band}</td>
            <td class="${stateClass(b.status)}">${b.status || '-'}</td>
            <td>${b.condition || '-'}</td>
            <td>${b.mode || '-'}</td>
            <td class="text-end">${fmt(b.score)}</td>
            <td class="text-end">${b.unique_links || 0}</td>
            <td class="text-end">${fmt(b.spots_per_minute)}</td>
        </tr>`).join('');
    out.innerHTML = header + `
        <div class="table-responsive">
            <table class="table table-sm table-striped">
                <thead><tr><th>Band</th><th>Status</th><th>Condition</th><th>Mode</th><th class="text-end">Score</th><th class="text-end">Links</th><th class="text-end">Spots/m</th></tr></thead>
                <tbody>${rows || '<tr><td colspan="7" class="text-muted">No data</td></tr>'}</tbody>
            </table>
        </div>`;
}

function renderLadder(data) {
    const out = el('result-b');
    if (data.error) {
        out.innerHTML = `<div class="alert alert-danger">${escapeHtml(data.error)}</div>`;
        return;
    }
    const muf = data.empirical_muf ? `${data.empirical_muf.toFixed(1)} MHz` : 'none';
    const runs = (data.open_runs || []).map(r => r.join('+')).join(', ') || 'none';
    const header = `
        <div class="d-flex justify-content-between mb-2 proplab-mono">
            <span>MUF: <strong>${muf}</strong></span>
            <span class="text-muted">${data.data_thin ? 'thin' : 'ok'} | runs: ${runs}</span>
        </div>`;
    const rows = (data.bands || []).map(b => `
        <tr class="proplab-band-row" style="${bandStyle(b.band)}">
            <td>${b.band}</td>
            <td class="${stateClass(b.state)}">${b.state || '-'}</td>
            <td>${escapeHtml(b.reason || '')}</td>
            <td class="text-end">${fmt(b.confidence)}</td>
            <td class="text-end">${fmt(b.links_per_minute)}</td>
            <td class="text-end">${b.onset_min_ago >= 0 ? b.onset_min_ago + ' min' : '-'}</td>
            <td class="proplab-mono">${(b.forecast_hints || []).join('; ') || '-'}</td>
        </tr>`).join('');
    out.innerHTML = header + `
        <div class="table-responsive">
            <table class="table table-sm table-striped">
                <thead><tr><th>Band</th><th>State</th><th>Reason</th><th class="text-end">Conf</th><th class="text-end">Links/m</th><th class="text-end">Onset</th><th>Hints</th></tr></thead>
                <tbody>${rows || '<tr><td colspan="7" class="text-muted">No data</td></tr>'}</tbody>
            </table>
        </div>`;
}

function renderFusion(data) {
    const out = el('result-c');
    if (data.error) {
        out.innerHTML = `<div class="alert alert-danger">${escapeHtml(data.error)}</div>`;
        return;
    }
    const drap = data.has_drap
        ? `D-RAP ${data.drap_age_min >= 0 ? data.drap_age_min + ' min' : 'fresh'}`
        : 'D-RAP off';
    const header = `
        <div class="d-flex justify-content-between mb-2 proplab-mono">
            <span>SW: <strong>${data.sw_available ? 'available' : 'off'}</strong> | ${drap}</span>
            <span class="text-muted">${data.data_thin ? 'thin' : 'ok'} | events: ${data.events_active || 0}</span>
        </div>`;
    const rows = (data.bands || []).map(b => `
        <tr class="proplab-band-row" style="${bandStyle(b.band)}">
            <td>${b.band}</td>
            <td class="${stateClass(b.state)}">${b.label || b.state || '-'}</td>
            <td>${escapeHtml(b.reason || '')}</td>
            <td class="text-end">${fmt(b.confidence)}</td>
            <td class="text-end">${fmt(b.links_per_minute)}</td>
            <td class="text-end">${fmt(b.baseline_p50)}</td>
            <td class="text-end">${fmt(b.activity_ratio)}</td>
            <td>${b.closure_type ? escapeHtml(b.closure_type) : '-'}</td>
        </tr>`).join('');
    out.innerHTML = header + `
        <div class="table-responsive">
            <table class="table table-sm table-striped">
                <thead><tr><th>Band</th><th>State</th><th>Reason</th><th class="text-end">Conf</th><th class="text-end">Links/m</th><th class="text-end">Baseline</th><th class="text-end">Ratio</th><th>Cause</th></tr></thead>
                <tbody>${rows || '<tr><td colspan="8" class="text-muted">No data</td></tr>'}</tbody>
            </table>
        </div>`;
}

function fmt(v) {
    if (v === undefined || v === null) return '-';
    if (typeof v === 'number') {
        if (Math.abs(v) >= 100) return v.toFixed(0);
        return v.toFixed(2);
    }
    return String(v);
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function setupRefresh() {
    if (refreshTimer) clearInterval(refreshTimer);
    const ms = parseInt(el('refresh-select').value, 10);
    if (ms > 0) {
        refreshTimer = setInterval(runAll, ms);
    }
}

async function init() {
    try {
        defaults = await getJson(API.params);
    } catch (e) {
        console.error('Failed to load default params', e);
        defaults = { b: {}, c: {} };
    }
    buildParamControls('params-b', B_PARAMS, defaults.b || {}, 'b');
    buildParamControls('params-c', C_PARAMS, defaults.c || {}, 'c');

    el('proplab-form').addEventListener('submit', (e) => {
        e.preventDefault();
        history = [];
        expandedBand = null;
        lastNorm = null;
        runAll();
    });
    el('refresh-select').addEventListener('change', setupRefresh);
    el('compare-active-only').addEventListener('change', () => {
        if (lastNorm) renderMatrix(...lastNorm);
        renderHistory();
    });
    el('compare-details-toggle').addEventListener('click', () => {
        document.body.classList.toggle('pl-min');
        const hidden = document.body.classList.contains('pl-min');
        el('compare-details-toggle').textContent = hidden ? 'Show tables' : 'Hide tables';
    });
    el('compare-history-wrap').addEventListener('toggle', () => {
        if (el('compare-history-wrap').open) renderHistory();
    });
    setupRefresh();
    runAll();
}

init();
