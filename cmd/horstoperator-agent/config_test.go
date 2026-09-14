package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidateConfigUltrabeam(t *testing.T) {
	t.Run("rejects enabled with empty broker", func(t *testing.T) {
		err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": ""})
		if err == nil {
			t.Fatal("expected error when UB_ENABLED=true and UB_BROKER_URL empty")
		}
	})

	t.Run("rejects enabled with invalid broker scheme", func(t *testing.T) {
		err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "http://localhost:1883"})
		if err == nil {
			t.Fatal("expected error for non-MQTT broker scheme")
		}
	})

	t.Run("accepts enabled with valid tcp broker", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "tcp://127.0.0.1:1883"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts tls broker", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "true", "UB_BROKER_URL": "tls://broker.example.org:8883"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts disabled regardless of broker fields", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "false", "UB_BROKER_URL": ""}); err != nil {
			t.Fatalf("unexpected error when disabled: %v", err)
		}
		if err := validateConfig(map[string]string{"UB_ENABLED": "false", "UB_BROKER_URL": "garbage"}); err != nil {
			t.Fatalf("unexpected error when disabled with junk broker: %v", err)
		}
	})

	t.Run("rejects non-bool enabled", func(t *testing.T) {
		if err := validateConfig(map[string]string{"UB_ENABLED": "yes"}); err == nil {
			t.Fatal("expected error for non-bool UB_ENABLED")
		}
	})
}

func TestUltrabeamTopicPrefixDefault(t *testing.T) {
	// Pure resolution — no broker connection (must not dial or leak goroutines).
	if got := resolveUltrabeamPrefix(serviceConfig{UBTopicPrefix: ""}); got != defaultUltrabeamTopicPrefix {
		t.Fatalf("prefix = %q, want %q", got, defaultUltrabeamTopicPrefix)
	}
	if got := resolveUltrabeamPrefix(serviceConfig{UBTopicPrefix: "shack/ub/"}); got != "shack/ub" {
		t.Fatalf("prefix = %q, want trimmed shack/ub", got)
	}
}

func TestUltrabeamPasswordNotInConfigFields(t *testing.T) {
	// UB_PASSWORD is a Secret field; confirm it is marked Secret so it is never
	// echoed back in the Settings page payload (sourced from env only).
	for _, f := range configFields {
		if f.Key == "UB_PASSWORD" {
			if !f.Secret {
				t.Fatal("UB_PASSWORD must be marked Secret")
			}
			return
		}
	}
	t.Fatal("UB_PASSWORD field not found in configFields")
}

func TestRestartArgs(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{
		"./horstoperator-agent",
		"-listen", "127.0.0.1:9955",
		"-station-locator", "JO32we",
		"-ub-enabled",
		"-pst-host=127.0.0.1",
		"-dev",
	}

	got := restartArgs()
	want := []string{
		"./horstoperator-agent",
		"-listen", "127.0.0.1:9955",
		"-dev",
	}

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---- .env parsing / writing -------------------------------------------------

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file is empty", func(t *testing.T) {
		got := parseEnvFile(filepath.Join(dir, "absent.env"))
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("parses keys", func(t *testing.T) {
		path := filepath.Join(dir, "a.env")
		content := strings.Join([]string{
			"# comment line",
			"",
			"PLAIN=value",
			"  SPACED =  trimmed  ",
			`export EXPORTED=1`,
			`DOUBLE="quoted value"`,
			`SINGLE='single quoted'`,
			"HASHED=abc # not a trailing comment (kept verbatim)",
			"NO_EQUALS_SIGN",
			"=EMPTYKEY",
			`NESTED_QUOTES="has \"escape\""`,
		}, "\n")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got := parseEnvFile(path)
		want := map[string]string{
			"PLAIN":         "value",
			"SPACED":        "trimmed",
			"EXPORTED":      "1",
			"DOUBLE":        "quoted value",
			"SINGLE":        "single quoted",
			"HASHED":        "abc # not a trailing comment (kept verbatim)",
			"NESTED_QUOTES": `has \"escape\"`,
		}
		if len(got) != len(want) {
			t.Fatalf("got %d keys (%v), want %d", len(got), got, len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
	})
}

func TestUpsertEnvFile(t *testing.T) {
	t.Run("preserves comments, order and unmanaged keys", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		orig := "# my secrets live here\nUNMANAGED=keep\nPST_PORT=12000\nWAVELOG_API_KEY=oldkey\n"
		if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := upsertEnvFile(path, map[string]string{"PST_PORT": "13000", "STATION_LOCATOR": "JO62qm"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := string(data)
		for _, want := range []string{"# my secrets live here", "UNMANAGED=keep", "PST_PORT=13000", "WAVELOG_API_KEY=oldkey", "STATION_LOCATOR=JO62qm"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		if strings.Contains(got, "12000") {
			t.Errorf("old port still present:\n%s", got)
		}
		if !strings.HasSuffix(got, "\n") {
			t.Error("file does not end with a newline")
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Error("temp file left behind — write is not atomic-rename")
		}
	})

	t.Run("quotes values with special characters", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		// Embedded quotes are written as-is inside the wrapping quotes: the
		// loader strips only the first/last quote, so the value must parse
		// back exactly (quote→load round-trip, no backslash escapes).
		if err := upsertEnvFile(path, map[string]string{"STATION_NAME": `Shack #1 "main"`}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `STATION_NAME="Shack #1 "main""`) {
			t.Errorf("raw line = %q, want quoted without escapes", raw)
		}
		if got := parseEnvFile(path)[`STATION_NAME`]; got != `Shack #1 "main"` {
			t.Errorf("parseEnvFile round-trip = %q, want the original value", got)
		}
	})

	t.Run("missing path is an error", func(t *testing.T) {
		if err := upsertEnvFile("", map[string]string{"A": "b"}); err == nil {
			t.Error("expected error for empty path")
		}
	})
}

func TestQuoteEnvValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"with space", `"with space"`},
		{"hash#", `"hash#"`},
		{"say \"hi\"", `"say "hi""`},
		{"ends with quote\"", `"ends with quote""`},
		{"'single'", `"'single'"`},
	}
	for _, c := range cases {
		if got := quoteEnvValue(c.in); got != c.want {
			t.Errorf("quoteEnvValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- validation --------------------------------------------------------------

func TestValidateConfigFields(t *testing.T) {
	cases := []struct {
		name    string
		updates map[string]string
		wantErr string
	}{
		{"valid locator", map[string]string{"STATION_LOCATOR": "jo62qm"}, ""},
		{"locator with spaces ok", map[string]string{"STATION_LOCATOR": " JO62qm "}, ""},
		{"invalid locator", map[string]string{"STATION_LOCATOR": "ZZ99xx"}, "Maidenhead"},
		{"invalid locator garbage", map[string]string{"STATION_LOCATOR": "not-a-grid"}, "Maidenhead"},
		{"locator digits wrong", map[string]string{"STATION_LOCATOR": "JO9999"}, "Maidenhead"},
		{"valid port", map[string]string{"PST_PORT": "12000"}, ""},
		{"port zero", map[string]string{"PST_PORT": "0"}, "1..65535"},
		{"port too big", map[string]string{"PST_PORT": "70000"}, "1..65535"},
		{"port not numeric", map[string]string{"PST_PORT": "udp"}, "1..65535"},
		{"valid beamwidth", map[string]string{"BEAMWIDTH_3DB_DEG": "45.5"}, ""},
		{"beamwidth not numeric", map[string]string{"BEAMWIDTH_3DB_DEG": "wide"}, "beamwidth"},
		{"valid control bool", map[string]string{"CONTROL_PERMITTED": "false"}, ""},
		{"control bool invalid", map[string]string{"CONTROL_PERMITTED": "maybe"}, "true/false"},
		{"debug logging invalid", map[string]string{"DEBUG_LOGGING": "on"}, "true/false"},
		{"valid transport", map[string]string{"RIG_TRANSPORT": "WaveLogGate"}, ""},
		{"invalid transport", map[string]string{"RIG_TRANSPORT": "rigctld"}, "none, waveloggate, or log4om"},
		{"valid log4om addr", map[string]string{"RIG_LOG4OM_ADDR": "192.168.1.10:2236"}, ""},
		{"log4om addr without port", map[string]string{"RIG_LOG4OM_ADDR": "192.168.1.10"}, "host:port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateConfig(c.updates)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q, want it to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestValidateBrokerURL(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr string
	}{
		{"tcp://127.0.0.1:1883", ""},
		{"tls://broker.example.org:8883", ""},
		{"ssl://broker.example.org:8883", ""},
		{"ws://127.0.0.1:9001", ""},
		{"wss://broker.example.org/mqtt", ""},
		{"mqtt://127.0.0.1:1883", ""},
		{"mqtts://broker.example.org", ""},
		{"http://127.0.0.1:1883", "tcp://, tls://, ws:// or wss://"},
		{"no-scheme.example.org:1883", "must use"},
		{"tcp://", "host:port"},
		{"   tcp://127.0.0.1:1883   ", ""}, // trimmed
	}
	for _, c := range cases {
		err := validateBrokerURL(c.raw)
		if c.wantErr == "" && err != nil {
			t.Errorf("validateBrokerURL(%q) = %v, want nil", c.raw, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("validateBrokerURL(%q) = %v, want error containing %q", c.raw, err, c.wantErr)
		}
	}
}

func TestIsSecretKey(t *testing.T) {
	for _, key := range []string{"WAVELOG_API_KEY", "UB_PASSWORD"} {
		if !isSecretKey(key) {
			t.Errorf("%s should be Secret", key)
		}
	}
	for _, key := range []string{"STATION_LOCATOR", "PST_PORT", "WAVELOG_URL"} {
		if isSecretKey(key) {
			t.Errorf("%s should not be Secret", key)
		}
	}
	if isSecretKey("NOT_A_FIELD") {
		t.Error("unknown key reported as Secret")
	}
}

func TestManagedEnvKeysMatchConfigFields(t *testing.T) {
	m := managedEnvKeys()
	if len(m) != len(configFields) {
		t.Fatalf("managed keys = %d, configFields = %d", len(m), len(configFields))
	}
	for _, f := range configFields {
		if _, ok := m[f.Key]; !ok {
			t.Errorf("field %q missing from managedEnvKeys", f.Key)
		}
	}
}

// envOr & friends are the flag-default layer: env beats built-in default (the
// .env feeds the env via dotenv.Load, which only sets unset vars, so env beats
// .env), and an explicit flag wins because it overrides the default.
func TestEnvResolutionHelpers(t *testing.T) {
	t.Setenv("HR_TEST_STR", "from-env")
	if got := envOr("HR_TEST_STR", "def"); got != "from-env" {
		t.Errorf("envOr = %q", got)
	}
	t.Setenv("HR_TEST_STR", "   ") // blank env counts as unset
	if got := envOr("HR_TEST_STR", "def"); got != "def" {
		t.Errorf("envOr blank = %q, want default", got)
	}
	if got := envOr("HR_TEST_MISSING", "def"); got != "def" {
		t.Errorf("envOr missing = %q", got)
	}

	t.Setenv("HR_TEST_INT", "42")
	if got := envIntOr("HR_TEST_INT", 1); got != 42 {
		t.Errorf("envIntOr = %d", got)
	}
	t.Setenv("HR_TEST_INT", "nan")
	if got := envIntOr("HR_TEST_INT", 1); got != 1 {
		t.Errorf("envIntOr invalid = %d, want default", got)
	}

	t.Setenv("HR_TEST_BOOL", "false")
	if envBoolOr("HR_TEST_BOOL", true) {
		t.Error("envBoolOr should parse false")
	}
	t.Setenv("HR_TEST_BOOL", "junk")
	if !envBoolOr("HR_TEST_BOOL", true) {
		t.Error("envBoolOr invalid should fall back to default true")
	}

	t.Setenv("HR_TEST_FLOAT", "33.5")
	if got := envFloatOr("HR_TEST_FLOAT", 60); got != 33.5 {
		t.Errorf("envFloatOr = %v", got)
	}
	t.Setenv("HR_TEST_FLOAT", "abc")
	if got := envFloatOr("HR_TEST_FLOAT", 60); got != 60 {
		t.Errorf("envFloatOr invalid = %v, want default", got)
	}
}

// ---- handler-level tests (httptest.NewRecorder) -----------------------------

func newConfigTestServer(t *testing.T, envContent string) (*server, string) {
	t.Helper()
	envPath := filepath.Join(t.TempDir(), ".env")
	if envContent != "" {
		if err := os.WriteFile(envPath, []byte(envContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{cfg: serviceConfig{
		EnvFile: envPath,
		Timeout: 100 * time.Millisecond,
		PSTHost: "127.0.0.1",
		PSTPort: 12000,
		Station: stationConfig{Locator: "JO62qm", Name: "Test Station"},
	}}
	s.client = newPSTUDPClient(s.cfg)
	return s, envPath
}

func TestHandleConfig_MethodRouting(t *testing.T) {
	s, _ := newConfigTestServer(t, "")
	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: status %d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, POST" {
		t.Errorf("Allow = %q", got)
	}

	rec = httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: status %d", rec.Code)
	}
}

func TestHandleConfigGet(t *testing.T) {
	s, envPath := newConfigTestServer(t, "PST_PORT=13000\nWAVELOG_API_KEY=sekrit\n")
	s.cfg.WavelogAPIKey = "sekrit" // secret set on the running config too

	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Fields     []configField `json:"fields"`
		Values     map[string]string
		Set        map[string]bool
		EnvFile    string `json:"env_file"`
		CanRestart bool   `json:"can_restart"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Fields) == 0 {
		t.Fatal("no fields returned")
	}
	if resp.EnvFile != envPath {
		t.Errorf("env_file = %q, want %q", resp.EnvFile, envPath)
	}
	if !resp.CanRestart {
		t.Error("can_restart should be true")
	}
	// Secret from the .env must never be echoed back.
	if resp.Values["WAVELOG_API_KEY"] != "" {
		t.Errorf("secret echoed back: %q", resp.Values["WAVELOG_API_KEY"])
	}
	if !resp.Set["WAVELOG_API_KEY"] {
		t.Error("secret should still be reported as set")
	}
	if resp.Values["PST_PORT"] != "13000" {
		t.Errorf("PST_PORT = %q, want value from .env", resp.Values["PST_PORT"])
	}
	// Key absent from the .env falls back to the running config.
	if resp.Values["STATION_LOCATOR"] != "JO62qm" {
		t.Errorf("STATION_LOCATOR = %q, want running fallback JO62qm", resp.Values["STATION_LOCATOR"])
	}
	if !resp.Set["STATION_LOCATOR"] {
		t.Error("STATION_LOCATOR should be set via running fallback")
	}
	// Unset + unconfigured field: blank and not set.
	if resp.Values["BACKEND_URL"] != "" || resp.Set["BACKEND_URL"] {
		t.Errorf("BACKEND_URL should be blank/unset: %q / %v", resp.Values["BACKEND_URL"], resp.Set["BACKEND_URL"])
	}
	// The whole payload must not contain the secret anywhere.
	if strings.Contains(rec.Body.String(), "sekrit") {
		t.Error("response body leaked the secret")
	}
}

func TestHandleConfigPost(t *testing.T) {
	t.Run("saves managed keys and reports restart required", func(t *testing.T) {
		s, envPath := newConfigTestServer(t, "# keep me\nUNMANAGED=keep\nPST_PORT=12000\n")
		body := `{"values":{"STATION_LOCATOR":"jo62qm","PST_PORT":"13000","TOTALLY_UNKNOWN":"x","BACKEND_URL":"  https://hr.example.org  "}}`
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			OK              bool `json:"ok"`
			Saved           int  `json:"saved"`
			RestartRequired bool `json:"restart_required"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if !resp.OK || resp.Saved != 3 || !resp.RestartRequired {
			t.Errorf("envelope = %+v (unknown key must be ignored)", resp)
		}
		data, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(data)
		// Characterization: the locator is stored raw (validation does not normalize).
		for _, want := range []string{"STATION_LOCATOR=jo62qm", "PST_PORT=13000", "BACKEND_URL=https://hr.example.org", "# keep me", "UNMANAGED=keep"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("invalid locator rejected with clear error", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config",
			strings.NewReader(`{"values":{"STATION_LOCATOR":"nope!!"}}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Maidenhead") {
			t.Errorf("error %q, want a Maidenhead hint", rec.Body.String())
		}
		// Nothing must have been written.
		if _, err := os.Stat(s.cfg.EnvFile); !os.IsNotExist(err) {
			t.Error(".env created despite validation failure")
		}
	})

	t.Run("invalid port rejected", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config",
			strings.NewReader(`{"values":{"PST_PORT":"0"}}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})

	t.Run("blank secret leaves stored value unchanged", func(t *testing.T) {
		s, envPath := newConfigTestServer(t, "WAVELOG_API_KEY=existing-secret\n")
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config",
			strings.NewReader(`{"values":{"WAVELOG_API_KEY":"","PST_PORT":"12000"}}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		data, _ := os.ReadFile(envPath)
		if !strings.Contains(string(data), "WAVELOG_API_KEY=existing-secret") {
			t.Errorf("secret overwritten by blank: %q", data)
		}
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config", strings.NewReader(`{not json`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})

	t.Run("unwritable .env is a 500", func(t *testing.T) {
		s := &server{cfg: serviceConfig{EnvFile: filepath.Join(t.TempDir(), "is-a-dir")}}
		if err := os.MkdirAll(s.cfg.EnvFile, 0o700); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.handleConfigPost(rec, httptest.NewRequest(http.MethodPost, "/v1/config",
			strings.NewReader(`{"values":{"PST_PORT":"12000"}}`)))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "could not write .env") {
			t.Errorf("body %q", rec.Body.String())
		}
	})
}

func TestHandleRestart_MethodGate(t *testing.T) {
	s, _ := newConfigTestServer(t, "")
	rec := httptest.NewRecorder()
	s.handleRestart(rec, httptest.NewRequest(http.MethodGet, "/v1/restart", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q", got)
	}
}

func TestHandlePSTTest(t *testing.T) {
	t.Run("method gate", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		rec := httptest.NewRecorder()
		s.handlePSTTest(rec, httptest.NewRequest(http.MethodGet, "/v1/pst/test", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status %d want 405", rec.Code)
		}
	})

	t.Run("host required", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		rec := httptest.NewRecorder()
		s.handlePSTTest(rec, httptest.NewRequest(http.MethodPost, "/v1/pst/test", strings.NewReader(`{"port":12000}`)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "host is required") {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("port validated", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		for _, port := range []int{0, -1, 65536} {
			rec := httptest.NewRecorder()
			s.handlePSTTest(rec, httptest.NewRequest(http.MethodPost, "/v1/pst/test",
				strings.NewReader(`{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "1..65535") {
				t.Fatalf("port %d: status %d body %s", port, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("unreachable endpoint reports failure as 200", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		port := unusedUDPPort(t)
		rec := httptest.NewRecorder()
		s.handlePSTTest(rec, httptest.NewRequest(http.MethodPost, "/v1/pst/test",
			strings.NewReader(`{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"timeout_ms":300}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var resp struct {
			OK       bool   `json:"ok"`
			Endpoint string `json:"endpoint"`
			Error    string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.OK {
			t.Errorf("unexpected success: %+v", resp)
		}
		if resp.Error == "" {
			t.Error("error detail empty")
		}
	})

	t.Run("reachable endpoint returns azimuth", func(t *testing.T) {
		s, _ := newConfigTestServer(t, "")
		listen := unusedUDPPort(t)
		startUDPAzimuthResponder(t, listen, listen+1, "AZ=275")
		rec := httptest.NewRecorder()
		s.handlePSTTest(rec, httptest.NewRequest(http.MethodPost, "/v1/pst/test",
			strings.NewReader(`{"host":"127.0.0.1","port":`+strconv.Itoa(listen)+`,"timeout_ms":1500}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			OK         bool    `json:"ok"`
			Endpoint   string  `json:"endpoint"`
			AzimuthDeg float64 `json:"azimuth_deg"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if !resp.OK || resp.AzimuthDeg != 275 {
			t.Errorf("got %+v", resp)
		}
		if resp.Endpoint != "127.0.0.1:"+strconv.Itoa(listen) {
			t.Errorf("endpoint %q", resp.Endpoint)
		}
	})
}
