// Command horstprop is a standalone HF link-quality scoring service. It scores
// the path from the operator's home station to a spotted DX station and returns
// a 0-100 score + grade + confidence + per-layer breakdown.
//
// See docs/horstprop.md for the full spec. Active layers: L1 empirical (rolling
// store fed read-only from HorstReporter), L2 MUF gate (KC2G), blended
// empirical-first. L3 (propagation model) is scaffolded and abstains until built
// on a Linux host. The active layer set is reported by /v1/health and the banner.
//
// Like cmd/horstoperator-agent, this is a separate binary in the same module,
// decoupled over HTTP. It shares only the contract types in internal/propcontract
// and imports no HorstReporter runtime code.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"horstreporter/cmd/horstprop/internal/api"
	"horstreporter/cmd/horstprop/internal/config"
	"horstreporter/cmd/horstprop/internal/engine"
	"horstreporter/cmd/horstprop/internal/feed"
	"horstreporter/cmd/horstprop/internal/geo"
	"horstreporter/cmd/horstprop/internal/kc2g"
	"horstreporter/cmd/horstprop/internal/model"
	"horstreporter/cmd/horstprop/internal/store"
	"horstreporter/internal/propcontract"
)

const version = "v1"

func main() {
	cfg := config.FromEnv(config.Defaults())

	listen := flag.String("listen", cfg.Listen, "scoring API listen address")
	homeGrid := flag.String("home-grid", cfg.HomeGrid, "operator home Maidenhead locator")
	ctyPath := flag.String("cty-path", cfg.CtyPath, "path to AD1C cty.dat (enables callsign→centroid)")
	horstURL := flag.String("horst-url", cfg.HorstBaseURL, "HorstReporter base URL (enables Layer-1 feed), e.g. http://127.0.0.1:8080")
	areaRings := flag.Int("area-rings", cfg.AreaRings, "area-of-interest radius in Maidenhead grid-square rings around home (1 ring ≈ 1° lat / 2° lon ≈ 100–150 km)")
	matchRings := flag.Int("match-rings", cfg.MatchRings, "how near (grid-square rings) a reception must be to the DX to count toward its path score")
	kc2gEnable := flag.Bool("kc2g-enable", cfg.KC2GEnable, "enable the Layer-2 MUF gate from KC2G")
	kc2gURL := flag.String("kc2g-url", cfg.KC2GURL, "KC2G stations JSON endpoint")
	modelEnable := flag.Bool("model-enable", cfg.ModelEnable, "enable the Layer-3 propagation model (Phase 3; not built in this binary)")
	flag.Parse()
	cfg.Listen, cfg.HomeGrid, cfg.CtyPath = *listen, *homeGrid, *ctyPath
	cfg.HorstBaseURL, cfg.AreaRings, cfg.MatchRings = *horstURL, *areaRings, *matchRings
	cfg.KC2GEnable, cfg.KC2GURL = *kc2gEnable, *kc2gURL
	cfg.ModelEnable = *modelEnable

	// cty.dat resolver (optional)
	var resolver engine.Resolver
	if cfg.CtyPath != "" {
		r, err := loadCty(cfg.CtyPath)
		if err != nil {
			log.Printf("horstprop: WARNING cty.dat %q not loaded: %v (callsign→centroid disabled)", cfg.CtyPath, err)
		} else {
			resolver = r
			log.Printf("horstprop: loaded cty.dat from %s", cfg.CtyPath)
		}
	}

	// Layer-1 rolling store (only meaningful with a feed + a resolvable home).
	var st *store.Store
	if cfg.FeedEnabled() {
		if hx, hy, ok := geo.SquareXY(cfg.HomeGrid); ok {
			st = store.New(time.Duration(cfg.WindowMinutes)*time.Minute, hx, hy, cfg.AreaRings)
		} else {
			log.Printf("horstprop: WARNING home-grid %q unparseable; Layer-1 store disabled", cfg.HomeGrid)
		}
	}

	// Layer-2 MUF gate (KC2G), optional. The client serves from cache; a poller
	// refreshes it. engine sees an empty client until the first refresh lands.
	var muf engine.MUFProvider
	var kc *kc2g.Client
	if cfg.KC2GEnable {
		kc = kc2g.New(cfg.KC2GURL)
		muf = kc
	}

	// Layer-3 model (scaffold). Phase 3 swaps model.Disabled{} for a self-hosted
	// ITURHFProp/voacapl-backed provider (docs/horstprop.md §8 Phase 3). Until then
	// it abstains, so scoring runs on Layers 1+2.
	var mdl model.Provider = model.Disabled{}
	if cfg.ModelEnable {
		log.Printf("horstprop: -model-enable set but Layer-3 model is not built in this binary (scaffold) — abstaining")
	}

	eng := engine.New(cfg.HomeGrid, resolver, st, muf, mdl, cfg.MatchRings)
	if !eng.HomeResolved() {
		log.Printf("horstprop: WARNING home-grid %q unparseable; geometry will omit distance/bearing", cfg.HomeGrid)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if kc != nil {
		go pollKC2G(ctx, kc, cfg.KC2GURL)
	} else {
		log.Printf("horstprop: KC2G MUF gate disabled")
	}

	// Layer-1 feed (optional): read-only, resilient reconnect loop feeding the store.
	if st != nil {
		streamURL := cfg.StreamURL()
		log.Printf("horstprop: Layer-1 feed: %s (area %d rings around %s, match %d rings, window %dm)",
			streamURL, cfg.AreaRings, cfg.HomeGrid, cfg.MatchRings, cfg.WindowMinutes)
		go runFeed(ctx, streamURL, st)
		go pruneLoop(ctx, st)
	} else {
		log.Printf("horstprop: no Layer-1 store; scores will be neutral (50/?)")
	}

	scorer := scorerLabel(cfg, st != nil)
	srv := &http.Server{Addr: cfg.Listen, Handler: api.New(eng, cfg.HomeGrid, cfg.FeedEnabled(), scorer).Handler()}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("horstprop %s listening on %s (home %s, scorer: %s)", version, cfg.Listen, cfg.HomeGrid, scorer)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("horstprop: %v", err)
	}
}

// scorerLabel describes which scoring layers are actually active.
func scorerLabel(cfg config.Config, hasStore bool) string {
	var parts []string
	if hasStore {
		parts = append(parts, "L1-empirical")
	}
	if cfg.KC2GEnable {
		parts = append(parts, "L2-mufgate")
	}
	if cfg.ModelEnable {
		parts = append(parts, "L3-model")
	}
	if len(parts) == 0 {
		return "neutral (no layers)"
	}
	return strings.Join(parts, "+")
}

func loadCty(path string) (*geo.CtyResolver, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return geo.ParseCty(f)
}

// runFeed consumes the HorstReporter stream read-only into the Layer-1 store,
// reconnecting on failure.
func runFeed(ctx context.Context, url string, st *store.Store) {
	src := feed.New(url)
	var count int64
	for ctx.Err() == nil {
		err := src.Run(ctx, func(pr propcontract.PropReport) {
			st.Add(pr)
			count++
		})
		if ctx.Err() != nil {
			return
		}
		log.Printf("horstprop: feed disconnected after %d reports (stored=%d, %v); retrying in 5s", count, st.Len(), err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// pollKC2G refreshes the KC2G snapshot at startup and every 5 min (its refresh
// cadence). Failures are logged and retried on the next tick; never fatal.
func pollKC2G(ctx context.Context, kc *kc2g.Client, url string) {
	refresh := func() {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := kc.Refresh(cctx); err != nil {
			log.Printf("horstprop: kc2g refresh failed (%s): %v", url, err)
			return
		}
		log.Printf("horstprop: kc2g refreshed — %d fresh stations", kc.FreshStations())
	}
	refresh()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			refresh()
		}
	}
}

// pruneLoop expires stale store entries periodically.
func pruneLoop(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st.Prune()
		}
	}
}
