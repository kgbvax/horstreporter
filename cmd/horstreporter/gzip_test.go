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

func gzipDo(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	gzipMiddleware(h).ServeHTTP(rec, req)
	return rec.Result()
}

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

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
}

func TestGzipMiddlewareCompressesLargeJSON(t *testing.T) {
	body := `{"x":"` + strings.Repeat("abcdefgh", 500) + `"}`
	resp := gzipDo(t, jsonHandler(body), "GET", "/api/dx_conditions", ae)
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers: %v", resp.Header)
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Errorf("Content-Length must be dropped, got %q", resp.Header.Get("Content-Length"))
	}
	if got := gunzipBody(t, resp); got != body {
		t.Fatalf("round trip mismatch (%d bytes)", len(got))
	}
}

func TestGzipMiddlewareLeavesSmallAndUnacceptedAlone(t *testing.T) {
	small := `{"ok":true}`
	if resp := gzipDo(t, jsonHandler(small), "GET", "/api/x", ae); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("small body was encoded")
	} else if b, _ := io.ReadAll(resp.Body); string(b) != small {
		t.Errorf("small body = %q", b)
	}
	big := strings.Repeat("a", 4000)
	if resp := gzipDo(t, jsonHandler(big), "GET", "/api/x", nil); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("encoded without Accept-Encoding")
	}
	if resp := gzipDo(t, jsonHandler(big), "GET", "/api/x", map[string]string{"Accept-Encoding": "gzip;q=0"}); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("encoded despite gzip;q=0")
	}
	if resp := gzipDo(t, jsonHandler(big), "HEAD", "/api/x", ae); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("HEAD was encoded")
	}
	if resp := gzipDo(t, jsonHandler(big), "GET", "/api/x", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-10"}); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Range request was encoded")
	}
}

func TestGzipMiddlewarePassesThroughPreEncodedStreamsAndBinary(t *testing.T) {
	pre := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(bytes.Repeat([]byte{1}, 3000))
	})
	resp := gzipDo(t, pre, "GET", "/api/history", ae)
	if b, _ := io.ReadAll(resp.Body); len(b) != 3000 || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("pre-encoded body altered (%d bytes)", len(b))
	}

	sse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hi\n\n")
		w.(http.Flusher).Flush()
	})
	resp = gzipDo(t, sse, "GET", "/somewhere/events", ae)
	if b, _ := io.ReadAll(resp.Body); string(b) != "data: hi\n\n" || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("event-stream altered: %q", b)
	}

	// Flushing before any content type is known also means "streaming".
	flusher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "part")
		w.(http.Flusher).Flush()
		io.WriteString(w, strings.Repeat("b", 4000))
	})
	resp = gzipDo(t, flusher, "GET", "/x", ae)
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("flushed response must pass through")
	}

	png := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(bytes.Repeat([]byte{7}, 5000))
	})
	if resp := gzipDo(t, png, "GET", "/a.png", ae); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("png was encoded")
	}

	nm := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) })
	if resp := gzipDo(t, nm, "GET", "/a.js", ae); resp.StatusCode != http.StatusNotModified || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("304 altered: %d %v", resp.StatusCode, resp.Header)
	}

	for _, p := range []string{"/api/stream", "/pathscope/x"} {
		if resp := gzipDo(t, jsonHandler(strings.Repeat("a", 4000)), "GET", p, ae); resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s must be skipped", p)
		}
	}
}

func TestGzipMiddlewareSniffsMissingContentType(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html>"+strings.Repeat("x", 3000)+"</html>")
	})
	resp := gzipDo(t, h, "GET", "/", ae)
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("sniffed html must compress: %v", resp.Header)
	}
}

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
