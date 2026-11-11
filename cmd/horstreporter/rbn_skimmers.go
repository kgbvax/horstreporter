package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"horstreporter/internal/maidenhead"
)

// RBN skimmer locators. The telnet spot line names the skimmer by call only;
// RBN publishes every skimmer with its grid on the status page (an HTML table:
// call, bands, grid, prefix, continent, ...). The list is a few hundred rows
// and changes rarely, so it is fetched at startup and every few hours.

const (
	rbnSkimmerListURL      = "https://www.reversebeacon.net/cont_includes/status.php?t=skt"
	rbnSkimmerListInterval = 6 * time.Hour
	rbnSkimmerRetryDelay   = 10 * time.Minute
	rbnSkimmerFetchTimeout = 15 * time.Second
	rbnSkimmerMaxBody      = 4 << 20
)

type rbnSkimmerList struct {
	mu   sync.RWMutex
	locs map[string]string // normalized skimmer call -> upper-case locator
	// fetchedAt is the unix second of the last successful fetch (0: never).
	fetchedAt atomic.Int64
	failures  atomic.Int64
}

var rbnSkimmers = &rbnSkimmerList{}

var (
	rbnRowPattern  = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	rbnCellPattern = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
	rbnTagPattern  = regexp.MustCompile(`(?s)<[^>]+>`)
)

// parseRBNSkimmerList reads the status table: cell 0 is the skimmer call,
// cell 2 its grid. Rows without a valid grid are skipped.
func parseRBNSkimmerList(page string) map[string]string {
	out := make(map[string]string, 512)
	for _, row := range rbnRowPattern.FindAllStringSubmatch(page, -1) {
		cells := rbnCellPattern.FindAllStringSubmatch(row[1], 4)
		if len(cells) < 3 {
			continue
		}
		call := normalizeSkimmerCall(cellText(cells[0][1]))
		loc := maidenhead.Normalize(cellText(cells[2][1]))
		if call == "" || loc == "" {
			continue
		}
		out[call] = loc
	}
	return out
}

func cellText(s string) string {
	return strings.TrimSpace(html.UnescapeString(rbnTagPattern.ReplaceAllString(s, "")))
}

// normalizeSkimmerCall maps a skimmer call to the status page's key: upper
// case without the telnet relay's "-#" marker ("W3LPL-#" -> "W3LPL",
// "VE6WZ-4-#" -> "VE6WZ-4").
func normalizeSkimmerCall(call string) string {
	call = strings.ToUpper(strings.TrimSpace(call))
	return strings.TrimSuffix(call, "-#")
}

// skimmerBaseCall drops a "-<n>" receiver suffix ("VE6WZ-4" -> "VE6WZ").
func skimmerBaseCall(call string) string {
	if i := strings.LastIndexByte(call, '-'); i > 0 {
		return call[:i]
	}
	return call
}

// lookup returns the grid for a skimmer call as it appears in the telnet line
// (already upper case): the exact entry, else the same station without its
// receiver number. It allocates nothing for the common "-#" forms.
func (l *rbnSkimmerList) lookup(skimmer string) (string, bool) {
	call := strings.TrimSuffix(skimmer, "-#")
	l.mu.RLock()
	defer l.mu.RUnlock()
	if loc, ok := l.locs[call]; ok {
		return loc, true
	}
	if base := skimmerBaseCall(call); base != call {
		if loc, ok := l.locs[base]; ok {
			return loc, true
		}
	}
	return "", false
}

func (l *rbnSkimmerList) size() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.locs)
}

func (l *rbnSkimmerList) set(locs map[string]string, now int64) {
	l.mu.Lock()
	l.locs = locs
	l.mu.Unlock()
	l.fetchedAt.Store(now)
}

// fetch downloads and parses the list once. An empty parse counts as a
// failure (the page changed shape), so the previous list stays.
func (l *rbnSkimmerList) fetch(client *http.Client, url string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "horstreporter")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, rbnSkimmerMaxBody))
	if err != nil {
		return err
	}
	locs := parseRBNSkimmerList(string(body))
	if len(locs) == 0 {
		return fmt.Errorf("no skimmer rows with a grid")
	}
	l.set(locs, time.Now().Unix())
	return nil
}

// startRBNSkimmerList refreshes the list forever. Failures retry sooner and
// keep the last good list; only the first failure after a success is logged.
func startRBNSkimmerList(url string) {
	client := &http.Client{Timeout: rbnSkimmerFetchTimeout}
	logged := false
	for {
		wait := rbnSkimmerListInterval
		if err := rbnSkimmers.fetch(client, url); err != nil {
			rbnSkimmers.failures.Add(1)
			if !logged {
				logInfo("RBN skimmer list fetch failed (%s): %v; keeping %d skimmers", url, err, rbnSkimmers.size())
				logged = true
			}
			wait = rbnSkimmerRetryDelay
		} else {
			logInfo("RBN skimmer list: %d skimmers with a grid", rbnSkimmers.size())
			logged = false
		}
		time.Sleep(wait)
	}
}
