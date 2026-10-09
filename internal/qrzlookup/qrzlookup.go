// Package qrzlookup resolves callsigns to locator, name and country through the
// QRZ.com XML API, with a session, a result cache and portable-callsign
// fallbacks (EA8/DL7VEE/P).
package qrzlookup

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/maidenhead"
)

type Resolver interface {
	LookupLocator(callsign string) (string, error)
	// LookupInfo returns locator + operator name + country in one (cached) lookup.
	LookupInfo(callsign string) (Info, error)
}

// Info is the subset of a QRZ record we surface (locator, op first name,
// country/DXCC-entity name).
type Info struct {
	Locator string
	Name    string
	Country string
}

type cacheEntry struct {
	info    Info
	expires time.Time
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string

	mu           sync.Mutex
	sessionKey   string
	sessionValid time.Time
	cache        map[string]cacheEntry
}

type xmlResponse struct {
	XMLName  xml.Name `xml:"QRZDatabase"`
	Session  xmlSession
	Callsign xmlCallsign `xml:"Callsign"`
}

type xmlSession struct {
	Key   string `xml:"Key"`
	Error string `xml:"Error"`
}

type xmlCallsign struct {
	Grid    string `xml:"grid"`
	Fname   string `xml:"fname"`
	Name    string `xml:"name"`
	Country string `xml:"country"`
}

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
		cache:      make(map[string]cacheEntry, 2048),
	}
}

// lookupCandidates returns ordered lookup candidates for slash-style callsigns.
// Examples:
//
//	EK/RX3DPK   -> EK/RX3DPK, RX3DPK, EK
//	DL7VEE/P    -> DL7VEE/P, DL7VEE, P
//	EA8/DL7VEE/P -> EA8/DL7VEE/P, DL7VEE, EA8, P
func lookupCandidates(callsign string) []string {
	call := strings.ToUpper(strings.TrimSpace(callsign))
	if call == "" {
		return nil
	}

	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{}, 6)
	add := func(v string) {
		v = strings.ToUpper(strings.TrimSpace(v))
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		candidates = append(candidates, v)
	}

	add(call)
	if !strings.Contains(call, "/") {
		return candidates
	}

	partsRaw := strings.Split(call, "/")
	parts := make([]string, 0, len(partsRaw))
	for _, p := range partsRaw {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return candidates
	}

	longestIdx := 0
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > len(parts[longestIdx]) {
			longestIdx = i
		}
	}
	add(parts[longestIdx])
	for i, p := range parts {
		if i == longestIdx {
			continue
		}
		add(p)
	}

	return candidates
}

// LookupLocator returns just the locator (kept for callers that only need it).
func (c *Client) LookupLocator(callsign string) (string, error) {
	info, err := c.LookupInfo(callsign)
	return info.Locator, err
}

// LookupInfo returns locator + op name + country, cached per callsign.
func (c *Client) LookupInfo(callsign string) (Info, error) {
	if c == nil {
		return Info{}, fmt.Errorf("qrz resolver not configured")
	}

	call := strings.ToUpper(strings.TrimSpace(callsign))
	if call == "" {
		return Info{}, fmt.Errorf("empty callsign")
	}

	now := time.Now()
	c.mu.Lock()
	if cached, ok := c.cache[call]; ok && now.Before(cached.expires) {
		c.mu.Unlock()
		return cached.info, nil
	}
	c.mu.Unlock()

	candidates := lookupCandidates(call)
	var lastErr error
	hadNoError := false

	for _, candidate := range candidates {
		info, err := c.lookupWithRetry(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		hadNoError = true
		if info.Locator == "" && info.Name == "" && info.Country == "" {
			continue
		}
		c.mu.Lock()
		c.cache[call] = cacheEntry{info: info, expires: now.Add(24 * time.Hour)}
		c.mu.Unlock()
		return info, nil
	}

	if hadNoError {
		c.mu.Lock()
		c.cache[call] = cacheEntry{info: Info{}, expires: now.Add(20 * time.Minute)}
		c.mu.Unlock()
		return Info{}, nil
	}

	if lastErr != nil {
		return Info{}, lastErr
	}
	return Info{}, nil
}

func (c *Client) lookupWithRetry(call string) (Info, error) {
	if err := c.ensureSession(); err != nil {
		return Info{}, err
	}

	info, sessionExpired, err := c.lookupCall(call)
	if err == nil {
		return info, nil
	}
	if !sessionExpired {
		return Info{}, err
	}

	c.mu.Lock()
	c.sessionKey = ""
	c.sessionValid = time.Time{}
	c.mu.Unlock()

	if err := c.ensureSession(); err != nil {
		return Info{}, err
	}
	info, _, err = c.lookupCall(call)
	if err != nil {
		return Info{}, err
	}
	return info, nil
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

	resp, err := c.httpClient.Get(c.baseURL + "?" + params.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var data xmlResponse
	if err := xml.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	if strings.TrimSpace(data.Session.Error) != "" {
		return fmt.Errorf("qrz login failed: %s", strings.TrimSpace(data.Session.Error))
	}
	if strings.TrimSpace(data.Session.Key) == "" {
		return fmt.Errorf("qrz login failed: missing session key")
	}

	c.mu.Lock()
	c.sessionKey = strings.TrimSpace(data.Session.Key)
	c.sessionValid = time.Now().Add(12 * time.Hour)
	c.mu.Unlock()
	return nil
}

func (c *Client) lookupCall(call string) (info Info, sessionExpired bool, err error) {
	c.mu.Lock()
	session := c.sessionKey
	c.mu.Unlock()

	if strings.TrimSpace(session) == "" {
		return Info{}, true, fmt.Errorf("qrz session unavailable")
	}

	params := url.Values{}
	params.Set("s", session)
	params.Set("callsign", call)

	resp, err := c.httpClient.Get(c.baseURL + "?" + params.Encode())
	if err != nil {
		return Info{}, false, err
	}
	defer resp.Body.Close()

	var data xmlResponse
	if err := xml.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Info{}, false, err
	}

	sessionErr := strings.ToLower(strings.TrimSpace(data.Session.Error))
	if sessionErr != "" {
		if strings.Contains(sessionErr, "session") || strings.Contains(sessionErr, "expired") || strings.Contains(sessionErr, "timeout") {
			return Info{}, true, fmt.Errorf("qrz session expired: %s", data.Session.Error)
		}
		return Info{}, false, fmt.Errorf("qrz lookup error: %s", data.Session.Error)
	}

	out := Info{
		Name:    strings.TrimSpace(data.Callsign.Fname), // first name (friendly)
		Country: strings.TrimSpace(data.Callsign.Country),
	}
	if out.Name == "" {
		out.Name = strings.TrimSpace(data.Callsign.Name)
	}
	grid := strings.ToUpper(strings.TrimSpace(data.Callsign.Grid))
	if len(grid) >= 4 && maidenhead.IsLocator(grid[:4]) {
		out.Locator = grid[:4]
	}
	return out, false, nil
}
