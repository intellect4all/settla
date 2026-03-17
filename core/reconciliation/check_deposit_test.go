package reconciliation

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

type mockDepositQuerier struct {
	stuckSessions     int
	staleCheckpoints  int
	availableAddrs    int
	txMismatches      int
	stuckErr          error
	checkpointErr     error
	poolErr           error
	mismatchErr       error
}

func (m *mockDepositQuerier) CountStuckDepositSessions(_ context.Context, _ time.Time) (int, error) {
	return m.stuckSessions, m.stuckErr
}

func (m *mockDepositQuerier) CountStaleBlockCheckpoints(_ context.Context, _ time.Time) (int, error) {
	return m.staleCheckpoints, m.checkpointErr
}

func (m *mockDepositQuerier) CountAvailablePoolAddressesAll(_ context.Context) (int, error) {
	return m.availableAddrs, m.poolErr
}

func (m *mockDepositQuerier) CountDepositTxAmountMismatches(_ context.Context) (int, error) {
	return m.txMismatches, m.mismatchErr
}

func TestDepositCheck_AllHealthy(t *testing.T) {
	check := NewDepositCheck(&mockDepositQuerier{
		stuckSessions:    0,
		staleCheckpoints: 0,
		availableAddrs:   500,
		txMismatches:     0,
	}, slog.Default(), 0, 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", result.Status, result.Details)
	}
	if result.Mismatches != 0 {
		t.Errorf("expected 0 mismatches, got %d", result.Mismatches)
	}
}

func TestDepositCheck_StuckSessions(t *testing.T) {
	check := NewDepositCheck(&mockDepositQuerier{
		stuckSessions:    5,
		staleCheckpoints: 0,
		availableAddrs:   500,
		txMismatches:     0,
	}, slog.Default(), 0, 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 5 {
		t.Errorf("expected 5 mismatches, got %d", result.Mismatches)
	}
}

func TestDepositCheck_PoolDepleting(t *testing.T) {
	check := NewDepositCheck(&mockDepositQuerier{
		stuckSessions:    0,
		staleCheckpoints: 0,
		availableAddrs:   10, // below default threshold of 50
		txMismatches:     0,
	}, slog.Default(), 0, 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
}

func TestDepositCheck_MultipleIssues(t *testing.T) {
	check := NewDepositCheck(&mockDepositQuerier{
		stuckSessions:    3,
		staleCheckpoints: 2,
		availableAddrs:   10,
		txMismatches:     1,
	}, slog.Default(), 0, 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	// 3 stuck + 2 stale + 1 pool + 1 mismatch = 7
	if result.Mismatches != 7 {
		t.Errorf("expected 7 mismatches, got %d", result.Mismatches)
	}
}

func TestDepositCheck_Name(t *testing.T) {
	check := NewDepositCheck(&mockDepositQuerier{}, slog.Default(), 0, 0, 0)
	if check.Name() != "deposit_health" {
		t.Errorf("expected 'deposit_health', got %q", check.Name())
	}
}
