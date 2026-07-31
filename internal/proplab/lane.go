package proplab

// lane.go maps ingest source types to the source lanes used by the Ladder
// engine. The lane names are part of the proplab_cell_buckets primary key.

// LaneForSourceType maps an ingest source tag to its proplab lane.
func LaneForSourceType(t string) string {
	switch t {
	case "rbn":
		return "rbn"
	case "dxcluster":
		return "dcx"
	default:
		return "ft8"
	}
}
