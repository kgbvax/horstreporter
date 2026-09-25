import { describe, expect, it, vi } from 'vitest';

vi.mock('../static/perf.js', () => ({
    startPerfTimer: () => 0,
    endPerfTimer: () => {},
    incrementPerfCounter: () => {},
    isPerfProfilingEnabled: () => false,
}));
vi.mock('../static/map.js', () => ({ map: {} }));

import { readFileSync } from 'node:fs';
import { bandActivity, updateBandLabels } from '../static/renderers.js';

const ctx = { minSnrMode: 'all', ssbMinDb: 0, cwMinDb: -15 };

describe('bandActivity (band rail counts + sparkline bins)', () => {
    it('bins live stream spots by receive-stamped age (they carry no `t`)', () => {
        const recvMs = 1_800_000_000_000;
        const live = (band, ageSeconds) => ({ band, snr: 0, ageSeconds, __recvAge: ageSeconds, __recvMs: recvMs });
        const spots = [live('20m', 0), live('20m', 10), live('20m', 890), live('40m', 450)];

        const act = bandActivity(spots, ctx);

        expect(act.get('20m').count).toBe(3);
        // 15-minute window, 15 bins: newest two land in the last bin, the
        // ~15-min-old one in the first; 40m's mid-window spot in the middle.
        expect(act.get('20m').bins[14]).toBe(2);
        expect(act.get('20m').bins[0]).toBe(1);
        expect(act.get('40m').bins[7]).toBe(1);
    });

    it('uses the absolute `t` of timeline spots', () => {
        const spots = [{ band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 - 60 * 7.5 }];
        const act = bandActivity(spots, ctx);
        expect(act.get('20m').bins[14]).toBe(1);
        expect(act.get('20m').bins[7]).toBe(1);
    });

    it('smooths the drawn line with a 3-bin moving average', () => {
        const spots = [{ band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 0, t: 1000 - 150 }];
        const { bins, line } = bandActivity(spots, ctx).get('20m');
        expect(bins.slice(12)).toEqual([1, 0, 2]);
        expect(line[13]).toBe(1);
        expect(line[14]).toBe(1);
    });

    it('applies the SNR floor', () => {
        const spots = [{ band: '20m', snr: -20, t: 1000 }, { band: '20m', snr: 5, t: 1000 }];
        expect(bandActivity(spots, { ...ctx, minSnrMode: 'ssb' }).get('20m').count).toBe(1);
    });
});

function row(band, checked) {
    return `<div class="band-wrapper band-pill band-row" data-band="${band}">
        <input type="checkbox" class="form-check-input band-enable" value="${band}" ${checked ? 'checked' : ''} aria-label="Enable ${band}">
        <button type="button" class="band-solo" aria-pressed="false" aria-label="Show only ${band}" aria-describedby="band-desc-${band}">
            <span class="band-pill-name">${band}</span><span class="band-count"></span>
            <svg class="band-spark"><polyline points="" /></svg>
        </button>
        <span class="visually-hidden" id="band-desc-${band}"></span>
    </div>`;
}

describe('band rail accessibility', () => {
    it('index.html: two sibling controls per row, no role=button wrapper, unique names', () => {
        document.body.innerHTML = readFileSync('static/index.html', 'utf8'); // vitest runs from the repo root
        const rows = Array.from(document.querySelectorAll('.band-rail .band-row'));
        expect(rows.length).toBe(13);
        for (const r of rows) {
            expect(r.hasAttribute('role')).toBe(false);
            expect(r.hasAttribute('tabindex')).toBe(false);
            const cb = r.querySelector(':scope > input.band-enable');
            const btn = r.querySelector(':scope > button.band-solo');
            expect(cb).not.toBeNull();
            expect(btn).not.toBeNull();
            expect(btn.querySelector('input, button, [tabindex]')).toBeNull();
        }
        const names = rows.map((r) => r.querySelector('.band-enable').getAttribute('aria-label'));
        expect(new Set(names).size).toBe(13);
        for (const r of rows) {
            const btn = r.querySelector('.band-solo');
            // Name contains the visible chip text (WCAG 2.5.3); title == name (no duplicate description).
            expect(btn.getAttribute('aria-label')).toBe(`Show only ${r.dataset.band}`);
            expect(btn.getAttribute('title')).toBe(btn.getAttribute('aria-label'));
            expect(document.getElementById(btn.getAttribute('aria-describedby'))).not.toBeNull();
        }
    });

    it('updateBandLabels drives the solo button: pressed, stable name, count as description, disabled when off', () => {
        document.body.innerHTML = `<div id="band-container" data-focus-band="">${row('20m', true)}${row('17m', true)}${row('40m', false)}</div>`;
        const spots = [{ band: '20m', snr: 0, t: 1000 }, { band: '20m', snr: 3, t: 990 }, { band: '17m', snr: 1, t: 995 }];
        updateBandLabels(spots);
        const b20 = document.querySelector('[data-band="20m"] .band-solo');
        const b40 = document.querySelector('[data-band="40m"] .band-solo');
        expect(b20.getAttribute('aria-pressed')).toBe('false');
        expect(b20.disabled).toBe(false);
        expect(b20.getAttribute('aria-label')).toBe('Show only 20m'); // never rewritten with the count
        expect(document.getElementById('band-desc-20m').textContent).toBe('2 spots');
        expect(document.getElementById('band-desc-17m').textContent).toBe('1 spot');
        expect(b40.disabled).toBe(true);
        expect(document.getElementById('band-desc-40m').textContent).toBe('band disabled');

        document.getElementById('band-container').dataset.focusBand = '20m';
        updateBandLabels(spots);
        expect(b20.getAttribute('aria-pressed')).toBe('true');
    });
});
