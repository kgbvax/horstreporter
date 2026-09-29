// live-area.js — the adaptive live area (server side: live_area.go).
//
// Every live request sends rings=auto. In a sparse region the server widens the
// block of grid squares around the home square until enough bands carry a full
// sample, and says so in an `area` object (dx_conditions, hot_bands,
// prop_intel/v2) and an `area` stream event. A band that only has a sample
// because of the widening carries `area_widened` in its dx_conditions metrics.
// This module parses that and words it; it touches no DOM.

// Normalise a raw `area` payload (object or JSON string). null when it is
// missing or malformed, so callers can treat "no area" and "bad area" alike.
export function parseAreaPayload(raw) {
    let value = raw;
    if (typeof raw === 'string') {
        try {
            value = JSON.parse(raw);
        } catch {
            return null;
        }
    }
    if (!value || typeof value !== 'object') return null;
    const radius = Number(value.radius);
    const baseRadius = Number(value.base_radius);
    const centre = String(value.centre || '').trim().toUpperCase();
    if (!Number.isInteger(radius) || radius < 0 || !centre) return null;
    return {
        centre,
        radius,
        baseRadius: Number.isInteger(baseRadius) && baseRadius >= 0 ? baseRadius : 0,
        widened: value.widened === true,
    };
}

// Side of the block in grid squares: radius 2 -> 5 (a 5x5 block).
export function areaSide(area) {
    return area ? 2 * area.radius + 1 : 0;
}

// "5×5 squares around FN76"
export function areaLabel(area) {
    if (!area) return '';
    const side = areaSide(area);
    return `${side}×${side} squares around ${area.centre}`;
}

// One line for the dock header, only when the area is wider than what was
// asked for: a row says something only when there is something to say.
export function areaSummary(area) {
    if (!area || !area.widened) return '';
    return `Area: ${areaLabel(area)}, widened for a fuller sample.`;
}

// Per-band note for a dx_conditions band entry.
export function bandAreaTag(metrics) {
    return metrics && metrics.area_widened === true ? 'wide area' : '';
}

// True when the area a stream delivers changed for the same station, which
// makes the session ring's earlier windows foreign (see app.js).
export function areaCohortChanged(prev, next) {
    if (!prev || !next) return false;
    return prev.centre === next.centre && prev.radius !== next.radius;
}
