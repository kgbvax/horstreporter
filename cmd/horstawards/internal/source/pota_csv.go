package source

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"horstreporter/cmd/horstawards/internal/award"
)

// POTACSV is the recommended POTA progress source: the operator exports their
// hunted-parks list from POTA as CSV and points horstawards at the file. Unlike
// the public POTA API (counts only), the CSV is the authoritative, complete list
// of parks hunted — so it can drive POTA "wanted" with no guessing.
//
// The parser is tolerant of column layout: it uses a column whose header looks
// like a park reference ("reference"/"ref"/"park") when present, otherwise scans
// every cell for a POTA-ref token. Each distinct reference becomes a hunted (=
// worked) park slot.
type POTACSV struct {
	path     string
	interval time.Duration
}

// potaRefRe matches a POTA park reference: a letter-led prefix, a dash, 4-6
// digits (e.g. K-0817, KH6-0123, VK-1234).
var potaRefRe = regexp.MustCompile(`(?i)\b([A-Z][A-Z0-9]{0,3}-\d{4,6})\b`)

// NewPOTACSV builds the file source.
func NewPOTACSV(path string, interval time.Duration) *POTACSV {
	if interval <= 0 {
		interval = time.Hour
	}
	return &POTACSV{path: strings.TrimSpace(path), interval: interval}
}

func (s *POTACSV) Name() string               { return "pota-csv" }
func (s *POTACSV) Programs() []award.Program  { return []award.Program{award.ProgPOTA} }
func (s *POTACSV) MinInterval() time.Duration { return s.interval }

// Refresh re-reads the CSV file and folds it into POTA park slots.
func (s *POTACSV) Refresh(ctx context.Context, prev *Snapshot) (*Snapshot, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("pota csv open %q: %w", s.path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows
	r.LazyQuotes = true

	slots := map[string]award.SlotStatus{}
	stats := map[string]int{}
	refCol := -1
	row := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("pota csv parse %q: %w", s.path, err)
		}
		row++
		if row == 1 {
			refCol = referenceColumn(rec)
			if refCol >= 0 {
				continue // header row consumed
			}
			// No header match — fall through and treat row 1 as data (scan cells).
		}
		ref := extractRef(rec, refCol)
		if ref == "" {
			continue
		}
		putSlot(slots, award.Slot{Program: award.ProgPOTA, Entity: ref}, award.StatusWorked)
		stats["pota_parks"]++
	}

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

// referenceColumn returns the index of a header column naming the park reference,
// or -1 if none looks like one.
func referenceColumn(header []string) int {
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "reference", "ref", "park", "park reference", "hunted reference":
			return i
		}
	}
	return -1
}

// extractRef pulls a POTA ref from a row: the reference column if known, else the
// first cell containing a POTA-ref token.
func extractRef(rec []string, refCol int) string {
	if refCol >= 0 && refCol < len(rec) {
		if m := potaRefRe.FindStringSubmatch(rec[refCol]); m != nil {
			return strings.ToUpper(m[1])
		}
		// A reference column was identified but this cell has no ref-shaped value.
		return ""
	}
	for _, cell := range rec {
		if m := potaRefRe.FindStringSubmatch(cell); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}
