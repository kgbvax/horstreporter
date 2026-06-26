package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"horstreporter/cmd/horstawards/internal/award"
)

// POTA is an OPTIONAL, supplemental progress source: it pulls the operator's
// hunted parks from the POTA API to catch POTA-credited hunts that may not be in
// the local log. POTA park-hunt already works from the Wavelog ADIF (a hunter QSO
// records the park ref); this source only adds park coverage on top.
//
// It is best-effort and fail-soft: any HTTP/parse failure returns an error, the
// registry logs and skips it, and POTA wanted continues to work from the log.
//
// NOTE: the PUBLIC api.pota.app/profile/{call} endpoint returns only aggregate
// counts (stats.hunter.parks) plus a short recent_activity list — NOT a full
// per-callsign hunted-park list. So against the public profile this adapter finds
// no parks array, declares no coverage, and POTA "wanted" is driven entirely by
// the operator's own log (the wavelog-adif source). The adapter remains useful if
// pointed (via POTA_BASE_URL + an auth POTA_TOKEN) at an endpoint that returns a
// full parks array of {reference}; it only marks coverage when it parses one, so a
// counts-only or mismatched response never produces false "wanted".
type POTA struct {
	base     string
	call     string
	token    string // optional bearer; from env only; never logged
	http     *http.Client
	interval time.Duration
	pathTmpl string // profile path; "{call}" is substituted
}

// NewPOTA builds the adapter. call must be non-empty (caller gates on config).
func NewPOTA(base, call, token string, interval time.Duration) *POTA {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return &POTA{
		base:     base,
		call:     strings.ToUpper(strings.TrimSpace(call)),
		token:    strings.TrimSpace(token),
		http:     &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseCrossHostRedirect},
		interval: interval,
		pathTmpl: "/profile/{call}",
	}
}

func (s *POTA) Name() string               { return "pota" }
func (s *POTA) Programs() []award.Program  { return []award.Program{award.ProgPOTA} }
func (s *POTA) MinInterval() time.Duration { return s.interval }

// potaPark is one hunted park in the assumed response shape.
type potaPark struct {
	Reference string `json:"reference"`
}

// potaProfile is tolerant of a few plausible response shapes for the parks list.
type potaProfile struct {
	Callsign    string     `json:"callsign"`
	HunterParks []potaPark `json:"hunter_parks"`
	Parks       []potaPark `json:"parks"`
	Hunter      struct {
		Parks []potaPark `json:"parks"`
	} `json:"hunter"`
}

func (p potaProfile) parkList() []potaPark {
	switch {
	case len(p.HunterParks) > 0:
		return p.HunterParks
	case len(p.Hunter.Parks) > 0:
		return p.Hunter.Parks
	default:
		return p.Parks
	}
}

// Refresh pulls the hunter's parks and folds them into POTA slots.
func (s *POTA) Refresh(ctx context.Context, prev *Snapshot) (*Snapshot, error) {
	path := strings.ReplaceAll(s.pathTmpl, "{call}", s.call)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pota request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("pota returned 401 (set POTA_TOKEN or check callsign); disabling this refresh")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pota returned HTTP %d", resp.StatusCode)
	}

	var prof potaProfile
	if err := json.Unmarshal(raw, &prof); err != nil {
		return nil, fmt.Errorf("pota response parse failed: %w", err)
	}
	parks := prof.parkList()

	slots := map[string]award.SlotStatus{}
	stats := map[string]int{}
	for _, pk := range parks {
		ref := strings.ToUpper(strings.TrimSpace(pk.Reference))
		if ref == "" {
			continue
		}
		putSlot(slots, award.Slot{Program: award.ProgPOTA, Entity: ref}, award.StatusWorked)
		stats["pota_parks"]++
	}

	// Declare POTA coverage only when we actually parsed a parks list, so a schema
	// mismatch (empty parse) never marks POTA loaded with no baseline.
	programs := []award.Program{}
	if stats["pota_parks"] > 0 {
		programs = s.Programs()
	}

	return &Snapshot{
		Source:   s.Name(),
		TakenAt:  time.Now().UTC(),
		Programs: programs,
		Slots:    foldSlots(slots),
		Stats:    stats,
	}, nil
}
