package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tunables (package variables so tests can shorten them).
var (
	// streamFlushInterval is how long live spots are coalesced before one
	// write+flush. It bounds the added latency of live delivery.
	streamFlushInterval = time.Second
	// streamBatchMax flushes early when this many spots are pending.
	streamBatchMax = 500
	// streamHeartbeatInterval is the idle time after which a `: hb` comment
	// keeps the connection alive through proxies and reaps dead peers.
	streamHeartbeatInterval = 25 * time.Second
	// streamHistoryChunk is the v2 history dump chunk size (spots per frame).
	streamHistoryChunk = 1000
	// streamResumeOverlap re-sends this many sequence numbers before the
	// client's last seen id: ingest goroutines can deliver slightly out of
	// sequence order. The client dedups the overlap.
	streamResumeOverlap uint64 = 512
)

// streamRequest is the parsed /api/stream query.
type streamRequest struct {
	qth          string
	surroundings bool
	minutes      int
	v2           bool
	filter       streamClientFilter

	// Resume (v2 only). sinceRaw is the Last-Event-ID header, else `since`.
	sinceRaw   string
	prev       bool // prev=1: also send spots the previous filter excluded
	prevFilter streamClientFilter
	prevRadius int
	hasPrevRad bool
}

func parseStreamRequest(r *http.Request) (streamRequest, error) {
	q := r.URL.Query()
	qth, surroundings := resolveQTHQuery(r)
	if qth == "" {
		return streamRequest{}, fmt.Errorf("qth required")
	}
	minutes, err := strconv.Atoi(q.Get("minutes"))
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	if minutes > 60 {
		minutes = 60
	}
	req := streamRequest{
		qth:          qth,
		surroundings: surroundings,
		minutes:      minutes,
		v2:           q.Get("v") == "2",
		filter:       streamFilterFromQuery(q, "", true),
	}
	if req.v2 {
		req.sinceRaw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
		if req.sinceRaw == "" {
			req.sinceRaw = strings.TrimSpace(q.Get("since"))
		}
		if q.Get("prev") == "1" {
			req.prev = true
			req.prevFilter = streamFilterFromQuery(q, "prev_", true)
		}
		if raw := q.Get("prev_radius"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				req.prevRadius, req.hasPrevRad = v, true
			}
		}
	}
	return req, nil
}

// streamWriter serializes stream frames onto the (optionally gzipped)
// response and tracks flush accounting.
type streamWriter struct {
	cw        *countingResponseWriter
	gz        *gzip.Writer
	w         io.Writer
	buf       []byte
	lastWrite time.Time
	err       error
}

func newStreamWriter(cw *countingResponseWriter, gz *gzip.Writer) *streamWriter {
	sw := &streamWriter{cw: cw, gz: gz, w: cw, lastWrite: time.Now()}
	if gz != nil {
		sw.w = gz
	}
	return sw
}

// write appends raw frame bytes (already terminated by a blank line).
func (s *streamWriter) write(p []byte) {
	if s.err != nil {
		return
	}
	_, s.err = s.w.Write(p)
	s.lastWrite = time.Now()
}

func (s *streamWriter) writeString(str string) { s.write([]byte(str)) }

// flush pushes buffered bytes to the client; the first error is sticky.
func (s *streamWriter) flush() error {
	if s.err == nil && s.gz != nil {
		s.err = s.gz.Flush()
	}
	if s.err == nil {
		s.cw.Flush()
		streamAccounting.framesFlushed.Add(1)
	}
	return s.err
}

func (s *streamWriter) heartbeat() error {
	s.writeString(": hb\n\n")
	return s.flush()
}

// writeSpotsV1 writes one `data:` frame per spot (the original wire format).
func (s *streamWriter) writeSpotsV1(spots []Spot) {
	for _, spot := range spots {
		b, _ := json.Marshal(toStreamSpot(spot))
		s.buf = append(s.buf[:0], "data: "...)
		s.buf = append(s.buf, b...)
		s.buf = append(s.buf, "\n\n"...)
		s.write(s.buf)
	}
	streamAccounting.spotsSent.Add(int64(len(spots)))
}

// writeSpotsV2 writes spots as `spots` frames of at most chunk spots each.
func (s *streamWriter) writeSpotsV2(spots []Spot, n int64, chunk int, id string) {
	if chunk <= 0 {
		chunk = len(spots)
	}
	for start := 0; start < len(spots); start += chunk {
		end := start + chunk
		if end > len(spots) {
			end = len(spots)
		}
		s.buf = appendV2SpotsFrame(s.buf[:0], spots[start:end], n, id)
		s.write(s.buf)
	}
	streamAccounting.spotsSent.Add(int64(len(spots)))
}

// streamSnapshot is the client's registration + history copy, taken under
// one hub lock so no live spot is lost or duplicated at the seam.
type streamSnapshot struct {
	window  []MQTTMessage
	baseSeq uint64 // seq of window[0] is baseSeq+1
	highSeq uint64
	ok      bool // false when the server is at capacity
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	req, err := parseStreamRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	qthSet := qthSquares(req.qth, req.surroundings)

	client := &Client{
		qthSet: qthSet,
		send:   make(chan Spot, 10000), // Buffer to handle initial history dump
	}

	// Optional configurable "area of interest": rings=N (>0) with a locator
	// qth matches any sender/receiver within N grid-squares of the qth, for
	// region feeds (e.g. horstprop); rings=auto lets the server widen the
	// block until enough bands carry a full sample (live_area.go). Read-only;
	// default behaviour unchanged. The area is announced as an `area` event.
	area := liveAreaForRequest(r, req.qth, req.surroundings, time.Now().Unix())
	areaRadius := 0
	if area != nil {
		client.areaActive = true
		client.areaX, client.areaY, client.areaRings = area.x, area.y, area.Radius
		areaRadius = area.Radius
	}

	now := time.Now().Unix()
	cutoff := now - int64(req.minutes*60)

	// Add client and copy the relevant history window under the write lock.
	// matchAndCreateSpot processing happens outside the lock so broadcastMsg
	// is not stalled for the duration of the history scan.
	hub.Lock()
	if maxClients > 0 && len(hub.clients) >= maxClients {
		hub.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprintf(w, "event: server_error\ndata: Server is at capacity. Please try again later.\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	hub.clients[client] = true
	streamAccounting.startSession()
	logInfo("New client stream started for qth: %v (History: %d mins)", qthSet, req.minutes)

	// Resume: decide the mode before copying so the copy can start late.
	resumeMode, resumeReason, sinceSeq := decideResume(req, areaRadius, hub.highSeqLocked())
	startIdx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})
	if resumeMode == resumeDelta && !req.prev {
		// Plain resume: nothing before the overlap can be new.
		if from := sinceSeq - minU64(sinceSeq, streamResumeOverlap); from > hub.baseSeq {
			if i := int(from - hub.baseSeq); i > startIdx {
				startIdx = minInt(i, len(hub.history))
			}
		}
	}
	snap := streamSnapshot{
		window:  make([]MQTTMessage, len(hub.history)-startIdx),
		baseSeq: hub.baseSeq + uint64(startIdx),
		highSeq: hub.highSeqLocked(),
		ok:      true,
	}
	copy(snap.window, hub.history[startIdx:])
	hub.Unlock()

	var historySpots []Spot
	overlapFloor := uint64(0)
	if sinceSeq > streamResumeOverlap {
		overlapFloor = sinceSeq - streamResumeOverlap
	}
	for i, msg := range snap.window {
		spot, ok := matchAndCreateSpot(client, msg, now)
		if !ok || !req.filter.spotAllowed(spot) {
			continue
		}
		if resumeMode == resumeDelta && req.prev {
			seq := snap.baseSeq + uint64(i) + 1
			if seq <= overlapFloor && req.prevFilter.spotAllowed(spot) {
				continue // the client already holds this spot
			}
		}
		spot.Seq = snap.baseSeq + uint64(i) + 1
		historySpots = append(historySpots, spot)
	}

	var cw *countingResponseWriter
	defer func() {
		if cw != nil {
			streamAccounting.completeSession(cw.bytesWritten)
		}
		hub.Lock()
		if _, ok := hub.clients[client]; ok {
			delete(hub.clients, client)
			close(client.send)
		}
		hub.Unlock()
		logInfo("Client stream closed for qth: %v", qthSet)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	cw = &countingResponseWriter{ResponseWriter: w}
	var gz *gzip.Writer
	if compressStream && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz = gzip.NewWriter(cw)
		defer gz.Close()
	}
	sw := newStreamWriter(cw, gz)

	if area != nil {
		b, _ := json.Marshal(area)
		sw.writeString(fmt.Sprintf("event: area\ndata: %s\n\n", string(b)))
	}
	if req.v2 {
		if resumeMode != resumeNone {
			streamAccounting.noteResume(resumeMode)
			b, _ := json.Marshal(map[string]string{"mode": resumeMode.String(), "reason": resumeReason})
			if resumeReason == "" {
				b = []byte(`{"mode":"` + resumeMode.String() + `"}`)
			}
			sw.writeString("event: resume\ndata: " + string(b) + "\n\n")
		}
		sw.writeSpotsV2(historySpots, now, streamHistoryChunk, "")
		sw.writeString(fmt.Sprintf("event: history_end\nid: %s\ndata: {\"n\":%d,\"count\":%d}\n\n",
			streamEventID(snap.highSeq), now, len(historySpots)))
	} else {
		sw.writeSpotsV1(historySpots)
		sw.writeString("event: history_end\ndata: {}\n\n")
	}
	if sw.flush() != nil {
		return
	}
	streamAccounting.historyBytes.Add(cw.bytesWritten)

	ctx := r.Context()
	ticker := time.NewTicker(streamFlushInterval)
	defer ticker.Stop()
	pending := make([]Spot, 0, 64)
	maxSeq := snap.highSeq
	flushPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		if req.v2 {
			for _, s := range pending {
				if s.Seq > maxSeq {
					maxSeq = s.Seq
				}
			}
			sw.writeSpotsV2(pending, time.Now().Unix(), streamBatchMax, streamEventID(maxSeq))
		} else {
			sw.writeSpotsV1(pending)
		}
		pending = pending[:0]
		return sw.flush()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case spot, ok := <-client.send:
			if !ok {
				return
			}
			if !req.filter.spotAllowed(spot) {
				continue
			}
			pending = append(pending, spot)
			if len(pending) >= streamBatchMax && flushPending() != nil {
				return
			}
		case <-ticker.C:
			if len(pending) > 0 {
				if flushPending() != nil {
					return
				}
			} else if time.Since(sw.lastWrite) >= streamHeartbeatInterval {
				if sw.heartbeat() != nil {
					return
				}
			}
		}
	}
}

type resumeKind int

const (
	resumeNone resumeKind = iota
	resumeDelta
	resumeFull
)

func (k resumeKind) String() string {
	switch k {
	case resumeDelta:
		return "delta"
	case resumeFull:
		return "full"
	}
	return ""
}

// decideResume interprets since/Last-Event-ID. It returns resumeNone when the
// client asked for no resume, resumeDelta with the last seen sequence number
// when the server can honour it, else resumeFull with the reason.
func decideResume(req streamRequest, areaRadius int, highSeq uint64) (resumeKind, string, uint64) {
	if !req.v2 || req.sinceRaw == "" {
		return resumeNone, "", 0
	}
	epoch, seq, ok := parseStreamEventID(req.sinceRaw)
	switch {
	case !ok:
		return resumeFull, "bad_id", 0
	case epoch != hubEpoch || seq > highSeq:
		return resumeFull, "epoch", 0
	case req.hasPrevRad && req.prevRadius != areaRadius:
		return resumeFull, "area", 0
	}
	return resumeDelta, "", seq
}

func (a *streamAccountingState) noteResume(k resumeKind) {
	if k == resumeDelta {
		a.resumeDelta.Add(1)
	} else if k == resumeFull {
		a.resumeFull.Add(1)
	}
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
