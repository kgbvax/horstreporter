//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	_ "embed"

	"fyne.io/systray"
)

//go:embed icon_running.ico
var iconRunning []byte

//go:embed icon_error.ico
var iconError []byte

//go:embed icon_warn.ico
var iconWarn []byte

// serveAgent on Windows starts the HTTP server in the background and runs the
// systray event loop on the main goroutine (systray.Run must own main). The
// tray icon turns green when the PSTrotator poll is healthy and red otherwise,
// giving the operator an at-a-glance "is it running" indicator.
//
// When -tray is not set we keep the original headless behaviour so the agent
// can still run under a service manager / scheduled task without a desktop.
func serveAgent(cfg serviceConfig, srv *server, handler http.Handler, tray bool) error {
	if !tray {
		return serveHTTP(cfg, handler)
	}

	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: handler}
	serveErr := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	// If the listener fails immediately (e.g. port in use), surface it instead
	// of showing a tray icon for a dead server.
	select {
	case err := <-serveErr:
		return err
	case <-time.After(250 * time.Millisecond):
	}

	openTarget := openURL(cfg)

	onReady := func() {
		systray.SetIcon(iconError)
		systray.SetTitle("HorstOperator")
		systray.SetTooltip("HorstOperator agent: starting\u2026")

		mStatus := systray.AddMenuItem("Starting\u2026", "Current PSTrotator poll status")
		mStatus.Disable()
		mListen := systray.AddMenuItem("Listening on "+cfg.ListenAddr, "Local agent HTTP address")
		mListen.Disable()
		systray.AddSeparator()
		mOpen := systray.AddMenuItem("Open opmode UI", "Open the HorstReporter operator UI in your browser")
		mSettings := systray.AddMenuItem("Settings…", "Edit station / PSTrotator / Wavelog settings in your browser")
		mDiag := systray.AddMenuItem("Diagnostics…", "Check all external connections and overall readiness")
		mLogTraffic := systray.AddMenuItemCheckbox("Log UDP traffic", "Toggle PSTrotator UDP TX/RX logging", cfg.UDPLogTraffic)
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "Stop the agent and remove the tray icon")

		// Menu click handlers.
		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if err := browseURL(openTarget); err != nil {
						log.Printf("[WARN] could not open browser: %v", err)
					}
				case <-mSettings.ClickedCh:
					if err := browseURL(settingsURL(cfg)); err != nil {
						log.Printf("[WARN] could not open settings page: %v", err)
					}
				case <-mDiag.ClickedCh:
					if err := browseURL(settingsURL(cfg)); err != nil {
						log.Printf("[WARN] could not open diagnostics page: %v", err)
					}
				case <-mLogTraffic.ClickedCh:
					// Toggle is advisory: it flips the live flag the UDP client
					// reads, so diagnostics can be enabled without a restart.
					srv.cfg.UDPLogTraffic = !srv.cfg.UDPLogTraffic
					srv.client.cfg.UDPLogTraffic = srv.cfg.UDPLogTraffic
					if srv.cfg.UDPLogTraffic {
						mLogTraffic.Check()
						log.Printf("[INFO] UDP traffic logging enabled via tray")
					} else {
						mLogTraffic.Uncheck()
						log.Printf("[INFO] UDP traffic logging disabled via tray")
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()

		// Readiness loop: periodically probe the optional external links so the
		// icon can go amber when something you've configured (Wavelog, backend, …)
		// is down even though the rotator itself is fine. Slower cadence because it
		// makes real network calls; the required PSTrotator link is covered by the
		// fast Health() loop below.
		var optionalDegraded atomic.Bool
		go func() {
			first := true
			prevReady := true
			for {
				checks := srv.runDiagnostics(context.Background())
				degraded := false
				ready := true
				var down []string
				for _, c := range checks {
					if c.Required && !c.OK {
						ready = false
					}
					if c.Configured && !c.OK {
						ready = false
						down = append(down, c.Label)
						if !c.Required {
							degraded = true
						}
					}
				}
				optionalDegraded.Store(degraded)

				// Notify only on a transition (and not on the first probe), so the
				// operator gets a toast the moment readiness changes without spam.
				if first {
					first = false
					prevReady = ready
				} else if ready != prevReady {
					if ready {
						notify("HorstOperator: ready", "All configured connections are up.")
					} else {
						notify("HorstOperator: attention needed", "Down: "+strings.Join(down, ", "))
					}
					prevReady = ready
				}
				time.Sleep(30 * time.Second)
			}
		}()

		// Health refresh loop: recolour the icon + update tooltip/menu.
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				ok, detail := srv.Health()
				switch {
				case !ok:
					systray.SetIcon(iconError)
				case optionalDegraded.Load():
					systray.SetIcon(iconWarn)
					detail += " (some configured links down — see Diagnostics)"
				default:
					systray.SetIcon(iconRunning)
				}
				systray.SetTooltip("HorstOperator agent: " + detail)
				mStatus.SetTitle(statusLabel(ok, detail))
				<-ticker.C
			}
		}()
	}

	onExit := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}

	// systray.Run blocks until systray.Quit is called.
	systray.Run(onReady, onExit)

	// Drain any late listener error so the caller can report it.
	select {
	case err := <-serveErr:
		return err
	default:
		return nil
	}
}

func statusLabel(ok bool, detail string) string {
	if ok {
		return "\u2714 " + detail
	}
	return "\u26a0 " + detail
}

// openURL is what the tray's "Open opmode UI" opens. It is ALWAYS the agent's own
// local address, never the backend URL directly: the agent reverse-proxies the
// backend's operator UI at this same origin, so the page's /v1/* calls (rotor
// control, enrich) reach the LOCAL agent. Opening the backend origin directly
// would load the UI but leave those operator calls unable to find the agent.
func openURL(cfg serviceConfig) string {
	return localBase(cfg) + "/"
}

// settingsURL always points at the agent's own config page on its loopback
// listen address (the page talks to /v1/config on the same origin).
func settingsURL(cfg serviceConfig) string {
	return localBase(cfg) + "/config"
}

// localBase normalizes the agent's listen address into a browser-reachable
// http://host:port base, mapping wildcard binds onto loopback.
func localBase(cfg serviceConfig) string {
	addr := cfg.ListenAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	} else if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return "http://" + addr
}

func browseURL(target string) error {
	if target == "" {
		return fmt.Errorf("no URL to open")
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
}
