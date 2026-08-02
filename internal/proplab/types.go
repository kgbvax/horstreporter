package proplab

// types.go contains the data-contract types shared between the proplab
// engines, the main HorstReporter service, and the backtest harness.

// CellRow is a closed 15-minute midpoint-cell bucket ready for persistence.
type CellRow struct {
	BucketStart   int64
	Band          string
	Cell4         string
	Region        string
	Lane          string // "ft8", "rbn", "dcx"
	SpotCount     int
	LinkCount     int
	ReporterCount int
	SnrMedian     int
	SnrP10        int
	DistMedianKm  int
	DistMaxKm     int
}

// BaselineDayRow is one day's aggregation per (band, region, slot_of_day).
type BaselineDayRow struct {
	Band      string
	Region    string
	Slot      int
	DayIndex  int64
	LinkCount int64
	SpotCount int64
	DistMaxKm int
}

// DestRow is a closed 15-minute destination bucket ready for persistence.
// Unlike CellRow (path-midpoint semantics for the Ladder engine), dest rows
// answer "stations in Maidenhead field Scope2 had activity with DX destination
// region Region" — the remote end's location, not the path midpoint. A link
// counts symmetrically into both orientations; transmission direction is
// ignored deliberately ("can stations near me reach R").
type DestRow struct {
	BucketStart   int64
	Band          string
	Scope2        string // 2-char Maidenhead field of the near end, e.g. "JO"
	Region        string // 11-region destination of the remote end
	SpotCount     int
	LinkCount     int // deduplicated station pairs
	ReporterCount int // distinct near-end callsigns (witnesses)
	SnrMedian     int
	DistMedianKm  int
	DistMaxKm     int
}
