package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"horstreporter/cmd/horstawards/internal/adif"
	"horstreporter/cmd/horstawards/internal/award"
	"horstreporter/cmd/horstawards/internal/refdata"
)

// WavelogADIF pulls the operator's full log from Wavelog's get_contacts_adif API
// and computes DXCC + WAS progress locally. It paginates with fetchfromid until
// the server returns no more QSOs, so a single Refresh always reflects the whole
// log — which also catches QSL upgrades on old QSOs for free. (Incremental
// since-cursor sync is a future optimization; full pull is the always-correct v1.)
//
// The API key is read-capable only and is never logged or echoed.
type WavelogADIF struct {
	endpoint  string // <base>/api/get_contacts_adif
	key       string
	stationID string
	http      *http.Client
	interval  time.Duration

	// maxPages bounds pagination as a runaway guard; 0 = unbounded.
	maxPages int
}

// NewWavelogADIF builds the adapter. base is the Wavelog base URL (…/index.php).
func NewWavelogADIF(base, key, stationID string, interval time.Duration) *WavelogADIF {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &WavelogADIF{
		endpoint:  base + "/api/get_contacts_adif",
		key:       strings.TrimSpace(key),
		stationID: strings.TrimSpace(stationID),
		http:      &http.Client{Timeout: 60 * time.Second, CheckRedirect: refuseCrossHostRedirect},
		interval:  interval,
		maxPages:  1000,
	}
}

func (s *WavelogADIF) Name() string { return "wavelog-adif" }

// Programs reports the programs this source can POTENTIALLY cover. Actual
// per-refresh coverage is narrower: Refresh only declares POTA coverage when the
// log actually contains POTA QSOs (otherwise a log with no POTA activity would
// mark POTA loaded with no real baseline).
func (s *WavelogADIF) Programs() []award.Program {
	return []award.Program{award.ProgDXCC, award.ProgWAS, award.ProgPOTA}
}
func (s *WavelogADIF) MinInterval() time.Duration { return s.interval }

// wlExportResp is the get_contacts_adif JSON envelope. Wavelog stringifies
// numeric fields (DCLNext returns lastfetchedid/exported_qsos as JSON strings),
// so those use flexInt to accept either a string or a number.
type wlExportResp struct {
	ExportedQSOs  flexInt `json:"exported_qsos"`
	LastFetchedID flexInt `json:"lastfetchedid"`
	Message       string  `json:"message"`
	ADIF          string  `json:"adif"`
}

// flexInt is an int64 that unmarshals from a JSON number OR a JSON string
// ("123"), tolerating Wavelog's stringified numerics.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
		return nil
	}
	ff, err := strconv.ParseFloat(s, 64) // tolerate "2.0"
	if err != nil {
		return fmt.Errorf("flexInt: cannot parse %q", s)
	}
	*f = flexInt(int64(ff))
	return nil
}

// Refresh pulls the full log (paginated) and folds it into DXCC + WAS slots.
func (s *WavelogADIF) Refresh(ctx context.Context, prev *Snapshot) (*Snapshot, error) {
	slots := map[string]award.SlotStatus{}
	stats := map[string]int{}
	var fetchFrom int64 // 0 = from the beginning (full pull)
	pages := 0

	for {
		pages++
		if s.maxPages > 0 && pages > s.maxPages {
			return nil, fmt.Errorf("wavelog adif: exceeded %d pages (cursor stuck at id %d)", s.maxPages, fetchFrom)
		}
		resp, err := s.fetchPage(ctx, fetchFrom)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(resp.ADIF) != "" {
			if err := adif.Parse(strings.NewReader(resp.ADIF), func(rec adif.Record) {
				foldRecord(rec, slots, stats)
			}); err != nil {
				return nil, fmt.Errorf("wavelog adif: parse failed: %w", err)
			}
		}
		stats["qso_pages"] = pages
		last := int64(resp.LastFetchedID)
		if resp.ExportedQSOs <= 0 {
			fetchFrom = last
			break
		}
		// Cursor failed to advance though the server still reports QSOs. This is the
		// expected shape for an instance that ignores fetchfromid (page 1 already
		// holds the whole log), but it is AMBIGUOUS with a genuinely-paginating
		// instance that returns a bad cursor (which would silently truncate the
		// log). Stop, but record it so the truncation risk is observable.
		if last <= fetchFrom {
			stats["cursor_stalled"] = 1
			log.Printf("horstawards: wavelog adif cursor did not advance (id=%d, exported=%d on page %d) — treating page as the full log; if your Wavelog paginates this would truncate, set WAVELOG_STATION_ID / check the instance", last, int(resp.ExportedQSOs), pages)
			fetchFrom = last
			break
		}
		fetchFrom = last
	}

	// Declare POTA coverage only when the log actually contains POTA QSOs —
	// otherwise a log with no POTA activity would mark POTA "loaded" and (once a
	// park ref is plumbed) flag parks as wanted with no real progress baseline.
	programs := []award.Program{award.ProgDXCC, award.ProgWAS}
	if stats["pota_hunts"] > 0 {
		programs = append(programs, award.ProgPOTA)
	}

	return &Snapshot{
		Source:   s.Name(),
		TakenAt:  time.Now().UTC(),
		Programs: programs,
		Slots:    foldSlots(slots),
		Cursor:   fmt.Sprintf("%d", fetchFrom),
		Stats:    stats,
	}, nil
}

func (s *WavelogADIF) fetchPage(ctx context.Context, fetchFrom int64) (*wlExportResp, error) {
	payload := map[string]any{
		"key":         s.key,
		"fetchfromid": fetchFrom,
	}
	if s.stationID != "" {
		payload["station_id"] = s.stationID
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wavelog adif request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20)) // 64 MiB cap per page
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Include a short snippet of the body to aid diagnosis (e.g. a 400 when
		// station_id is missing). The body is a Wavelog error message, not a secret.
		return nil, fmt.Errorf("wavelog adif returned HTTP %d: %s", resp.StatusCode, snippet(raw, 200))
	}
	var out wlExportResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("wavelog adif response parse failed: %w", err)
	}
	return &out, nil
}

// foldRecord derives DXCC + WAS slots from one QSO and merges them into slots.
func foldRecord(rec adif.Record, slots map[string]award.SlotStatus, stats map[string]int) {
	stats["qso_count"]++
	status := recordStatus(rec)
	if status == award.StatusConfirmed {
		stats["confirmed_count"]++
	}

	band := refdata.NormBand(rec.Get("BAND"))
	if band == "" {
		band = refdata.BandFromFreqStr(rec.Get("FREQ"))
	}
	modeCl := refdata.ModeClass(rec.Get("SUBMODE"))
	if modeCl == "" {
		modeCl = refdata.ModeClass(rec.Get("MODE"))
	}

	// DXCC slots: overall + per-band + per-band-mode (each at this QSO's status).
	// DXCC "0" means no/deleted/unresolvable entity — not a real entity — skip it.
	if dxcc := strings.TrimSpace(rec.Get("DXCC")); dxcc != "" && dxcc != "0" {
		putSlot(slots, award.Slot{Program: award.ProgDXCC, Entity: dxcc}, status)
		if band != "" {
			putSlot(slots, award.Slot{Program: award.ProgDXCC, Entity: dxcc, Band: band}, status)
			if modeCl != "" {
				putSlot(slots, award.Slot{Program: award.ProgDXCC, Entity: dxcc, Band: band, ModeCl: modeCl}, status)
			}
		}

		// WAS slots: overall + per-band, only for US-entity QSOs with a valid state.
		if refdata.IsUSEntity(dxcc) {
			if st, ok := refdata.NormState(rec.Get("STATE")); ok {
				putSlot(slots, award.Slot{Program: award.ProgWAS, Entity: st}, status)
				if band != "" {
					putSlot(slots, award.Slot{Program: award.ProgWAS, Entity: st, Band: band}, status)
				}
			}
		}
	}

	// POTA park-hunt slots from the log. A hunter QSO records the worked station's
	// park in SIG_INFO when SIG=POTA; some logs use POTA_REF. Hunting counts at
	// WORKED (no confirmation needed). A field can carry multiple comma-separated
	// refs (multi-park "n-fer") — split them.
	for _, ref := range potaRefsFromRecord(rec) {
		putSlot(slots, award.Slot{Program: award.ProgPOTA, Entity: ref}, award.StatusWorked)
		stats["pota_hunts"]++
	}
}

// potaRefsFromRecord extracts the hunted park reference(s) from a QSO record.
func potaRefsFromRecord(rec adif.Record) []string {
	var raw string
	if strings.EqualFold(rec.Get("SIG"), "POTA") {
		raw = rec.Get("SIG_INFO")
	}
	if raw == "" {
		raw = rec.Get("POTA_REF")
	}
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if r := strings.ToUpper(strings.TrimSpace(part)); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// recordStatus classifies a QSO as confirmed (any of QSL/LoTW/eQSL received) or
// worked. ADIF *_QSL_RCVD is "Y" when confirmed.
func recordStatus(rec adif.Record) award.Status {
	if isY(rec.Get("QSL_RCVD")) || isY(rec.Get("LOTW_QSL_RCVD")) || isY(rec.Get("EQSL_QSL_RCVD")) {
		return award.StatusConfirmed
	}
	return award.StatusWorked
}

func isY(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "Y") }

// snippet returns up to n bytes of b as a single-line string for error messages.
func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
