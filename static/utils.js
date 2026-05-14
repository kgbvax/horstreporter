export function formatNumber(num) {
    if (num == null) return '';
    return num.toString().replace(/\B(?=(\d{3})+(?!\d))/g, " ");
}

export function latLngToLocator(lat, lng, precision = 4) {
    lng = Math.max(-180, Math.min(180, lng));
    lat = Math.max(-90, Math.min(90, lat));
    let _lon = lng + 180;
    let _lat = lat + 90;
    let char1 = String.fromCharCode(65 + Math.floor(_lon / 20));
    let char2 = String.fromCharCode(65 + Math.floor(_lat / 10));
    _lon = _lon % 20;
    _lat = _lat % 10;
    let char3 = String.fromCharCode(48 + Math.floor(_lon / 2));
    let char4 = String.fromCharCode(48 + Math.floor(_lat / 1));
    
    if (precision < 6) return char1 + char2 + char3 + char4;
    
    _lon = (_lon % 2) * 60;
    _lat = (_lat % 1) * 60;
    let char5 = String.fromCharCode(65 + Math.floor(_lon / 5));
    let char6 = String.fromCharCode(65 + Math.floor(_lat / 2.5));
    return char1 + char2 + char3 + char4 + char5 + char6;
}

export function locatorToBounds(locator) {
    if (!locator || locator.length < 4) return null;
    locator = locator.toUpperCase();
    let lng = (locator.charCodeAt(0) - 65) * 20 - 180;
    let lat = (locator.charCodeAt(1) - 65) * 10 - 90;
    lng += (locator.charCodeAt(2) - 48) * 2;
    lat += (locator.charCodeAt(3) - 48) * 1;
    
    if (locator.length >= 6) {
        lng += (locator.charCodeAt(4) - 65) * (5/60);
        lat += (locator.charCodeAt(5) - 65) * (2.5/60);
        return [[lat, lng], [lat + (2.5/60), lng + (5/60)]];
    }
    return [[lat, lng], [lat + 1, lng + 2]];
}

export function setFaviconColor(color) {
    const favicon = document.getElementById('favicon');
    if (favicon) {
        const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="8" fill="${color}"/></svg>`;
        favicon.href = `data:image/svg+xml,${encodeURIComponent(svg)}`;
    }
}

export const bandColors = {
    '80m': '#800080',
    '40m': '#0000FF',
    '20m': '#008000',
    '17m': '#808000',
    '15m': '#FFA500',
    '12m': '#00FFFF',
    '10m': '#FF0000',
    '6m':  '#FF00FF',
    'all': '#555555'
};

export function getGridResolution() {
    const target = document.getElementById('target')?.value.trim() || '';
    if (/^[A-Za-z]{2}[0-9]{2}[A-Za-z]{2}/.test(target)) {
        return 6;
    }
    return 4;
}

export function getMinSnrMode() {
    const checkedRadio = document.querySelector('input[name="min-snr"]:checked');
    return checkedRadio ? checkedRadio.value : 'none';
}

export function getSelectedBand() {
    const checkedRadio = document.querySelector('input[name="band"]:checked');
    return checkedRadio ? checkedRadio.value : 'all';
}

export function getEnabledBands() {
    const enabled = new Set();
    document.querySelectorAll('.band-enable').forEach(cb => {
        if (cb.checked) enabled.add(cb.value);
    });
    return enabled;
}