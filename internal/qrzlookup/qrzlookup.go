// Package qrzlookup resolves callsigns to a Maidenhead locator through the
// QRZ.com XML API (a subscription is needed for the grid field). It keeps the
// session key and tries the plain call behind a portable suffix (DL7VEE/P).
// It does no caching: the caller owns the cache.
package qrzlookup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/maidenhead"
)

// ErrNotFound is returned when QRZ has no record for any candidate call.
var ErrNotFound = errors.New("qrz: callsign not found")

// AuthError is a failed login (bad credentials, no subscription). Retrying
// will not help until the configuration changes.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return "qrz login failed: " + e.Msg }

// Info is the subset of a QRZ record the backend uses.
type Info struct {
	// Locator is the record's grid, 6 characters when QRZ has them, else 4;
	// "" when the record carries no valid grid.
	Locator string
	Country string
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string

	mu           sync.Mutex
	sessionKey   string
	sessionValid time.Time
}

// NewClient returns nil when either credential is empty.
func NewClient(username, password string) *Client {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil
	}
	return &Client{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		baseURL:    "https://xmldata.qrz.com/xml/current/",
		username:   username,
		password:   password,
	}
}

// WithBaseURL points the client at another endpoint (tests).
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

type xmlResponse struct {
	XMLName  xml.Name    `xml:"QRZDatabase"`
	Session  xmlSession  `xml:"Session"`
	Callsign xmlCallsign `xml:"Callsign"`
}

type xmlSession struct {
	Key     string `xml:"Key"`
	Error   string `xml:"Error"`
	Message string `xml:"Message"`
	SubExp  string `xml:"SubExp"`
}

// noSubscription reports a login or lookup answered for an account without
// the XML data subscription: QRZ still issues a key but leaves the grid out
// of every record, so each answer would read as "no grid".
func (s xmlSession) noSubscription() bool {
	return strings.EqualFold(strings.TrimSpace(s.SubExp), "non-subscriber") ||
		strings.Contains(strings.ToLower(s.Message), "subscription is required")
}

const noSubscriptionMsg = "account has no QRZ XML data subscription (the grid field needs one)"

type xmlCallsign struct {
	Call    string `xml:"call"`
	Grid    string `xml:"grid"`
	Country string `xml:"country"`
}

// portableSuffixes are the suffixes that leave the station at or near its
// home grid, so the plain call's record is a fair answer. /MM, /AM and a
// prefix (EK/RX3DPK: operating from another entity) are not: the home grid
// would place the station in the wrong place.
var portableSuffixes = map[string]bool{"P": true, "QRP": true, "M": true, "A": true}

// LookupCandidates returns the calls to query for callsign, in order:
//
//	DL7VEE      -> DL7VEE
//	DL7VEE/P    -> DL7VEE/P, DL7VEE
//	EK/RX3DPK   -> EK/RX3DPK
//	DL7VEE/MM   -> DL7VEE/MM
func LookupCandidates(callsign string) []string {
	call := strings.ToUpper(strings.TrimSpace(callsign))
	if call == "" {
		return nil
	}
	out := []string{call}
	parts := strings.Split(call, "/")
	if len(parts) != 2 || parts[0] == "" {
		return out
	}
	if portableSuffixes[parts[1]] {
		out = append(out, parts[0])
	}
	return out
}

// LookupInfo queries QRZ for callsign. It returns ErrNotFound when no
// candidate has a record, an *AuthError when login fails, and another error
// for network or protocol trouble. A found record without a grid returns an
// Info with an empty Locator and a nil error.
func (c *Client) LookupInfo(callsign string) (Info, error) {
	if c == nil {
		return Info{}, fmt.Errorf("qrz resolver not configured")
	}
	candidates := LookupCandidates(callsign)
	if len(candidates) == 0 {
		return Info{}, fmt.Errorf("empty callsign")
	}
	found := false
	var first Info
	for _, cand := range candidates {
		info, err := c.lookupWithRetry(cand)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Info{}, err
		}
		if info.Locator != "" {
			return info, nil
		}
		if !found {
			found, first = true, info
		}
	}
	if found {
		return first, nil
	}
	return Info{}, ErrNotFound
}

func (c *Client) lookupWithRetry(call string) (Info, error) {
	if err := c.ensureSession(); err != nil {
		return Info{}, err
	}
	info, sessionExpired, err := c.lookupCall(call)
	if !sessionExpired {
		return info, err
	}
	c.mu.Lock()
	c.sessionKey = ""
	c.sessionValid = time.Time{}
	c.mu.Unlock()
	if err := c.ensureSession(); err != nil {
		return Info{}, err
	}
	info, _, err = c.lookupCall(call)
	return info, err
}

func (c *Client) get(params url.Values) (xmlResponse, error) {
	var data xmlResponse
	resp, err := c.httpClient.Get(c.baseURL + "?" + params.Encode())
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("qrz: HTTP %d", resp.StatusCode)
	}
	err = xml.NewDecoder(resp.Body).Decode(&data)
	return data, err
}

func (c *Client) ensureSession() error {
	c.mu.Lock()
	if c.sessionKey != "" && time.Now().Before(c.sessionValid) {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	params := url.Values{}
	params.Set("username", c.username)
	params.Set("password", c.password)
	params.Set("agent", "horstreporter")
	data, err := c.get(params)
	if err != nil {
		return err
	}
	if msg := strings.TrimSpace(data.Session.Error); msg != "" {
		return &AuthError{Msg: msg}
	}
	if data.Session.noSubscription() {
		return &AuthError{Msg: noSubscriptionMsg}
	}
	key := strings.TrimSpace(data.Session.Key)
	if key == "" {
		return &AuthError{Msg: "missing session key"}
	}
	c.mu.Lock()
	c.sessionKey = key
	c.sessionValid = time.Now().Add(12 * time.Hour)
	c.mu.Unlock()
	return nil
}

func (c *Client) lookupCall(call string) (info Info, sessionExpired bool, err error) {
	c.mu.Lock()
	session := c.sessionKey
	c.mu.Unlock()
	if session == "" {
		return Info{}, true, fmt.Errorf("qrz session unavailable")
	}

	params := url.Values{}
	params.Set("s", session)
	params.Set("callsign", call)
	data, err := c.get(params)
	if err != nil {
		return Info{}, false, err
	}

	msg := strings.TrimSpace(data.Session.Error)
	lower := strings.ToLower(msg)
	switch {
	case strings.HasPrefix(lower, "not found"):
		return Info{}, false, ErrNotFound
	case msg != "" && (strings.Contains(lower, "session") || strings.Contains(lower, "expired") || strings.Contains(lower, "timeout")):
		return Info{}, true, fmt.Errorf("qrz session expired: %s", msg)
	case msg != "":
		return Info{}, false, fmt.Errorf("qrz lookup error: %s", msg)
	case data.Session.noSubscription():
		// The subscription lapsed while the session was open.
		return Info{}, false, &AuthError{Msg: noSubscriptionMsg}
	case strings.TrimSpace(data.Session.Key) == "" && strings.TrimSpace(data.Callsign.Call) == "":
		// No key and no record: the session was dropped without an error text.
		return Info{}, true, fmt.Errorf("qrz session missing in response")
	}
	// QRZ may answer a portable or prefixed query with another record (the
	// plain call, an alias). For EA8/DL1ABC or DL1ABC/MM that is the home grid,
	// the wrong place; for DL1ABC/P LookupInfo asks for the plain call next.
	if got := strings.ToUpper(strings.TrimSpace(data.Callsign.Call)); got != "" && got != call && strings.Contains(call, "/") {
		return Info{}, false, ErrNotFound
	}

	return Info{
		Locator: maidenhead.Normalize(data.Callsign.Grid),
		Country: strings.TrimSpace(data.Callsign.Country),
	}, false, nil
}
