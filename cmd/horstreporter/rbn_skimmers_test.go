package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestParseRBNSkimmerList(t *testing.T) {
	page, err := os.ReadFile("testdata/rbn_skimmers.html")
	if err != nil {
		t.Fatal(err)
	}
	got := parseRBNSkimmerList(string(page) + `<tr><td>N0GRID</td><td></td><td></td></tr>`)
	want := map[string]string{"VE6WZ-4": "DO21ST", "IK4VET": "JN54MM", "JI1HFJ": "PM95SS", "YO5LD-U": "KN05NR"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for call, loc := range want {
		if got[call] != loc {
			t.Errorf("%s = %q, want %q", call, got[call], loc)
		}
	}
}

func TestRBNSkimmerLookup(t *testing.T) {
	l := &rbnSkimmerList{}
	l.set(map[string]string{"VE6WZ-4": "DO21ST", "W3LPL": "FM19LG"}, 1)
	for skimmer, want := range map[string]string{
		"VE6WZ-4-#": "DO21ST", // telnet form of a numbered receiver
		"W3LPL-#":   "FM19LG",
		"W3LPL-2-#": "FM19LG", // unlisted receiver number: same station
		"W3LPL":     "FM19LG",
	} {
		if loc, ok := l.lookup(skimmer); !ok || loc != want {
			t.Errorf("lookup(%q) = %q %v, want %q", skimmer, loc, ok, want)
		}
	}
	if _, ok := l.lookup("K1TTT-#"); ok {
		t.Error("unknown skimmer resolved")
	}
	if n := testing.AllocsPerRun(1000, func() { l.lookup("W3LPL-2-#") }); n != 0 {
		t.Fatalf("lookup allocates %v per call", n)
	}
}

func TestRBNSkimmerFetchKeepsListOnBadPage(t *testing.T) {
	page, _ := os.ReadFile("testdata/rbn_skimmers.html")
	body := string(page)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "horstreporter" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	l := &rbnSkimmerList{}
	if err := l.fetch(srv.Client(), srv.URL); err != nil || l.size() != 4 || l.fetchedAt.Load() == 0 {
		t.Fatalf("fetch: err %v size %d", err, l.size())
	}
	body = "<html>maintenance</html>"
	if err := l.fetch(srv.Client(), srv.URL); err == nil || l.size() != 4 {
		t.Fatalf("a page without rows must fail and keep the list: err %v size %d", err, l.size())
	}
}
