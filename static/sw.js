// static/sw.js — retired Service Worker.
//
// It used to receive Web Push surge alerts. Web Push was removed in favour of
// Horst-Kevin's desktop nag (static/horst-kevin.js). Browsers that registered
// the old worker re-fetch this file on their next visit; this version drops
// their push subscription and unregisters itself, so no stale worker or
// subscription is left behind. Nothing registers it any more.

self.addEventListener('install', () => {
    self.skipWaiting();
});

self.addEventListener('activate', (event) => {
    event.waitUntil((async () => {
        try {
            const sub = await self.registration.pushManager.getSubscription();
            if (sub) await sub.unsubscribe();
        } catch (_) { /* best effort */ }
        await self.registration.unregister();
    })());
});
