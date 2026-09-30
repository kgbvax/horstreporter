// hot-bands-client.test.js — the shared /api/hot_bands fetch used by the
// hot-band pills and Horst-Kevin.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import { fetchHotBands, hotBandsParams, resetHotBandsClient } from '../static/hot-bands-client.js';

function okResponse(body) {
    return { ok: true, status: 200, json: async () => body };
}

describe('hotBandsParams', () => {
    it('builds the same query for every caller', () => {
        expect(hotBandsParams({ qth: 'JO32', surroundings: true, currentBand: '20M' }).toString())
            .toBe('qth=JO32&surroundings=true&rings=auto&current_band=20m');
    });

    it('omits surroundings and the all/empty band', () => {
        expect(hotBandsParams({ qth: 'JO32', surroundings: false, currentBand: 'all' }).toString())
            .toBe('qth=JO32&rings=auto');
        expect(hotBandsParams({ qth: 'JO32' }).toString()).toBe('qth=JO32&rings=auto');
    });
});

describe('fetchHotBands', () => {
    let fetchMock;
    beforeEach(() => {
        resetHotBandsClient();
        fetchMock = vi.fn(async () => okResponse({ recommendations: [1] }));
        vi.stubGlobal('fetch', fetchMock);
    });
    afterEach(() => vi.unstubAllGlobals());

    const params = (qth = 'JO32') => hotBandsParams({ qth });

    it('shares one request between concurrent callers', async () => {
        const [a, b] = await Promise.all([fetchHotBands(params()), fetchHotBands(params())]);
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(a).toBe(b);
    });

    it('reuses a fresh response and refetches after the TTL', async () => {
        let t = 1000;
        const opts = { ttlMs: 10000, now: () => t };
        await fetchHotBands(params(), opts);
        t += 9000;
        await fetchHotBands(params(), opts);
        expect(fetchMock).toHaveBeenCalledTimes(1);
        t += 2000;
        await fetchHotBands(params(), opts);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    it('does not share between different params', async () => {
        await Promise.all([fetchHotBands(params('JO32')), fetchHotBands(params('FN31'))]);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    it('rejects every waiting caller and does not cache the failure', async () => {
        fetchMock.mockResolvedValueOnce({ ok: false, status: 503, json: async () => ({}) });
        const results = await Promise.allSettled([fetchHotBands(params()), fetchHotBands(params())]);
        expect(results.map((r) => r.status)).toEqual(['rejected', 'rejected']);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        await expect(fetchHotBands(params())).resolves.toEqual({ recommendations: [1] });
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });
});
