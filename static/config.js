export function loadConfig() {
    let initialCenter = [20, 0];
    let initialZoom = 2;

    try {
        const savedCenter = localStorage.getItem('mapCenter');
        const savedZoom = localStorage.getItem('mapZoom');
        if (savedCenter) initialCenter = JSON.parse(savedCenter);
        if (savedZoom) initialZoom = parseFloat(savedZoom);
    } catch (e) {
        console.error("Error parsing saved map state", e);
    }

    try {
        document.querySelectorAll('.band-enable').forEach(cb => {
            const savedEnable = localStorage.getItem(`enable-${cb.value}`);
            if (savedEnable !== null) {
                cb.checked = savedEnable === 'true';
                const radio = document.querySelector(`input[name="band"][value="${cb.value}"]`);
                if (radio) radio.disabled = !cb.checked;
            }
        });

        const savedTarget = localStorage.getItem('target');
        if (savedTarget) document.getElementById('target').value = savedTarget;

        const savedMinutes = localStorage.getItem('minutes');
        if (savedMinutes) document.getElementById('minutes').value = savedMinutes;

        const savedMinSnr = localStorage.getItem('minSnrSelect');
        if (savedMinSnr) {
            const radio = document.querySelector(`input[name="min-snr"][value="${savedMinSnr}"]`);
            if (radio) radio.checked = true;
        }

        const savedStyle = localStorage.getItem('mapStyle');
        if (savedStyle) {
            const radio = document.querySelector(`input[name="style-select"][value="${savedStyle}"]`);
            if (radio) radio.checked = true;
        }

        const savedBand = localStorage.getItem('selectedBand');
        if (savedBand) {
            const radio = document.querySelector(`input[name="band"][value="${savedBand}"]`);
            if (radio) radio.checked = true;
        }

        const savedSsbMinDb = localStorage.getItem('ssbMinDb');
        if (savedSsbMinDb !== null && document.getElementById('ssb-min-db')) {
            document.getElementById('ssb-min-db').value = savedSsbMinDb;
        }

        const savedCwMinDb = localStorage.getItem('cwMinDb');
        if (savedCwMinDb !== null && document.getElementById('cw-min-db')) {
            document.getElementById('cw-min-db').value = savedCwMinDb;
        }

        const savedCycleTime = localStorage.getItem('cycleTime');
        if (savedCycleTime !== null && document.getElementById('cycle-time')) {
            document.getElementById('cycle-time').value = savedCycleTime;
        }

        const savedClusterDist = localStorage.getItem('clusterDistance');
        if (savedClusterDist !== null && document.getElementById('cluster-distance')) {
            document.getElementById('cluster-distance').value = savedClusterDist;
            document.getElementById('cluster-dist-val').textContent = savedClusterDist;
        }

        const savedSmoothEdges = localStorage.getItem('smoothEdges');
        if (savedSmoothEdges !== null && document.getElementById('smooth-edges')) {
            document.getElementById('smooth-edges').checked = savedSmoothEdges === 'true';
        }

        const savedAutoZoom = localStorage.getItem('autoZoom');
        if (savedAutoZoom !== null && document.getElementById('auto-zoom')) {
            document.getElementById('auto-zoom').checked = savedAutoZoom === 'true';
        }

        const savedSurroundings = localStorage.getItem('surroundings');
        if (savedSurroundings !== null && document.getElementById('surroundings')) {
            document.getElementById('surroundings').checked = savedSurroundings === 'true';
        }
    } catch (e) {
        console.error("Error parsing saved form state", e);
    }

    return { initialCenter, initialZoom };
}