package main

import (
	"flag"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Thin reverse proxy to the horstvideo sidecar (cmd/horstvideo) — the video
// renderer is a separate binary (the sanctioned sidecar pattern, like
// horstprop) so the core never carries chromium or ffmpeg; this only forwards
// job submit/status/download. Disabled when -video-service-url is empty.
var videoServiceURL = flag.String("video-service-url", "http://127.0.0.1:9961", "horstvideo sidecar base URL; empty disables /api/video/")

var (
	videoProxyOnce sync.Once
	videoProxyInst *httputil.ReverseProxy
)

func getVideoProxy() *httputil.ReverseProxy {
	videoProxyOnce.Do(func() {
		raw := strings.TrimSpace(*videoServiceURL)
		if raw == "" {
			return
		}
		target, err := url.Parse(raw)
		if err != nil {
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		// The job API is trivial; don't hang forever if the sidecar dies
		// mid-request. Long-running work is job-poll, not one HTTP call.
		proxy.Transport = &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
		videoProxyInst = proxy
	})
	return videoProxyInst
}

// videoProxyHandler forwards /api/video/* to the horstvideo sidecar:
// /api/video/render -> /render, /api/video/job/{id} -> /job/{id},
// /api/video/file/{name} -> /file/{name}.
func videoProxyHandler(w http.ResponseWriter, r *http.Request) {
	proxy := getVideoProxy()
	if proxy == nil {
		http.Error(w, "video service not configured", http.StatusServiceUnavailable)
		return
	}
	r.URL.Host = ""
	r.URL.Scheme = ""
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/video")
	if !strings.HasPrefix(r.URL.Path, "/") {
		r.URL.Path = "/" + r.URL.Path
	}
	proxy.ServeHTTP(w, r)
}
