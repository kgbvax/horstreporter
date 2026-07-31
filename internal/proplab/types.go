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
