import { test, expect } from '@playwright/test';

// U2 slice parity: the SNR threshold sliders are now Svelte components bound
// to the store, replacing the index.html inputs + app.js listeners. The 5
// render-path readers still read #ssb-min-db / #cw-min-db, so ids and values
// must match the old behavior exactly.

test.beforeEach(async ({ page }) => {
    await page.goto('/index.html');
});

test('SNR sliders mount with original ids and default values', async ({ page }) => {
    const ssb = page.locator('#ssb-min-db');
    const cw = page.locator('#cw-min-db');
    await expect(ssb).toHaveCount(1);
    await expect(cw).toHaveCount(1);
    await expect(ssb).toHaveValue('0');
    await expect(cw).toHaveValue('-15');
    await expect(page.locator('#ssb-min-db-val')).toHaveText('0');
    await expect(page.locator('#cw-min-db-val')).toHaveText('-15');
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
    await page.$eval('#cw-min-db', (el) => { el.value = '-3'; el.dispatchEvent(new Event('input', { bubbles: true })); });
    await page.reload();
    await expect(page.locator('#cw-min-db')).toHaveValue('-3');
    await expect(page.locator('#cw-min-db-val')).toHaveText('-3');
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
