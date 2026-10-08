// Package proxy builds the same-origin reverse proxies that mount the
// operator-local scoring services (horstprop, pathscope) under HorstReporter's
// TLS endpoint. They are thin passthroughs and add no scoring logic.
package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// parseTarget returns the upstream URL for a configured target. A blank target
// means "not configured" and yields (nil, nil); a malformed one is an error.
func parseTarget(target, flagName string) (*url.URL, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, nil
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid -%s %q (%v)", flagName, target, err)
	}
	return u, nil
}

// Horstprop builds a reverse-proxy handler for the local horstprop
// scoring service (default http://127.0.0.1:9970), mounted at /horstprop/.
//
// horstprop is operator-local and binds to localhost; this lets a browser reach
// it same-origin under HorstReporter's TLS (no CORS, no mixed content) while the
// service stays off the public internet. It is a thin read-only passthrough — it
// adds no scoring logic to HorstReporter (which stays small per the design). The
// returned handler is nil (with a nil error) when no target is configured, so
// the mount is skipped; an invalid target is an error.
func Horstprop(target string) (http.Handler, error) {
	u, err := parseTarget(target, "horstprop-url")
	if u == nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// horstprop down/unreachable: fail soft so the Chase Queue can render
		// spots without scores rather than erroring hard.
		http.Error(w, "horstprop unavailable", http.StatusBadGateway)
	}
	// /horstprop/v1/score -> 127.0.0.1:9970/v1/score
	return http.StripPrefix("/horstprop", proxy), nil
}

// Pathscope builds a reverse-proxy handler for the local
// pathscope scoring service (default http://127.0.0.1:9960), mounted at
// /pathscope/. Mirrors Horstprop.
//
// pathscope is operator-local and binds to localhost; this lets a browser
// reach it same-origin under HorstReporter's TLS (no CORS, no mixed
// content) while the service stays off the public internet. It is a
// thin read-only passthrough — it adds no scoring logic to
// HorstReporter (which stays small per the design). The returned
// handler is nil (with a nil error) when no target is configured, so the mount
// is skipped; an invalid target is an error.
//
// SSE note: pathscope exposes a Server-Sent Events stream at
// /api/pathscope/v1/stream that flushes per event. Go's ReverseProxy
// buffers writes by default; FlushInterval: -1 forces a flush after
// every upstream Write so the bytes reach the browser immediately. This
// is the documented way to make SSE flow through ReverseProxy; without
// it the browser sees a single chunk on disconnect.
func Pathscope(target string) (http.Handler, error) {
	u, err := parseTarget(target, "pathscope-url")
	if u == nil {
		return nil, err
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
	return proxy, nil
}
