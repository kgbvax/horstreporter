package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// almanac_wspr_backfill.go fills the seasonal record's WSPR layer (plan U5,
// KTD8, R10/R11) from the wspr.live ClickHouse archive for the configured
// areas. It follows the prop_baseline_backfill.go pattern: a resumable
// background job with dx_meta keys, off by default, never triggered by a
// visitor request.
//
//   - Areas: -almanac-wspr-backfill-areas (grid4 list). Each area uses the
//     full widening ring (almanacMaxWidenRadius, rings 0–2), so any radius the
//     Almanac later picks is covered.
//   - Months: complete months only, newest first, back
//     -almanac-wspr-backfill-years years, never the current month and never
//     before 2008-03 (the archive start).
//   - Per UTC day two GETs (only a query= parameter): the ring aggregate by
//     (slot, band, rx4, tx4) and a global per-slot count (the WSPR
//     ingest-alive totals). Both orientations are emitted in Go, like
//     dx_region_baseline_daily: (rx4, tx-region) and (tx4, rx-region). Far-end
//     grid4s map to regions via internal/region.
//   - A ClickHouse timeout (code 159) or an oversized response splits the day
//     into hourly windows instead of re-running the identical query.
//   - Every request is paced under wspr.live's 20 req/min; failures back off
//     exponentially and 3 consecutive failures abort the run (the failed
//     month is not committed; a restart redoes it from scratch).
//   - A month commits in one transaction: DELETE the WSPR layer for (ring
//     grid4s, month), INSERT, set the month-done key — replace, never add.
//   - The job refuses to start (and stops before the next month) when the
//     database disk is over almanacDiskFullThreshold (fail-safe probe).

const (
	// almanacSeasonLayerWSPR is the seasonal-record layer filled by this
	// backfill (the drill-down labels months from it as WSPR).
	almanacSeasonLayerWSPR = "wspr"

	almanacWSPRDefaultEndpoint = "https://db1.wspr.live"
	// almanacWSPRPauseAfterRequest is the pause between the end of one
	// request and the start of the next (be gentle with the volunteer-run
	// wspr.live service).
	almanacWSPRPauseAfterRequest = 2 * time.Second
	// almanacWSPRMaxPerMinute caps backfill requests in any rolling minute,
	// so fast responses (2 s pause + short query) plus the live poller's
	// ~1/min stay under the documented 20 req/min wspr.live limit.
	almanacWSPRMaxPerMinute = 18
	// almanacWSPRBackoffBase is the first retry delay; it doubles per
	// consecutive failure.
	almanacWSPRBackoffBase = 15 * time.Second
	// almanacWSPRMaxConsecutiveFailures aborts the run.
	almanacWSPRMaxConsecutiveFailures = 3
	// almanacWSPRHTTPTimeout is the backfill client's own (long) timeout.
	almanacWSPRHTTPTimeout = 5 * time.Minute
	// almanacWSPRMaxRowsPerResponse bounds one decoded response (memory); a
	// day above it is split into hours.
	almanacWSPRMaxRowsPerResponse = 250000
	// almanacWSPRMaxURLBytes: wspr.live rejects long GET URLs.
	almanacWSPRMaxURLBytes = 8192
	// almanacWSPRCommitTimeout bounds the month transaction.
	almanacWSPRCommitTimeout = 2 * time.Minute
	// almanacWSPRArchiveStart is the first month in the wspr.live archive.
	almanacWSPRArchiveStart = 200803

	almanacWSPRLastErrorKey = "almanac_wspr_backfill_last_error"

	clickHouseCodeTimeout      = 159
	clickHouseCodeTooManyRows  = 396
	almanacWSPRErrorBodySample = 64 << 10
)

var (
	errAlmanacWSPRBackfillAborted = errors.New("almanac WSPR backfill aborted")
	errAlmanacWSPRDiskFull        = errors.New("almanac WSPR backfill refused: database disk over threshold (or probe failed)")
	errAlmanacWSPROversized       = errors.New("wspr.live response too large")
)

// almanacWSPRBandCodes is the wspr.live band whitelist: the codes whose band
// is an Almanac in-scope band (160 m … 10 m), ascending.
var almanacWSPRBandCodes = func() string {
	inScope := make(map[string]bool, len(almanacInScopeBands))
	for _, b := range almanacInScopeBands {
		inScope[b] = true
	}
	var codes []string
	for c := 0; c <= 30; c++ {
		if inScope[bandFromWSPR(c)] {
			codes = append(codes, strconv.Itoa(c))
		}
	}
	return strings.Join(codes, ",")
}()

func almanacWSPRMonthDoneKey(area string, ym int) string {
	return fmt.Sprintf("almanac_wspr_backfill_done_%s_%d", area, ym)
}

func almanacWSPRIngestDoneKey(ym int) string {
	return fmt.Sprintf("almanac_wspr_ingest_done_%d", ym)
}

// parseAlmanacWSPRBackfillAreas parses the areas flag: comma-separated
// grid4s, upper-cased and de-duplicated in order. Empty means off.
func parseAlmanacWSPRBackfillAreas(s string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		a := strings.ToUpper(strings.TrimSpace(part))
		if a == "" {
			continue
		}
		if len(a) != 4 || !validAlmanacLocator(a) {
			return nil, fmt.Errorf("almanac WSPR backfill area %q is not a grid4 locator", part)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out, nil
}

// almanacWSPRBackfillMonths lists the complete months to backfill, newest
// first: the `years`×12 months before now's month, never before 2008-03.
func almanacWSPRBackfillMonths(now time.Time, years int) []int {
	now = now.UTC()
	cur := now.Year()*100 + int(now.Month())
	var out []int
	for i := 1; i <= years*12; i++ {
		ym := almanacYMAdd(cur, -i)
		if ym < almanacWSPRArchiveStart {
			break
		}
		out = append(out, ym)
	}
	return out
}

// almanacWSPRMonthDays returns the first and last UTC day index of yyyymm.
func almanacWSPRMonthDays(ym int) (first, last int64) {
	first = almanacYMFirstDay(ym)
	return first, first + int64(almanacYMDays(ym)) - 1
}

// almanacWSPRRegion maps a far-end grid4 to its region ("" when invalid or
// outside every region box). Narrow boxes (KH6, JA, CAR) win over continents.
func almanacWSPRRegion(grid4 string) string {
	if !region.IsLocator(grid4) {
		return ""
	}
	r := regionFromLocatorCached(grid4)
	if !r.IsValid() {
		return ""
	}
	return string(r)
}

// ---------------------------------------------------------------------------
// ClickHouse queries
// ---------------------------------------------------------------------------

const almanacWSPRSlotExpr = "intDiv(toUnixTimestamp(time) % 86400, 1800)"

// almanacWSPRSNRBinsExpr is the per-group SNR histogram (KTD13) in the
// almanacSNRHist bins: [<−20], [−20,−15), [−15,−10), [−10,−5), [−5,0), [≥0].
const almanacWSPRSNRBinsExpr = "[toUInt32(countIf(snr < -20)), toUInt32(countIf(snr >= -20 AND snr < -15)), " +
	"toUInt32(countIf(snr >= -15 AND snr < -10)), toUInt32(countIf(snr >= -10 AND snr < -5)), " +
	"toUInt32(countIf(snr >= -5 AND snr < 0)), toUInt32(countIf(snr >= 0))] AS h"

// almanacWSPRRingSQL aggregates one window of spots touching the ring by
// (slot, band, rx4, tx4), with the SNR histogram; orientations are emitted
// in Go.
func almanacWSPRRingSQL(ringList string, from, to int64) string {
	return "SELECT " + almanacWSPRSlotExpr + " AS slot, band, " +
		"substring(upper(rx_loc),1,4) AS rx4, substring(upper(tx_loc),1,4) AS tx4, toUInt32(count()) AS c, " +
		almanacWSPRSNRBinsExpr + " " +
		"FROM wspr.rx " +
		fmt.Sprintf("WHERE time >= toDateTime(%d) AND time < toDateTime(%d) ", from, to) +
		"AND band IN (" + almanacWSPRBandCodes + ") " +
		"AND (substring(upper(rx_loc),1,4) IN (" + ringList + ") OR substring(upper(tx_loc),1,4) IN (" + ringList + ")) " +
		"GROUP BY slot, band, rx4, tx4 FORMAT JSONEachRow"
}

// almanacWSPRGlobalSQL counts one window's spots with both locators per slot
// (the WSPR ingest-alive totals).
func almanacWSPRGlobalSQL(from, to int64) string {
	return "SELECT " + almanacWSPRSlotExpr + " AS slot, toUInt32(count()) AS c " +
		"FROM wspr.rx " +
		fmt.Sprintf("WHERE time >= toDateTime(%d) AND time < toDateTime(%d) ", from, to) +
		"AND band IN (" + almanacWSPRBandCodes + ") " +
		"AND length(rx_loc) >= 4 AND length(tx_loc) >= 4 " +
		"GROUP BY slot FORMAT JSONEachRow"
}

// almanacWSPRInt accepts a JSON number or a quoted number (ClickHouse quotes
// 64-bit integers by default).
type almanacWSPRInt int64

func (n *almanacWSPRInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*n = almanacWSPRInt(v)
	return nil
}

// almanacWSPRAggRow is one JSONEachRow line of either query (the global query
// leaves band/rx4/tx4/h empty). H is the SNR histogram (almanacSNRHist bins).
type almanacWSPRAggRow struct {
	Slot almanacWSPRInt   `json:"slot"`
	Band almanacWSPRInt   `json:"band"`
	Rx4  string           `json:"rx4"`
	Tx4  string           `json:"tx4"`
	C    almanacWSPRInt   `json:"c"`
	H    []almanacWSPRInt `json:"h"`
}

// hist is the row's SNR histogram (saturating; all-zero when absent).
func (r *almanacWSPRAggRow) hist() almanacSNRHist {
	var h almanacSNRHist
	for b := 0; b < almanacSNRBins && b < len(r.H); b++ {
		satAdd8(&h[b], int64(r.H[b]))
	}
	return h
}

// almanacWSPRQueryError is a failed request. Code is the ClickHouse
// exception code when one was reported.
type almanacWSPRQueryError struct {
	Status int
	Code   int
	Msg    string
}

func (e *almanacWSPRQueryError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("wspr.live HTTP %d (ClickHouse code %d): %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("wspr.live HTTP %d: %s", e.Status, e.Msg)
}

// almanacWSPRSplittable: a timeout or an oversized result is answered by
// splitting the window, never by re-running the identical query.
func almanacWSPRSplittable(err error) bool {
	if errors.Is(err, errAlmanacWSPROversized) {
		return true
	}
	var qe *almanacWSPRQueryError
	return errors.As(err, &qe) && (qe.Code == clickHouseCodeTimeout || qe.Code == clickHouseCodeTooManyRows)
}

var clickHouseCodeRE = regexp.MustCompile(`Code: (\d+)`)

func clickHouseErrorCode(header http.Header, body string) int {
	if v := header.Get("X-ClickHouse-Exception-Code"); v != "" {
		if c, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return c
		}
	}
	if m := clickHouseCodeRE.FindStringSubmatch(body); m != nil {
		c, _ := strconv.Atoi(m[1])
		return c
	}
	return 0
}

// ---------------------------------------------------------------------------
// Pacing
// ---------------------------------------------------------------------------

// almanacWSPRClock is injectable so tests pace and back off instantly.
type almanacWSPRClock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type almanacWSPRRealClock struct{}

func (almanacWSPRRealClock) Now() time.Time { return time.Now() }

func (almanacWSPRRealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// almanacWSPRLimiter paces requests: a fixed pause after each request has
// finished, plus a cap of maxPerMinute request starts in any rolling minute.
// A backoff sleep (>= pause) already covers the pause, so they never stack.
type almanacWSPRLimiter struct {
	clock        almanacWSPRClock
	pause        time.Duration
	maxPerMinute int
	lastDone     time.Time
	starts       []time.Time
}

func (l *almanacWSPRLimiter) wait(ctx context.Context) error {
	if !l.lastDone.IsZero() {
		if d := l.lastDone.Add(l.pause).Sub(l.clock.Now()); d > 0 {
			if err := l.clock.Sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	if l.maxPerMinute > 0 {
		for {
			cutoff := l.clock.Now().Add(-time.Minute)
			for len(l.starts) > 0 && !l.starts[0].After(cutoff) {
				l.starts = l.starts[1:]
			}
			if len(l.starts) < l.maxPerMinute {
				break
			}
			if err := l.clock.Sleep(ctx, l.starts[0].Sub(cutoff)); err != nil {
				return err
			}
		}
	}
	l.starts = append(l.starts, l.clock.Now())
	return nil
}

// done marks the end of a request (success or failure); the next wait
// pauses from here.
func (l *almanacWSPRLimiter) done() { l.lastDone = l.clock.Now() }

// ---------------------------------------------------------------------------
// Month accumulator
// ---------------------------------------------------------------------------

// almanacWSPRMonth is one (area, month) accumulated in Go and committed in a
// single transaction.
type almanacWSPRMonth struct {
	Area      string
	YearMonth int
	FirstDay  int64
	LastDay   int64
	// Ring is the area's r=2 grid4 set: the DELETE scope of the commit.
	Ring []string
	// Counts is the dense month grid per key, sparse-encoded on commit; Hist
	// the matching SNR histograms (KTD13; absent key = no SNR data).
	Counts   map[almanacSegKey]*[almanacSeasonCountsLen]byte
	Hist     map[almanacSegKey]*[almanacSeasonCountsLen]almanacSNRHist
	Activity map[almanacGridBand]uint32
	// IngestTotals is nil when this month's WSPR ingest totals were already
	// committed (by another area); otherwise it replaces the month's totals.
	IngestTotals  map[almanacIngestSlotKey]int64
	DoneKey       string
	IngestDoneKey string

	ringSet map[string]bool
}

func newAlmanacWSPRMonth(area string, ring []string, ym int, withIngest bool) *almanacWSPRMonth {
	first, last := almanacWSPRMonthDays(ym)
	m := &almanacWSPRMonth{
		Area:          area,
		YearMonth:     ym,
		FirstDay:      first,
		LastDay:       last,
		Ring:          ring,
		Counts:        make(map[almanacSegKey]*[almanacSeasonCountsLen]byte, 256),
		Hist:          make(map[almanacSegKey]*[almanacSeasonCountsLen]almanacSNRHist, 256),
		Activity:      make(map[almanacGridBand]uint32, 64),
		DoneKey:       almanacWSPRMonthDoneKey(area, ym),
		IngestDoneKey: almanacWSPRIngestDoneKey(ym),
		ringSet:       make(map[string]bool, len(ring)),
	}
	for _, g := range ring {
		m.ringSet[g] = true
	}
	if withIngest {
		m.IngestTotals = make(map[almanacIngestSlotKey]int64, 31*almanacSeasonSlotsPerDay)
	}
	return m
}

// addRing emits a ring-aggregate row like dxPulseRegionBaselineKeysForSpot:
// (rx4, tx-region) and (tx4, rx-region), each only for a near end inside the
// ring; a same-grid pair yields one key, as the daily table de-duplicates.
func (m *almanacWSPRMonth) addRing(day int64, r almanacWSPRAggRow) {
	band := bandFromWSPR(int(r.Band))
	if band == "" || !almanacBandInScope(band) {
		return
	}
	slot := int(r.Slot)
	if slot < 0 || slot >= almanacSeasonSlotsPerDay || r.C <= 0 {
		return
	}
	rx4 := strings.ToUpper(strings.TrimSpace(r.Rx4))
	tx4 := strings.ToUpper(strings.TrimSpace(r.Tx4))
	h := r.hist()
	m.emit(day, slot, band, rx4, tx4, int64(r.C), &h)
	if tx4 != rx4 {
		m.emit(day, slot, band, tx4, rx4, int64(r.C), &h)
	}
}

func (m *almanacWSPRMonth) emit(day int64, slot int, band, near4, far4 string, c int64, h *almanacSNRHist) {
	if !m.ringSet[near4] {
		return
	}
	reg := almanacWSPRRegion(far4)
	if reg == "" {
		return
	}
	dom := int(day-m.FirstDay) + 1
	k := almanacSegKey{Grid4: near4, Band: band, Region: reg}
	counts := m.Counts[k]
	if counts == nil {
		counts = new([almanacSeasonCountsLen]byte)
		m.Counts[k] = counts
	}
	satAdd8(&counts[almanacSegmentOffset(dom)+slot], c)
	if h.total() > 0 {
		hist := m.Hist[k]
		if hist == nil {
			hist = new([almanacSeasonCountsLen]almanacSNRHist)
			m.Hist[k] = hist
		}
		hist[almanacSegmentOffset(dom)+slot].add(h)
	}
	m.Activity[almanacGridBand{Grid: near4, Band: band}] |= 1 << uint(dom-1)
}

// addIngest adds a global per-slot count in the region-key unit: a spot with
// both locators emits two region keys in dx_region_baseline_daily.
func (m *almanacWSPRMonth) addIngest(day int64, r almanacWSPRAggRow) {
	slot := int(r.Slot)
	if m.IngestTotals == nil || slot < 0 || slot >= almanacSeasonSlotsPerDay || r.C <= 0 {
		return
	}
	m.IngestTotals[almanacIngestSlotKey{Day: day, Slot: slot}] += 2 * int64(r.C)
}

// ---------------------------------------------------------------------------
// Backfill driver
// ---------------------------------------------------------------------------

type almanacWSPRBackfillStore interface {
	metaExists(ctx context.Context, key string) (bool, error)
	setMeta(ctx context.Context, key, value string) error
	commitMonth(ctx context.Context, m *almanacWSPRMonth) error
}

type almanacWSPRBackfillConfig struct {
	Endpoint  string
	Areas     []string
	Years     int
	Client    *http.Client
	Store     almanacWSPRBackfillStore
	Clock     almanacWSPRClock
	DiskProbe func() (float64, error)
}

type almanacWSPRBackfill struct {
	endpoint  string
	areas     []string
	years     int
	client    *http.Client
	store     almanacWSPRBackfillStore
	clock     almanacWSPRClock
	limiter   *almanacWSPRLimiter
	diskProbe func() (float64, error)
	maxRows   int

	failures int // consecutive request failures
}

func newAlmanacWSPRBackfill(cfg almanacWSPRBackfillConfig) *almanacWSPRBackfill {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = almanacWSPRDefaultEndpoint
	}
	clock := cfg.Clock
	if clock == nil {
		clock = almanacWSPRRealClock{}
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: almanacWSPRHTTPTimeout}
	}
	return &almanacWSPRBackfill{
		endpoint:  endpoint,
		areas:     cfg.Areas,
		years:     cfg.Years,
		client:    client,
		store:     cfg.Store,
		clock:     clock,
		limiter:   &almanacWSPRLimiter{clock: clock, pause: almanacWSPRPauseAfterRequest, maxPerMinute: almanacWSPRMaxPerMinute},
		diskProbe: cfg.DiskProbe,
		maxRows:   almanacWSPRMaxRowsPerResponse,
	}
}

func (b *almanacWSPRBackfill) diskOver() bool {
	if b.diskProbe == nil {
		return true // fail-safe
	}
	return almanacDiskOverThreshold(b.diskProbe())
}

// run walks every configured area's complete months newest-first, skipping
// months already done. It returns on the first abort, disk refusal or context
// cancellation; completed months stay committed, so a restart resumes.
func (b *almanacWSPRBackfill) run(ctx context.Context) error {
	b.failures = 0
	if b.diskOver() {
		return errAlmanacWSPRDiskFull
	}
	months := almanacWSPRBackfillMonths(b.clock.Now(), b.years)
	for _, area := range b.areas {
		ring := getSquaresWithinRings(area, almanacMaxWidenRadius)
		sort.Strings(ring)
		for _, ym := range months {
			done, err := b.store.metaExists(ctx, almanacWSPRMonthDoneKey(area, ym))
			if err != nil {
				return err
			}
			if done {
				continue
			}
			if b.diskOver() {
				return errAlmanacWSPRDiskFull
			}
			ingestDone, err := b.store.metaExists(ctx, almanacWSPRIngestDoneKey(ym))
			if err != nil {
				return err
			}
			m, err := b.fetchMonth(ctx, area, ring, ym, !ingestDone)
			if err != nil {
				return err
			}
			if err := b.store.commitMonth(ctx, m); err != nil {
				return fmt.Errorf("almanac WSPR backfill commit %s %d: %w", area, ym, err)
			}
			logInfo("almanac WSPR backfill: %s %d committed (%d seasonal rows)", area, ym, len(m.Counts))
		}
	}
	return nil
}

func (b *almanacWSPRBackfill) fetchMonth(ctx context.Context, area string, ring []string, ym int, withIngest bool) (*almanacWSPRMonth, error) {
	m := newAlmanacWSPRMonth(area, ring, ym, withIngest)
	quoted := make([]string, len(ring))
	for i, g := range ring {
		quoted[i] = "'" + g + "'"
	}
	ringList := strings.Join(quoted, ",")
	for day := m.FirstDay; day <= m.LastDay; day++ {
		from := day * 86400
		rows, err := b.fetchWindow(ctx, from, from+86400, func(f, t int64) string {
			return almanacWSPRRingSQL(ringList, f, t)
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			m.addRing(day, r)
		}
		if withIngest {
			rows, err := b.fetchWindow(ctx, from, from+86400, almanacWSPRGlobalSQL)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				m.addIngest(day, r)
			}
		}
	}
	return m, nil
}

// fetchWindow queries [from, to); on a timeout or oversized result it splits
// the window into hourly windows instead of re-running the same query.
func (b *almanacWSPRBackfill) fetchWindow(ctx context.Context, from, to int64, sqlFor func(from, to int64) string) ([]almanacWSPRAggRow, error) {
	rows, err := b.request(ctx, sqlFor(from, to), to-from > 3600)
	if err == nil || !almanacWSPRSplittable(err) {
		return rows, err
	}
	logInfo("almanac WSPR backfill: splitting %s..+%ds into hours: %v",
		time.Unix(from, 0).UTC().Format("2006-01-02 15:04"), to-from, err)
	var all []almanacWSPRAggRow
	for h := from; h < to; h += 3600 {
		end := h + 3600
		if end > to {
			end = to
		}
		part, err := b.request(ctx, sqlFor(h, end), false)
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	return all, nil
}

// request runs one query with pacing, exponential backoff and the
// consecutive-failure abort. With canSplit, a splittable error is returned
// at once (and not counted) so the caller can split the window.
func (b *almanacWSPRBackfill) request(ctx context.Context, sql string, canSplit bool) ([]almanacWSPRAggRow, error) {
	for {
		if err := b.limiter.wait(ctx); err != nil {
			return nil, err
		}
		rows, err := b.do(ctx, sql)
		b.limiter.done()
		if err == nil {
			b.failures = 0
			return rows, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if canSplit && almanacWSPRSplittable(err) {
			return nil, err
		}
		b.failures++
		logInfo("almanac WSPR backfill request failed (%d consecutive): %v", b.failures, err)
		if serr := b.store.setMeta(ctx, almanacWSPRLastErrorKey,
			fmt.Sprintf("%s %v", b.clock.Now().UTC().Format(time.RFC3339), err)); serr != nil {
			logInfo("almanac WSPR backfill: recording error failed: %v", serr)
		}
		if b.failures >= almanacWSPRMaxConsecutiveFailures {
			return nil, fmt.Errorf("%w after %d consecutive failures: %v", errAlmanacWSPRBackfillAborted, b.failures, err)
		}
		if err := b.clock.Sleep(ctx, almanacWSPRBackoffBase<<uint(b.failures-1)); err != nil {
			return nil, err
		}
	}
}

// do issues one GET (only a query= parameter) and stream-decodes the
// JSONEachRow body. The whole response is decoded before any row is used,
// so a mid-stream failure leaves no partial counts.
func (b *almanacWSPRBackfill) do(ctx context.Context, sql string) ([]almanacWSPRAggRow, error) {
	u := b.endpoint + "/?query=" + url.QueryEscape(sql)
	if len(u) >= almanacWSPRMaxURLBytes {
		return nil, fmt.Errorf("wspr.live URL is %d bytes (limit %d)", len(u), almanacWSPRMaxURLBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", almanacWSPRUserAgent)
	res, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, almanacWSPRErrorBodySample))
		msg := strings.TrimSpace(string(body))
		return nil, &almanacWSPRQueryError{Status: res.StatusCode, Code: clickHouseErrorCode(res.Header, msg), Msg: almanacWSPRTruncate(msg)}
	}
	dec := json.NewDecoder(res.Body)
	var rows []almanacWSPRAggRow
	for {
		var r almanacWSPRAggRow
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			// ClickHouse reports an exception raised after streaming began
			// as trailing text in the 200 body.
			rest, _ := io.ReadAll(io.MultiReader(dec.Buffered(), io.LimitReader(res.Body, almanacWSPRErrorBodySample)))
			msg := strings.TrimSpace(string(rest))
			return nil, &almanacWSPRQueryError{Status: res.StatusCode, Code: clickHouseErrorCode(res.Header, msg),
				Msg: almanacWSPRTruncate(fmt.Sprintf("decode: %v: %s", err, msg))}
		}
		if len(rows) >= b.maxRows {
			return nil, fmt.Errorf("%w: more than %d rows", errAlmanacWSPROversized, b.maxRows)
		}
		rows = append(rows, r)
	}
}

func almanacWSPRTruncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Postgres store
// ---------------------------------------------------------------------------

type pgAlmanacWSPRBackfillStore struct {
	pool *pgxpool.Pool
}

func (p *pgAlmanacWSPRBackfillStore) metaExists(ctx context.Context, key string) (bool, error) {
	var v string
	err := p.pool.QueryRow(ctx, `SELECT v FROM dx_meta WHERE k = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const almanacWSPRSetMetaSQL = `
	INSERT INTO dx_meta (k, v) VALUES ($1, $2)
	ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v`

func (p *pgAlmanacWSPRBackfillStore) setMeta(ctx context.Context, key, value string) error {
	_, err := p.pool.Exec(ctx, almanacWSPRSetMetaSQL, key, value)
	return err
}

// commitMonth replaces the WSPR layer for (ring grid4s, month) in one
// transaction, so a re-run never double-counts and rows from an earlier run
// or ring configuration inside the ring are removed.
func (p *pgAlmanacWSPRBackfillStore) commitMonth(ctx context.Context, m *almanacWSPRMonth) error {
	ctx, cancel := context.WithTimeout(ctx, almanacWSPRCommitTimeout)
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM almanac_season_counts WHERE layer = $1 AND year_month = $2 AND grid4 = ANY($3::text[])`,
		almanacSeasonLayerWSPR, m.YearMonth, m.Ring); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM almanac_area_activity WHERE layer = $1 AND year_month = $2 AND grid4 = ANY($3::text[])`,
		almanacSeasonLayerWSPR, m.YearMonth, m.Ring); err != nil {
		return err
	}
	if len(m.Counts) > 0 {
		rows := make([][]any, 0, len(m.Counts))
		for k, c := range m.Counts {
			rows = append(rows, []any{k.Grid4, k.Band, k.Region, int32(m.YearMonth), almanacSeasonLayerWSPR, almanacSparseEncode(c, m.Hist[k])})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"almanac_season_counts"},
			[]string{"grid4", "band", "region", "year_month", "layer", "counts"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if len(m.Activity) > 0 {
		rows := make([][]any, 0, len(m.Activity))
		for k, mask := range m.Activity {
			rows = append(rows, []any{k.Grid, k.Band, int32(m.YearMonth), almanacSeasonLayerWSPR, int32(mask)})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"almanac_area_activity"},
			[]string{"grid4", "band", "year_month", "layer", "day_mask"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if m.IngestTotals != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM almanac_ingest_slots WHERE layer = $1 AND day_index BETWEEN $2 AND $3`,
			almanacSeasonLayerWSPR, m.FirstDay, m.LastDay); err != nil {
			return err
		}
		if len(m.IngestTotals) > 0 {
			rows := make([][]any, 0, len(m.IngestTotals))
			for k, v := range m.IngestTotals {
				rows = append(rows, []any{k.Day, int32(k.Slot), almanacSeasonLayerWSPR, v})
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"almanac_ingest_slots"},
				[]string{"day_index", "slot_of_day", "layer", "spot_total"}, pgx.CopyFromRows(rows)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, almanacWSPRSetMetaSQL, m.IngestDoneKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, almanacWSPRSetMetaSQL, m.DoneKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// startAlmanacWSPRBackfill starts the backfill goroutine when areas (the
// parsed -almanac-wspr-backfill-areas) is non-empty and Postgres is
// available. endpoint is the wspr.live base URL (the -wspr-endpoint flag;
// empty = default), diskPath the -almanac-disk-path probe path and years
// -almanac-wspr-backfill-years. Returns nil when not started.
func startAlmanacWSPRBackfill(ctx context.Context, st *dxPostgresStore, endpoint, diskPath string, areas []string, years int) *almanacWSPRBackfill {
	if len(areas) == 0 {
		return nil
	}
	if st == nil || st.pool == nil {
		logInfo("almanac WSPR backfill disabled: Postgres is not available")
		return nil
	}
	if years < 1 {
		logInfo("almanac WSPR backfill disabled: -almanac-wspr-backfill-years=%d", years)
		return nil
	}
	b := newAlmanacWSPRBackfill(almanacWSPRBackfillConfig{
		Endpoint:  endpoint,
		Areas:     areas,
		Years:     years,
		Client:    &http.Client{Timeout: almanacWSPRHTTPTimeout},
		Store:     &pgAlmanacWSPRBackfillStore{pool: st.pool},
		DiskProbe: func() (float64, error) { return almanacDiskUsedFraction(diskPath) },
	})
	logInfo("almanac WSPR backfill starting: areas=%s years=%d endpoint=%s", strings.Join(areas, ","), years, b.endpoint)
	go func() {
		if err := b.run(ctx); err != nil {
			logError("almanac WSPR backfill stopped: %v", err)
			return
		}
		logInfo("almanac WSPR backfill finished")
	}()
	return b
}
