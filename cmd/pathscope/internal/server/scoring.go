package server

import (
	"context"
	"log"
	"time"
)

// StartScoring launches the periodic re-scoring goroutine. It returns
// immediately; the goroutine exits when the parent context is cancelled
// or Stop is called. Call Stop from main after httpSrv.Shutdown returns
// to drain the goroutine before process exit.
//
// The loop calls buildGlance and Broadcasts the result every `interval`
// (default 5 s, configurable via PATHSCOPE_TICK_INTERVAL → Deps.Interval).
// Errors are logged and the tick is skipped — no backoff, because adding
// backoff would slow recovery from a transient PG blip, and the next
// successful tick will simply overwrite the stale matrix.
//
// Follow-up: a Prometheus counter for the error branch is a sensible
// observability addition once the dashboard grows past one user.
func (s *Server) StartScoring(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.scoringStop = cancel
	s.scoringWG.Add(1)
	go s.runScoringLoop(ctx)
}

// Stop cancels the scoring loop and waits up to 5 s for the goroutine to
// exit. Safe to call multiple times (subsequent calls are no-ops).
func (s *Server) Stop() {
	if s.scoringStop != nil {
		s.scoringStop()
		s.scoringStop = nil
	}
	done := make(chan struct{})
	go func() { s.scoringWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("pathscope: scoring loop did not exit within 5 s")
	}
}

// runScoringLoop is the goroutine body for StartScoring. Every
// `s.interval` it calls buildGlance and Broadcasts the result.
func (s *Server) runScoringLoop(ctx context.Context) {
	defer s.scoringWG.Done()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, err := s.buildGlance(tickCtx)
			cancel()
			if err != nil {
				log.Printf("pathscope: tick: %v", err)
				continue
			}
			s.hub.Broadcast(resp)
		}
	}
}