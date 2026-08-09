import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, latLngToLocator, locatorToBounds, initialBearingDeg, getMinSnrMode, getSelectedBand, getEnabledBands, formatNumber } from './utils.js';

function escapeHtml(value) {
    return String(value ?? '')
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
}

export function initUI() {
    initInfoOverlay();
    initAutoLocateCoachmark();
}

// Grid-SNR brightness legend: shows the opacity-ramp panel below the style
// selector only while the Grid style is active.
export function initGridSnrLegend() {
    const panel = document.getElementById('grid-snr-legend');
    if (!panel || panel.dataset.legendWired === 'true') return;
    panel.dataset.legendWired = 'true';

    const syncLegend = () => {
        const style = document.querySelector('input[name="style-select"]:checked')?.value;
        panel.hidden = style !== 'grid-snr';
    };

    // The style radios are rendered later by the Svelte control bundle, so
    // listen via delegation instead of binding to them directly.
    document.addEventListener('change', (e) => {
        if (e.target && e.target.name === 'style-select') syncLegend();
    });

    // One-time cleanup: retired grading prefs (A/B highlight model +
    // score-gate experiments, Jul 2026); drop the stale keys.
    try { localStorage.removeItem('gridHighlightModel'); } catch (_) { /* private mode */ }
    try { localStorage.removeItem('gridScoreGate'); } catch (_) { /* private mode */ }

    syncLegend();
}

let autoLocateCoachmarkRetries = 0;
const AUTO_LOCATE_COACHMARK_MAX_RETRIES = 200; // ~10s at 50ms

function initAutoLocateCoachmark() {
    const DISMISS_KEY = 'autoLocateCoachmarkDismissed';
    const LEGACY_REAPPEAR_KEYS = [
        'autoLocateCoachmarkNextShowAt',
        'autoLocateCoachmarkDismissedAt',
        'autoLocateCoachmarkReappearDays'
    ];
    const qthInput = document.getElementById('qth');
    const geoButton = document.getElementById('btn-geo');
    const fetchForm = document.getElementById('fetch-form');
    const controls = document.getElementById('controls');

    // The #qth input is created by the Svelte bundle (dist/horst-ui.js), which
    // loads after app.js. If it hasn't mounted yet, retry shortly (mirrors
    // maybeAutoStartSavedQth's retry for the same element). Bounded so a failed
    // bundle load can't spin forever.
    if (!geoButton || !qthInput || !fetchForm) {
        if (autoLocateCoachmarkRetries < AUTO_LOCATE_COACHMARK_MAX_RETRIES) {
            autoLocateCoachmarkRetries += 1;
            setTimeout(initAutoLocateCoachmark, 50);
        }
        return;
    }

    // Explicitly disable any legacy TTL-based reappearance behavior.
    LEGACY_REAPPEAR_KEYS.forEach((key) => localStorage.removeItem(key));

    const hasExistingQth = !!localStorage.getItem('qth');
    if (hasExistingQth || localStorage.getItem(DISMISS_KEY) === 'true') {
        return;
    }

    const coachmark = document.createElement('div');
    coachmark.id = 'auto-locate-coachmark';
    coachmark.innerHTML = `
        <div class="coachmark-text">To get started, share your browser location or enter your Maidenhead locator</div>
        <div class="coachmark-arrow" aria-hidden="true"></div>
    `;

    document.body.appendChild(coachmark);

    const dismiss = () => {
        localStorage.setItem(DISMISS_KEY, 'true');
        if (coachmark.parentNode) {
            coachmark.parentNode.removeChild(coachmark);
        }
        window.removeEventListener('resize', positionCoachmark);
        controls?.removeEventListener('scroll', positionCoachmark);
        fetchForm.removeEventListener('submit', onQthSubmit, true);
    };

    const positionCoachmark = () => {
        if (!coachmark.isConnected) return;

        const r = geoButton.getBoundingClientRect();
        const margin = 8;
        const bubbleWidth = coachmark.offsetWidth || 320;
        const bubbleHeight = coachmark.offsetHeight || 80;

        let left = r.left + (r.width / 2) - (bubbleWidth / 2);
        left = Math.max(margin, Math.min(window.innerWidth - bubbleWidth - margin, left));

        let top = r.top - bubbleHeight - 14;
        if (top < margin) {
            top = Math.min(window.innerHeight - bubbleHeight - margin, r.bottom + 14);
            coachmark.classList.add('below-target');
        } else {
            coachmark.classList.remove('below-target');
        }

        coachmark.style.left = `${Math.round(left)}px`;
        coachmark.style.top = `${Math.round(top)}px`;

        const arrow = coachmark.querySelector('.coachmark-arrow');
        if (arrow) {
            const targetCenter = r.left + (r.width / 2);
            const arrowLeft = Math.max(18, Math.min(bubbleWidth - 18, targetCenter - left));
            arrow.style.left = `${Math.round(arrowLeft)}px`;
        }
    };

    const onQthSubmit = () => {
        const qth = qthInput.value.trim();
        if (qth) {
            dismiss();
        }
    };

    fetchForm.addEventListener('submit', onQthSubmit, true);
    window.addEventListener('resize', positionCoachmark);
    controls?.addEventListener('scroll', positionCoachmark);

    requestAnimationFrame(positionCoachmark);
}

export function attachUITooltipEvents() {
    const tooltip = document.getElementById('tooltip');
    if (!tooltip || !map) return;

    const HOVER_FETCH_DELAY_MS = 750;

    map.off('mousemove');
    map.off('mouseout');

    const hoverCache = new Map();
    let hoverTimer = null;
    let hoverController = null;
    let hoverRequestSeq = 0;
    let activeHoverKey = '';
    let hoverSquareLayer = null;
    let hoverSquareLocator = '';
    let hoverSquareTheme = '';

    function getHoverSquareStyle() {
        const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
        if (theme === 'dark') {
            return {
                color: '#ffe39a',
                weight: 2,
                opacity: 1,
                fillColor: '#ffe39a',
                fillOpacity: 0.20,
                interactive: false
            };
        }

        return {
            color: '#cc9a1f',
            weight: 2,
            opacity: 0.95,
            fillColor: '#ffd166',
            fillOpacity: 0.14,
            interactive: false
        };
    }

    function clearHoverSquareHighlight() {
        if (!hoverSquareLayer || !map) return;
        map.removeLayer(hoverSquareLayer);
        hoverSquareLayer = null;
        hoverSquareLocator = '';
        hoverSquareTheme = '';
    }

    function updateHoverSquareHighlight(locator) {
        if (!map || !locator) {
            clearHoverSquareHighlight();
            return;
        }

        const theme = document.body.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
        const style = getHoverSquareStyle();

        if (hoverSquareLocator === locator && hoverSquareLayer) {
            if (hoverSquareTheme !== theme) {
                hoverSquareLayer.setStyle(style);
                hoverSquareTheme = theme;
            }
            return;
        }

        const bounds = locatorToBounds(locator);
        if (!bounds) {
            clearHoverSquareHighlight();
            return;
        }

        clearHoverSquareHighlight();
        hoverSquareLayer = L.rectangle(bounds, style).addTo(map);
        hoverSquareLocator = locator;
        hoverSquareTheme = theme;
    }

    function hideTooltip() {
        if (hoverTimer) {
            clearTimeout(hoverTimer);
            hoverTimer = null;
        }
        if (hoverController) {
            hoverController.abort();
            hoverController = null;
        }
        activeHoverKey = '';
        tooltip.style.display = 'none';
        clearHoverSquareHighlight();
    }

    function positionTooltip(e) {
        tooltip.style.left = (e.originalEvent.pageX + 15) + 'px';
        tooltip.style.top = (e.originalEvent.pageY + 15) + 'px';
    }

    // Great-circle bearing from the target square (station) to the hovered
    // square center, e.g. " 302°". Empty when the qth isn't a valid
    // locator (the field also accepts callsigns) or hovers the qth itself.
    function hoverSquareAzimuthText(hoverLocator) {
        const qth = document.getElementById('qth')?.value?.trim()?.toUpperCase() || '';
        const locatorRe = /^[A-R]{2}[0-9]{2}([A-X]{2})?$/;
        if (!locatorRe.test(qth) || !locatorRe.test(hoverLocator)) return '';
        if (qth === hoverLocator) return '';
        const qthBounds = locatorToBounds(qth);
        const hoverBounds = locatorToBounds(hoverLocator);
        if (!qthBounds || !hoverBounds) return '';
        const center = (b) => [(b[0][0] + b[1][0]) / 2, (b[0][1] + b[1][1]) / 2];
        const [tLat, tLng] = center(qthBounds);
        const [hLat, hLng] = center(hoverBounds);
        const bearing = Math.round(initialBearingDeg(tLat, tLng, hLat, hLng)) % 360;
        return ` ${bearing}°`;
    }

    function renderDetails(locator, data) {
        const count = Number(data?.count || 0);
        if (count <= 0) {
            hideTooltip();
            return;
        }
        const min = Number.isFinite(Number(data?.min_snr)) ? Number(data.min_snr) : 0;
        const max = Number.isFinite(Number(data?.max_snr)) ? Number(data.max_snr) : 0;
        const avg = Number.isFinite(Number(data?.avg_snr)) ? Number(data.avg_snr) : 0;
        const bestBand = data?.best_band ? escapeHtml(data.best_band) : '—';
        const topReports = Array.isArray(data?.top_reports) ? data.top_reports : [];

        let reportsHtml = '';
        if (topReports.length > 0) {
            reportsHtml = `<hr style="margin: 5px 0; border: 0; border-top: 1px solid var(--tooltip-border);">` +
                `<span style="font-size: 11px;"><b>Top Reports:</b><br>` +
                topReports.map((r) => {
                    const sender = escapeHtml(r.sender || '—');
                    const receiver = escapeHtml(r.receiver || '—');
                    const band = escapeHtml(r.band || '—');
                    const snr = Number.isFinite(Number(r.snr)) ? Number(r.snr) : 0;
                    return `${sender} / ${receiver} / ${band} / ${snr}dB`;
                }).join('<br>') +
                `</span>`;
        }

        tooltip.innerHTML = `<strong>${escapeHtml(locator)}${escapeHtml(hoverSquareAzimuthText(locator))}</strong><br>` +
            `Min: ${min}dB<br>` +
            `Max: ${max}dB<br>` +
            `Avg: ${Math.round(avg)}dB<br>` +
            `Best Band: ${bestBand}<br>` +
            `Spots: ${formatNumber(count)}` +
            reportsHtml;
        tooltip.style.display = 'block';
    }

    function buildHoverRequestKey(params) {
        // Bucket the key by wall-clock time so the cache naturally expires:
        // spot counts change as spots arrive and age out, and without a time
        // component the cached payload would be served forever for unchanged
        // filter params. 30s is plenty fresh for a hover preview.
        const timeBucket = Math.floor(Date.now() / 30000);
        return [
            timeBucket,
            params.qth,
            params.locator,
            params.minutes,
            params.surroundings ? '1' : '0',
            params.minSnrMode,
            params.ssbMinDb,
            params.cwMinDb,
            params.selectedBand,
            params.enabledBands
        ].join('|');
    }

    function getHoverParams(locator) {
        const qth = document.getElementById('qth')?.value?.trim()?.toUpperCase() || '';
        const minutes = parseInt(document.getElementById('minutes')?.value || '15', 10) || 15;
        const minSnrMode = getMinSnrMode();
        const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
        const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
        const selectedBand = getSelectedBand();
        const enabledBands = Array.from(getEnabledBands()).sort().join(',');
        const surroundings = document.getElementById('surroundings')?.checked === true;

        return {
            qth,
            locator,
            minutes,
            minSnrMode,
            ssbMinDb,
            cwMinDb,
            selectedBand,
            enabledBands,
            surroundings
        };
    }

    async function fetchHoverDetails(params, requestKey) {
        if (!params.qth) {
            hideTooltip();
            return;
        }

        if (hoverController) {
            hoverController.abort();
        }
        hoverController = new AbortController();
        const seq = ++hoverRequestSeq;

        const query = new URLSearchParams();
        query.set('qth', params.qth);
        query.set('locator', params.locator);
        query.set('minutes', String(params.minutes));
        if (params.surroundings) query.set('surroundings', 'true');
        if (params.minSnrMode) query.set('min_snr_mode', params.minSnrMode);
        if (Number.isFinite(params.ssbMinDb)) query.set('ssb_min_db', String(params.ssbMinDb));
        if (Number.isFinite(params.cwMinDb)) query.set('cw_min_db', String(params.cwMinDb));
        if (params.selectedBand) query.set('selected_band', params.selectedBand);
        if (params.enabledBands) query.set('enabled_bands', params.enabledBands);

        try {
            const res = await fetch(`/api/square_details?${query.toString()}`, { signal: hoverController.signal });
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            const payload = await res.json();
            if (seq !== hoverRequestSeq || requestKey !== activeHoverKey) return;
            hoverCache.set(requestKey, payload);
            renderDetails(params.locator, payload);
        } catch (err) {
            if (err?.name === 'AbortError') return;
            if (seq !== hoverRequestSeq || requestKey !== activeHoverKey) return;
            tooltip.innerHTML = `<strong>${escapeHtml(params.locator)}</strong><br><span style="color: #c00;">Hover details unavailable</span>`;
            tooltip.style.display = 'block';
        } finally {
            if (hoverController?.signal?.aborted) {
                hoverController = null;
            }
        }
    }

    map.on('mousemove', function(e) {
        if (state.dxClusterHoverActive) {
            hideTooltip();
            return;
        }

        const res = getGridResolution();
        const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, res);
        updateHoverSquareHighlight(loc);

        const params = getHoverParams(loc);
        if (!params.qth) {
            hideTooltip();
            return;
        }

        activeHoverKey = buildHoverRequestKey(params);
        positionTooltip(e);

        const cached = hoverCache.get(activeHoverKey);
        if (cached) {
            if (Number(cached?.count || 0) > 0) {
                renderDetails(loc, cached);
            } else {
                hideTooltip();
            }
            return;
        }

        tooltip.style.display = 'none';

        if (hoverTimer) {
            clearTimeout(hoverTimer);
        }
        hoverTimer = setTimeout(() => {
            hoverTimer = null;
            void fetchHoverDetails(params, activeHoverKey);
        }, HOVER_FETCH_DELAY_MS);
    });

    map.on('mouseout', function() {
        hideTooltip();
    });
}

function initInfoOverlay() {
    const themeToggleBtn = document.getElementById('theme-toggle');
    if (themeToggleBtn && themeToggleBtn.parentNode) {
        const headerActions = document.createElement('div');
        headerActions.style.display = 'flex';
        headerActions.style.gap = '8px';
        
        const infoBtn = document.createElement('button');
        infoBtn.id = 'info-toggle';
        infoBtn.innerHTML = '<i class="fas fa-question-circle"></i>';
        infoBtn.title = 'Help';
        infoBtn.style.background = 'none';
        infoBtn.style.border = '1px solid var(--border-color)';
        infoBtn.style.borderRadius = '5px';
        infoBtn.style.cursor = 'pointer';
        infoBtn.style.fontSize = '18px';
        infoBtn.style.padding = '4px 8px';
        infoBtn.style.lineHeight = '1';

        themeToggleBtn.parentNode.insertBefore(headerActions, themeToggleBtn);
        headerActions.appendChild(infoBtn);
        headerActions.appendChild(themeToggleBtn);

        const overlay = document.createElement('div');
        overlay.id = 'info-overlay';
        overlay.innerHTML = `
            <div class="info-content">
                <button id="close-info" title="Close">&times;</button>
                <iframe src="info.html" frameborder="0"></iframe>
            </div>
        `;
        document.body.appendChild(overlay);

        infoBtn.addEventListener('click', () => overlay.style.display = 'flex');
        document.getElementById('close-info').addEventListener('click', () => overlay.style.display = 'none');
        overlay.addEventListener('click', (e) => { if (e.target === overlay) overlay.style.display = 'none'; });

        if (!localStorage.getItem('infoShown')) {
            overlay.style.display = 'flex';
            localStorage.setItem('infoShown', 'true');
        }
    }
}

function initServerStats() {
    function updateServerStats() {
        if (document.hidden) return;

        const statsEl = document.getElementById('server-stats');
        if (!statsEl) return;

        fetch('/api/stats')
            .then(response => response.json())
            .then(stats => {
                statsEl.innerHTML = `Connections: ${formatNumber(stats.active_connections)} History: ${formatNumber(stats.history_size)} spots (${formatNumber(stats.history_minutes)} mins)`;
            })
            .catch(error => {
                console.error('Error fetching server stats:', error);
                statsEl.innerHTML = 'Server stats unavailable.';
            });
    }

    updateServerStats();
    setInterval(updateServerStats, 10000);
    document.addEventListener('visibilitychange', () => {
        if (!document.hidden) {
            updateServerStats();
        }
    });
}