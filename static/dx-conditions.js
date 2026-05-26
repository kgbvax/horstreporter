let pollTimer = null;

function ensureDxInfoOverlay() {
    let overlay = document.getElementById('dx-info-overlay');
    if (overlay) return overlay;

    overlay = document.createElement('div');
    overlay.id = 'dx-info-overlay';
    overlay.className = 'dx-info-overlay';
    overlay.innerHTML = `
        <div class="info-content">
            <button id="close-dx-info" title="Close">&times;</button>
            <iframe src="info.html?doc=dxscore.md&title=DX%20Potential%20Scoring" frameborder="0"></iframe>
        </div>
    `;
    document.body.appendChild(overlay);

    overlay.addEventListener('click', (e) => {
        if (e.target === overlay) {
            overlay.style.display = 'none';
        }
    });

    overlay.querySelector('#close-dx-info')?.addEventListener('click', () => {
        overlay.style.display = 'none';
    });

    return overlay;
}

export function initDxConditionsUI() {
    const infoBtn = document.getElementById('dx-info-toggle');
    if (!infoBtn || infoBtn.dataset.bound === 'true') return;

    infoBtn.dataset.bound = 'true';
    infoBtn.addEventListener('click', () => {
        const overlay = ensureDxInfoOverlay();
        overlay.style.display = 'flex';
    });
}

function setText(id, value) {
    const el = document.getElementById(id);
    if (el) el.textContent = value;
}

function trendGlyph(trend) {
    if (trend === 'rising') return '↗';
    if (trend === 'falling') return '↘';
    return '→';
}

function statusGlyph(status) {
    if (status === 'green') return '🟢';
    if (status === 'yellow') return '🟡';
    if (status === 'red') return '🔴';
    return '⚪';
}

function sparklineSvg(points) {
    if (!Array.isArray(points) || points.length === 0) {
        return '';
    }

    const width = 96;
    const height = 20;
    const step = points.length > 1 ? width / (points.length - 1) : width;
    const safe = points.map((v) => Number.isFinite(Number(v)) ? Number(v) : 0);
    const max = Math.max(...safe, 1);
    const coords = safe
        .map((v, i) => {
            const x = i * step;
            const y = height - ((v / max) * (height - 2)) - 1;
            return `${x.toFixed(1)},${Math.max(1, y).toFixed(1)}`;
        })
        .join(' ');

    return `<svg class="dx-sparkline" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true"><polyline points="${coords}"/></svg>`;
}

export function setDxConditionsVisible(visible) {
    const panel = document.getElementById('dx-conditions-panel');
    if (!panel) return;
    panel.style.display = visible ? '' : 'none';
}

export function resetDxConditions() {
    setText('dx-overall-score', '—');
    setText('dx-overall-condition', 'Waiting for stream...');
    setText('dx-confidence', '—');
    setText('dx-best-bands', '—');

    const list = document.getElementById('dx-band-list');
    if (list) list.innerHTML = '';
}

function renderBandRows(bands) {
    const list = document.getElementById('dx-band-list');
    if (!list) return;

    if (!Array.isArray(bands) || bands.length === 0) {
        list.innerHTML = '<li class="text-muted">No current band activity in window.</li>';
        return;
    }

    const top = bands.slice(0, 4);
    list.innerHTML = top
        .map((b) => {
            const trend = trendGlyph(b.trend);
            const status = statusGlyph(b.status);
            const repeatPct = Number(b.repeat_ratio || 0) * 100;
            const longHaulPct = Number(b.long_haul_ratio || 0) * 100;
            return `<li class="dx-band-item">
                <div class="dx-band-head">
                    <strong>${status} ${b.band}</strong>
                    <span class="dx-trend">${trend} ${b.condition}</span>
                </div>
                <div class="dx-band-sub">Score ${Number(b.score || 0).toFixed(1)} · conf ${Number(b.confidence || 0).toFixed(0)}% · mode ${b.mode || 'none'} · dir ${b.dominant_direction || '-'}</div>
                <div class="dx-band-metrics">uniq ${Number(b.unique_links || 0)} · repeats ${repeatPct.toFixed(0)}% · long ${longHaulPct.toFixed(0)}%</div>
                <div class="dx-band-metrics">${b.recommendation || ''}</div>
                ${sparklineSvg(b.sparkline)}
            </li>`;
        })
        .join('');
}

export function renderDxConditions(payload) {
    if (!payload || typeof payload !== 'object') {
        return;
    }

    setText('dx-overall-score', Number(payload.overall_score || 0).toFixed(1));
    const overallTrend = trendGlyph(payload.trend);
    const overallStatus = statusGlyph(payload.status);
    setText('dx-overall-condition', `${overallStatus} ${overallTrend} ${payload.condition || 'Unknown'}`);
    setText('dx-confidence', `${Number(payload.confidence || 0).toFixed(0)}%`);

    const bestBands = Array.isArray(payload.best_bands) && payload.best_bands.length > 0
        ? payload.best_bands.join(', ')
        : '—';
    setText('dx-best-bands', bestBands);

    renderBandRows(payload.bands);
}

async function fetchDxConditions({ target, minutes, surroundings }) {
    const params = new URLSearchParams();
    params.set('target', target);
    params.set('minutes', String(minutes));
    if (surroundings) {
        params.set('surroundings', 'true');
    }

    const res = await fetch(`/api/dx_conditions?${params.toString()}`);
    if (!res.ok) {
        throw new Error(`DX conditions request failed: ${res.status}`);
    }
    return await res.json();
}

export async function refreshDxConditions(params) {
    if (!params || !params.target) {
        resetDxConditions();
        return;
    }

    try {
        const payload = await fetchDxConditions(params);
        renderDxConditions(payload);
    } catch (err) {
        setText('dx-overall-condition', 'DX feed unavailable');
    }
}

export function startDxPolling(paramsProvider, intervalMs = 30000) {
    stopDxPolling();

    const tick = async () => {
        const params = paramsProvider ? paramsProvider() : null;
        if (!params || !params.target) return;
        await refreshDxConditions(params);
    };

    void tick();
    pollTimer = setInterval(() => {
        void tick();
    }, intervalMs);
}

export function stopDxPolling() {
    if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
    }
}
