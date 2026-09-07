package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The allocator flag map only accepts string and bool values — an int flag
// (e.g. force-device-scale-factor) makes Allocate fail with the cryptic
// "invalid exec pool flag". Regression: chromedpContext must build cleanly.
func TestChromedpContextFlagsValid(t *testing.T) {
	ctx, cancel := chromedpContext("/nonexistent-chromium", 1280, 720)
	defer cancel()
	// Drive Allocate's validation: a flag-type error surfaces before the
	// (missing) chromium binary is ever launched.
	if err := chromedp.Run(ctx); err != nil {
		if strings.Contains(err.Error(), "invalid exec pool flag") {
			t.Fatalf("chromedpContext has an invalid flag type: %v", err)
		}
		if !strings.Contains(err.Error(), "executable") && !errors.Is(err, context.DeadlineExceeded) {
			t.Logf("expected a binary-not-found error, got: %v", err)
		}
	}
}

func TestValidateDefaults(t *testing.T) {
	s := &service{maxFrames: 1800}
	cfg := videoConfig{QTH: " jo62qm "}
	end := time.Now().Unix()
	if err := s.validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.QTH != "JO62QM" {
		t.Errorf("qth not uppercased: %q", cfg.QTH)
	}
	if cfg.StepSeconds != 120 || cfg.FPS != 10 {
		t.Errorf("defaults not applied: step=%d fps=%d", cfg.StepSeconds, cfg.FPS)
	}
	if cfg.Width != 1280 || cfg.Height != 720 {
		t.Errorf("size defaults not applied: %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.End < end { // End was filled to ~now
		t.Errorf("end default not applied: %d", cfg.End)
	}
	if cfg.Start != cfg.End-24*3600 {
		t.Errorf("start default not 24h before end: %d vs %d", cfg.Start, cfg.End)
	}
}

func TestValidateRejects(t *testing.T) {
	s := &service{maxFrames: 100}
	cases := []struct {
		name string
		cfg  videoConfig
		want string
	}{
		{"no qth", videoConfig{Start: 1, End: 2}, "qth"},
		{"start after end", videoConfig{QTH: "JO62QM", Start: 200, End: 100}, "start must be before end"},
		{"bad step", videoConfig{QTH: "JO62QM", Start: 1, End: 100000, StepSeconds: 30}, "step_seconds"},
		{"bad fps", videoConfig{QTH: "JO62QM", Start: 1, End: 1000, FPS: 24}, "fps"},
		{"bad size", videoConfig{QTH: "JO62QM", Start: 1, End: 1000, Width: 99999}, "width/height"},
		{"too many frames", videoConfig{QTH: "JO62QM", Start: 0, End: 100 * 120, StepSeconds: 120}, "more than 100 frames"},
	}
	for _, tc := range cases {
		err := s.validate(&tc.cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestStageURL(t *testing.T) {
	s := &service{backend: "https://127.0.0.1:443"}
	got := s.stageURL(videoConfig{
		QTH: "JO62QM", Start: 1000, End: 2000, StepSeconds: 120,
		FPS: 10, Width: 1280, Height: 720, Zoom: 5.5,
		CenterLat: 52.5, CenterLng: 13.4,
		Bands: []string{"20m", "40m"}, Surroundings: true,
	})
	for _, want := range []string{
		"https://127.0.0.1:443/video-stage.html?",
		"qth=JO62QM", "start=1000", "end=2000", "step=120",
		"zoom=5.50", "lat=52.50000", "lng=13.40000",
		"w=1280", "h=720", "bands=20m%2C40m", "surroundings=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stageURL missing %q in %s", want, got)
		}
	}
}
