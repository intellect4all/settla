package synthetic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// SyntheticTenantID is the dedicated tenant for canary transactions.
	// This tenant must exist in the database and be excluded from business reporting.
	SyntheticTenantID = "00000000-0000-0000-0000-000000000099"

	defaultInterval   = 30 * time.Second
	defaultPollPeriod = 60 * time.Second
	defaultPollTick   = 2 * time.Second
)

// Corridor defines a currency pair and amount for canary transactions.
type Corridor struct {
	SourceCurrency string
	DestCurrency   string
	Amount         string // decimal string, e.g. "10"
}

// DefaultCorridors returns the default corridors for canary testing.
func DefaultCorridors() []Corridor {
	return []Corridor{
		{SourceCurrency: "GBP", DestCurrency: "NGN", Amount: "10"},
		{SourceCurrency: "NGN", DestCurrency: "GBP", Amount: "5000"},
	}
}

// Config holds canary configuration, typically populated from environment variables.
type Config struct {
	// Enabled controls whether the canary runs. Default false.
	Enabled bool
	// Interval between canary runs. Default 30s.
	Interval time.Duration
	// GatewayURL is the base URL of the API gateway, e.g. "http://gateway:3000".
	GatewayURL string
	// APIKey is the Bearer token for the synthetic tenant.
	APIKey string
	// PollTimeout is how long to wait for a transfer to complete. Default 60s.
	PollTimeout time.Duration
	// Corridors is the list of currency corridors to test. Default: GBP→NGN, NGN→GBP.
	Corridors []Corridor
}

func (c *Config) applyDefaults() {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.PollTimeout <= 0 {
		c.PollTimeout = defaultPollPeriod
	}
	if len(c.Corridors) == 0 {
		c.Corridors = DefaultCorridors()
	}
}

// Canary runs a lightweight synthetic test transfer through the full pipeline
// to verify end-to-end health. It exercises: quote creation, transfer initiation,
// completion polling, and balance verification.
type Canary struct {
	cfg    Config
	client *http.Client
	logger *slog.Logger

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}

	// Prometheus metrics
	duration *prometheus.HistogramVec
	failures *prometheus.CounterVec
	runs     prometheus.Counter
}

// Package-level metrics registered once to avoid duplicate registration panics.
var (
	syntheticDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "settla_synthetic_duration_seconds",
		Help:    "Duration of synthetic canary steps.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"step", "corridor"})

	syntheticFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "settla_synthetic_failures_total",
		Help: "Total synthetic canary failures by step.",
	}, []string{"step", "corridor"})

	syntheticRuns = promauto.NewCounter(prometheus.CounterOpts{
		Name: "settla_synthetic_runs_total",
		Help: "Total synthetic canary executions.",
	})
)

// NewCanary creates a new synthetic canary with the given configuration.
func NewCanary(cfg Config, logger *slog.Logger) *Canary {
	cfg.applyDefaults()
	return &Canary{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.PollTimeout + 10*time.Second,
		},
		logger:   logger.With("component", "synthetic-canary"),
		stopCh:   make(chan struct{}),
		duration: syntheticDuration,
		failures: syntheticFailures,
		runs:     syntheticRuns,
	}
}

// Start begins the canary loop in a background goroutine. It is safe to call
// multiple times; subsequent calls are no-ops.
func (c *Canary) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running || !c.cfg.Enabled {
		return
	}
	c.running = true
	c.logger.Info("starting synthetic canary",
		"interval", c.cfg.Interval,
		"gateway_url", c.cfg.GatewayURL,
	)
	go c.loop()
}

// Stop terminates the canary loop. It blocks until the current iteration
// finishes (if one is in progress).
func (c *Canary) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return
	}
	close(c.stopCh)
	c.running = false
	c.logger.Info("synthetic canary stopped")
}

func (c *Canary) loop() {
	// Run once immediately on start.
	c.execute()

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.execute()
		}
	}
}

func (c *Canary) execute() {
	c.runs.Inc()

	for _, corridor := range c.cfg.Corridors {
		corridorLabel := corridor.SourceCurrency + "_" + corridor.DestCurrency
		c.executeForCorridor(corridor, corridorLabel)
	}
}

func (c *Canary) executeForCorridor(corridor Corridor, corridorLabel string) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.PollTimeout+5*time.Second)
	defer cancel()

	quoteID, err := c.createQuoteForCorridor(ctx, corridor, corridorLabel)
	if err != nil {
		c.failures.WithLabelValues("quote", corridorLabel).Inc()
		c.logger.Error("synthetic canary: quote creation failed",
			"error", err, "corridor", corridorLabel)
		return
	}

	transferID, err := c.createTransferWithCorridor(ctx, quoteID, corridorLabel)
	if err != nil {
		c.failures.WithLabelValues("transfer", corridorLabel).Inc()
		c.logger.Error("synthetic canary: transfer creation failed",
			"error", err, "quote_id", quoteID, "corridor", corridorLabel)
		return
	}

	err = c.pollTransferWithCorridor(ctx, transferID, corridorLabel)
	if err != nil {
		c.failures.WithLabelValues("poll", corridorLabel).Inc()
		c.logger.Error("synthetic canary: transfer poll failed",
			"error", err, "transfer_id", transferID, "corridor", corridorLabel)
		return
	}

	err = c.verifyTransferWithCorridor(ctx, transferID, corridorLabel)
	if err != nil {
		c.failures.WithLabelValues("verify", corridorLabel).Inc()
		c.logger.Error("synthetic canary: verification failed",
			"error", err, "transfer_id", transferID, "corridor", corridorLabel)
		return
	}

	c.logger.Debug("synthetic canary: corridor round completed",
		"transfer_id", transferID, "corridor", corridorLabel)
}

// ── Step implementations ────────────────────────────────────────────────────

func (c *Canary) createQuoteForCorridor(ctx context.Context, corridor Corridor, corridorLabel string) (string, error) {
	start := time.Now()
	defer func() {
		c.duration.WithLabelValues("quote", corridorLabel).Observe(time.Since(start).Seconds())
	}()

	amount := corridor.Amount
	if amount == "" {
		amount = "0.01"
	}

	body := map[string]any{
		"source_currency":      corridor.SourceCurrency,
		"destination_currency": corridor.DestCurrency,
		"source_amount":        amount,
		"metadata": map[string]string{
			"synthetic": "true",
		},
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/quotes", body)
	if err != nil {
		return "", fmt.Errorf("settla-synthetic: creating quote: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("settla-synthetic: quote returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("settla-synthetic: decoding quote response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("settla-synthetic: quote response missing id")
	}

	return result.ID, nil
}

func (c *Canary) createTransfer(ctx context.Context, quoteID string) (string, error) {
	// Default corridor label for backward compatibility.
	return c.createTransferWithCorridor(ctx, quoteID, "default")
}

func (c *Canary) createTransferWithCorridor(ctx context.Context, quoteID string, corridorLabel string) (string, error) {
	start := time.Now()
	defer func() { c.duration.WithLabelValues("transfer", corridorLabel).Observe(time.Since(start).Seconds()) }()

	body := map[string]any{
		"quote_id":        quoteID,
		"idempotency_key": fmt.Sprintf("synthetic-%d", time.Now().UnixNano()),
		"metadata": map[string]string{
			"synthetic": "true",
		},
	}

	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/transfers", body)
	if err != nil {
		return "", fmt.Errorf("settla-synthetic: creating transfer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("settla-synthetic: transfer returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("settla-synthetic: decoding transfer response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("settla-synthetic: transfer response missing id")
	}

	return result.ID, nil
}

func (c *Canary) pollTransfer(ctx context.Context, transferID string) error {
	return c.pollTransferWithCorridor(ctx, transferID, "default")
}

func (c *Canary) pollTransferWithCorridor(ctx context.Context, transferID string, corridorLabel string) error {
	start := time.Now()
	defer func() { c.duration.WithLabelValues("poll", corridorLabel).Observe(time.Since(start).Seconds()) }()

	deadline := time.After(c.cfg.PollTimeout)
	tick := time.NewTicker(defaultPollTick)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("settla-synthetic: context cancelled polling transfer %s: %w", transferID, ctx.Err())
		case <-deadline:
			return fmt.Errorf("settla-synthetic: timeout waiting for transfer %s to complete", transferID)
		case <-tick.C:
			status, err := c.getTransferStatus(ctx, transferID)
			if err != nil {
				c.logger.Warn("synthetic canary: poll error", "error", err, "transfer_id", transferID)
				continue
			}
			switch status {
			case "COMPLETED", "completed":
				return nil
			case "FAILED", "failed", "REJECTED", "rejected":
				return fmt.Errorf("settla-synthetic: transfer %s reached terminal state %s", transferID, status)
			}
			// Still in progress, keep polling.
		}
	}
}

func (c *Canary) getTransferStatus(ctx context.Context, transferID string) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/transfers/"+transferID, nil)
	if err != nil {
		return "", fmt.Errorf("settla-synthetic: getting transfer status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("settla-synthetic: get transfer returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("settla-synthetic: decoding transfer status: %w", err)
	}

	return result.Status, nil
}

func (c *Canary) verifyTransfer(ctx context.Context, transferID string) error {
	return c.verifyTransferWithCorridor(ctx, transferID, "default")
}

func (c *Canary) verifyTransferWithCorridor(ctx context.Context, transferID string, corridorLabel string) error {
	start := time.Now()
	defer func() { c.duration.WithLabelValues("verify", corridorLabel).Observe(time.Since(start).Seconds()) }()

	// Verify: re-fetch transfer and confirm it has status=completed.
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/transfers/"+transferID, nil)
	if err != nil {
		return fmt.Errorf("settla-synthetic: verifying transfer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("settla-synthetic: verify returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("settla-synthetic: decoding verify response: %w", err)
	}

	if result.Status != "COMPLETED" && result.Status != "completed" {
		return fmt.Errorf("settla-synthetic: expected COMPLETED, got %s for transfer %s", result.Status, transferID)
	}

	return nil
}

// ── HTTP helper ─────────────────────────────────────────────────────────────

func (c *Canary) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("settla-synthetic: marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.GatewayURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("settla-synthetic: creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	req.Header.Set("X-Synthetic", "true")

	return c.client.Do(req)
}
