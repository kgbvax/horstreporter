package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// newPathscopeProxy builds a reverse-proxy handler for the local
// pathscope scoring service (default http://127.0.0.1:9960), mounted at
// /pathscope/. Mirrors newHorstpropProxy.
//
// pathscope is operator-local and binds to localhost; this lets a browser
// reach it same-origin under HorstReporter's TLS (no CORS, no mixed
// content) while the service stays off the public internet. It is a
// thin read-only passthrough — it adds no scoring logic to
// HorstReporter (which stays small per the design). ok is false when no
// target is configured, so the mount is skipped.
//
// SSE note: pathscope exposes a Server-Sent Events stream at
// /api/pathscope/v1/stream that flushes per event. Go's ReverseProxy
// buffers writes by default; FlushInterval: -1 forces a flush after
// every upstream Write so the bytes reach the browser immediately. This
// is the documented way to make SSE flow through ReverseProxy; without
// it the browser sees a single chunk on disconnect.
func newPathscopeProxy(target string) (http.Handler, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, false
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		logInfo("pathscope proxy disabled: invalid -pathscope-url %q (%v)", target, err)
		return nil, false
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.FlushInterval = -1 // flush after every Write — required for SSE
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// pathscope down/unreachable: fail soft so the static UI still
		// renders its index.html and the JS can show a connection error
		// rather than a hard 502.
		http.Error(w, "pathscope unavailable", http.StatusBadGateway)
	}
	// /pathscope/app.js -> 127.0.0.1:9960/pathscope/app.js
	// /pathscope/api/pathscope/v1/stream -> 127.0.0.1:9960/pathscope/api/pathscope/v1/stream
	// The pathscope binary serves everything under /pathscope/* (static
	// assets AND an API mirror), so we forward the prefix as-is. No
	// StripPrefix — pathscope's own mux knows what to do.
	return proxy, true
}