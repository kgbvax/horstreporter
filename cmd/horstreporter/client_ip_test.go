package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// clientIPFromRequest keys the per-IP rate limit of /api/history.
func TestClientIPFromRequest(t *testing.T) {
	// Save/restore the global trusted-proxy allowlist so this test does not
	// leak state into others.
	orig := trustedProxies
	defer func() { trustedProxies = orig }()
	trustedProxies = nil

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
		if err := setTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
			t.Fatal(err)
		}
		defer func() { trustedProxies = nil }()
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
		if err := setTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
			t.Fatal(err)
		}
		defer func() { trustedProxies = nil }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "8.8.8.8:9"
		if got := clientIPFromRequest(req); got != "8.8.8.8" {
			t.Errorf("clientIPFromRequest() = %q, want 8.8.8.8 (untrusted XFF ignored)", got)
		}
	})

	// No XFF at all: always fall back to RemoteAddr, port stripped.
	t.Run("no XFF, RemoteAddr with port", func(t *testing.T) {
		trustedProxies = nil
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "5.6.7.8:9"
		if got := clientIPFromRequest(req); got != "5.6.7.8" {
			t.Errorf("clientIPFromRequest() = %q, want 5.6.7.8", got)
		}
	})

	// setTrustedProxies rejects bad CIDRs.
	t.Run("bad CIDR rejected", func(t *testing.T) {
		if err := setTrustedProxies([]string{"not-a-cidr"}); err == nil {
			t.Errorf("setTrustedProxies(bad) = nil, want error")
		}
		trustedProxies = nil
	})
}
