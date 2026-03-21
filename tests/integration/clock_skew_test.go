//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// TestTreasuryIdempotency_ClockJumpBackward verifies that treasury reserve
// idempotency holds even when the same transferID is used for a second Reserve
// call. Because the treasury manager uses the transferID as an idempotency
// reference, calling Reserve twice with the same transferID must not double-lock
// funds, regardless of wall-clock behavior.
func TestTreasuryIdempotency_ClockJumpBackward(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		currency      = domain.CurrencyGBP
		location      = "bank:gbp"
		reserveAmount = 200 // GBP
	)

	transferID := uuid.New()
	amount := decimal.NewFromInt(reserveAmount)

	// 1. Reserve for transferID at "T=0".
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID); err != nil {
		t.Fatalf("first Reserve call failed: %v", err)
	}

	// 2. Capture locked amount after the first reserve.
	posAfterFirst, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after first reserve: %v", err)
	}
	lockedAfterFirst := posAfterFirst.Locked

	// Locked should include at least the reserve amount.
	if lockedAfterFirst.LessThan(amount) {
		t.Fatalf("locked amount should be at least %s after first call, got %s",
			amount.String(), lockedAfterFirst.String())
	}

	// 3. Simulate "clock jump backward" by immediately calling Reserve again
	// with the same transferID. In a real clock-skew scenario the second call
	// would arrive with an earlier timestamp, but the idempotency check is
	// based on the transferID reference, not wall clock.
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID); err != nil {
		t.Fatalf("second Reserve call (idempotent replay) returned unexpected error: %v", err)
	}

	// 4. Capture locked amount after the second (idempotent) reserve.
	posAfterSecond, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after second reserve: %v", err)
	}
	lockedAfterSecond := posAfterSecond.Locked

	// 5. The locked amount must not have changed -- no double-reservation.
	if !lockedAfterSecond.Equal(lockedAfterFirst) {
		t.Errorf("double-reservation detected: locked after first=%s, after second=%s (expected no change)",
			lockedAfterFirst.String(), lockedAfterSecond.String())
	}

	t.Logf("treasury idempotency (clock skew simulation): transferID=%s, locked_after_first=%s, locked_after_second=%s",
		transferID, lockedAfterFirst.String(), lockedAfterSecond.String())
}

// TestTreasuryIdempotency_ReserveRelease_ThenReReserve verifies that after a
// Reserve+Release cycle, a new Reserve with a DIFFERENT transferID succeeds,
// while the same transferID is still blocked by idempotency.
func TestTreasuryIdempotency_ReserveRelease_ThenReReserve(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		currency      = domain.CurrencyGBP
		location      = "bank:gbp"
		reserveAmount = 150
	)

	transferID1 := uuid.New()
	transferID2 := uuid.New()
	amount := decimal.NewFromInt(reserveAmount)

	// 1. Reserve with transferID1.
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID1); err != nil {
		t.Fatalf("Reserve with transferID1 failed: %v", err)
	}

	// 2. Release with transferID1.
	if err := h.Treasury.Release(ctx, LemfiTenantID, currency, location, amount, transferID1); err != nil {
		t.Fatalf("Release with transferID1 failed: %v", err)
	}

	// 3. Attempt Reserve again with transferID1 -- should be an idempotent no-op
	// (the idempotency key is already consumed).
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID1); err != nil {
		t.Fatalf("re-reserve with same ID should be idempotent: %v", err)
	}

	posAfterReplay, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after replay: %v", err)
	}

	// 4. Reserve with a NEW transferID2 -- should succeed and actually lock funds.
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID2); err != nil {
		t.Fatalf("reserve with new transferID should succeed: %v", err)
	}

	posAfterNew, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after new reserve: %v", err)
	}

	// The new reservation should have increased the locked amount.
	if !posAfterNew.Locked.GreaterThan(posAfterReplay.Locked) {
		t.Errorf("new transferID should increase locked amount: before=%s after=%s",
			posAfterReplay.Locked.String(), posAfterNew.Locked.String())
	}

	t.Logf("reserve-release-rereserve: ID1=%s (idempotent), ID2=%s (new lock), locked=%s",
		transferID1, transferID2, posAfterNew.Locked.String())
}

// TestTreasuryReserve_ConcurrentSameTransferID verifies that concurrent calls
// to Reserve with the same transferID do not double-lock. Only one reservation
// should take effect.
func TestTreasuryReserve_ConcurrentSameTransferID(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		currency   = domain.CurrencyGBP
		location   = "bank:gbp"
		goroutines = 50
	)

	transferID := uuid.New()
	amount := decimal.NewFromInt(100)

	// Get initial locked amount.
	posBefore, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition before test: %v", err)
	}
	lockedBefore := posBefore.Locked

	// Fire goroutines concurrently, all with the same transferID.
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			errs <- h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID)
		}()
	}
	wg.Wait()
	close(errs)

	// Collect results.
	var successCount, errCount int
	for err := range errs {
		if err != nil {
			errCount++
		} else {
			successCount++
		}
	}

	// All calls should succeed (first reserves, rest are idempotent no-ops).
	if successCount != goroutines {
		t.Errorf("expected all %d concurrent Reserve calls to succeed, got %d successes and %d errors",
			goroutines, successCount, errCount)
	}

	// The locked amount should have increased by exactly one reservation.
	posAfter, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after test: %v", err)
	}
	lockedIncrease := posAfter.Locked.Sub(lockedBefore)

	if !lockedIncrease.Equal(amount) {
		t.Errorf("locked should increase by exactly %s (one reservation), got %s",
			amount.String(), lockedIncrease.String())
	}

	t.Logf("concurrent idempotency: %d goroutines, locked increase=%s (expected %s)",
		goroutines, lockedIncrease.String(), amount.String())
}
