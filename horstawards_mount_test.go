package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHorstawardsProxyScope(t *testing.T) {
	// Upstream stub standing in for horstawards on localhost.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream:" + r.URL.Path))
	}))
	defer upstream.Close()

	proxy, ok := newHorstawardsProxy(upstream.URL)
	if !ok {
		t.Fatal("expected proxy to be enabled")
	}
	mux := http.NewServeMux()
	mux.Handle("/horstawards/", proxy)

	cases := []struct {
		method, path string
		wantCode     int
		wantBody     string
	}{
		{"GET", "/horstawards/v1/health", http.StatusOK, "upstream:/v1/health"},
		{"POST", "/horstawards/v1/wanted", http.StatusOK, "upstream:/v1/wanted"},
		{"POST", "/horstawards/v1/refresh", http.StatusNotFound, ""}, // not exposed
		{"GET", "/horstawards/v1/debug", http.StatusNotFound, ""},    // not exposed
		{"GET", "/horstawards/", http.StatusNotFound, ""},            // not exposed
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.wantCode {
			t.Errorf("%s %s: code=%d want %d", c.method, c.path, rec.Code, c.wantCode)
		}
		if c.wantBody != "" && !strings.Contains(rec.Body.String(), c.wantBody) {
			t.Errorf("%s %s: body=%q want contains %q", c.method, c.path, rec.Body.String(), c.wantBody)
		}
	}
}

func TestHorstawardsProxyDisabledWhenEmpty(t *testing.T) {
	if _, ok := newHorstawardsProxy(""); ok {
		t.Error("expected disabled for empty target")
	}
	if _, ok := newHorstawardsProxy("not a url"); ok {
		t.Error("expected disabled for invalid target")
	}
}
