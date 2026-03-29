// Package main implements a standalone HTTP mock provider service for Settla demos.
//
// It simulates payment provider behavior with configurable latency, error rates,
// and specific failure scenarios. An admin API allows operators to dynamically
// control provider behavior during live demos.
//
// Usage:
//
//	MOCKPROVIDER_PORT=9095 go run ./tools/mockprovider/
//
// Provider endpoints:
//
//	POST /api/onramp/quote       — Get a mock on-ramp quote
//	POST /api/onramp/execute     — Execute a mock on-ramp transaction
//	POST /api/offramp/quote      — Get a mock off-ramp quote
//	POST /api/offramp/execute    — Execute a mock off-ramp transaction
//	GET  /api/status/{txId}      — Get transaction status
//	POST /api/blockchain/send    — Send a mock blockchain transaction
//	GET  /api/blockchain/balance — Get mock balance
//
// Admin endpoints:
//
//	GET  /admin/config                      — View current config
//	POST /admin/config                      — Update config (partial update)
//	POST /admin/reset                       — Reset config to defaults
//	POST /admin/scenarios/provider-outage   — Simulate provider outage
//	POST /admin/scenarios/high-latency      — Simulate high latency
//	POST /admin/scenarios/partial-failure   — Simulate partial failures
//	GET  /admin/logs                        — View recent request log
//	GET  /admin/stats                       — View aggregate statistics
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := os.Getenv("MOCKPROVIDER_PORT")
	if port == "" {
		port = "9095"
	}

	cfg := DefaultConfig()
	reqLog := NewRequestLog()

	mux := http.NewServeMux()

	// Health check.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Provider-facing handlers.
	providerHandlers := NewProviderHandlers(cfg, reqLog)
	providerHandlers.Register(mux)

	// Admin control handlers.
	adminHandlers := NewAdminHandlers(cfg, reqLog)
	adminHandlers.Register(mux)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("mockprovider: listening on :%s", port)
		log.Printf("mockprovider: admin API at http://localhost:%s/admin/config", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mockprovider: server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("mockprovider: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("mockprovider: shutdown error: %v", err)
	}

	fmt.Println("mockprovider: stopped")
}
