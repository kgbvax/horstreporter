package server

import (
	"encoding/json"
	"embed"
	"net/http"
	"net/http/httptest"
	"testing"

	"horstreporter/internal/region"
)

//go:embed static
var testStatic embed.FS

// TestHealth is a smoke test that the route is wired and the JSON shape
// is correct. It does not touch pathscope.Store or pathscope.Engine
// because those require a real pgxpool. Server.New accepts nil for both
// at the route-wiring level; only the JSON response handlers touch them.
func TestHealth(t *testing.T) {
	s := New(Deps{
		Store:      nil,
		Engine:     nil,
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/health", nil)
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if resp.Version != "test" {
		t.Errorf("version = %q, want test", resp.Version)
	}
	if resp.QTH != "JO62qm" {
		t.Errorf("qth = %q, want JO62qm", resp.QTH)
	}
	if resp.HomeRegion != "EU" {
		t.Errorf("home region = %q, want EU", resp.HomeRegion)
	}
}

// TestStaticRootServed verifies that the embedded static is reachable on
// /pathscope/ once the embed.FS is plumbed through.
func TestStaticRootServed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})

	// emjay's http.FileServer is finicky about trailing slashes — request
	// the file directly so the test is deterministic.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope/", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static /pathscope/: status = %d, body=%q, location=%q",
			rec.Code, rec.Body.String(), rec.Header().Get("Location"))
	}
	if rec.Body.Len() == 0 {
		t.Errorf("static body empty")
	}
}

func TestRedirectToSlash(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("redirect status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/pathscope/" {
		t.Errorf("Location = %q, want /pathscope/", loc)
	}
}

func TestGlanceMethodNotAllowed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pathscope/v1/glance", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestCellMissingParams(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/cell", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCellInvalidRegion(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/cell?band=20M&region=ZZ", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestPrefixedHealth exercises the /pathscope/api/pathscope/v1/* mirror
// mounted so the static UI (served at /pathscope/) can call relative
// URLs without an nginx rewrite rule.
func TestPrefixedHealth(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope/api/pathscope/v1/health", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
}

func TestPrefixedGlanceMethodNotAllowed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pathscope/api/pathscope/v1/glance", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
