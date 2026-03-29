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
	"strings"
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

	// Optional: direct database URLs for post-test consistency checks.
	// If empty, DB-backed checks are skipped (best-effort, no failure).
	TransferDBURL string // e.g. postgres://settla:settla@localhost:6434/settla_transfer?sslmode=disable
	LedgerDBURL   string // e.g. postgres://settla:settla@localhost:6433/settla_ledger?sslmode=disable

	// MaxErrorRate is the maximum acceptable error rate as a percentage (default 1.0%).
	MaxErrorRate float64

	// Zipf distribution for tenant selection (Scenarios G, H, I, J).
	// When true, tenants are selected using Zipf distribution instead of uniform random.
	UseZipf      bool
	ZipfExponent float64 // Default 1.2 — top 1% generates ~50% traffic

	// Spike mode (Scenario E): instant jump, no ramp.
	SpikeMode    bool
	SpikeBaseTPS int // Base TPS before/after spike (default 100)

	// HotSpot mode (Scenario F): concentrate traffic on first tenant.
	HotSpotMode bool
	HotSpotPct  float64 // Percentage of traffic to first tenant (default 80)

	// Settlement mode (Scenario J): trigger settlement after load phase.
	SettlementMode bool

	// OpsAPIKey for settlement trigger (Scenario J).
	OpsAPIKey string
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

	// Tenant selection strategies (initialized in Run)
	zipf    *ZipfDistribution // Non-nil when UseZipf is true
	hotspot *HotspotSelector  // Non-nil when HotSpotMode is true
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

	// Initialize Zipf distribution if configured
	if r.config.UseZipf && len(r.config.Tenants) > 1 {
		exp := r.config.ZipfExponent
		if exp <= 0 {
			exp = 1.2
		}
		r.zipf = NewZipfDistribution(len(r.config.Tenants), exp)
		stats := r.zipf.Stats()
		r.logger.Info("zipf distribution initialized",
			"tenants", stats.TenantCount,
			"exponent", stats.Exponent,
			"top_1pct_traffic", fmt.Sprintf("%.1f%%", stats.Top1PctTraffic*100),
			"top_10pct_traffic", fmt.Sprintf("%.1f%%", stats.Top10PctTraffic*100),
		)
	}

	// Initialize hotspot selector if configured
	if r.config.HotSpotMode && len(r.config.Tenants) >= 2 {
		pct := r.config.HotSpotPct
		if pct <= 0 {
			pct = 80.0
		}
		var err error
		r.hotspot, err = HotspotTenantPool(r.config.Tenants, pct)
		if err != nil {
			return fmt.Errorf("hotspot setup: %w", err)
		}
		r.logger.Info("hotspot mode enabled",
			"hot_tenant", r.config.Tenants[0].ID,
			"hot_pct", pct,
		)
	}

	r.logger.Info("starting load test",
		"target_tps", r.config.TargetTPS,
		"duration", r.config.Duration,
		"tenants", len(r.config.Tenants),
	)

	// Start metrics reporter
	go r.reportMetrics(ctx)

	// Start result collector
	go r.collectResults(ctx)

	if err := r.phaseRampUp(ctx); err != nil {
		return fmt.Errorf("ramp-up phase failed: %w", err)
	}

	if err := r.phaseSustainedPeak(ctx); err != nil {
		return fmt.Errorf("sustained peak phase failed: %w", err)
	}

	if err := r.phaseDrain(ctx); err != nil {
		return fmt.Errorf("drain phase failed: %w", err)
	}

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

	// Select tenant based on configured distribution
	var tenant TenantConfig
	switch {
	case r.hotspot != nil:
		tenant = r.hotspot.Select()
	case r.zipf != nil:
		tenant = r.config.Tenants[r.zipf.Sample()]
	default:
		tenant = r.config.Tenants[rand.Intn(len(r.config.Tenants))]
	}

	// Step 1: Create quote
	quoteStart := time.Now()
	quote, err := r.createQuote(ctx, tenant)
	if err != nil {
		r.inflight.Add(-1)
		r.logger.Error("quote creation failed", "tenant", tenant.ID, "error", err)
		r.metrics.RecordError(categorizeError(err))
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
		r.metrics.RecordError(categorizeError(err))
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
		"recipient": buildRecipient(tenant.Country),
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
		category := categorizeHTTPError(resp.StatusCode)
		r.metrics.RecordError(category)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(respBody))
	}

	r.metrics.RequestsTotal.Add(1)
	return respBody, nil
}

// categorizeError maps an error to a category based on its message.
func categorizeError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "http 429"):
		return "rate_limited_429"
	case strings.Contains(msg, "http 5"):
		return "server_error_5xx"
	case strings.Contains(msg, "http 4"):
		return "client_error_4xx"
	case strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "dial"):
		return "connection_error"
	default:
		return "unknown"
	}
}

// categorizeHTTPError maps an HTTP status code to an error category.
func categorizeHTTPError(statusCode int) string {
	switch {
	case statusCode == 429:
		return "rate_limited_429"
	case statusCode >= 500:
		return "server_error_5xx"
	case statusCode >= 400:
		return "client_error_4xx"
	default:
		return "unknown"
	}
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

// buildRecipient constructs a valid recipient payload with proper payment details
// for the destination country. GB recipients require sort_code or iban.
func buildRecipient(country string) map[string]string {
	switch country {
	case "GB":
		return map[string]string{
			"name":      "Load Test Recipient",
			"country":   "GB",
			"sort_code": "040004",
			"account_number": "12345678",
			"bank_name": "Loadtest Bank UK",
		}
	case "NG":
		return map[string]string{
			"name":           "Load Test Recipient",
			"country":        "NG",
			"account_number": "0123456789",
			"bank_name":      "Loadtest Bank NG",
		}
	case "US":
		return map[string]string{
			"name":    "Load Test Recipient",
			"country": "US",
			"iban":    "US12345678901234567890",
		}
	default:
		return map[string]string{
			"name":    "Load Test Recipient",
			"country": country,
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
	// Sub-command dispatch: "seed" runs the tenant provisioning tool
	if len(os.Args) > 1 && os.Args[1] == "seed" {
		if err := RunSeed(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Sub-command: "report" aggregates JSON results into a single report
	if len(os.Args) > 1 && os.Args[1] == "report" {
		resultsDir := "tests/loadtest/results"
		outputPath := "tests/loadtest/results/aggregate-report.json"
		if len(os.Args) > 2 {
			resultsDir = os.Args[2]
		}
		if len(os.Args) > 3 {
			outputPath = os.Args[3]
		}
		if err := WriteAggregateReport(resultsDir, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "report failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Aggregate report written to %s\n", outputPath)
		return
	}

	var (
		gatewayURL    = flag.String("gateway", "http://localhost:3000", "Gateway URL")
		targetTPS     = flag.Int("tps", 1000, "Target transactions per second")
		duration      = flag.Duration("duration", 10*time.Minute, "Test duration")
		tenants       = flag.Int("tenants", 2, "Number of tenants to simulate")
		rampUp        = flag.Duration("rampup", 30*time.Second, "Ramp-up duration")
		drain         = flag.Duration("drain", 60*time.Second, "Drain duration")
		soakMode      = flag.Bool("soak", false, "Run in soak test mode (with health monitoring)")
		pprofURL      = flag.String("pprof", "http://localhost:6060", "settla-server pprof URL")
		scenarioName  = flag.String("scenario", "", "Named scenario (SmokeTest|SustainedLoad|PeakBurst|SoakTest|SpikeTest|HotSpot|TenantScale20K|TenantScale100K|TenantScalePeak|SettlementBatch|PeakLoad|BurstRecovery|SingleTenantFlood|MultiTenantScale)")
		transferDBURL = flag.String("transfer-db", "", "Transfer DB URL for post-test outbox/stuck-transfer checks (optional)")
		ledgerDBURL   = flag.String("ledger-db", "", "Ledger DB URL for post-test debit=credit balance check (optional)")
		maxErrorRate  = flag.Float64("max-error-rate", 1.0, "Maximum acceptable error rate percentage (default 1.0%)")
		jsonReport    = flag.Bool("json", false, "Write structured JSON result to tests/loadtest/results/")
	)
	flag.Parse()

	// Setup logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Seed tenant configs — these must match the API keys in db/seed/transfer_seed.sql
	seedTenants := []TenantConfig{
		{ID: "a0000000-0000-0000-0000-000000000001", APIKey: "sk_live_lemfi_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "b0000000-0000-0000-0000-000000000002", APIKey: "sk_live_fincra_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "c0000000-0000-0000-0000-000000000003", APIKey: "sk_live_paystack_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "d0000000-0000-0000-0000-000000000004", APIKey: "sk_live_flutterwave_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "e0000000-0000-0000-0000-000000000005", APIKey: "sk_live_chipper_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "f0000000-0000-0000-0000-000000000006", APIKey: "sk_live_moniepoint_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "10000000-0000-0000-0000-000000000007", APIKey: "sk_live_kuda_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "20000000-0000-0000-0000-000000000008", APIKey: "sk_live_opay_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "30000000-0000-0000-0000-000000000009", APIKey: "sk_live_ecobank_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "40000000-0000-0000-0000-000000000010", APIKey: "sk_live_access_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000011", APIKey: "sk_live_palmpay_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000012", APIKey: "sk_live_carbon_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000013", APIKey: "sk_live_fairmoney_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000014", APIKey: "sk_live_wise_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000015", APIKey: "sk_live_worldremit_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000016", APIKey: "sk_live_monzo_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000017", APIKey: "sk_live_revolut_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000018", APIKey: "sk_live_paga_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000019", APIKey: "sk_live_remitly_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000020", APIKey: "sk_live_teamapt_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000021", APIKey: "sk_live_starling_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000022", APIKey: "sk_live_vfd_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000023", APIKey: "sk_live_nala_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000024", APIKey: "sk_live_wema_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000025", APIKey: "sk_live_sendwave_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000026", APIKey: "sk_live_zenith_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000027", APIKey: "sk_live_azimo_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000028", APIKey: "sk_live_gtbank_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000029", APIKey: "sk_live_tymebank_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000030", APIKey: "sk_live_firstbank_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000031", APIKey: "sk_live_xoom_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000032", APIKey: "sk_live_uba_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000033", APIKey: "sk_live_currencycloud_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000034", APIKey: "sk_live_interswitch_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000035", APIKey: "sk_live_transfergo_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000036", APIKey: "sk_live_stanbic_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000037", APIKey: "sk_live_skrill_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000038", APIKey: "sk_live_fcmb_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000039", APIKey: "sk_live_payoneer_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000040", APIKey: "sk_live_sterling_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000041", APIKey: "sk_live_afriex_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000042", APIKey: "sk_live_providus_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000043", APIKey: "sk_live_mukuru_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000044", APIKey: "sk_live_fidelity_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000045", APIKey: "sk_live_taptap_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000046", APIKey: "sk_live_polaris_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000047", APIKey: "sk_live_cellulant_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000048", APIKey: "sk_live_unionbank_demo_key", Currency: "NGN", Country: "GB"},
		{ID: "50000000-0000-0000-0000-000000000049", APIKey: "sk_live_mfsafrica_demo_key", Currency: "GBP", Country: "NG"},
		{ID: "50000000-0000-0000-0000-000000000050", APIKey: "sk_live_heritage_demo_key", Currency: "NGN", Country: "GB"},
	}

	var config LoadTestConfig
	var scenario Scenario

	// Named scenario overrides individual flags.
	if *scenarioName != "" {
		s, ok := GetScenario(*scenarioName)
		if !ok {
			logger.Error("unknown scenario", "name", *scenarioName,
				"available", ScenarioNames())
			os.Exit(1)
		}
		logger.Info("running named scenario", "name", s.Name, "description", s.Description)
		scenario = s
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
			MaxErrorRate:   *maxErrorRate,
		}
		scenario = Scenario{
			Name:        "Custom",
			Description: fmt.Sprintf("Custom: %d TPS, %d tenants, %s", *targetTPS, *tenants, *duration),
			Config:      config,
		}
	}

	// DB URLs are always injectable regardless of scenario/flag mode.
	if *transferDBURL != "" {
		config.TransferDBURL = *transferDBURL
	}
	if *ledgerDBURL != "" {
		config.LedgerDBURL = *ledgerDBURL
	}
	if config.MaxErrorRate == 0 {
		config.MaxErrorRate = *maxErrorRate
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

		// Set up result collector for JSON reporting
		var collector *ResultCollector
		if *jsonReport {
			collector = NewResultCollector(scenario, runner.metrics)
		}

		if err := runner.Run(ctx); err != nil {
			logger.Error("load test failed", "error", err)

			// Still write partial result on failure if JSON mode
			if collector != nil {
				result := collector.Collect(nil)
				result.Passed = false
				result.FailReason = err.Error()
				if path, writeErr := result.WriteJSON("tests/loadtest/results"); writeErr == nil {
					logger.Info("partial result written", "path", path)
				}
			}

			os.Exit(1)
		}

		runner.printMetrics()

		// Write structured JSON result
		if collector != nil {
			result := collector.Collect(nil)
			if path, writeErr := result.WriteJSON("tests/loadtest/results"); writeErr != nil {
				logger.Error("failed to write JSON result", "error", writeErr)
			} else {
				logger.Info("result written", "path", path, "passed", result.Passed)
			}
		}

		logger.Info("load test completed successfully")
	}
}
