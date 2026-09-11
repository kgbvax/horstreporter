package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"horstreporter/internal/awards"
)

// startUDPAzimuthResponder binds a UDP socket on 127.0.0.1:listenPort (a
// port reserved via unusedUDPPort) and answers every datagram with reply sent
// to 127.0.0.1:replyPort — mirroring PSTrotator, which answers the AZ? poll on
// the client's reply port (PSTPort+1), not on the datagram's source port.
func startUDPAzimuthResponder(t *testing.T, listenPort, replyPort int, reply string) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(listenPort))
	if err != nil {
		t.Fatalf("bind UDP responder: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: replyPort}
	go func() {
		buf := make([]byte, 2048)
		for {
			_, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo([]byte(reply), dst)
		}
	}()
}

// unusedUDPPort returns a loopback UDP port that is (almost certainly) closed,
// for exercising unreachable-endpoint failure paths quickly.
func unusedUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe UDP port: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port
}

// newDiagServer builds a server for diagnostics/config handler tests. env (may
// be nil) is written to a temp .env; pstPort 0 points the PSTrotator client at
// a closed loopback port. rig/wavelog/awards are left nil (unconfigured).
func newDiagServer(t *testing.T, env map[string]string, pstPort int) *server {
	t.Helper()
	envPath := filepath.Join(t.TempDir(), ".env")
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		b.WriteString(k + "=" + env[k] + "\n")
	}
	if err := os.WriteFile(envPath, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	cfg := serviceConfig{
		EnvFile:      envPath,
		Timeout:      400 * time.Millisecond,
		PSTHost:      "127.0.0.1",
		PSTPort:      pstPort,
		RigTransport: "none",
	}
	s := &server{cfg: cfg}
	s.client = newPSTUDPClient(cfg)
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findCheck returns the check with the given name.
func findCheck(t *testing.T, checks []diagCheck, name string) diagCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %+v", name, checks)
	return diagCheck{}
}

func TestHandleDiagnostics_MethodGate(t *testing.T) {
	s := newDiagServer(t, nil, unusedUDPPort(t))
	rec := httptest.NewRecorder()
	s.handleDiagnostics(rec, httptest.NewRequest(http.MethodPost, "/v1/diagnostics", strings.NewReader(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status %d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow header %q, want GET", got)
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Errorf("body %q", rec.Body.String())
	}
}

// Characterization: with every upstream unset (and PSTrotator unreachable), the
// report still renders — only the Required pstrotator check fails, everything
// unconfigured is reported as "not set"/"disabled" and does not affect readiness.
func TestHandleDiagnostics_UnconfiguredUpstreams(t *testing.T) {
	s := newDiagServer(t, nil, unusedUDPPort(t))
	rec := httptest.NewRecorder()
	s.handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/v1/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Ready  bool        `json:"ready"`
		Checks []diagCheck `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.Ready {
		t.Error("ready=true although the required PSTrotator check failed")
	}
	byName := map[string]diagCheck{}
	for _, c := range resp.Checks {
		byName[c.Name] = c
	}
	for _, name := range []string{"pstrotator", "backend", "wavelog", "rig", "awards", "pota_csv"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing check %q in %+v", name, resp.Checks)
		}
	}
	if c := byName["pstrotator"]; !c.Required || c.OK {
		t.Errorf("pstrotator = %+v, want Required && !OK (unreachable)", c)
	}
	if c := byName["backend"]; c.Configured || !strings.Contains(c.Detail, "not set") {
		t.Errorf("backend = %+v, want unconfigured \"not set\"", c)
	}
	if c := byName["wavelog"]; c.Configured || !strings.Contains(c.Detail, "WAVELOG_API_KEY") {
		t.Errorf("wavelog = %+v", c)
	}
	if c := byName["rig"]; c.Configured || !strings.Contains(c.Detail, "disabled") {
		t.Errorf("rig = %+v, want disabled transport detail", c)
	}
	if c := byName["awards"]; c.Configured || !strings.Contains(c.Detail, "not set") {
		t.Errorf("awards = %+v", c)
	}
	if c := byName["pota_csv"]; c.Configured || !strings.Contains(c.Detail, "POTA_HUNTED_CSV") {
		t.Errorf("pota_csv = %+v", c)
	}
}

// Characterization: optional systems that are NOT configured never fail
// readiness — once the only Required check (PSTrotator) answers, ready=true.
func TestHandleDiagnostics_ReadyWhenOptionalUnset(t *testing.T) {
	listen := unusedUDPPort(t)
	startUDPAzimuthResponder(t, listen, listen+1, "AZ=180")
	s := newDiagServer(t, nil, listen)
	rec := httptest.NewRecorder()
	s.handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/v1/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Ready  bool        `json:"ready"`
		Checks []diagCheck `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Ready {
		t.Fatalf("ready=false, checks=%+v", resp.Checks)
	}
	c := findCheck(t, resp.Checks, "pstrotator")
	if !c.OK {
		t.Errorf("pstrotator not OK: %+v", c)
	}
	if !strings.Contains(c.Detail, "azimuth 180") {
		t.Errorf("pstrotator detail %q, want azimuth echo", c.Detail)
	}
}

func TestEffectiveConfig_OverlaysEnvAndFlagsPending(t *testing.T) {
	env := map[string]string{
		"BACKEND_URL":         "https://new.example.org/", // trailing slash trimmed
		"PST_HOST":            "10.0.0.5",
		"PST_PORT":            "12001",
		"WAVELOG_API_KEY":     "env-key",
		"WAVELOG_URL":         "https://wl.example.org/index.php",
		"POTA_HUNTED_CSV":     "/tmp/parks.csv",
		"RIG_TRANSPORT":       "waveloggate",
		"RIG_LOG4OM_ADDR":     "127.0.0.1:2236",
		"RIG_WAVELOGGATE_URL": "http://127.0.0.1:54321",
	}
	s := newDiagServer(t, env, 12000)
	s.cfg.BackendBaseURL = "https://old.example.org"
	s.cfg.WavelogAPIKey = "run-key"

	cfg, pending := s.effectiveConfig()
	if cfg.BackendBaseURL != "https://new.example.org" {
		t.Errorf("BackendBaseURL = %q, want trailing slash trimmed", cfg.BackendBaseURL)
	}
	if cfg.PSTHost != "10.0.0.5" || cfg.PSTPort != 12001 {
		t.Errorf("pst = %s:%d, want 10.0.0.5:12001", cfg.PSTHost, cfg.PSTPort)
	}
	if cfg.WavelogAPIKey != "env-key" || cfg.WavelogURL != "https://wl.example.org/index.php" {
		t.Errorf("wavelog creds not overlaid from .env: url=%q", cfg.WavelogURL)
	}
	if cfg.RigTransport != "waveloggate" || cfg.RigLog4OM != "127.0.0.1:2236" {
		t.Errorf("rig config not overlaid: %q / %q", cfg.RigTransport, cfg.RigLog4OM)
	}
	if cfg.Awards.POTAHuntedCSV != "/tmp/parks.csv" {
		t.Errorf("POTA csv = %q", cfg.Awards.POTAHuntedCSV)
	}
	for _, k := range []string{"backend", "pstrotator", "wavelog", "pota_csv", "rig"} {
		if !pending[k] {
			t.Errorf("pending[%q] = false, want true (saved value differs from running)", k)
		}
	}
	if len(pending) != 5 {
		t.Errorf("pending = %v, want exactly the five changed groups", pending)
	}
}

// The .env is the source of truth: values identical to the running config must
// NOT be flagged as restart-pending, and an unparseable PST_PORT is ignored.
func TestEffectiveConfig_NoPendingWhenEnvMatches(t *testing.T) {
	env := map[string]string{
		"BACKEND_URL": "https://old.example.org",
		"PST_PORT":    "not-a-number", // ignored
		"PST_HOST":    "127.0.0.1",
	}
	s := newDiagServer(t, env, 12000)
	s.cfg.BackendBaseURL = "https://old.example.org"

	cfg, pending := s.effectiveConfig()
	if cfg.PSTPort != 12000 {
		t.Errorf("invalid PST_PORT applied: %d, want running value kept", cfg.PSTPort)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %v, want none", pending)
	}
}

func TestWithPending(t *testing.T) {
	if got := withPending("reachable", false); got != "reachable" {
		t.Errorf("got %q", got)
	}
	got := withPending("reachable", true)
	if !strings.Contains(got, "reachable") || !strings.Contains(got, "restart to apply") {
		t.Errorf("got %q, want hint appended", got)
	}
}

func TestCheckBackend(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stats" {
			t.Errorf("probe path %q, want /api/stats", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ok.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	t.Run("not configured", func(t *testing.T) {
		c := (&server{}).checkBackend(context.Background(), serviceConfig{}, nil)
		if c.OK || c.Configured || !strings.Contains(c.Detail, "not set") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("reachable", func(t *testing.T) {
		c := (&server{}).checkBackend(context.Background(), serviceConfig{BackendBaseURL: ok.URL}, nil)
		if !c.OK || !strings.Contains(c.Detail, "HTTP 200") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("http 500 fails", func(t *testing.T) {
		c := (&server{}).checkBackend(context.Background(), serviceConfig{BackendBaseURL: failing.URL}, nil)
		if c.OK || !strings.Contains(c.Detail, "HTTP 500") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("pending hint", func(t *testing.T) {
		c := (&server{}).checkBackend(context.Background(), serviceConfig{BackendBaseURL: ok.URL}, map[string]bool{"backend": true})
		if !c.OK || !strings.Contains(c.Detail, "restart to apply") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		c := (&server{}).checkBackend(context.Background(), serviceConfig{BackendBaseURL: "http://127.0.0.1:1"}, nil)
		if c.OK || c.Detail == "" {
			t.Errorf("got %+v, want error detail", c)
		}
	})
}

func TestCheckWavelog(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wavelogLookup{Callsign: "DL0HRP", DXCC: "GERMANY"})
	}))
	defer ok.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // bad key
	}))
	defer failing.Close()

	t.Run("not configured", func(t *testing.T) {
		c := (&server{}).checkWavelog(context.Background(), serviceConfig{}, nil)
		if c.OK || c.Configured || !strings.Contains(c.Detail, "not set") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("key accepted", func(t *testing.T) {
		s := &server{wavelog: newWavelogClient(ok.URL, "k")}
		c := s.checkWavelog(context.Background(), serviceConfig{WavelogURL: ok.URL, WavelogAPIKey: "k"}, nil)
		if !c.OK || !strings.Contains(c.Detail, "key accepted") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("rejected key fails check", func(t *testing.T) {
		s := &server{wavelog: newWavelogClient(failing.URL, "k")}
		c := s.checkWavelog(context.Background(), serviceConfig{WavelogURL: failing.URL, WavelogAPIKey: "k"}, nil)
		if c.OK || c.Detail == "" {
			t.Errorf("got %+v, want failure detail", c)
		}
	})
	// pending["wavelog"] forces a throwaway client so a just-saved key is probed
	// before the restart — the OK detail must carry the restart hint.
	t.Run("pending probes saved key", func(t *testing.T) {
		s := &server{wavelog: newWavelogClient(ok.URL, "k")}
		c := s.checkWavelog(context.Background(), serviceConfig{WavelogURL: ok.URL, WavelogAPIKey: "k"}, map[string]bool{"wavelog": true})
		if !c.OK || !strings.Contains(c.Detail, "restart to apply") {
			t.Errorf("got %+v", c)
		}
	})
}

func TestCheckRig(t *testing.T) {
	t.Run("transport none disabled", func(t *testing.T) {
		for _, transport := range []string{"", "none"} {
			c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: transport}, nil)
			if c.OK || c.Configured || !strings.Contains(c.Detail, "disabled") {
				t.Errorf("transport %q: got %+v", transport, c)
			}
		}
	})
	t.Run("waveloggate reachable", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer mock.Close()
		c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: "waveloggate", RigWaveLogGate: mock.URL}, nil)
		if !c.OK || !c.Configured || !strings.Contains(c.Detail, "HTTP 200") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("waveloggate 404 means server active", func(t *testing.T) {
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer mock.Close()
		c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: "waveloggate", RigWaveLogGate: mock.URL}, nil)
		if !c.OK || !strings.Contains(c.Detail, "server active") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("waveloggate unreachable", func(t *testing.T) {
		c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: "waveloggate", RigWaveLogGate: "http://127.0.0.1:1"}, nil)
		if c.OK || !strings.HasPrefix(c.Detail, "WaveLogGate: ") {
			t.Errorf("got %+v", c)
		}
	})
	// Slow path: binds the Log4OM reply port and waits out its 1.2s read window.
	t.Run("log4om no readback", func(t *testing.T) {
		port := unusedUDPPort(t)
		c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: "log4om", RigLog4OM: "127.0.0.1:" + strconv.Itoa(port)}, nil)
		if c.OK || !c.Configured {
			t.Errorf("got %+v, want Configured && !OK", c)
		}
		if !strings.Contains(c.Detail, "no readback") {
			t.Errorf("detail %q, want no-readback note", c.Detail)
		}
	})
	t.Run("unknown transport", func(t *testing.T) {
		c := (&server{}).checkRig(context.Background(), serviceConfig{RigTransport: "rigctld"}, nil)
		if c.OK || !strings.Contains(c.Detail, "unknown transport rigctld") {
			t.Errorf("got %+v", c)
		}
	})
}

func TestCheckAwards(t *testing.T) {
	t.Run("no source configured", func(t *testing.T) {
		c := (&server{}).checkAwards(context.Background(), serviceConfig{}, nil)
		if c.OK || c.Configured || !strings.Contains(c.Detail, "not set") {
			t.Errorf("got %+v", c)
		}
	})
	t.Run("sources saved but engine missing prompts restart", func(t *testing.T) {
		cfg := serviceConfig{WavelogAPIKey: "k"}
		c := (&server{}).checkAwards(context.Background(), cfg, nil)
		if !strings.Contains(c.Detail, "sources saved") || !strings.Contains(c.Detail, "restart") {
			t.Errorf("got %+v", c)
		}
		cfg = serviceConfig{Awards: awards.Config{POTAHuntedCSV: "hunted.csv"}}
		c = (&server{}).checkAwards(context.Background(), cfg, nil)
		if !strings.Contains(c.Detail, "sources saved") {
			t.Errorf("got %+v", c)
		}
	})
	// A freshly built index with no snapshots is degraded — the check must say so.
	t.Run("degraded manager", func(t *testing.T) {
		cfg := awards.Defaults()
		cfg.DataDir = t.TempDir()
		mgr, err := awards.New(cfg)
		if err != nil {
			t.Fatalf("awards.New: %v", err)
		}
		s := &server{awards: mgr}
		c := s.checkAwards(context.Background(), serviceConfig{}, nil)
		if c.OK {
			t.Errorf("got %+v, want !OK (fresh index is degraded)", c)
		}
		if !strings.Contains(c.Detail, "degraded") {
			t.Errorf("detail %q", c.Detail)
		}
	})
}

func TestCheckPOTACSV(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "hunted.csv")
	if err := os.WriteFile(good, []byte("park,call\nUS-0001,K1ABC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		pending bool
		wantOK  bool
		wantIn  string
	}{
		{"not set", "", false, false, "not set"},
		{"missing file", filepath.Join(dir, "nope.csv"), false, false, "cannot read"},
		{"empty file", empty, false, false, "empty or a directory"},
		{"readable", good, false, true, "readable"},
		{"readable pending", good, true, true, "restart to apply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := serviceConfig{Awards: awardsConfigFor(tc.path)}
			pending := map[string]bool{}
			if tc.pending {
				pending["pota_csv"] = true
			}
			c := (&server{}).checkPOTACSV(context.Background(), cfg, pending)
			if c.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v (detail %q)", c.OK, tc.wantOK, c.Detail)
			}
			if !strings.Contains(c.Detail, tc.wantIn) {
				t.Errorf("detail %q, want %q", c.Detail, tc.wantIn)
			}
			if c.Configured != (tc.path != "") {
				t.Errorf("Configured = %v", c.Configured)
			}
		})
	}
}

// awardsConfigFor builds the nested awards config with just the CSV path.
func awardsConfigFor(path string) awards.Config {
	cfg := awards.Defaults()
	cfg.POTAHuntedCSV = path
	return cfg
}
