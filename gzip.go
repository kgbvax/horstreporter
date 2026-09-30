package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

// gzipMinSize is the smallest response worth compressing; below it the gzip
// header/trailer overhead and CPU aren't worth it.
const gzipMinSize = 1024

var gzipWriterPool = sync.Pool{
	New: func() any {
		zw, _ := gzip.NewWriterLevel(io.Discard, 5)
		return zw
	},
}

// gzipCompressibleType reports whether a Content-Type is text-like.
func gzipCompressibleType(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case ct == "text/event-stream":
		return false
	case strings.HasPrefix(ct, "text/"):
		return true
	case strings.Contains(ct, "json"), strings.Contains(ct, "javascript"),
		strings.Contains(ct, "xml"), strings.Contains(ct, "svg"):
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		// gzip;q=0 means "not acceptable".
		if q := strings.TrimSpace(params); strings.HasPrefix(q, "q=") && strings.Trim(q[2:], "0.") == "" {
			return false
		}
		return true
	}
	return false
}

// gzipMiddleware compresses compressible responses of at least gzipMinSize
// for clients that accept gzip. It never touches the SSE stream (which does
// its own per-flush gzip), the reverse-proxied pathscope mount, HEAD or Range
// requests, upgrades, or responses that already carry a Content-Encoding
// (e.g. the pre-gzipped /api/history bundles).
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.URL.Path == "/api/stream" ||
			strings.HasPrefix(r.URL.Path, "/pathscope/") ||
			r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" ||
			!acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter defers the compress/pass-through decision until it has
// seen the headers and the first gzipMinSize bytes of the body.
type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	compress    bool
	buf         []byte
	gz          *gzip.Writer
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	g.status = code
	h := g.Header()
	// Decide now when the headers alone rule out compression.
	if h.Get("Content-Encoding") != "" || code < 200 || code == http.StatusNoContent ||
		code == http.StatusNotModified {
		g.passThrough()
		return
	}
	if ct := h.Get("Content-Type"); ct != "" && !gzipCompressibleType(ct) {
		g.passThrough()
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, ok := parseContentLength(cl); ok && n < gzipMinSize {
			g.passThrough()
		}
	}
}

func parseContentLength(s string) (int64, bool) {
	var n int64
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
}

// passThrough sends the header and any buffered body unmodified.
func (g *gzipResponseWriter) passThrough() {
	g.decided = true
	g.ResponseWriter.WriteHeader(g.status)
	if len(g.buf) > 0 {
		g.ResponseWriter.Write(g.buf)
		g.buf = nil
	}
}

func (g *gzipResponseWriter) startCompress() {
	g.decided, g.compress = true, true
	h := g.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	// A strong validator no longer describes the coded bytes.
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
	g.ResponseWriter.WriteHeader(g.status)
	g.gz = gzipWriterPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
	if len(g.buf) > 0 {
		g.gz.Write(g.buf)
		g.buf = nil
	}
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.decided {
		if g.compress {
			return g.gz.Write(p)
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf = append(g.buf, p...)
	if len(g.buf) < gzipMinSize {
		return len(p), nil
	}
	ct := g.Header().Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(g.buf)
		g.Header().Set("Content-Type", ct)
	}
	if gzipCompressibleType(ct) {
		g.startCompress()
	} else {
		g.passThrough()
	}
	return len(p), nil
}

// Flush forces the decision: a handler that flushes is streaming, so the
// response passes through uncompressed (SSE-like bodies must not be held).
func (g *gzipResponseWriter) Flush() {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if !g.decided {
		g.passThrough()
	}
	if g.compress {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) close() {
	if !g.decided {
		if !g.wroteHeader {
			return // handler wrote nothing; net/http sends the implicit 200
		}
		g.passThrough() // short body: send as-is with its own Content-Length
		return
	}
	if g.compress {
		g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipWriterPool.Put(g.gz)
	}
}

// gzipStaticVariant is a precompressed embedded asset.
type gzipStaticVariant struct {
	body        []byte
	etag        string
	contentType string
}

// buildGzipStaticVariant compresses b when its type is compressible and the
// result is meaningfully smaller (under 90% of the original).
func buildGzipStaticVariant(name string, b []byte, etag string) (gzipStaticVariant, bool) {
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" || !gzipCompressibleType(ct) || len(b) < gzipMinSize {
		return gzipStaticVariant{}, false
	}
	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	if out.Len() >= len(b)*9/10 {
		return gzipStaticVariant{}, false
	}
	return gzipStaticVariant{
		body:        out.Bytes(),
		etag:        strings.TrimSuffix(etag, `"`) + `-gz"`,
		contentType: ct,
	}, true
}
