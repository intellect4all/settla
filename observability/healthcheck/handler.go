package healthcheck

import (
	"encoding/json"
	"net/http"
	"runtime"
)

// Handler serves deep health check endpoints for Kubernetes probes and
// debugging. It provides four endpoints:
//
//   - GET /health/live    — liveness: is the process running?
//   - GET /health/ready   — readiness: can this instance serve traffic?
//   - GET /health/startup — startup: has initialization completed?
//   - GET /health         — full report with all check details
type Handler struct {
	checker          *Checker
	maxGoroutines    int
}

// NewHandler creates an HTTP health handler backed by the given Checker.
// maxGoroutines is the liveness threshold for goroutine count (default 100000).
func NewHandler(checker *Checker, maxGoroutines int) *Handler {
	if maxGoroutines <= 0 {
		maxGoroutines = 100000
	}
	return &Handler{
		checker:       checker,
		maxGoroutines: maxGoroutines,
	}
}

// Register mounts all health endpoints on the given ServeMux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.handleFull)
	mux.HandleFunc("/health/live", h.handleLive)
	mux.HandleFunc("/health/ready", h.handleReady)
	mux.HandleFunc("/health/startup", h.handleStartup)
}

// handleLive is the liveness probe. Returns 200 unless the process appears
// deadlocked (goroutine count growing unboundedly). This endpoint never
// calls external dependencies — it checks only the process itself.
func (h *Handler) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	count := runtime.NumGoroutine()
	status := StatusHealthy
	httpStatus := http.StatusOK
	errMsg := ""

	if count > h.maxGoroutines {
		status = StatusUnhealthy
		httpStatus = http.StatusServiceUnavailable
		errMsg = "goroutine count exceeds threshold"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)

	resp := map[string]any{
		"status":     status,
		"goroutines": count,
	}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	json.NewEncoder(w).Encode(resp)
}

// handleReady is the readiness probe. Runs all required health checks.
// Returns 200 if healthy or degraded, 503 if unhealthy.
func (h *Handler) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	report := h.checker.RunChecks(r.Context())
	writeReport(w, report)
}

// handleStartup is the startup probe. Returns 503 until MarkStartupComplete()
// has been called on the checker. After startup, behaves like the readiness probe.
func (h *Handler) handleStartup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.checker.IsStartupComplete() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  StatusUnhealthy,
			"message": "startup in progress",
		})
		return
	}

	report := h.checker.RunChecks(r.Context())
	writeReport(w, report)
}

// handleFull returns the complete health report with all check details.
// Intended for debugging and dashboards, not Kubernetes probes.
func (h *Handler) handleFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	report := h.checker.RunChecks(r.Context())
	writeReport(w, report)
}

func writeReport(w http.ResponseWriter, report *HealthReport) {
	httpStatus := http.StatusOK
	if report.Status == StatusUnhealthy {
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(report)
}
