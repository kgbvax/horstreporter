// @ts-check

/** @typedef {{ band: string, state: string, label: string, current_spot_count: number, confidence: number }} DxPulseSummaryBand */
/** @typedef {{ region: string, total_spot_count: number, active_bands: number, best_band?: string, best_band_state?: string, best_band_label?: string }} DxPulseSummaryRegion */
/** @typedef {{ band: string, region: string, state: string, label: string, current_spot_count: number, confidence: number, strength: number }} DxPulseSummaryCell */
/** @typedef {{ target: string, surroundings: boolean, mode: string, mode_label: string, window_minutes: number, generated_at: number, baseline_available: boolean, best_bands: DxPulseSummaryBand[], top_regions: DxPulseSummaryRegion[], hot_cells: DxPulseSummaryCell[] }} DxPulseSummaryResponse */

/** @typedef {{ band: string, region: string, state: string, label: string, color_bucket?: string, current_spot_count: number, current_unique_paths: number, current_unique_remote_grids?: number, confidence: number, baseline_ratio?: number, baseline_expected_spot_count?: number, baseline_support?: number }} DxPulseMatrixCell */
/** @typedef {{ target: string, surroundings: boolean, mode: string, mode_label: string, window_minutes: number, generated_at: number, slot_of_day: number, baseline_lookback_days?: number, baseline_available: boolean, bands: string[], regions: string[], matrix: DxPulseMatrixCell[][] }} DxPulseMatrixResponse */

/** @typedef {{ min: number, max: number }} ScaleRange */
/** @typedef {{ quality: ScaleRange, anomaly: ScaleRange, qualitySamples: number[], anomalySamples: number[] }} ScaleContext */
/** @typedef {'horizon' | 'viridis' | 'plasma' | 'inferno'} DxPulsePaletteName */

const params = new URLSearchParams(window.location.search);

const elements = {
  form: /** @type {HTMLFormElement} */ (document.getElementById('dxpulse-form')),
  target: /** @type {HTMLInputElement} */ (document.getElementById('target')),
  mode: /** @type {HTMLInputElement} */ (document.getElementById('mode')),
  minutes: /** @type {HTMLSelectElement} */ (document.getElementById('minutes')),
  lookback: /** @type {HTMLSelectElement} */ (document.getElementById('lookback')),
  palette: /** @type {HTMLSelectElement} */ (document.getElementById('palette')),
  surroundings: /** @type {HTMLInputElement} */ (document.getElementById('surroundings')),
  modeButtons: Array.from(document.querySelectorAll('[data-mode]')),
  status: /** @type {HTMLElement} */ (document.getElementById('status')),
  meta: /** @type {HTMLElement} */ (document.getElementById('meta')),
  colorKey: /** @type {HTMLElement | null} */ (document.getElementById('color-key')),
  table: /** @type {HTMLTableElement} */ (document.getElementById('matrix-table')),
  compactTable: /** @type {HTMLTableElement} */ (document.getElementById('compact-matrix-table')),
  bestBands: /** @type {HTMLElement} */ (document.getElementById('best-bands')),
  topRegions: /** @type {HTMLElement} */ (document.getElementById('top-regions')),
  hotCells: /** @type {HTMLElement} */ (document.getElementById('hot-cells'))
};

/** @type {Record<DxPulsePaletteName, string[]>} */
const DXPULSE_PALETTES = {
  horizon: [
    '#d1d5db', '#c8d9ec', '#bdd4ea', '#b0cfe6',
    '#a1c8de', '#8cbfd2', '#72b2c2', '#57a6ae',
    '#3f9a95', '#3a9f74', '#59aa5e', '#80b552',
    '#a9bf4f', '#d1b74a', '#e89e3f', '#dd6f35'
  ],
  viridis: [
    '#440154', '#482374', '#404387', '#345e8d',
    '#29788e', '#20908c', '#22a884', '#43bf71',
    '#73d056', '#a0da39', '#c2df23', '#d8e219',
    '#e5e419', '#f1e51d', '#f8e621', '#fde725'
  ],
  plasma: [
    '#0d0887', '#2b0594', '#43039e', '#5901a5',
    '#6e00a8', '#8305a7', '#9612a1', '#a72197',
    '#b7318a', '#c1417d', '#cc5170', '#e16462',
    '#ed7953', '#f89441', '#fdb32f', '#f0f921'
  ],
  inferno: [
    '#000004', '#120d31', '#331067', '#59157e',
    '#7c1d6f', '#9f2963', '#bd3853', '#d94d3d',
    '#ed6925', '#f98c0a', '#fbb81f', '#f6d746',
    '#f4e55c', '#f7f173', '#fcffa4', '#fcffa4'
  ]
};

/** @type {Record<string, DxPulsePaletteName>} */
const DEFAULT_PALETTES = {
  quality: 'horizon',
  anomaly: 'plasma'
};

const NO_PROPAGATION_COLOR = '#8f98a8';
/** @type {ScaleContext | null} */
let currentScaleContext = null;

/** @type {DxPulseMatrixResponse | null} */
let currentMatrixData = null;

/** @type {number | null} */
let refreshTimer = null;

init();

function init() {
  initInfoOverlay();
  elements.target.value = (params.get('target') || 'JO42').toUpperCase();
  elements.mode.value = params.get('mode') || 'quality';
  updateModeButtons(elements.mode.value);
  syncPaletteSelect(elements.mode.value);
  elements.minutes.value = params.get('minutes') || '15';
  elements.lookback.value = params.get('lookback_days') || '45';
  elements.surroundings.checked = params.get('surroundings') === 'true';

  elements.form.addEventListener('submit', (event) => {
    event.preventDefault();
    loadAll();
  });

  elements.target.addEventListener('change', () => {
    loadAll();
  });

  elements.minutes.addEventListener('change', () => {
    loadAll();
  });

  elements.lookback.addEventListener('change', () => {
    loadAll();
  });

  elements.palette.addEventListener('change', () => {
    updatePalettePreference(elements.mode.value, elements.palette.value);
    rerenderCurrentMatrix();
  });

  elements.surroundings.addEventListener('change', () => {
    loadAll();
  });

  for (const button of elements.modeButtons) {
    button.addEventListener('click', () => {
      const mode = button.getAttribute('data-mode') || 'quality';
      if (mode === elements.mode.value) {
        loadAll();
        return;
      }
      elements.mode.value = mode;
      updateModeButtons(mode);
      syncPaletteSelect(mode);
      loadAll();
    });
  }

  loadAll();
  refreshTimer = window.setInterval(loadAll, 30000);
}

function initInfoOverlay() {
  const overlay = document.getElementById('dxpulse-info-overlay');
  const openBtn = document.getElementById('dxpulse-info-toggle');
  const closeBtn = document.getElementById('dxpulse-info-close');
  if (!(overlay instanceof HTMLElement) || !(openBtn instanceof HTMLButtonElement) || !(closeBtn instanceof HTMLButtonElement)) {
    return;
  }

  const openOverlay = () => {
    overlay.classList.add('is-open');
    overlay.setAttribute('aria-hidden', 'false');
    document.body.classList.add('dxpulse-overlay-open');
  };

  const closeOverlay = () => {
    overlay.classList.remove('is-open');
    overlay.setAttribute('aria-hidden', 'true');
    document.body.classList.remove('dxpulse-overlay-open');
  };

  openBtn.addEventListener('click', openOverlay);
  closeBtn.addEventListener('click', closeOverlay);
  overlay.addEventListener('click', (event) => {
    if (event.target === overlay) {
      closeOverlay();
    }
  });

  window.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && overlay.classList.contains('is-open')) {
      closeOverlay();
    }
  });

  if (!localStorage.getItem('dxpulseInfoShown')) {
    openOverlay();
    localStorage.setItem('dxpulseInfoShown', 'true');
  }
}

/** @param {string} mode */
function updateModeButtons(mode) {
  for (const button of elements.modeButtons) {
    const buttonMode = button.getAttribute('data-mode') || '';
    const isActive = buttonMode === mode;
    button.classList.toggle('is-active', isActive);
    button.setAttribute('aria-pressed', isActive ? 'true' : 'false');
  }
}

/** @param {string} mode */
function syncPaletteSelect(mode) {
  const palette = getPalettePreference(mode);
  if (elements.palette.value !== palette) {
    elements.palette.value = palette;
  }
}

/**
 * @param {string} mode
 * @returns {DxPulsePaletteName}
 */
function getPalettePreference(mode) {
  const normalizedMode = String(mode || 'quality').toLowerCase() === 'anomaly' ? 'anomaly' : 'quality';
  const stored = localStorage.getItem(`dxpulsePalette:${normalizedMode}`);
  if (stored === 'horizon' || stored === 'viridis' || stored === 'plasma' || stored === 'inferno') {
    return stored;
  }
  return DEFAULT_PALETTES[normalizedMode];
}

/**
 * @param {string} mode
 * @param {string} palette
 */
function updatePalettePreference(mode, palette) {
  const normalizedMode = String(mode || 'quality').toLowerCase() === 'anomaly' ? 'anomaly' : 'quality';
  if (palette !== 'horizon' && palette !== 'viridis' && palette !== 'plasma' && palette !== 'inferno') {
    return;
  }
  localStorage.setItem(`dxpulsePalette:${normalizedMode}`, palette);
}

function rerenderCurrentMatrix() {
  if (!currentMatrixData) {
    return;
  }
  renderMatrix(currentMatrixData);
  renderCompactMatrix(currentMatrixData);
}

async function loadAll() {
  const target = elements.target.value.trim().toUpperCase();
  if (!target) {
    setStatus('Enter a Maidenhead square to load DXPulse.', 'muted');
    return;
  }

  const query = buildQuery();
  syncUrl(query);
  setStatus('Loading DXPulse…', 'muted');

  try {
    const [summaryRaw, matrixRaw] = await Promise.all([
      fetchJson(`/api/dxpulse/v1/summary?${query}`),
      fetchJson(`/api/dxpulse/v1/matrix?${query}`)
    ]);
    if (!isDxPulseSummaryResponse(summaryRaw)) {
      throw new Error('DXPulse summary payload is invalid');
    }
    if (!isDxPulseMatrixResponse(matrixRaw)) {
      throw new Error('DXPulse matrix payload is invalid');
    }
    const summary = summaryRaw;
    const matrix = matrixRaw;
    currentMatrixData = matrix;
    renderSummary(summary);
    renderMatrix(matrix);
    renderCompactMatrix(matrix);
    renderMeta(summary, matrix);
    setStatus(`Updated ${formatTimestamp(summary.generated_at)}.`, 'success');
  } catch (error) {
    console.error(error);
    clearPanels();
    const message = error instanceof Error ? error.message : String(error || 'Could not load DXPulse data.');
    setStatus(message || 'Could not load DXPulse data.', 'danger');
  }
}

function buildQuery() {
  const query = new URLSearchParams();
  query.set('target', elements.target.value.trim().toUpperCase());
  query.set('mode', elements.mode.value);
  query.set('minutes', elements.minutes.value);
  query.set('lookback_days', elements.lookback.value);
  if (elements.surroundings.checked) {
    query.set('surroundings', 'true');
  }
  return query.toString();
}

/** @param {string} query */
function syncUrl(query) {
  const url = `${window.location.pathname}?${query}`;
  window.history.replaceState({}, '', url);
}

/**
 * @param {string} url
 * @returns {Promise<unknown>}
 */
async function fetchJson(url) {
  const response = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `Request failed with ${response.status}`);
  }
  return response.json();
}

/** @param {unknown} value */
function isObjectRecord(value) {
  return typeof value === 'object' && value !== null;
}

/**
 * @param {unknown} value
 * @returns {value is DxPulseSummaryResponse}
 */
function isDxPulseSummaryResponse(value) {
  if (!isObjectRecord(value)) return false;
  const v = /** @type {Record<string, unknown>} */ (value);
  return typeof v.target === 'string'
    && typeof v.mode === 'string'
    && typeof v.mode_label === 'string'
    && Array.isArray(v.best_bands)
    && Array.isArray(v.top_regions)
    && Array.isArray(v.hot_cells);
}

/**
 * @param {unknown} value
 * @returns {value is DxPulseMatrixResponse}
 */
function isDxPulseMatrixResponse(value) {
  if (!isObjectRecord(value)) return false;
  const v = /** @type {Record<string, unknown>} */ (value);
  return typeof v.target === 'string'
    && typeof v.mode === 'string'
    && Array.isArray(v.bands)
    && Array.isArray(v.regions)
    && Array.isArray(v.matrix);
}

/** @param {DxPulseSummaryResponse} summary */
function renderSummary(summary) {
  renderList(elements.bestBands, summary.best_bands || [], (item) => `
    <div class="summary-chip">
      <div>
        <strong>${escapeHtml(item.band)}</strong>
        <span>${escapeHtml(item.label || item.state)}</span>
      </div>
      <div class="text-end">
        <strong>${item.current_spot_count}</strong>
        <span>${Math.round((item.confidence || 0) * 100)}% conf</span>
      </div>
    </div>
  `, 'No active bands yet.');

  renderList(elements.topRegions, summary.top_regions || [], (item) => `
    <div class="summary-chip">
      <div>
        <strong>${escapeHtml(item.region)}</strong>
        <span>${escapeHtml(item.best_band || '—')} · ${escapeHtml(item.best_band_label || item.best_band_state || 'quiet')}</span>
      </div>
      <div class="text-end">
        <strong>${item.total_spot_count}</strong>
        <span>${item.active_bands} bands</span>
      </div>
    </div>
  `, 'No active regions yet.');

  renderList(elements.hotCells, summary.hot_cells || [], (item) => `
    <div class="summary-chip">
      <div>
        <strong>${escapeHtml(item.band)} → ${escapeHtml(item.region)}</strong>
        <span>${escapeHtml(item.label || item.state)}</span>
      </div>
      <div class="text-end">
        <strong>${item.current_spot_count}</strong>
        <span>${Math.round((item.confidence || 0) * 100)}% conf</span>
      </div>
    </div>
  `, 'No hot cells yet.');
}

/**
 * @param {HTMLElement} container
 * @param {any[]} items
 * @param {(item: any) => string} renderItem
 * @param {string} emptyText
 */
function renderList(container, items, renderItem, emptyText) {
  if (!items.length) {
    container.innerHTML = `<div class="text-muted small">${escapeHtml(emptyText)}</div>`;
    return;
  }
  container.innerHTML = items.map(renderItem).join('');
}

/** @param {DxPulseMatrixResponse} matrix */
function renderMatrix(matrix) {
  const bands = matrix.bands || [];
  const regions = matrix.regions || [];
  const rows = matrix.matrix || [];
  if (!bands.length || !regions.length || !rows.length) {
    elements.table.innerHTML = '<tbody><tr><td class="text-muted">No matrix data in the selected window.</td></tr></tbody>';
    return;
  }

  currentScaleContext = buildScaleContext(matrix);

  const header = `
    <thead>
      <tr>
        <th class="dxpulse-band-cell">Band</th>
        ${regions.map((region) => `<th>${escapeHtml(region)}</th>`).join('')}
      </tr>
    </thead>
  `;

  const body = rows.map((row, rowIndex) => {
    const band = bands[rowIndex] || row[0]?.band || '—';
    return `
      <tr>
        <th class="dxpulse-band-cell">${escapeHtml(band)}</th>
        ${row.map(renderCell).join('')}
      </tr>
    `;
  }).join('');

  elements.table.innerHTML = `${header}<tbody>${body}</tbody>`;
  renderColorKey(matrix);
}

/** @param {DxPulseMatrixResponse} matrix */
function renderCompactMatrix(matrix) {
  const bands = matrix.bands || [];
  const regions = matrix.regions || [];
  const rows = matrix.matrix || [];
  if (!bands.length || !regions.length || !rows.length) {
    elements.compactTable.innerHTML = '<tbody><tr><td class="text-muted">No compact matrix data in the selected window.</td></tr></tbody>';
    return;
  }

  const header = `
    <thead>
      <tr>
        <th class="dxpulse-band-cell">Band</th>
        ${regions.map((region) => `<th>${escapeHtml(region)}</th>`).join('')}
      </tr>
    </thead>
  `;

  const body = rows.map((row, rowIndex) => {
    const band = bands[rowIndex] || row[0]?.band || '—';
    return `
      <tr>
        <th class="dxpulse-band-cell">${escapeHtml(band)}</th>
        ${row.map(renderCompactCell).join('')}
      </tr>
    `;
  }).join('');

  elements.compactTable.innerHTML = `${header}<tbody>${body}</tbody>`;
}

/** @param {DxPulseMatrixCell} cell */
function renderCell(cell) {
  const mode = String(elements.mode?.value || 'quality').toLowerCase();
  const { color, scaleIndex } = colorForCell(cell, mode);
  const textColor = bestTextColor(color);
  const scaleInfo = scaleIndex >= 0 ? `scale ${scaleIndex + 1}/16` : 'no propagation';
  const isNoPropagation = scaleIndex < 0;

  if (isNoPropagation) {
    return `
      <td>
        <div class="dxpulse-cell-fill" style="background:${color}" title="${escapeHtml(scaleInfo)}" aria-label="No propagation"></div>
      </td>
    `;
  }

  const primary = mode === 'anomaly'
    ? escapeHtml(formatAnomalyHeadline(cell))
    : escapeHtml(cell.label || cell.state || 'quiet');
  const secondary = `${cell.current_spot_count || 0} spots · ${cell.current_unique_paths || 0} paths`;
  const tertiary = mode === 'anomaly'
    ? formatAnomalyDetail(cell)
    : `${Math.round((cell.confidence || 0) * 100)}% conf`;
  return `
    <td>
      <div class="dxpulse-cell-fill" style="background:${color};color:${textColor}" title="${escapeHtml(scaleInfo)}">
        <div>${primary}</div>
        <small>${escapeHtml(secondary)}</small>
        <small>${escapeHtml(tertiary)}</small>
      </div>
    </td>
  `;
}

/** @param {DxPulseMatrixCell} cell */
function renderCompactCell(cell) {
  const mode = String(elements.mode?.value || 'quality').toLowerCase();
  const { color, scaleIndex } = colorForCell(cell, mode);
  const tooltip = buildCellTooltip(cell, mode, scaleIndex);
  return `
    <td>
      <div
        class="dxpulse-compact-cell"
        style="background:${color}"
        title="${escapeHtml(tooltip)}"
        aria-label="${escapeHtml(tooltip)}"></div>
    </td>
  `;
}

/**
 * @param {DxPulseMatrixCell} cell
 * @param {string} mode
 * @param {number} scaleIndex
 */
function buildCellTooltip(cell, mode, scaleIndex) {
  const lines = [`${cell.band || '—'} → ${cell.region || '—'}`];

  if (scaleIndex < 0 || Number(cell.current_spot_count || 0) <= 0) {
    lines.push('No propagation');
    return lines.join(' · ');
  }

  if (mode === 'anomaly') {
    lines.push(formatAnomalyHeadline(cell));
  } else {
    lines.push(cell.label || cell.state || 'Current activity');
  }

  lines.push(`${cell.current_spot_count || 0} spots`);
  lines.push(`${cell.current_unique_paths || 0} paths`);

  if (mode === 'anomaly') {
    const support = Number(cell.baseline_support || 0);
    if (support > 0) {
      lines.push(`${support} baseline events`);
    }
  }

  lines.push(`${Math.round((cell.confidence || 0) * 100)}% conf`);
  return lines.join(' · ');
}

/** @param {DxPulseMatrixCell} cell */
function formatAnomalyHeadline(cell) {
  const ratio = Number(cell.baseline_ratio || 0);
  const state = String(cell.state || '').toLowerCase();

  if (Number.isFinite(ratio) && ratio > 0) {
    return `×${formatCompactNumber(ratio)} baseline`;
  }

  if (state === 'insufficient_baseline') {
    return 'Sparse baseline';
  }

  return 'Current activity';
}

/** @param {DxPulseMatrixCell} cell */
function formatAnomalyDetail(cell) {
  const support = Number(cell.baseline_support || 0);
  const confidence = Math.round((Number(cell.confidence || 0)) * 100);

  if (support > 0) {
    return `${support} baseline events · ${confidence}% conf`;
  }
  return `${confidence}% conf`;
}

/** @param {number} value */
function formatCompactNumber(value) {
  if (!Number.isFinite(value)) return '0';
  if (value >= 100) return value.toFixed(0);
  if (value >= 10) return value.toFixed(1);
  return value.toFixed(2);
}

/** @param {DxPulseMatrixResponse} matrix */
function renderColorKey(matrix) {
  if (!elements.colorKey) return;
  const mode = String(matrix?.mode || elements.mode?.value || 'quality').toLowerCase();
  const palette = getActivePalette(mode);
  const labelLeft = mode === 'anomaly' ? 'Far below' : 'Weak';
  const labelRight = mode === 'anomaly' ? 'Far above' : 'Strong';
  elements.colorKey.innerHTML = `
    <span class="dxpulse-color-key-label">${escapeHtml(labelLeft)}</span>
    <div class="dxpulse-color-key-track" role="img" aria-label="16-step ${escapeHtml(mode)} color scale">
      ${palette.map((color, i) => `<span class="dxpulse-color-key-swatch" style="background:${color}" title="step ${i + 1}/16"></span>`).join('')}
    </div>
    <span class="dxpulse-color-key-label">${escapeHtml(labelRight)}</span>
    <span class="dxpulse-color-key-label">No propagation: <span class="dxpulse-color-key-swatch" style="display:inline-block;vertical-align:middle;width:16px;background:${NO_PROPAGATION_COLOR}"></span></span>
  `;
}

/**
 * @param {DxPulseMatrixCell} cell
 * @param {string} mode
 */
function colorForCell(cell, mode) {
  const spots = Number(cell.current_spot_count || 0);
  if (spots <= 0 || String(cell.state || '').toLowerCase() === 'none') {
    return { color: NO_PROPAGATION_COLOR, scaleIndex: -1 };
  }
  const palette = getActivePalette(mode);
  const value = mode === 'anomaly'
    ? anomalyScaleValue(cell, currentScaleContext)
    : qualityScaleValue(cell, currentScaleContext);
  const idx = Math.max(0, Math.min(palette.length - 1, Math.round(value * (palette.length - 1))));
  return { color: palette[idx], scaleIndex: idx };
}

/**
 * @param {string} mode
 * @returns {string[]}
 */
function getActivePalette(mode) {
  const paletteName = getPalettePreference(mode);
  return DXPULSE_PALETTES[paletteName] || DXPULSE_PALETTES[DEFAULT_PALETTES[mode === 'anomaly' ? 'anomaly' : 'quality']];
}

/**
 * @param {DxPulseMatrixCell} cell
 * @param {ScaleContext | null} context
 */
function qualityScaleValue(cell, context) {
  const spots = Number(cell.current_spot_count || 0);
  const paths = Number(cell.current_unique_paths || 0);
  const confidence = Number(cell.confidence || 0);
  const raw = (Math.log1p(spots) * 0.7) + (Math.log1p(paths) * 0.2) + (confidence * 0.8);
  const samples = Array.isArray(context?.qualitySamples) ? context.qualitySamples : [];
  if (samples.length >= 4) {
    const rank = percentileRank(samples, raw);
    return clamp01(Math.pow(rank, 0.9));
  }
  const normalized = normalizeWithContext(raw, context?.quality);
  return clamp01(Math.pow(normalized, 0.85));
}

/**
 * @param {DxPulseMatrixCell} cell
 * @param {ScaleContext | null} context
 */
function anomalyScaleValue(cell, context) {
  const raw = anomalySignal(cell);
  const samples = Array.isArray(context?.anomalySamples) ? context.anomalySamples : [];
  if (samples.length >= 4) {
    const rank = percentileRank(samples, raw);
    return clamp01(Math.pow(rank, 0.9));
  }
  const normalized = normalizeWithContext(raw, context?.anomaly);
  return clamp01(Math.pow(normalized, 0.9));
}

/** @param {DxPulseMatrixCell} cell */
function anomalySignal(cell) {
  const ratio = Number(cell.baseline_ratio || 0);
  const spots = Number(cell.current_spot_count || 0);
  const confidence = Number(cell.confidence || 0);

  if (!Number.isFinite(ratio) || ratio <= 0) {
    return 0;
  }

  const ratioTerm = Math.log2(Math.max(ratio, 1 / 64));
  const volumeTerm = Math.log1p(Math.max(0, spots));
  return (ratioTerm * 0.8) + (volumeTerm * 0.17) + (confidence * 0.03);
}

/** @param {DxPulseMatrixResponse} matrix */
function buildScaleContext(matrix) {
  const rows = Array.isArray(matrix?.matrix) ? matrix.matrix : [];
  const qualityValues = [];
  const anomalyValues = [];

  for (const row of rows) {
    for (const cell of row) {
      const spots = Number(cell?.current_spot_count || 0);
      if (spots <= 0 || String(cell?.state || '').toLowerCase() === 'none') {
        continue;
      }

      const paths = Number(cell?.current_unique_paths || 0);
      const confidence = Number(cell?.confidence || 0);
      qualityValues.push((Math.log1p(spots) * 0.7) + (Math.log1p(paths) * 0.2) + (confidence * 0.8));

      const ratio = Number(cell?.baseline_ratio || 0);
      if (Number.isFinite(ratio) && ratio > 0) {
        anomalyValues.push(anomalySignal(cell));
      }
    }
  }

  const sortedAnomalyValues = anomalyValues.slice().sort((a, b) => a - b);

  const sortedQualityValues = qualityValues.slice().sort((a, b) => a - b);
  return {
    quality: rangeFromValues(qualityValues, { min: 0, max: 4 }),
    anomaly: rangeFromValues(anomalyValues, { min: -2, max: 2 }),
    qualitySamples: sortedQualityValues,
    anomalySamples: sortedAnomalyValues
  };
}

/**
 * @param {number[]} sortedValues
 * @param {number} value
 */
function percentileRank(sortedValues, value) {
  if (!sortedValues.length || !Number.isFinite(value)) {
    return 0;
  }

  let low = 0;
  let high = sortedValues.length;
  while (low < high) {
    const mid = Math.floor((low + high) / 2);
    if (sortedValues[mid] <= value) {
      low = mid + 1;
    } else {
      high = mid;
    }
  }

  const rank = low / sortedValues.length;
  return clamp01(rank);
}

/**
 * @param {number[]} values
 * @param {ScaleRange} fallback
 * @returns {ScaleRange}
 */
function rangeFromValues(values, fallback) {
  if (!values.length) {
    return fallback;
  }
  let min = values[0];
  let max = values[0];
  for (const v of values) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  if (!Number.isFinite(min) || !Number.isFinite(max) || max - min < 0.001) {
    return fallback;
  }
  return { min, max };
}

/**
 * @param {number} value
 * @param {ScaleRange | undefined} context
 */
function normalizeWithContext(value, context) {
  const min = Number(context?.min);
  const max = Number(context?.max);
  if (!Number.isFinite(min) || !Number.isFinite(max) || max <= min) {
    return clamp01(value);
  }
  return clamp01((value - min) / (max - min));
}

/** @param {number} value */
function clamp01(value) {
  if (!Number.isFinite(value)) return 0;
  if (value < 0) return 0;
  if (value > 1) return 1;
  return value;
}

/** @param {string} hex */
function bestTextColor(hex) {
  const rgb = hexToRgb(hex);
  if (!rgb) return '#ffffff';
  const luminance = (0.299 * rgb.r) + (0.587 * rgb.g) + (0.114 * rgb.b);
  return luminance > 150 ? '#18202d' : '#ffffff';
}

/** @param {string} hex */
function hexToRgb(hex) {
  const cleaned = String(hex || '').replace('#', '');
  if (!/^[0-9a-fA-F]{6}$/.test(cleaned)) {
    return null;
  }
  return {
    r: parseInt(cleaned.slice(0, 2), 16),
    g: parseInt(cleaned.slice(2, 4), 16),
    b: parseInt(cleaned.slice(4, 6), 16)
  };
}

/**
 * @param {DxPulseSummaryResponse} summary
 * @param {DxPulseMatrixResponse} matrix
 */
function renderMeta(summary, matrix) {
  const parts = [
    `${escapeHtml(summary.mode_label || summary.mode || 'Mode')}`,
    `${matrix.window_minutes || summary.window_minutes} min`,
    `slot ${matrix.slot_of_day}`
  ];
  if (summary.baseline_available) {
    parts.push(`baseline ${matrix.baseline_lookback_days || 0} d`);
  }
  elements.meta.textContent = parts.join(' · ');
}

function clearPanels() {
  elements.bestBands.innerHTML = '';
  elements.topRegions.innerHTML = '';
  elements.hotCells.innerHTML = '';
  elements.table.innerHTML = '';
  elements.compactTable.innerHTML = '';
  currentMatrixData = null;
  if (elements.colorKey) elements.colorKey.innerHTML = '';
  elements.meta.textContent = '';
}

/**
 * @param {string} message
 * @param {string} tone
 */
function setStatus(message, tone) {
  elements.status.className = `small mb-2 text-${tone}`;
  elements.status.textContent = message;
}

/** @param {number} unixSeconds */
function formatTimestamp(unixSeconds) {
  if (!unixSeconds) {
    return 'just now';
  }
  return new Date(unixSeconds * 1000).toLocaleString();
}

/** @param {unknown} value */
function escapeHtml(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

window.addEventListener('beforeunload', () => {
  if (refreshTimer) {
    window.clearInterval(refreshTimer);
  }
});