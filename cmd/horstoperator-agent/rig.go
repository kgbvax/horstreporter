package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// rigController abstracts the rig CAT backend so the browser contract
// (/v1/rig/tune, /v1/operate) stays stable while the backend can be swapped.
// The WaveLogGate backend below does single-VFO tune only; a future
// rigctld/FLRig backend can add Preview (VFO B) + Split and flip those caps.
type rigController interface {
	// Tune sets the (active) VFO to freqHz with the given rig mode string.
	Tune(ctx context.Context, freqHz int64, mode string) error
	Capabilities() rigCapabilities
}

type rigCapabilities struct {
	Tune    bool
	Preview bool // VFO-B pre-listen (dual-watch) — needs a rigctld/FLRig backend
	Split   bool // split TX/RX — needs a rigctld/FLRig backend
}

// rigStateProvider is an optional capability: a backend that can report the rig's
// live operating state (frequency/mode, and split RX). WaveLogGate implements it
// via its WebSocket status broadcast; Log4OM/none do not. Handlers type-assert.
type rigStateProvider interface {
	RadioState() (rigRadioState, bool)
}

// waveLogGateBackend drives the rig via WaveLogGate's local tune callback:
//
//	GET <base>/{freq_hz}/{mode}
//
// WaveLogGate performs the actual CAT write (via rigctld/FLRig) and applies
// mode-on-QSY. Tune is single-VFO; live status (incl. split RX) is read back over
// WaveLogGate's WebSocket broadcast (see waveLogGateWS), not the tune callback.
type waveLogGateBackend struct {
	base string
	http *http.Client
	ws   *waveLogGateWS // nil when the WS URL couldn't be derived
}

func newWaveLogGateBackend(base string) *waveLogGateBackend {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	return &waveLogGateBackend{
		base: trimmed,
		http: &http.Client{Timeout: 5 * time.Second},
		ws:   newWaveLogGateWS(trimmed),
	}
}

func (b *waveLogGateBackend) Capabilities() rigCapabilities {
	return rigCapabilities{Tune: true} // preview/split intentionally false
}

// RadioState surfaces WaveLogGate's live radio status (freq/mode + split RX) when
// the WS subscription has received at least one update.
func (b *waveLogGateBackend) RadioState() (rigRadioState, bool) {
	if b.ws == nil {
		return rigRadioState{}, false
	}
	return b.ws.RadioState()
}

func (b *waveLogGateBackend) Tune(ctx context.Context, freqHz int64, mode string) error {
	endpoint := fmt.Sprintf("%s/%d/%s", b.base, freqHz, url.PathEscape(mode))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("waveloggate request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("waveloggate returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// rigModeForBackend maps a logical/spot mode (+ frequency for sideband choice)
// to the lowercase CAT mode string the backend expects. WaveLogGate also applies
// mode-on-QSY, so this just needs to be sensible.
func rigModeForBackend(mode string, freqHz int64) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "CW":
		return "cw"
	case "USB":
		return "usb"
	case "LSB":
		return "lsb"
	case "AM":
		return "am"
	case "FM":
		return "fm"
	case "RTTY":
		return "rtty"
	case "FT8", "FT4", "DATA", "PSK", "PSK31", "DIGI", "DIGITAL", "JT65", "MFSK":
		return "data"
	case "SSB", "":
		// sideband by convention: LSB below 10 MHz, USB at/above
		if freqHz > 0 && freqHz < 10_000_000 {
			return "lsb"
		}
		return "usb"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

type rigTuneRequest struct {
	PermitControl bool   `json:"permit_control"`
	FreqHz        int64  `json:"freq_hz"`
	Mode          string `json:"mode"`
}

func (s *server) handleRigTune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.rig == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "rig control not configured (set -rig-transport)"})
		return
	}
	if !s.cfg.ControlPermitted {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "control disabled by agent configuration"})
		return
	}
	var req rigTuneRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !req.PermitControl {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permit_control must be true"})
		return
	}
	if req.FreqHz <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "freq_hz is required"})
		return
	}

	mode := rigModeForBackend(req.Mode, req.FreqHz)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	if err := s.rig.Tune(ctx, req.FreqHz, mode); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "freq_hz": req.FreqHz, "rig_mode": mode})
}

type operateRequest struct {
	PermitControl bool     `json:"permit_control"`
	FreqHz        int64    `json:"freq_hz"`
	Mode          string   `json:"mode"`
	AzimuthDeg    *float64 `json:"azimuth_deg"`
}

// handleOperate is the composite "Tune + Turn": tune the rig AND rotate the beam.
// It returns 200 with per-leg {ok} flags so the UI can report partial success
// ("tuned, rotor failed").
func (s *server) handleOperate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.cfg.ControlPermitted {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "control disabled by agent configuration"})
		return
	}
	var req operateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !req.PermitControl {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permit_control must be true"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	overallOK := true

	rig := map[string]any{"ok": false}
	if req.FreqHz > 0 {
		if s.rig == nil {
			rig["error"] = "rig control not configured"
			overallOK = false
		} else {
			mode := rigModeForBackend(req.Mode, req.FreqHz)
			if err := s.rig.Tune(ctx, req.FreqHz, mode); err != nil {
				rig["error"] = err.Error()
				overallOK = false
			} else {
				rig["ok"] = true
				rig["freq_hz"] = req.FreqHz
				rig["rig_mode"] = mode
			}
		}
	} else {
		rig["skipped"] = "no freq_hz"
	}

	antenna := map[string]any{"ok": false}
	if req.AzimuthDeg != nil && isFinite(*req.AzimuthDeg) {
		az := normalizeAzimuth(*req.AzimuthDeg)
		if err := s.client.RotateToAzimuth(ctx, az); err != nil {
			antenna["error"] = err.Error()
			overallOK = false
		} else {
			s.beginFastPollingForTarget(az)
			antenna["ok"] = true
			antenna["azimuth_deg"] = az
		}
	} else {
		antenna["skipped"] = "no azimuth_deg"
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": overallOK, "rig": rig, "antenna": antenna})
}
