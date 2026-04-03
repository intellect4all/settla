package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/intellect4all/settla/domain"
)

// ReconcilerMetrics holds Prometheus metrics for the reconciler.
type ReconcilerMetrics struct {
	ReconciliationRuns     prometheus.Counter
	ReconciliationFailed   prometheus.Counter
	DiscrepanciesFound     *prometheus.CounterVec // labels: check_name
	ReconciliationDuration prometheus.Histogram
}

// NewReconcilerMetrics creates and registers ReconcilerMetrics with the given registerer.
func NewReconcilerMetrics(reg prometheus.Registerer) *ReconcilerMetrics {
	m := &ReconcilerMetrics{
		ReconciliationRuns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "settla_reconciliation_runs_total",
			Help: "Total reconciliation runs",
		}),
		ReconciliationFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "settla_reconciliation_failed_total",
			Help: "Total reconciliation failures",
		}),
		DiscrepanciesFound: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_reconciliation_discrepancies_total",
			Help: "Total discrepancies found per check",
		}, []string{"check_name"}),
		ReconciliationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_reconciliation_duration_seconds",
			Help:    "Duration of reconciliation runs",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.ReconciliationRuns, m.ReconciliationFailed, m.DiscrepanciesFound, m.ReconciliationDuration)
	return m
}

// CheckResult is an alias for domain.ReconciliationCheckResult.
type CheckResult = domain.ReconciliationCheckResult

// Report is an alias for domain.ReconciliationReport.
type Report = domain.ReconciliationReport

// Check is a single reconciliation check that verifies one aspect of system consistency.
type Check interface {
	// Name returns a human-readable identifier for this check.
	Name() string
	// Run executes the check and returns its result.
	Run(ctx context.Context) (*CheckResult, error)
}

// ReportStore persists and retrieves reconciliation reports.
type ReportStore interface {
	CreateReconciliationReport(ctx context.Context, report *Report) error
	GetLatestReport(ctx context.Context) (*Report, error)
}

// FeatureFlagChecker allows the reconciler to gate checks behind feature flags.
type FeatureFlagChecker interface {
	IsEnabled(name string) bool
}

// contextKey is an unexported type for context keys in this package.
type contextKey string

// sinceKey is the context key for the incremental reconciliation timestamp.
const sinceKey contextKey = "reconciliation_since"

// SinceFromContext extracts the "since" timestamp from the context. If not set,
// returns the zero value (checks should fall back to full scan behavior).
func SinceFromContext(ctx context.Context) time.Time {
	if v, ok := ctx.Value(sinceKey).(time.Time); ok {
		return v
	}
	return time.Time{}
}

// Reconciler orchestrates reconciliation checks and stores reports.
type Reconciler struct {
	checks      []Check
	store       ReportStore
	logger      *slog.Logger
	metrics     *ReconcilerMetrics
	flagChecker FeatureFlagChecker

	// lastRunAt tracks the last successful run time per check for incremental reconciliation.
	lastRunAt sync.Map // map[string]time.Time
}

// NewReconciler creates a Reconciler with the given checks, store, and logger.
func NewReconciler(checks []Check, store ReportStore, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		checks: checks,
		store:  store,
		logger: logger,
	}
}

// WithMetrics attaches a ReconcilerMetrics instance to the Reconciler.
// If not called, metrics are silently skipped (nil-safe).
func (r *Reconciler) WithMetrics(m *ReconcilerMetrics) *Reconciler {
	r.metrics = m
	return r
}

// WithFeatureFlags attaches a FeatureFlagChecker to the Reconciler.
// When set, checks whose names start with "enhanced_" are gated behind
// the "enhanced_reconciliation" feature flag.
func (r *Reconciler) WithFeatureFlags(checker FeatureFlagChecker) *Reconciler {
	r.flagChecker = checker
	return r
}

// Run executes all registered checks in parallel (max 4 concurrent), builds a
// report, stores it, and returns it. OverallPass is true only if every check
// has status "pass".
func (r *Reconciler) Run(ctx context.Context) (*Report, error) {
	start := time.Now()

	if r.metrics != nil {
		r.metrics.ReconciliationRuns.Inc()
	}

	report := &Report{
		ID:          uuid.New(),
		RunAt:       time.Now().UTC(),
		OverallPass: true,
	}

	// Filter checks based on feature flags.
	var activeChecks []Check
	for _, check := range r.checks {
		if r.flagChecker != nil && strings.HasPrefix(check.Name(), "enhanced_") {
			if !r.flagChecker.IsEnabled("enhanced_reconciliation") {
				r.logger.Debug("settla-reconciliation: skipping gated check",
					slog.String("check", check.Name()),
				)
				continue
			}
		}
		activeChecks = append(activeChecks, check)
	}

	// Run checks in parallel with a concurrency limit of 4.
	type indexedResult struct {
		idx    int
		result CheckResult
	}
	resultsCh := make(chan indexedResult, len(activeChecks))
	sem := make(chan struct{}, 4) // max 4 concurrent checks
	var wg sync.WaitGroup

	for i, check := range activeChecks {
		wg.Add(1)
		go func(i int, check Check) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release

			r.logger.Info("settla-reconciliation: running check",
				slog.String("check", check.Name()),
			)

			// Inject "since" timestamp for incremental reconciliation.
			checkCtx := ctx
			if lastRun, ok := r.lastRunAt.Load(check.Name()); ok {
				checkCtx = context.WithValue(ctx, sinceKey, lastRun.(time.Time))
			}

			result, err := check.Run(checkCtx)

			if err != nil {
				r.logger.Error("settla-reconciliation: check error",
					slog.String("check", check.Name()),
					slog.String("error", err.Error()),
				)
				result = &CheckResult{
					Name:      check.Name(),
					Status:    "fail",
					Details:   fmt.Sprintf("check error: %v", err),
					CheckedAt: time.Now().UTC(),
				}
			}

			r.logger.Info("settla-reconciliation: check completed",
				slog.String("check", result.Name),
				slog.String("status", result.Status),
				slog.Int("mismatches", result.Mismatches),
			)

			// Update lastRunAt on successful check for next incremental run.
			if result.Status == "pass" {
				r.lastRunAt.Store(check.Name(), time.Now().UTC())
			}

			resultsCh <- indexedResult{idx: i, result: *result}
		}(i, check)
	}

	wg.Wait()
	close(resultsCh)

	// Collect results and preserve original check order.
	ordered := make([]CheckResult, len(activeChecks))
	for ir := range resultsCh {
		ordered[ir.idx] = ir.result
	}

	for _, result := range ordered {
		if result.Status != "pass" {
			report.OverallPass = false
			if r.metrics != nil && result.Mismatches > 0 {
				r.metrics.DiscrepanciesFound.WithLabelValues(result.Name).Add(float64(result.Mismatches))
			}
		}
		report.Results = append(report.Results, result)
	}

	if err := r.store.CreateReconciliationReport(ctx, report); err != nil {
		if r.metrics != nil {
			r.metrics.ReconciliationFailed.Inc()
		}
		return nil, fmt.Errorf("settla-reconciliation: storing report: %w", err)
	}

	if r.metrics != nil {
		r.metrics.ReconciliationDuration.Observe(time.Since(start).Seconds())
		if !report.OverallPass {
			r.metrics.ReconciliationFailed.Inc()
		}
	}

	r.logger.Info("settla-reconciliation: run completed",
		slog.String("report_id", report.ID.String()),
		slog.Bool("overall_pass", report.OverallPass),
		slog.Int("total_checks", len(report.Results)),
	)

	return report, nil
}
