import { test, expect } from '@playwright/test';

// Synthetic spots around the JO32 target. The exact geography is not important;
// we only need enough reports so the active-area density field produces regions.
const spots = [
    { lat: 55.0, lng: 25.0, snr: 12, band: '40m', sourceType: 'ft8', ageSeconds: 60 },
    { lat: 50.0, lng: 14.4, snr: 10, band: '20m', sourceType: 'ft8', ageSeconds: 30 },
    { lat: 49.6, lng: 13.2, snr: 11, band: '20m', sourceType: 'ft8', ageSeconds: 45 },
    { lat: 50.6, lng: 15.2, snr: 9, band: '20m', sourceType: 'ft8', ageSeconds: 55 },
    { lat: 45.76, lng: 4.84, snr: 14, band: '80m', sourceType: 'ft8', ageSeconds: 10 },
    { lat: 45.80, lng: 4.88, snr: 16, band: '80m', sourceType: 'ft8', ageSeconds: 12 },
    { lat: 45.72, lng: 4.80, snr: 13, band: '80m', sourceType: 'ft8', ageSeconds: 15 },
];

test.describe('capture mode URL params', () => {
    test('capture URL projection and style are not clobbered by Svelte store defaults', async ({ page }) => {
        await page.route('**/api/capture_snapshot**', (route) =>
            route.fulfill({
                status: 200,
                contentType: 'application/json',
                body: JSON.stringify({ spots, snapshot_at: 0, generated_at: 'e2e' }),
            })
        );

        await page.goto(
            '/index.html?capture=1&projection=azimuthal&style=active-area&target=JO32&min_snr_mode=none&minutes=15',
            { waitUntil: 'networkidle' }
        );

        await page.waitForFunction(() => window.__horstCaptureReady?.ready === true, { timeout: 15000 });

        const projection = await page.evaluate(
            () => document.querySelector('input[name="projection-select"]:checked')?.value
        );
        const style = await page.evaluate(
            () => document.querySelector('input[name="style-select"]:checked')?.value
        );
        const azimuthEnabled = await page.evaluate(
            () => document.querySelector('#azimuth-canvas') &&
                window.getComputedStyle(document.querySelector('#azimuth-canvas')).display !== 'none'
        );

        expect(projection).toBe('azimuthal');
        expect(style).toBe('active-area');
        expect(azimuthEnabled).toBe(true);
    });
});
