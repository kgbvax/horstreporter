import { test, expect } from '@playwright/test';

// U2 slice parity: the SNR threshold sliders are now Svelte components bound
// to the store, replacing the index.html inputs + app.js listeners. The 5
// render-path readers still read #ssb-min-db / #cw-min-db, so ids and values
// must match the old behavior exactly.

test.beforeEach(async ({ page }) => {
    await page.goto('/index.html');
});

test('SNR sliders mount with original ids and default values', async ({ page }) => {
    // Default mode is SSB, so only the SSB slider is mounted; the CW slider
    // is unmounted (its value is kept in the store and reappears in CW mode).
    const ssb = page.locator('#ssb-min-db');
    const cw = page.locator('#cw-min-db');
    await expect(ssb).toHaveCount(1);
    await expect(cw).toHaveCount(0);
    await expect(ssb).toHaveValue('0');
    await expect(page.locator('#ssb-min-db-val')).toHaveText('0');
    await expect(page.locator('#cw-min-db-val')).toHaveCount(0);
});

test('moving SSB slider updates label, persists, and triggers render', async ({ page }) => {
    await page.evaluate(() => { window.__renderCalls = 0; window.__horstScheduleRender = () => { window.__renderCalls++; }; });
    await page.$eval('#ssb-min-db', (el) => { el.value = '-7'; el.dispatchEvent(new Event('input', { bubbles: true })); });
    await expect(page.locator('#ssb-min-db-val')).toHaveText('-7');
    const persisted = await page.evaluate(() => JSON.parse(localStorage.getItem('horst-ui-state')).ssbMinDb);
    expect(persisted).toBe(-7);
    expect(await page.evaluate(() => window.__renderCalls)).toBeGreaterThan(0);
});

test('persisted SNR values rehydrate on reload', async ({ page }) => {
    // The CW slider only exists in CW mode; switch there first so the slider
    // is mounted before we drive it.
    await page.locator('#snr-cw').dispatchEvent('click');
    await expect(page.locator('#cw-min-db')).toHaveCount(1);
    await page.$eval('#cw-min-db', (el) => { el.value = '-3'; el.dispatchEvent(new Event('input', { bubbles: true })); });
    await page.reload();
    await expect(page.locator('#snr-cw')).toBeChecked();
    await expect(page.locator('#cw-min-db')).toHaveValue('-3');
    await expect(page.locator('#cw-min-db-val')).toHaveText('-3');
});

test('only the active min-SNR mode slider is mounted', async ({ page }) => {
    // SSB mode: SSB slider only.
    await expect(page.locator('#snr-ssb')).toBeChecked();
    await expect(page.locator('#ssb-min-db')).toHaveCount(1);
    await expect(page.locator('#cw-min-db')).toHaveCount(0);
    await expect(page.locator('text=No SNR filter')).toHaveCount(0);

    // CW mode: CW slider only.
    await page.locator('#snr-cw').dispatchEvent('click');
    await expect(page.locator('#cw-min-db')).toHaveCount(1);
    await expect(page.locator('#ssb-min-db')).toHaveCount(0);
    await expect(page.locator('text=No SNR filter')).toHaveCount(0);

    // All (none) mode: no sliders, "No SNR filter" hint instead.
    await page.locator('#snr-none').dispatchEvent('click');
    await expect(page.locator('#ssb-min-db')).toHaveCount(0);
    await expect(page.locator('#cw-min-db')).toHaveCount(0);
    await expect(page.locator('text=No SNR filter')).toHaveCount(1);

    // Switching back to SSB re-mounts the SSB slider with its persisted value.
    await page.locator('#snr-ssb').dispatchEvent('click');
    await expect(page.locator('#ssb-min-db')).toHaveCount(1);
    await expect(page.locator('#ssb-min-db')).toHaveValue('0');
});

test('minutes and cluster-distance sliders mount with default values and ids', async ({ page }) => {
    await expect(page.locator('#minutes')).toHaveValue('15');
    await expect(page.locator('#minutes-val')).toHaveText('15');
    await expect(page.locator('#cluster-distance')).toHaveValue('500');
    await expect(page.locator('#cluster-dist-val')).toHaveText('500');
});

test('minutes slider persists and rehydrates', async ({ page }) => {
    await page.$eval('#minutes', (el) => { el.value = '42'; el.dispatchEvent(new Event('input', { bubbles: true })); });
    await expect(page.locator('#minutes-val')).toHaveText('42');
    await page.reload();
    await expect(page.locator('#minutes')).toHaveValue('42');
});

test('auto-zoom toggle persists and rehydrates', async ({ page }) => {
    const cb = page.locator('#auto-zoom');
    await expect(cb).not.toBeChecked();
    await cb.dispatchEvent('click');
    await expect(cb).toBeChecked();
    await page.reload();
    await expect(page.locator('#auto-zoom')).toBeChecked();
});

test('min-snr radios default to ssb, select+persist, fire render', async ({ page }) => {
    await expect(page.locator('#snr-ssb')).toBeChecked();
    await page.evaluate(() => { window.__renderCalls = 0; window.__horstScheduleRender = () => { window.__renderCalls++; }; });
    await page.locator('#snr-cw').dispatchEvent('click');
    await expect(page.locator('#snr-cw')).toBeChecked();
    expect(await page.evaluate(() => window.__renderCalls)).toBeGreaterThan(0);
    await page.reload();
    await expect(page.locator('#snr-cw')).toBeChecked();
});

test('style radios default to grid, switch+persist', async ({ page }) => {
    await expect(page.locator('#style-grid')).toBeChecked();
    await page.locator('#style-area').dispatchEvent('click');
    await expect(page.locator('#style-area')).toBeChecked();
    await page.reload();
    await expect(page.locator('#style-area')).toBeChecked();
});

test('projection radios default to mercator and invoke projection hook', async ({ page }) => {
    await expect(page.locator('#proj-mercator')).toBeChecked();
    await page.evaluate(() => { window.__projCalls = []; window.__horstApplyProjection = (p) => window.__projCalls.push(p); });
    await page.locator('#proj-azimuthal').dispatchEvent('click');
    await expect(page.locator('#proj-azimuthal')).toBeChecked();
    expect(await page.evaluate(() => window.__projCalls)).toContain('azimuthal');
    await page.reload();
    await expect(page.locator('#proj-azimuthal')).toBeChecked();
});

test('surroundings toggle persists and fires resubmit hook', async ({ page }) => {
    await page.evaluate(() => { window.__sCalls = 0; window.__horstSurroundingsChanged = () => { window.__sCalls++; }; });
    const cb = page.locator('#surroundings');
    await expect(cb).not.toBeChecked();
    await cb.dispatchEvent('click');
    await expect(cb).toBeChecked();
    expect(await page.evaluate(() => window.__sCalls)).toBeGreaterThan(0);
    await page.reload();
    await expect(page.locator('#surroundings')).toBeChecked();
});

test('country-coloring defaults on, toggles off, fires hook', async ({ page }) => {
    await page.evaluate(() => { window.__cCalls = 0; window.__horstCountryColoringChanged = () => { window.__cCalls++; }; });
    const cb = page.locator('#show-country-coloring');
    await expect(cb).toBeChecked();
    await cb.dispatchEvent('click');
    await expect(cb).not.toBeChecked();
    expect(await page.evaluate(() => window.__cCalls)).toBeGreaterThan(0);
    await page.reload();
    await expect(page.locator('#show-country-coloring')).not.toBeChecked();
});

test('qth input persists, rehydrates, and external setter updates it', async ({ page }) => {
    await page.fill('#qth', 'JO32');
    await expect(page.locator('#qth')).toHaveValue('JO32');
    await page.reload();
    await expect(page.locator('#qth')).toHaveValue('JO32');
    await page.evaluate(() => window.__horstSetQTH('FN31'));
    await expect(page.locator('#qth')).toHaveValue('FN31');
});

test('legacy target key migrates to qth on load', async ({ page }) => {
    // Simulate an existing user who has the old 'target' key from before
    // the QTH rename. The app should migrate it to 'qth' and populate the
    // input field without requiring a click on Go.
    await page.evaluate(() => {
        localStorage.removeItem('qth');
        localStorage.setItem('target', 'JO42');
    });
    await page.reload();
    await expect(page.locator('#qth')).toHaveValue('JO42');
});

test('dx-cluster defaults on; external setter syncs it', async ({ page }) => {
    await expect(page.locator('#show-dxcluster-spots')).toBeChecked();
    await page.evaluate(() => window.__horstSetDxcluster(false));
    await expect(page.locator('#show-dxcluster-spots')).not.toBeChecked();
    await page.reload();
    await expect(page.locator('#show-dxcluster-spots')).not.toBeChecked();
});
