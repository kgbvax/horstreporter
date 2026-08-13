package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// push_test.go verifies U5 test scenarios:
//   - subscription storage (POST /api/push/subscribe → stored, retrievable)
//   - unsubscription (POST /api/push/unsubscribe → removed)
//   - surge triggers push (NotifySurges → sendFunc called)
//   - no push when disabled ({"all": false} + no per-band/region → no push)
//   - VAPID key endpoint returns public key, not private
//   - max-subscription cap evicts oldest (FIFO)
//   - rate limiting: 11th POST from same IP within an hour → 429
//   - re-subscription after restart: re-POST is idempotent
//
// The Web Push send is mocked by injecting a fake pushSendFunc into
// pushStore.sendFunc so the tests do not need a real P-256 subscription
// key (webpush-go encrypts BEFORE the HTTP send, so mocking only the
// HTTP client would fail on test keys).

// mockPushSend captures the payload(s) sent via sendFunc. Records every
// call; the response status is configurable via status field. A 0
// status acts like a transport error (returns 0 + nil).
type mockPushSend struct {
	mu         sync.Mutex
	payloads   []pushSurgePayload
	subs       []*pushSubscription
	status     int
	failWith   error
	goneOnIdx  int // when >0, return goneStatus on this send index (1-based)
	goneStatus int // status to return at goneOnIdx; 0 defaults to 410 Gone
}

func (m *mockPushSend) send(ctx context.Context, sub *pushSubscription, payload []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := len(m.payloads) + 1
	var p pushSurgePayload
	_ = json.Unmarshal(payload, &p)
	m.payloads = append(m.payloads, p)
	m.subs = append(m.subs, sub)
	if m.failWith != nil {
		return 0, m.failWith
	}
	status := m.status
	if status == 0 {
		status = http.StatusCreated
	}
	if m.goneOnIdx > 0 && idx == m.goneOnIdx {
		status = m.goneStatus
		if status == 0 {
			status = http.StatusGone
		}
	}
	return status, nil
}

func (m *mockPushSend) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.payloads)
}

func (m *mockPushSend) lastPayload() pushSurgePayload {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.payloads) == 0 {
		return pushSurgePayload{}
	}
	return m.payloads[len(m.payloads)-1]
}

// newTestPushStore returns a configured, isolated push store with a
// mock sendFunc for sendPushNotification tests.
func newTestPushStore(t *testing.T, mock *mockPushSend) {
	t.Helper()
	pushStore.configure("test-private-key", "test-public-key", "mailto:test@example.com", true)
	pushStore.Lock()
	pushStore.sendFunc = mock.send
	pushStore.Unlock()
}

// resetPushStoreForTest clears the global pushStore between tests so
// state does not leak.
func resetPushStoreForTest() {
	pushStore.Lock()
	pushStore.subs = make(map[string]*pushSubscription)
	pushStore.order = nil
	pushStore.sendFunc = nil
	pushStore.httpClient = nil
	pushStore.Unlock()
	pushRateLimiter.Lock()
	pushRateLimiter.counts = make(map[string]*pushIPRateWindow)
	pushRateLimiter.Unlock()
}

// makeSub builds a valid pushSubscription for tests.
func makeSub(endpoint string, prefs map[string]bool) *pushSubscription {
	if prefs == nil {
		prefs = map[string]bool{}
	}
	return &pushSubscription{
		Endpoint:    endpoint,
		Keys:        pushSubscriptionKeys{Auth: "auth-key", P256dh: "p256dh-key"},
		QTH:         "JO62",
		Preferences: prefs,
		CreatedAt:   time.Now(),
	}
}

// equalStringSlices reports whether two string slices are equal in length
// and element order. Used to assert FIFO order is preserved.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPushStoreSubscribeAndRetrieve(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	sub := makeSub("https://fcm.googleapis.com/fcm/abc", map[string]bool{"all": true})
	pushStore.add(sub)

	if !pushStore.has(sub.Endpoint) {
		t.Errorf("expected subscription stored and retrievable")
	}
	if got := pushStore.size(); got != 1 {
		t.Errorf("size = %d, want 1", got)
	}
}

func TestPushStoreUnsubscribe(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	sub := makeSub("https://fcm.googleapis.com/fcm/def", map[string]bool{"all": true})
	pushStore.add(sub)
	pushStore.remove(sub.Endpoint)
	if pushStore.has(sub.Endpoint) {
		t.Errorf("expected subscription removed after unsubscribe")
	}
	if got := pushStore.size(); got != 0 {
		t.Errorf("size = %d, want 0 after unsubscribe", got)
	}
}

func TestPushNotifySurgesSendsPush(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	// Subscription opts into 10m:CAR.
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/surge1", map[string]bool{"10m:CAR": true}))

	cells := []propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "tune to 10m, surge to Caribbean"}},
		// Non-surge cell should not generate a push.
		{Band: "20m", Region: "EU"},
	}
	pushStore.NotifySurges(cells, "JO62")

	if got := mock.count(); got != 1 {
		t.Errorf("expected 1 push sent, got %d", got)
	}
	p := mock.lastPayload()
	if p.Band != "10m" {
		t.Errorf("push payload band = %q, want 10m", p.Band)
	}
	if p.Region != "CAR" {
		t.Errorf("push payload region = %q, want CAR", p.Region)
	}
	if p.Label != "tune to 10m, surge to Caribbean" {
		t.Errorf("push payload label = %q, want surge label", p.Label)
	}
	if p.ZScore != 7.0 {
		t.Errorf("push payload z_score = %v, want 7.0", p.ZScore)
	}
}

func TestPushNotifySurgesNoPushWhenDisabled(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	// Subscription with empty preferences (no "all", no per-band/region)
	// — inert. Matches the "no push when disabled" scenario.
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/inert", map[string]bool{}))
	// Also test an explicit {"all": false} subscription.
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/falseall", map[string]bool{"all": false}))

	cells := []propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "tune to 10m, surge to Caribbean"}},
	}
	pushStore.NotifySurges(cells, "JO62")

	if got := mock.count(); got != 0 {
		t.Errorf("expected 0 pushes for inert subscription, got %d", got)
	}
}

func TestPushNotifySurgesAllPreference(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	// "all" preference should match every surge.
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/all", map[string]bool{"all": true}))
	cells := []propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "tune to 10m, surge to Caribbean"}},
		{Band: "20m", Region: "EU", Surge: &SurgeInfo{ZScore: 3.0, Label: "tune to 20m, surge to Europe"}},
	}
	pushStore.NotifySurges(cells, "JO62")
	if got := mock.count(); got != 2 {
		t.Errorf("expected 2 pushes for all-preference subscription, got %d", got)
	}
}

func TestPushNotifySurgesDisabledStore(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	// Push disabled — NotifySurges is a no-op.
	pushStore.configure("", "", "", false)
	mock := &mockPushSend{}
	pushStore.Lock()
	pushStore.sendFunc = mock.send
	pushStore.Unlock()

	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/disabled", map[string]bool{"all": true}))
	pushStore.NotifySurges([]propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "x"}},
	}, "")
	if got := mock.count(); got != 0 {
		t.Errorf("expected 0 pushes when push disabled, got %d", got)
	}
}

func TestPushNotifySurgesNoSurgeCells(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/nosurge", map[string]bool{"all": true}))
	// No surged cells → no pushes.
	pushStore.NotifySurges([]propIntelCell{
		{Band: "10m", Region: "CAR"},
	}, "")
	if got := mock.count(); got != 0 {
		t.Errorf("expected 0 pushes when no cells have Surge, got %d", got)
	}
}

func TestPushVAPIDPublicKeyHandler(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	newTestPushStore(t, &mockPushSend{})

	req := httptest.NewRequest(http.MethodGet, "/api/push/vapid-public-key", nil)
	rec := httptest.NewRecorder()
	pushVAPIDPublicKeyHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var data struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if data.PublicKey != "test-public-key" {
		t.Errorf("public_key = %q, want test-public-key", data.PublicKey)
	}
	// The private key must never appear in the response.
	if strings.Contains(rec.Body.String(), "test-private-key") {
		t.Errorf("response leaked the private key: %s", rec.Body.String())
	}
}

func TestPushVAPIDPublicKeyHandlerDisabled(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	pushStore.configure("", "", "", false)
	req := httptest.NewRequest(http.MethodGet, "/api/push/vapid-public-key", nil)
	rec := httptest.NewRecorder()
	pushVAPIDPublicKeyHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when push disabled", rec.Code)
	}
}

func TestPushSubscribeHandlerStoresAndRejects(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	newTestPushStore(t, &mockPushSend{})

	// Valid subscription → stored.
	body := `{"endpoint":"https://fcm.googleapis.com/fcm/valid","keys":{"auth":"a","p256dh":"p"},"qth":"JO62","preferences":{"all":true}}`
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:5678"
	rec := httptest.NewRecorder()
	pushSubscribeHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid subscribe status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !pushStore.has("https://fcm.googleapis.com/fcm/valid") {
		t.Errorf("valid subscription was not stored")
	}

	// Missing endpoint → 400.
	badBody := `{"endpoint":"","keys":{"auth":"a","p256dh":"p"}}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(badBody))
	req2.RemoteAddr = "1.2.3.4:5678"
	rec2 := httptest.NewRecorder()
	pushSubscribeHandler(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("missing-endpoint status = %d, want 400", rec2.Code)
	}

	// Non-HTTPS endpoint → 400.
	badHTTPS := `{"endpoint":"http://example.com/push","keys":{"auth":"a","p256dh":"p"}}`
	req3 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(badHTTPS))
	req3.RemoteAddr = "1.2.3.4:5678"
	rec3 := httptest.NewRecorder()
	pushSubscribeHandler(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("non-https status = %d, want 400", rec3.Code)
	}

	// Missing p256dh → 400.
	badKeys := `{"endpoint":"https://fcm.googleapis.com/fcm/x","keys":{"auth":"a","p256dh":""}}`
	req4 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(badKeys))
	req4.RemoteAddr = "1.2.3.4:5678"
	rec4 := httptest.NewRecorder()
	pushSubscribeHandler(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Errorf("missing-p256dh status = %d, want 400", rec4.Code)
	}

	// Invalid JSON body → 400.
	req5 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader("{not json"))
	req5.RemoteAddr = "1.2.3.4:5678"
	rec5 := httptest.NewRecorder()
	pushSubscribeHandler(rec5, req5)
	if rec5.Code != http.StatusBadRequest {
		t.Errorf("invalid-json status = %d, want 400", rec5.Code)
	}

	// Wrong method → 405.
	req6 := httptest.NewRequest(http.MethodGet, "/api/push/subscribe", nil)
	rec6 := httptest.NewRecorder()
	pushSubscribeHandler(rec6, req6)
	if rec6.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec6.Code)
	}
}

func TestPushUnsubscribeHandlerRemoves(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	newTestPushStore(t, &mockPushSend{})

	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/unsub", map[string]bool{"all": true}))
	body := `{"endpoint":"https://fcm.googleapis.com/fcm/unsub"}`
	req := httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(body))
	rec := httptest.NewRecorder()
	pushUnsubscribeHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d, want 200", rec.Code)
	}
	if pushStore.has("https://fcm.googleapis.com/fcm/unsub") {
		t.Errorf("subscription still present after unsubscribe")
	}
}

func TestPushSubscriptionStatusHandler(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	newTestPushStore(t, &mockPushSend{})

	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/status", map[string]bool{"all": true}))

	// Existing → registered=true.
	req := httptest.NewRequest(http.MethodGet, "/api/push/subscription-status?endpoint=https://fcm.googleapis.com/fcm/status", nil)
	rec := httptest.NewRecorder()
	pushSubscriptionStatusHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var data struct {
		Registered bool `json:"registered"`
	}
	json.NewDecoder(rec.Body).Decode(&data)
	if !data.Registered {
		t.Errorf("expected registered=true for stored endpoint, got %s", rec.Body.String())
	}

	// Missing → registered=false.
	req2 := httptest.NewRequest(http.MethodGet, "/api/push/subscription-status?endpoint=https://fcm.googleapis.com/fcm/missing", nil)
	rec2 := httptest.NewRecorder()
	pushSubscriptionStatusHandler(rec2, req2)
	json.NewDecoder(rec2.Body).Decode(&data)
	if data.Registered {
		t.Errorf("expected registered=false for missing endpoint, got %s", rec2.Body.String())
	}

	// Missing endpoint param → 400.
	req3 := httptest.NewRequest(http.MethodGet, "/api/push/subscription-status", nil)
	rec3 := httptest.NewRecorder()
	pushSubscriptionStatusHandler(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("missing-endpoint-param status = %d, want 400", rec3.Code)
	}
}

func TestPushRateLimiting(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	// Use a fresh per-IP limiter to avoid cross-test state.
	rl := &pushIPRateLimiter{counts: make(map[string]*pushIPRateWindow)}
	ip := "203.0.113.9"
	// First pushSubscribeRatePerHour (10) requests are allowed.
	for i := 0; i < pushSubscribeRatePerHour; i++ {
		if !rl.allow(ip) {
			t.Fatalf("request %d should be allowed, was rate-limited", i+1)
		}
	}
	// 11th → denied.
	if rl.allow(ip) {
		t.Errorf("11th request from same IP should be rate-limited (429)")
	}
	// A different IP is unaffected.
	if !rl.allow("198.51.100.1") {
		t.Errorf("different IP should be allowed")
	}
	// An empty IP (e.g. misconfigured proxy) is always allowed —
	// abuse from a blank IP is bounded by the max-subscription cap.
	if !rl.allow("") {
		t.Errorf("empty IP should be allowed (bounded by subscription cap)")
	}
}

// TestPushRateLimitingHandler verifies the 11th POST from the same IP
// within an hour returns 429 Too Many Requests.
func TestPushRateLimitingHandler(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	newTestPushStore(t, &mockPushSend{})

	body := `{"endpoint":"https://fcm.googleapis.com/fcm/rl-%s","keys":{"auth":"a","p256dh":"p"},"preferences":{"all":true}}`
	ip := "192.0.2.7"
	// First pushSubscribeRatePerHour requests succeed.
	for i := 0; i < pushSubscribeRatePerHour; i++ {
		b := strings.ReplaceAll(body, "%s", string(rune('a'+i%26))+string(rune('a'+i/26)))
		req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(b))
		req.RemoteAddr = ip + ":5000"
		rec := httptest.NewRecorder()
		pushSubscribeHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (body=%s)", i+1, rec.Code, rec.Body.String())
		}
	}
	// 11th → 429.
	b := strings.ReplaceAll(body, "%s", "zz")
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(b))
	req.RemoteAddr = ip + ":5000"
	rec := httptest.NewRecorder()
	pushSubscribeHandler(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("11th request status = %d, want 429", rec.Code)
	}
}

func TestPushMaxSubscriptionCapFIFOEviction(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	// Add exactly pushMaxSubscriptions subscriptions, then one more.
	// The first (oldest) should be evicted.
	for i := 0; i < pushMaxSubscriptions; i++ {
		endpoint := "https://fcm.googleapis.com/fcm/cap" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		pushStore.add(makeSub(endpoint, map[string]bool{"all": true}))
	}
	firstEndpoint := "https://fcm.googleapis.com/fcm/capaa"
	if !pushStore.has(firstEndpoint) {
		t.Fatalf("setup: expected first subscription present before eviction")
	}
	// 1001st → evicts the oldest (firstEndpoint).
	pushStore.add(makeSub("https://fcm.googleapis.com/fcm/capNEW", map[string]bool{"all": true}))
	if pushStore.has(firstEndpoint) {
		t.Errorf("expected oldest subscription evicted after cap exceeded; still present")
	}
	if got := pushStore.size(); got != pushMaxSubscriptions {
		t.Errorf("size after eviction = %d, want %d", got, pushMaxSubscriptions)
	}
	if !pushStore.has("https://fcm.googleapis.com/fcm/capNEW") {
		t.Errorf("new subscription not present after eviction")
	}
}

func TestPushReSubscribeIdempotent(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)

	endpoint := "https://fcm.googleapis.com/fcm/resub"
	other := "https://fcm.googleapis.com/fcm/other"
	// First subscription: all=true. Then a second, distinct endpoint so the
	// FIFO order has more than one entry to permute.
	pushStore.add(makeSub(endpoint, map[string]bool{"all": true}))
	pushStore.add(makeSub(other, map[string]bool{"all": true}))
	// snapshot() iterates a map (random order), so look up by endpoint
	// rather than relying on slice index.
	original := findSubByEndpoint(pushStore.snapshot(), endpoint)
	orderBefore := append([]string(nil), pushStore.order...)
	// Re-POST with different preferences (idempotent update): 10m:CAR only.
	pushStore.add(makeSub(endpoint, map[string]bool{"10m:CAR": true}))
	if got := pushStore.size(); got != 2 {
		t.Errorf("size after re-subscribe = %d, want 2 (idempotent)", got)
	}
	updated := findSubByEndpoint(pushStore.snapshot(), endpoint)
	if updated.Preferences["all"] {
		t.Errorf("re-subscribe should have overwritten preferences; 'all' still true")
	}
	if !updated.Preferences["10m:CAR"] {
		t.Errorf("re-subscribe should have set 10m:CAR=true")
	}
	// CreatedAt should be preserved (FIFO order unchanged).
	if !original.CreatedAt.Equal(updated.CreatedAt) {
		t.Errorf("re-subscribe changed CreatedAt (FIFO order should be preserved)")
	}
	// FIFO order must be unchanged: re-subscribing an existing endpoint must
	// NOT append it to the tail (which would move it to the front of
	// eviction). The order slice should be identical to before.
	if got, want := pushStore.order, orderBefore; !equalStringSlices(got, want) {
		t.Errorf("re-subscribe changed FIFO order: got %v, want %v", got, want)
	}
}

// findSubByEndpoint returns the subscription with the given endpoint from a
// snapshot, or nil. snapshot() iterates a map in random order, so callers
// must look up by endpoint rather than assuming a slice position.
func findSubByEndpoint(subs []*pushSubscription, endpoint string) *pushSubscription {
	for _, s := range subs {
		if s.Endpoint == endpoint {
			return s
		}
	}
	return nil
}

func TestPushSendRemovesSubscriptionOnGone(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{goneOnIdx: 1}
	newTestPushStore(t, mock)

	endpoint := "https://fcm.googleapis.com/fcm/expired"
	pushStore.add(makeSub(endpoint, map[string]bool{"all": true}))
	pushStore.NotifySurges([]propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "x"}},
	}, "")
	// The 410 Gone response should have removed the subscription.
	if pushStore.has(endpoint) {
		t.Errorf("expected subscription removed after 410 Gone")
	}
}

// TestPushSendRemovesSubscriptionOn404 mirrors the 410 case for a 404
// response. RFC 8030 treats 404 the same as 410 for subscription validity:
// the endpoint is no longer valid and must be removed so the store does
// not keep retrying a dead endpoint.
func TestPushSendRemovesSubscriptionOn404(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{goneOnIdx: 1, goneStatus: http.StatusNotFound}
	newTestPushStore(t, mock)

	endpoint := "https://fcm.googleapis.com/fcm/notfound"
	pushStore.add(makeSub(endpoint, map[string]bool{"all": true}))
	pushStore.NotifySurges([]propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "x"}},
	}, "")
	if pushStore.has(endpoint) {
		t.Errorf("expected subscription removed after 404 Not Found")
	}
}

func TestPushSendLogsButKeepsOnTransientError(t *testing.T) {
	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{status: 503}
	newTestPushStore(t, mock)
	endpoint := "https://fcm.googleapis.com/fcm/transient"
	pushStore.add(makeSub(endpoint, map[string]bool{"all": true}))
	pushStore.NotifySurges([]propIntelCell{
		{Band: "10m", Region: "CAR", Surge: &SurgeInfo{ZScore: 7.0, Label: "x"}},
	}, "")
	// 5xx is transient — subscription should be retained.
	if !pushStore.has(endpoint) {
		t.Errorf("expected subscription retained after 5xx (transient)")
	}
}

func TestPushValidateSubscription(t *testing.T) {
	cases := []struct {
		name string
		sub  *pushSubscription
		want bool // true = valid (no error)
	}{
		{"valid", &pushSubscription{Endpoint: "https://x/y", Keys: pushSubscriptionKeys{Auth: "a", P256dh: "p"}}, true},
		{"missing endpoint", &pushSubscription{Endpoint: "", Keys: pushSubscriptionKeys{Auth: "a", P256dh: "p"}}, false},
		{"http endpoint", &pushSubscription{Endpoint: "http://x/y", Keys: pushSubscriptionKeys{Auth: "a", P256dh: "p"}}, false},
		{"hostless https endpoint", &pushSubscription{Endpoint: "https:///fcm/x", Keys: pushSubscriptionKeys{Auth: "a", P256dh: "p"}}, false},
		{"missing p256dh", &pushSubscription{Endpoint: "https://x/y", Keys: pushSubscriptionKeys{Auth: "a", P256dh: ""}}, false},
		{"missing auth", &pushSubscription{Endpoint: "https://x/y", Keys: pushSubscriptionKeys{Auth: "", P256dh: "p"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePushSubscription(tc.sub)
			got := err == nil
			if got != tc.want {
				t.Errorf("validatePushSubscription() = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

func TestPushMatches(t *testing.T) {
	cases := []struct {
		name   string
		prefs  map[string]bool
		band   string
		region string
		want   bool
	}{
		{"all true", map[string]bool{"all": true}, "10m", "CAR", true},
		{"all false, matching cell", map[string]bool{"all": false, "10m:CAR": true}, "10m", "CAR", true},
		{"all false, non-matching cell", map[string]bool{"all": false, "10m:CAR": true}, "20m", "EU", false},
		{"empty prefs", map[string]bool{}, "10m", "CAR", false},
		{"nil prefs", nil, "10m", "CAR", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &pushSubscription{Preferences: tc.prefs}
			if got := sub.matches(tc.band, tc.region); got != tc.want {
				t.Errorf("matches(%s,%s) = %v, want %v", tc.band, tc.region, got, tc.want)
			}
		})
	}
}

func TestPushClientIPFromRequest(t *testing.T) {
	// Save/restore the global trusted-proxy allowlist so this test does not
	// leak state into others.
	orig := pushTrustedProxies
	defer func() { pushTrustedProxies = orig }()
	pushTrustedProxies = nil

	// Without a trusted-proxy allowlist, X-Forwarded-For must NEVER be
	// honored — otherwise a client could spoof it to rotate the rate-limit
	// key. The client IP comes from RemoteAddr.
	t.Run("no trusted proxies: XFF ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "5.6.7.8:9"
		if got := clientIPFromRequest(req); got != "5.6.7.8" {
			t.Errorf("clientIPFromRequest() = %q, want 5.6.7.8 (XFF must be ignored)", got)
		}
	})

	// With a trusted proxy matching the direct peer, XFF is honored
	// (leftmost entry).
	t.Run("trusted proxy: XFF honored", func(t *testing.T) {
		if err := setPushTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
			t.Fatal(err)
		}
		defer func() { pushTrustedProxies = nil }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4, 9.8.7.6")
		req.RemoteAddr = "10.0.0.1:9"
		if got := clientIPFromRequest(req); got != "1.2.3.4" {
			t.Errorf("clientIPFromRequest() = %q, want 1.2.3.4", got)
		}
	})

	// XFF from a peer NOT in the trusted allowlist is ignored (the peer is
	// a direct client, not a proxy, so its XFF is untrusted/spoofed).
	t.Run("untrusted peer: XFF ignored", func(t *testing.T) {
		if err := setPushTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
			t.Fatal(err)
		}
		defer func() { pushTrustedProxies = nil }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "8.8.8.8:9"
		if got := clientIPFromRequest(req); got != "8.8.8.8" {
			t.Errorf("clientIPFromRequest() = %q, want 8.8.8.8 (untrusted XFF ignored)", got)
		}
	})

	// No XFF at all: always fall back to RemoteAddr, port stripped.
	t.Run("no XFF, RemoteAddr with port", func(t *testing.T) {
		pushTrustedProxies = nil
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "5.6.7.8:9"
		if got := clientIPFromRequest(req); got != "5.6.7.8" {
			t.Errorf("clientIPFromRequest() = %q, want 5.6.7.8", got)
		}
	})

	// setPushTrustedProxies rejects bad CIDRs.
	t.Run("bad CIDR rejected", func(t *testing.T) {
		if err := setPushTrustedProxies([]string{"not-a-cidr"}); err == nil {
			t.Errorf("setPushTrustedProxies(bad) = nil, want error")
		}
		pushTrustedProxies = nil
	})
}

// TestPushSurgePresent guards the goroutine-skip helper in prop_intel.go.
func TestPushSurgePresent(t *testing.T) {
	if surgePresent(nil) {
		t.Errorf("surgePresent(nil) = true, want false")
	}
	if surgePresent([]propIntelCell{{Band: "10m", Region: "CAR"}}) {
		t.Errorf("surgePresent(no surge) = true, want false")
	}
	if !surgePresent([]propIntelCell{{Band: "10m", Region: "CAR", Surge: &SurgeInfo{Label: "x"}}}) {
		t.Errorf("surgePresent(with surge) = false, want true")
	}
}

// TestPushSubscriptionRequestShape confirms the request struct decodes
// the browser PushSubscription JSON.
func TestPushSubscriptionRequestShape(t *testing.T) {
	raw := `{"endpoint":"https://fcm/x","keys":{"auth":"a","p256dh":"p"},"qth":"jo62","preferences":{"all":true}}`
	var req pushSubscribeRequest
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Endpoint != "https://fcm/x" {
		t.Errorf("endpoint = %q", req.Endpoint)
	}
	if req.Keys.Auth != "a" || req.Keys.P256dh != "p" {
		t.Errorf("keys = %+v", req.Keys)
	}
	if req.QTH != "jo62" {
		t.Errorf("qth = %q", req.QTH)
	}
	if !req.Preferences["all"] {
		t.Errorf("preferences.all missing")
	}
}

// TestPushEndpointHost verifies redacted logging.
func TestPushEndpointHost(t *testing.T) {
	if got := pushEndpointHost("https://fcm.googleapis.com/fcm/x"); got != "fcm.googleapis.com" {
		t.Errorf("host = %q, want fcm.googleapis.com", got)
	}
	if got := pushEndpointHost("not a url"); got != "?" {
		t.Errorf("host = %q, want ?", got)
	}
}
