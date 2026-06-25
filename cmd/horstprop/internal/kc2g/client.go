// Package kc2g fetches near-real-time ionospheric MUF from prop.kc2g.com
// (worldwide ionosondes via GIRO) and interpolates MUF(3000) at arbitrary path
// points for horstprop's Layer-2 gate. It polls on a schedule and serves from
// cache — never a synchronous fetch in the scoring hot path (docs/horstprop.md §9).
//
// Endpoint/schema confirmed live: GET /api/stations.json returns a JSON array of
// stations, each with `mufd` (MUF(3000) MHz), `time` (ISO, UTC), and a nested
// `station` with STRING `latitude`/`longitude` where longitude is 0–360° East.
package kc2g

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
)

// DefaultURL is the confirmed KC2G stations endpoint.
const DefaultURL = "https://prop.kc2g.com/api/stations.json"

type station struct {
	loc geo.LatLon
	muf float64
	at  time.Time
}

// Client polls KC2G and answers MUF queries from cache.
type Client struct {
	url         string
	http        *http.Client
	maxAge      time.Duration // ignore stations older than this
	idwRadiusKm float64       // only interpolate from stations within this radius

	mu        sync.RWMutex
	stations  []station
	fetchedAt time.Time

	Now func() time.Time // injectable clock
}

// New builds a KC2G client for the given URL (use DefaultURL).
func New(url string) *Client {
	return &Client{
		url:         url,
		http:        &http.Client{Timeout: 15 * time.Second},
		maxAge:      3 * time.Hour,
		idwRadiusKm: 3000,
		Now:         time.Now,
	}
}

// Refresh fetches and caches the latest station snapshot.
func (c *Client) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	st, err := parseStations(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.stations = st
	c.fetchedAt = c.Now()
	c.mu.Unlock()
	return nil
}

type apiStation struct {
	Mufd    *float64 `json:"mufd"`
	Time    string   `json:"time"`
	Station struct {
		Latitude  string `json:"latitude"`
		Longitude string `json:"longitude"`
	} `json:"station"`
}

// parseStations decodes the KC2G array, skipping entries without a usable MUF or
// coordinates. Longitude is normalised from 0–360°E to ±180°E.
func parseStations(body []byte) ([]station, error) {
	var raw []apiStation
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]station, 0, len(raw))
	for _, r := range raw {
		if r.Mufd == nil || *r.Mufd <= 0 {
			continue
		}
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(r.Station.Latitude), 64)
		lon, err2 := strconv.ParseFloat(strings.TrimSpace(r.Station.Longitude), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if lon > 180 {
			lon -= 360
		}
		at, err := time.Parse("2006-01-02T15:04:05", strings.TrimSpace(r.Time))
		if err != nil {
			continue
		}
		out = append(out, station{loc: geo.LatLon{Lat: lat, Lon: lon}, muf: *r.Mufd, at: at.UTC()})
	}
	return out, nil
}

// MUFAt interpolates MUF(3000) in MHz at (lat,lon) using inverse-distance
// weighting over fresh stations within idwRadiusKm. ageMin is the freshest
// contributing station's age. ok=false if no fresh station is in range.
func (c *Client) MUFAt(lat, lon float64) (mufMHz, ageMin float64, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.Now()
	at := geo.LatLon{Lat: lat, Lon: lon}
	var sumW, sumWM float64
	bestAge := time.Duration(math.MaxInt64)
	for _, s := range c.stations {
		age := now.Sub(s.at)
		if age > c.maxAge || age < -5*time.Minute {
			continue
		}
		d := geo.HaversineKm(at, s.loc)
		if d > c.idwRadiusKm {
			continue
		}
		w := 1.0 / (d*d + 1.0)
		sumW += w
		sumWM += w * s.muf
		if age < bestAge {
			bestAge = age
		}
	}
	if sumW == 0 {
		return 0, 0, false
	}
	return sumWM / sumW, bestAge.Minutes(), true
}

// FreshStations returns how many cached stations are within maxAge of now.
func (c *Client) FreshStations() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.Now()
	n := 0
	for _, s := range c.stations {
		if age := now.Sub(s.at); age <= c.maxAge && age >= -5*time.Minute {
			n++
		}
	}
	return n
}
