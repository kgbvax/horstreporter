// Package feed implements PropFeedSource: it consumes HorstReporter's SSE stream
// read-only and maps each locator-level streamSpot into a propcontract.PropReport,
// dropping dxcluster-sourced spots. It never writes to HorstReporter.
package feed

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"horstreporter/internal/propcontract"
)

// streamSpot mirrors HorstReporter's toStreamSpot wire shape (server.go).
// NOTE: locator-level only — no callsigns, freq, or mode.
type streamSpot struct {
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	SNR             int     `json:"snr"`
	AgeSeconds      int64   `json:"ageSeconds"`
	Locator         string  `json:"locator"`
	ReporterLocator string  `json:"reporterLocator"`
	SourceType      string  `json:"sourceType"`
	Band            string  `json:"band"`
}

// mapSpot converts a streamSpot to a PropReport relative to the ingest time.
// ok=false for dxcluster spots or spots missing the data Layer 1 needs.
func mapSpot(s streamSpot, now time.Time) (propcontract.PropReport, bool) {
	if strings.EqualFold(s.SourceType, "dxcluster") {
		return propcontract.PropReport{}, false // never use cluster spots
	}
	if s.Band == "" || s.Locator == "" {
		return propcontract.PropReport{}, false
	}
	src := s.SourceType
	if src == "" {
		src = "mqtt"
	}
	return propcontract.PropReport{
		TxLocator:  strings.ToUpper(s.Locator),
		RxLocator:  strings.ToUpper(s.ReporterLocator),
		SNRDb:      s.SNR,
		Band:       s.Band,
		Source:     src,
		ObservedAt: now.Add(-time.Duration(s.AgeSeconds) * time.Second),
	}, true
}

// Source is a read-only SSE consumer of HorstReporter's stream.
type Source struct {
	URL    string
	Client *http.Client
	Now    func() time.Time // injectable clock (defaults to time.Now)
}

// New builds a Source with sane defaults.
func New(url string) *Source {
	return &Source{URL: url, Client: &http.Client{}, Now: time.Now}
}

// Run connects to the SSE stream and invokes handle for every mapped PropReport
// until the context is cancelled or the stream ends. It is the caller's job to
// reconnect on return (resilience).
func (s *Source) Run(ctx context.Context, handle func(propcontract.PropReport)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return s.consume(resp.Body, handle)
}

// consume parses an SSE byte stream. Exposed shape is testable without a network.
func (s *Source) consume(r interface{ Read([]byte) (int, error) }, handle func(propcontract.PropReport)) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue // skip "event:" lines, comments, blanks
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "{}" {
			continue // history_end sentinel etc.
		}
		var sp streamSpot
		if err := json.Unmarshal([]byte(data), &sp); err != nil {
			continue // tolerate malformed lines
		}
		if pr, ok := mapSpot(sp, now()); ok {
			handle(pr)
		}
	}
	return sc.Err()
}
