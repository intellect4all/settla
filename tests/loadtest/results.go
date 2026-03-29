package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ScenarioResult is the structured JSON output for a single scenario run.
type ScenarioResult struct {
	// Metadata
	Scenario    string    `json:"scenario"`
	Description string    `json:"description"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Duration    string    `json:"duration"`

	// Configuration
	Config ScenarioConfigSummary `json:"config"`

	// Throughput
	Throughput ThroughputResult `json:"throughput"`

	// Latency
	Latency LatencyResult `json:"latency"`

	// Errors
	Errors ErrorResult `json:"errors"`

	// Verification
	Verification VerificationResult `json:"verification"`

	// Resource usage (populated for soak/scale tests)
	Resources *ResourceResult `json:"resources,omitempty"`

	// Zipf distribution stats (populated for scale tests)
	ZipfStats *ZipfStats `json:"zipf_stats,omitempty"`

	// Settlement (Scenario J only)
	Settlement *SettlementResult `json:"settlement,omitempty"`

	// Thresholds and pass/fail
	Thresholds ThresholdResults `json:"thresholds"`
	Passed     bool             `json:"passed"`
	FailReason string           `json:"fail_reason,omitempty"`
}

// ScenarioConfigSummary captures the test configuration.
type ScenarioConfigSummary struct {
	TargetTPS  int    `json:"target_tps"`
	Duration   string `json:"duration"`
	TenantCount int   `json:"tenant_count"`
	UseZipf    bool   `json:"use_zipf"`
	SpikeMode  bool   `json:"spike_mode"`
	HotSpotMode bool  `json:"hotspot_mode"`
}

// ThroughputResult captures throughput metrics.
type ThroughputResult struct {
	TransfersCreated   int64   `json:"transfers_created"`
	TransfersCompleted int64   `json:"transfers_completed"`
	TransfersFailed    int64   `json:"transfers_failed"`
	ActualTPS          float64 `json:"actual_tps"`
	PeakTPS            int64   `json:"peak_tps"`
	SuccessRate        float64 `json:"success_rate_pct"`
}

// LatencyResult captures latency percentiles (in milliseconds).
type LatencyResult struct {
	QuoteP50  float64 `json:"quote_p50_ms"`
	QuoteP95  float64 `json:"quote_p95_ms"`
	QuoteP99  float64 `json:"quote_p99_ms"`
	CreateP50 float64 `json:"create_p50_ms"`
	CreateP95 float64 `json:"create_p95_ms"`
	CreateP99 float64 `json:"create_p99_ms"`
	E2EP50    float64 `json:"e2e_p50_ms"`
	E2EP95    float64 `json:"e2e_p95_ms"`
	E2EP99    float64 `json:"e2e_p99_ms"`
}

// ErrorResult captures error metrics.
type ErrorResult struct {
	TotalErrors    int64              `json:"total_errors"`
	ErrorRate      float64            `json:"error_rate_pct"`
	ErrorsByType   map[string]int64   `json:"errors_by_type"`
}

// VerificationResult captures post-test verification.
type VerificationResult struct {
	OutboxDrained     bool `json:"outbox_drained"`
	LedgerBalanced    bool `json:"ledger_balanced"`
	TreasuryReconciled bool `json:"treasury_reconciled"`
	StuckTransfers    int  `json:"stuck_transfers"`
}

// ResourceResult captures resource usage (soak/scale tests).
type ResourceResult struct {
	StartRSSMB       float64 `json:"start_rss_mb"`
	EndRSSMB         float64 `json:"end_rss_mb"`
	RSSGrowthPct     float64 `json:"rss_growth_pct"`
	StartGoroutines  int     `json:"start_goroutines"`
	EndGoroutines    int     `json:"end_goroutines"`
	PeakGoroutines   int     `json:"peak_goroutines"`
	PgBMaxWaiting    int     `json:"pgbouncer_max_waiting"`
	NatsMaxDepth     int64   `json:"nats_max_depth"`
	AuthCacheHitRate float64 `json:"auth_cache_hit_rate_pct"`
}

// SettlementResult captures settlement batch metrics (Scenario J).
type SettlementResult struct {
	TotalTenantsSettled  int           `json:"total_tenants_settled"`
	SettlementDuration   string        `json:"settlement_duration"`
	PerTenantP50         string        `json:"per_tenant_p50"`
	PerTenantP99         string        `json:"per_tenant_p99"`
	ReconciliationPassed bool          `json:"reconciliation_passed"`
}

// ThresholdResults captures pass/fail for each threshold.
type ThresholdResults struct {
	P50LatencyPassed  bool    `json:"p50_latency_passed"`
	P50LatencyActual  float64 `json:"p50_latency_actual_ms"`
	P50LatencyLimit   float64 `json:"p50_latency_limit_ms"`
	P99LatencyPassed  bool    `json:"p99_latency_passed"`
	P99LatencyActual  float64 `json:"p99_latency_actual_ms"`
	P99LatencyLimit   float64 `json:"p99_latency_limit_ms"`
	ErrorRatePassed   bool    `json:"error_rate_passed"`
	ErrorRateActual   float64 `json:"error_rate_actual_pct"`
	ErrorRateLimit    float64 `json:"error_rate_limit_pct"`
	StuckPassed       bool    `json:"stuck_transfers_passed"`
	StuckActual       int     `json:"stuck_transfers_actual"`
	StuckLimit        int     `json:"stuck_transfers_limit"`
	MemoryPassed      *bool   `json:"memory_passed,omitempty"`
	MemoryGrowthPct   *float64 `json:"memory_growth_pct,omitempty"`
	MemoryGrowthLimit *float64 `json:"memory_growth_limit_pct,omitempty"`
}

// ResultCollector builds a ScenarioResult from test execution data.
type ResultCollector struct {
	scenario   Scenario
	metrics    *LoadTestMetrics
	startTime  time.Time
}

// NewResultCollector creates a new result collector.
func NewResultCollector(scenario Scenario, metrics *LoadTestMetrics) *ResultCollector {
	return &ResultCollector{
		scenario:  scenario,
		metrics:   metrics,
		startTime: time.Now(),
	}
}

// Collect builds the final ScenarioResult.
func (rc *ResultCollector) Collect(verReport *VerificationReport) *ScenarioResult {
	now := time.Now()
	duration := now.Sub(rc.startTime)

	created := rc.metrics.TransfersCreated.Load()
	completed := rc.metrics.TransfersCompleted.Load()
	failed := rc.metrics.TransfersFailed.Load()

	successRate := 0.0
	if created > 0 {
		successRate = float64(completed) / float64(created) * 100
	}

	actualTPS := 0.0
	if duration.Seconds() > 0 {
		actualTPS = float64(created) / duration.Seconds()
	}

	// Latency
	qp50, qp95, qp99 := rc.metrics.quoteLatency.Stats()
	cp50, cp95, cp99 := rc.metrics.createLatency.Stats()
	ep50, ep95, ep99 := rc.metrics.endToEndLatency.Stats()

	// Errors
	errorsByType := make(map[string]int64)
	totalErrors := int64(0)
	rc.metrics.errorsMu.RLock()
	for code, cnt := range rc.metrics.errors {
		v := cnt.Load()
		errorsByType[code] = v
		totalErrors += v
	}
	rc.metrics.errorsMu.RUnlock()

	errorRate := 0.0
	if created > 0 {
		errorRate = float64(totalErrors) / float64(created) * 100
	}

	// Build result
	result := &ScenarioResult{
		Scenario:    rc.scenario.Name,
		Description: rc.scenario.Description,
		StartTime:   rc.startTime,
		EndTime:     now,
		Duration:    duration.Round(time.Second).String(),
		Config: ScenarioConfigSummary{
			TargetTPS:   rc.scenario.Config.TargetTPS,
			Duration:    rc.scenario.Config.Duration.String(),
			TenantCount: len(rc.scenario.Config.Tenants),
			UseZipf:     rc.scenario.Config.UseZipf,
			SpikeMode:   rc.scenario.Config.SpikeMode,
			HotSpotMode: rc.scenario.Config.HotSpotMode,
		},
		Throughput: ThroughputResult{
			TransfersCreated:   created,
			TransfersCompleted: completed,
			TransfersFailed:    failed,
			ActualTPS:          actualTPS,
			PeakTPS:            rc.metrics.PeakTPS.Load(),
			SuccessRate:        successRate,
		},
		Latency: LatencyResult{
			QuoteP50:  float64(qp50.Microseconds()) / 1000,
			QuoteP95:  float64(qp95.Microseconds()) / 1000,
			QuoteP99:  float64(qp99.Microseconds()) / 1000,
			CreateP50: float64(cp50.Microseconds()) / 1000,
			CreateP95: float64(cp95.Microseconds()) / 1000,
			CreateP99: float64(cp99.Microseconds()) / 1000,
			E2EP50:    float64(ep50.Microseconds()) / 1000,
			E2EP95:    float64(ep95.Microseconds()) / 1000,
			E2EP99:    float64(ep99.Microseconds()) / 1000,
		},
		Errors: ErrorResult{
			TotalErrors:  totalErrors,
			ErrorRate:    errorRate,
			ErrorsByType: errorsByType,
		},
	}

	// Verification
	if verReport != nil {
		result.Verification = VerificationResult{
			OutboxDrained:      verReport.OutboxPass,
			LedgerBalanced:     verReport.LedgerPass,
			TreasuryReconciled: verReport.TreasuryPass,
			StuckTransfers:     verReport.StuckTransfers,
		}
	}

	// Evaluate thresholds
	th := rc.scenario.Thresholds
	result.Thresholds = ThresholdResults{
		P50LatencyPassed: ep50 <= th.MaxP50Latency || th.MaxP50Latency == 0,
		P50LatencyActual: float64(ep50.Microseconds()) / 1000,
		P50LatencyLimit:  float64(th.MaxP50Latency.Microseconds()) / 1000,
		P99LatencyPassed: ep99 <= th.MaxP99Latency || th.MaxP99Latency == 0,
		P99LatencyActual: float64(ep99.Microseconds()) / 1000,
		P99LatencyLimit:  float64(th.MaxP99Latency.Microseconds()) / 1000,
		ErrorRatePassed:  errorRate/100 <= th.MaxErrorRate || th.MaxErrorRate == 0,
		ErrorRateActual:  errorRate,
		ErrorRateLimit:   th.MaxErrorRate * 100,
		StuckPassed:      verReport == nil || verReport.StuckTransfers <= th.MaxStuckTransfers,
		StuckActual:      0,
		StuckLimit:       th.MaxStuckTransfers,
	}
	if verReport != nil {
		result.Thresholds.StuckActual = verReport.StuckTransfers
	}

	// Overall pass
	result.Passed = result.Thresholds.P50LatencyPassed &&
		result.Thresholds.P99LatencyPassed &&
		result.Thresholds.ErrorRatePassed &&
		result.Thresholds.StuckPassed

	if !result.Passed {
		reasons := []string{}
		if !result.Thresholds.P50LatencyPassed {
			reasons = append(reasons, fmt.Sprintf("p50 latency %.1fms > %.1fms", result.Thresholds.P50LatencyActual, result.Thresholds.P50LatencyLimit))
		}
		if !result.Thresholds.P99LatencyPassed {
			reasons = append(reasons, fmt.Sprintf("p99 latency %.1fms > %.1fms", result.Thresholds.P99LatencyActual, result.Thresholds.P99LatencyLimit))
		}
		if !result.Thresholds.ErrorRatePassed {
			reasons = append(reasons, fmt.Sprintf("error rate %.2f%% > %.2f%%", result.Thresholds.ErrorRateActual, result.Thresholds.ErrorRateLimit))
		}
		if !result.Thresholds.StuckPassed {
			reasons = append(reasons, fmt.Sprintf("stuck transfers %d > %d", result.Thresholds.StuckActual, result.Thresholds.StuckLimit))
		}
		result.FailReason = fmt.Sprintf("%v", reasons)
	}

	return result
}

// WriteJSON writes the result as formatted JSON to a file.
func (r *ScenarioResult) WriteJSON(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	ts := r.StartTime.Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.json", r.Scenario, ts)
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write result: %w", err)
	}

	return path, nil
}

// AggregateReport combines results from multiple scenario runs.
type AggregateReport struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Results     []ScenarioResult  `json:"results"`
	Summary     AggregateSummary  `json:"summary"`
}

// AggregateSummary provides a high-level summary of all scenarios.
type AggregateSummary struct {
	TotalScenarios int  `json:"total_scenarios"`
	Passed         int  `json:"passed"`
	Failed         int  `json:"failed"`
	AllPassed      bool `json:"all_passed"`
}

// WriteAggregateReport combines all JSON result files in a directory into one report.
func WriteAggregateReport(resultsDir, outputPath string) error {
	files, err := filepath.Glob(filepath.Join(resultsDir, "*.json"))
	if err != nil {
		return fmt.Errorf("glob results: %w", err)
	}

	report := AggregateReport{
		GeneratedAt: time.Now(),
		Results:     make([]ScenarioResult, 0, len(files)),
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r ScenarioResult
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		report.Results = append(report.Results, r)
	}

	report.Summary.TotalScenarios = len(report.Results)
	for _, r := range report.Results {
		if r.Passed {
			report.Summary.Passed++
		} else {
			report.Summary.Failed++
		}
	}
	report.Summary.AllPassed = report.Summary.Failed == 0

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	return os.WriteFile(outputPath, data, 0o644)
}
