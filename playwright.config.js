import { defineConfig, devices } from '@playwright/test';

// Headless browser tests against the real built UI. Builds the Svelte bundle,
// then serves static/ so dist/horst-ui.js loads exactly as embedded in prod.
export default defineConfig({
    testDir: './e2e',
    timeout: 30_000,
    use: {
        baseURL: 'http://localhost:4173',
        ...devices['Desktop Chrome'],
    },
    webServer: {
        command: 'npm run build && python3 -m http.server 4173 -d static',
        url: 'http://localhost:4173/index.html',
        reuseExistingServer: !process.env.CI,
        timeout: 60_000,
    },
});
