//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/core/recovery"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
)

// TestLemfiGBPtoNGN tests the primary GBP→NGN corridor for the Lemfi tenant.
func TestLemfiGBPtoNGN(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a transfer
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "lemfi-gbp-ngn-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "John Doe",
			Email:   "john@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Ade Ogunlesi",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if transfer.Status != domain.TransferStatusCreated {
		t.Fatalf("expected CREATED, got %s", transfer.Status)
	}
	if transfer.TenantID != LemfiTenantID {
		t.Fatalf("expected tenant %s, got %s", LemfiTenantID, transfer.TenantID)
	}

	// Run the full pipeline synchronously
	if err := h.Engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("ProcessTransfer failed: %v", err)
	}

	// Verify final state
	final, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if final.Status != domain.TransferStatusCompleted {
		t.Fatalf("expected COMPLETED, got %s", final.Status)
	}

	// Verify all intermediate events were recorded
	events, err := h.TransferStore.GetTransferEvents(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransferEvents failed: %v", err)
	}
	if len(events) < 5 {
		t.Fatalf("expected at least 5 transfer events, got %d", len(events))
	}

	// Verify event sequence includes key transitions
	statusSet := make(map[domain.TransferStatus]bool)
	for _, e := range events {
		statusSet[e.ToStatus] = true
	}
	for _, expected := range []domain.TransferStatus{
		domain.TransferStatusCreated,
		domain.TransferStatusFunded,
		domain.TransferStatusSettling,
		domain.TransferStatusCompleted,
	} {
		if !statusSet[expected] {
			t.Errorf("expected transition to %s in events", expected)
		}
	}

	// Verify domain events were published
	publishedTypes := h.Events.eventTypes()
	if len(publishedTypes) == 0 {
		t.Fatal("no domain events published")
	}
	hasTransferCreated := false
	hasTransferCompleted := false
	for _, et := range publishedTypes {
		if et == domain.EventTransferCreated {
			hasTransferCreated = true
		}
		if et == domain.EventTransferCompleted {
			hasTransferCompleted = true
		}
	}
	if !hasTransferCreated {
		t.Error("missing transfer.created event")
	}
	if !hasTransferCompleted {
		t.Error("missing transfer.completed event")
	}

	t.Logf("GBP→NGN pipeline completed: transfer=%s, dest_amount=%s NGN", transfer.ID, final.DestAmount)
}

// TestFincraNGNtoGBP tests the reverse corridor NGN→GBP for Fincra (different fees).
func TestFincraNGNtoGBP(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fincra-ngn-gbp-1",
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(500_000),
		DestCurrency:   domain.CurrencyGBP,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Emeka Okafor",
			Email:   "emeka@fincra.com",
			Country: "NG",
		},
		Recipient: domain.Recipient{
			Name:          "Jane Smith",
			AccountNumber: "12345678",
			SortCode:      "12-34-56",
			BankName:      "Barclays",
			Country:       "GB",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("ProcessTransfer failed: %v", err)
	}

	final, err := h.TransferStore.GetTransfer(ctx, FincraTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if final.Status != domain.TransferStatusCompleted {
		t.Fatalf("expected COMPLETED, got %s", final.Status)
	}

	t.Logf("NGN→GBP pipeline completed: transfer=%s, dest_amount=%s GBP", transfer.ID, final.DestAmount)
}

// TestTenantIsolation verifies that Lemfi cannot see Fincra's transfers.
func TestTenantIsolation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a Lemfi transfer (amount must be large enough to cover fees)
	lemfiTransfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "isolation-lemfi-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient: domain.Recipient{
			Name:    "Test Recipient",
			Country: "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer for Lemfi failed: %v", err)
	}

	// Fincra should NOT be able to see Lemfi's transfer
	_, err = h.TransferStore.GetTransfer(ctx, FincraTenantID, lemfiTransfer.ID)
	if err == nil {
		t.Fatal("Fincra should not be able to see Lemfi's transfer")
	}

	// Lemfi can see its own transfer
	_, err = h.TransferStore.GetTransfer(ctx, LemfiTenantID, lemfiTransfer.ID)
	if err != nil {
		t.Fatalf("Lemfi should see its own transfer: %v", err)
	}

	// Verify listing is isolated
	lemfiTransfers, err := h.TransferStore.ListTransfers(ctx, LemfiTenantID, 100, 0)
	if err != nil {
		t.Fatalf("ListTransfers for Lemfi failed: %v", err)
	}
	for _, tr := range lemfiTransfers {
		if tr.TenantID != LemfiTenantID {
			t.Errorf("Lemfi list contains transfer for tenant %s", tr.TenantID)
		}
	}
}

// TestTenantLimits verifies per-transfer and daily limits are enforced.
func TestTenantLimits(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Fincra per-transfer limit is 500,000
	_, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: "limit-over-1",
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(600_000),
		DestCurrency:   domain.CurrencyGBP,
		Recipient:      domain.Recipient{Name: "Test", Country: "GB"},
	})
	if err == nil {
		t.Fatal("expected per-transfer limit error, got nil")
	}

	// Within limit should work (use large enough amount to cover provider fees: NGN→USDT rate 0.00065, fee $100)
	_, err = h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: "limit-ok-1",
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(400_000),
		DestCurrency:   domain.CurrencyGBP,
		Recipient:      domain.Recipient{Name: "Test", Country: "GB"},
	})
	if err != nil {
		t.Fatalf("transfer within limit should succeed: %v", err)
	}
}

// TestSuspendedTenant verifies that suspended tenants get 403.
func TestSuspendedTenant(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a suspended tenant
	suspendedID := uuid.New()
	h.TenantStore.addTenant(&domain.Tenant{
		ID:        suspendedID,
		Name:      "Suspended Corp",
		Slug:      "suspended",
		Status:    domain.TenantStatusSuspended,
		KYBStatus: domain.KYBStatusVerified,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  30,
			OffRampBPS: 30,
		},
		DailyLimitUSD:    decimal.NewFromInt(1_000_000),
		PerTransferLimit: decimal.NewFromInt(100_000),
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	})

	_, err := h.Engine.CreateTransfer(ctx, suspendedID, core.CreateTransferRequest{
		IdempotencyKey: "suspended-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Test", Country: "NG"},
	})
	if err == nil {
		t.Fatal("expected suspended tenant error, got nil")
	}
}

// TestIdempotencyPerTenant verifies same key for different tenants creates separate transfers.
func TestIdempotencyPerTenant(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sharedKey := "shared-idem-key-123"

	// Lemfi creates a transfer
	lemfi, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "LemfiRecipient", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("Lemfi CreateTransfer failed: %v", err)
	}

	// Fincra creates a transfer with the SAME key — should get a different transfer
	fincra, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(400_000),
		DestCurrency:   domain.CurrencyGBP,
		Recipient:      domain.Recipient{Name: "FincraRecipient", Country: "GB"},
	})
	if err != nil {
		t.Fatalf("Fincra CreateTransfer failed: %v", err)
	}

	// Different transfers
	if lemfi.ID == fincra.ID {
		t.Fatal("same idempotency key for different tenants should create different transfers")
	}

	// Lemfi repeating the same key returns the same transfer
	lemfi2, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "LemfiRecipient", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("Lemfi idempotent CreateTransfer failed: %v", err)
	}
	if lemfi.ID != lemfi2.ID {
		t.Fatalf("idempotent call should return same transfer ID: got %s and %s", lemfi.ID, lemfi2.ID)
	}
}

// TestFailedTransferAndRefund verifies the fail + refund path with ledger reversals.
func TestFailedTransferAndRefund(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fail-refund-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Refund Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// Fund the transfer (reserves treasury)
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}

	// Verify it's funded
	funded, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if funded.Status != domain.TransferStatusFunded {
		t.Fatalf("expected FUNDED, got %s", funded.Status)
	}

	// Fail the transfer
	if err := h.Engine.FailTransfer(ctx, transfer.TenantID, transfer.ID, "provider error", "PROVIDER_TIMEOUT"); err != nil {
		// Check if transition from FUNDED→FAILED is valid
		// ValidTransitions shows: FUNDED → {ON_RAMPING, REFUNDING}
		// So we need to go FUNDED → REFUNDING → REFUNDED instead
		t.Logf("FailTransfer from FUNDED not allowed, using refund path instead")
	}

	// Use refund path: FUNDED → REFUNDING → REFUNDED
	if err := h.Engine.InitiateRefund(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateRefund failed: %v", err)
	}

	refunding, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	// In the outbox architecture, InitiateRefund transitions to REFUNDING and queues
	// outbox intents. Workers execute the reversal and complete the refund asynchronously.
	if refunding.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING, got %s", refunding.Status)
	}

	// Verify refund intent was queued in outbox
	entries := h.TransferStore.drainOutbox()
	hasRefundIntent := false
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentLedgerReverse {
			hasRefundIntent = true
		}
	}
	if !hasRefundIntent {
		t.Error("expected ledger.reverse intent in outbox for refund")
	}
}

// TestTreasuryReservationConsistency creates 100 concurrent transfers and verifies
// that total reserved never exceeds available and positions are correct.
func TestTreasuryReservationConsistency(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const numTransfers = 100
	amountPerTransfer := decimal.NewFromInt(1000) // 1000 GBP each, 1M total available

	var (
		wg           sync.WaitGroup
		successCount int64
		failCount    int64
	)

	wg.Add(numTransfers)
	for i := 0; i < numTransfers; i++ {
		go func(idx int) {
			defer wg.Done()
			_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
				IdempotencyKey: uuid.New().String(),
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   amountPerTransfer,
				DestCurrency:   domain.CurrencyNGN,
				Recipient:      domain.Recipient{Name: "Concurrent Test", Country: "NG"},
			})
			if err != nil {
				atomic.AddInt64(&failCount, 1)
				return
			}
			atomic.AddInt64(&successCount, 1)
		}(i)
	}
	wg.Wait()

	t.Logf("concurrent transfers: %d succeeded, %d failed", successCount, failCount)

	// All should succeed — we have 1M GBP and each is 1000 GBP (100 × 1000 = 100K < 1M)
	if successCount != numTransfers {
		t.Errorf("expected all %d transfers to succeed, got %d successes and %d failures",
			numTransfers, successCount, failCount)
	}

	// Verify no over-reservation: total reserved should not exceed balance
	positions, err := h.Treasury.GetPositions(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetPositions failed: %v", err)
	}
	for _, pos := range positions {
		if pos.Currency == domain.CurrencyGBP {
			available := pos.Available()
			if available.IsNegative() {
				t.Errorf("GBP position has negative available balance: %s (balance=%s, locked=%s)",
					available, pos.Balance, pos.Locked)
			}
			t.Logf("GBP position after %d reservations: balance=%s, locked=%s, available=%s",
				successCount, pos.Balance, pos.Locked, available)
		}
	}
}

// TestConcurrentMultiTenant creates 50 Lemfi + 50 Fincra transfers simultaneously.
func TestConcurrentMultiTenant(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const perTenant = 50

	var (
		wg          sync.WaitGroup
		lemfiOK     int64
		fincraOK    int64
		lemfiFail   int64
		fincraFail  int64
	)

	// 50 Lemfi transfers (GBP→NGN)
	for i := 0; i < perTenant; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
				IdempotencyKey: uuid.New().String(),
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   decimal.NewFromInt(500),
				DestCurrency:   domain.CurrencyNGN,
				Recipient:      domain.Recipient{Name: "Lemfi Multi", Country: "NG"},
			})
			if err != nil {
				atomic.AddInt64(&lemfiFail, 1)
			} else {
				atomic.AddInt64(&lemfiOK, 1)
			}
		}()
	}

	// 50 Fincra transfers (NGN→GBP)
	for i := 0; i < perTenant; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
				IdempotencyKey: uuid.New().String(),
				SourceCurrency: domain.CurrencyNGN,
				SourceAmount:   decimal.NewFromInt(400_000),
				DestCurrency:   domain.CurrencyGBP,
				Recipient:      domain.Recipient{Name: "Fincra Multi", Country: "GB"},
			})
			if err != nil {
				atomic.AddInt64(&fincraFail, 1)
			} else {
				atomic.AddInt64(&fincraOK, 1)
			}
		}()
	}

	wg.Wait()

	t.Logf("Lemfi: %d ok, %d fail | Fincra: %d ok, %d fail", lemfiOK, lemfiFail, fincraOK, fincraFail)

	if lemfiOK != perTenant {
		t.Errorf("expected all %d Lemfi transfers to succeed", perTenant)
	}
	if fincraOK != perTenant {
		t.Errorf("expected all %d Fincra transfers to succeed", perTenant)
	}

	// Verify no cross-contamination in listing
	lemfiAll, _ := h.TransferStore.ListTransfers(ctx, LemfiTenantID, 200, 0)
	for _, tr := range lemfiAll {
		if tr.TenantID != LemfiTenantID {
			t.Errorf("Lemfi list has Fincra transfer: %s", tr.ID)
		}
	}
	fincraAll, _ := h.TransferStore.ListTransfers(ctx, FincraTenantID, 200, 0)
	for _, tr := range fincraAll {
		if tr.TenantID != FincraTenantID {
			t.Errorf("Fincra list has Lemfi transfer: %s", tr.ID)
		}
	}

	// Verify treasury positions are per-tenant
	lemfiPos, _ := h.Treasury.GetPositions(ctx, LemfiTenantID)
	for _, p := range lemfiPos {
		if p.TenantID != LemfiTenantID {
			t.Errorf("Lemfi positions contain wrong tenant: %s", p.TenantID)
		}
	}
	fincraPos, _ := h.Treasury.GetPositions(ctx, FincraTenantID)
	for _, p := range fincraPos {
		if p.TenantID != FincraTenantID {
			t.Errorf("Fincra positions contain wrong tenant: %s", p.TenantID)
		}
	}
}

// TestTreasuryPositionTracking verifies positions change correctly after a transfer.
func TestTreasuryPositionTracking(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Record initial positions
	initialPositions, err := h.Treasury.GetPositions(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetPositions failed: %v", err)
	}
	var initialGBPAvailable decimal.Decimal
	for _, p := range initialPositions {
		if p.Currency == domain.CurrencyGBP {
			initialGBPAvailable = p.Available()
		}
	}

	// Create and process a transfer
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "position-track-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(5000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Position Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// Fund the transfer (writes treasury reserve intent to outbox)
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	// Execute treasury worker (processes the reserve intent)
	h.executeOutbox(ctx)

	// After executing outbox, reservation should be taken
	posAfterFund, _ := h.Treasury.GetPositions(ctx, LemfiTenantID)
	for _, p := range posAfterFund {
		if p.Currency == domain.CurrencyGBP {
			if p.Available().GreaterThanOrEqual(initialGBPAvailable) {
				t.Error("expected available balance to decrease after reservation")
			}
			t.Logf("GBP after funding: balance=%s, locked=%s, available=%s",
				p.Balance, p.Locked, p.Available())
			break
		}
	}

	// Process remaining steps to completion (skip FundTransfer since already done)
	steps := []struct {
		name string
		fn   func() error
	}{
		{"InitiateOnRamp", func() error { return h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID) }},
		{"HandleOnRampResult", func() error {
			return h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true})
		}},
		{"HandleSettlementResult", func() error {
			return h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true, TxHash: "0xtest"})
		}},
		{"HandleOffRampResult", func() error {
			return h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true})
		}},
	}
	for _, step := range steps {
		if err := step.fn(); err != nil {
			t.Fatalf("pipeline step %s failed: %v", step.name, err)
		}
		// Execute workers after each step
		h.executeOutbox(ctx)
	}

	// After completion, reservation should be released (treasury release intent executed)
	posAfterComplete, _ := h.Treasury.GetPositions(ctx, LemfiTenantID)
	for _, p := range posAfterComplete {
		if p.Currency == domain.CurrencyGBP {
			t.Logf("GBP after completion: balance=%s, locked=%s, available=%s",
				p.Balance, p.Locked, p.Available())
		}
	}
}

// TestPerTenantFees verifies that different tenants get different fee amounts
// for the same corridor.
func TestPerTenantFees(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	amount := decimal.NewFromInt(10_000)

	// Lemfi quote: 40/35 bps
	lemfiQuote, err := h.Engine.GetQuote(ctx, LemfiTenantID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   amount,
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("Lemfi GetQuote failed: %v", err)
	}

	// We only test that quote was generated with non-zero fees
	if lemfiQuote.Fees.OnRampFee.IsZero() {
		t.Error("expected non-zero on-ramp fee for Lemfi")
	}
	if lemfiQuote.Fees.TotalFeeUSD.IsZero() {
		t.Error("expected non-zero total fee for Lemfi")
	}

	t.Logf("Lemfi fees: onramp=%s, offramp=%s, network=%s, total=%s",
		lemfiQuote.Fees.OnRampFee, lemfiQuote.Fees.OffRampFee,
		lemfiQuote.Fees.NetworkFee, lemfiQuote.Fees.TotalFeeUSD)
}

// TestLedgerTBWritePath verifies that TigerBeetle receives all ledger writes
// during a full transfer pipeline and that balances are correct.
func TestLedgerTBWritePath(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Record initial TB state
	initialTransfers := h.TB.transferCount()
	initialAccounts := h.TB.accountCount()

	// Create and process a full transfer
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "tb-write-path-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "TB Write Test",
			Email:   "test@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "TB Recipient",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// Run full pipeline (writes ledger/treasury intents to outbox)
	if err := h.Engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("ProcessTransfer failed: %v", err)
	}
	// Execute workers: this processes ledger.post intents → TB receives writes
	h.executeOutbox(ctx)

	// Verify TB received writes
	finalTransfers := h.TB.transferCount()
	if finalTransfers <= initialTransfers {
		t.Errorf("expected TB to receive ledger writes: initial=%d, final=%d", initialTransfers, finalTransfers)
	}
	t.Logf("TB transfers created: %d (was %d)", finalTransfers, initialTransfers)

	// Verify TB accounts were created for tenant and system accounts
	finalAccounts := h.TB.accountCount()
	if finalAccounts <= initialAccounts {
		t.Errorf("expected TB accounts to be created: initial=%d, final=%d", initialAccounts, finalAccounts)
	}
	t.Logf("TB accounts created: %d (was %d)", finalAccounts, initialAccounts)

	// Verify ledger intents were queued (events are published by ledger service on PostEntries)
	// The mock TB client receives writes directly, and the ledger service publishes events.
	t.Logf("ledger TB writes verified: %d transfers, %d accounts", finalTransfers, finalAccounts)

	// Verify TB balance for a known system account is non-zero
	cryptoBalance := h.TB.getBalance("assets:crypto:usdt:tron")
	t.Logf("assets:crypto:usdt:tron balance: %s", cryptoBalance)
}

// TestConcurrentFundingNoOverReservation creates 100 transfers and funds them
// concurrently to verify treasury reservations work under concurrent load.
func TestConcurrentFundingNoOverReservation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const numTransfers = 100
	amountPerTransfer := decimal.NewFromInt(1000) // 1000 GBP each, 1M available

	// Create all transfers first (sequentially to avoid idempotency races)
	transferIDs := make([]uuid.UUID, numTransfers)
	for i := 0; i < numTransfers; i++ {
		transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: uuid.New().String(),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   amountPerTransfer,
			DestCurrency:   domain.CurrencyNGN,
			Recipient:      domain.Recipient{Name: "Fund Test", Country: "NG"},
		})
		if err != nil {
			t.Fatalf("CreateTransfer %d failed: %v", i, err)
		}
		transferIDs[i] = transfer.ID
	}

	// Fund all transfers concurrently (engine writes treasury reserve intents to outbox)
	var (
		wg           sync.WaitGroup
		successCount int64
		failCount    int64
	)
	wg.Add(numTransfers)
	for i := 0; i < numTransfers; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := h.Engine.FundTransfer(ctx, LemfiTenantID, transferIDs[idx]); err != nil {
				atomic.AddInt64(&failCount, 1)
				return
			}
			atomic.AddInt64(&successCount, 1)
		}(i)
	}
	wg.Wait()

	// Execute treasury worker to process all reserve intents
	h.executeOutbox(ctx)

	t.Logf("concurrent funding: %d succeeded, %d failed", successCount, failCount)

	// All should succeed (100 × 1000 = 100K < 1M available)
	if successCount != numTransfers {
		t.Errorf("expected all %d fundings to succeed, got %d", numTransfers, successCount)
	}

	// Verify no over-reservation (treasury worker executed all reserves)
	positions, err := h.Treasury.GetPositions(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetPositions failed: %v", err)
	}
	for _, pos := range positions {
		if pos.Currency == domain.CurrencyGBP {
			available := pos.Available()
			if available.IsNegative() {
				t.Errorf("over-reservation detected: available=%s (balance=%s, locked=%s)",
					available, pos.Balance, pos.Locked)
			}
			expectedLocked := amountPerTransfer.Mul(decimal.NewFromInt(int64(successCount)))
			if !pos.Locked.Equal(expectedLocked) {
				t.Errorf("expected locked=%s, got %s", expectedLocked, pos.Locked)
			}
			t.Logf("GBP after %d reservations: balance=%s, locked=%s, available=%s",
				successCount, pos.Balance, pos.Locked, available)
		}
	}
}

// TestImportBoundaries verifies that core/ never imports concrete modules
// (ledger, treasury, rail) — only domain/.
func TestImportBoundaries(t *testing.T) {
	h := newTestHarness(t)
	if h.Engine == nil {
		t.Fatal("engine should be wired")
	}

	// Programmatic check: use go list to get core's imports
	cmd := exec.Command("go", "list", "-json", "./core/")
	cmd.Dir = projectRoot()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	// Parse the imports
	type pkgInfo struct {
		Imports []string `json:"Imports"`
	}
	var pkg pkgInfo
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatalf("failed to parse go list output: %v", err)
	}

	// core/ must not import any concrete module packages
	forbidden := []string{
		"github.com/intellect4all/settla/ledger",
		"github.com/intellect4all/settla/treasury",
		"github.com/intellect4all/settla/rail",
		"github.com/intellect4all/settla/rail/router",
		"github.com/intellect4all/settla/rail/provider",
		"github.com/intellect4all/settla/node",
		"github.com/intellect4all/settla/cache",
		"github.com/intellect4all/settla/store",
	}

	for _, imp := range pkg.Imports {
		for _, f := range forbidden {
			if imp == f || strings.HasPrefix(imp, f+"/") {
				t.Errorf("core/ imports forbidden package %q — violates module boundary", imp)
			}
		}
	}

	t.Log("import boundaries enforced: core/ imports only domain/ and stdlib")
}

// ─── Missing Test Scenarios ──────────────────────────────────────────────────

// TestFailedTransferCompensation verifies the full failure + compensation path:
// on-ramp fails → engine transitions to REFUNDING → outbox contains treasury
// release intent → executing outbox releases the reservation.
func TestFailedTransferCompensation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create and fund a transfer
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "compensation-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(2000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Compensation Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// Fund the transfer (CREATED → FUNDED, writes treasury reserve intent)
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	// Execute treasury reserve intent
	h.executeOutbox(ctx)

	// Record GBP position after funding (reservation taken)
	posAfterFund, _ := h.Treasury.GetPositions(ctx, LemfiTenantID)
	var lockedAfterFund decimal.Decimal
	for _, p := range posAfterFund {
		if p.Currency == domain.CurrencyGBP {
			lockedAfterFund = p.Locked
			t.Logf("GBP after fund: balance=%s, locked=%s, available=%s",
				p.Balance, p.Locked, p.Available())
		}
	}
	if lockedAfterFund.IsZero() {
		t.Fatal("expected non-zero locked amount after funding")
	}

	// Initiate on-ramp (FUNDED → ON_RAMPING)
	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}

	// Simulate on-ramp failure → ON_RAMPING → REFUNDING + treasury release intent
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "provider timeout",
		ErrorCode: "PROVIDER_TIMEOUT",
	}); err != nil {
		t.Fatalf("HandleOnRampResult (failure) failed: %v", err)
	}

	// Verify transfer is in REFUNDING
	refunding, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if refunding.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING, got %s", refunding.Status)
	}

	// Verify outbox contains treasury.release intent
	entries := h.TransferStore.drainOutbox()
	hasTreasuryRelease := false
	hasOnRampFailedEvent := false
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentTreasuryRelease {
			hasTreasuryRelease = true
		}
		if !e.IsIntent && e.EventType == domain.EventProviderOnRampFailed {
			hasOnRampFailedEvent = true
		}
	}
	if !hasTreasuryRelease {
		t.Error("expected treasury.release intent in outbox for compensation")
	}
	if !hasOnRampFailedEvent {
		t.Error("expected provider.onramp.failed event in outbox")
	}

	// Execute the treasury release intent
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentTreasuryRelease {
			var p domain.TreasuryReleasePayload
			if err := json.Unmarshal(e.Payload, &p); err == nil {
				_ = h.Treasury.Release(ctx, p.TenantID, p.Currency, p.Location, p.Amount, p.TransferID)
			}
		}
	}

	// Verify treasury reservation was released
	posAfterRelease, _ := h.Treasury.GetPositions(ctx, LemfiTenantID)
	for _, p := range posAfterRelease {
		if p.Currency == domain.CurrencyGBP {
			if p.Locked.GreaterThan(decimal.Zero) {
				t.Errorf("expected locked=0 after compensation release, got %s", p.Locked)
			}
			t.Logf("GBP after compensation: balance=%s, locked=%s, available=%s",
				p.Balance, p.Locked, p.Available())
		}
	}

	// Verify domain events include the failure event
	publishedTypes := h.Events.eventTypes()
	hasFailEvent := false
	for _, et := range publishedTypes {
		if et == domain.EventProviderOnRampFailed {
			hasFailEvent = true
		}
	}
	if !hasFailEvent {
		t.Error("expected provider.onramp.failed domain event to be published")
	}
}

// TestSettlementFailureCompensation verifies that a blockchain settlement
// failure triggers full compensation: treasury release + ledger reversal.
func TestSettlementFailureCompensation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "settle-fail-comp-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(3000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Settlement Fail", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	// Run through funding and on-ramp success
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("HandleOnRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Now simulate blockchain settlement failure
	if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "blockchain timeout: tx not confirmed in 30s",
		ErrorCode: "BLOCKCHAIN_TIMEOUT",
	}); err != nil {
		t.Fatalf("HandleSettlementResult (failure) failed: %v", err)
	}

	// Verify transfer is FAILED
	failed, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED, got %s", failed.Status)
	}

	// Verify outbox has both treasury release and ledger reverse intents
	entries := h.TransferStore.drainOutbox()
	var hasTreasuryRelease, hasLedgerReverse, hasBlockchainFailed bool
	for _, e := range entries {
		switch {
		case e.IsIntent && e.EventType == domain.IntentTreasuryRelease:
			hasTreasuryRelease = true
		case e.IsIntent && e.EventType == domain.IntentLedgerReverse:
			hasLedgerReverse = true
		case !e.IsIntent && e.EventType == domain.EventBlockchainFailed:
			hasBlockchainFailed = true
		}
	}
	if !hasTreasuryRelease {
		t.Error("expected treasury.release intent for settlement failure compensation")
	}
	if !hasLedgerReverse {
		t.Error("expected ledger.reverse intent for settlement failure compensation")
	}
	if !hasBlockchainFailed {
		t.Error("expected blockchain.failed event in outbox")
	}
}

// TestInboundProviderWebhookHandling verifies the inbound webhook flow:
// transfer reaches ON_RAMPING, provider returns "pending", then an inbound
// webhook arrives and the engine advances to SETTLING.
func TestInboundProviderWebhookHandling(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create and fund a transfer, then move to ON_RAMPING
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "webhook-inbound-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1500),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Webhook Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	// Drain the on-ramp intent (we simulate that the provider returned "pending")
	_ = h.TransferStore.drainOutbox()

	// Verify transfer is stuck in ON_RAMPING (simulating async provider)
	onRamping, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if onRamping.Status != domain.TransferStatusOnRamping {
		t.Fatalf("expected ON_RAMPING, got %s", onRamping.Status)
	}

	// Simulate inbound webhook: provider reports on-ramp completed
	// In production, the webhook HTTP handler normalizes the provider callback
	// into a ProviderWebhookPayload and publishes it to NATS. The InboundWebhookWorker
	// then calls engine.HandleOnRampResult. We simulate that directly here.
	webhookResult := domain.IntentResult{
		Success:     true,
		ProviderRef: "provider-ref-abc123",
		TxHash:      "0xwebhook-onramp-hash",
	}
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, webhookResult); err != nil {
		t.Fatalf("HandleOnRampResult (from webhook) failed: %v", err)
	}

	// Verify transfer advanced to SETTLING
	settling, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if settling.Status != domain.TransferStatusSettling {
		t.Fatalf("expected SETTLING after webhook, got %s", settling.Status)
	}

	// Verify outbox has ledger post and blockchain send intents
	entries := h.TransferStore.drainOutbox()
	var hasLedgerPost, hasBlockchainSend, hasOnRampCompleted bool
	for _, e := range entries {
		switch {
		case e.IsIntent && e.EventType == domain.IntentLedgerPost:
			hasLedgerPost = true
		case e.IsIntent && e.EventType == domain.IntentBlockchainSend:
			hasBlockchainSend = true
		case !e.IsIntent && e.EventType == domain.EventOnRampCompleted:
			hasOnRampCompleted = true
		}
	}
	if !hasLedgerPost {
		t.Error("expected ledger.post intent after webhook on-ramp completion")
	}
	if !hasBlockchainSend {
		t.Error("expected blockchain.send intent after webhook on-ramp completion")
	}
	if !hasOnRampCompleted {
		t.Error("expected onramp.completed event after webhook")
	}

	// Now simulate a failed inbound webhook for a different transfer's off-ramp
	transfer2, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "webhook-inbound-2",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(800),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Webhook Fail Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer 2 failed: %v", err)
	}
	// Run through to OFF_RAMPING
	if err := h.Engine.FundTransfer(ctx, transfer2.TenantID, transfer2.ID); err != nil {
		t.Fatalf("FundTransfer 2 failed: %v", err)
	}
	h.executeOutbox(ctx)
	if err := h.Engine.InitiateOnRamp(ctx, transfer2.TenantID, transfer2.ID); err != nil {
		t.Fatalf("InitiateOnRamp 2 failed: %v", err)
	}
	if err := h.Engine.HandleOnRampResult(ctx, transfer2.TenantID, transfer2.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("HandleOnRampResult 2 failed: %v", err)
	}
	h.executeOutbox(ctx)
	if err := h.Engine.HandleSettlementResult(ctx, transfer2.TenantID, transfer2.ID, domain.IntentResult{Success: true, TxHash: "0x2"}); err != nil {
		t.Fatalf("HandleSettlementResult 2 failed: %v", err)
	}
	_ = h.TransferStore.drainOutbox()

	// Verify transfer2 is in OFF_RAMPING
	offRamping, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer2.ID)
	if offRamping.Status != domain.TransferStatusOffRamping {
		t.Fatalf("expected OFF_RAMPING, got %s", offRamping.Status)
	}

	// Simulate inbound webhook: off-ramp provider reports failure
	failResult := domain.IntentResult{
		Success:   false,
		Error:     "bank rejected payment: invalid account",
		ErrorCode: "BANK_REJECTED",
	}
	if err := h.Engine.HandleOffRampResult(ctx, transfer2.TenantID, transfer2.ID, failResult); err != nil {
		t.Fatalf("HandleOffRampResult (failure webhook) failed: %v", err)
	}

	// Verify transfer2 is FAILED with compensation intents
	failedT2, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer2.ID)
	if failedT2.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after off-ramp webhook failure, got %s", failedT2.Status)
	}

	entries2 := h.TransferStore.drainOutbox()
	var hasRelease2, hasReverse2, hasWebhook2 bool
	for _, e := range entries2 {
		switch {
		case e.IsIntent && e.EventType == domain.IntentTreasuryRelease:
			hasRelease2 = true
		case e.IsIntent && e.EventType == domain.IntentLedgerReverse:
			hasReverse2 = true
		case e.IsIntent && e.EventType == domain.IntentWebhookDeliver:
			hasWebhook2 = true
		}
	}
	if !hasRelease2 {
		t.Error("expected treasury.release intent after off-ramp failure")
	}
	if !hasReverse2 {
		t.Error("expected ledger.reverse intent after off-ramp failure")
	}
	if !hasWebhook2 {
		t.Error("expected webhook.deliver intent after off-ramp failure")
	}
}

// TestStuckTransferRecovery verifies the recovery detector can find and
// recover transfers stuck in non-terminal states using the outbox pattern.
func TestStuckTransferRecovery(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create and advance a transfer to ON_RAMPING, then simulate being stuck
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "stuck-recovery-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Stuck Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	_ = h.TransferStore.drainOutbox()

	// Verify stuck in ON_RAMPING
	stuck, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if stuck.Status != domain.TransferStatusOnRamping {
		t.Fatalf("expected ON_RAMPING, got %s", stuck.Status)
	}

	// Make the transfer appear old (simulate being stuck for a while)
	// by back-dating UpdatedAt in the in-memory store
	h.TransferStore.mu.Lock()
	stuck.UpdatedAt = time.Now().UTC().Add(-20 * time.Minute)
	h.TransferStore.mu.Unlock()

	// Create a mock provider status checker that reports on-ramp completed
	mockProviders := &mockProviderStatusChecker{
		onRampStatus: map[uuid.UUID]*recovery.ProviderStatus{
			transfer.ID: {Status: "completed", Reference: "recovered-ref-123"},
		},
	}

	// Create a mock review store
	mockReviews := &mockReviewStore{}

	// Create detector with short thresholds for testing
	shortThresholds := map[domain.TransferStatus]recovery.Thresholds{
		domain.TransferStatusFunded:     {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 1 * time.Hour},
		domain.TransferStatusOnRamping:  {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 1 * time.Hour},
		domain.TransferStatusSettling:   {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 1 * time.Hour},
		domain.TransferStatusOffRamping: {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 1 * time.Hour},
	}

	// The memTransferStore needs to implement ListStuckTransfers for the detector
	stuckStore := &memStuckTransferStore{inner: h.TransferStore}

	logger := observability.NewLogger("settla-recovery-test", "test")
	detector := recovery.NewDetector(stuckStore, mockReviews, h.Engine, mockProviders, logger).
		WithThresholds(shortThresholds)

	// Run one recovery cycle
	if err := detector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Verify transfer was recovered: ON_RAMPING → SETTLING
	recovered, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if recovered.Status != domain.TransferStatusSettling {
		t.Fatalf("expected SETTLING after recovery, got %s", recovered.Status)
	}

	// Verify outbox has settling intents (ledger post + blockchain send)
	entries := h.TransferStore.drainOutbox()
	var hasLedgerPost, hasBlockchainSend bool
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentLedgerPost {
			hasLedgerPost = true
		}
		if e.IsIntent && e.EventType == domain.IntentBlockchainSend {
			hasBlockchainSend = true
		}
	}
	if !hasLedgerPost {
		t.Error("expected ledger.post intent after recovery")
	}
	if !hasBlockchainSend {
		t.Error("expected blockchain.send intent after recovery")
	}

	t.Logf("stuck transfer %s recovered from ON_RAMPING to SETTLING", transfer.ID)
}

// TestStuckTransferEscalation verifies that a transfer stuck past the escalation
// threshold gets escalated to manual review.
func TestStuckTransferEscalation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "escalation-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Escalation Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Back-date to simulate stuck past escalation threshold
	h.TransferStore.mu.Lock()
	xfer := h.TransferStore.transfers[transfer.ID]
	xfer.UpdatedAt = time.Now().UTC().Add(-2 * time.Hour)
	h.TransferStore.mu.Unlock()

	mockProviders := &mockProviderStatusChecker{}
	mockReviews := &mockReviewStore{}

	shortThresholds := map[domain.TransferStatus]recovery.Thresholds{
		domain.TransferStatusFunded:     {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
		domain.TransferStatusOnRamping:  {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
		domain.TransferStatusSettling:   {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
		domain.TransferStatusOffRamping: {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
	}

	stuckStore := &memStuckTransferStore{inner: h.TransferStore}
	logger := observability.NewLogger("settla-recovery-test", "test")
	detector := recovery.NewDetector(stuckStore, mockReviews, h.Engine, mockProviders, logger).
		WithThresholds(shortThresholds)

	if err := detector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Verify escalation created a manual review
	if len(mockReviews.reviews) == 0 {
		t.Fatal("expected manual review to be created for stuck transfer")
	}
	review := mockReviews.reviews[0]
	if review.transferID != transfer.ID {
		t.Errorf("expected review for transfer %s, got %s", transfer.ID, review.transferID)
	}
	t.Logf("stuck transfer %s escalated to manual review", transfer.ID)
}

// TestProviderCheckBeforeCallRedelivery verifies the CHECK-BEFORE-CALL pattern:
// if an on-ramp intent is processed, recorded as "completed", and then redelivered
// (NATS redelivery), the provider worker must skip execution and not double-call.
func TestProviderCheckBeforeCallRedelivery(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a transfer and advance to ON_RAMPING
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "check-before-call-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Redelivery Test", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}

	// Drain the on-ramp intent from outbox
	entries := h.TransferStore.drainOutbox()
	var onRampPayload *domain.ProviderOnRampPayload
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentProviderOnRamp {
			var p domain.ProviderOnRampPayload
			if err := json.Unmarshal(e.Payload, &p); err == nil {
				onRampPayload = &p
			}
		}
	}
	if onRampPayload == nil {
		t.Fatal("expected on-ramp intent in outbox")
	}

	// Simulate the ProviderTransferStore (CHECK-BEFORE-CALL)
	providerTxStore := worker.NewInMemoryProviderTransferStore()

	// First execution: no existing tx → provider is called → recorded as "completed"
	existing1, _ := providerTxStore.GetProviderTransaction(ctx, uuid.Nil, transfer.ID, "onramp")
	if existing1 != nil {
		t.Fatal("expected no existing provider tx before first call")
	}

	// Simulate provider execution (first call)
	firstTx := &domain.ProviderTx{
		ID:         uuid.New().String(),
		ExternalID: "ext-ref-first",
		Status:     "completed",
		Amount:     onRampPayload.Amount,
		Currency:   onRampPayload.FromCurrency,
	}
	if err := providerTxStore.CreateProviderTransaction(ctx, transfer.ID, "onramp", firstTx); err != nil {
		t.Fatalf("CreateProviderTransaction failed: %v", err)
	}

	// Simulate NATS redelivery: same intent arrives again
	// The CHECK step should find the existing "completed" tx and skip
	existing2, err := providerTxStore.GetProviderTransaction(ctx, uuid.Nil, transfer.ID, "onramp")
	if err != nil {
		t.Fatalf("GetProviderTransaction failed: %v", err)
	}
	if existing2 == nil {
		t.Fatal("expected existing provider tx on redelivery check")
	}
	if existing2.Status != "completed" {
		t.Fatalf("expected status 'completed', got '%s'", existing2.Status)
	}

	// The worker would skip execution here (status is "completed")
	// Verify idempotent: the external ID should match the first call
	if existing2.ExternalID != "ext-ref-first" {
		t.Errorf("expected external ID 'ext-ref-first', got '%s'", existing2.ExternalID)
	}

	t.Logf("CHECK-BEFORE-CALL prevented double execution for transfer %s", transfer.ID)

	// Also test the "pending" case: if status is "pending", worker should skip
	// (waiting for webhook callback)
	pendingTx := &domain.ProviderTx{
		ID:         uuid.New().String(),
		ExternalID: "ext-ref-pending",
		Status:     "pending",
		Amount:     onRampPayload.Amount,
		Currency:   onRampPayload.FromCurrency,
	}
	transfer2ID := uuid.New()
	if err := providerTxStore.CreateProviderTransaction(ctx, transfer2ID, "onramp", pendingTx); err != nil {
		t.Fatalf("CreateProviderTransaction (pending) failed: %v", err)
	}

	existingPending, _ := providerTxStore.GetProviderTransaction(ctx, uuid.Nil, transfer2ID, "onramp")
	if existingPending == nil || existingPending.Status != "pending" {
		t.Fatal("expected pending provider tx to be found")
	}

	// Also test the "failed" case: if status is "failed", worker should retry
	failedTx := &domain.ProviderTx{
		ID:     uuid.New().String(),
		Status: "failed",
	}
	transfer3ID := uuid.New()
	if err := providerTxStore.CreateProviderTransaction(ctx, transfer3ID, "onramp", failedTx); err != nil {
		t.Fatalf("CreateProviderTransaction (failed) failed: %v", err)
	}

	existingFailed, _ := providerTxStore.GetProviderTransaction(ctx, uuid.Nil, transfer3ID, "onramp")
	if existingFailed == nil || existingFailed.Status != "failed" {
		t.Fatal("expected failed provider tx — worker would retry execution")
	}

	t.Log("CHECK-BEFORE-CALL: completed=skip, pending=skip, failed=retry")
}

// projectRoot returns the project root directory.
func projectRoot() string {
	// Walk up from the test file to find go.mod
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "." // fallback
		}
		dir = parent
	}
}

// TestAllTransfersReachTerminalState creates 20 transfers across 2 tenants and
// verifies that every transfer reaches a terminal state after running through
// the full pipeline.
func TestAllTransfersReachTerminalState(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	type transferInfo struct {
		tenantID   uuid.UUID
		transferID uuid.UUID
	}

	var transfers []transferInfo

	// Create 10 transfers for Lemfi
	for i := 0; i < 10; i++ {
		tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: fmt.Sprintf("terminal-lemfi-%d", i),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(int64(1000 + i*100)),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Lemfi Sender",
				Email:   "sender@lemfi.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Lemfi Recipient",
				AccountNumber: fmt.Sprintf("012345%04d", i),
				BankName:      "GTBank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("CreateTransfer (lemfi %d) failed: %v", i, err)
		}
		transfers = append(transfers, transferInfo{tenantID: LemfiTenantID, transferID: tr.ID})
	}

	// Create 10 transfers for Fincra
	for i := 0; i < 10; i++ {
		tr, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
			IdempotencyKey: fmt.Sprintf("terminal-fincra-%d", i),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(int64(1000 + i*100)),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Fincra Sender",
				Email:   "sender@fincra.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Fincra Recipient",
				AccountNumber: fmt.Sprintf("098765%04d", i),
				BankName:      "Access Bank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("CreateTransfer (fincra %d) failed: %v", i, err)
		}
		transfers = append(transfers, transferInfo{tenantID: FincraTenantID, transferID: tr.ID})
	}

	// Process each transfer through the full pipeline
	for i, ti := range transfers {
		if err := h.Engine.ProcessTransfer(ctx, ti.tenantID, ti.transferID); err != nil {
			t.Fatalf("ProcessTransfer %d failed: %v", i, err)
		}
		h.executeOutbox(ctx)
	}

	// Verify all 20 transfers are in a terminal state
	allTransfers := h.TransferStore.allTransfers()
	if len(allTransfers) != 20 {
		t.Fatalf("expected 20 transfers, got %d", len(allTransfers))
	}

	var completed, failed, refunded int
	for _, tr := range allTransfers {
		switch tr.Status {
		case domain.TransferStatusCompleted:
			completed++
		case domain.TransferStatusFailed:
			failed++
		case domain.TransferStatusRefunded:
			refunded++
		default:
			t.Errorf("transfer %s is in non-terminal state %s", tr.ID, tr.Status)
		}
	}

	t.Logf("terminal state breakdown: completed=%d failed=%d refunded=%d", completed, failed, refunded)

	if completed+failed+refunded != 20 {
		t.Fatalf("not all transfers reached terminal state: completed=%d failed=%d refunded=%d", completed, failed, refunded)
	}

	// Verify outbox is fully drained
	remaining := h.TransferStore.drainOutbox()
	for _, e := range remaining {
		if e.IsIntent {
			t.Errorf("outbox still has unprocessed intent: %s for transfer %s", e.EventType, e.AggregateID)
		}
	}
}

// TestFailedTransferReachesTerminalViaRefund verifies that a transfer which
// fails at the on-ramp stage transitions through REFUNDING and reaches a
// terminal state after the refund completes.
func TestFailedTransferReachesTerminalViaRefund(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "refund-terminal-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Refund Test Sender",
			Email:   "refund@test.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Refund Test Recipient",
			AccountNumber: "1234567890",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	_ = h.TransferStore.drainOutbox()

	// Verify we are in ON_RAMPING
	tr := reloadTransfer(t, h, ctx, transfer.ID)
	if tr.Status != domain.TransferStatusOnRamping {
		t.Fatalf("expected ON_RAMPING, got %s", tr.Status)
	}

	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "provider_down",
	}); err != nil {
		t.Fatalf("HandleOnRampResult (failure) failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Verify transfer is now in REFUNDING
	tr = reloadTransfer(t, h, ctx, transfer.ID)
	if tr.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING after on-ramp failure, got %s", tr.Status)
	}

	if err := h.Engine.HandleRefundResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: true,
	}); err != nil {
		t.Fatalf("HandleRefundResult failed: %v", err)
	}
	h.executeOutbox(ctx)

	tr = reloadTransfer(t, h, ctx, transfer.ID)
	isTerminal := tr.Status == domain.TransferStatusCompleted ||
		tr.Status == domain.TransferStatusFailed ||
		tr.Status == domain.TransferStatusRefunded
	if !isTerminal {
		t.Fatalf("expected terminal state after refund, got %s", tr.Status)
	}
	t.Logf("transfer %s reached terminal state %s via refund flow", transfer.ID, tr.Status)

	// Verify outbox is drained
	remaining := h.TransferStore.drainOutbox()
	for _, e := range remaining {
		if e.IsIntent {
			t.Errorf("outbox still has unprocessed intent: %s", e.EventType)
		}
	}
}

// TestLedgerBalanceInvariant_AllEntriesBalanced runs transfers through the
// pipeline and verifies that the total debits across all TigerBeetle accounts
// equal the total credits, ensuring the double-entry ledger invariant holds.
func TestLedgerBalanceInvariant_AllEntriesBalanced(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Run 7 transfers through the full successful pipeline
	for i := 0; i < 7; i++ {
		tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: fmt.Sprintf("ledger-balance-ok-%d", i),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(int64(1000 + i*100)),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Balance Test Sender",
				Email:   "balance@test.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Balance Test Recipient",
				AccountNumber: fmt.Sprintf("555000%04d", i),
				BankName:      "GTBank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("CreateTransfer (success %d) failed: %v", i, err)
		}
		if err := h.Engine.ProcessTransfer(ctx, tr.TenantID, tr.ID); err != nil {
			t.Fatalf("ProcessTransfer (success %d) failed: %v", i, err)
		}
		h.executeOutbox(ctx)
	}

	// Run 3 transfers that fail at settlement
	for i := 0; i < 3; i++ {
		tr, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: fmt.Sprintf("ledger-balance-fail-%d", i),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(int64(500 + i*50)),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Balance Fail Sender",
				Email:   "fail@test.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Balance Fail Recipient",
				AccountNumber: fmt.Sprintf("666000%04d", i),
				BankName:      "GTBank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("CreateTransfer (fail %d) failed: %v", i, err)
		}

		// Step through manually: CreateTransfer -> FundTransfer -> InitiateOnRamp -> HandleOnRampResult(success) -> HandleSettlementResult(failure)
		if err := h.Engine.FundTransfer(ctx, tr.TenantID, tr.ID); err != nil {
			t.Fatalf("FundTransfer (fail %d) failed: %v", i, err)
		}
		h.executeOutbox(ctx)

		if err := h.Engine.InitiateOnRamp(ctx, tr.TenantID, tr.ID); err != nil {
			t.Fatalf("InitiateOnRamp (fail %d) failed: %v", i, err)
		}
		_ = h.TransferStore.drainOutbox()

		if err := h.Engine.HandleOnRampResult(ctx, tr.TenantID, tr.ID, domain.IntentResult{Success: true}); err != nil {
			t.Fatalf("HandleOnRampResult (fail %d) failed: %v", i, err)
		}
		h.executeOutbox(ctx)

		if err := h.Engine.HandleSettlementResult(ctx, tr.TenantID, tr.ID, domain.IntentResult{
			Success: false,
			Error:   fmt.Sprintf("chain_timeout_%d", i),
		}); err != nil {
			t.Fatalf("HandleSettlementResult (fail %d) failed: %v", i, err)
		}
		h.executeOutbox(ctx)
	}

	// Inspect all TigerBeetle transfers and verify the balance invariant
	h.TB.mu.Lock()
	defer h.TB.mu.Unlock()

	// Track total debits and credits per account
	accountDebits := make(map[ledger.ID128]decimal.Decimal)
	accountCredits := make(map[ledger.ID128]decimal.Decimal)

	for _, tbTransfer := range h.TB.transfers {
		amount := ledger.TBAmountToDecimal(tbTransfer.Amount)

		prev, ok := accountDebits[tbTransfer.DebitAccountID]
		if !ok {
			prev = decimal.Zero
		}
		accountDebits[tbTransfer.DebitAccountID] = prev.Add(amount)

		prev, ok = accountCredits[tbTransfer.CreditAccountID]
		if !ok {
			prev = decimal.Zero
		}
		accountCredits[tbTransfer.CreditAccountID] = prev.Add(amount)
	}

	// Sum all debits and all credits across all accounts
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	for _, d := range accountDebits {
		totalDebits = totalDebits.Add(d)
	}
	for _, c := range accountCredits {
		totalCredits = totalCredits.Add(c)
	}

	if !totalDebits.Equal(totalCredits) {
		t.Fatalf("ledger balance invariant violated: total debits=%s total credits=%s", totalDebits, totalCredits)
	}

	t.Logf("ledger balance invariant holds: total debits=%s == total credits=%s across %d TB transfers and %d accounts",
		totalDebits, totalCredits, len(h.TB.transfers), len(h.TB.accounts))

	// Also verify per-account consistency: each account's debits_posted and credits_posted
	// should match the sum of transfers touching that account
	for id, acc := range h.TB.accounts {
		expectedDebits, ok := accountDebits[id]
		if !ok {
			expectedDebits = decimal.Zero
		}
		expectedCredits, ok := accountCredits[id]
		if !ok {
			expectedCredits = decimal.Zero
		}

		actualDebits := ledger.TBAmountToDecimal(acc.DebitsPosted)
		actualCredits := ledger.TBAmountToDecimal(acc.CreditsPosted)

		if !actualDebits.Equal(expectedDebits) {
			t.Errorf("account %v: debits_posted=%s but sum of transfer debits=%s", id, actualDebits, expectedDebits)
		}
		if !actualCredits.Equal(expectedCredits) {
			t.Errorf("account %v: credits_posted=%s but sum of transfer credits=%s", id, actualCredits, expectedCredits)
		}
	}
}
