package settla

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// fastDelays returns a delay table with very short durations so tests finish quickly.
func fastDelays() map[string][2]time.Duration {
	return map[string][2]time.Duration{
		"TST": {10 * time.Millisecond, 20 * time.Millisecond},
	}
}

// deterministicConfig returns a zero-failure-rate config with short delays.
func deterministicConfig() SimulatorConfig {
	return SimulatorConfig{FailureRate: 0, CurrencyDelays: fastDelays()}
}

// alwaysFailConfig returns a 100% failure-rate config with short delays.
func alwaysFailConfig() SimulatorConfig {
	return SimulatorConfig{FailureRate: 1, CurrencyDelays: fastDelays()}
}

// pollUntilTerminal waits up to timeout for tx to reach a terminal status.
func pollUntilTerminal(t *testing.T, s *FiatSimulator, txID string, timeout time.Duration) *FiatTransaction {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := s.GetStatus(txID)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		st := result.Status
		if st == FiatStatusCollected || st == FiatStatusCompleted || st == FiatStatusFailed {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("transaction did not reach terminal status within timeout")
	return nil
}

// --- SimulateCollection ---

func TestSimulateCollection_InitialStatus(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	tx, err := s.SimulateCollection(context.Background(), decimal.NewFromInt(100), "TST", "ref-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status != FiatStatusPending {
		t.Errorf("want PENDING, got %s", tx.Status)
	}
	if tx.Type != FiatTxCollection {
		t.Errorf("want COLLECTION, got %s", tx.Type)
	}
	if tx.BankRef == "" {
		t.Error("BankRef must not be empty")
	}
}

func TestSimulateCollection_Progression(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	tx, err := s.SimulateCollection(context.Background(), decimal.NewFromInt(50), "TST", "ref-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	final := pollUntilTerminal(t, s, tx.ID, 500*time.Millisecond)

	if final.Status != FiatStatusCollected {
		t.Errorf("want COLLECTED, got %s", final.Status)
	}
	if final.CompletedAt == nil {
		t.Error("CompletedAt must be set on terminal status")
	}

	wantStatuses := []FiatStatus{FiatStatusPending, FiatStatusProcessing, FiatStatusCollected}
	if len(final.History) != len(wantStatuses) {
		t.Fatalf("history length: want %d, got %d", len(wantStatuses), len(final.History))
	}
	for i, want := range wantStatuses {
		if final.History[i].Status != want {
			t.Errorf("history[%d]: want %s, got %s", i, want, final.History[i].Status)
		}
	}
}

func TestSimulateCollection_FailurePath(t *testing.T) {
	s := NewFiatSimulator(alwaysFailConfig())
	tx, _ := s.SimulateCollection(context.Background(), decimal.NewFromInt(100), "TST", "ref-003")

	final := pollUntilTerminal(t, s, tx.ID, 500*time.Millisecond)

	if final.Status != FiatStatusFailed {
		t.Errorf("want FAILED, got %s", final.Status)
	}
}

func TestSimulateCollection_InvalidAmount(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	_, err := s.SimulateCollection(context.Background(), decimal.NewFromInt(0), "TST", "ref")
	if err == nil {
		t.Error("expected error for zero amount")
	}
	_, err = s.SimulateCollection(context.Background(), decimal.NewFromInt(-10), "TST", "ref")
	if err == nil {
		t.Error("expected error for negative amount")
	}
}

// --- SimulatePayout ---

func TestSimulatePayout_InitialStatus(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	tx, err := s.SimulatePayout(context.Background(), decimal.NewFromInt(200), "TST", "GB29NWBK60161331926819")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Status != FiatStatusPayoutInitiated {
		t.Errorf("want PAYOUT_INITIATED, got %s", tx.Status)
	}
	if tx.Type != FiatTxPayout {
		t.Errorf("want PAYOUT, got %s", tx.Type)
	}
}

func TestSimulatePayout_Progression(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	tx, _ := s.SimulatePayout(context.Background(), decimal.NewFromInt(500), "TST", "recipient-001")

	final := pollUntilTerminal(t, s, tx.ID, 500*time.Millisecond)

	if final.Status != FiatStatusCompleted {
		t.Errorf("want COMPLETED, got %s", final.Status)
	}

	wantStatuses := []FiatStatus{FiatStatusPayoutInitiated, FiatStatusPayoutProcessing, FiatStatusCompleted}
	if len(final.History) != len(wantStatuses) {
		t.Fatalf("history length: want %d, got %d", len(wantStatuses), len(final.History))
	}
	for i, want := range wantStatuses {
		if final.History[i].Status != want {
			t.Errorf("history[%d]: want %s, got %s", i, want, final.History[i].Status)
		}
	}
}

func TestSimulatePayout_FailurePath(t *testing.T) {
	s := NewFiatSimulator(alwaysFailConfig())
	tx, _ := s.SimulatePayout(context.Background(), decimal.NewFromInt(100), "TST", "recipient-fail")

	final := pollUntilTerminal(t, s, tx.ID, 500*time.Millisecond)

	if final.Status != FiatStatusFailed {
		t.Errorf("want FAILED, got %s", final.Status)
	}
}

func TestSimulatePayout_InvalidAmount(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	_, err := s.SimulatePayout(context.Background(), decimal.Zero, "TST", "recip")
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

// --- GetStatus ---

func TestGetStatus_UnknownID(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	_, err := s.GetStatus("nonexistent-id")
	if err == nil {
		t.Error("expected error for unknown transaction ID")
	}
}

func TestGetStatus_ReturnsCopy(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	tx, _ := s.SimulateCollection(context.Background(), decimal.NewFromInt(100), "TST", "ref")

	result, _ := s.GetStatus(tx.ID)
	// Mutate the copy; internal state must not change.
	result.History = nil
	result.Status = FiatStatusFailed

	internal, _ := s.GetStatus(tx.ID)
	if internal.Status == FiatStatusFailed {
		t.Error("GetStatus should return a copy, not a pointer to internal state")
	}
	if len(internal.History) == 0 {
		t.Error("internal history should not be affected by mutations to the returned copy")
	}
}

// --- Concurrency ---

func TestConcurrentSimulations(t *testing.T) {
	s := NewFiatSimulator(deterministicConfig())
	ctx := context.Background()

	const n = 50
	ids := make([]string, n)

	for i := range n {
		var tx *FiatTransaction
		var err error
		if i%2 == 0 {
			tx, err = s.SimulateCollection(ctx, decimal.NewFromInt(int64(i+1)), "TST", fmt.Sprintf("ref-%d", i))
		} else {
			tx, err = s.SimulatePayout(ctx, decimal.NewFromInt(int64(i+1)), "TST", fmt.Sprintf("recip-%d", i))
		}
		if err != nil {
			t.Fatalf("index %d: %v", i, err)
		}
		ids[i] = tx.ID
	}

	for _, id := range ids {
		pollUntilTerminal(t, s, id, 2*time.Second)
	}
}

// --- Currency delay coverage ---

func TestCurrencyDelays_AllConfigured(t *testing.T) {
	currencies := []string{"NGN", "GBP", "USD", "EUR", "GHS"}
	for _, c := range currencies {
		bounds, ok := defaultCurrencyDelays[c]
		if !ok {
			t.Errorf("currency %s missing from default delay table", c)
			continue
		}
		if bounds[0] >= bounds[1] {
			t.Errorf("currency %s: min delay must be < max delay", c)
		}
	}
}

// --- BankRef format ---

func TestGenerateBankRef_Format(t *testing.T) {
	for range 20 {
		ref := generateBankRef()
		if len(ref) != 10 { // "BNK-" (4) + 6 chars
			t.Errorf("unexpected bank ref length %d: %q", len(ref), ref)
		}
		if ref[:4] != "BNK-" {
			t.Errorf("bank ref must start with BNK-: %q", ref)
		}
	}
}
