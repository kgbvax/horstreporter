package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncRecorder is a goroutine-safe ResponseWriter that counts flushes, so a
// test can run streamHandler in the background and inspect what it wrote.
type syncRecorder struct {
	mu      sync.Mutex
	hdr     http.Header
	body    strings.Builder
	flushes int
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{hdr: http.Header{}} }

func (r *syncRecorder) Header() http.Header { return r.hdr }
func (r *syncRecorder) WriteHeader(int)     {}
func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}
func (r *syncRecorder) Flush() {
	r.mu.Lock()
	r.flushes++
	r.mu.Unlock()
}
func (r *syncRecorder) snapshot() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String(), r.flushes
}

// startStream runs streamHandler in the background (no gzip) and returns the
// recorder plus a stop func that cancels and waits for the handler.
func startStream(t *testing.T, target string, hdr map[string]string) (*syncRecorder, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := newSyncRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamHandler(rec, req)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("streamHandler did not return after cancel")
		}
	}
	t.Cleanup(cancel)
	return rec, stop
}

func waitForBody(t *testing.T, rec *syncRecorder, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		body, _ := rec.snapshot()
		if strings.Contains(body, want) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in body:\n%s", want, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func setStreamTunables(t *testing.T, flush, heartbeat time.Duration) {
	t.Helper()
	of, oh, ob, oo := streamFlushInterval, streamHeartbeatInterval, streamBatchMax, streamResumeOverlap
	streamFlushInterval, streamHeartbeatInterval = flush, heartbeat
	t.Cleanup(func() {
		streamFlushInterval, streamHeartbeatInterval, streamBatchMax, streamResumeOverlap = of, oh, ob, oo
	})
}

func streamMsg(now int64, band string, sl, rl string) MQTTMessage {
	return MQTTMessage{T: now - 5, SC: "DK1ABC", RC: "W1XYZ", SL: sl, RL: rl, RP: -10, B: band, MD: "FT8"}
}

func TestLiveSpotsAreBatchedIntoOneFlush(t *testing.T) {
	withHubSnapshot(t, func() {
		setStreamTunables(t, 80*time.Millisecond, time.Hour)
		rec, stop := startStream(t, "/api/stream?qth=JO62", nil)
		defer stop()
		waitForBody(t, rec, "event: history_end")
		_, before := rec.snapshot()

		now := time.Now().Unix()
		hub.broadcastMsg(streamMsg(now, "20m", "JO62AB", "FN31AB"))
		hub.broadcastMsg(streamMsg(now, "40m", "JO62AB", "FN20AB"))
		body := waitForBody(t, rec, `"band":"40m"`)
		_, after := rec.snapshot()

		if !strings.Contains(body, `"band":"20m"`) {
			t.Fatalf("both spots must arrive, body:\n%s", body)
		}
		if after-before != 1 {
			t.Fatalf("two spots inside one interval took %d flushes, want 1", after-before)
		}
	})
}

func TestStreamBatchMaxFlushesEarly(t *testing.T) {
	withHubSnapshot(t, func() {
		setStreamTunables(t, time.Hour, time.Hour)
		streamBatchMax = 2
		rec, stop := startStream(t, "/api/stream?qth=JO62", nil)
		defer stop()
		waitForBody(t, rec, "event: history_end")

		now := time.Now().Unix()
		hub.broadcastMsg(streamMsg(now, "20m", "JO62AB", "FN31AB"))
		hub.broadcastMsg(streamMsg(now, "40m", "JO62AB", "FN20AB"))
		waitForBody(t, rec, `"band":"40m"`) // arrives without waiting an hour
	})
}

func TestStreamHeartbeatWhenIdle(t *testing.T) {
	withHubSnapshot(t, func() {
		setStreamTunables(t, 10*time.Millisecond, 40*time.Millisecond)
		rec, stop := startStream(t, "/api/stream?qth=JO62", nil)
		defer stop()
		waitForBody(t, rec, ": hb\n\n")
	})
}

func TestAppendV2Spot(t *testing.T) {
	n := int64(1000)
	cases := []struct {
		name string
		s    Spot
		want string
	}{
		{"plain pskreporter, no reporter", Spot{Locator: "JO62", Band: "20m", SNR: -12, T: 563, SourceType: "mqtt"}, `["JO62","20m",-12,437]`},
		{"reporter only", Spot{Locator: "JO62QM", Band: "20m", SNR: 3, T: 990, ReporterLocator: "FN31", SourceType: "mqtt"}, `["JO62QM","20m",3,10,"FN31"]`},
		{"wspr keeps empty reporter slot", Spot{Locator: "EM12", Band: "40m", SNR: -20, T: 900, SourceType: "wspr"}, `["EM12","40m",-20,100,"","w"]`},
		{"rbn", Spot{Locator: "EM12", Band: "20m", SNR: 20, T: 999, ReporterLocator: "JO31", SourceType: "rbn", Sender: "X", Receiver: "Y"}, `["EM12","20m",20,1,"JO31","r"]`},
		{"dxcluster carries calls", Spot{Locator: "K", Band: "20m", SNR: 0, T: 1000, SourceType: "dxcluster", Sender: "DL1ABC", Receiver: "K1XYZ"}, `["K","20m",0,0,"","d","DL1ABC","K1XYZ"]`},
		{"future spot gives negative age", Spot{Locator: "JO62", Band: "20m", SNR: 1, T: 1005, SourceType: "mqtt"}, `["JO62","20m",1,-5]`},
		{"escaping", Spot{Locator: "JO62", Band: "20m", SNR: 1, T: 1000, SourceType: "dxcluster", Sender: `A"B`, Receiver: "é"}, `["JO62","20m",1,0,"","d","A\"B","é"]`},
	}
	for _, c := range cases {
		if got := string(appendV2Spot(nil, c.s, n)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestAppendV2SpotsFrame(t *testing.T) {
	spots := []Spot{
		{Locator: "JO62", Band: "20m", SNR: -12, T: 563, SourceType: "mqtt"},
		{Locator: "EM12", Band: "40m", SNR: -20, T: 900, SourceType: "wspr"},
	}
	got := string(appendV2SpotsFrame(nil, spots, 1000, "abc-7"))
	want := "event: spots\nid: abc-7\ndata: {\"n\":1000,\"s\":[[\"JO62\",\"20m\",-12,437],[\"EM12\",\"40m\",-20,100,\"\",\"w\"]]}\n\n"
	if got != want {
		t.Fatalf("frame:\n got %q\nwant %q", got, want)
	}
	if got := string(appendV2SpotsFrame(nil, nil, 5, "")); got != "event: spots\ndata: {\"n\":5,\"s\":[]}\n\n" {
		t.Fatalf("empty frame: %q", got)
	}
}

func TestParseStreamEventID(t *testing.T) {
	epoch, seq, ok := parseStreamEventID("1a2b-c-42")
	if !ok || epoch != "1a2b-c" || seq != 42 {
		t.Fatalf("got %q %d %v", epoch, seq, ok)
	}
	for _, bad := range []string{"", "nodash", "-5", "abc-", "abc-x"} {
		if _, _, ok := parseStreamEventID(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// v2History returns spots of the frames before history_end, plus the id on
// history_end.
func v2HistoryEndID(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "event: history_end\nid: ")
	if i < 0 {
		t.Fatalf("no id on history_end:\n%s", body)
	}
	rest := body[i+len("event: history_end\nid: "):]
	return rest[:strings.Index(rest, "\n")]
}

func TestStreamV2ResumeProtocol(t *testing.T) {
	now := time.Now().Unix()
	withHubHistory(t, nil)
	withHubSnapshot(t, func() {
		setStreamTunables(t, time.Hour, time.Hour)
		streamResumeOverlap = 0
		hub.Lock()
		hub.history = []MQTTMessage{
			streamMsg(now, "20m", "JO62AB", "FN31AB"),
			streamMsg(now, "20m", "JO62AB", "FN31CD"),
			streamMsg(now, "40m", "JO62AB", "FN20AB"),
		}
		hub.Unlock()

		run := func(target string, hdr map[string]string, wait string) string {
			rec, stop := startStream(t, target, hdr)
			body := waitForBody(t, rec, wait)
			stop()
			return body
		}

		full := run("/api/stream?qth=JO62&v=2", nil, "event: history_end")
		if strings.Contains(full, "event: resume") {
			t.Fatalf("a fresh connect must not emit resume:\n%s", full)
		}
		if strings.Count(full, `["FN`) != 3 {
			t.Fatalf("expected all 3 spots in the first dump:\n%s", full)
		}
		id := v2HistoryEndID(t, full)

		delta := run("/api/stream?qth=JO62&v=2&since="+id, nil, "event: history_end")
		if !strings.Contains(delta, `event: resume`+"\n"+`data: {"mode":"delta"}`) || strings.Contains(delta, `["FN`) {
			t.Fatalf("resume from the high-water mark must be an empty delta:\n%s", delta)
		}

		// Last-Event-ID beats ?since=.
		hdr := run("/api/stream?qth=JO62&v=2&since=bogus-1", map[string]string{"Last-Event-ID": id}, "event: history_end")
		if !strings.Contains(hdr, `"mode":"delta"`) {
			t.Fatalf("Last-Event-ID header must win:\n%s", hdr)
		}

		// Widening the filter: 40m becomes enabled; only its old spot returns.
		widen := run("/api/stream?qth=JO62&v=2&since="+id+"&enabled_bands=20m,40m&prev=1&prev_enabled_bands=20m", nil, "event: history_end")
		if !strings.Contains(widen, `"mode":"delta"`) || !strings.Contains(widen, `"FN20AB"`) ||
			strings.Contains(widen, `"FN31AB"`) || strings.Contains(widen, `"FN31CD"`) {
			t.Fatalf("widening must resend only newly enabled bands:\n%s", widen)
		}

		// Wrong epoch and mismatched radius fall back to a full dump.
		epoch := run("/api/stream?qth=JO62&v=2&since=other-1", nil, "event: history_end")
		if !strings.Contains(epoch, `"reason":"epoch"`) || strings.Count(epoch, `["FN`) != 3 {
			t.Fatalf("epoch mismatch must send everything:\n%s", epoch)
		}
		area := run("/api/stream?qth=JO62&v=2&since="+id+"&prev_radius=3", nil, "event: history_end")
		if !strings.Contains(area, `"reason":"area"`) || strings.Count(area, `["FN`) != 3 {
			t.Fatalf("radius mismatch must send everything:\n%s", area)
		}
	})
}

func TestStreamV2LiveFramesCarryMonotonicIDs(t *testing.T) {
	withHubSnapshot(t, func() {
		setStreamTunables(t, 20*time.Millisecond, time.Hour)
		rec, stop := startStream(t, "/api/stream?qth=JO62&v=2", nil)
		defer stop()
		body := waitForBody(t, rec, "event: history_end")
		hwm, _ := parseStreamEventIDSeq(t, v2HistoryEndID(t, body))

		hub.broadcastMsg(streamMsg(time.Now().Unix(), "20m", "JO62AB", "FN31AB"))
		body = waitForBody(t, rec, `"FN31AB"`)
		i := strings.Index(body, "event: spots\nid: ")
		if i < 0 {
			t.Fatalf("live frame lacks an id:\n%s", body)
		}
		rest := body[i+len("event: spots\nid: "):]
		got, _ := parseStreamEventIDSeq(t, rest[:strings.Index(rest, "\n")])
		if got != hwm+1 {
			t.Fatalf("live id seq = %d, want %d", got, hwm+1)
		}
	})
}

func parseStreamEventIDSeq(t *testing.T, id string) (uint64, string) {
	t.Helper()
	epoch, seq, ok := parseStreamEventID(id)
	if !ok || epoch != hubEpoch {
		t.Fatalf("bad id %q", id)
	}
	return seq, epoch
}

func TestStreamSourceFilter(t *testing.T) {
	f := streamFilterFromQuery(map[string][]string{"include_wspr": {"false"}, "include_rbn": {"0"}}, "", true)
	if f.spotAllowed(Spot{SourceType: "wspr", Band: "20m"}) || f.spotAllowed(Spot{SourceType: "rbn", Band: "20m"}) {
		t.Fatal("wspr and rbn must be excluded")
	}
	if !f.spotAllowed(Spot{SourceType: "mqtt", Band: "20m"}) || !f.spotAllowed(Spot{SourceType: "dxcluster", Band: "20m"}) {
		t.Fatal("mqtt and dxcluster must stay")
	}
	if g := streamFilterFromQuery(map[string][]string{"include_wspr": {"false"}}, "", false); !g.spotAllowed(Spot{SourceType: "wspr"}) {
		t.Fatal("withSources=false must ignore include_*")
	}
}

func TestHubSequenceSurvivesPruneAndReplace(t *testing.T) {
	h := &Hub{clients: map[*Client]bool{}}
	for i := 0; i < 5; i++ {
		h.history = append(h.history, MQTTMessage{T: int64(i)})
	}
	h.baseSeq = 0
	if h.highSeqLocked() != 5 {
		t.Fatalf("high = %d", h.highSeqLocked())
	}
	h.dropFrontLocked(2)
	if h.baseSeq != 2 || h.highSeqLocked() != 5 || len(h.history) != 3 {
		t.Fatalf("after drop: base=%d high=%d len=%d", h.baseSeq, h.highSeqLocked(), len(h.history))
	}
	h.dropFrontLocked(10)
	if h.baseSeq != 5 || len(h.history) != 0 || h.highSeqLocked() != 5 {
		t.Fatalf("after full drop: base=%d len=%d", h.baseSeq, len(h.history))
	}
	h.history = []MQTTMessage{{}, {}}
	h.replaceHistoryLocked([]MQTTMessage{{}, {}, {}})
	if h.baseSeq != 7 || h.highSeqLocked() != 10 {
		t.Fatalf("after replace: base=%d high=%d", h.baseSeq, h.highSeqLocked())
	}
}
