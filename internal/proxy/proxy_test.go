package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnconfiguredAndInvalidTargets(t *testing.T) {
	for _, build := range []func(string) (http.Handler, error){Horstprop, Pathscope} {
		if h, err := build("  "); h != nil || err != nil {
			t.Fatalf("blank target: got (%v, %v), want (nil, nil)", h, err)
		}
		if h, err := build("not a url"); h != nil || err == nil {
			t.Fatalf("invalid target: got (%v, %v), want (nil, error)", h, err)
		}
	}
}

func TestHorstpropStripsPrefix(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path }))
	defer up.Close()
	h, err := Horstprop(up.URL)
	if err != nil || h == nil {
		t.Fatalf("Horstprop: %v, %v", h, err)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/horstprop/v1/score", nil))
	if gotPath != "/v1/score" {
		t.Fatalf("upstream path = %q, want /v1/score", gotPath)
	}
}

func TestPathscopeKeepsPrefixAndFailsSoft(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path }))
	h, _ := Pathscope(up.URL)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/pathscope/app.js", nil))
	if gotPath != "/pathscope/app.js" {
		t.Fatalf("upstream path = %q, want /pathscope/app.js", gotPath)
	}
	up.Close()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/pathscope/app.js", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("down upstream: status %d, want 502", rec.Code)
	}
}
