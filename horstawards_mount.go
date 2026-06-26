package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// horstawardsPublicPaths are the only horstawards endpoints exposed through the
// public reverse-proxy. Unlike horstprop (public propagation scores), horstawards
// serves the operator's personal award progress, so the mount is read-only:
// /v1/refresh is deliberately NOT proxied — refreshes are triggered
// server-internally (curl 127.0.0.1:9956 / systemd), never from the public host.
var horstawardsPublicPaths = map[string]bool{
	"/horstawards/v1/wanted": true, // POST: the agent's progress query
	"/horstawards/v1/health": true, // GET: status
}

// newHorstawardsProxy builds a reverse-proxy handler for the local horstawards
// award-progress service (default http://127.0.0.1:9956), mounted at
// /horstawards/. horstawards runs co-located on this server bound to localhost;
// this lets the operator's (local) agent reach it same-origin under
// HorstReporter's TLS while the service stays off the public internet. Thin
// passthrough; no award logic lives here. ok is false when no target is
// configured, so the mount is skipped.
func newHorstawardsProxy(target string) (http.Handler, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, false
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		logInfo("horstawards proxy disabled: invalid -horstawards-url %q (%v)", target, err)
		return nil, false
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// horstawards down/unreachable: fail soft so the Chase Queue still renders
		// (the agent treats a failed wanted-lookup as degraded, keeping spots).
		http.Error(w, "horstawards unavailable", http.StatusBadGateway)
	}
	// /horstawards/v1/wanted -> 127.0.0.1:9956/v1/wanted
	stripped := http.StripPrefix("/horstawards", proxy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !horstawardsPublicPaths[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	}), true
}
