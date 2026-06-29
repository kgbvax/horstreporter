package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// configField describes one operator-tunable setting surfaced in the tray
// Settings page. Each maps to a KEY=VALUE line in the agent's .env, which is the
// single source of truth: the env-backed flag defaults in main() read these, so
// saving + restarting applies them.
type configField struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`
	Help    string         `json:"help"`
	Kind    string         `json:"kind"` // text | number | bool | password | select
	Secret  bool           `json:"secret,omitempty"`
	Options []configOption `json:"options,omitempty"` // for kind == select
}

// configOption is one choice in a select-kind field.
type configOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// configFields is the ordered set of keys the Settings page manages. Anything
// not listed here is preserved verbatim when the .env is rewritten.
var configFields = []configField{
	{Key: "STATION_LOCATOR", Label: "Station locator", Help: "Maidenhead grid, e.g. JO62qm", Kind: "text"},
	{Key: "STATION_NAME", Label: "Station name", Help: "Display name", Kind: "text"},
	{Key: "PST_HOST", Label: "PSTrotator host", Help: "IP/hostname of the PSTrotator PC", Kind: "text"},
	{Key: "PST_PORT", Label: "PSTrotator UDP port", Help: "Default 12000", Kind: "number"},
	{Key: "CONTROL_PERMITTED", Label: "Allow control", Help: "Master switch for rotor + rig control. Off = monitor only", Kind: "bool"},
	{Key: "RIG_TRANSPORT", Label: "Rig control", Help: "How the agent tunes the radio. Log4OM = Remote Control over UDP; WaveLogGate = local HTTP callback.", Kind: "select", Options: []configOption{{"none", "Disabled"}, {"log4om", "Log4OM remote (UDP)"}, {"waveloggate", "WaveLogGate (HTTP)"}}},
	{Key: "RIG_LOG4OM_ADDR", Label: "Log4OM address", Help: "host:port of Log4OM's Inbound UDP (Connections \u2192 Inbound). Default " + log4omDefaultAddr, Kind: "text"},
	{Key: "RIG_WAVELOGGATE_URL", Label: "WaveLogGate URL", Help: "WaveLogGate tune callback base, e.g. http://127.0.0.1:54321", Kind: "text"},
	{Key: "ALLOWED_MODES", Label: "Allowed modes", Help: "Comma-separated: forward,backward,bidirectional", Kind: "text"},
	{Key: "BEAMWIDTH_3DB_DEG", Label: "Beamwidth (°)", Help: "Antenna 3 dB beamwidth shown in UI", Kind: "number"},
	{Key: "BACKEND_URL", Label: "Backend URL", Help: "HorstReporter server, e.g. https://horstreporter.kgbvax.net", Kind: "text"},
	{Key: "WAVELOG_API_KEY", Label: "Wavelog API key", Help: "Read key; enables enrichment + awards. Never leaves this PC.", Kind: "password", Secret: true},
	{Key: "WAVELOG_STATION_ID", Label: "Wavelog station id", Help: "Required for DCLNext award sources", Kind: "text"},
	{Key: "WAVELOG_URL", Label: "Wavelog URL", Help: "Default: " + defaultWavelogURL, Kind: "text"},
	{Key: "POTA_HUNTED_CSV", Label: "POTA hunted CSV", Help: "Path to a POTA hunted-parks export (optional)", Kind: "text"},
	{Key: "DEBUG_LOGGING", Label: "Debug logging", Help: "Log all traffic to external systems (PSTrotator UDP + Wavelog/backend/rig HTTP). Verbose; secrets redacted. Applied on restart.", Kind: "bool"},
}

// managedEnvKeys returns the set of keys the Settings page owns, used both when
// rewriting the .env and when building a clean child environment for restart.
func managedEnvKeys() map[string]struct{} {
	m := make(map[string]struct{}, len(configFields))
	for _, f := range configFields {
		m[f.Key] = struct{}{}
	}
	return m
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleConfigGet(w, r)
	case http.MethodPost:
		s.handleConfigPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	current := parseEnvFile(s.cfg.EnvFile)
	values := make(map[string]string, len(configFields))
	set := make(map[string]bool, len(configFields))
	for _, f := range configFields {
		v, ok := current[f.Key]
		if !ok {
			// Fall back to whatever the process actually resolved (e.g. a value
			// supplied via a flag rather than the file) so the page isn't blank.
			v = strings.TrimSpace(os.Getenv(f.Key))
			ok = v != ""
		}
		set[f.Key] = ok && v != ""
		if f.Secret {
			v = "" // never echo secrets back to the browser
		}
		values[f.Key] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fields":      configFields,
		"values":      values,
		"set":         set,
		"env_file":    s.cfg.EnvFile,
		"log_file":    s.cfg.LogFile,
		"can_restart": true,
	})
}

type configPostRequest struct {
	Values map[string]string `json:"values"`
}

func (s *server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	var req configPostRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	managed := managedEnvKeys()
	updates := make(map[string]string)
	for k, v := range req.Values {
		if _, ok := managed[k]; !ok {
			continue // ignore unknown keys
		}
		v = strings.TrimSpace(v)
		// A blank password field means "leave the stored secret unchanged".
		if v == "" && isSecretKey(k) {
			continue
		}
		updates[k] = v
	}

	if err := validateConfig(updates); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if err := upsertEnvFile(s.cfg.EnvFile, updates); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not write .env: " + err.Error()})
		return
	}
	log.Printf("[INFO] settings saved to %s (%d keys); restart required to apply", s.cfg.EnvFile, len(updates))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"saved":            len(updates),
		"restart_required": true,
	})
}

func (s *server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	// Respond first, then restart shortly after so the browser sees the 200.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		if err := restartAgent(s.cfg); err != nil {
			log.Printf("[ERROR] restart failed: %v", err)
		}
	}()
}

type pstTestRequest struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	TimeoutMs int    `json:"timeout_ms"`
}

// handlePSTTest does a single, throwaway AZ? query against the supplied
// host/port so the Settings page can validate connectivity BEFORE saving. It
// builds a temporary client and never mutates the running configuration.
func (s *server) handlePSTTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var req pstTestRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "host is required"})
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "port must be 1..65535"})
		return
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = s.cfg.Timeout
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	tmp := s.cfg
	tmp.PSTHost = host
	tmp.PSTPort = req.Port
	tmp.Timeout = timeout
	client := newPSTUDPClient(tmp)

	ctx, cancel := context.WithTimeout(r.Context(), timeout+250*time.Millisecond)
	defer cancel()

	reply, err := client.sendAndMaybeReceive(ctx, "test-az", pstQueryAzCommand, true)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       false,
			"endpoint": fmt.Sprintf("%s:%d", host, req.Port),
			"error":    err.Error(),
		})
		return
	}
	az, perr := parseAzimuth(reply)
	if perr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       false,
			"endpoint": fmt.Sprintf("%s:%d", host, req.Port),
			"reply":    reply,
			"error":    "got a reply but could not parse azimuth: " + perr.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"endpoint":    fmt.Sprintf("%s:%d", host, req.Port),
		"azimuth_deg": az,
	})
}

func isSecretKey(key string) bool {
	for _, f := range configFields {
		if f.Key == key {
			return f.Secret
		}
	}
	return false
}

func validateConfig(updates map[string]string) error {
	if v, ok := updates["STATION_LOCATOR"]; ok && v != "" {
		if _, err := normalizeLocator(v); err != nil {
			return err
		}
	}
	if v, ok := updates["PST_PORT"]; ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("PSTrotator UDP port must be 1..65535")
		}
	}
	if v, ok := updates["BEAMWIDTH_3DB_DEG"]; ok && v != "" {
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return fmt.Errorf("beamwidth must be a number")
		}
	}
	if v, ok := updates["CONTROL_PERMITTED"]; ok && v != "" {
		if _, err := strconv.ParseBool(v); err != nil {
			return fmt.Errorf("allow control must be true/false")
		}
	}
	if v, ok := updates["DEBUG_LOGGING"]; ok && v != "" {
		if _, err := strconv.ParseBool(v); err != nil {
			return fmt.Errorf("debug logging must be true/false")
		}
	}
	if v, ok := updates["RIG_TRANSPORT"]; ok && v != "" {
		switch strings.ToLower(v) {
		case "none", "waveloggate", "log4om":
		default:
			return fmt.Errorf("rig control must be none, waveloggate, or log4om")
		}
	}
	if v, ok := updates["RIG_LOG4OM_ADDR"]; ok && v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("Log4OM address must be host:port (e.g. %s)", log4omDefaultAddr)
		}
	}
	return nil
}

// parseEnvFile reads KEY=VALUE pairs from path for display. Quotes are stripped
// to mirror dotenv.Load. A missing file yields an empty map.
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

// upsertEnvFile updates the given keys in the .env at path, preserving every
// other line (comments, unmanaged keys, ordering). Keys not already present are
// appended. Values containing spaces or # are double-quoted. The write is atomic
// (temp file + rename).
func upsertEnvFile(path string, updates map[string]string) error {
	if path == "" {
		return fmt.Errorf("no .env path configured")
	}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	}

	seen := map[string]bool{}
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		body := strings.TrimPrefix(trimmed, "export ")
		eq := strings.IndexByte(body, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(body[:eq])
		if v, ok := updates[key]; ok {
			lines[i] = key + "=" + quoteEnvValue(v)
			seen[key] = true
		}
	}

	// Append any keys that weren't already in the file, in stable key order.
	var missing []string
	for k := range updates {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		lines = append(lines, k+"="+quoteEnvValue(updates[k]))
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func quoteEnvValue(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, " \t#\"'") {
		return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return v
}

// restartEnviron returns the current environment with the Settings-managed keys
// removed, so a re-exec'd child re-reads them fresh from the (just-saved) .env
// instead of inheriting the old in-process values.
func restartEnviron() []string {
	managed := managedEnvKeys()
	var out []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 {
			if _, ok := managed[kv[:eq]]; ok {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}
