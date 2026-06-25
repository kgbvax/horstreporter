package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// newHorstpropProxy builds a reverse-proxy handler for the local horstprop
// scoring service (default http://127.0.0.1:9970), mounted at /horstprop/.
//
// horstprop is operator-local and binds to localhost; this lets a browser reach
// it same-origin under HorstReporter's TLS (no CORS, no mixed content) while the
// service stays off the public internet. It is a thin read-only passthrough — it
// adds no scoring logic to HorstReporter (which stays small per the design). ok
// is false when no target is configured, so the mount is skipped.
func newHorstpropProxy(target string) (http.Handler, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, false
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		logInfo("horstprop proxy disabled: invalid -horstprop-url %q (%v)", target, err)
		return nil, false
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// horstprop down/unreachable: fail soft so the Chase Queue can render
		// spots without scores rather than erroring hard.
		http.Error(w, "horstprop unavailable", http.StatusBadGateway)
	}
	// /horstprop/v1/score -> 127.0.0.1:9970/v1/score
	return http.StripPrefix("/horstprop", proxy), true
}
