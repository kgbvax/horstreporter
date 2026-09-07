// horstvideo — standalone video-export service for HorstReporter.
//
// Renders the animated time-travel timeline (map + Band Stats panel, the
// /video-stage page) frame by frame in headless chromium and stitches the
// frames into an MP4 with ffmpeg. Consumes the backend read-only over HTTP —
// the sanctioned sidecar pattern (like cmd/horstprop): the core binary gains
// no rendering dependency, and the render load never lands on the live
// request path.
//
// API (bound to 127.0.0.1 by default; the core proxies /api/video/* here):
//
//	POST /render          {config} -> {"id": ...}          enqueue a render job
//	GET  /job/{id}        -> {status, frames_done, total, video, error}
//	GET  /file/{id}.mp4   -> the finished MP4
//
// One render at a time: headless chromium wants several hundred MB and the
// prod box is small. Frame capture walks the stage's window.__horstVideo
// driver API (stepTo) — the stage page owns all rendering and data fetching.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const jpgQuality = 90

type videoConfig struct {
	QTH          string   `json:"qth"`
	Start        int64    `json:"start"` // unix seconds, first frame's bucket END
	End          int64    `json:"end"`   // unix seconds, exclusive
	StepSeconds  int64    `json:"step_seconds"`
	FPS          int      `json:"fps"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Zoom         float64  `json:"zoom"`
	CenterLat    float64  `json:"center_lat"`
	CenterLng    float64  `json:"center_lng"`
	Bands        []string `json:"bands"` // empty = all enabled
	Surroundings bool     `json:"surroundings"`
	Theme        string   `json:"theme"` // light|dark; empty = stage default (dark)
}

type job struct {
	ID          string      `json:"id"`
	Config      videoConfig `json:"-"`
	Status      string      `json:"status"` // queued|rendering|stitching|done|error
	FramesDone  int         `json:"frames_done"`
	FramesTotal int         `json:"frames_total"`
	Video       string      `json:"video,omitempty"` // download path under /file/
	Error       string      `json:"error,omitempty"`

	createdAt time.Time
}

type service struct {
	mu        sync.Mutex
	jobs      map[string]*job
	queue     chan *job
	backend   string
	chromium  string
	ffmpeg    string
	workDir   string
	outDir    string
	maxFrames int
	nextID    int64
}

func main() {
	listen := flag.String("listen", "127.0.0.1:9961", "job API listen address")
	backend := flag.String("backend", "https://127.0.0.1:443", "HorstReporter origin the stage page is loaded from")
	chromium := flag.String("chromium", "/usr/bin/chromium", "chromium/chrome executable")
	ffmpeg := flag.String("ffmpeg", "/usr/bin/ffmpeg", "ffmpeg executable")
	workDir := flag.String("work", "/tmp/horstvideo", "scratch dir for frame captures")
	outDir := flag.String("out", "/tmp/horstvideo/out", "finished MP4 output dir")
	maxFrames := flag.Int("max-frames", 1800, "hard cap on frames per job")
	flag.Parse()

	if _, err := os.Stat(*chromium); err != nil {
		log.Fatalf("chromium not found at %s (install chromium or pass -chromium)", *chromium)
	}
	for _, dir := range []string{*workDir, *outDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("cannot create %s: %v", dir, err)
		}
	}
	sweepStaleFrameDirs(*workDir, *outDir)

	s := &service{
		jobs:      make(map[string]*job),
		queue:     make(chan *job, 8),
		backend:   strings.TrimRight(*backend, "/"),
		chromium:  *chromium,
		ffmpeg:    *ffmpeg,
		workDir:   *workDir,
		outDir:    *outDir,
		maxFrames: *maxFrames,
	}
	go s.worker()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /render", s.handleRender)
	mux.HandleFunc("GET /job/{id}", s.handleJob)
	mux.HandleFunc("GET /file/{name}", s.handleFile)
	log.Printf("horstvideo listening on %s (backend %s)", *listen, *backend)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

// --- API -----------------------------------------------------------------

// sweepStaleFrameDirs removes per-job frame dirs orphaned by a previous run
// (crash/restart) — the deferred RemoveAll in runJob only covers jobs this
// process executes. Keeps the out dir and the unit's HOME dir.
func sweepStaleFrameDirs(workDir, outDir string) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(workDir, e.Name())
		if p == filepath.Clean(outDir) || e.Name() == "home" {
			continue
		}
		log.Printf("sweeping stale frame dir %s", p)
		_ = os.RemoveAll(p)
	}
}

func (s *service) handleRender(w http.ResponseWriter, r *http.Request) {
	var cfg videoConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&cfg); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.validate(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.nextID++
	id := time.Now().UTC().Format("20060102-150405") + "-" + strconv.FormatInt(s.nextID%1000, 10)
	j := &job{
		ID:          id,
		Config:      cfg,
		Status:      "queued",
		FramesTotal: int((cfg.End - cfg.Start) / cfg.StepSeconds),
		createdAt:   time.Now(),
	}
	s.jobs[id] = j
	s.pruneJobsLocked()
	s.mu.Unlock()

	select {
	case s.queue <- j:
	default:
		// The job keeps its queued entry only while the queue holds it;
		// reject immediately when full so the UI can say so.
		s.mu.Lock()
		delete(s.jobs, id)
		s.mu.Unlock()
		http.Error(w, "render queue full; try again later", http.StatusTooManyRequests)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (s *service) validate(cfg *videoConfig) error {
	cfg.QTH = strings.ToUpper(strings.TrimSpace(cfg.QTH))
	if cfg.QTH == "" {
		return errors.New("qth required")
	}
	if cfg.StepSeconds == 0 {
		cfg.StepSeconds = 120
	}
	if cfg.StepSeconds < 60 || cfg.StepSeconds > 3600 {
		return errors.New("step_seconds must be 60..3600")
	}
	if cfg.FPS == 0 {
		cfg.FPS = 10
	}
	switch cfg.FPS {
	case 5, 10, 15, 30:
	default:
		return errors.New("fps must be one of 5, 10, 15, 30")
	}
	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 720
	}
	if cfg.Width < 640 || cfg.Width > 2560 || cfg.Height < 360 || cfg.Height > 1440 {
		return errors.New("width/height out of range (640..2560 x 360..1440)")
	}
	if cfg.End == 0 {
		cfg.End = time.Now().Unix()
	}
	if cfg.Start == 0 {
		cfg.Start = cfg.End - 24*3600
	}
	if cfg.Start >= cfg.End {
		return errors.New("start must be before end")
	}
	if (cfg.End-cfg.Start)/cfg.StepSeconds > int64(s.maxFrames) {
		return fmt.Errorf("span yields more than %d frames at %ds steps; widen the step or shorten the span", s.maxFrames, cfg.StepSeconds)
	}
	if cfg.Zoom == 0 {
		// No explicit camera => the interactive app's boot default
		// (static/config.js initialZoom = 2); the stage then centers on QTH.
		cfg.Zoom = 2
	}
	if cfg.Theme != "" && cfg.Theme != "light" && cfg.Theme != "dark" {
		return errors.New("theme must be light or dark")
	}
	return nil
}

// pruneJobsLocked keeps the job map bounded: finished jobs older than 24h are
// dropped (their MP4s live in the out dir; pruneOutDir cleans those too).
func (s *service) pruneJobsLocked() {
	cutoff := time.Now().Add(-24 * time.Hour)
	for id, j := range s.jobs {
		if (j.Status == "done" || j.Status == "error") && j.createdAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
	s.pruneOutDir(cutoff)
}

// pruneOutDir removes finished MP4s older than the job retention.
func (s *service) pruneOutDir(cutoff time.Time) {
	entries, err := os.ReadDir(s.outDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".mp4") {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(s.outDir, e.Name()))
			}
		}
	}
}

func (s *service) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		http.Error(w, "unknown job", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

func (s *service) handleFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// Only bare filenames from our own id space; no traversal.
	if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".mp4") {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.outDir, name))
}

// --- worker ---------------------------------------------------------------

func (s *service) worker() {
	for j := range s.queue {
		s.runJob(j)
	}
}

func (s *service) runJob(j *job) {
	s.mu.Lock()
	j.Status = "rendering"
	s.mu.Unlock()

	framesDir := filepath.Join(s.workDir, j.ID)
	outFile := filepath.Join(s.outDir, j.ID+".mp4")
	defer os.RemoveAll(framesDir)

	if err := s.renderFrames(j, framesDir); err != nil {
		s.fail(j, "render: "+err.Error())
		return
	}

	s.mu.Lock()
	j.Status = "stitching"
	s.mu.Unlock()

	if err := s.stitch(j, framesDir, outFile); err != nil {
		s.fail(j, "stitch: "+err.Error())
		return
	}

	s.mu.Lock()
	j.Status = "done"
	j.Video = "/file/" + j.ID + ".mp4"
	s.mu.Unlock()
	log.Printf("job %s done: %s", j.ID, outFile)
}

func (s *service) fail(j *job, msg string) {
	log.Printf("job %s failed: %s", j.ID, msg)
	s.mu.Lock()
	j.Status = "error"
	j.Error = msg
	s.mu.Unlock()
}

// renderFrames drives the stage page: one bucket per frame, one screenshot
// per bucket. The stage owns all rendering; this only walks time.
func (s *service) renderFrames(j *job, framesDir string) error {
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return err
	}
	cfg := j.Config

	ctx, cancel := chromedpContext(s.chromium, cfg.Width, cfg.Height)
	defer cancel()
	// Generous overall budget: ~1.2s per frame plus a minute of slack.
	ctx, cancel2 := context.WithTimeout(ctx, time.Duration(j.FramesTotal)*1200*time.Millisecond+time.Minute)
	defer cancel2()

	if err := chromedp.Run(ctx,
		// The --window-size flag does not pin the viewport in this chromium
		// build (720px window => 577px content) — force exact render dims via
		// the emulation domain, which is what screenshots capture.
		chromedp.EmulateViewport(int64(cfg.Width), int64(cfg.Height)),
		chromedp.Navigate(s.stageURL(cfg)),
		// The stage sets ready once tiles, layout and the band panel are up.
		chromedp.Poll(`window.__horstVideo && window.__horstVideo.ready === true`, nil, chromedp.WithPollingTimeout(90*time.Second)),
	); err != nil {
		return fmt.Errorf("stage load: %w", err)
	}

	for i := int64(0); i < int64(j.FramesTotal); i++ {
		t := cfg.Start + i*cfg.StepSeconds
		// stepTo installs the bucket, rerenders map + band stats and flips
		// frameDone once the paints settled (it returns a promise; the poll
		// is the robust wait either way).
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(fmt.Sprintf(`window.__horstVideo.stepTo(%d)`, t), nil),
			chromedp.Poll(`window.__horstVideo.frameDone === true`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			return fmt.Errorf("frame %d: %w", i, err)
		}

		var shot []byte
		if err := chromedp.Run(ctx, chromedp.FullScreenshot(&shot, 100)); err != nil {
			return fmt.Errorf("frame %d screenshot: %w", i, err)
		}
		if err := pngToJPEG(shot, filepath.Join(framesDir, fmt.Sprintf("frame-%05d.jpg", i)), jpgQuality); err != nil {
			return fmt.Errorf("frame %d encode: %w", i, err)
		}

		s.mu.Lock()
		j.FramesDone = int(i) + 1
		s.mu.Unlock()
	}
	return nil
}

// chromedpContext allocates the browser with the flags a headless render farm
// of one needs: no GPU, no /dev/shm surprises, exact viewport, and TLS errors
// ignored (the backend serves its production cert on the loopback origin).
func chromedpContext(chromium string, width, height int) (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		// Required under systemd (NoNewPrivileges / unprivileged user) — the
		// SUID sandbox helper can't run there.
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("force-device-scale-factor", "1"),
		// Debian chromium's crashpad handler aborts startup under systemd
		// ("--database is required") unless crash reporting is disabled and
		// it has a writable dumps dir.
		chromedp.Flag("disable-crash-reporter", true),
		chromedp.Flag("crash-dumps-dir", "/tmp"),
		chromedp.WindowSize(width, height),
		// The stage needs the tile CDN; nothing else external. Headless still
		// loads it fine, keep the default transport.
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	// NewExecAllocator alone is not a runnable context — chromedp.Run needs a
	// browser session created via NewContext, else it fails "invalid context".
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	return ctx, func() { cancelCtx(); cancelAlloc() }
}

// stageURL assembles the clean stage page URL with the job's parameters.
func (s *service) stageURL(cfg videoConfig) string {
	q := url.Values{}
	q.Set("qth", cfg.QTH)
	q.Set("start", strconv.FormatInt(cfg.Start, 10))
	q.Set("end", strconv.FormatInt(cfg.End, 10))
	q.Set("step", strconv.FormatInt(cfg.StepSeconds, 10))
	q.Set("zoom", strconv.FormatFloat(cfg.Zoom, 'f', 2, 64))
	q.Set("lat", strconv.FormatFloat(cfg.CenterLat, 'f', 5, 64))
	q.Set("lng", strconv.FormatFloat(cfg.CenterLng, 'f', 5, 64))
	q.Set("w", strconv.Itoa(cfg.Width))
	q.Set("h", strconv.Itoa(cfg.Height))
	if len(cfg.Bands) > 0 {
		q.Set("bands", strings.Join(cfg.Bands, ","))
	}
	if cfg.Surroundings {
		q.Set("surroundings", "true")
	}
	if cfg.Theme != "" {
		q.Set("theme", cfg.Theme)
	}
	return s.backend + "/video-stage.html?" + q.Encode()
}

func (s *service) stitch(j *job, framesDir, outFile string) error {
	tmp := outFile + ".part"
	args := []string{
		"-y",
		"-framerate", strconv.Itoa(j.Config.FPS),
		"-i", filepath.Join(framesDir, "frame-%05d.jpg"),
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "20",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		// The .part suffix defeats ffmpeg's output-format inference.
		"-f", "mp4",
		tmp,
	}
	cmd := exec.Command(s.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("job %s ffmpeg failed: %s", j.ID, stderr.String())
		return fmt.Errorf("ffmpeg: %v: %s", err, truncate(stderr.String(), 3000))
	}
	return os.Rename(tmp, outFile)
}

// pngToJPEG converts a captured PNG to a JPEG frame (roughly 3-5x smaller on
// disk — a 48h/2min job is ~1400 frames).
func pngToJPEG(pngBytes []byte, outPath string, quality int) error {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Keep head AND tail: ffmpeg prefixes everything with a long banner, and
	// the actual failure lands at the very end of stderr.
	head, tail := n/3, 2*n/3
	return s[:head] + "…[" + fmt.Sprint(len(s)-head-tail) + " bytes elided]…" + s[len(s)-tail:]
}
