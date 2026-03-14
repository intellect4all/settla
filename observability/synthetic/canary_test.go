package synthetic

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanary_FullPipelineSuccess(t *testing.T) {
	// Track which endpoints were called.
	var quoteCalled, transferCalled, pollCalled atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/quotes":
			quoteCalled.Add(1)
			json.NewEncoder(w).Encode(map[string]string{
				"id": "quote-001",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers":
			transferCalled.Add(1)
			json.NewEncoder(w).Encode(map[string]string{
				"id": "transfer-001",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/transfers/transfer-001":
			pollCalled.Add(1)
			json.NewEncoder(w).Encode(map[string]string{
				"id":     "transfer-001",
				"status": "COMPLETED",
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    100 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 5 * time.Second,
	}

	logger := slog.Default()
	canary := NewCanary(cfg, logger)

	// Run a single execution directly.
	canary.execute()

	// execute() runs once per corridor; default corridors are GBP_NGN and NGN_GBP (2).
	numCorridors := int32(len(cfg.Corridors))
	if numCorridors == 0 {
		numCorridors = 2 // default corridors
	}
	if quoteCalled.Load() != numCorridors {
		t.Errorf("expected quote endpoint called %d times (once per corridor), got %d", numCorridors, quoteCalled.Load())
	}
	if transferCalled.Load() != numCorridors {
		t.Errorf("expected transfer endpoint called %d times (once per corridor), got %d", numCorridors, transferCalled.Load())
	}
	// Poll is called twice per corridor: once during pollTransfer, once during verifyTransfer.
	if pollCalled.Load() < 2*numCorridors {
		t.Errorf("expected transfer GET called at least %d times, got %d", 2*numCorridors, pollCalled.Load())
	}
}

func TestCanary_QuoteFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    100 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 2 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	// Should not panic, just log and increment failure counter.
	canary.execute()
}

func TestCanary_TransferFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/quotes" {
			json.NewEncoder(w).Encode(map[string]string{"id": "quote-002"})
			return
		}
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    100 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 2 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	canary.execute()
}

func TestCanary_PollTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/quotes":
			json.NewEncoder(w).Encode(map[string]string{"id": "quote-003"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers":
			json.NewEncoder(w).Encode(map[string]string{"id": "transfer-003"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/transfers/transfer-003":
			// Always return PENDING, causing a timeout.
			json.NewEncoder(w).Encode(map[string]string{
				"id":     "transfer-003",
				"status": "PENDING",
			})
		}
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    100 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 3 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	canary.execute()
	// Should complete without panic; failure is logged and metric incremented.
}

func TestCanary_TransferFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/quotes":
			json.NewEncoder(w).Encode(map[string]string{"id": "quote-004"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers":
			json.NewEncoder(w).Encode(map[string]string{"id": "transfer-004"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/transfers/transfer-004":
			json.NewEncoder(w).Encode(map[string]string{
				"id":     "transfer-004",
				"status": "FAILED",
			})
		}
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    100 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 5 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	canary.execute()
}

func TestCanary_StartStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": "test-id", "status": "COMPLETED"})
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    50 * time.Millisecond,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_synthetic",
		PollTimeout: 2 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	canary.Start()

	// Let it run a couple of iterations.
	time.Sleep(200 * time.Millisecond)

	canary.Stop()

	// Second stop should be a no-op.
	canary.Stop()
}

func TestCanary_DisabledNoop(t *testing.T) {
	cfg := Config{
		Enabled:    false,
		GatewayURL: "http://localhost:0",
	}

	canary := NewCanary(cfg, slog.Default())
	canary.Start()
	// Should not start when disabled.
	canary.mu.Lock()
	running := canary.running
	canary.mu.Unlock()
	if running {
		t.Error("canary should not be running when disabled")
	}
}

func TestCanary_AuthHeader(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": "test-id", "status": "COMPLETED"})
	}))
	defer srv.Close()

	cfg := Config{
		Enabled:     true,
		Interval:    time.Hour,
		GatewayURL:  srv.URL,
		APIKey:      "sk_test_canary_key",
		PollTimeout: 2 * time.Second,
	}

	canary := NewCanary(cfg, slog.Default())
	canary.execute()

	expected := "Bearer sk_test_canary_key"
	if receivedAuth != expected {
		t.Errorf("expected Authorization header %q, got %q", expected, receivedAuth)
	}
}
