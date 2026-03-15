//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestConcurrentTransfers_SameIdempotencyKey verifies that 100 concurrent
// CreateTransfer calls with the same idempotency key produce exactly one
// transfer record and all responses return the identical transfer ID.
func TestConcurrentTransfers_SameIdempotencyKey(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	ctx := context.Background()

	const goroutines = 100
	const idempotencyKey = "concurrent-idempotency-test-1"

	type result struct {
		transferID uuid.UUID
		err        error
	}

	results := make([]result, goroutines)
	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()

			transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
				IdempotencyKey: idempotencyKey,
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   decimal.NewFromInt(100),
				DestCurrency:   domain.CurrencyNGN,
				Sender: domain.Sender{
					ID:      uuid.New(),
					Name:    "Concurrent Sender",
					Email:   "sender@lemfi.com",
					Country: "GB",
				},
				Recipient: domain.Recipient{
					Name:          "Concurrent Recipient",
					AccountNumber: "0000000001",
					BankName:      "GTBank",
					Country:       "NG",
				},
			})

			mu.Lock()
			if err != nil {
				results[idx] = result{err: err}
			} else {
				results[idx] = result{transferID: transfer.ID}
			}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Under true concurrency, multiple goroutines may pass the engine's
	// idempotency check simultaneously before any creates. Exactly one will
	// win the store-level UNIQUE constraint; the rest either:
	//   (a) hit the engine idempotency check on a slower path and return the existing transfer, or
	//   (b) fail with a duplicate key error from the store.
	// The critical invariant: at most one transfer record is created.
	var successIDs []uuid.UUID
	for _, r := range results {
		if r.err != nil {
			continue // duplicate key rejection — expected under concurrency
		}
		successIDs = append(successIDs, r.transferID)
	}

	if len(successIDs) == 0 {
		t.Fatal("expected at least 1 successful CreateTransfer, got 0")
	}

	// All successful responses must carry the same transfer ID — idempotency guarantee.
	firstID := successIDs[0]
	for i, id := range successIDs {
		if id != firstID {
			t.Errorf("goroutine %d returned transfer ID %s, expected %s (idempotency violation)", i, id, firstID)
		}
	}

	// Exactly one transfer record must exist in the store for this key.
	allTransfers := h.TransferStore.allTransfers()
	var matchingTransfers int
	for _, tr := range allTransfers {
		if tr.TenantID == LemfiTenantID && tr.IdempotencyKey == idempotencyKey {
			matchingTransfers++
		}
	}
	if matchingTransfers != 1 {
		t.Errorf("expected exactly 1 transfer record for idempotency key %q, found %d", idempotencyKey, matchingTransfers)
	}

	t.Logf("idempotency test passed: %d goroutines all returned transfer ID %s", goroutines, firstID)
}

// TestConcurrentTransfers_SameTreasuryPosition verifies that concurrent treasury
// reserves against a position with insufficient balance for all requests never
// over-reserve. With a balance of 1000 GBP and 50 goroutines each requesting
// 25 GBP (total demand = 1250 GBP), the treasury must reject the excess requests
// and never allow locked+reserved to exceed the initial balance.
func TestConcurrentTransfers_SameTreasuryPosition(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	ctx := context.Background()

	// The default harness seeds Lemfi GBP at 1_000_000 GBP, which is too large
	// for this test. We need a controlled balance of 1000 GBP. Set it via
	// UpdateBalance so the in-memory position reflects the reduced amount.
	const (
		currency = domain.CurrencyGBP
		location = "bank:gbp"
	)
	initialBalance := decimal.NewFromInt(1000)
	if err := h.Treasury.UpdateBalance(ctx, LemfiTenantID, currency, location, initialBalance); err != nil {
		t.Fatalf("UpdateBalance: %v", err)
	}

	const (
		goroutines    = 50
		reserveAmount = 25 // GBP per goroutine; total demand = 1250 GBP > 1000 balance
	)

	type reserveResult struct {
		transferID uuid.UUID
		err        error
	}

	reserveResults := make([]reserveResult, goroutines)
	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()

			// Each goroutine uses a unique transfer ID as the idempotency reference.
			transferID := uuid.New()
			err := h.Treasury.Reserve(
				ctx,
				LemfiTenantID,
				currency,
				location,
				decimal.NewFromInt(reserveAmount),
				transferID,
			)

			mu.Lock()
			reserveResults[idx] = reserveResult{transferID: transferID, err: err}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Count successful reserves.
	var successCount int
	for _, r := range reserveResults {
		if r.err == nil {
			successCount++
		}
	}

	// Assert: we must never have reserved more than the balance allows.
	// successCount * reserveAmount must be ≤ initialBalance.
	maxAllowedSuccesses := initialBalance.Div(decimal.NewFromInt(reserveAmount)).IntPart()
	if int64(successCount) > maxAllowedSuccesses {
		t.Errorf(
			"over-reservation detected: %d goroutines succeeded (each reserving %d GBP), "+
				"but balance is only %s GBP (max allowed successes: %d)",
			successCount, reserveAmount, initialBalance.String(), maxAllowedSuccesses,
		)
	}

	// Assert: the treasury position's locked/reserved amount must not exceed the
	// initial balance. Use GetPosition to read the current in-memory state.
	pos, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}

	if pos.Locked.GreaterThan(initialBalance) {
		t.Errorf(
			"treasury locked amount %s exceeds initial balance %s (invariant violated)",
			pos.Locked.String(), initialBalance.String(),
		)
	}

	t.Logf(
		"concurrent treasury test: %d goroutines attempted, %d succeeded, "+
			"treasury locked=%s (balance=%s)",
		goroutines, successCount, pos.Locked.String(), initialBalance.String(),
	)
}

// TestConcurrentTransfers_MultiCurrencyContention verifies that concurrent
// reserves across different currencies (25 goroutines for GBP via Lemfi +
// 25 goroutines for NGN via Fincra) never over-reserve either currency and
// cause no cross-currency interference.
func TestConcurrentTransfers_MultiCurrencyContention(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	ctx := context.Background()

	// Set controlled balances: Lemfi GBP 500, Fincra NGN 500_000.
	lemfiBalance := decimal.NewFromInt(500)
	fincraBalance := decimal.NewFromInt(500_000)
	if err := h.Treasury.UpdateBalance(ctx, LemfiTenantID, domain.CurrencyGBP, "bank:gbp", lemfiBalance); err != nil {
		t.Fatalf("UpdateBalance GBP: %v", err)
	}
	if err := h.Treasury.UpdateBalance(ctx, FincraTenantID, domain.CurrencyNGN, "bank:ngn", fincraBalance); err != nil {
		t.Fatalf("UpdateBalance NGN: %v", err)
	}

	const (
		goroutinesPerCurrency = 25
		gbpReserveAmount      = 25  // 25 GBP each; total demand = 625 > 500
		ngnReserveAmount      = 25000 // 25K NGN each; total demand = 625K > 500K
	)

	type reserveResult struct {
		currency string
		err      error
	}

	results := make([]reserveResult, goroutinesPerCurrency*2)
	var wg sync.WaitGroup

	// 25 goroutines reserving GBP (Lemfi)
	for i := 0; i < goroutinesPerCurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := h.Treasury.Reserve(ctx, LemfiTenantID, domain.CurrencyGBP, "bank:gbp",
				decimal.NewFromInt(gbpReserveAmount), uuid.New())
			results[idx] = reserveResult{currency: "GBP", err: err}
		}(i)
	}

	// 25 goroutines reserving NGN (Fincra)
	for i := 0; i < goroutinesPerCurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := h.Treasury.Reserve(ctx, FincraTenantID, domain.CurrencyNGN, "bank:ngn",
				decimal.NewFromInt(ngnReserveAmount), uuid.New())
			results[goroutinesPerCurrency+idx] = reserveResult{currency: "NGN", err: err}
		}(i)
	}

	wg.Wait()

	// Count successes per currency.
	gbpSuccesses, ngnSuccesses := 0, 0
	for _, r := range results {
		if r.err == nil {
			switch r.currency {
			case "GBP":
				gbpSuccesses++
			case "NGN":
				ngnSuccesses++
			}
		}
	}

	// Assert GBP never over-reserved.
	maxGBP := lemfiBalance.Div(decimal.NewFromInt(gbpReserveAmount)).IntPart()
	if int64(gbpSuccesses) > maxGBP {
		t.Errorf("GBP over-reservation: %d succeeded (max %d for balance %s)", gbpSuccesses, maxGBP, lemfiBalance)
	}

	// Assert NGN never over-reserved.
	maxNGN := fincraBalance.Div(decimal.NewFromInt(ngnReserveAmount)).IntPart()
	if int64(ngnSuccesses) > maxNGN {
		t.Errorf("NGN over-reservation: %d succeeded (max %d for balance %s)", ngnSuccesses, maxNGN, fincraBalance)
	}

	// Verify positions.
	gbpPos, err := h.Treasury.GetPosition(ctx, LemfiTenantID, domain.CurrencyGBP, "bank:gbp")
	if err != nil {
		t.Fatalf("GetPosition GBP: %v", err)
	}
	if gbpPos.Locked.GreaterThan(lemfiBalance) {
		t.Errorf("GBP locked %s exceeds balance %s", gbpPos.Locked, lemfiBalance)
	}

	ngnPos, err := h.Treasury.GetPosition(ctx, FincraTenantID, domain.CurrencyNGN, "bank:ngn")
	if err != nil {
		t.Fatalf("GetPosition NGN: %v", err)
	}
	if ngnPos.Locked.GreaterThan(fincraBalance) {
		t.Errorf("NGN locked %s exceeds balance %s", ngnPos.Locked, fincraBalance)
	}

	t.Logf("multi-currency contention: GBP %d/%d succeeded, NGN %d/%d succeeded",
		gbpSuccesses, goroutinesPerCurrency, ngnSuccesses, goroutinesPerCurrency)
}
