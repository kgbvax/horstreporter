package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/proplab"
)

// eventcal.go polls free contest/DXpedition/POTA calendars for the Propagation
// Lab Fusion engine. It runs only when -proplab-events-enable is set. Events are
// stored in proplab_events and used by Fusion to mark demand-side spikes as
// "closed_but_active" rather than propagation openings.
//
// Feeds (all public, no API key):
//   - WA7BNM contest calendar RSS
//   - NG3K ADXO DXpedition list (HTML scrape, robust to minor format changes)
//   - POTA activator spots API (live spots, mapped to short event windows)

const (
	eventcalContestRSS = "https://www.contestcalendar.com/fcccalendars/weeklycontest.rss"
	eventcalDxpedRSS   = "https://www.ng3k.com/Misc/adxo.xml"
	eventcalPotaURL    = "https://api.pota.app/spot/activator"
)

var (
	// bandRangePattern matches tokens like "80m-10m", "160m-6m", etc.
	bandRangePattern = regexp.MustCompile(`(?i)(?:\b(?:160|80|60|40|30|20|17|15|12|10|6|4|2)m\b(?:\s*/\s*(?:cw|ssb|data|digital))?\s*)?(?:-\s*(?:160|80|60|40|30|20|17|15|12|10|6|4|2)m\b)`)
	// singleBandPattern matches single band mentions.
	singleBandPattern = regexp.MustCompile(`(?i)\b(160|80|60|40|30|20|17|15|12|10|6|4|2)m\b`)
)

type eventcalService struct {
	mu     sync.RWMutex
	store  *dxPostgresStore
	stopCh chan struct{}
	wg     sync.WaitGroup
	client *http.Client
}

func startProplabEventIngest() {
	svc := &eventcalService{
		store:  proplabService.store,
		stopCh: make(chan struct{}),
		client: &http.Client{Timeout: 30 * time.Second},
	}
	svc.start()
}

func (s *eventcalService) start() {
	if s.store == nil {
		logInfo("Proplab event-calendar ingest disabled: no Postgres store")
		return
	}

	s.fetchAll()

	feeds := []struct {
		name     string
		interval time.Duration
		fn       func()
	}{
		{"contest-rss", 60 * time.Minute, s.fetchContests},
		{"dxped-rss", 60 * time.Minute, s.fetchDxpeds},
		{"pota", 5 * time.Minute, s.fetchPota},
	}

	for _, f := range feeds {
		s.wg.Add(1)
		go func(f struct {
			name     string
			interval time.Duration
			fn       func()
		}) {
			defer s.wg.Done()
			ticker := time.NewTicker(f.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					f.fn()
				case <-s.stopCh:
					return
				}
			}
		}(f)
	}
}

func (s *eventcalService) fetchAll() {
	s.fetchContests()
	s.fetchDxpeds()
	s.fetchPota()
}

func (s *eventcalService) fetchContests() {
	body, err := httpGet(s.client, eventcalContestRSS)
	if err != nil {
		logInfo("Event calendar contest fetch failed: %v", err)
		return
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		logInfo("Event calendar contest parse failed: %v", err)
		return
	}
	now := time.Now().Unix()
	rows := make([]proplabEventRow, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		start, end, ok := parseEventWindow(item.Title, item.PubDate, item.Description, now)
		if !ok {
			continue
		}
		rows = append(rows, proplabEventRow{
			Source:   "wa7bnm",
			EventID:  item.Link,
			Title:    strings.TrimSpace(item.Title),
			BandMask: extractBandMask(item.Title + " " + item.Description),
			StartUTC: start,
			EndUTC:   end,
			Locator4: "",
		})
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabEvents(ctx, rows); err != nil {
		logInfo("Event calendar contest store failed: %v", err)
	}
}

func (s *eventcalService) fetchDxpeds() {
	body, err := httpGet(s.client, eventcalDxpedRSS)
	if err != nil {
		logInfo("Event calendar DXped fetch failed: %v", err)
		return
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		logInfo("Event calendar DXped parse failed: %v", err)
		return
	}
	now := time.Now().Unix()
	rows := make([]proplabEventRow, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		start, end, ok := parseEventWindow(item.Title, item.PubDate, item.Description, now)
		if !ok {
			continue
		}
		rows = append(rows, proplabEventRow{
			Source:   "ng3k",
			EventID:  item.Link,
			Title:    strings.TrimSpace(item.Title),
			BandMask: extractBandMask(item.Title + " " + item.Description),
			StartUTC: start,
			EndUTC:   end,
			Locator4: extractLocator(item.Description),
		})
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabEvents(ctx, rows); err != nil {
		logInfo("Event calendar DXped store failed: %v", err)
	}
}

// potaSpot is a single activator spot from the public POTA API.
type potaSpot struct {
	SpotID       int         `json:"spotId"`
	Activator    string      `json:"activator"`
	Reference    string      `json:"reference"`
	Frequency    json.Number `json:"frequency"` // kHz; served as string by the API
	Mode         string      `json:"mode"`
	Grid4        string      `json:"grid4"`
	SpotTime     string      `json:"spotTime"`  // local ISO8601, e.g. "2026-07-31T19:26:08"
	Expire       int         `json:"expire"`    // seconds remaining, if provided
	LocationDesc string      `json:"locationDesc"`
}

func (s *eventcalService) fetchPota() {
	body, err := httpGet(s.client, eventcalPotaURL)
	if err != nil {
		logInfo("Event calendar POTA fetch failed: %v", err)
		return
	}
	var spots []potaSpot
	if err := json.Unmarshal(body, &spots); err != nil {
		logInfo("Event calendar POTA parse failed: %v", err)
		return
	}
	now := time.Now().Unix()
	rows := make([]proplabEventRow, 0, len(spots))
	for _, sp := range spots {
		freqKHz, err := strconv.ParseFloat(sp.Frequency.String(), 64)
		if err != nil {
			continue
		}
		band := proplab.BandForFrequencyKHz(freqKHz)
		if band == "" {
			continue
		}
		start, _ := parsePotaSpotTime(sp.SpotTime)
		if start == 0 {
			start = now
		}
		// Treat the spot as active for its reported expire time or 30 minutes,
		// whichever is longer, so a flurry of spots keeps the event visible.
		end := start + 30*60
		if sp.Expire > 0 && int64(sp.Expire) > 30*60 {
			end = start + int64(sp.Expire)
		}
		if end < now {
			continue
		}
		rows = append(rows, proplabEventRow{
			Source:   "pota",
			EventID:  sp.Reference + "/" + sp.Activator + "/" + strconv.Itoa(sp.SpotID),
			Title:    sp.Activator + " @ " + sp.Reference,
			BandMask: band,
			StartUTC: start,
			EndUTC:   end,
			Locator4: strings.ToUpper(strings.TrimSpace(sp.Grid4)),
		})
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabEvents(ctx, rows); err != nil {
		logInfo("Event calendar POTA store failed: %v", err)
	}
}

func parsePotaSpotTime(s string) (int64, bool) {
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// rssFeed is a minimal RSS 2.0 envelope.
type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// parseEventWindow extracts a start/end time from the event text. RSS feeds for
// contests often encode the weekend in the title or description. We fall back
// to a 48-hour window around any parsed date so the event is visible even when
// the upstream format is vague.
func parseEventWindow(title, pubDate, description string, now int64) (start, end int64, ok bool) {
	text := title + " " + description + " " + pubDate
	var t time.Time
	var found bool
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC822,
		time.RFC822Z,
		"2006-01-02",
		"02 Jan 2006",
		"Jan 02, 2006",
	} {
		if tt, err := time.Parse(layout, strings.TrimSpace(pubDate)); err == nil {
			t = tt
			found = true
			break
		}
	}
	if !found {
		// Try to find a YYYY-MM-DD anywhere in the text.
		if m := regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`).FindStringSubmatch(text); m != nil {
			if tt, err := time.Parse("2006-01-02", m[1]); err == nil {
				t = tt
				found = true
			}
		}
	}
	if !found {
		// Fallback: use current UTC midnight and assume the event spans it.
		t = time.Unix(now, 0).UTC().Truncate(24 * time.Hour)
	}

	start = t.Unix()
	end = start + 48*60*60
	if end < now-int64(7*24*60*60) {
		// Event is more than a week old and already ended; skip.
		return 0, 0, false
	}
	return start, end, true
}

func extractBandMask(text string) string {
	var ranges []string
	seen := make(map[string]bool)
	for _, m := range bandRangePattern.FindAllString(text, -1) {
		m = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(m), " ", ""))
		if !seen[m] {
			seen[m] = true
			ranges = append(ranges, m)
		}
	}
	if len(ranges) > 0 {
		return strings.Join(ranges, ", ")
	}
	// No range found; collect individual bands.
	var bands []string
	for _, m := range singleBandPattern.FindAllStringSubmatch(text, -1) {
		b := strings.ToLower(m[1]) + "m"
		if !seen[b] {
			seen[b] = true
			bands = append(bands, b)
		}
	}
	if len(bands) > 0 {
		return strings.Join(bands, ", ")
	}
	return ""
}

func extractLocator(text string) string {
	// Look for a 4- or 6-char Maidenhead locator.
	if m := regexp.MustCompile(`\b([A-Ra-r]{2}\d{2}([a-xa-x]{2})?)\b`).FindStringSubmatch(text); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// eventBandMatches (in proplab_fusion.go) already handles single bands and
// "80m-10m" style ranges. The comma-joined mask produced here is compatible.
