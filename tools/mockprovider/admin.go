package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var depositCounter atomic.Int64

// AdminHandlers provides the control plane for the mock provider.
type AdminHandlers struct {
	cfg *Config
	log *RequestLog
}

// NewAdminHandlers creates a new AdminHandlers.
func NewAdminHandlers(cfg *Config, log *RequestLog) *AdminHandlers {
	return &AdminHandlers{cfg: cfg, log: log}
}

// Register registers all admin routes on the given mux.
func (h *AdminHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/config", h.getConfig)
	mux.HandleFunc("POST /admin/config", h.setConfig)
	mux.HandleFunc("POST /admin/reset", h.resetConfig)
	mux.HandleFunc("POST /admin/scenarios/provider-outage", h.scenarioProviderOutage)
	mux.HandleFunc("POST /admin/scenarios/high-latency", h.scenarioHighLatency)
	mux.HandleFunc("POST /admin/scenarios/partial-failure", h.scenarioPartialFailure)
	mux.HandleFunc("POST /admin/scenarios/simulate-deposit", h.scenarioSimulateDeposit)
	mux.HandleFunc("GET /admin/logs", h.getLogs)
	mux.HandleFunc("GET /admin/stats", h.getStats)
}

func (h *AdminHandlers) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.cfg.Get())
}

func (h *AdminHandlers) setConfig(w http.ResponseWriter, r *http.Request) {
	var update ConfigUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	h.cfg.Update(update)
	writeJSON(w, http.StatusOK, h.cfg.Get())
}

func (h *AdminHandlers) resetConfig(w http.ResponseWriter, r *http.Request) {
	h.cfg.Reset()
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "config reset to defaults",
		"config":  h.cfg.Get(),
	})
}

// scenarioProviderOutage simulates a complete provider outage.
// Body: {"provider": "mock-onramp-gbp"} or omit to fail all providers.
func (h *AdminHandlers) scenarioProviderOutage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if body.Provider != "" {
		// Fail a specific provider.
		snap := h.cfg.Get()
		snap.FailProviders[body.Provider] = true
		h.cfg.Update(ConfigUpdate{FailProviders: snap.FailProviders})
		writeJSON(w, http.StatusOK, map[string]any{
			"scenario": "provider-outage",
			"provider": body.Provider,
			"message":  "provider " + body.Provider + " will now return errors",
			"config":   h.cfg.Get(),
		})
	} else {
		// Global outage: 100% error rate.
		rate := 1.0
		h.cfg.Update(ConfigUpdate{ErrorRate: &rate})
		writeJSON(w, http.StatusOK, map[string]any{
			"scenario": "provider-outage",
			"provider": "all",
			"message":  "all providers will now return errors (100% error rate)",
			"config":   h.cfg.Get(),
		})
	}
}

// scenarioHighLatency simulates high latency (5000ms).
func (h *AdminHandlers) scenarioHighLatency(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LatencyMs *int `json:"latency_ms"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	latency := 5000
	if body.LatencyMs != nil {
		latency = *body.LatencyMs
	}
	h.cfg.Update(ConfigUpdate{LatencyMs: &latency})
	writeJSON(w, http.StatusOK, map[string]any{
		"scenario": "high-latency",
		"message":  "all provider calls will now have high latency",
		"config":   h.cfg.Get(),
	})
}

// scenarioPartialFailure simulates partial failures (30% error rate by default).
func (h *AdminHandlers) scenarioPartialFailure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ErrorRate *float64 `json:"error_rate"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	rate := 0.3
	if body.ErrorRate != nil {
		rate = *body.ErrorRate
	}
	h.cfg.Update(ConfigUpdate{ErrorRate: &rate})
	writeJSON(w, http.StatusOK, map[string]any{
		"scenario": "partial-failure",
		"message":  "providers will now fail intermittently",
		"config":   h.cfg.Get(),
	})
}

// scenarioSimulateDeposit queues a simulated on-chain deposit for detection by the
// blockchain transactions endpoint. The chain monitor will pick it up on its next poll.
//
// Body: {"address": "TXyz...", "amount": "100.00", "token": "USDT", "chain": "tron"}
func (h *AdminHandlers) scenarioSimulateDeposit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
		Token   string `json:"token"`
		Chain   string `json:"chain"`
		From    string `json:"from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if body.Address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}
	if body.Amount == "" {
		body.Amount = "100.00"
	}
	if body.Token == "" {
		body.Token = "USDT"
	}
	if body.Chain == "" {
		body.Chain = "tron"
	}
	if body.From == "" {
		body.From = "TMockSender000000000000000000000"
	}

	deposit := PendingDeposit{
		TxHash:      fmt.Sprintf("mock-deposit-%s-%d", body.Chain, depositCounter.Add(1)),
		From:        body.From,
		To:          body.Address,
		Token:       body.Token,
		Amount:      body.Amount,
		BlockNumber: uint64(2000000 + depositCounter.Load()),
	}

	h.cfg.AddPendingDeposit(deposit)

	writeJSON(w, http.StatusOK, map[string]any{
		"scenario": "simulate-deposit",
		"message":  "deposit queued for detection on next poll",
		"deposit":  deposit,
	})
}

func (h *AdminHandlers) getLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.log.Entries())
}

func (h *AdminHandlers) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.log.GetStats())
}
