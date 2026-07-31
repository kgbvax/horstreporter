import { bandColors } from '../utils.js';

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
    return `border-left-color: ${color}; background-color: ${color}10;`;
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
        renderBaseline(baseline);
        renderLadder(ladder);
        renderFusion(fusion);
    } catch (e) {
        console.error(e);
    }
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
        runAll();
    });
    el('refresh-select').addEventListener('change', setupRefresh);
    setupRefresh();
    runAll();
}

init();
