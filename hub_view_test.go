package main

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// The zero-copy window views (Hub.windowFromLocked) are only safe while hub
// history stays append-only. This hammers appends, front drops and wholesale
// replacement against readers that validate every element of their view; run
// with -race to catch any in-place write.
func TestHubWindowViewsSafeUnderConcurrentAppendAndPrune(t *testing.T) {
	h := &Hub{clients: map[*Client]bool{}}
	mk := func(i int64) MQTTMessage { return MQTTMessage{T: i, SC: strconv.FormatInt(i, 10), RP: int(i % 97)} }
	for i := int64(0); i < 1000; i++ {
		h.history = append(h.history, mk(i))
	}

	var next atomic.Int64
	next.Store(1000)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.Lock()
			for k := 0; k < 50; k++ {
				h.history = append(h.history, mk(next.Add(1)))
			}
			if len(h.history) > 4000 {
				h.dropFrontLocked(len(h.history) / 2)
			}
			h.Unlock()
		}
	}()

	var bad atomic.Int64
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.RLock()
				view := h.windowFromLocked(len(h.history) / 3)
				h.RUnlock()
				if cap(view) != len(view) {
					bad.Add(1)
				}
				prev := int64(-1)
				for _, m := range view {
					if m.SC != strconv.FormatInt(m.T, 10) || m.RP != int(m.T%97) || m.T <= prev {
						bad.Add(1)
						break
					}
					prev = m.T
				}
			}
		}()
	}

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d corrupted or non-clamped window views", n)
	}
}

func TestHubWindowFromLockedClampsCapacity(t *testing.T) {
	h := &Hub{history: make([]MQTTMessage, 10, 64)}
	v := h.windowFromLocked(4)
	if len(v) != 6 || cap(v) != 6 {
		t.Fatalf("view len/cap = %d/%d, want 6/6", len(v), cap(v))
	}
	// An append to the view must not write into the hub's spare capacity.
	_ = append(v, MQTTMessage{SC: "X"})
	h.history = h.history[:11]
	if h.history[10].SC != "" {
		t.Fatalf("append to the view leaked into the hub backing array")
	}
}

// hub.history holds about a million MQTTMessage values; every byte added here
// is a megabyte of resident memory. Keep rarely-used fields behind X.
func TestMQTTMessageStaysCompact(t *testing.T) {
	const maxBytes = 152
	if got := unsafe.Sizeof(MQTTMessage{}); got > maxBytes {
		t.Fatalf("MQTTMessage is %d bytes, budget %d: move new rarely-used fields behind DXExtra", got, maxBytes)
	}
}
