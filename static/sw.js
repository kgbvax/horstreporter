// static/sw.js — Service Worker for the Web Push notification channel (U5).
//
// Scope is the app root ("/") so the browser can deliver push events even
// when the horstreporter tab is closed. The worker intentionally handles
// ONLY `push` and `notificationclick` events: it does not intercept
// fetch/navigation, so existing requests flow straight through to the
// network. This keeps the worker cache-free (no stale-app-code risk) and
// avoids interfering with the live SSE stream.
//
// Cache-busting: the registration URL carries a `?v=` query so a new
// worker version is fetched on update. Existing push subscriptions
// survive SW updates (the push subscription is bound to the registration,
// not the worker version).
//
// Push payload shape (sent by push.go::pushSurgePayload):
//   { "band": "10m", "region": "CAR", "label": "tune to 10m, surge to Caribbean", "z_score": 7.0 }
//
// The label is the human-readable surge string shown as the notification
// title; band + region go in the body for context.

self.addEventListener('install', (event) => {
    // Skip waiting so a new worker activates immediately on update.
    event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (event) => {
    // Take over all clients immediately (no stale-worker window).
    event.waitUntil(self.clients.claim());
});

self.addEventListener('push', (event) => {
    let payload = {};
    try {
        if (event.data) {
            // The backend sends JSON. Some push services send a plain
            // string when the payload is small; handle both.
            const text = event.data.text();
            try {
                payload = JSON.parse(text);
            } catch (_) {
                payload = { label: text, band: '', region: '' };
            }
        }
    } catch (_) {
        payload = {};
    }

    const title = payload.label || 'horstreporter surge';
    const bodyParts = [];
    if (payload.band) bodyParts.push(payload.band);
    if (payload.region) bodyParts.push(payload.region);
    if (payload.z_score && typeof payload.z_score === 'number') {
        bodyParts.push(`z=${payload.z_score.toFixed(1)}`);
    }
    const body = bodyParts.join(' · ') || 'propagation surge detected';

    const options = {
        body,
        // The icon helps the notification read as horstreporter on the
        // OS-level notification center. Falls back to the default favicon
        // data URL used by index.html.
        icon: '/dl9et_2_140.jpg',
        badge: '/dl9et_2_140.jpg',
        tag: payload.band ? `horstreporter-surge-${payload.band}-${payload.region || ''}` : 'horstreporter-surge',
        // Renotify: a new surge on the same (band, region) replaces the
        // previous notification rather than stacking, so the operator
        // sees the latest state.
        renotify: true,
        data: {
            band: payload.band || '',
            region: payload.region || '',
            url: '/'
        }
    };

    event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    const targetUrl = (event.notification.data && event.notification.data.url) || '/';

    // Focus an existing horstreporter tab if one is open; otherwise open
    // a new one. Mirrors the plan's "focus the horstreporter tab" click
    // handler.
    event.waitUntil(
        self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
            for (const client of clientList) {
                if (client.url.includes(targetUrl) && 'focus' in client) {
                    return client.focus();
                }
            }
            if (self.clients.openWindow) {
                return self.clients.openWindow(targetUrl);
            }
            return null;
        })
    );
});