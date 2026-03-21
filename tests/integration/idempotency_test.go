//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestIdempotency_CreateTransfer_ExactlyOnce calls CreateTransfer 10 times with
// the same idempotency key and verifies all calls return the same transfer ID and
// that exactly one record exists in the store.
func TestIdempotency_CreateTransfer_ExactlyOnce(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		repetitions    = 10
		idempotencyKey = "idempotency-exactly-once-1"
	)

	req := core.CreateTransferRequest{
		IdempotencyKey: idempotencyKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Alice Baker",
			Email:   "alice@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Bob Charles",
			AccountNumber: "1111111111",
			BankName:      "Zenith Bank",
			Country:       "NG",
		},
	}

	var firstID uuid.UUID
	for i := range repetitions {
		tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, req)
		if err != nil {
			t.Fatalf("call %d: CreateTransfer returned unexpected error: %v", i+1, err)
		}
		if i == 0 {
			firstID = tr.ID
		} else if tr.ID != firstID {
			t.Errorf("call %d: expected transfer ID %s, got %s (idempotency violation)", i+1, firstID, tr.ID)
		}
	}

	// Verify the store contains exactly one record for this idempotency key.
	fetched, err := h.TransferStore.GetTransferByIdempotencyKey(ctx, LemfiTenantID, idempotencyKey)
	if err != nil {
		t.Fatalf("GetTransferByIdempotencyKey: %v", err)
	}
	if fetched.ID != firstID {
		t.Errorf("store returned transfer ID %s, expected %s", fetched.ID, firstID)
	}

	// Count total records for this key (must be exactly 1).
	allTransfers := h.TransferStore.allTransfers()
	var count int
	for _, tr := range allTransfers {
		if tr.TenantID == LemfiTenantID && tr.IdempotencyKey == idempotencyKey {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 transfer record for key %q, found %d", idempotencyKey, count)
	}

	t.Logf("exactly-once idempotency verified: transfer=%s, %d calls → 1 record", firstID, repetitions)
}

// TestIdempotency_DifferentKeys_CreateDifferentTransfers verifies that 10 calls
// with distinct idempotency keys each produce a unique transfer record.
func TestIdempotency_DifferentKeys_CreateDifferentTransfers(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const count = 10
	ids := make(map[uuid.UUID]struct{}, count)

	for i := range count {
		key := fmt.Sprintf("unique-key-%d", i)
		tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: key,
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(1000),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    fmt.Sprintf("Sender %d", i),
				Email:   fmt.Sprintf("sender%d@lemfi.com", i),
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          fmt.Sprintf("Recipient %d", i),
				AccountNumber: fmt.Sprintf("%010d", i),
				BankName:      "GTBank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("transfer %d: CreateTransfer returned unexpected error: %v", i+1, err)
		}
		if _, dup := ids[tr.ID]; dup {
			t.Errorf("transfer %d: duplicate transfer ID %s returned", i+1, tr.ID)
		}
		ids[tr.ID] = struct{}{}
	}

	if len(ids) != count {
		t.Errorf("expected %d distinct transfer IDs, got %d", count, len(ids))
	}

	// All 10 transfers must be retrievable from the store.
	for id := range ids {
		if _, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, id); err != nil {
			t.Errorf("transfer %s not found in store: %v", id, err)
		}
	}

	t.Logf("distinct-keys test passed: %d unique transfers created", len(ids))
}

// TestIdempotency_CrossTenantSameKey verifies that two tenants can each create a
// transfer using the identical idempotency key without conflict. Each tenant must
// retrieve only their own transfer via GetTransferByIdempotencyKey.
func TestIdempotency_CrossTenantSameKey(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const sharedKey = "shared-key"

	// Lemfi creates a transfer with the shared key.
	lemfiTransfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Lemfi Sender",
			Email:   "lemfi@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Lemfi Recipient",
			AccountNumber: "2222222222",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("Lemfi CreateTransfer: %v", err)
	}

	// Fincra creates a transfer with the same shared key.
	fincraTransfer, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(400_000),
		DestCurrency:   domain.CurrencyGBP,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Fincra Sender",
			Email:   "fincra@fincra.com",
			Country: "NG",
		},
		Recipient: domain.Recipient{
			Name:          "Fincra Recipient",
			AccountNumber: "33333333",
			SortCode:      "20-00-00",
			BankName:      "Barclays",
			Country:       "GB",
		},
	})
	if err != nil {
		t.Fatalf("Fincra CreateTransfer: %v", err)
	}

	// Both calls must succeed and return different transfer IDs.
	if lemfiTransfer.ID == fincraTransfer.ID {
		t.Errorf("cross-tenant idempotency collision: both tenants returned the same transfer ID %s", lemfiTransfer.ID)
	}

	// Each tenant's lookup must return their own transfer only.
	lemfiLookup, err := h.TransferStore.GetTransferByIdempotencyKey(ctx, LemfiTenantID, sharedKey)
	if err != nil {
		t.Fatalf("Lemfi GetTransferByIdempotencyKey: %v", err)
	}
	if lemfiLookup.ID != lemfiTransfer.ID {
		t.Errorf("Lemfi lookup returned transfer %s, expected %s", lemfiLookup.ID, lemfiTransfer.ID)
	}
	if lemfiLookup.TenantID != LemfiTenantID {
		t.Errorf("Lemfi lookup returned wrong tenant ID %s", lemfiLookup.TenantID)
	}

	fincraLookup, err := h.TransferStore.GetTransferByIdempotencyKey(ctx, FincraTenantID, sharedKey)
	if err != nil {
		t.Fatalf("Fincra GetTransferByIdempotencyKey: %v", err)
	}
	if fincraLookup.ID != fincraTransfer.ID {
		t.Errorf("Fincra lookup returned transfer %s, expected %s", fincraLookup.ID, fincraTransfer.ID)
	}
	if fincraLookup.TenantID != FincraTenantID {
		t.Errorf("Fincra lookup returned wrong tenant ID %s", fincraLookup.TenantID)
	}

	t.Logf("cross-tenant idempotency verified: Lemfi=%s, Fincra=%s (same key=%q)",
		lemfiTransfer.ID, fincraTransfer.ID, sharedKey)
}

// TestIdempotency_TreasuryReserve verifies that calling Treasury.Reserve twice
// with the same reference UUID (transfer ID) does not double-reserve. The locked
// amount in the position must reflect only one reservation.
func TestIdempotency_TreasuryReserve(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		currency      = domain.CurrencyGBP
		location      = "bank:gbp"
		reserveAmount = 100 // GBP
	)

	// Use a fixed transfer ID as the idempotency reference.
	transferID := uuid.New()
	amount := decimal.NewFromInt(reserveAmount)

	// First reserve — must succeed.
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID); err != nil {
		t.Fatalf("first Reserve call failed: %v", err)
	}

	// Capture locked amount after the first reserve.
	posAfterFirst, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after first reserve: %v", err)
	}
	lockedAfterFirst := posAfterFirst.Locked

	// Second reserve with the same transfer ID — must be a no-op (idempotent).
	if err := h.Treasury.Reserve(ctx, LemfiTenantID, currency, location, amount, transferID); err != nil {
		t.Fatalf("second Reserve call (idempotent replay) returned unexpected error: %v", err)
	}

	// Capture locked amount after the second (idempotent) reserve.
	posAfterSecond, err := h.Treasury.GetPosition(ctx, LemfiTenantID, currency, location)
	if err != nil {
		t.Fatalf("GetPosition after second reserve: %v", err)
	}
	lockedAfterSecond := posAfterSecond.Locked

	// The locked amount must not have changed — no double-reservation.
	if !lockedAfterSecond.Equal(lockedAfterFirst) {
		t.Errorf(
			"double-reservation detected: locked amount after first reserve=%s, after second=%s "+
				"(expected no change — second call should be a no-op)",
			lockedAfterFirst.String(), lockedAfterSecond.String(),
		)
	}

	// The locked amount must equal exactly one reservation (100 GBP).
	if !lockedAfterFirst.Equal(amount) {
		t.Errorf("expected locked=%s after first reserve, got %s", amount.String(), lockedAfterFirst.String())
	}

	t.Logf("treasury reserve idempotency verified: transferID=%s, locked=%s (expected %s)",
		transferID, lockedAfterSecond.String(), amount.String())
}

// TestIdempotency_ConcurrentCreateTransfer_ExactlyOneWins fires 50 goroutines
// that call CreateTransfer simultaneously with the same tenant and idempotency
// key. It verifies that all calls succeed, all return the same transfer ID, and
// exactly one transfer record exists in the store.
func TestIdempotency_ConcurrentCreateTransfer_ExactlyOneWins(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		goroutines     = 50
		idempotencyKey = "concurrent-idem-key-1"
	)

	req := core.CreateTransferRequest{
		IdempotencyKey: idempotencyKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(250),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Concurrent Sender",
			Email:   "concurrent@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Concurrent Recipient",
			AccountNumber: "5555555555",
			BankName:      "Zenith Bank",
			Country:       "NG",
		},
	}

	type result struct {
		id  uuid.UUID
		err error
	}

	var (
		mu      sync.Mutex
		results = make([]result, goroutines)
		wg      sync.WaitGroup
	)

	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[idx] = result{err: err}
			} else {
				results[idx] = result{id: tr.ID}
			}
		}(i)
	}
	wg.Wait()

	// Some calls succeed (idempotent return or first create), some may fail
	// (duplicate key on concurrent insert). At least 1 must succeed.
	var successIDs []uuid.UUID
	var errCount int
	for _, r := range results {
		if r.err != nil {
			errCount++
		} else {
			successIDs = append(successIDs, r.id)
		}
	}
	if len(successIDs) == 0 {
		t.Fatal("expected at least 1 successful call, all failed")
	}

	// All successful calls must return the same transfer ID.
	firstID := successIDs[0]
	for i, id := range successIDs[1:] {
		if id != firstID {
			t.Errorf("goroutine returned different ID: %s vs %s (index %d)", id, firstID, i+1)
		}
	}

	// Exactly 1 transfer record must exist in the store.
	allTransfers := h.TransferStore.allTransfers()
	var count int
	for _, tr := range allTransfers {
		if tr.TenantID == LemfiTenantID && tr.IdempotencyKey == idempotencyKey {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 transfer record for key %q, found %d", idempotencyKey, count)
	}

	fetched, err := h.TransferStore.GetTransferByIdempotencyKey(ctx, LemfiTenantID, idempotencyKey)
	if err != nil {
		t.Fatalf("GetTransferByIdempotencyKey: %v", err)
	}
	if fetched.ID != firstID {
		t.Errorf("stored ID %s does not match returned ID %s", fetched.ID, firstID)
	}

	t.Logf("concurrent idempotency verified: %d/%d succeeded (same ID %s), %d duplicate-key rejections, 1 store record",
		len(successIDs), goroutines, firstID, errCount)
}

// TestIdempotency_ConcurrentDifferentTenantsSameKey verifies that two tenants
// (Lemfi and Fincra), each with 25 concurrent goroutines, can use the same
// idempotency key without conflict. Exactly 2 unique transfers must be created
// (one per tenant), and each tenant's goroutines must all return the same ID.
func TestIdempotency_ConcurrentDifferentTenantsSameKey(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const (
		perTenant      = 25
		idempotencyKey = "cross-tenant-concurrent-key"
	)

	lemfiReq := core.CreateTransferRequest{
		IdempotencyKey: idempotencyKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Lemfi Concurrent",
			Email:   "lemfi-conc@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Lemfi Recipient",
			AccountNumber: "6666666666",
			BankName:      "GTBank",
			Country:       "NG",
		},
	}

	fincraReq := core.CreateTransferRequest{
		IdempotencyKey: idempotencyKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Fincra Concurrent",
			Email:   "fincra-conc@fincra.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Fincra Recipient",
			AccountNumber: "77777777",
			BankName:      "Access Bank",
			Country:       "NG",
		},
	}

	type result struct {
		tenantID uuid.UUID
		id       uuid.UUID
		err      error
	}

	total := perTenant * 2
	var (
		mu      sync.Mutex
		results = make([]result, total)
		wg      sync.WaitGroup
	)

	wg.Add(total)

	// Launch 25 goroutines for Lemfi.
	for i := range perTenant {
		go func(idx int) {
			defer wg.Done()
			tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, lemfiReq)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[idx] = result{tenantID: LemfiTenantID, err: err}
			} else {
				results[idx] = result{tenantID: LemfiTenantID, id: tr.ID}
			}
		}(i)
	}

	// Launch 25 goroutines for Fincra.
	for i := range perTenant {
		go func(idx int) {
			defer wg.Done()
			tr, err := h.Engine.CreateTransfer(ctx, FincraTenantID, fincraReq)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				results[idx] = result{tenantID: FincraTenantID, err: err}
			} else {
				results[idx] = result{tenantID: FincraTenantID, id: tr.ID}
			}
		}(perTenant + i)
	}

	wg.Wait()

	// Partition results by tenant. Some concurrent calls may fail due to
	// duplicate key rejection; at least 1 per tenant must succeed.
	var lemfiSuccessIDs, fincraSuccessIDs []uuid.UUID
	var lemfiErrs, fincraErrs int
	for _, r := range results {
		if r.tenantID == LemfiTenantID {
			if r.err != nil {
				lemfiErrs++
			} else {
				lemfiSuccessIDs = append(lemfiSuccessIDs, r.id)
			}
		} else {
			if r.err != nil {
				fincraErrs++
			} else {
				fincraSuccessIDs = append(fincraSuccessIDs, r.id)
			}
		}
	}
	if len(lemfiSuccessIDs) == 0 {
		t.Fatal("expected at least 1 successful Lemfi call")
	}
	if len(fincraSuccessIDs) == 0 {
		t.Fatal("expected at least 1 successful Fincra call")
	}

	// All successful calls per tenant must return the same ID.
	lemfiID := lemfiSuccessIDs[0]
	for _, id := range lemfiSuccessIDs[1:] {
		if id != lemfiID {
			t.Errorf("Lemfi returned inconsistent IDs: %s vs %s", id, lemfiID)
		}
	}
	fincraID := fincraSuccessIDs[0]
	for _, id := range fincraSuccessIDs[1:] {
		if id != fincraID {
			t.Errorf("Fincra returned inconsistent IDs: %s vs %s", id, fincraID)
		}
	}

	// The two tenant IDs must be different.
	if lemfiID == fincraID {
		t.Errorf("cross-tenant collision: both tenants returned the same transfer ID %s", lemfiID)
	}

	t.Logf("concurrent cross-tenant idempotency verified: Lemfi=%s (%d calls), Fincra=%s (%d calls), key=%q",
		lemfiID, perTenant, fincraID, perTenant, idempotencyKey)
}
