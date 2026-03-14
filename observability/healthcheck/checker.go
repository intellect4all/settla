package healthcheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Status represents the health status of a check or the overall system.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// CheckResult holds the outcome of a single health check.
type CheckResult struct {
	Name     string  `json:"name"`
	Status   Status  `json:"status"`
	Latency  float64 `json:"latency_ms"`
	Error    string  `json:"error,omitempty"`
	Optional bool    `json:"optional"`
}

// HealthReport is the composite result of all health checks.
type HealthReport struct {
	Status    Status        `json:"status"`
	Checks   []CheckResult `json:"checks"`
	Timestamp time.Time    `json:"timestamp"`
	Version   string       `json:"version"`
}

// Check is the interface each dependency check must implement.
type Check interface {
	Name() string
	Check(ctx context.Context) error
	Optional() bool
}

// Checker runs deep health checks against all infrastructure dependencies.
// Each check has a configurable timeout (default 100ms) and reports status
// independently. The overall health is degraded if any non-optional check fails.
type Checker struct {
	checks       []Check
	checkTimeout time.Duration
	version      string
	logger       *slog.Logger

	mu          sync.RWMutex
	cachedAt    time.Time
	cached      *HealthReport
	cacheTTL    time.Duration

	// Startup tracking
	startupMu   sync.RWMutex
	startupDone bool

	// Prometheus metrics
	checkDuration *prometheus.HistogramVec
	checkStatus   *prometheus.GaugeVec
}

// Option configures the Checker.
type Option func(*Checker)

// WithCheckTimeout sets the per-check timeout. Default is 100ms.
func WithCheckTimeout(d time.Duration) Option {
	return func(c *Checker) { c.checkTimeout = d }
}

// WithCacheTTL sets how long health results are cached. Default is 1s.
func WithCacheTTL(d time.Duration) Option {
	return func(c *Checker) { c.cacheTTL = d }
}

// WithVersion sets the version string reported in health output.
func WithVersion(v string) Option {
	return func(c *Checker) { c.version = v }
}

// Package-level metrics registered once to avoid duplicate registration panics.
var (
	checkDurationMetric = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "settla_health_check_duration_seconds",
		Help:    "Duration of individual health checks.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
	}, []string{"check"})

	checkStatusMetric = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "settla_health_check_status",
		Help: "Health check status: 1=healthy, 0=unhealthy.",
	}, []string{"check"})
)

// NewChecker creates a Checker with the given checks and options.
func NewChecker(logger *slog.Logger, checks []Check, opts ...Option) *Checker {
	c := &Checker{
		checks:        checks,
		checkTimeout:  100 * time.Millisecond,
		cacheTTL:      1 * time.Second,
		version:       "unknown",
		logger:        logger,
		checkDuration: checkDurationMetric,
		checkStatus:   checkStatusMetric,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// MarkStartupComplete signals that initialization has finished. Until called,
// the startup probe returns unhealthy.
func (c *Checker) MarkStartupComplete() {
	c.startupMu.Lock()
	defer c.startupMu.Unlock()
	c.startupDone = true
	c.logger.Info("startup complete, health checks enabled")
}

// IsStartupComplete reports whether startup has finished.
func (c *Checker) IsStartupComplete() bool {
	c.startupMu.RLock()
	defer c.startupMu.RUnlock()
	return c.startupDone
}

// RunChecks executes all registered checks in parallel and returns a composite
// HealthReport. Results are cached for cacheTTL to prevent excessive probing.
func (c *Checker) RunChecks(ctx context.Context) *HealthReport {
	c.mu.RLock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cacheTTL {
		report := c.cached
		c.mu.RUnlock()
		return report
	}
	c.mu.RUnlock()

	results := make([]CheckResult, len(c.checks))
	var wg sync.WaitGroup

	for i, chk := range c.checks {
		wg.Add(1)
		go func(idx int, ch Check) {
			defer wg.Done()
			results[idx] = c.runSingle(ctx, ch)
		}(i, chk)
	}
	wg.Wait()

	overall := StatusHealthy
	for _, r := range results {
		if r.Status == StatusUnhealthy && !r.Optional {
			overall = StatusUnhealthy
			break
		}
		if r.Status == StatusDegraded || (r.Status == StatusUnhealthy && r.Optional) {
			overall = StatusDegraded
		}
	}

	report := &HealthReport{
		Status:    overall,
		Checks:   results,
		Timestamp: time.Now().UTC(),
		Version:   c.version,
	}

	c.mu.Lock()
	c.cached = report
	c.cachedAt = time.Now()
	c.mu.Unlock()

	return report
}

func (c *Checker) runSingle(parent context.Context, ch Check) CheckResult {
	ctx, cancel := context.WithTimeout(parent, c.checkTimeout)
	defer cancel()

	start := time.Now()
	err := ch.Check(ctx)
	elapsed := time.Since(start)

	c.checkDuration.WithLabelValues(ch.Name()).Observe(elapsed.Seconds())

	result := CheckResult{
		Name:     ch.Name(),
		Latency:  float64(elapsed.Milliseconds()),
		Optional: ch.Optional(),
	}

	if err != nil {
		result.Status = StatusUnhealthy
		result.Error = err.Error()
		c.checkStatus.WithLabelValues(ch.Name()).Set(0)
		c.logger.Warn("health check failed",
			"check", ch.Name(),
			"error", err,
			"latency_ms", result.Latency,
		)
	} else {
		result.Status = StatusHealthy
		c.checkStatus.WithLabelValues(ch.Name()).Set(1)
	}

	return result
}

// ── Built-in check implementations ──────────────────────────────────────────

// PostgresCheck verifies a single Postgres connection pool with SELECT 1.
type PostgresCheck struct {
	name string
	db   *sql.DB
}

// NewPostgresCheck creates a Postgres health check for the given pool.
// name should be descriptive, e.g. "postgres_transfer".
func NewPostgresCheck(name string, db *sql.DB) *PostgresCheck {
	return &PostgresCheck{name: name, db: db}
}

func (p *PostgresCheck) Name() string           { return p.name }
func (p *PostgresCheck) Optional() bool          { return false }
func (p *PostgresCheck) Check(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

// CallbackCheck wraps a callback function as a health check.
// Used for TigerBeetle, NATS, Redis, etc. where we don't import the client directly.
type CallbackCheck struct {
	name     string
	optional bool
	fn       func(ctx context.Context) error
}

// NewCallbackCheck creates a health check backed by a callback function.
func NewCallbackCheck(name string, optional bool, fn func(ctx context.Context) error) *CallbackCheck {
	return &CallbackCheck{name: name, optional: optional, fn: fn}
}

func (cb *CallbackCheck) Name() string                    { return cb.name }
func (cb *CallbackCheck) Optional() bool                  { return cb.optional }
func (cb *CallbackCheck) Check(ctx context.Context) error { return cb.fn(ctx) }

// NewTigerBeetleCheck returns a check for TigerBeetle connectivity.
// The callback should perform a lightweight TB operation (e.g. lookup a known account).
func NewTigerBeetleCheck(fn func(ctx context.Context) error) *CallbackCheck {
	return NewCallbackCheck("tigerbeetle", false, fn)
}

// NewNATSCheck returns a check for NATS JetStream connectivity.
// The callback should verify the connection is alive and streams exist.
func NewNATSCheck(fn func(ctx context.Context) error) *CallbackCheck {
	return NewCallbackCheck("nats", false, fn)
}

// NewRedisCheck returns a check for Redis connectivity.
// Redis is optional because the system degrades gracefully to DB-only when cache is down.
func NewRedisCheck(fn func(ctx context.Context) error) *CallbackCheck {
	return NewCallbackCheck("redis", true, fn)
}

// DiskCheck monitors available disk space on a given path.
type DiskCheck struct {
	path         string
	warnPercent  float64 // warn threshold (0.0-1.0), default 0.80
	critPercent  float64 // critical threshold (0.0-1.0), default 0.95
}

// NewDiskCheck creates a disk usage health check.
// warnPercent (e.g. 0.80) triggers degraded, critPercent (e.g. 0.95) triggers unhealthy.
func NewDiskCheck(path string, warnPercent, critPercent float64) *DiskCheck {
	return &DiskCheck{path: path, warnPercent: warnPercent, critPercent: critPercent}
}

func (d *DiskCheck) Name() string  { return "disk" }
func (d *DiskCheck) Optional() bool { return false }

func (d *DiskCheck) Check(_ context.Context) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(d.path, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", d.path, err)
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total == 0 {
		return fmt.Errorf("disk total is zero for %s", d.path)
	}
	usedPercent := 1.0 - float64(free)/float64(total)
	if usedPercent >= d.critPercent {
		return fmt.Errorf("disk usage critical: %.1f%% used on %s", usedPercent*100, d.path)
	}
	if usedPercent >= d.warnPercent {
		return fmt.Errorf("disk usage warning: %.1f%% used on %s", usedPercent*100, d.path)
	}
	return nil
}

// GoroutineCheck verifies goroutine count is within bounds. This is useful as
// a liveness signal — unbounded goroutine growth suggests a leak/deadlock.
type GoroutineCheck struct {
	maxGoroutines int
}

// NewGoroutineCheck creates a goroutine count check. maxGoroutines is the
// threshold above which the check returns unhealthy.
func NewGoroutineCheck(maxGoroutines int) *GoroutineCheck {
	return &GoroutineCheck{maxGoroutines: maxGoroutines}
}

func (g *GoroutineCheck) Name() string  { return "goroutines" }
func (g *GoroutineCheck) Optional() bool { return false }

func (g *GoroutineCheck) Check(_ context.Context) error {
	count := runtime.NumGoroutine()
	if count > g.maxGoroutines {
		return fmt.Errorf("goroutine count %d exceeds max %d", count, g.maxGoroutines)
	}
	return nil
}

// MarshalJSON implements custom JSON for HealthReport to ensure consistent output.
func (r *HealthReport) MarshalJSON() ([]byte, error) {
	type Alias HealthReport
	return json.Marshal(&struct {
		*Alias
		Timestamp string `json:"timestamp"`
	}{
		Alias:     (*Alias)(r),
		Timestamp: r.Timestamp.Format(time.RFC3339),
	})
}
