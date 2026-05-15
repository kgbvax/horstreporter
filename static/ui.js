import { state } from './state.js';
import { map } from './map.js';
import { getGridResolution, latLngToLocator, getMinSnrMode, getSelectedBand, getEnabledBands, formatNumber } from './utils.js';

export function initUI() {
    initTooltip();
    initInfoOverlay();
    initServerStats();
}

function initTooltip() {
    const tooltip = document.getElementById('tooltip');
    if (!tooltip) return;

    map.on('mousemove', function(e) {
        const res = getGridResolution();
        const loc = latLngToLocator(e.latlng.lat, e.latlng.lng, res);
        const minSnrMode = getMinSnrMode();
        const ssbMinDb = parseInt(document.getElementById('ssb-min-db')?.value || '0', 10);
        const cwMinDb = parseInt(document.getElementById('cw-min-db')?.value || '-15', 10);
        const selectedBand = getSelectedBand();
        const enabledBands = getEnabledBands();

        const squareSpots = state.liveSpots.filter(s => {
            if (!s.locator || !s.locator.startsWith(loc)) return false;
            if (minSnrMode === 'ssb' && s.snr < ssbMinDb) return false;
            if (minSnrMode === 'cw' && s.snr < cwMinDb) return false;
            if (!enabledBands.has(s.band)) return false;
            if (selectedBand !== 'all' && s.band !== selectedBand) return false;
            return true;
        });

        if (squareSpots.length > 0) {
            let min = Math.min(...squareSpots.map(s => s.snr));
            let max = Math.max(...squareSpots.map(s => s.snr));
            let avg = Math.round(squareSpots.reduce((sum, s) => sum + s.snr, 0) / squareSpots.length);
            
            let bestBand = 'N/A';
            let bestSnr = -999;
            squareSpots.forEach(s => {
                if (s.snr > bestSnr) {
                    bestSnr = s.snr;
                    bestBand = s.band;
                }
            });
            
            squareSpots.sort((a, b) => b.snr - a.snr);
            
            let uniqueSpots = [];
            let seenPairs = new Set();
            for (let s of squareSpots) {
                let pairKey = `${s.sender}-${s.receiver}`;
                if (!seenPairs.has(pairKey)) {
                    seenPairs.add(pairKey);
                    uniqueSpots.push(s);
                    if (uniqueSpots.length >= 10) break;
                }
            }

            let reportsHtml = `<hr style="margin: 5px 0; border: 0; border-top: 1px solid var(--tooltip-border);">` +
                              `<span style="font-size: 11px;"><b>Top Reports:</b><br>`;
            uniqueSpots.forEach(s => {
                reportsHtml += `${s.sender} / ${s.receiver} / ${s.snr}dB<br>`;
            });
            reportsHtml += `</span>`;

            tooltip.innerHTML = `<strong>${loc}</strong><br>Min: ${min}dB<br>Max: ${max}dB<br>Avg: ${avg}dB<br>Best Band: ${bestBand}<br>Spots: ${formatNumber(squareSpots.length)}${reportsHtml}`;
            tooltip.style.display = 'block';
            tooltip.style.left = (e.originalEvent.pageX + 15) + 'px';
            tooltip.style.top = (e.originalEvent.pageY + 15) + 'px';
        } else {
            tooltip.style.display = 'none';
        }
    });

    map.on('mouseout', function() {
        tooltip.style.display = 'none';
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
}