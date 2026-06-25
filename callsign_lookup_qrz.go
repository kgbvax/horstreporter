package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type CallsignLocatorResolver interface {
	LookupLocator(callsign string) (string, error)
	// LookupInfo returns locator + operator name + country in one (cached) lookup.
	LookupInfo(callsign string) (CallsignInfo, error)
}

// CallsignInfo is the subset of a QRZ record we surface (locator, op first name,
// country/DXCC-entity name).
type CallsignInfo struct {
	Locator string
	Name    string
	Country string
}

type qrzCacheEntry struct {
	info    CallsignInfo
	expires time.Time
}

type qrzLookupClient struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string

	mu           sync.Mutex
	sessionKey   string
	sessionValid time.Time
	cache        map[string]qrzCacheEntry
}

type qrzXMLResponse struct {
	XMLName  xml.Name `xml:"QRZDatabase"`
	Session  qrzXMLSession
	Callsign qrzXMLCallsign `xml:"Callsign"`
}

type qrzXMLSession struct {
	Key   string `xml:"Key"`
	Error string `xml:"Error"`
}

type qrzXMLCallsign struct {
	Grid    string `xml:"grid"`
	Fname   string `xml:"fname"`
	Name    string `xml:"name"`
	Country string `xml:"country"`
}

func newQRZLookupClient(username, password string) *qrzLookupClient {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil
	}
	return &qrzLookupClient{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		baseURL:    "https://xmldata.qrz.com/xml/current/",
		username:   username,
		password:   password,
		cache:      make(map[string]qrzCacheEntry, 2048),
	}
}

// qrzLookupCandidates returns ordered lookup candidates for slash-style callsigns.
// Examples:
//
//	EK/RX3DPK   -> EK/RX3DPK, RX3DPK, EK
//	DL7VEE/P    -> DL7VEE/P, DL7VEE, P
//	EA8/DL7VEE/P -> EA8/DL7VEE/P, DL7VEE, EA8, P
func qrzLookupCandidates(callsign string) []string {
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
func (c *qrzLookupClient) LookupLocator(callsign string) (string, error) {
	info, err := c.LookupInfo(callsign)
	return info.Locator, err
}

// LookupInfo returns locator + op name + country, cached per callsign.
func (c *qrzLookupClient) LookupInfo(callsign string) (CallsignInfo, error) {
	if c == nil {
		return CallsignInfo{}, fmt.Errorf("qrz resolver not configured")
	}

	call := strings.ToUpper(strings.TrimSpace(callsign))
	if call == "" {
		return CallsignInfo{}, fmt.Errorf("empty callsign")
	}

	now := time.Now()
	c.mu.Lock()
	if cached, ok := c.cache[call]; ok && now.Before(cached.expires) {
		c.mu.Unlock()
		return cached.info, nil
	}
	c.mu.Unlock()

	candidates := qrzLookupCandidates(call)
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
		c.cache[call] = qrzCacheEntry{info: info, expires: now.Add(24 * time.Hour)}
		c.mu.Unlock()
		return info, nil
	}

	if hadNoError {
		c.mu.Lock()
		c.cache[call] = qrzCacheEntry{info: CallsignInfo{}, expires: now.Add(20 * time.Minute)}
		c.mu.Unlock()
		return CallsignInfo{}, nil
	}

	if lastErr != nil {
		return CallsignInfo{}, lastErr
	}
	return CallsignInfo{}, nil
}

func (c *qrzLookupClient) lookupWithRetry(call string) (CallsignInfo, error) {
	if err := c.ensureSession(); err != nil {
		return CallsignInfo{}, err
	}

	info, sessionExpired, err := c.lookupCall(call)
	if err == nil {
		return info, nil
	}
	if !sessionExpired {
		return CallsignInfo{}, err
	}

	c.mu.Lock()
	c.sessionKey = ""
	c.sessionValid = time.Time{}
	c.mu.Unlock()

	if err := c.ensureSession(); err != nil {
		return CallsignInfo{}, err
	}
	info, _, err = c.lookupCall(call)
	if err != nil {
		return CallsignInfo{}, err
	}
	return info, nil
}

func (c *qrzLookupClient) ensureSession() error {
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

	var data qrzXMLResponse
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

func (c *qrzLookupClient) lookupCall(call string) (info CallsignInfo, sessionExpired bool, err error) {
	c.mu.Lock()
	session := c.sessionKey
	c.mu.Unlock()

	if strings.TrimSpace(session) == "" {
		return CallsignInfo{}, true, fmt.Errorf("qrz session unavailable")
	}

	params := url.Values{}
	params.Set("s", session)
	params.Set("callsign", call)

	resp, err := c.httpClient.Get(c.baseURL + "?" + params.Encode())
	if err != nil {
		return CallsignInfo{}, false, err
	}
	defer resp.Body.Close()

	var data qrzXMLResponse
	if err := xml.NewDecoder(resp.Body).Decode(&data); err != nil {
		return CallsignInfo{}, false, err
	}

	sessionErr := strings.ToLower(strings.TrimSpace(data.Session.Error))
	if sessionErr != "" {
		if strings.Contains(sessionErr, "session") || strings.Contains(sessionErr, "expired") || strings.Contains(sessionErr, "timeout") {
			return CallsignInfo{}, true, fmt.Errorf("qrz session expired: %s", data.Session.Error)
		}
		return CallsignInfo{}, false, fmt.Errorf("qrz lookup error: %s", data.Session.Error)
	}

	out := CallsignInfo{
		Name:    strings.TrimSpace(data.Callsign.Fname), // first name (friendly)
		Country: strings.TrimSpace(data.Callsign.Country),
	}
	if out.Name == "" {
		out.Name = strings.TrimSpace(data.Callsign.Name)
	}
	grid := strings.ToUpper(strings.TrimSpace(data.Callsign.Grid))
	if len(grid) >= 4 && isLocator(grid[:4]) {
		out.Locator = grid[:4]
	}
	return out, false, nil
}
