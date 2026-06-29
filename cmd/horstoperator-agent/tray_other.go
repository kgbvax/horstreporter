//go:build !windows

package main

import (
	"log"
	"net/http"
)

// serveAgent on non-Windows platforms always runs headless. The tray is a
// Windows-only convenience for the shack PC; if -tray is passed elsewhere we
// log a warning and serve normally so systemd/dev workflows are unaffected.
func serveAgent(cfg serviceConfig, _ *server, handler http.Handler, tray bool) error {
	if tray {
		log.Printf("[WARN] -tray is only supported on Windows; running headless")
	}
	return serveHTTP(cfg, handler)
}
