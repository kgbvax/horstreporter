import { describe, expect, it } from 'vitest';
import { isDareBand } from '../static/horst-kevin.js';

describe('Horst-Kevin band scope', () => {
    it('comments on HF from 80m to 10m', () => {
        for (const b of ['80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '20M']) {
            expect(isDareBand(b)).toBe(true);
        }
    });

    it('stays quiet about 160m and everything above 10m', () => {
        for (const b of ['160m', '6m', '4m', '2m', '70cm', '23cm', '13cm', '', null, undefined]) {
            expect(isDareBand(b)).toBe(false);
        }
    });
});
