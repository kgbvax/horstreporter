package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// diagCheck is one external-dependency probe result. The Settings page renders
// these as a readiness panel so the operator can see, at a glance, which links
// are up without trawling logs.
type diagCheck struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Required   bool   `json:"required"`   // a failed required check blocks "ready"
	Configured bool   `json:"configured"` // false = not set up; ignored for readiness
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	LatencyMs  int64  `json:"latency_ms"`
}

func (s *server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	checks := s.runDiagnostics(r.Context())

	// "ready" = every REQUIRED check passes, and nothing you've CONFIGURED is
	// failing. Optional systems you haven't set up don't count against you.
	ready := true
	for _, c := range checks {
		if c.Required && !c.OK {
			ready = false
		}
		if c.Configured && !c.OK {
			ready = false
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ready":  ready,
		"checks": checks,
	})
}

// effectiveConfig overlays the persisted .env (which the Settings page writes and
// treats as the source of truth) on top of the running config, and reports which
// settings have been SAVED but not yet applied to the live process. This is why a
// value you just saved shows "set" in the form while the running process hasn't
// adopted it until a restart: diagnostics probes the saved value so readiness
// matches what you configured, and annotates anything still pending a restart.
func (s *server) effectiveConfig() (serviceConfig, map[string]bool) {
	cfg := s.cfg
	pending := map[string]bool{}
	env := parseEnvFile(s.cfg.EnvFile)
	get := func(key string) (string, bool) {
		v, ok := env[key]
		v = strings.TrimSpace(v)
		return v, ok && v != ""
	}
	if v, ok := get("BACKEND_URL"); ok {
		v = strings.TrimRight(v, "/")
		if v != cfg.BackendBaseURL {
			pending["backend"] = true
		}
		cfg.BackendBaseURL = v
	}
	if v, ok := get("PST_HOST"); ok {
		if v != cfg.PSTHost {
			pending["pstrotator"] = true
		}
		cfg.PSTHost = v
	}
	if v, ok := get("PST_PORT"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			if n != cfg.PSTPort {
				pending["pstrotator"] = true
			}
			cfg.PSTPort = n
		}
	}
	if v, ok := get("WAVELOG_URL"); ok {
		cfg.WavelogURL = v
	}
	if v, ok := get("WAVELOG_API_KEY"); ok {
		if v != cfg.WavelogAPIKey {
			pending["wavelog"] = true
		}
		cfg.WavelogAPIKey = v
	}
	if v, ok := get("POTA_HUNTED_CSV"); ok {
		if v != cfg.Awards.POTAHuntedCSV {
			pending["pota_csv"] = true
		}
		cfg.Awards.POTAHuntedCSV = v
	}
	if v, ok := get("RIG_TRANSPORT"); ok {
		if !strings.EqualFold(v, cfg.RigTransport) {
			pending["rig"] = true
		}
		cfg.RigTransport = v
	}
	if v, ok := get("RIG_LOG4OM_ADDR"); ok {
		if v != cfg.RigLog4OM {
			pending["rig"] = true
		}
		cfg.RigLog4OM = v
	}
	if v, ok := get("RIG_WAVELOGGATE_URL"); ok {
		if v != cfg.RigWaveLogGate {
			pending["rig"] = true
		}
		cfg.RigWaveLogGate = v
	}
	return cfg, pending
}

// withPending appends a "restart to apply" hint to an otherwise-OK detail when the
// probed value came from a saved-but-not-yet-applied .env edit.
func withPending(detail string, pending bool) string {
	if pending {
		return detail + " \u2014 saved, restart to apply"
	}
	return detail
}

// runDiagnostics probes all external systems concurrently (each with its own
// timeout) so the whole report returns in roughly the slowest single check.
func (s *server) runDiagnostics(ctx context.Context) []diagCheck {
	eff, pending := s.effectiveConfig()
	type probe func(context.Context, serviceConfig, map[string]bool) diagCheck
	probes := []probe{
		s.checkPSTrotator,
		s.checkBackend,
		s.checkWavelog,
		s.checkRig,
		s.checkAwards,
		s.checkPOTACSV,
	}
	results := make([]diagCheck, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p probe) {
			defer wg.Done()
			results[i] = p(ctx, eff, pending)
		}(i, p)
	}
	wg.Wait()
	return results
}

func timedDetail(start time.Time) int64 {
	return time.Since(start).Round(time.Millisecond).Milliseconds()
}

// checkPSTrotator reuses the live client (which serialises UDP via its mutex, so
// it never collides with the background poller's reply-port listener). Probing a
// just-changed host/port would need a second listener on the same reply port, so
// we keep the live client and flag the change as restart-pending instead.
func (s *server) checkPSTrotator(ctx context.Context, cfg serviceConfig, pending map[string]bool) diagCheck {
	c := diagCheck{Name: "pstrotator", Label: "PSTrotator rotator", Required: true, Configured: cfg.PSTHost != ""}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout+250*time.Millisecond)
	defer cancel()
	reply, err := s.client.sendAndMaybeReceive(cctx, "diag-az", pstQueryAzCommand, true)
	c.LatencyMs = timedDetail(start)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	az, perr := parseAzimuth(reply)
	if perr != nil {
		c.Detail = "reply received but azimuth unparseable: " + reply
		return c
	}
	c.OK = true
	c.Detail = withPending(fmt.Sprintf("reachable \u2014 azimuth %.0f\u00b0", az), pending["pstrotator"])
	return c
}

func (s *server) checkBackend(ctx context.Context, cfg serviceConfig, pending map[string]bool) diagCheck {
	c := diagCheck{Name: "backend", Label: "HorstReporter backend", Configured: cfg.BackendBaseURL != ""}
	if !c.Configured {
		c.Detail = "not set (BACKEND_URL)"
		return c
	}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// /api/stats is a cheap JSON endpoint; fall back to the base if it 404s.
	target := strings.TrimRight(cfg.BackendBaseURL, "/") + "/api/stats"
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	resp, err := http.DefaultClient.Do(req)
	c.LatencyMs = timedDetail(start)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		c.Detail = fmt.Sprintf("HTTP %d from %s", resp.StatusCode, target)
		return c
	}
	c.OK = true
	c.Detail = withPending(fmt.Sprintf("reachable \u2014 HTTP %d", resp.StatusCode), pending["backend"])
	return c
}

func (s *server) checkWavelog(ctx context.Context, cfg serviceConfig, pending map[string]bool) diagCheck {
	c := diagCheck{Name: "wavelog", Label: "Wavelog API", Configured: strings.TrimSpace(cfg.WavelogAPIKey) != ""}
	if !c.Configured {
		c.Detail = "not set (WAVELOG_API_KEY)"
		return c
	}
	// Probe the saved key/url even before a restart so the panel reflects what you
	// configured. Reuse the live client when nothing relevant changed.
	client := s.wavelog
	if client == nil || pending["wavelog"] {
		client = newWavelogClient(cfg.WavelogURL, cfg.WavelogAPIKey)
	}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// A throwaway lookup verifies reachability AND that the key is accepted.
	_, err := client.Lookup(cctx, "DL0HRP", "20m", "FT8")
	c.LatencyMs = timedDetail(start)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	c.OK = true
	c.Detail = withPending("reachable \u2014 key accepted", pending["wavelog"] || s.wavelog == nil)
	return c
}

func (s *server) checkRig(ctx context.Context, cfg serviceConfig, pending map[string]bool) diagCheck {
	transport := strings.ToLower(strings.TrimSpace(cfg.RigTransport))
	c := diagCheck{Name: "rig", Label: "Rig control"}
	switch transport {
	case "", "none":
		c.Detail = withPending("disabled (RIG_TRANSPORT=none)", pending["rig"])
		return c
	case "waveloggate":
		c.Configured = true
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		// Any HTTP response means the local callback server is up; a 404 is fine.
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, cfg.RigWaveLogGate, nil)
		resp, err := http.DefaultClient.Do(req)
		c.LatencyMs = timedDetail(start)
		if err != nil {
			c.Detail = "WaveLogGate: " + err.Error()
			return c
		}
		resp.Body.Close()
		c.OK = true
		var statusText string
		if resp.StatusCode == http.StatusNotFound {
			statusText = "WaveLogGate reachable \u2014 server active"
		} else {
			statusText = fmt.Sprintf("WaveLogGate reachable \u2014 HTTP %d", resp.StatusCode)
		}
		c.Detail = withPending(statusText, pending["rig"])
		return c
	case "log4om":
		c.Configured = true
		addr := strings.TrimSpace(cfg.RigLog4OM)
		if addr == "" {
			addr = log4omDefaultAddr
		}
		// Reuse the live backend when it is already this Log4OM (so its mutex
		// serialises the reply-port bind against a concurrent tune); otherwise
		// (transport changed but not yet restarted) probe a throwaway one.
		be, _ := s.rig.(*log4omBackend)
		if be == nil || pending["rig"] || be.addr != addr {
			be = newLog4OMBackend(addr)
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		start := time.Now()
		err := be.ping(cctx)
		c.LatencyMs = timedDetail(start)
		if err != nil {
			c.Detail = withPending(fmt.Sprintf("Log4OM %s \u2014 no readback on port %d (%v)", be.addr, be.replyPort, err), pending["rig"])
			return c
		}
		c.OK = true
		c.Detail = withPending(fmt.Sprintf("Log4OM %s \u2014 alive, replies on port %d", be.addr, be.replyPort), pending["rig"])
		return c
	default:
		c.Configured = true
		c.Detail = "unknown transport " + transport
		return c
	}
}

func (s *server) checkAwards(_ context.Context, cfg serviceConfig, _ map[string]bool) diagCheck {
	c := diagCheck{Name: "awards", Label: "Award engine (local log)", Configured: s.awards != nil}
	if s.awards == nil {
		// Sources may be saved but the engine is only built at startup, so prompt a
		// restart rather than silently showing "not set" after you configured them.
		if strings.TrimSpace(cfg.WavelogAPIKey) != "" || strings.TrimSpace(cfg.Awards.POTAHuntedCSV) != "" {
			c.Detail = "sources saved \u2014 restart to load the index"
			return c
		}
		c.Detail = "not set (needs Wavelog or POTA source)"
		return c
	}
	if s.awards.Degraded() {
		detail := "degraded \u2014 index stale or empty"
		if reason := s.awards.Diagnostic(); reason != "" {
			detail += " (" + reason + ")"
		} else {
			detail += " (check Wavelog station id / sources)"
		}
		c.Detail = detail
		return c
	}
	c.OK = true
	c.Detail = "index loaded (sources: " + s.awards.SourceLabel() + ")"
	return c
}

func (s *server) checkPOTACSV(_ context.Context, cfg serviceConfig, pending map[string]bool) diagCheck {
	path := strings.TrimSpace(cfg.Awards.POTAHuntedCSV)
	c := diagCheck{Name: "pota_csv", Label: "POTA hunted CSV", Configured: path != ""}
	if !c.Configured {
		c.Detail = "not set (POTA_HUNTED_CSV)"
		return c
	}
	info, err := os.Stat(path)
	if err != nil {
		c.Detail = "cannot read: " + err.Error()
		return c
	}
	if info.IsDir() || info.Size() == 0 {
		c.Detail = "file is empty or a directory: " + path
		return c
	}
	c.OK = true
	c.Detail = withPending(fmt.Sprintf("readable \u2014 %d bytes", info.Size()), pending["pota_csv"])
	return c
}
