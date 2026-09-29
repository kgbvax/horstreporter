import { describe, expect, it } from 'vitest';

import {
    areaCohortChanged,
    areaLabel,
    areaSide,
    areaSummary,
    bandAreaTag,
    parseAreaPayload,
} from './live-area.js';

const wide = { centre: 'FN76', base_radius: 0, radius: 2, widened: true };

describe('parseAreaPayload', () => {
    it('reads the server object', () => {
        expect(parseAreaPayload(wide)).toEqual({ centre: 'FN76', baseRadius: 0, radius: 2, widened: true });
    });

    it('reads a JSON string, as delivered by the stream event', () => {
        expect(parseAreaPayload(JSON.stringify(wide))).toEqual({ centre: 'FN76', baseRadius: 0, radius: 2, widened: true });
    });

    it('normalises the centre and defaults a missing base radius', () => {
        expect(parseAreaPayload({ centre: ' fn76 ', radius: 1 })).toEqual({ centre: 'FN76', baseRadius: 0, radius: 1, widened: false });
    });

    it('only treats widened === true as widened', () => {
        expect(parseAreaPayload({ centre: 'FN76', radius: 1, widened: 'true' }).widened).toBe(false);
    });

    it('rejects missing and malformed payloads', () => {
        for (const bad of [null, undefined, '', 'not json', 42, [], {}, { centre: 'FN76' }, { radius: 1 },
            { centre: 'FN76', radius: -1 }, { centre: 'FN76', radius: 1.5 }, { centre: 'FN76', radius: 'x' }]) {
            expect(parseAreaPayload(bad)).toBeNull();
        }
    });
});

describe('area wording', () => {
    const area = parseAreaPayload(wide);

    it('gives the block side in squares', () => {
        expect(areaSide(parseAreaPayload({ centre: 'FN76', radius: 0 }))).toBe(1);
        expect(areaSide(area)).toBe(5);
        expect(areaSide(null)).toBe(0);
    });

    it('labels the block', () => {
        expect(areaLabel(area)).toBe('5×5 squares around FN76');
        expect(areaLabel(null)).toBe('');
    });

    it('summarises only a widened area', () => {
        expect(areaSummary(area)).toBe('Area: 5×5 squares around FN76, widened for a fuller sample.');
        expect(areaSummary(parseAreaPayload({ centre: 'JO32', radius: 0, widened: false }))).toBe('');
        expect(areaSummary(null)).toBe('');
    });

    it('carries no emoji', () => {
        expect(areaSummary(area)).toMatch(/^[\x20-\x7e×]+$/);
    });
});

describe('bandAreaTag', () => {
    it('tags a band that only has a sample through widening', () => {
        expect(bandAreaTag({ area_widened: true })).toBe('wide area');
    });

    it('is blank otherwise', () => {
        expect(bandAreaTag({ area_widened: false })).toBe('');
        expect(bandAreaTag({})).toBe('');
        expect(bandAreaTag(null)).toBe('');
    });
});

describe('areaCohortChanged', () => {
    const a = (radius, centre = 'FN76') => parseAreaPayload({ centre, radius });

    it('flags a radius change for the same station', () => {
        expect(areaCohortChanged(a(1), a(2))).toBe(true);
        expect(areaCohortChanged(a(2), a(0))).toBe(true);
    });

    it('ignores an unchanged radius, a new station and a first area', () => {
        expect(areaCohortChanged(a(2), a(2))).toBe(false);
        expect(areaCohortChanged(a(1, 'JO32'), a(2, 'FN76'))).toBe(false);
        expect(areaCohortChanged(null, a(2))).toBe(false);
        expect(areaCohortChanged(a(2), null)).toBe(false);
    });
});
