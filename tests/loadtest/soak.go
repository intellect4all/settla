package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SoakConfig configures a soak test run.
type SoakConfig struct {
	LoadTestConfig                   // Embeds the base load test config
	CheckInterval    time.Duration   // How often to run health checks (default: 60s)
	BaselineWindow   time.Duration   // How long to collect baseline metrics (default: 5min)
	MaxMemoryGrowth  int64           // Max RSS growth in bytes before failure (default: 50MB)
	MaxGoroutineGrowth int64         // Max goroutine count growth before failure (default: 1000)
	MaxP99Degradation float64        // Max p99 latency degradation factor (default: 2.0 = 100% increase)
	MaxErrorRate     float64          // Max error rate before failure (default: 0.01 = 1%)
	StuckTimeout     time.Duration   // How long a transfer can be non-terminal (default: 5min)
	PprofURL         string          // settla-server pprof URL (default: http://localhost:6060)
	ProfileDir       string          // Directory to save profiles (default: tests/loadtest/profiles)
	TimeSeriesFile   string          // File to write time-series JSON (default: tests/loadtest/soak-metrics.json)

	// Infrastructure connection strings for health checks
	PgBouncerLedgerURL   string // PgBouncer admin for ledger (default: localhost:6433)
	PgBouncerTransferURL string // PgBouncer admin for transfer (default: localhost:6434)
	PgBouncerTreasuryURL string // PgBouncer admin for treasury (default: localhost:6435)
	RedisURL             string // Redis URL (default: localhost:6379)
	NatsMonitorURL       string // NATS monitoring URL (default: http://localhost:8222)
}

// DefaultSoakConfig returns a soak config with sensible defaults.
func DefaultSoakConfig() SoakConfig {
	return SoakConfig{
		CheckInterval:      60 * time.Second,
		BaselineWindow:     5 * time.Minute,
		MaxMemoryGrowth:    50 * 1024 * 1024, // 50MB
		MaxGoroutineGrowth: 1000,
		MaxP99Degradation:  2.0,
		MaxErrorRate:       0.01,
		StuckTimeout:       5 * time.Minute,
		PprofURL:           "http://localhost:6060",
		ProfileDir:         "tests/loadtest/profiles",
		TimeSeriesFile:     "tests/loadtest/soak-metrics.json",
		PgBouncerLedgerURL:   "postgres://settla:settla@localhost:6433/pgbouncer?sslmode=disable",
		PgBouncerTransferURL: "postgres://settla:settla@localhost:6434/pgbouncer?sslmode=disable",
		PgBouncerTreasuryURL: "postgres://settla:settla@localhost:6435/pgbouncer?sslmode=disable",
		RedisURL:             "localhost:6379",
		NatsMonitorURL:       "http://localhost:8222",
	}
}

// SoakSnapshot captures system state at a point in time.
type SoakSnapshot struct {
	Timestamp     time.Time `json:"timestamp"`
	ElapsedSec    float64   `json:"elapsed_sec"`

	// Process metrics
	MemoryRSS     int64 `json:"memory_rss_bytes"`
	HeapAlloc     int64 `json:"heap_alloc_bytes"`
	HeapSys       int64 `json:"heap_sys_bytes"`
	GoroutineCount int  `json:"goroutine_count"`
	NumGC         uint32 `json:"num_gc"`

	// Throughput
	TransfersCreated   int64 `json:"transfers_created"`
	TransfersCompleted int64 `json:"transfers_completed"`
	TransfersFailed    int64 `json:"transfers_failed"`
	CurrentTPS         float64 `json:"current_tps"`

	// Latency (microseconds)
	P50Latency int64 `json:"p50_latency_us"`
	P95Latency int64 `json:"p95_latency_us"`
	P99Latency int64 `json:"p99_latency_us"`

	// Error rate
	ErrorRate float64 `json:"error_rate"`

	// PgBouncer
	PgBouncerActiveConns  int `json:"pgbouncer_active_conns"`
	PgBouncerWaitingClients int `json:"pgbouncer_waiting_clients"`
	PgBouncerPoolUtil     float64 `json:"pgbouncer_pool_utilization"`

	// Redis
	RedisConnectedClients int64 `json:"redis_connected_clients"`
	RedisUsedMemory       int64 `json:"redis_used_memory_bytes"`

	// NATS
	NatsStreamDepth    int64 `json:"nats_stream_depth"`
	NatsConsumerPending int64 `json:"nats_consumer_pending"`

	// Inflight
	Inflight int64 `json:"inflight"`
}

// SoakRunner orchestrates a soak test with continuous monitoring.
type SoakRunner struct {
	config     SoakConfig
	runner     *LoadTestRunner
	logger     *slog.Logger
	httpClient *http.Client

	// Monitoring state
	startTime     time.Time
	snapshots     []SoakSnapshot
	snapshotMu    sync.Mutex
	baselineP99   int64 // microseconds
	baselineRSS   int64
	baselineGoroutines int

	// Fail condition tracking
	waitingClientsStart time.Time
	waitingClientsHigh  bool
	errorRateStart      time.Time
	errorRateHigh       bool

	// Transfer tracking for stuck detection
	transferTimestamps sync.Map // transferID -> time.Time (creation time)

	// Counters for TPS calculation
	lastCheckCreated int64
	lastCheckTime    time.Time

	// Stop signal
	failReason string
	failed     atomic.Bool
}

// NewSoakRunner creates a new soak test runner.
func NewSoakRunner(config SoakConfig, logger *slog.Logger) *SoakRunner {
	runner := NewLoadTestRunner(config.LoadTestConfig, logger)

	return &SoakRunner{
		config:     config,
		runner:     runner,
		logger:     logger,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		snapshots:  make([]SoakSnapshot, 0, 1000),
	}
}

// Run executes the soak test with continuous monitoring.
func (s *SoakRunner) Run(ctx context.Context) (*SoakReport, error) {
	s.startTime = time.Now()
	s.lastCheckTime = s.startTime
	s.logger.Info("starting soak test",
		"target_tps", s.config.TargetTPS,
		"duration", s.config.Duration,
		"check_interval", s.config.CheckInterval,
	)

	// Ensure profile directory exists
	if err := os.MkdirAll(s.config.ProfileDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating profile dir: %w", err)
	}

	// Create context that we can cancel on fail condition
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start health monitor
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		s.monitorHealth(ctx)
	}()

	// Run the load test
	err := s.runner.Run(ctx)

	// Check if we failed due to a health check
	if s.failed.Load() {
		cancel() // Stop the load test
		<-monitorDone
		return s.generateReport(), fmt.Errorf("soak test failed: %s", s.failReason)
	}

	cancel()
	<-monitorDone

	if err != nil {
		return s.generateReport(), fmt.Errorf("load test failed: %w", err)
	}

	report := s.generateReport()

	// Write time-series data
	if err := s.writeTimeSeries(); err != nil {
		s.logger.Error("failed to write time-series data", "error", err)
	}

	return report, nil
}

// monitorHealth runs periodic health checks and fail condition evaluation.
func (s *SoakRunner) monitorHealth(ctx context.Context) {
	ticker := time.NewTicker(s.config.CheckInterval)
	defer ticker.Stop()

	checkNum := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkNum++
			snapshot := s.collectSnapshot()
			s.snapshotMu.Lock()
			s.snapshots = append(s.snapshots, snapshot)
			s.snapshotMu.Unlock()

			s.logSnapshot(checkNum, snapshot)

			// Set baseline after the baseline window
			elapsed := time.Since(s.startTime)
			if elapsed >= s.config.BaselineWindow && s.baselineP99 == 0 {
				s.setBaseline(snapshot)
			}

			// Check fail conditions (only after baseline is set)
			if s.baselineP99 > 0 {
				if reason := s.checkFailConditions(snapshot); reason != "" {
					s.failReason = reason
					s.failed.Store(true)
					s.logger.Error("SOAK TEST FAIL CONDITION TRIGGERED", "reason", reason)
					return
				}
			}

			// Capture profiles at baseline and near end
			if elapsed >= 5*time.Minute && elapsed < 6*time.Minute {
				go s.captureProfiles("baseline")
			}
			if elapsed >= s.config.Duration-5*time.Minute && elapsed < s.config.Duration-4*time.Minute {
				go s.captureProfiles("final")
			}
		}
	}
}

// collectSnapshot captures current system state.
func (s *SoakRunner) collectSnapshot() SoakSnapshot {
	now := time.Now()
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Calculate current TPS
	created := s.runner.metrics.TransfersCreated.Load()
	elapsed := now.Sub(s.lastCheckTime).Seconds()
	var currentTPS float64
	if elapsed > 0 {
		currentTPS = float64(created-s.lastCheckCreated) / elapsed
	}
	s.lastCheckCreated = created
	s.lastCheckTime = now

	// Get latency stats
	var p50, p95, p99 int64
	if s.runner.metrics.endToEndLatency.Count() > 0 {
		p50us, p95us, p99us := s.runner.metrics.endToEndLatency.Stats()
		p50 = p50us.Microseconds()
		p95 = p95us.Microseconds()
		p99 = p99us.Microseconds()
	}

	// Calculate error rate
	total := s.runner.metrics.TransfersCreated.Load()
	failed := s.runner.metrics.TransfersFailed.Load()
	var errorRate float64
	if total > 0 {
		errorRate = float64(failed) / float64(total)
	}

	snapshot := SoakSnapshot{
		Timestamp:          now,
		ElapsedSec:         now.Sub(s.startTime).Seconds(),
		MemoryRSS:          int64(memStats.Sys),
		HeapAlloc:          int64(memStats.HeapAlloc),
		HeapSys:            int64(memStats.HeapSys),
		GoroutineCount:     runtime.NumGoroutine(),
		NumGC:              memStats.NumGC,
		TransfersCreated:   created,
		TransfersCompleted: s.runner.metrics.TransfersCompleted.Load(),
		TransfersFailed:    failed,
		CurrentTPS:         currentTPS,
		P50Latency:         p50,
		P95Latency:         p95,
		P99Latency:         p99,
		ErrorRate:          errorRate,
		Inflight:           s.runner.inflight.Load(),
	}

	// Collect infrastructure metrics (best effort)
	s.collectPgBouncerStats(&snapshot)
	s.collectRedisStats(&snapshot)
	s.collectNatsStats(&snapshot)

	return snapshot
}

// collectPgBouncerStats queries PgBouncer admin console for connection stats.
func (s *SoakRunner) collectPgBouncerStats(snapshot *SoakSnapshot) {
	// Query PgBouncer via SQL admin console (SHOW POOLS)
	urls := []string{
		s.config.PgBouncerLedgerURL,
		s.config.PgBouncerTransferURL,
		s.config.PgBouncerTreasuryURL,
	}

	var totalActive, totalWaiting int
	for _, connStr := range urls {
		active, waiting := queryPgBouncerPool(connStr)
		totalActive += active
		totalWaiting += waiting
	}

	snapshot.PgBouncerActiveConns = totalActive
	snapshot.PgBouncerWaitingClients = totalWaiting
	if totalActive > 0 {
		// Pool utilization = active / (active + idle), approximate with active/150 (3 pools × 50 default)
		snapshot.PgBouncerPoolUtil = float64(totalActive) / 150.0
	}
}

// queryPgBouncerPool queries a single PgBouncer instance for pool stats.
func queryPgBouncerPool(connStr string) (active, waiting int) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return 0, 0
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(5 * time.Second)

	rows, err := db.Query("SHOW POOLS")
	if err != nil {
		return 0, 0
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, 0
	}

	for rows.Next() {
		// PgBouncer SHOW POOLS returns variable columns; scan into interface slice
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		// Find cl_active and cl_waiting columns by name
		for i, col := range cols {
			switch strings.ToLower(col) {
			case "cl_active":
				if v, ok := toInt(values[i]); ok {
					active += v
				}
			case "cl_waiting":
				if v, ok := toInt(values[i]); ok {
					waiting += v
				}
			}
		}
	}

	return active, waiting
}

// toInt converts an interface{} to int (handles []byte, int64, string).
func toInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int64:
		return int(val), true
	case []byte:
		var n int
		_, err := fmt.Sscanf(string(val), "%d", &n)
		return n, err == nil
	case string:
		var n int
		_, err := fmt.Sscanf(val, "%d", &n)
		return n, err == nil
	default:
		return 0, false
	}
}

// collectRedisStats queries Redis INFO for connection and memory stats.
func (s *SoakRunner) collectRedisStats(snapshot *SoakSnapshot) {
	resp, err := s.httpClient.Get(fmt.Sprintf("http://%s", s.config.RedisURL))
	if err != nil {
		// Redis doesn't have HTTP; use INFO via redis-cli or skip
		// For the soak test, we'll parse from the metrics endpoint if available
		return
	}
	defer resp.Body.Close()
	// If Redis has a metrics endpoint, parse it here
}

// collectNatsStats queries NATS monitoring endpoint for stream stats.
func (s *SoakRunner) collectNatsStats(snapshot *SoakSnapshot) {
	// Query NATS JetStream info
	resp, err := s.httpClient.Get(s.config.NatsMonitorURL + "/jsz?streams=true")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var jsz struct {
		Streams []struct {
			State struct {
				Messages  int64 `json:"messages"`
				Consumers int   `json:"consumer_count"`
			} `json:"state"`
			Name string `json:"name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(body, &jsz); err != nil {
		return
	}

	for _, stream := range jsz.Streams {
		snapshot.NatsStreamDepth += stream.State.Messages
	}

	// Query consumer info for pending counts
	resp2, err := s.httpClient.Get(s.config.NatsMonitorURL + "/jsz?consumers=true")
	if err != nil {
		return
	}
	defer resp2.Body.Close()
	// Additional consumer pending parsing would go here
}

// setBaseline records the initial metrics to compare against.
func (s *SoakRunner) setBaseline(snapshot SoakSnapshot) {
	s.baselineP99 = snapshot.P99Latency
	s.baselineRSS = snapshot.MemoryRSS
	s.baselineGoroutines = snapshot.GoroutineCount

	s.logger.Info("baseline established",
		"p99_latency_us", s.baselineP99,
		"memory_rss_mb", s.baselineRSS/(1024*1024),
		"goroutines", s.baselineGoroutines,
	)
}

// checkFailConditions evaluates all fail conditions and returns a reason if any trigger.
func (s *SoakRunner) checkFailConditions(snapshot SoakSnapshot) string {
	// 1. Memory growth
	memGrowth := snapshot.MemoryRSS - s.baselineRSS
	if memGrowth > s.config.MaxMemoryGrowth {
		return fmt.Sprintf("memory growth %dMB exceeds limit %dMB (RSS: %dMB → %dMB)",
			memGrowth/(1024*1024), s.config.MaxMemoryGrowth/(1024*1024),
			s.baselineRSS/(1024*1024), snapshot.MemoryRSS/(1024*1024))
	}

	// 2. Goroutine growth
	goroutineGrowth := int64(snapshot.GoroutineCount) - int64(s.baselineGoroutines)
	if goroutineGrowth > s.config.MaxGoroutineGrowth {
		return fmt.Sprintf("goroutine growth %d exceeds limit %d (%d → %d)",
			goroutineGrowth, s.config.MaxGoroutineGrowth,
			s.baselineGoroutines, snapshot.GoroutineCount)
	}

	// 3. PgBouncer waiting clients sustained >60s
	if snapshot.PgBouncerWaitingClients > 10 {
		if !s.waitingClientsHigh {
			s.waitingClientsHigh = true
			s.waitingClientsStart = time.Now()
		} else if time.Since(s.waitingClientsStart) > 60*time.Second {
			return fmt.Sprintf("PgBouncer waiting clients >10 for >60s (current: %d)",
				snapshot.PgBouncerWaitingClients)
		}
	} else {
		s.waitingClientsHigh = false
	}

	// 4. p99 latency degradation
	if s.baselineP99 > 0 && snapshot.P99Latency > 0 {
		degradation := float64(snapshot.P99Latency) / float64(s.baselineP99)
		if degradation > s.config.MaxP99Degradation {
			return fmt.Sprintf("p99 latency degradation %.1fx exceeds limit %.1fx (%dus → %dus)",
				degradation, s.config.MaxP99Degradation,
				s.baselineP99, snapshot.P99Latency)
		}
	}

	// 5. Error rate sustained >60s
	if snapshot.ErrorRate > s.config.MaxErrorRate {
		if !s.errorRateHigh {
			s.errorRateHigh = true
			s.errorRateStart = time.Now()
		} else if time.Since(s.errorRateStart) > 60*time.Second {
			return fmt.Sprintf("error rate %.2f%% exceeds limit %.2f%% for >60s",
				snapshot.ErrorRate*100, s.config.MaxErrorRate*100)
		}
	} else {
		s.errorRateHigh = false
	}

	return "" // All checks passed
}

// captureProfiles captures CPU and heap profiles from settla-server.
func (s *SoakRunner) captureProfiles(label string) {
	s.logger.Info("capturing profiles", "label", label)

	// CPU profile (30 seconds)
	go func() {
		cpuFile := filepath.Join(s.config.ProfileDir, fmt.Sprintf("cpu-%s.prof", label))
		if err := downloadProfile(s.httpClient, s.config.PprofURL+"/debug/pprof/profile?seconds=30", cpuFile); err != nil {
			s.logger.Error("failed to capture CPU profile", "label", label, "error", err)
		} else {
			s.logger.Info("CPU profile captured", "label", label, "file", cpuFile)
		}
	}()

	// Heap profile
	heapFile := filepath.Join(s.config.ProfileDir, fmt.Sprintf("heap-%s.prof", label))
	if err := downloadProfile(s.httpClient, s.config.PprofURL+"/debug/pprof/heap", heapFile); err != nil {
		s.logger.Error("failed to capture heap profile", "label", label, "error", err)
	} else {
		s.logger.Info("heap profile captured", "label", label, "file", heapFile)
	}

	// Goroutine profile
	goroutineFile := filepath.Join(s.config.ProfileDir, fmt.Sprintf("goroutine-%s.prof", label))
	if err := downloadProfile(s.httpClient, s.config.PprofURL+"/debug/pprof/goroutine", goroutineFile); err != nil {
		s.logger.Error("failed to capture goroutine profile", "label", label, "error", err)
	} else {
		s.logger.Info("goroutine profile captured", "label", label, "file", goroutineFile)
	}
}

// downloadProfile downloads a pprof profile to a file.
func downloadProfile(client *http.Client, url, destFile string) error {
	// Use a longer timeout for CPU profiles (30s capture + overhead)
	profClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := profClient.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	f, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", destFile, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", destFile, err)
	}

	return nil
}

// logSnapshot logs the current snapshot in a readable format.
func (s *SoakRunner) logSnapshot(checkNum int, snap SoakSnapshot) {
	s.logger.Info(fmt.Sprintf("health check #%d", checkNum),
		"elapsed", formatDuration(time.Duration(snap.ElapsedSec)*time.Second),
		"tps", fmt.Sprintf("%.1f", snap.CurrentTPS),
		"created", snap.TransfersCreated,
		"completed", snap.TransfersCompleted,
		"failed", snap.TransfersFailed,
		"inflight", snap.Inflight,
		"p50_ms", fmt.Sprintf("%.1f", float64(snap.P50Latency)/1000),
		"p99_ms", fmt.Sprintf("%.1f", float64(snap.P99Latency)/1000),
		"rss_mb", snap.MemoryRSS/(1024*1024),
		"heap_mb", snap.HeapAlloc/(1024*1024),
		"goroutines", snap.GoroutineCount,
		"pgb_active", snap.PgBouncerActiveConns,
		"pgb_waiting", snap.PgBouncerWaitingClients,
		"nats_depth", snap.NatsStreamDepth,
		"error_rate", fmt.Sprintf("%.4f", snap.ErrorRate),
	)
}

// writeTimeSeries writes all snapshots to a JSON file for analysis.
func (s *SoakRunner) writeTimeSeries() error {
	s.snapshotMu.Lock()
	data := make([]SoakSnapshot, len(s.snapshots))
	copy(data, s.snapshots)
	s.snapshotMu.Unlock()

	f, err := os.Create(s.config.TimeSeriesFile)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// formatDuration formats a duration as "XhYmZs".
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	}
	return fmt.Sprintf("%dm%02ds", m, sec)
}
