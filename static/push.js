// static/push.js — Web Push subscription UI for the surge notification
// channel (U5). Wires up the "Enable push notifications" checkbox and
// per-(band × region) enable matrix in the options panel, registers
// the Service Worker (static/sw.js), subscribes via the Push API, and
// posts the subscription to /api/push/subscribe.
//
// Re-subscription after restart: on panel open, the UI checks whether
// the browser's existing subscription is known to the server (GET
// /api/push/subscription-status?endpoint=...). When the server reports
// no record (e.g. after a server restart that lost the in-memory
// store), the UI re-POSTs the subscription to /api/push/subscribe
// (idempotent). A "push disabled — re-enable" indicator surfaces when
// the server reports zero subscriptions for the current client.
//
// iOS Safari: Web Push requires Home Screen PWA install — a plain
// Safari tab does NOT support the Push API even when
// feature-detection reports availability. The UI prompts the operator
// to "Add to Home Screen" before enabling push on iOS.
//
// Feature-detection: hides the push UI entirely when `serviceWorker` or
// `PushManager` are unsupported, so the matrix panel still works on
// browsers without Web Push.

const SW_PATH = '/sw.js?v=1';
import { WSPR_REGIONS } from './utils.js';
const PUSH_BANDS = ['160m', '80m', '60m', '40m', '30m', '20m', '17m', '15m', '12m', '10m', '6m', '4m', '2m'];
const PUSH_REGIONS = WSPR_REGIONS;

function isPushSupported() {
    return ('serviceWorker' in navigator) && ('PushManager' in window);
}

function isIOSSafari() {
    // iOS Safari 16.4+ supports Web Push only when installed as a Home
    // Screen PWA. Detect iOS Safari via the UA + standalone check. When
    // NOT standalone (plain Safari tab), prompt the operator to Add to
    // Home Screen.
    const ua = navigator.userAgent || '';
    const isIOS = /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
    const isSafari = /Safari/.test(ua) && !/CriOS|FxiOS/.test(ua);
    // window.matchMedia is not available in all environments (e.g. older
    // Safari without PWA support, or restricted iframes). Fall back to
    // the navigator.standalone check alone in that case.
    let isStandalone = window.navigator.standalone === true;
    if (!isStandalone && typeof window.matchMedia === 'function') {
        try {
            isStandalone = window.matchMedia('(display-mode: standalone)').matches;
        } catch (_) { /* feature-detect: ignore */ }
    }
    return isIOS && isSafari && !isStandalone;
}

// Load and cache the server's VAPID public key. Returns an
// ApplicationServerKey ready for PushManager.subscribe, or null when
// the server has push disabled.
async function fetchVAPIDPublicKey() {
    const resp = await fetch('/api/push/vapid-public-key');
    if (!resp.ok) return null;
    const data = await resp.json();
    if (!data || !data.public_key) return null;
    return data.public_key;
}

// Convert a base64url public key string to the Uint8Array the Push API
// expects for applicationServerKey.
function urlBase64ToUint8Array(base64String) {
    const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
    const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
    const raw = atob(base64);
    const out = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
}

// Read the current per-(band × region) preferences from the UI.
// Returns a map of "band:region" → bool, plus "all" when the all-surge
// checkbox is checked. Empty preferences (all + matrix all unchecked)
// yields an inert subscription (stored but no pushes fire — the "no
// push when disabled" test scenario).
function readPreferences() {
    const prefs = {};
    const allCheckbox = document.getElementById('push-pref-all');
    if (allCheckbox && allCheckbox.checked) {
        prefs['all'] = true;
        return prefs;
    }
    for (const band of PUSH_BANDS) {
        for (const region of PUSH_REGIONS) {
            const el = document.getElementById(`push-pref-${band}-${region}`);
            if (el && el.checked) prefs[`${band}:${region}`] = true;
        }
    }
    return prefs;
}

// Apply a preferences map back to the UI checkboxes. Used when
// re-hydrating the matrix from a re-subscription.
function applyPreferences(prefs) {
    prefs = prefs || {};
    const allCheckbox = document.getElementById('push-pref-all');
    if (allCheckbox) allCheckbox.checked = !!prefs['all'];
    for (const band of PUSH_BANDS) {
        for (const region of PUSH_REGIONS) {
            const el = document.getElementById(`push-pref-${band}-${region}`);
            if (el) el.checked = !!prefs[`${band}:${region}`];
        }
    }
}

function getQth() {
    const el = document.getElementById('qth');
    return (el && el.value || '').trim().toUpperCase();
}

async function registerServiceWorker() {
    const reg = await navigator.serviceWorker.register(SW_PATH, { scope: '/' });
    // Wait until the worker is active so subscribe() works reliably.
    if (reg.active) return reg;
    return new Promise((resolve, reject) => {
        const sw = reg.installing || reg.waiting;
        if (!sw) return resolve(reg);
        const onChange = () => {
            if (sw.state === 'activated') return resolve(reg);
            if (sw.state === 'redundant') return reject(new Error('service worker redundant'));
        };
        sw.addEventListener('statechange', onChange);
        // Safety timeout so a stuck worker doesn't hang the UI forever.
        setTimeout(() => resolve(reg), 5000);
    });
}

async function getOrCreateSubscription(reg, appServerKey) {
    let sub = await reg.pushManager.getSubscription();
    if (sub) return sub;
    return reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: appServerKey
    });
}

// Post the subscription + preferences to the backend. Idempotent: a
// re-POST of an existing endpoint updates preferences in place.
async function postSubscription(sub, preferences) {
    const body = {
        endpoint: sub.endpoint,
        keys: {
            auth: sub.keys ? sub.keys.auth : '',
            p256dh: sub.keys ? sub.keys.p256dh : ''
        },
        qth: getQth(),
        preferences
    };
    const resp = await fetch('/api/push/subscribe', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
    });
    if (!resp.ok) {
        const text = await resp.text().catch(() => '');
        throw new Error(`subscribe failed (${resp.status}): ${text}`);
    }
    return resp.json();
}

async function unsubscribeOnServer(endpoint) {
    if (!endpoint) return;
    try {
        await fetch('/api/push/unsubscribe', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ endpoint })
        });
    } catch (_) { /* best effort */ }
}

// Check whether the browser's existing subscription is known to the
// server. Returns true when the server has a record, false when the
// subscription should be re-POSTed (e.g. after a server restart that
// lost the in-memory store).
async function serverHasSubscription(endpoint) {
    if (!endpoint) return false;
    try {
        const resp = await fetch(`/api/push/subscription-status?endpoint=${encodeURIComponent(endpoint)}`);
        if (!resp.ok) return false;
        const data = await resp.json();
        return !!(data && data.registered);
    } catch (_) {
        return false;
    }
}

function setStatus(message, kind) {
    const el = document.getElementById('push-status');
    if (!el) return;
    el.textContent = message || '';
    el.dataset.kind = kind || 'idle';
}

function setReEnableIndicator(show) {
    const el = document.getElementById('push-reenable-hint');
    if (!el) return;
    el.style.display = show ? 'block' : 'none';
}

// buildPrefGridRows injects the per-band checkbox rows into the
// preference matrix. The region headers are already in index.html;
// this fills in the band-row cells so the HTML stays compact while the
// checkbox IDs stay stable for tests (push-pref-<band>-<region>).
function buildPrefGridRows() {
    const grid = document.querySelector('.push-pref-grid');
    if (!grid) return;
    // If the rows are already built (e.g. a second init call), no-op.
    if (grid.querySelector('.push-pref-band-label')) return;
    for (const band of PUSH_BANDS) {
        const label = document.createElement('div');
        label.className = 'push-pref-cell push-pref-band-label';
        label.textContent = band;
        grid.appendChild(label);
        for (const region of PUSH_REGIONS) {
            const cell = document.createElement('div');
            cell.className = 'push-pref-cell';
            const cb = document.createElement('input');
            cb.type = 'checkbox';
            cb.id = `push-pref-${band}-${region}`;
            cb.title = `${band} · ${region}`;
            cb.dataset.band = band;
            cb.dataset.region = region;
            cell.appendChild(cb);
            grid.appendChild(cell);
        }
    }
}

// cachedAppServerKey holds the VAPID public key fetched from the
// backend, exposed for tests.
let cachedAppServerKey = null;

// initPushUI wires up the push settings UI in the options panel.
// Returns null when push is unsupported (UI stays hidden); returns the
// push controller otherwise. Mirrors the hot-band-indicator.js
// factory pattern so app.js can store and stop it.
export async function initPushUI() {
    const root = document.getElementById('push-settings-root');
    if (!root || !isPushSupported()) {
        if (root) root.style.display = 'none';
        return null;
    }

    // Build the per-(band × region) checkbox grid rows in the DOM so
    // the IDs are stable for tests and the HTML stays compact. The
    // region headers are already in index.html; we only inject the
    // band rows.
    buildPrefGridRows();

    const enableCheckbox = document.getElementById('push-enable');
    const matrixRoot = document.getElementById('push-pref-matrix');
    const allCheckbox = document.getElementById('push-pref-all');

    // iOS Safari plain-tab prompt: surface an "Add to Home Screen" hint
    // and disable the enable checkbox until the operator installs the
    // PWA.
    const iosHint = document.getElementById('push-ios-hint');
    if (isIOSSafari() && iosHint) {
        iosHint.style.display = 'block';
        if (enableCheckbox) enableCheckbox.disabled = true;
    } else if (iosHint) {
        iosHint.style.display = 'none';
    }

    let currentSubscription = null;

    async function enable() {
        setStatus('enabling…', 'busy');
        try {
            const publicKey = await fetchVAPIDPublicKey();
            if (!publicKey) {
                setStatus('push not configured on the server', 'error');
                enableCheckbox.checked = false;
                return;
            }
            cachedAppServerKey = publicKey;
            const appServerKey = urlBase64ToUint8Array(publicKey);
            const reg = await registerServiceWorker();
            const sub = await getOrCreateSubscription(reg, appServerKey);
            currentSubscription = sub;
            const prefs = readPreferences();
            await postSubscription(sub, prefs);
            if (matrixRoot) matrixRoot.style.display = 'block';
            setStatus('push enabled', 'ok');
            setReEnableIndicator(false);
        } catch (err) {
            setStatus(`push enable failed: ${err.message || err}`, 'error');
            enableCheckbox.checked = false;
            if (matrixRoot) matrixRoot.style.display = 'none';
        }
    }

    async function disable() {
        setStatus('disabling…', 'busy');
        try {
            if (currentSubscription) {
                await unsubscribeOnServer(currentSubscription.endpoint);
                try { await currentSubscription.unsubscribe(); } catch (_) { /* best effort */ }
                currentSubscription = null;
            } else {
                // The browser may have a subscription from a previous
                // session that we never wired up; clear it too.
                const reg = await navigator.serviceWorker.getRegistration();
                if (reg) {
                    const existing = await reg.pushManager.getSubscription();
                    if (existing) {
                        await unsubscribeOnServer(existing.endpoint);
                        await existing.unsubscribe();
                    }
                }
            }
        } finally {
            if (matrixRoot) matrixRoot.style.display = 'none';
            setStatus('push disabled', 'idle');
        }
    }

    async function onToggle() {
        if (enableCheckbox.checked) await enable();
        else await disable();
    }

    if (enableCheckbox) enableCheckbox.addEventListener('change', onToggle);

    if (allCheckbox) {
        allCheckbox.addEventListener('change', () => {
            // Disable the matrix checkboxes when "all" is on.
            const checked = allCheckbox.checked;
            for (const band of PUSH_BANDS) {
                for (const region of PUSH_REGIONS) {
                    const el = document.getElementById(`push-pref-${band}-${region}`);
                    if (el) el.disabled = checked;
                }
            }
            resubscribeIfActive();
        });
    }

    // Per-cell matrix: any change re-POSTs the preferences.
    for (const band of PUSH_BANDS) {
        for (const region of PUSH_REGIONS) {
            const el = document.getElementById(`push-pref-${band}-${region}`);
            if (el) el.addEventListener('change', resubscribeIfActive);
        }
    }

    async function resubscribeIfActive() {
        if (!currentSubscription) return;
        try {
            await postSubscription(currentSubscription, readPreferences());
            setStatus('preferences updated', 'ok');
        } catch (err) {
            setStatus(`preferences update failed: ${err.message || err}`, 'error');
        }
    }

    // Re-subscription after restart: on panel open, check whether the
    // browser's existing subscription is known server-side. When the
    // server has no record (lost the in-memory store on restart),
    // re-POST the subscription. The "push disabled — re-enable"
    // indicator surfaces when the server reports zero records.
    async function reconcileAfterRestart() {
        if (!isPushSupported()) return;
        try {
            const reg = await navigator.serviceWorker.getRegistration();
            if (!reg) return;
            const existing = await reg.pushManager.getSubscription();
            if (!existing) return;
            const has = await serverHasSubscription(existing.endpoint);
            if (!has) {
                setReEnableIndicator(true);
                // Re-POST with the existing subscription + any saved
                // preferences. Idempotent: server treats this as a
                // no-op update.
                currentSubscription = existing;
                const prefs = readPreferences();
                // If no UI preferences are set, default to "all" so the
                // operator gets SOMETHING after a restart rather than a
                // silent inert subscription.
                if (Object.keys(prefs).length === 0) prefs['all'] = true;
                await postSubscription(existing, prefs);
                if (enableCheckbox) enableCheckbox.checked = true;
                if (matrixRoot) matrixRoot.style.display = 'block';
                setStatus('push re-enabled after restart', 'ok');
                setReEnableIndicator(false);
            }
        } catch (_) { /* best effort */ }
    }

    // Run the reconciliation on init (panel open) and when the tab
    // becomes visible again (covers the case where the operator had
    // the tab closed and re-opened it).
    setTimeout(reconcileAfterRestart, 500);
    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') reconcileAfterRestart();
    });

    return {
        enable,
        disable,
        reconcileAfterRestart,
        resubscribeIfActive,
        stop() { /* nothing to clean up; SW persists */ },
        // Test helpers.
        _readPreferences: readPreferences,
        _applyPreferences: applyPreferences,
        _getCachedAppServerKey: () => cachedAppServerKey
    };
}