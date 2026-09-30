import { describe, expect, it } from 'vitest';
import { isDareBand } from '../static/horst-kevin.js';

const OCT = Date.UTC(2026, 9, 15, 12);
const JUN = Date.UTC(2026, 5, 15, 12);

describe('Horst-Kevin band scope', () => {
    it('comments on HF from 80m to 10m, without 60m', () => {
        for (const b of ['80m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '20M']) {
            expect(isDareBand(b, OCT)).toBe(true);
        }
    });

    it('stays quiet about 160m, 60m and everything above 10m outside the Es season', () => {
        for (const b of ['160m', '60m', '6m', '4m', '2m', '70cm', '23cm', '13cm', '', null, undefined]) {
            expect(isDareBand(b, OCT)).toBe(false);
        }
    });

    it('adds 6m (only 6m) from May to August UTC', () => {
        expect(isDareBand('6m', JUN)).toBe(true);
        for (const b of ['160m', '60m', '4m', '2m']) expect(isDareBand(b, JUN)).toBe(false);
    });
});
