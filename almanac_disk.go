package main

import (
	"errors"
	"syscall"
)

// almanac_disk.go is the database-disk probe behind the Almanac prune grace
// period (plan KTD6) and, later, the WSPR backfill start guard (KTD8). On prod
// -almanac-disk-path points at the Postgres data directory.
//
// Fail-safe: any probe error (unset path, missing path, statfs failure) counts
// as "over the threshold". For the prune that means the grace period is
// skipped — the disk-full outages of Sep 14 / Sep 21 are worse than losing a
// few unfolded days, which are then recorded in almanac_lost_days.

// almanacDiskFullThreshold is the used fraction above which the disk counts as
// full: the prune grace period is skipped and the backfill refuses to start.
const almanacDiskFullThreshold = 0.80

var errAlmanacDiskPathUnset = errors.New("almanac disk probe: -almanac-disk-path is not set")

// almanacDiskUsedFraction returns the used fraction of the filesystem holding
// path, with df semantics: used / (used + available to unprivileged users).
func almanacDiskUsedFraction(path string) (float64, error) {
	if path == "" {
		return 0, errAlmanacDiskPathUnset
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Block counts only: Bsize cancels out (and differs in type per OS).
	return almanacUsedFraction(uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail)), nil
}

// almanacUsedFraction computes df's Use%: (blocks-free) / ((blocks-free)+avail).
// A filesystem reporting no blocks reads as full (fail-safe).
func almanacUsedFraction(blocks, free, avail uint64) float64 {
	if free > blocks {
		free = blocks
	}
	used := blocks - free
	denom := used + avail
	if denom == 0 {
		return 1
	}
	return float64(used) / float64(denom)
}

// almanacDiskOverThreshold is the fail-safe decision: a probe error counts as
// over the threshold.
func almanacDiskOverThreshold(frac float64, err error) bool {
	return err != nil || frac > almanacDiskFullThreshold
}
