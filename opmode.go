package main

import (
	"encoding/json"
	"net/http"
	"sync"
)

type opModeRuntime struct {
	mu             sync.RWMutex
	controlEnabled bool
}

var opMode = &opModeRuntime{}

func configureOpMode(controlEnabled bool) {

	opMode.mu.Lock()
	defer opMode.mu.Unlock()

	opMode.controlEnabled = controlEnabled
	logInfo("Operator mode backend active in direct-only mode (backend never contacts local agent; control=%v)", opMode.controlEnabled)
}

func (o *opModeRuntime) snapshot() (controlEnabled bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.controlEnabled
}

func opModeStatusHandler(w http.ResponseWriter, r *http.Request) {
	controlEnabled := opMode.snapshot()

	type statusResponse struct {
		Enabled        bool   `json:"enabled"`
		Configured     bool   `json:"configured"`
		ControlEnabled bool   `json:"control_enabled"`
		Mode           string `json:"mode"`
		ProxyActive    bool   `json:"proxy_active"`
		DirectCapable  bool   `json:"direct_capable"`
		AgentURL       string `json:"agent_url,omitempty"`
		AgentReachable bool   `json:"agent_reachable"`
		AgentControl   *bool  `json:"agent_control_permitted,omitempty"`
		Error          string `json:"error,omitempty"`
	}

	resp := statusResponse{
		Enabled:        true,
		Configured:     false,
		ControlEnabled: controlEnabled,
		Mode:           "direct",
		ProxyActive:    false,
		DirectCapable:  true,
		AgentReachable: false,
		Error:          "backend proxy disabled by design; browser must connect to local agent directly",
	}

	writeOpModeJSON(w, http.StatusOK, resp)
}

func writeOpModeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logError("failed writing opmode JSON response: %v", err)
	}
}
