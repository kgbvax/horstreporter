package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Save used to copy each bucket shallowly, so the snapshot shared the live
// DayCounts maps and json.MarshalIndent iterated them after the lock was
// released while Observe kept writing: prod died with "concurrent map
// iteration and map write". Run with -race.
func TestPropBaselineSaveConcurrentWithObserve(t *testing.T) {
	e := newPropBaselineEngine(filepath.Join(t.TempDir(), "prop_baseline.json"), "")
	base := time.Now().Unix()
	// Seed the bucket so Save's snapshot already holds its DayCounts map.
	e.Observe(propSpot(base, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Same slot, cycling over 400 days: inserts DayCounts keys while the map stays small.
			e.Observe(propSpot(base+(i%400)*86400, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))
		}
	}()
	for i := 0; i < 50; i++ {
		if err := e.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestWsprClimatologySaveConcurrentWithObserve(t *testing.T) {
	e := newWsprClimatologyEngine(filepath.Join(t.TempDir(), "wspr_climatology.json"))
	base := time.Now().Unix()
	e.Observe(wsprClimSpot(base))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			e.Observe(wsprClimSpot(base + (i%400)*86400))
		}
	}()
	for i := 0; i < 50; i++ {
		if err := e.Save(); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func wsprClimSpot(ts int64) MQTTMessage {
	return MQTTMessage{Source: "wspr", B: "20m", T: ts, SC: "RX", SL: "JO62qm", RC: "TX", RL: "FN31ab"}
}
