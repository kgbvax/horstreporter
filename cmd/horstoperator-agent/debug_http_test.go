package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestBodyPreviewRedactsSecrets(t *testing.T) {
	in := []byte(`{"key":"SUPERSECRET123","station_id":"7","fetchfromid":0}`)
	got := bodyPreview(in)
	if strings.Contains(got, "SUPERSECRET123") {
		t.Fatalf("preview leaked the API key: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("preview did not redact the key: %q", got)
	}
	if !strings.Contains(got, `"station_id":"7"`) {
		t.Fatalf("preview dropped non-secret fields: %q", got)
	}
}

func TestBodyPreviewTruncates(t *testing.T) {
	long := []byte(strings.Repeat("a", debugBodyPreview*2))
	got := bodyPreview(long)
	if len([]rune(got)) > debugBodyPreview+len("body=")+1 {
		t.Fatalf("preview not truncated: len=%d", len([]rune(got)))
	}
}

func TestRedactURLStripsSecretQuery(t *testing.T) {
	u, _ := url.Parse("https://api.pota.app/profile?token=abc123&call=DL0HRP")
	got := redactURL(u)
	if strings.Contains(got, "abc123") {
		t.Fatalf("redactURL leaked token: %q", got)
	}
	if !strings.Contains(got, "call=DL0HRP") {
		t.Fatalf("redactURL dropped non-secret param: %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	t.Run("nil URL", func(t *testing.T) {
		if got := redactURL(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("no query passes through", func(t *testing.T) {
		u, _ := url.Parse("https://hr.example.org/api/stats")
		if got := redactURL(u); got != "https://hr.example.org/api/stats" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("secret keys are case-insensitive", func(t *testing.T) {
		for _, raw := range []string{
			"https://x.example/p?Key=topsecret",
			"https://x.example/p?API_KEY=topsecret",
			"https://x.example/p?apikey=topsecret",
			"https://x.example/p?password=topsecret",
		} {
			u, _ := url.Parse(raw)
			got := redactURL(u)
			if strings.Contains(got, "topsecret") {
				t.Errorf("leaked secret via %s: %q", raw, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("no REDACTED marker for %s: %q", raw, got)
			}
		}
	})
	t.Run("non-secret query untouched", func(t *testing.T) {
		u, _ := url.Parse("https://x.example/p?band=20m&token=abc")
		got := redactURL(u)
		if !strings.Contains(got, "band=20m") {
			t.Errorf("dropped non-secret param: %q", got)
		}
	})
}

// stubTransport records the request the round-tripper forwarded, so the test can
// prove the peeked request body is re-joined intact.
type stubTransport struct {
	gotBody string
	resp    *http.Response
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	s.gotBody = string(b)
	return s.resp, nil
}

// The round-tripper peeks up to 512 bytes of request and response bodies for the
// log; the caller must still receive both bodies complete and unchanged.
func TestLoggingRoundTripperPreservesBodies(t *testing.T) {
	// Silence the transport's [DEBUG] log lines for this test.
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	respBody := strings.Repeat("x", debugBodyPreview+50) // longer than the peek window
	stub := &stubTransport{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(respBody)),
	}}
	rt := &loggingRoundTripper{base: stub}

	reqBody := `{"key":"SEKRIT","callsign":"DL0HRP"}`
	req, _ := http.NewRequest(http.MethodPost, "https://wl.example.org/api/private_lookup", strings.NewReader(reqBody))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if stub.gotBody != reqBody {
		t.Errorf("upstream saw request body %q, want original", stub.gotBody)
	}
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(got) != respBody {
		t.Errorf("response body truncated/altered: len=%d want=%d", len(got), len(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// End-to-end: with the logging transport in front of a real handler, the [DEBUG]
// log lines must never contain a secret from URL query, request body or response
// body — but must still show the redacted shape.
func TestLoggingRoundTripperRedactsSecretsInLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"RESPONSESECRET","ok":true}`))
	}))
	defer srv.Close()

	rt := &loggingRoundTripper{base: http.DefaultTransport}
	body := `{"key":"BODYSECRET","station_id":"7"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/private_lookup?key=QUERYSECRET", strings.NewReader(body))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	logs := buf.String()
	for _, secret := range []string{"BODYSECRET", "RESPONSESECRET", "QUERYSECRET"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log leaked %s:\n%s", secret, logs)
		}
	}
	if strings.Count(logs, "REDACTED") < 3 {
		t.Errorf("expected request body, response body and query redactions in:\n%s", logs)
	}
	if !strings.Contains(logs, "/api/private_lookup") || !strings.Contains(logs, "[DEBUG] ext http") {
		t.Errorf("logs missing the redacted request/response shape:\n%s", logs)
	}
}

// The flag-gated switch: off by default; enableExternalDebugLogging wraps the
// process default transport exactly once (idempotent) and flips the package
// scope flag. Global state is restored so other tests stay unaffected.
func TestEnableExternalDebugLoggingGate(t *testing.T) {
	prevTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prevTransport })

	if debugExternalEnabled() {
		t.Fatal("debug logging must be off by default")
	}

	enableExternalDebugLogging()
	if !debugExternalEnabled() {
		t.Error("debug flag not enabled after enableExternalDebugLogging")
	}
	wrapped, ok := http.DefaultTransport.(*loggingRoundTripper)
	if !ok {
		t.Fatalf("DefaultTransport = %T, want *loggingRoundTripper", http.DefaultTransport)
	}

	// Idempotent: a second call must not double-wrap.
	enableExternalDebugLogging()
	again, ok := http.DefaultTransport.(*loggingRoundTripper)
	if !ok || again != wrapped {
		t.Errorf("DefaultTransport re-wrapped: %T", http.DefaultTransport)
	}
}
