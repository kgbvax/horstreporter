package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func gunzipBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(b)
}

var ae = map[string]string{"Accept-Encoding": "gzip"}

func TestCachedStaticHandlerPrecompressedVariant(t *testing.T) {
	js := strings.Repeat("function foo(){return 1}\n", 200)
	fsys := fstest.MapFS{
		"app.js":     {Data: []byte(js)},
		"logo.png":   {Data: bytes.Repeat([]byte{9}, 3000)},
		"index.html": {Data: []byte("<html>" + strings.Repeat("a", 2000) + "</html>")},
		"tiny.js":    {Data: []byte("x=1")},
	}
	old := compressStream
	compressStream = true
	defer func() { compressStream = old }()
	h := cachedStaticHandler(fsys)

	get := func(target string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/app.js", ae)
	res := rec.Result()
	if res.Header.Get("Content-Encoding") != "gzip" || !strings.HasSuffix(res.Header.Get("ETag"), `-gz"`) || res.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("gz headers: %v", res.Header)
	}
	if got := gunzipBody(t, res); got != js {
		t.Fatal("precompressed body mismatch")
	}
	etag := res.Header.Get("ETag")

	// Round-trips to a 304 on the gz ETag.
	if again := get("/app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag}); again.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match on gz etag = %d, want 304", again.Code)
	}

	// Identity clients get the raw file with a different validator.
	plain := get("/app.js", nil).Result()
	if plain.Header.Get("Content-Encoding") != "" || plain.Header.Get("ETag") == etag || plain.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("identity headers: %v", plain.Header)
	}
	if b, _ := io.ReadAll(plain.Body); string(b) != js {
		t.Fatal("identity body mismatch")
	}

	if png := get("/logo.png", ae).Result(); png.Header.Get("Content-Encoding") != "" {
		t.Error("png must not be precompressed")
	}
	if tiny := get("/tiny.js", ae).Result(); tiny.Header.Get("Content-Encoding") != "" {
		t.Error("tiny files stay identity")
	}
	if root := get("/", ae).Result(); root.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("index served at / should be gz: %v", root.Header)
	}
}
