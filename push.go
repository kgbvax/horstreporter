package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// push.go implements the Web Push notification channel (U5 of the Propagation
// Intelligence Layer plan). It stores browser Push API subscriptions in an
// in-memory map keyed by the subscription endpoint URL, alongside the
// operator's QTH and per-(band × region) enable preferences. When
// detectSurges (U2) flags a cell, the engine calls
// pushStore.NotifySurges(cells, qth), which iterates subscriptions matching
// the cell's (band, region) and sends a Web Push message via the
// github.com/SherClockHolmes/webpush-go library.
//
// The store mirrors the hub.go in-memory map + sync.RWMutex pattern. A
// max-subscription cap (pushMaxSubscriptions) with FIFO eviction prevents
// unbounded memory growth from unauthenticated POSTs (the app is
// unauthenticated by design — see KTD7 / the plan's security section). A
// per-client-IP rate limiter (pushSubscribeRatePerHour) mitigates abuse on
// /api/push/subscribe.
//
// VAPID keys are provided via flags (-push-vapid-private-key /
// -push-vapid-public-key) or env vars (PUSH_VAPID_PRIVATE_KEY /
// PUSH_VAPID_PUBLIC_KEY). The private key is a server secret — never logged
// or exposed via any endpoint. The public key is served at
// /api/push/vapid-public-key for the frontend subscription flow.

const (
	// pushMaxSubscriptions caps the in-memory subscription store. Adding
	// beyond this evicts the oldest (FIFO) — mirrors the plan's abuse
	// mitigation. 1000 is the v1 cap; a PG-backed store is a v2 enhancement.
	pushMaxSubscriptions = 1000
	// pushSubscribeRatePerHour limits /api/push/subscribe POSTs per client
	// IP per hour. The endpoint is unauthenticated; this is the plan's
	// abuse mitigation. 10/hour is generous for a single operator who
	// re-enables push a few times after restarts.
	pushSubscribeRatePerHour = 10
	// pushSendTimeout caps a single Web Push HTTP POST. The plan requires
	// that a failed push (expired endpoint, network error) must not block
	// the /api/prop_intel response; the surge path runs push sends
	// asynchronously with this per-send timeout.
	pushSendTimeout = 10 * time.Second
	// pushVAPIDSubscriber is the mailto: used in the VAPID JWT. Operators
	// do not see this; it identifies the sending server to the push
	// service. Override via -push-vapid-subscriber if needed.
	pushVAPIDSubscriber = "mailto:horstreporter@example.com"
)

// pushSubscription is one stored browser Push API subscription. The store
// is keyed by Endpoint (the unique push service URL the browser hands
// back). Each subscription carries the operator's QTH (so surge matching
// can filter by the operator's region view) and a preference map of
// "band:region" → bool, or the special key "all" for every surge.
type pushSubscription struct {
	// Endpoint is the browser-assigned push service URL (FCM, Apple,
	// Mozilla). Used as the store key. Must be an HTTPS URL.
	Endpoint string `json:"endpoint"`
	// Keys are the per-subscription encryption keys (p256dh + auth),
	// base64url-encoded by the browser.
	Keys pushSubscriptionKeys `json:"keys"`
	// QTH is the operator's QTH at subscription time (locator or
	// callsign). Sent back to the operator in the re-subscription
	// check so the frontend can reconcile. May be empty.
	QTH string `json:"qth"`
	// Preferences is the per-(band × region) enable map. The special
	// key "all" enables every surge regardless of band/region. Any
	// other key is "band:region" (e.g. "10m:CAR"). An empty map with
	// no "all" entry means no pushes (subscription stored but inert).
	Preferences map[string]bool `json:"preferences"`
	// CreatedAt orders subscriptions for FIFO eviction. Monotonic
	// enough for the cap; not exposed in the API.
	CreatedAt time.Time `json:"-"`
}

// pushSubscriptionKeys mirrors the browser PushSubscription.getKey()
// output. The webpush-go library expects exactly these two fields.
type pushSubscriptionKeys struct {
	Auth   string `json:"auth"`
	P256dh string `json:"p256dh"`
}

// pushSubscriptionStore is the in-memory subscription map. Mirrors the
// hub.go pattern: sync.RWMutex + map. The order slice tracks insertion
// order for FIFO eviction when the cap is reached.
type pushSubscriptionStore struct {
	sync.RWMutex
	// subs is the keyed store. Key = Endpoint.
	subs map[string]*pushSubscription
	// order is the FIFO queue of endpoints (insertion order). The head
	// is the oldest; eviction pops from the head. A re-POST of an
	// existing endpoint does NOT move it to the tail — the original
	// insertion time is preserved (idempotent re-subscription per the
	// plan's restart-recovery requirement).
	order []string
	// vapidPrivateKey / vapidPublicKey are the server's VAPID key
	// pair, set once at startup from flags/env. The private key is
	// never logged or served.
	vapidPrivateKey string
	vapidPublicKey  string
	// vapidSubscriber is the mailto: in the VAPID JWT.
	vapidSubscriber string
	// httpClient sends the push POSTs. Overridable in tests; defaults
	// to a 10s-timeout http.Client in production.
	httpClient webpush.HTTPClient
	// sendFunc performs the actual Web Push send. Defaults to
	// webpush.SendNotificationWithContext; overridable in tests so the
	// suite does not need a real P-256 subscription key (the library
	// encrypts BEFORE calling httpClient, so mocking only the HTTP
	// layer would fail on test keys). The mock returns the HTTP status
	// and an error so tests can exercise the 410-removal path.
	sendFunc pushSendFunc
	// enabled gates the whole subsystem. When false, NotifySurges
	// is a no-op and the handlers return 503. Set from -push-enable.
	enabled bool
}

// pushSendFunc is the injectable Web Push send signature. payload is
// the JSON-encoded pushSurgePayload. Returns the push service HTTP
// status code and an error. A status of 0 means "no HTTP response
// (transport error)" — the caller treats it as a transient failure
// (logged, subscription kept).
type pushSendFunc func(ctx context.Context, sub *pushSubscription, payload []byte) (statusCode int, err error)

// pushStore is the package-level singleton, mirroring hub and propIntel.
// Wired in main.go; nil-safe methods guard on the store being unconfigured
// (no VAPID keys) so the rest of the app runs without push.
var pushStore = &pushSubscriptionStore{
	subs:            make(map[string]*pushSubscription),
	vapidSubscriber: pushVAPIDSubscriber,
}

// pushIPRateLimiter tracks per-client-IP POST counts to
// /api/push/subscribe over a rolling 1-hour window. The window is simple:
// each IP has a timestamp + count; when the timestamp is older than an
// hour, the count resets. This is intentionally inexact (a request at
// 59min then 61min sees a reset) but cheap and sufficient for abuse
// mitigation on an unauthenticated endpoint.
type pushIPRateLimiter struct {
	sync.Mutex
	// counts maps client IP → (windowStart, count).
	counts map[string]*pushIPRateWindow
}

type pushIPRateWindow struct {
	windowStart time.Time
	count       int
}

// pushRateLimiter is the package-level rate limiter for
// /api/push/subscribe.
var pushRateLimiter = &pushIPRateLimiter{
	counts: make(map[string]*pushIPRateWindow),
}

// allow reports whether a POST from the given IP is within the hourly
// rate limit. The first request in a window is always allowed and starts
// the window. Returns true (allowed) when the IP is empty (e.g. a
// misconfigured proxy) — abuse from a blank IP is bounded by the
// max-subscription cap anyway.
func (rl *pushIPRateLimiter) allow(ip string) bool {
	if ip == "" {
		return true
	}
	now := time.Now()
	rl.Lock()
	defer rl.Unlock()
	w, ok := rl.counts[ip]
	if !ok || now.Sub(w.windowStart) >= time.Hour {
		rl.counts[ip] = &pushIPRateWindow{windowStart: now, count: 1}
		return true
	}
	if w.count >= pushSubscribeRatePerHour {
		return false
	}
	w.count++
	return true
}

// configure sets the VAPID keys and enabled flag. Called from main.go
// after flag/env resolution. If either key is empty, push is disabled
// (the handlers return 503, NotifySurges is a no-op).
func (s *pushSubscriptionStore) configure(privateKey, publicKey, subscriber string, enabled bool) {
	s.Lock()
	defer s.Unlock()
	s.vapidPrivateKey = strings.TrimSpace(privateKey)
	s.vapidPublicKey = strings.TrimSpace(publicKey)
	if subscriber = strings.TrimSpace(subscriber); subscriber != "" {
		s.vapidSubscriber = subscriber
	}
	// Push is enabled only if both keys are present AND the operator
	// didn't explicitly disable it. This lets `-push-enable=false`
	// turn the subsystem off even when keys are configured.
	s.enabled = enabled && s.vapidPrivateKey != "" && s.vapidPublicKey != ""
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: pushSendTimeout}
	}
	if s.sendFunc == nil {
		s.sendFunc = defaultPushSendFunc(s)
	}
}

// defaultPushSendFunc returns the production sendFunc that wraps
// webpush.SendNotificationWithContext. Captured in a closure so the
// store's sendFunc stays self-contained. On a 410/404 the subscription
// is removed; other errors are logged (redacted).
func defaultPushSendFunc(s *pushSubscriptionStore) pushSendFunc {
	return func(ctx context.Context, sub *pushSubscription, payload []byte) (int, error) {
		s.RLock()
		privateKey, publicKey, subscriber := s.vapidPrivateKey, s.vapidPublicKey, s.vapidSubscriber
		client := s.httpClient
		s.RUnlock()
		if sub == nil || sub.Endpoint == "" {
			return 0, nil
		}
		wpSub := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				Auth:   sub.Keys.Auth,
				P256dh: sub.Keys.P256dh,
			},
		}
		opts := &webpush.Options{
			HTTPClient:      client,
			Subscriber:      subscriber,
			VAPIDPublicKey:  publicKey,
			VAPIDPrivateKey: privateKey,
			TTL:             24 * 3600,
			Urgency:         "high",
		}
		resp, err := webpush.SendNotificationWithContext(ctx, payload, wpSub, opts)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
}

// isEnabled reports whether push is configured and enabled.
func (s *pushSubscriptionStore) isEnabled() bool {
	s.RLock()
	defer s.RUnlock()
	return s.enabled
}

// publicKey returns the VAPID public key (base64url) or empty when
// unconfigured. Used by the vapid-public-key handler.
func (s *pushSubscriptionStore) publicKey() string {
	s.RLock()
	defer s.RUnlock()
	return s.vapidPublicKey
}

// validatePushSubscription checks the browser-provided subscription
// object for the required fields: an HTTPS endpoint URL and non-empty
// p256dh + auth keys. Returns nil when valid. Mirrors the validation
// called for in the plan's security section.
func validatePushSubscription(sub *pushSubscription) error {
	endpoint := strings.TrimSpace(sub.Endpoint)
	if endpoint == "" {
		return errPushInvalid("missing endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return errPushInvalid("endpoint must be an HTTPS URL")
	}
	if strings.TrimSpace(sub.Keys.P256dh) == "" {
		return errPushInvalid("missing keys.p256dh")
	}
	if strings.TrimSpace(sub.Keys.Auth) == "" {
		return errPushInvalid("missing keys.auth")
	}
	return nil
}

// errPushInvalid is a small typed error so handlers can distinguish
// validation failures (400) from internal errors (500) without a
// sentinel-check helper.
type errPushInvalid string

func (e errPushInvalid) Error() string { return string(e) }

// add stores or updates a subscription. A re-POST of an existing endpoint
// updates the QTH/preferences (the browser may re-subscribe with new
// keys after a permission reset) but preserves the original insertion
// order — idempotent re-subscription per the plan's restart-recovery
// requirement. When the store is at capacity, the oldest subscription
// (order head) is evicted before the new one is added.
func (s *pushSubscriptionStore) add(sub *pushSubscription) {
	s.Lock()
	defer s.Unlock()
	if sub == nil || sub.Endpoint == "" {
		return
	}
	if _, exists := s.subs[sub.Endpoint]; !exists {
		// Cap check: evict the oldest (FIFO) when at capacity.
		if len(s.subs) >= pushMaxSubscriptions {
			for len(s.order) > 0 {
				oldest := s.order[0]
				s.order = s.order[1:]
				if _, ok := s.subs[oldest]; ok {
					delete(s.subs, oldest)
					break
				}
			}
		}
		sub.CreatedAt = time.Now()
		s.order = append(s.order, sub.Endpoint)
	} else {
		// Preserve original CreatedAt across re-POSTs.
		sub.CreatedAt = s.subs[sub.Endpoint].CreatedAt
	}
	s.subs[sub.Endpoint] = sub
}

// remove deletes a subscription by endpoint. No-op when absent.
func (s *pushSubscriptionStore) remove(endpoint string) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return
	}
	s.Lock()
	defer s.Unlock()
	if _, ok := s.subs[endpoint]; !ok {
		return
	}
	delete(s.subs, endpoint)
	for i, e := range s.order {
		if e == endpoint {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// has reports whether a subscription for the given endpoint is stored.
// Used by the frontend's re-subscription check (GET /api/push/subscribe).
func (s *pushSubscriptionStore) has(endpoint string) bool {
	s.RLock()
	defer s.RUnlock()
	_, ok := s.subs[strings.TrimSpace(endpoint)]
	return ok
}

// snapshot returns a copy of the subscriptions for matching. Used by
// NotifySurges; the copy is taken under RLock and the sends happen
// outside the lock so a slow push endpoint cannot stall other reads.
func (s *pushSubscriptionStore) snapshot() []*pushSubscription {
	s.RLock()
	defer s.RUnlock()
	out := make([]*pushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}

// size returns the current subscription count.
func (s *pushSubscriptionStore) size() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.subs)
}

// matches reports whether a subscription wants pushes for the given
// (band, region). The "all" preference enables every surge; otherwise
// the per-(band:region) map is consulted. An empty preference map with
// no "all" key matches nothing (subscription stored but inert — the
// "no push when disabled" test scenario).
func (s *pushSubscription) matches(band, region string) bool {
	if s == nil || s.Preferences == nil {
		return false
	}
	if s.Preferences["all"] {
		return true
	}
	return s.Preferences[band+":"+region]
}

// pushSurgePayload is the JSON body sent to the push endpoint and read
// by the Service Worker's `push` event handler. The label is the
// human-readable surge string ("tune to 10m, surge to Caribbean"); the
// band/region are the machine-readable coordinates for the click-through
// focus action.
type pushSurgePayload struct {
	Band   string `json:"band"`
	Region string `json:"region"`
	Label  string `json:"label"`
	ZScore float64 `json:"z_score,omitempty"`
}

// NotifySurges iterates the stored subscriptions and sends a push for
// each subscription whose preferences match a surged cell's (band,
// region). Called from the prop_intel engine after detectSurges runs.
// qth is the operator's QTH for the request (subscriptions are not
// filtered by QTH in v1 — every subscription sees every surge it opted
// into; QTH filtering is a v2 enhancement).
//
// Sends run synchronously here but the caller (propIntelHandler) invokes
// this in a goroutine so a slow push endpoint cannot block the HTTP
// response (the plan's async-push requirement). Per-send errors are
// logged (redacting Authorization/VAPID material) and counted; a 410/404
// from an expired endpoint removes the subscription from the store.
func (s *pushSubscriptionStore) NotifySurges(cells []propIntelCell, qth string) {
	if !s.isEnabled() {
		return
	}
	// Collect the surged cells once.
	var surges []propIntelCell
	for _, c := range cells {
		if c.Surge != nil {
			surges = append(surges, c)
		}
	}
	if len(surges) == 0 {
		return
	}
	subs := s.snapshot()
	for _, sub := range subs {
		for _, c := range surges {
			if !sub.matches(c.Band, c.Region) {
				continue
			}
			s.sendPushNotification(sub, pushSurgePayload{
				Band:   c.Band,
				Region: c.Region,
				Label:  c.Surge.Label,
				ZScore: c.Surge.ZScore,
			})
		}
	}
}

// sendPushNotification encrypts and POSTs a Web Push message to the
// subscription's endpoint via the store's sendFunc. Errors are logged
// with the endpoint host only (never the full URL with keys, never the
// Authorization header or VAPID material). A 410 Gone or 404 response
// removes the subscription — the browser has revoked/unistalled and the
// endpoint is dead.
func (s *pushSubscriptionStore) sendPushNotification(sub *pushSubscription, payload pushSurgePayload) {
	s.RLock()
	sendFunc := s.sendFunc
	enabled := s.enabled
	s.RUnlock()
	if !enabled || sendFunc == nil || sub == nil || sub.Endpoint == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
	defer cancel()
	status, err := sendFunc(ctx, sub, body)
	if err != nil {
		// Redact: log only the endpoint host, not the URL (which
		// carries no secrets itself but the Authorization header we
		// just sent does). VAPID material is never in the error
		// string.
		logInfo("push send failed (host=%s): %v", pushEndpointHost(sub.Endpoint), err)
		return
	}
	if status == http.StatusGone || status == http.StatusNotFound {
		s.remove(sub.Endpoint)
		logInfo("push endpoint gone (host=%s, status=%d); subscription removed", pushEndpointHost(sub.Endpoint), status)
		return
	}
	if status >= 500 {
		logInfo("push endpoint transient error (host=%s, status=%d)", pushEndpointHost(sub.Endpoint), status)
		return
	}
	if status >= 400 {
		logInfo("push endpoint client error (host=%s, status=%d)", pushEndpointHost(sub.Endpoint), status)
		return
	}
}

// pushEndpointHost returns the host of a subscription endpoint URL for
// redacted logging, or "?" when the URL does not parse.
func pushEndpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "?"
	}
	return u.Host
}

// sortedPushPreferenceKeys returns the preference map's keys sorted,
// for stable iteration in tests/diagnostics.
func sortedPushPreferenceKeys(prefs map[string]bool) []string {
	out := make([]string, 0, len(prefs))
	for k := range prefs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}