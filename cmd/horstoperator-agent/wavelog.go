package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// wavelogClient is a thin, caching proxy over Wavelog's POST /api/private_lookup.
// Wavelog computes the worked/confirmed matrix server-side, so the agent does
// not maintain its own log mirror — it just calls private_lookup per spot,
// caches results (TTL), and maps them to the Chase Queue's "needed" vocabulary.
//
// The API key is read-capable only and is never logged or echoed to the browser.
type wavelogClient struct {
	endpoint string // <base>/api/private_lookup
	key      string
	http     *http.Client
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]wlCacheEntry
}

type wlCacheEntry struct {
	val *wavelogLookup
	exp time.Time
}

func newWavelogClient(base, key string) *wavelogClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	return &wavelogClient{
		endpoint: base + "/api/private_lookup",
		key:      strings.TrimSpace(key),
		http:     &http.Client{Timeout: 8 * time.Second},
		ttl:      5 * time.Minute,
		cache:    map[string]wlCacheEntry{},
	}
}

// wavelogLookup mirrors the documented private_lookup response. Numeric fields
// arrive as strings (dxcc_id etc.); we only keep what the UI needs. Worked/
// confirmed flags are booleans in the documented shape.
type wavelogLookup struct {
	Callsign string `json:"callsign"`
	DXCC     string `json:"dxcc"`
	DXCCID   string `json:"dxcc_id"`
	DXCCFlag string `json:"dxcc_flag"`
	Cont     string `json:"cont"`
	Name     string `json:"name"`
	Grid     string `json:"gridsquare"`
	State    string `json:"state"`
	Bearing  string `json:"bearing"`

	CallWorked         bool `json:"call_worked"`
	CallWorkedBand     bool `json:"call_worked_band"`
	CallWorkedBandMode bool `json:"call_worked_band_mode"`

	DXCCConfirmed         bool `json:"dxcc_confirmed"`
	DXCCConfirmedBand     bool `json:"dxcc_confirmed_on_band"`
	DXCCConfirmedBandMode bool `json:"dxcc_confirmed_on_band_mode"`

	CallConfirmed bool `json:"call_confirmed"`
	LOTWMember    bool `json:"lotw_member"`
}

func wlCacheKey(call, band, mode string) string {
	return strings.ToUpper(strings.TrimSpace(call)) + "|" +
		strings.ToLower(strings.TrimSpace(band)) + "|" +
		strings.ToUpper(strings.TrimSpace(mode))
}

// Lookup returns the private_lookup result for (call, band, mode), serving a
// fresh cache entry when available.
func (c *wavelogClient) Lookup(ctx context.Context, call, band, mode string) (*wavelogLookup, error) {
	key := wlCacheKey(call, band, mode)

	c.mu.Lock()
	if e, ok := c.cache[key]; ok && e.exp.After(time.Now()) {
		c.mu.Unlock()
		return e.val, nil
	}
	c.mu.Unlock()

	payload, _ := json.Marshal(map[string]any{
		"key":      c.key,
		"callsign": strings.ToUpper(strings.TrimSpace(call)),
		"band":     strings.ToLower(strings.TrimSpace(band)),
		"mode":     strings.ToUpper(strings.TrimSpace(mode)),
		"callbook": true,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wavelog request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wavelog returned HTTP %d", resp.StatusCode)
	}

	var out wavelogLookup
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("wavelog response parse failed: %w", err)
	}

	c.mu.Lock()
	c.cache[key] = wlCacheEntry{val: &out, exp: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return &out, nil
}

// neededFromLookup maps a private_lookup result to the Chase Queue "needed"
// vocabulary. It is confirmation-based (award-oriented): Wavelog exposes entity
// *confirmed* status but not entity *worked* status, so "needed" means "not yet
// confirmed for this award slot". Collapse rule: a new entity implies new
// band/mode, so only "dxcc" is returned in that case.
func neededFromLookup(l *wavelogLookup) []string {
	if l == nil {
		return nil
	}
	if !l.DXCCConfirmed {
		return []string{"dxcc"}
	}
	needed := []string{}
	if !l.DXCCConfirmedBand {
		needed = append(needed, "band")
	} else if !l.DXCCConfirmedBandMode {
		needed = append(needed, "mode")
	}
	return needed
}

// --- enrich endpoint ---

type enrichSpotReq struct {
	ID   string `json:"id"`
	Call string `json:"call"`
	Band string `json:"band"`
	Mode string `json:"mode"`
}

type enrichRequest struct {
	PermitLookup bool            `json:"permit_lookup"`
	Spots        []enrichSpotReq `json:"spots"`
}

type dxccBlock struct {
	Entity string `json:"entity"`
	ID     string `json:"id,omitempty"`
	Cont   string `json:"cont,omitempty"`
	Flag   string `json:"flag,omitempty"`
}

type workedBlock struct {
	Worked         bool `json:"worked"`
	WorkedBand     bool `json:"worked_band"`
	WorkedBandMode bool `json:"worked_band_mode"`
}

type enrichResult struct {
	ID           string       `json:"id"`
	DXCC         *dxccBlock   `json:"dxcc,omitempty"`
	Needed       []string     `json:"needed"`
	WorkedBefore *workedBlock `json:"worked_before,omitempty"`
}

const enrichMaxSpots = 60
const enrichConcurrency = 4

func (s *server) handleEnrich(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.wavelog == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "wavelog lookup not configured (set WAVELOG_API_KEY)"})
		return
	}
	var req enrichRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !req.PermitLookup {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permit_lookup must be true"})
		return
	}
	if len(req.Spots) > enrichMaxSpots {
		req.Spots = req.Spots[:enrichMaxSpots]
	}

	// Dedupe by lookup key so repeated calls in one batch cost one upstream call.
	type pending struct {
		call, band, mode string
		ids              []string
	}
	byKey := map[string]*pending{}
	order := []string{}
	for _, sp := range req.Spots {
		if strings.TrimSpace(sp.Call) == "" {
			continue
		}
		k := wlCacheKey(sp.Call, sp.Band, sp.Mode)
		if p, ok := byKey[k]; ok {
			p.ids = append(p.ids, sp.ID)
			continue
		}
		byKey[k] = &pending{call: sp.Call, band: sp.Band, mode: sp.Mode, ids: []string{sp.ID}}
		order = append(order, k)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	type outcome struct {
		key string
		res *wavelogLookup
		err error
	}
	sem := make(chan struct{}, enrichConcurrency)
	results := make(chan outcome, len(order))
	var wg sync.WaitGroup
	for _, k := range order {
		p := byKey[k]
		wg.Add(1)
		go func(key string, p *pending) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.wavelog.Lookup(ctx, p.call, p.band, p.mode)
			results <- outcome{key: key, res: res, err: err}
		}(k, p)
	}
	go func() { wg.Wait(); close(results) }()

	enriched := map[string]*wavelogLookup{}
	degraded := false
	for o := range results {
		if o.err != nil {
			degraded = true // transport/5xx failure → UI shows spots without chips
			continue
		}
		enriched[o.key] = o.res
	}

	out := make([]enrichResult, 0, len(req.Spots))
	for _, sp := range req.Spots {
		if strings.TrimSpace(sp.Call) == "" {
			continue
		}
		l := enriched[wlCacheKey(sp.Call, sp.Band, sp.Mode)]
		er := enrichResult{ID: sp.ID, Needed: []string{}}
		if l != nil {
			if l.DXCC != "" {
				er.DXCC = &dxccBlock{Entity: l.DXCC, ID: l.DXCCID, Cont: l.Cont, Flag: l.DXCCFlag}
			}
			er.Needed = neededFromLookup(l)
			er.WorkedBefore = &workedBlock{
				Worked:         l.CallWorked,
				WorkedBand:     l.CallWorkedBand,
				WorkedBandMode: l.CallWorkedBandMode,
			}
		}
		out = append(out, er)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "degraded": degraded, "results": out})
}
