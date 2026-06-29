// @ts-check

/**
 * @param {Element | null} el
 * @returns {HTMLInputElement | null}
 */
function asInput(el) {
    return el instanceof HTMLInputElement ? el : null;
}

/**
 * @param {string} id
 * @returns {HTMLInputElement | null}
 */
function inputById(id) {
    return asInput(document.getElementById(id));
}

/**
 * @param {string} selector
 * @returns {HTMLInputElement | null}
 */
function inputByQuery(selector) {
    return asInput(document.querySelector(selector));
}

export function loadConfig() {
    /** @type {[number, number]} */
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
        document.querySelectorAll('.band-enable').forEach((cb) => {
            const input = asInput(cb);
            if (!input) return;
            const savedEnable = localStorage.getItem(`enable-${input.value}`);
            if (savedEnable !== null) {
                input.checked = savedEnable === 'true';
                const radio = inputByQuery(`input[name="band"][value="${input.value}"]`);
                if (radio) radio.disabled = !input.checked;
            }
        });

        const savedBand = localStorage.getItem('selectedBand');
        if (savedBand) {
            const radio = inputByQuery(`input[name="band"][value="${savedBand}"]`);
            if (radio) radio.checked = true;
        }

        const savedCycleTime = localStorage.getItem('cycleTime');
        if (savedCycleTime !== null) {
            const el = inputById('cycle-time');
            if (el) el.value = savedCycleTime;
        }

        const savedShowDXClusterSpots = localStorage.getItem('showDXClusterSpots');
        const legacyShowDxclusterSpots = localStorage.getItem('showDxclusterSpots');
        const legacyShowSpotsSameRegion = localStorage.getItem('showSpotsSameRegion');
        const el = inputById('show-dxcluster-spots');
        if (el) {
            if (savedShowDXClusterSpots !== null) {
                el.checked = savedShowDXClusterSpots === 'true';
            } else if (legacyShowDxclusterSpots !== null) {
                el.checked = legacyShowDxclusterSpots === 'true';
            } else if (legacyShowSpotsSameRegion === 'true') {
                el.checked = true;
            } else {
                el.checked = true;
            }
        }

    } catch (e) {
        console.error("Error parsing saved form state", e);
    }

    return { initialCenter, initialZoom };
}