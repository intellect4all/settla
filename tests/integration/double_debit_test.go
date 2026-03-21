//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestHandleOnRampResult_DoubleCall_NoDuplicateBlockchainIntents verifies that
// calling HandleOnRampResult(success=true) twice (simulating NATS redelivery)
// does not create duplicate blockchain send intents. The engine checks the
// transfer status at the beginning of HandleOnRampResult; if it has already
// advanced past ON_RAMPING, the call is an idempotent no-op.
func TestHandleOnRampResult_DoubleCall_NoDuplicateBlockchainIntents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create a transfer.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "double-onramp-blockchain-1",
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
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}
	if transfer.Status != domain.TransferStatusCreated {
		t.Fatalf("expected CREATED, got %s", transfer.Status)
	}

	transferID := transfer.ID
	tenantID := transfer.TenantID

	// 2. Fund the transfer: CREATED -> FUNDED
	if err := h.Engine.FundTransfer(ctx, tenantID, transferID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}

	// 3. Initiate on-ramp: FUNDED -> ON_RAMPING
	if err := h.Engine.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}

	// 4. Drain outbox (clear pending intents from create/fund/initiate steps).
	_ = h.TransferStore.drainOutbox()

	// 5. First HandleOnRampResult(success=true): ON_RAMPING -> SETTLING
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:     true,
		ProviderRef: "onramp-ref-1",
	}); err != nil {
		t.Fatalf("first HandleOnRampResult failed: %v", err)
	}

	// 6. Drain outbox and count IntentBlockchainSend entries.
	entries := h.TransferStore.drainOutbox()
	blockchainIntents := 0
	for _, e := range entries {
		if e.EventType == domain.IntentBlockchainSend {
			blockchainIntents++
		}
	}
	if blockchainIntents != 1 {
		t.Errorf("expected exactly 1 blockchain send intent after first call, got %d", blockchainIntents)
	}

	// 7. Second HandleOnRampResult(success=true) -- simulates NATS redelivery.
	// The transfer is now in SETTLING status, so the engine should skip this.
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:     true,
		ProviderRef: "onramp-ref-1",
	}); err != nil {
		t.Fatalf("second HandleOnRampResult should succeed (idempotent no-op): %v", err)
	}

	// 8. Drain outbox again -- should be empty (no new intents produced).
	entries2 := h.TransferStore.drainOutbox()
	blockchainIntents2 := 0
	for _, e := range entries2 {
		if e.EventType == domain.IntentBlockchainSend {
			blockchainIntents2++
		}
	}
	if blockchainIntents2 != 0 {
		t.Errorf("second call should produce no blockchain intents, got %d", blockchainIntents2)
	}

	// 9. Verify transfer is in SETTLING status (not double-advanced).
	tx, err := h.TransferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if tx.Status != domain.TransferStatusSettling {
		t.Errorf("expected SETTLING after one successful HandleOnRampResult, got %s", tx.Status)
	}

	t.Logf("double-debit prevention (on-ramp): transfer=%s, status=%s, blockchain intents: first=%d second=%d",
		transferID, tx.Status, blockchainIntents, blockchainIntents2)
}

// TestHandleSettlementResult_DoubleCall_NoDuplicateOffRampIntents verifies that
// calling HandleSettlementResult(success=true) twice (simulating NATS redelivery)
// does not create duplicate off-ramp intents.
func TestHandleSettlementResult_DoubleCall_NoDuplicateOffRampIntents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create a transfer.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "double-settlement-offramp-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(750),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Charlie Delta",
			Email:   "charlie@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Dayo Emeka",
			AccountNumber: "2222222222",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	transferID := transfer.ID
	tenantID := transfer.TenantID

	// 2. Advance to SETTLING: CREATED -> FUNDED -> ON_RAMPING -> SETTLING
	if err := h.Engine.FundTransfer(ctx, tenantID, transferID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	if err := h.Engine.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:     true,
		ProviderRef: "onramp-ref-settle-1",
	}); err != nil {
		t.Fatalf("HandleOnRampResult failed: %v", err)
	}

	// 3. Drain outbox (clear intents from previous steps).
	_ = h.TransferStore.drainOutbox()

	// Verify the transfer is now in SETTLING status.
	tx, err := h.TransferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if tx.Status != domain.TransferStatusSettling {
		t.Fatalf("expected SETTLING before test, got %s", tx.Status)
	}

	// 4. First HandleSettlementResult(success=true): SETTLING -> OFF_RAMPING
	if err := h.Engine.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{
		Success: true,
		TxHash:  "0xabc123",
	}); err != nil {
		t.Fatalf("first HandleSettlementResult failed: %v", err)
	}

	// 5. Drain outbox and count IntentProviderOffRamp entries.
	entries := h.TransferStore.drainOutbox()
	offRampIntents := 0
	for _, e := range entries {
		if e.EventType == domain.IntentProviderOffRamp {
			offRampIntents++
		}
	}
	if offRampIntents != 1 {
		t.Errorf("expected exactly 1 off-ramp intent after first call, got %d", offRampIntents)
	}

	// 6. Second HandleSettlementResult(success=true) -- simulates NATS redelivery.
	if err := h.Engine.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{
		Success: true,
		TxHash:  "0xabc123",
	}); err != nil {
		t.Fatalf("second HandleSettlementResult should succeed (idempotent no-op): %v", err)
	}

	// 7. Drain outbox again -- should be empty.
	entries2 := h.TransferStore.drainOutbox()
	offRampIntents2 := 0
	for _, e := range entries2 {
		if e.EventType == domain.IntentProviderOffRamp {
			offRampIntents2++
		}
	}
	if offRampIntents2 != 0 {
		t.Errorf("second call should produce no off-ramp intents, got %d", offRampIntents2)
	}

	// 8. Verify transfer is in OFF_RAMPING status (not double-advanced).
	tx2, err := h.TransferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if tx2.Status != domain.TransferStatusOffRamping {
		t.Errorf("expected OFF_RAMPING after one successful HandleSettlementResult, got %s", tx2.Status)
	}

	t.Logf("double-debit prevention (settlement): transfer=%s, status=%s, off-ramp intents: first=%d second=%d",
		transferID, tx2.Status, offRampIntents, offRampIntents2)
}

// TestHandleOnRampResult_DoubleFailure_NoDuplicateReleaseIntents verifies that
// calling HandleOnRampResult(success=false) twice does not create duplicate
// treasury release intents.
func TestHandleOnRampResult_DoubleFailure_NoDuplicateReleaseIntents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create and advance to ON_RAMPING.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "double-onramp-fail-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(300),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Eve Foxtrot",
			Email:   "eve@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Femi Gbenga",
			AccountNumber: "3333333333",
			BankName:      "Access Bank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	transferID := transfer.ID
	tenantID := transfer.TenantID

	if err := h.Engine.FundTransfer(ctx, tenantID, transferID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	if err := h.Engine.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}

	// Drain outbox before the test.
	_ = h.TransferStore.drainOutbox()

	// 2. First HandleOnRampResult(failure): ON_RAMPING -> REFUNDING
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:   false,
		Error:     "provider timeout",
		ErrorCode: "TIMEOUT",
	}); err != nil {
		t.Fatalf("first HandleOnRampResult (failure) failed: %v", err)
	}

	entries := h.TransferStore.drainOutbox()
	releaseIntents := 0
	for _, e := range entries {
		if e.EventType == domain.IntentTreasuryRelease {
			releaseIntents++
		}
	}
	if releaseIntents != 1 {
		t.Errorf("expected exactly 1 treasury release intent after first failure, got %d", releaseIntents)
	}

	// 3. Second HandleOnRampResult(failure) -- NATS redelivery.
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:   false,
		Error:     "provider timeout",
		ErrorCode: "TIMEOUT",
	}); err != nil {
		t.Fatalf("second failure call should succeed (idempotent no-op): %v", err)
	}

	entries2 := h.TransferStore.drainOutbox()
	releaseIntents2 := 0
	for _, e := range entries2 {
		if e.EventType == domain.IntentTreasuryRelease {
			releaseIntents2++
		}
	}
	if releaseIntents2 != 0 {
		t.Errorf("second failure call should produce no treasury release intents, got %d", releaseIntents2)
	}

	// 4. Verify transfer is in REFUNDING status.
	tx, err := h.TransferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if tx.Status != domain.TransferStatusRefunding {
		t.Errorf("expected REFUNDING, got %s", tx.Status)
	}

	t.Logf("double-release prevention (on-ramp failure): transfer=%s, status=%s, release intents: first=%d second=%d",
		transferID, tx.Status, releaseIntents, releaseIntents2)
}

// TestHandleSettlementResult_DoubleFailure_NoDuplicateReverseIntents verifies that
// calling HandleSettlementResult(success=false) twice does not create duplicate
// ledger reverse or treasury release intents.
func TestHandleSettlementResult_DoubleFailure_NoDuplicateReverseIntents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create and advance to SETTLING.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "double-settle-fail-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(400),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Grace Hotel",
			Email:   "grace@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Hakeem Ibrahim",
			AccountNumber: "4444444444",
			BankName:      "UBA",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	transferID := transfer.ID
	tenantID := transfer.TenantID

	if err := h.Engine.FundTransfer(ctx, tenantID, transferID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	if err := h.Engine.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	if err := h.Engine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
		Success: true,
	}); err != nil {
		t.Fatalf("HandleOnRampResult failed: %v", err)
	}

	// Drain outbox before the settlement failure test.
	_ = h.TransferStore.drainOutbox()

	// 2. First HandleSettlementResult(failure): SETTLING -> FAILED
	if err := h.Engine.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:   false,
		Error:     "blockchain revert",
		ErrorCode: "CHAIN_REVERT",
	}); err != nil {
		t.Fatalf("first HandleSettlementResult (failure) failed: %v", err)
	}

	entries := h.TransferStore.drainOutbox()
	releaseIntents := 0
	reverseIntents := 0
	for _, e := range entries {
		if e.EventType == domain.IntentTreasuryRelease {
			releaseIntents++
		}
		if e.EventType == domain.IntentLedgerReverse {
			reverseIntents++
		}
	}
	if releaseIntents != 1 {
		t.Errorf("expected exactly 1 treasury release intent after first failure, got %d", releaseIntents)
	}
	if reverseIntents != 1 {
		t.Errorf("expected exactly 1 ledger reverse intent after first failure, got %d", reverseIntents)
	}

	// 3. Second HandleSettlementResult(failure) -- NATS redelivery.
	if err := h.Engine.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{
		Success:   false,
		Error:     "blockchain revert",
		ErrorCode: "CHAIN_REVERT",
	}); err != nil {
		t.Fatalf("second failure call should succeed (idempotent no-op): %v", err)
	}

	entries2 := h.TransferStore.drainOutbox()
	releaseIntents2 := 0
	reverseIntents2 := 0
	for _, e := range entries2 {
		if e.EventType == domain.IntentTreasuryRelease {
			releaseIntents2++
		}
		if e.EventType == domain.IntentLedgerReverse {
			reverseIntents2++
		}
	}
	if releaseIntents2 != 0 {
		t.Errorf("second failure call should produce no treasury release intents, got %d", releaseIntents2)
	}
	if reverseIntents2 != 0 {
		t.Errorf("second failure call should produce no ledger reverse intents, got %d", reverseIntents2)
	}

	// 4. Verify transfer is in FAILED status.
	tx, err := h.TransferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if tx.Status != domain.TransferStatusFailed {
		t.Errorf("expected FAILED, got %s", tx.Status)
	}

	t.Logf("double-reverse prevention (settlement failure): transfer=%s, status=%s, release: first=%d second=%d, reverse: first=%d second=%d",
		transferID, tx.Status, releaseIntents, releaseIntents2, reverseIntents, reverseIntents2)
}
