package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSWPCTime(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"2026-07-31 06:00:00.000", 1785477600, true},
		{"2026-07-31 06:00:00", 1785477600, true},
		{"", 0, false},
		{"not a date", 0, false},
	}
	for _, c := range cases {
		got, ok := parseSWPCTime(c.in)
		if ok != c.ok {
			t.Errorf("parseSWPCTime(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseSWPCTime(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseDailySolarIndicesF107(t *testing.T) {
	// NOAA daily-solar-indices.txt column layout (whitespace-separated):
	//   fields[0..2] = YYYY MM DD
	//   fields[3]    = F10.7 cm radio flux
	//   fields[4]    = SESC sunspot number
	//   fields[5+]   = area, new regions, mean field, x-ray bkgd, flares...
	// Missing values use -999 as a sentinel.
	cases := []struct {
		name     string
		text     string
		wantOk   bool
		wantFlux float64
	}{
		{
			name: "today row with -999 sentinel skipped, yesterday used",
			text: "# NOAA SWPC daily solar indices\n" +
				":Product: daily-solar-indices.txt\n" +
				"#                         Sunspot\n" +
				"#  Date     10.7cm Number  ...\n" +
				"2026 07 30  142.5    120     550      2    -999      *   4  1  0\n" +
				"2026 07 31  -999     0       0        0    -999      *   0  0  0\n",
			wantOk:   true,
			wantFlux: 142.5,
		},
		{
			name: "all rows -999 sentinel, no valid flux",
			text: "# NOAA SWPC\n" +
				"2026 07 30  -999     0       0\n" +
				"2026 07 31  -999     0       0\n",
			wantOk:   false,
			wantFlux: 0,
		},
		{
			name:     "empty input",
			text:     "",
			wantOk:   false,
			wantFlux: 0,
		},
		{
			name: "single valid row",
			text: "2026 07 30  142.5    120     550      2    -999      *   4  1  0\n",
			wantOk:   true,
			wantFlux: 142.5,
		},
		{
			name: "multiple valid rows, latest (ascending date) wins",
			text: "2026 07 29  140.0    100     410      2    -999      *   4  0  0\n" +
				"2026 07 30  152.5    130     460      0    -999      *   1  1  0\n",
			wantOk:   true,
			wantFlux: 152.5,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flux, _, ok := parseDailySolarIndicesF107(c.text)
			if ok != c.wantOk {
				t.Fatalf("ok = %v, want %v", ok, c.wantOk)
			}
			if ok && flux != c.wantFlux {
				t.Errorf("flux = %g, want %g", flux, c.wantFlux)
			}
		})
	}
}
// --- U3: SWPC feed fetchers against a mock upstream ------------------------
//
// The kp/F10.7/xray/OVATION URLs are package vars (defer-swapped below) so
// each fetcher is driven through a mock upstream server. No Postgres is
// available in tests: the fetchers are constructed with a nil store, and
// upsertProplabSW no-ops on a nil receiver, so the store step is a no-op. The
// parse/branch outcome is pinned via the log stream (fetch/parse failures log
// at INFO; a successful parse logs nothing), plus the request the mock
// upstream received.

// captureLog redirects the stdlib log output into a buffer (and restores it),
// pinning logLevel to INFO so logInfo lines are observable.
func captureLogForSWTest(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	origWriter := log.Writer()
	origLevel := logLevel
	log.SetOutput(buf)
	logLevel = "INFO"
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		logLevel = origLevel
	})
	return buf
}

// mockSWServer serves a SWPC feed fixture per path: /kp, /f107, /xray,
// /ovation. The behavior map selects fixture vs HTTP error vs malformed body.
// Fixtures are modeled on the live NOAA SWPC feed schemas (2026-09-11),
// trimmed to the few records each assertion needs; refresh if the upstream
// schema drifts.
func newSWMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kp":
			_, _ = w.Write([]byte(`[
				{"time_tag":"2026-09-11 05:00:00.000","kp_index":2,"kp":"2","estimated_kp":0},
				{"time_tag":"2026-09-11 06:00:00.000","kp_index":0,"kp":"","estimated_kp":2.33}
			]`))
		case "/f107":
			_, _ = w.Write([]byte(
				"# NOAA SWPC daily solar indices\n" +
					":Product: daily-solar-indices.txt\n" +
					"2026 09 10  148.5    120     550      2    -999      *   4  1  0\n" +
					"2026 09 11  -999     0       0        0    -999      *   0  0  0\n"))
		case "/xray":
			_, _ = w.Write([]byte(`[
				{"time_tag":"2026-09-11 05:58:00.000","flux":1.1e-7,"energy":"1000-8000"},
				{"time_tag":"2026-09-11 06:00:00.000","flux":2.5e-7,"energy":"0.05-0.4"},
				{"time_tag":"2026-09-11 06:00:30.000","flux":1.9e-7,"energy":"0.1-0.8"},
				{"time_tag":"2026-09-11 06:01:00.000","flux":2.2e-7,"energy":"0.1-0.8"}
			]`))
		case "/ovation":
			_, _ = w.Write([]byte(`{
				"Observation Time":"2026-09-11 05:30:00",
				"Forecast Time":"2026-09-11 06:30:00",
				"Coordinates":[[0,0,3],[10,20,5],[20,30,-1],[30,40,0]]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSWFetchersParseFixturesWithoutFailure(t *testing.T) {
	srv := newSWMockServer(t)
	defer srv.Close()

	// Defer-swap the feed URL vars (the production URLs are restored).
	savedKp, savedF107, savedXray, savedOvation := swKpURL, swF107URL, swXrayURL, swOvationURL
	swKpURL = srv.URL + "/kp"
	swF107URL = srv.URL + "/f107"
	swXrayURL = srv.URL + "/xray"
	swOvationURL = srv.URL + "/ovation"
	defer func() {
		swKpURL, swF107URL, swXrayURL, swOvationURL = savedKp, savedF107, savedXray, savedOvation
	}()

	logbuf := captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchAll()

	if out := logbuf.String(); out != "" {
		t.Fatalf("expected a fully successful fetchAll to log nothing, got:\n%s", out)
	}
}

func TestSWFetchersHitMockUpstream(t *testing.T) {
	// Sanity: with the URL vars swapped, the fetchers must hit the mock
	// upstream (proving the interception works and the HTTP client is used).
	var gotKpUA, gotKpAccept string
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch r.URL.Path {
		case "/kp":
			gotKpUA = r.Header.Get("User-Agent")
			gotKpAccept = r.Header.Get("Accept")
		case "/f107":
			if got := r.Header.Get("Accept"); got != "text/plain" {
				t.Errorf("F10.7 Accept = %q, want text/plain", got)
			}
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	savedKp, savedF107 := swKpURL, swF107URL
	swKpURL = srv.URL + "/kp"
	swF107URL = srv.URL + "/f107"
	defer func() { swKpURL, swF107URL = savedKp, savedF107 }()

	captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchKp()
	svc.fetchF107()

	if gotKpUA != "horstreporter-proplab/1.0" {
		t.Errorf("Kp User-Agent = %q, want horstreporter-proplab/1.0", gotKpUA)
	}
	if gotKpAccept != "application/json" {
		t.Errorf("Kp Accept = %q, want application/json", gotKpAccept)
	}
	// fetchKp on an empty array produces no rows → early return; F10.7 on an
	// empty body has no valid row → parse failure (both log nothing fatal).
	if len(gotPaths) != 2 {
		t.Fatalf("expected 2 mock requests, got %v", gotPaths)
	}
}

func TestSWFetchersDegradeOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	savedKp, savedF107, savedXray, savedOvation := swKpURL, swF107URL, swXrayURL, swOvationURL
	swKpURL, swF107URL, swXrayURL, swOvationURL = srv.URL+"/kp", srv.URL+"/f107", srv.URL+"/xray", srv.URL+"/ovation"
	defer func() { swKpURL, swF107URL, swXrayURL, swOvationURL = savedKp, savedF107, savedXray, savedOvation }()

	logbuf := captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchAll()

	for _, want := range []string{
		"SW Kp fetch failed",
		"SW F10.7 fetch failed",
		"SW X-ray fetch failed",
		"SW OVATION fetch failed",
	} {
		if !strings.Contains(logbuf.String(), want) {
			t.Errorf("expected log %q on HTTP 503 for every feed; log:\n%s", want, logbuf.String())
		}
	}
}

func TestSWFetchersDegradeOnMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not the feed</html>"))
	}))
	defer srv.Close()

	savedKp, savedF107, savedXray, savedOvation := swKpURL, swF107URL, swXrayURL, swOvationURL
	swKpURL, swF107URL, swXrayURL, swOvationURL = srv.URL+"/kp", srv.URL+"/f107", srv.URL+"/xray", srv.URL+"/ovation"
	defer func() { swKpURL, swF107URL, swXrayURL, swOvationURL = savedKp, savedF107, savedXray, savedOvation }()

	logbuf := captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchAll()

	// Kp and X-ray JSON parse failures; F10.7 finds no data row; OVATION
	// fails to unmarshal the HTML.
	for _, want := range []string{
		"SW Kp parse failed",
		"SW F10.7 parse failed",
		"SW X-ray parse failed",
		"SW OVATION parse failed",
	} {
		if !strings.Contains(logbuf.String(), want) {
			t.Errorf("expected log %q on malformed bodies; log:\n%s", want, logbuf.String())
		}
	}
}

func TestFetchXrayPrefersNewestLongChannelRecord(t *testing.T) {
	// Characterization via the log stream: the long-channel (0.1-0.8nm) record
	// is selected by newest time_tag; a fixture with ONLY short-channel
	// records must fail with "no 0.1-0.8nm record".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"time_tag":"2026-09-11 06:00:00.000","flux":2.5e-7,"energy":"0.05-0.4"}
		]`))
	}))
	defer srv.Close()

	savedXray := swXrayURL
	swXrayURL = srv.URL
	defer func() { swXrayURL = savedXray }()

	logbuf := captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchXray()

	if !strings.Contains(logbuf.String(), "no 0.1-0.8nm record") {
		t.Fatalf("expected 'no 0.1-0.8nm record' for a short-channel-only feed; log:\n%s", logbuf.String())
	}
}

func TestFetchOvationRejectsNoCoordinateValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Observation Time":"2026-09-11 05:30:00","Forecast Time":"2026-09-11 06:30:00","Coordinates":[[10,20,-1],[30,40,-5]]}`))
	}))
	defer srv.Close()

	savedOvation := swOvationURL
	swOvationURL = srv.URL + "/ovation"
	defer func() { swOvationURL = savedOvation }()

	logbuf := captureLogForSWTest(t)
	svc := &swIngestService{client: srv.Client()}
	svc.fetchOvation()

	if !strings.Contains(logbuf.String(), "no coordinate values") {
		t.Fatalf("expected 'no coordinate values' when every coordinate is negative; log:\n%s", logbuf.String())
	}
}
