package healthcheck

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── Mock check ──────────────────────────────────────────────────────────────

type mockCheck struct {
	name     string
	optional bool
	fn       func(ctx context.Context) error
}

func (m *mockCheck) Name() string                    { return m.name }
func (m *mockCheck) Optional() bool                  { return m.optional }
func (m *mockCheck) Check(ctx context.Context) error { return m.fn(ctx) }

func newPassingCheck(name string, optional bool) *mockCheck {
	return &mockCheck{name: name, optional: optional, fn: func(ctx context.Context) error { return nil }}
}

func newFailingCheck(name string, optional bool, err error) *mockCheck {
	return &mockCheck{name: name, optional: optional, fn: func(ctx context.Context) error { return err }}
}

func newSlowCheck(name string, optional bool, d time.Duration) *mockCheck {
	return &mockCheck{name: name, optional: optional, fn: func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

// ── Checker tests ───────────────────────────────────────────────────────────

func TestChecker_AllHealthy(t *testing.T) {
	checks := []Check{
		newPassingCheck("postgres_ledger", false),
		newPassingCheck("postgres_transfer", false),
		newPassingCheck("redis", true),
	}

	checker := NewChecker(slog.Default(), checks, WithVersion("test-v1"))
	report := checker.RunChecks(context.Background())

	if report.Status != StatusHealthy {
		t.Errorf("expected healthy, got %s", report.Status)
	}
	if len(report.Checks) != 3 {
		t.Errorf("expected 3 checks, got %d", len(report.Checks))
	}
	if report.Version != "test-v1" {
		t.Errorf("expected version test-v1, got %s", report.Version)
	}
	for _, c := range report.Checks {
		if c.Status != StatusHealthy {
			t.Errorf("check %s: expected healthy, got %s", c.Name, c.Status)
		}
	}
}

func TestChecker_RequiredCheckFails(t *testing.T) {
	checks := []Check{
		newPassingCheck("postgres_ledger", false),
		newFailingCheck("postgres_transfer", false, errors.New("connection refused")),
		newPassingCheck("redis", true),
	}

	checker := NewChecker(slog.Default(), checks)
	report := checker.RunChecks(context.Background())

	if report.Status != StatusUnhealthy {
		t.Errorf("expected unhealthy, got %s", report.Status)
	}

	for _, c := range report.Checks {
		if c.Name == "postgres_transfer" {
			if c.Status != StatusUnhealthy {
				t.Errorf("postgres_transfer: expected unhealthy, got %s", c.Status)
			}
			if c.Error == "" {
				t.Error("postgres_transfer: expected error message")
			}
		}
	}
}

func TestChecker_OptionalCheckFails_Degraded(t *testing.T) {
	checks := []Check{
		newPassingCheck("postgres_ledger", false),
		newPassingCheck("postgres_transfer", false),
		newFailingCheck("redis", true, errors.New("connection refused")),
	}

	checker := NewChecker(slog.Default(), checks)
	report := checker.RunChecks(context.Background())

	if report.Status != StatusDegraded {
		t.Errorf("expected degraded, got %s", report.Status)
	}
}

func TestChecker_TimeoutCheck(t *testing.T) {
	checks := []Check{
		newPassingCheck("fast", false),
		newSlowCheck("slow", false, 5*time.Second), // will timeout
	}

	checker := NewChecker(slog.Default(), checks, WithCheckTimeout(50*time.Millisecond))
	report := checker.RunChecks(context.Background())

	if report.Status != StatusUnhealthy {
		t.Errorf("expected unhealthy due to timeout, got %s", report.Status)
	}

	for _, c := range report.Checks {
		if c.Name == "slow" {
			if c.Status != StatusUnhealthy {
				t.Errorf("slow check: expected unhealthy, got %s", c.Status)
			}
			if c.Error == "" {
				t.Error("slow check: expected error message for timeout")
			}
		}
	}
}

func TestChecker_CachesResult(t *testing.T) {
	callCount := 0
	checks := []Check{
		&mockCheck{name: "counter", fn: func(ctx context.Context) error {
			callCount++
			return nil
		}},
	}

	checker := NewChecker(slog.Default(), checks, WithCacheTTL(1*time.Second))

	// First call executes checks.
	checker.RunChecks(context.Background())
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}

	// Second call uses cache.
	checker.RunChecks(context.Background())
	if callCount != 1 {
		t.Errorf("expected still 1 call (cached), got %d", callCount)
	}

	// Wait for cache to expire.
	time.Sleep(1100 * time.Millisecond)
	checker.RunChecks(context.Background())
	if callCount != 2 {
		t.Errorf("expected 2 calls after cache expiry, got %d", callCount)
	}
}

func TestChecker_Startup(t *testing.T) {
	checker := NewChecker(slog.Default(), nil)

	if checker.IsStartupComplete() {
		t.Error("startup should not be complete initially")
	}

	checker.MarkStartupComplete()

	if !checker.IsStartupComplete() {
		t.Error("startup should be complete after marking")
	}
}

func TestChecker_ParallelExecution(t *testing.T) {
	// Verify all checks run concurrently by having each take 50ms.
	// If sequential, total would be 150ms+. If parallel, ~50ms.
	checks := []Check{
		newSlowCheck("a", false, 50*time.Millisecond),
		newSlowCheck("b", false, 50*time.Millisecond),
		newSlowCheck("c", false, 50*time.Millisecond),
	}

	checker := NewChecker(slog.Default(), checks, WithCheckTimeout(200*time.Millisecond), WithCacheTTL(0))
	start := time.Now()
	report := checker.RunChecks(context.Background())
	elapsed := time.Since(start)

	if report.Status != StatusHealthy {
		t.Errorf("expected healthy, got %s", report.Status)
	}

	// Allow generous margin but should be well under 150ms if parallel.
	if elapsed > 130*time.Millisecond {
		t.Errorf("checks appear sequential: took %v, expected ~50ms", elapsed)
	}
}

// ── GoroutineCheck tests ────────────────────────────────────────────────────

func TestGoroutineCheck_Passes(t *testing.T) {
	check := NewGoroutineCheck(1000000) // very high threshold
	if err := check.Check(context.Background()); err != nil {
		t.Errorf("goroutine check should pass with high threshold: %v", err)
	}
}

func TestGoroutineCheck_Fails(t *testing.T) {
	check := NewGoroutineCheck(1) // impossibly low threshold
	if err := check.Check(context.Background()); err == nil {
		t.Error("goroutine check should fail with threshold of 1")
	}
}

// ── Handler tests ───────────────────────────────────────────────────────────

func TestHandler_Liveness(t *testing.T) {
	checker := NewChecker(slog.Default(), nil)
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()
	handler.handleLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != string(StatusHealthy) {
		t.Errorf("expected healthy status, got %v", resp["status"])
	}
}

func TestHandler_LivenessFailsOnGoroutineLeak(t *testing.T) {
	checker := NewChecker(slog.Default(), nil)
	handler := NewHandler(checker, 1) // impossibly low

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()
	handler.handleLive(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandler_Readiness(t *testing.T) {
	checks := []Check{
		newPassingCheck("db", false),
	}
	checker := NewChecker(slog.Default(), checks)
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	handler.handleReady(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandler_ReadinessUnhealthy(t *testing.T) {
	checks := []Check{
		newFailingCheck("db", false, errors.New("down")),
	}
	checker := NewChecker(slog.Default(), checks)
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	handler.handleReady(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandler_StartupNotReady(t *testing.T) {
	checker := NewChecker(slog.Default(), nil)
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health/startup", nil)
	rec := httptest.NewRecorder()
	handler.handleStartup(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 before startup, got %d", rec.Code)
	}
}

func TestHandler_StartupReady(t *testing.T) {
	checks := []Check{
		newPassingCheck("db", false),
	}
	checker := NewChecker(slog.Default(), checks)
	checker.MarkStartupComplete()
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health/startup", nil)
	rec := httptest.NewRecorder()
	handler.handleStartup(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 after startup, got %d", rec.Code)
	}
}

func TestHandler_FullReport(t *testing.T) {
	checks := []Check{
		newPassingCheck("postgres_ledger", false),
		newPassingCheck("postgres_transfer", false),
		newPassingCheck("redis", true),
	}

	checker := NewChecker(slog.Default(), checks, WithVersion("v1.2.3"))
	handler := NewHandler(checker, 100000)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.handleFull(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var report HealthReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("failed to decode report: %v", err)
	}

	if report.Status != StatusHealthy {
		t.Errorf("expected healthy, got %s", report.Status)
	}
	if len(report.Checks) != 3 {
		t.Errorf("expected 3 checks, got %d", len(report.Checks))
	}
	if report.Version != "v1.2.3" {
		t.Errorf("expected version v1.2.3, got %s", report.Version)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	checker := NewChecker(slog.Default(), nil)
	handler := NewHandler(checker, 100000)

	endpoints := []struct {
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"/health/live", handler.handleLive},
		{"/health/ready", handler.handleReady},
		{"/health/startup", handler.handleStartup},
		{"/health", handler.handleFull},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(http.MethodPost, ep.path, nil)
		rec := httptest.NewRecorder()
		ep.handler(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405 for POST, got %d", ep.path, rec.Code)
		}
	}
}

// ── CallbackCheck tests ─────────────────────────────────────────────────────

func TestCallbackCheck(t *testing.T) {
	t.Run("passing", func(t *testing.T) {
		check := NewCallbackCheck("test", false, func(ctx context.Context) error { return nil })
		if err := check.Check(context.Background()); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if check.Name() != "test" {
			t.Errorf("expected name 'test', got %s", check.Name())
		}
		if check.Optional() {
			t.Error("expected not optional")
		}
	})

	t.Run("failing", func(t *testing.T) {
		check := NewCallbackCheck("fail", true, func(ctx context.Context) error { return errors.New("boom") })
		if err := check.Check(context.Background()); err == nil {
			t.Error("expected error")
		}
		if !check.Optional() {
			t.Error("expected optional")
		}
	})
}

func TestNewTigerBeetleCheck(t *testing.T) {
	check := NewTigerBeetleCheck(func(ctx context.Context) error { return nil })
	if check.Name() != "tigerbeetle" {
		t.Errorf("expected name 'tigerbeetle', got %s", check.Name())
	}
	if check.Optional() {
		t.Error("tigerbeetle should not be optional")
	}
}

func TestNewNATSCheck(t *testing.T) {
	check := NewNATSCheck(func(ctx context.Context) error { return nil })
	if check.Name() != "nats" {
		t.Errorf("expected name 'nats', got %s", check.Name())
	}
	if check.Optional() {
		t.Error("nats should not be optional")
	}
}

func TestNewRedisCheck(t *testing.T) {
	check := NewRedisCheck(func(ctx context.Context) error { return nil })
	if check.Name() != "redis" {
		t.Errorf("expected name 'redis', got %s", check.Name())
	}
	if !check.Optional() {
		t.Error("redis should be optional")
	}
}

// ── Integration: Handler with ServeMux ──────────────────────────────────────

func TestHandler_RegisterAndServe(t *testing.T) {
	checks := []Check{
		newPassingCheck("db", false),
	}
	checker := NewChecker(slog.Default(), checks, WithVersion("test"))
	checker.MarkStartupComplete()
	handler := NewHandler(checker, 100000)

	mux := http.NewServeMux()
	handler.Register(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	paths := []string{"/health", "/health/live", "/health/ready", "/health/startup"}
	for _, path := range paths {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: expected 200, got %d", path, resp.StatusCode)
		}
	}
}
