// Package loadtest provides a load testing harness for Settla.
//
// This harness proves Settla can handle 50M transactions/day by sustaining
// peak TPS (5,000) for extended periods (10 minutes).
//
// Usage:
//
//	go run ./tests/loadtest -tps=5000 -duration=10m -tenants=10
//
// The load test runs in 4 phases:
//
//  1. Ramp-up (30s): Gradually increase from 0 to target TPS
//  2. Sustained peak (configurable): Maintain target TPS
//  3. Drain (60s): Wait for in-flight transfers to complete
//  4. Verification: Verify data consistency
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"golang.org/x/time/rate"
)

// LoadTestConfig configures a load test run.
type LoadTestConfig struct {
	GatewayURL     string
	Tenants        []TenantConfig
	TargetTPS      int
	Duration       time.Duration
	RampUpDuration time.Duration
	DrainDuration  time.Duration
}

// TenantConfig represents a test tenant with API credentials.
type TenantConfig struct {
	ID       string
	APIKey   string
	Currency string // Primary currency for this tenant
	Country  string // Primary recipient country
}

// LoadTestRunner orchestrates the load test.
type LoadTestRunner struct {
	config     LoadTestConfig
	metrics    *LoadTestMetrics
	logger     *slog.Logger
	client     *http.Client
	stopCh     chan struct{}
	wg         sync.WaitGroup
	inflight   atomic.Int64
	transferCh chan TransferResult
	startTime  time.Time

	// TPS tracking (updated every second by reportMetrics)
	lastTPSCheck   time.Time
	lastTPSCreated int64
}

// TransferResult tracks the outcome of a single transfer flow.
type TransferResult struct {
	TransferID     string
	TenantID       string
	Status         string
	Amount         decimal.Decimal
	SourceCurrency string
	LatencyMs      int64 // End-to-end latency
	Error          error
}

// NewLoadTestRunner creates a new load test runner.
func NewLoadTestRunner(config LoadTestConfig, logger *slog.Logger) *LoadTestRunner {
	return &LoadTestRunner{
		config:     config,
		metrics:    NewLoadTestMetrics(),
		logger:     logger,
		client:     &http.Client{Timeout: 30 * time.Second},
		stopCh:     make(chan struct{}),
		transferCh: make(chan TransferResult, 10000),
	}
}

// Run executes the complete load test with all phases.
func (r *LoadTestRunner) Run(ctx context.Context) error {
	r.startTime = time.Now()
	r.lastTPSCheck = r.startTime
	r.logger.Info("starting load test",
		"target_tps", r.config.TargetTPS,
		"duration", r.config.Duration,
		"tenants", len(r.config.Tenants),
	)

	// Start metrics reporter
	go r.reportMetrics(ctx)

	// Start result collector
	go r.collectResults(ctx)

	// Phase 1: Ramp-up
	if err := r.phaseRampUp(ctx); err != nil {
		return fmt.Errorf("ramp-up phase failed: %w", err)
	}

	// Phase 2: Sustained peak
	if err := r.phaseSustainedPeak(ctx); err != nil {
		return fmt.Errorf("sustained peak phase failed: %w", err)
	}

	// Phase 3: Drain
	if err := r.phaseDrain(ctx); err != nil {
		return fmt.Errorf("drain phase failed: %w", err)
	}

	// Phase 4: Verification
	if err := r.phaseVerification(ctx); err != nil {
		return fmt.Errorf("verification phase failed: %w", err)
	}

	return nil
}

// phaseRampUp gradually increases load from 0 to target TPS.
// Workers are launched with a shared rate limiter whose limit increases
// linearly over RampUpDuration, warming caches and connection pools before
// the sustained peak phase begins.
func (r *LoadTestRunner) phaseRampUp(ctx context.Context) error {
	r.logger.Info("phase 1: ramp-up", "duration", r.config.RampUpDuration)

	rampCtx, rampCancel := context.WithTimeout(ctx, r.config.RampUpDuration)
	defer rampCancel()

	// Start with 1 TPS; workers block on limiter until rate increases.
	limiter := rate.NewLimiter(1, 1)

	// Cap ramp workers so we don't spawn thousands at high TPS targets.
	numWorkers := r.config.TargetTPS / 20
	if numWorkers < 5 {
		numWorkers = 5
	}
	if numWorkers > 100 {
		numWorkers = 100
	}

	var rampWg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		rampWg.Add(1)
		go func() {
			defer rampWg.Done()
			for {
				select {
				case <-rampCtx.Done():
					return
				default:
				}
				if err := limiter.Wait(rampCtx); err != nil {
					return
				}
				r.executeTransferFlow(rampCtx)
			}
		}()
	}

	// Gradually increase the rate once per second.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-rampCtx.Done():
			rampWg.Wait()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.logger.Info("ramp-up complete")
			return nil
		case <-ticker.C:
			elapsed := time.Since(start)
			progress := float64(elapsed) / float64(r.config.RampUpDuration)
			currentTPS := int(float64(r.config.TargetTPS) * progress)
			if currentTPS < 1 {
				currentTPS = 1
			}
			limiter.SetLimit(rate.Limit(currentTPS))
			limiter.SetBurst(currentTPS)
		}
	}
}

// phaseSustainedPeak maintains target TPS for the configured duration.
func (r *LoadTestRunner) phaseSustainedPeak(ctx context.Context) error {
	r.logger.Info("phase 2: sustained peak",
		"target_tps", r.config.TargetTPS,
		"duration", r.config.Duration,
	)

	// Create rate limiter for target TPS
	limiter := rate.NewLimiter(rate.Limit(r.config.TargetTPS), r.config.TargetTPS)

	ctx, cancel := context.WithTimeout(ctx, r.config.Duration)
	defer cancel()

	// Launch worker pool
	for i := 0; i < r.config.TargetTPS/10; i++ {
		r.wg.Add(1)
		go r.transferWorker(ctx, limiter)
	}

	// Wait for duration
	<-ctx.Done()

	// Signal workers to stop
	close(r.stopCh)
	r.wg.Wait()

	r.logger.Info("sustained peak complete")
	return nil
}

// phaseDrain waits for all in-flight transfers to complete.
func (r *LoadTestRunner) phaseDrain(ctx context.Context) error {
	r.logger.Info("phase 3: drain", "duration", r.config.DrainDuration)

	ctx, cancel := context.WithTimeout(ctx, r.config.DrainDuration)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			inflight := r.inflight.Load()
			if inflight > 0 {
				return fmt.Errorf("drain timeout with %d transfers still in-flight", inflight)
			}
			r.logger.Info("drain complete")
			return nil
		case <-ticker.C:
			inflight := r.inflight.Load()
			r.logger.Info("draining", "inflight", inflight)
			if inflight == 0 {
				r.logger.Info("drain complete")
				return nil
			}
		}
	}
}

// phaseVerification verifies data consistency after the test.
func (r *LoadTestRunner) phaseVerification(ctx context.Context) error {
	r.logger.Info("phase 4: verification")

	v := NewVerifier(r.config, r.metrics, r.logger)
	report, err := v.VerifyConsistency(ctx)
	if report != nil {
		fmt.Print(report.String())

		// Persist report to file
		reportDir := "tests/loadtest/reports"
		if mkErr := os.MkdirAll(reportDir, 0o755); mkErr == nil {
			ts := time.Now().Format("20060102-150405")
			path := fmt.Sprintf("%s/verification-%s.txt", reportDir, ts)
			_ = os.WriteFile(path, []byte(report.String()), 0o644)
		}
	}
	return err
}

// transferWorker continuously creates transfers until stopped.
func (r *LoadTestRunner) transferWorker(ctx context.Context, limiter *rate.Limiter) {
	defer r.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		default:
		}

		// Wait for rate limiter
		if err := limiter.Wait(ctx); err != nil {
			return
		}

		// Execute transfer flow
		r.executeTransferFlow(ctx)
	}
}

// executeTransferFlow creates a quote + transfer, then polls asynchronously.
// The worker is not blocked by polling, so it can keep creating transfers.
func (r *LoadTestRunner) executeTransferFlow(ctx context.Context) {
	r.inflight.Add(1)

	// Track peak inflight
	if peak := r.metrics.PeakInflight.Load(); peak < r.inflight.Load() {
		r.metrics.PeakInflight.Store(r.inflight.Load())
	}

	// Select random tenant
	tenant := r.config.Tenants[rand.Intn(len(r.config.Tenants))]

	// Step 1: Create quote
	quoteStart := time.Now()
	quote, err := r.createQuote(ctx, tenant)
	if err != nil {
		r.inflight.Add(-1)
		r.logger.Error("quote creation failed", "tenant", tenant.ID, "error", err)
		r.metrics.RecordError("quote_create_failed")
		r.transferCh <- TransferResult{Error: err, TenantID: tenant.ID}
		return
	}
	r.metrics.RecordQuoteLatency(time.Since(quoteStart))

	// Step 2: Create transfer
	createStart := time.Now()
	transfer, err := r.createTransfer(ctx, tenant, quote)
	if err != nil {
		r.inflight.Add(-1)
		r.logger.Error("transfer creation failed", "tenant", tenant.ID, "error", err)
		r.metrics.RecordError("transfer_create_failed")
		r.transferCh <- TransferResult{Error: err, TenantID: tenant.ID}
		return
	}
	r.metrics.RecordCreateLatency(time.Since(createStart))
	r.metrics.TransfersCreated.Add(1)

	// Step 3: Poll asynchronously with its own context (not tied to test phase duration)
	start := time.Now()
	go func() {
		defer r.inflight.Add(-1)

		pollCtx, pollCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer pollCancel()

		pollStart := time.Now()
		status, err := r.pollTransfer(pollCtx, tenant, transfer.ID)
		if err != nil {
			r.logger.Error("transfer polling failed", "tenant", tenant.ID, "transfer_id", transfer.ID, "error", err)
			r.metrics.RecordError("poll_failed")
			r.transferCh <- TransferResult{Error: err, TenantID: tenant.ID}
			return
		}
		r.metrics.RecordPollLatency(time.Since(pollStart))

		endToEnd := time.Since(start)
		r.metrics.RecordEndToEndLatency(endToEnd)

		switch status {
		case "COMPLETED":
			r.metrics.TransfersCompleted.Add(1)
		case "FAILED":
			r.metrics.TransfersFailed.Add(1)
		}

		amount, _ := decimal.NewFromString(transfer.SourceAmount)
		r.transferCh <- TransferResult{
			TransferID:     transfer.ID,
			TenantID:       tenant.ID,
			Status:         status,
			Amount:         amount,
			SourceCurrency: transfer.SourceCurrency,
			LatencyMs:      endToEnd.Milliseconds(),
		}
	}()
}

// QuoteResponse represents a quote API response.
type QuoteResponse struct {
	ID             string `json:"id"`
	SourceCurrency string `json:"source_currency"`
	SourceAmount   string `json:"source_amount"`
	DestCurrency   string `json:"dest_currency"`
	DestAmount     string `json:"dest_amount"`
}

// createQuote creates a new quote via the API.
func (r *LoadTestRunner) createQuote(ctx context.Context, tenant TenantConfig) (*QuoteResponse, error) {
	body := map[string]interface{}{
		"source_currency": tenant.Currency,
		"source_amount":   randomAmount(tenant.Currency).String(),
		"dest_currency":   randomDestCurrency(tenant.Currency),
		"dest_country":    tenant.Country,
	}

	resp, err := r.doRequest(ctx, "POST", "/v1/quotes", tenant.APIKey, body)
	if err != nil {
		return nil, err
	}

	var quote QuoteResponse
	if err := json.Unmarshal(resp, &quote); err != nil {
		return nil, fmt.Errorf("unmarshal quote: %w", err)
	}

	return &quote, nil
}

// TransferResponse represents a transfer API response.
type TransferResponse struct {
	ID             string `json:"id"`
	SourceCurrency string `json:"source_currency"`
	SourceAmount   string `json:"source_amount"`
	DestCurrency   string `json:"dest_currency"`
	Status         string `json:"status"`
}

// createTransfer creates a new transfer via the API.
func (r *LoadTestRunner) createTransfer(ctx context.Context, tenant TenantConfig, quote *QuoteResponse) (*TransferResponse, error) {
	body := map[string]interface{}{
		"idempotency_key": uuid.New().String(),
		"source_currency": quote.SourceCurrency,
		"source_amount":   quote.SourceAmount,
		"dest_currency":   quote.DestCurrency,
		"quote_id":        quote.ID,
		"sender": map[string]string{
			"name":  "Load Test Sender",
			"email": "loadtest@example.com",
			"country": func() string {
				if tenant.Currency == "GBP" {
					return "GB"
				}
				return "NG"
			}(),
		},
		"recipient": map[string]string{
			"name":    "Load Test Recipient",
			"country": tenant.Country,
		},
	}

	resp, err := r.doRequest(ctx, "POST", "/v1/transfers", tenant.APIKey, body)
	if err != nil {
		return nil, err
	}

	var transfer TransferResponse
	if err := json.Unmarshal(resp, &transfer); err != nil {
		return nil, fmt.Errorf("unmarshal transfer: %w", err)
	}

	return &transfer, nil
}

// pollTransfer polls a transfer until it reaches a terminal state.
func (r *LoadTestRunner) pollTransfer(ctx context.Context, tenant TenantConfig, transferID string) (string, error) {
	maxAttempts := 60
	pollInterval := time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		resp, err := r.doRequest(ctx, "GET", fmt.Sprintf("/v1/transfers/%s", transferID), tenant.APIKey, nil)
		if err != nil {
			return "", err
		}

		var transfer TransferResponse
		if err := json.Unmarshal(resp, &transfer); err != nil {
			return "", fmt.Errorf("unmarshal transfer: %w", err)
		}

		// Status may come as "COMPLETED" or "TRANSFER_STATUS_COMPLETED" (proto enum)
		switch transfer.Status {
		case "COMPLETED", "FAILED", "REFUNDED",
			"TRANSFER_STATUS_COMPLETED", "TRANSFER_STATUS_FAILED", "TRANSFER_STATUS_REFUNDED":
			// Normalize to short form
			status := transfer.Status
			if len(status) > 16 {
				status = status[len("TRANSFER_STATUS_"):]
			}
			return status, nil
		}

		time.Sleep(pollInterval)
	}

	return "", fmt.Errorf("transfer %s did not reach terminal state after %d attempts", transferID, maxAttempts)
}

// doRequest makes an HTTP request to the gateway.
func (r *LoadTestRunner) doRequest(ctx context.Context, method, path, apiKey string, body interface{}) ([]byte, error) {
	url := r.config.GatewayURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(respBody))
	}

	r.metrics.RequestsTotal.Add(1)
	return respBody, nil
}

// reportMetrics prints live metrics every 5 seconds and tracks current TPS.
func (r *LoadTestRunner) reportMetrics(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.printMetrics()
		}
	}
}

// printMetrics prints current metrics in the live dashboard format.
func (r *LoadTestRunner) printMetrics() {
	created := r.metrics.TransfersCreated.Load()
	completed := r.metrics.TransfersCompleted.Load()
	failed := r.metrics.TransfersFailed.Load()

	// Compute current TPS over the last 5-second window.
	now := time.Now()
	windowSec := now.Sub(r.lastTPSCheck).Seconds()
	currentTPS := int64(0)
	if windowSec > 0 {
		currentTPS = int64(float64(created-r.lastTPSCreated) / windowSec)
	}
	r.lastTPSCheck = now
	r.lastTPSCreated = created

	r.metrics.CurrentTPS.Store(currentTPS)
	if currentTPS > r.metrics.PeakTPS.Load() {
		r.metrics.PeakTPS.Store(currentTPS)
	}

	// Elapsed time for the timestamp prefix.
	elapsed := time.Duration(0)
	if !r.startTime.IsZero() {
		elapsed = time.Since(r.startTime)
	}
	mins := int(elapsed.Minutes())
	secs := int(elapsed.Seconds()) % 60

	// Memory stats.
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memMB := memStats.HeapAlloc / (1024 * 1024)
	goroutines := runtime.NumGoroutine()

	// Latency stats.
	qp50, qp95, qp99 := r.metrics.quoteLatency.Stats()
	cp50, cp95, cp99 := r.metrics.createLatency.Stats()
	ep50, ep95, ep99 := r.metrics.endToEndLatency.Stats()

	// Error summary.
	r.metrics.errorsMu.RLock()
	errSummary := ""
	for code, cnt := range r.metrics.errors {
		errSummary += fmt.Sprintf(" %s=%d", code, cnt.Load())
	}
	r.metrics.errorsMu.RUnlock()
	if errSummary == "" {
		errSummary = " (none)"
	}

	fmt.Printf("[%02d:%02d] TPS: %d/%d | Created: %d | Completed: %d | Failed: %d\n",
		mins, secs, currentTPS, r.config.TargetTPS, created, completed, failed)
	fmt.Printf("Quote  p50: %v p95: %v p99: %v\n", qp50.Round(time.Millisecond), qp95.Round(time.Millisecond), qp99.Round(time.Millisecond))
	fmt.Printf("Create p50: %v p95: %v p99: %v\n", cp50.Round(time.Millisecond), cp95.Round(time.Millisecond), cp99.Round(time.Millisecond))
	fmt.Printf("E2E    p50: %v p95: %v p99: %v\n", ep50.Round(time.Millisecond), ep95.Round(time.Millisecond), ep99.Round(time.Millisecond))
	fmt.Printf("Errors:%s\n", errSummary)
	fmt.Printf("Memory: %dMB | Goroutines: %d\n\n", memMB, goroutines)
}

// collectResults collects transfer results from the channel.
func (r *LoadTestRunner) collectResults(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-r.transferCh:
			r.metrics.AddResult(result)
		}
	}
}

// randomAmount generates a random transfer amount appropriate for the currency.
// GBP: 100-10,000; NGN: 10,000-1,000,000 (reflects real-world ranges).
func randomAmount(currency string) decimal.Decimal {
	switch currency {
	case "NGN":
		amount := 10000 + rand.Intn(990000)
		return decimal.NewFromInt(int64(amount))
	default:
		amount := 100 + rand.Intn(9900)
		return decimal.NewFromInt(int64(amount))
	}
}

// randomDestCurrency returns a destination currency based on source.
func randomDestCurrency(source string) string {
	if source == "GBP" {
		return "NGN"
	}
	return "GBP"
}

func main() {
	var (
		gatewayURL   = flag.String("gateway", "http://localhost:3000", "Gateway URL")
		targetTPS    = flag.Int("tps", 1000, "Target transactions per second")
		duration     = flag.Duration("duration", 10*time.Minute, "Test duration")
		tenants      = flag.Int("tenants", 2, "Number of tenants to simulate")
		rampUp       = flag.Duration("rampup", 30*time.Second, "Ramp-up duration")
		drain        = flag.Duration("drain", 60*time.Second, "Drain duration")
		soakMode     = flag.Bool("soak", false, "Run in soak test mode (with health monitoring)")
		pprofURL     = flag.String("pprof", "http://localhost:6060", "settla-server pprof URL")
		scenarioName = flag.String("scenario", "", "Named scenario (PeakLoad|SustainedLoad|BurstRecovery|SingleTenantFlood|MultiTenantScale)")
	)
	flag.Parse()

	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Seed tenant configs — these must match the API keys in db/seed/transfer_seed.sql
	seedTenants := []TenantConfig{
		{
			ID:       "a0000000-0000-0000-0000-000000000001",
			APIKey:   "sk_live_lemfi_demo_key",
			Currency: "GBP",
			Country:  "NG",
		},
		{
			ID:       "b0000000-0000-0000-0000-000000000002",
			APIKey:   "sk_live_fincra_demo_key",
			Currency: "NGN",
			Country:  "GB",
		},
	}

	var config LoadTestConfig

	// Named scenario overrides individual flags.
	if *scenarioName != "" {
		s, ok := GetScenario(*scenarioName)
		if !ok {
			logger.Error("unknown scenario", "name", *scenarioName,
				"available", ScenarioNames())
			os.Exit(1)
		}
		logger.Info("running named scenario", "name", s.Name, "description", s.Description)
		config = s.Config
		config.GatewayURL = *gatewayURL // always allow gateway override
	} else {
		// Generate tenant configs — cycle through seed tenants
		tenantConfigs := make([]TenantConfig, *tenants)
		for i := 0; i < *tenants; i++ {
			tenantConfigs[i] = seedTenants[i%len(seedTenants)]
		}

		config = LoadTestConfig{
			GatewayURL:     *gatewayURL,
			Tenants:        tenantConfigs,
			TargetTPS:      *targetTPS,
			Duration:       *duration,
			RampUpDuration: *rampUp,
			DrainDuration:  *drain,
		}
	}

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received shutdown signal, stopping test...")
		cancel()
	}()

	if *soakMode {
		// Soak test mode: wraps load test with continuous health monitoring
		soakConfig := DefaultSoakConfig()
		soakConfig.LoadTestConfig = config
		soakConfig.PprofURL = *pprofURL

		soakRunner := NewSoakRunner(soakConfig, logger)
		report, err := soakRunner.Run(ctx)

		if report != nil {
			fmt.Println(report.String())
			if writeErr := report.WriteToFile("tests/reports/soak-report.txt"); writeErr != nil {
				logger.Error("failed to write soak report", "error", writeErr)
			}
		}

		if err != nil {
			logger.Error("soak test failed", "error", err)
			os.Exit(1)
		}

		logger.Info("soak test completed successfully")
	} else {
		// Standard load test mode
		runner := NewLoadTestRunner(config, logger)

		if err := runner.Run(ctx); err != nil {
			logger.Error("load test failed", "error", err)
			os.Exit(1)
		}

		runner.printMetrics()
		logger.Info("load test completed successfully")
	}
}
