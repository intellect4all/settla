//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestProviderFailover verifies that an on-ramp failure for a FUNDED transfer
// triggers the correct compensation: the transfer moves to REFUNDING and the
// outbox contains an IntentTreasuryRelease for the full source amount.
func TestProviderFailover(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create a GBP→NGN transfer for Lemfi.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "failover-onramp-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Alice Patel",
			Email:   "alice@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Biodun Adeyemi",
			AccountNumber: "1234567890",
			BankName:      "First Bank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}
	if transfer.Status != domain.TransferStatusCreated {
		t.Fatalf("expected CREATED, got %s", transfer.Status)
	}

	// 2. Fund the transfer (CREATED → FUNDED) and drain the outbox so the
	//    treasury reserve intent is executed, leaving funds locked.
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx) // processes IntentTreasuryReserve

	// 3. Initiate on-ramp (FUNDED → ON_RAMPING).
	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	h.TransferStore.drainOutbox() // clear on-ramp intent — worker won't run in tests

	// 4. Report on-ramp failure (ON_RAMPING → REFUNDING).
	failResult := domain.IntentResult{
		Success:   false,
		Error:     "provider_timeout",
		ErrorCode: "TIMEOUT",
	}
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, failResult); err != nil {
		t.Fatalf("HandleOnRampResult(failure) failed: %v", err)
	}

	// 5. Verify the transfer is now in REFUNDING.
	updated, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after failure: %v", err)
	}
	if updated.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING after on-ramp failure, got %s", updated.Status)
	}

	// 6. Verify the outbox contains an IntentTreasuryRelease for the full source amount.
	entries := h.TransferStore.drainOutbox()
	releaseEntry := findOutboxEntry(entries, domain.IntentTreasuryRelease)
	if releaseEntry == nil {
		t.Fatalf("expected IntentTreasuryRelease in outbox after on-ramp failure, got entries: %v",
			outboxEventTypes(entries))
	}

	var releasePayload domain.TreasuryReleasePayload
	if err := json.Unmarshal(releaseEntry.Payload, &releasePayload); err != nil {
		t.Fatalf("unmarshal TreasuryReleasePayload: %v", err)
	}
	if releasePayload.TransferID != transfer.ID {
		t.Errorf("release payload transfer_id: want %s, got %s", transfer.ID, releasePayload.TransferID)
	}
	if releasePayload.TenantID != LemfiTenantID {
		t.Errorf("release payload tenant_id: want %s, got %s", LemfiTenantID, releasePayload.TenantID)
	}
	if !releasePayload.Amount.Equal(transfer.SourceAmount) {
		t.Errorf("release payload amount: want %s, got %s",
			transfer.SourceAmount, releasePayload.Amount)
	}
	if releasePayload.Currency != domain.CurrencyGBP {
		t.Errorf("release payload currency: want GBP, got %s", releasePayload.Currency)
	}

	// 7. Verify an EventProviderOnRampFailed was published.
	eventTypes := h.Events.eventTypes()
	if !containsString(eventTypes, domain.EventProviderOnRampFailed) {
		t.Errorf("expected %s domain event to be published, got: %v",
			domain.EventProviderOnRampFailed, eventTypes)
	}

	t.Logf("provider failover: transfer=%s reached REFUNDING with treasury release intent", transfer.ID)
}

// TestProviderFailoverAfterPartialSuccess verifies the compensation path when
// on-ramp succeeds (transfer enters SETTLING) but blockchain settlement then
// fails. In that case the engine must write IntentTreasuryRelease and
// IntentLedgerReverse so the completed on-ramp is unwound.
func TestProviderFailoverAfterPartialSuccess(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// 1. Create and fund a GBP→NGN transfer.
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "failover-partial-success-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Bob Kapoor",
			Email:   "bob@lemfi.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Chidi Okonkwo",
			AccountNumber: "0987654321",
			BankName:      "Zenith Bank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx) // processes IntentTreasuryReserve

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	h.TransferStore.drainOutbox()

	// 2. On-ramp succeeds → transfer enters SETTLING.
	successResult := domain.IntentResult{
		Success:     true,
		ProviderRef: "PROVIDER-REF-001",
	}
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, successResult); err != nil {
		t.Fatalf("HandleOnRampResult(success) failed: %v", err)
	}

	// Verify SETTLING state before next step.
	afterOnRamp, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after on-ramp success: %v", err)
	}
	if afterOnRamp.Status != domain.TransferStatusSettling {
		t.Fatalf("expected SETTLING after on-ramp success, got %s", afterOnRamp.Status)
	}

	// Drain the ledger-post and blockchain-send intents written by HandleOnRampResult.
	h.TransferStore.drainOutbox()
	h.Events.reset()

	// 3. Blockchain settlement fails → SETTLING → FAILED.
	blockchainFailResult := domain.IntentResult{
		Success:   false,
		Error:     "insufficient_on_chain_balance",
		ErrorCode: "CHAIN_ERR_BALANCE",
	}
	if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, blockchainFailResult); err != nil {
		t.Fatalf("HandleSettlementResult(failure) failed: %v", err)
	}

	// 4. Verify the transfer is now FAILED.
	afterChainFail, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after settlement failure: %v", err)
	}
	if afterChainFail.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after settlement failure, got %s", afterChainFail.Status)
	}

	// 5. Verify compensation intents in outbox.
	entries := h.TransferStore.drainOutbox()

	releaseEntry := findOutboxEntry(entries, domain.IntentTreasuryRelease)
	if releaseEntry == nil {
		t.Fatalf("expected IntentTreasuryRelease after settlement failure, got: %v",
			outboxEventTypes(entries))
	}
	var releasePayload domain.TreasuryReleasePayload
	if err := json.Unmarshal(releaseEntry.Payload, &releasePayload); err != nil {
		t.Fatalf("unmarshal TreasuryReleasePayload: %v", err)
	}
	if !releasePayload.Amount.Equal(transfer.SourceAmount) {
		t.Errorf("release amount: want %s, got %s", transfer.SourceAmount, releasePayload.Amount)
	}

	reverseEntry := findOutboxEntry(entries, domain.IntentLedgerReverse)
	if reverseEntry == nil {
		t.Fatalf("expected IntentLedgerReverse after settlement failure, got: %v",
			outboxEventTypes(entries))
	}
	var reversePayload domain.LedgerPostPayload
	if err := json.Unmarshal(reverseEntry.Payload, &reversePayload); err != nil {
		t.Fatalf("unmarshal LedgerPostPayload for reversal: %v", err)
	}
	if reversePayload.TransferID != transfer.ID {
		t.Errorf("reversal payload transfer_id: want %s, got %s", transfer.ID, reversePayload.TransferID)
	}

	// 6. Verify EventBlockchainFailed domain event was published.
	eventTypes := h.Events.eventTypes()
	if !containsString(eventTypes, domain.EventBlockchainFailed) {
		t.Errorf("expected %s domain event, got: %v", domain.EventBlockchainFailed, eventTypes)
	}

	// 7. Verify the full event history captures the key transitions.
	history, err := h.TransferStore.GetTransferEvents(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransferEvents: %v", err)
	}
	seen := make(map[domain.TransferStatus]bool)
	for _, ev := range history {
		seen[ev.ToStatus] = true
	}
	for _, want := range []domain.TransferStatus{
		domain.TransferStatusFunded,
		domain.TransferStatusOnRamping,
		domain.TransferStatusSettling,
		domain.TransferStatusFailed,
	} {
		if !seen[want] {
			t.Errorf("expected transition to %s in event history", want)
		}
	}

	t.Logf("partial success failover: transfer=%s — on-ramp succeeded, settlement failed → FAILED with compensation intents", transfer.ID)
}

// ─── helpers ────────────────────────────────────────────────────────────────

// findOutboxEntry returns the first outbox entry matching the given event type,
// or nil if none is found. Searches both intent and event entries.
func findOutboxEntry(entries []domain.OutboxEntry, eventType string) *domain.OutboxEntry {
	for i := range entries {
		if entries[i].EventType == eventType {
			return &entries[i]
		}
	}
	return nil
}

// outboxEventTypes returns a slice of event type strings for display in test
// failure messages.
func outboxEventTypes(entries []domain.OutboxEntry) []string {
	types := make([]string, len(entries))
	for i, e := range entries {
		types[i] = e.EventType
	}
	return types
}

// containsString returns true if slice s contains target.
func containsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}
