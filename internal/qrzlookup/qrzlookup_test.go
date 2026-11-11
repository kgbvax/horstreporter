package qrzlookup

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestLookupCandidates(t *testing.T) {
	tests := []struct {
		call string
		want []string
	}{
		{"DL7VEE", []string{"DL7VEE"}},
		{" dl7vee/p ", []string{"DL7VEE/P", "DL7VEE"}},
		{"DL7VEE/QRP", []string{"DL7VEE/QRP", "DL7VEE"}},
		{"EK/RX3DPK", []string{"EK/RX3DPK"}},
		{"DL7VEE/MM", []string{"DL7VEE/MM"}},
		{"EA8/DL7VEE/P", []string{"EA8/DL7VEE/P"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := LookupCandidates(tt.call); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("LookupCandidates(%q) = %v, want %v", tt.call, got, tt.want)
		}
	}
}

// fakeQRZ serves a login and a callsign table; records maps call -> grid
// ("" = record without grid).
func fakeQRZ(t *testing.T, records map[string]string, logins *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/xml")
		if q.Get("username") != "" {
			logins.Add(1)
			if q.Get("password") == "nosub" {
				fmt.Fprint(w, `<QRZDatabase><Session><Key>k1</Key><SubExp>non-subscriber</SubExp><Message>A subscription is required to access the complete record.</Message></Session></QRZDatabase>`)
				return
			}
			if q.Get("password") != "secret" {
				fmt.Fprint(w, `<QRZDatabase><Session><Error>Username/password incorrect</Error></Session></QRZDatabase>`)
				return
			}
			fmt.Fprint(w, `<QRZDatabase><Session><Key>k1</Key></Session></QRZDatabase>`)
			return
		}
		if q.Get("s") != "k1" {
			fmt.Fprint(w, `<QRZDatabase><Session><Error>Session Timeout</Error></Session></QRZDatabase>`)
			return
		}
		call := q.Get("callsign")
		if call == "EA8/DL7VEE" { // QRZ substitutes the home record
			fmt.Fprint(w, `<QRZDatabase><Callsign><call>DL7VEE</call><grid>JO62QM</grid></Callsign><Session><Key>k1</Key></Session></QRZDatabase>`)
			return
		}
		grid, ok := records[call]
		if !ok {
			fmt.Fprintf(w, `<QRZDatabase><Session><Key>k1</Key><Error>Not found: %s</Error></Session></QRZDatabase>`, call)
			return
		}
		fmt.Fprintf(w, `<QRZDatabase><Callsign><call>%s</call><grid>%s</grid><country>Germany</country></Callsign><Session><Key>k1</Key></Session></QRZDatabase>`, call, grid)
	}))
}

func TestLookupInfo(t *testing.T) {
	var logins atomic.Int32
	srv := fakeQRZ(t, map[string]string{"DL7VEE": "jo62qm", "DK0XX": "", "EK/RX3DPK": "LN20"}, &logins)
	defer srv.Close()
	c := NewClient("u", "secret").WithBaseURL(srv.URL)

	if info, err := c.LookupInfo("dl7vee/p"); err != nil || info.Locator != "JO62QM" {
		t.Fatalf("portable fallback: %+v %v", info, err)
	}
	if info, err := c.LookupInfo("DK0XX"); err != nil || info.Locator != "" {
		t.Fatalf("record without grid: %+v %v", info, err)
	}
	if _, err := c.LookupInfo("N0CALL"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown call: %v", err)
	}
	if info, _ := c.LookupInfo("EK/RX3DPK"); info.Locator != "LN20" {
		t.Fatalf("prefix portable: %+v", info)
	}
	if _, err := c.LookupInfo("EA8/DL7VEE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("substituted home record for a prefixed call: %v, want not found", err)
	}
	if n := logins.Load(); n != 1 {
		t.Fatalf("logged in %d times, want the session reused", n)
	}
}

func TestLookupInfoAuthError(t *testing.T) {
	var logins atomic.Int32
	srv := fakeQRZ(t, nil, &logins)
	defer srv.Close()
	c := NewClient("u", "wrong").WithBaseURL(srv.URL)
	var ae *AuthError
	if _, err := c.LookupInfo("DL7VEE"); !errors.As(err, &ae) {
		t.Fatalf("bad password: %v, want *AuthError", err)
	}
}

func TestLookupInfoWithoutSubscriptionIsAnAuthError(t *testing.T) {
	var logins atomic.Int32
	srv := fakeQRZ(t, map[string]string{"DL7VEE": "JO62QM"}, &logins)
	defer srv.Close()
	c := NewClient("u", "nosub").WithBaseURL(srv.URL)
	var ae *AuthError
	if _, err := c.LookupInfo("DL7VEE"); !errors.As(err, &ae) {
		t.Fatalf("non-subscriber login: %v, want *AuthError", err)
	}
}

func TestNewClientNeedsCredentials(t *testing.T) {
	if NewClient("", "x") != nil || NewClient("u", " ") != nil {
		t.Fatal("client without credentials")
	}
}
