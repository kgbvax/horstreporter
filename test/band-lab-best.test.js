import { describe, it, expect } from 'vitest';
import { enabledBestBands } from '../static/band-lab.js';

// /api/dx_conditions returns `bands` sorted by score (desc) plus best_bands /
// recommended_bands cut to the top 3 of ALL bands (dx_conditions.go).
const resp = {
    bands: [
        { band: '10m', status: 'green' },
        { band: '12m', status: 'green' },
        { band: '15m', status: 'yellow' },
        { band: '20m', status: 'green' },
        { band: '40m', status: 'yellow' },
        { band: '80m', status: 'red' },
    ],
    best_bands: ['10m', '12m', '15m'],
    recommended_bands: ['10m', '12m', '15m'],
};

describe('enabledBestBands (Band stats "Best now")', () => {
    it('re-derives from the full list so enabled bands below the global top 3 are kept', () => {
        const { bestBands, recBands } = enabledBestBands(resp, new Set(['20m', '40m', '80m']));
        expect(bestBands).toEqual(['20m', '40m', '80m']);
        expect(recBands).toEqual(['20m', '40m']);
    });

    it('never recommends a disabled band', () => {
        const { bestBands, recBands } = enabledBestBands(resp, new Set(['12m', '20m']));
        expect(bestBands).toEqual(['12m', '20m']);
        expect(recBands).toEqual(['12m', '20m']);
    });

    it('falls back to filtering the short lists when `bands` is absent', () => {
        const { bestBands, recBands } = enabledBestBands(
            { best_bands: ['10m', '20m'], recommended_bands: ['10m'] }, new Set(['20m']));
        expect(bestBands).toEqual(['20m']);
        expect(recBands).toEqual([]);
    });
});
