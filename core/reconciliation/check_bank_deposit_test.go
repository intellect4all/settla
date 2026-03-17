package reconciliation

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

type mockBankDepositQuerier struct {
	stuckSessions    int
	stuckCrediting   int
	orphanedAccounts int
	stuckErr         error
	creditingErr     error
	orphanedErr      error
}

func (m *mockBankDepositQuerier) CountStuckBankDepositSessions(_ context.Context, _ time.Time) (int, error) {
	return m.stuckSessions, m.stuckErr
}

func (m *mockBankDepositQuerier) CountStuckBankDepositCrediting(_ context.Context, _ time.Time) (int, error) {
	return m.stuckCrediting, m.creditingErr
}

func (m *mockBankDepositQuerier) CountOrphanedVirtualAccounts(_ context.Context) (int, error) {
	return m.orphanedAccounts, m.orphanedErr
}

func TestBankDepositCheck_AllHealthy(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{
		stuckSessions:    0,
		stuckCrediting:   0,
		orphanedAccounts: 0,
	}, slog.Default(), 0, 0)

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

func TestBankDepositCheck_StuckSessions(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{
		stuckSessions:    5,
		stuckCrediting:   0,
		orphanedAccounts: 0,
	}, slog.Default(), 0, 0)

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

func TestBankDepositCheck_StuckCrediting(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{
		stuckSessions:    0,
		stuckCrediting:   3,
		orphanedAccounts: 0,
	}, slog.Default(), 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 3 {
		t.Errorf("expected 3 mismatches, got %d", result.Mismatches)
	}
}

func TestBankDepositCheck_OrphanedAccounts(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{
		stuckSessions:    0,
		stuckCrediting:   0,
		orphanedAccounts: 2,
	}, slog.Default(), 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	if result.Mismatches != 2 {
		t.Errorf("expected 2 mismatches, got %d", result.Mismatches)
	}
}

func TestBankDepositCheck_MultipleIssues(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{
		stuckSessions:    3,
		stuckCrediting:   2,
		orphanedAccounts: 1,
	}, slog.Default(), 0, 0)

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected fail, got %s", result.Status)
	}
	// 3 stuck + 2 crediting + 1 orphaned = 6
	if result.Mismatches != 6 {
		t.Errorf("expected 6 mismatches, got %d", result.Mismatches)
	}
}

func TestBankDepositCheck_Name(t *testing.T) {
	check := NewBankDepositCheck(&mockBankDepositQuerier{}, slog.Default(), 0, 0)
	if check.Name() != "bank_deposit" {
		t.Errorf("expected 'bank_deposit', got %q", check.Name())
	}
}
