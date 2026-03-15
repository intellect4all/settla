//go:build integration

package integration

// TEST-4: Blockchain reorg and eventual confirmation scenarios.
// These tests verify that the settlement engine correctly handles:
//   - Reverted blockchain transactions (SETTLING → FAILED with compensation)
//   - Pending blockchain transactions that eventually confirm (SETTLING → OFF_RAMPING)

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// advanceToSettling is a helper that creates a Lemfi GBP→NGN transfer and
// drives it to SETTLING state (CREATED → FUNDED → ON_RAMPING → SETTLING).
// It returns the settled transfer so block_reorg tests can call HandleSettlementResult
// directly against the engine.
func advanceToSettling(t *testing.T, h *testHarness, idempotencyKey string) *domain.Transfer {
	t.Helper()
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: idempotencyKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Block Reorg Sender",
			Email:   "reorg@test.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Block Reorg Recipient",
			AccountNumber: "9876543210",
			BankName:      "AccessBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("advanceToSettling: CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("advanceToSettling: FundTransfer failed: %v", err)
	}

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("advanceToSettling: InitiateOnRamp failed: %v", err)
	}

	// On-ramp succeeds → SETTLING
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:     true,
		ProviderRef: "mock-onramp-ref-001",
	}); err != nil {
		t.Fatalf("advanceToSettling: HandleOnRampResult failed: %v", err)
	}

	settled, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("advanceToSettling: GetTransfer failed: %v", err)
	}
	if settled.Status != domain.TransferStatusSettling {
		t.Fatalf("advanceToSettling: expected SETTLING, got %s", settled.Status)
	}

	return settled
}

// TestBlockReorg_RevertedTransaction verifies that a blockchain revert (Success=false)
// from SETTLING state transitions the transfer to FAILED and queues both a
// treasury-release and ledger-reverse intent in the outbox.
func TestBlockReorg_RevertedTransaction(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer := advanceToSettling(t, h, "reorg-reverted-1")

	// Drain any outbox entries accumulated during setup so we get a clean slate
	// to inspect only the entries produced by HandleSettlementResult.
	h.TransferStore.drainOutbox()

	// Simulate a blockchain reorg / reverted transaction: the on-chain tx was
	// included in a block that was later reorganised away.  The worker reports
	// this as Success=false with an appropriate error message.
	revertResult := domain.IntentResult{
		Success:   false,
		Error:     "transaction reverted: block reorganisation detected",
		ErrorCode: "BLOCKCHAIN_REORG",
	}

	err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, revertResult)
	if err != nil {
		t.Fatalf("HandleSettlementResult (reorg) failed: %v", err)
	}

	// 1. Verify state transitioned to FAILED (not SETTLING or REFUNDING).
	//    The valid transition from SETTLING on failure is directly to FAILED.
	failed, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after reorg failed: %v", err)
	}
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after blockchain reorg, got %s", failed.Status)
	}

	// 2. Inspect outbox entries produced by HandleSettlementResult.
	entries := h.TransferStore.drainOutbox()
	if len(entries) == 0 {
		t.Fatal("expected outbox entries after settlement failure, got none")
	}

	var hasTreasuryRelease, hasLedgerReverse bool
	for _, e := range entries {
		if !e.IsIntent {
			continue
		}
		switch e.EventType {
		case domain.IntentTreasuryRelease:
			// Verify the release payload carries the correct transfer and reason.
			var p domain.TreasuryReleasePayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Errorf("failed to unmarshal TreasuryReleasePayload: %v", err)
				continue
			}
			if p.TransferID != transfer.ID {
				t.Errorf("TreasuryReleasePayload.TransferID mismatch: want %s, got %s", transfer.ID, p.TransferID)
			}
			if p.TenantID != LemfiTenantID {
				t.Errorf("TreasuryReleasePayload.TenantID mismatch: want %s, got %s", LemfiTenantID, p.TenantID)
			}
			if p.Amount.IsZero() || p.Amount.IsNegative() {
				t.Errorf("TreasuryReleasePayload.Amount should be positive, got %s", p.Amount)
			}
			if p.Reason != "settlement_failure" {
				t.Errorf("TreasuryReleasePayload.Reason: want settlement_failure, got %s", p.Reason)
			}
			hasTreasuryRelease = true

		case domain.IntentLedgerReverse:
			// Verify the ledger reverse payload references this transfer.
			var p domain.LedgerPostPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Errorf("failed to unmarshal LedgerPostPayload (reverse): %v", err)
				continue
			}
			if p.TransferID != transfer.ID {
				t.Errorf("LedgerReversePayload.TransferID mismatch: want %s, got %s", transfer.ID, p.TransferID)
			}
			hasLedgerReverse = true
		}
	}

	if !hasTreasuryRelease {
		t.Error("expected IntentTreasuryRelease in outbox after blockchain reorg, not found")
	}
	if !hasLedgerReverse {
		t.Error("expected IntentLedgerReverse in outbox after blockchain reorg, not found")
	}

	// 3. Verify the blockchain-failed event was emitted.
	publishedTypes := h.Events.eventTypes()
	hasBlockchainFailed := false
	for _, et := range publishedTypes {
		if et == domain.EventBlockchainFailed {
			hasBlockchainFailed = true
		}
	}
	if !hasBlockchainFailed {
		t.Error("expected blockchain.failed domain event to be published")
	}

	t.Logf("reorg test: transfer=%s transitioned to %s with %d outbox entries",
		transfer.ID, failed.Status, len(entries))
}

// TestBlockReorg_EventualConfirmation verifies two scenarios:
//  1. A "pending" result (Success=false, no revert) from SETTLING leaves the
//     transfer in an error state that the engine surfaces as FAILED — because
//     the valid transitions from SETTLING on failure go to FAILED directly.
//     The test then creates a fresh transfer to validate the confirmation path.
//  2. A confirmed result (Success=true) from SETTLING advances to OFF_RAMPING
//     and queues an IntentProviderOffRamp intent.
//
// Note: the Settla engine does not have an intermediate "pending" state between
// SETTLING and the terminal/next states. A Success=false result always moves to
// FAILED regardless of whether the blockchain tx is "pending" or "reverted".
// Confirmation polling happens at the worker level via NATS redelivery; the
// engine only sees the final outcome. This test validates both outcomes.
func TestBlockReorg_EventualConfirmation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	t.Run("pending_result_transitions_to_failed", func(t *testing.T) {
		transfer := advanceToSettling(t, h, "reorg-pending-1")
		h.TransferStore.drainOutbox()

		// "Pending" from the worker's perspective = not yet confirmed, not reverted.
		// From the engine's perspective this is still a failure signal — the worker
		// will re-poll and eventually call HandleSettlementResult(success=true) or
		// re-deliver the NATS message. Here we test the failure branch directly.
		pendingResult := domain.IntentResult{
			Success:   false,
			Error:     "blockchain tx not yet confirmed: pending inclusion",
			ErrorCode: "TX_PENDING",
		}

		if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, pendingResult); err != nil {
			t.Fatalf("HandleSettlementResult (pending) failed: %v", err)
		}

		// Engine transitions SETTLING → FAILED; the worker is responsible for
		// retrying before reporting to the engine in production.
		after, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
		if err != nil {
			t.Fatalf("GetTransfer after pending result failed: %v", err)
		}
		if after.Status != domain.TransferStatusFailed {
			t.Fatalf("expected FAILED on pending (unconfirmed) result, got %s", after.Status)
		}

		// Verify compensation intents were queued.
		entries := h.TransferStore.drainOutbox()
		hasTreasuryRelease := false
		for _, e := range entries {
			if e.IsIntent && e.EventType == domain.IntentTreasuryRelease {
				hasTreasuryRelease = true
			}
		}
		if !hasTreasuryRelease {
			t.Error("expected IntentTreasuryRelease in outbox after unconfirmed settlement result")
		}
	})

	t.Run("confirmed_result_advances_to_off_ramping", func(t *testing.T) {
		// Fresh transfer for the confirmed path.
		transfer := advanceToSettling(t, h, "reorg-confirmed-1")
		h.TransferStore.drainOutbox()
		h.Events.reset()

		confirmedResult := domain.IntentResult{
			Success: true,
			TxHash:  "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		}

		if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, confirmedResult); err != nil {
			t.Fatalf("HandleSettlementResult (confirmed) failed: %v", err)
		}

		// Verify state advanced to OFF_RAMPING.
		offRamping, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
		if err != nil {
			t.Fatalf("GetTransfer after confirmed settlement failed: %v", err)
		}
		if offRamping.Status != domain.TransferStatusOffRamping {
			t.Fatalf("expected OFF_RAMPING after blockchain confirmation, got %s", offRamping.Status)
		}

		// Verify IntentProviderOffRamp was queued in the outbox.
		entries := h.TransferStore.drainOutbox()
		hasOffRampIntent := false
		for _, e := range entries {
			if e.IsIntent && e.EventType == domain.IntentProviderOffRamp {
				var p domain.ProviderOffRampPayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Errorf("failed to unmarshal ProviderOffRampPayload: %v", err)
					continue
				}
				if p.TransferID != transfer.ID {
					t.Errorf("ProviderOffRampPayload.TransferID mismatch: want %s, got %s", transfer.ID, p.TransferID)
				}
				if p.TenantID != LemfiTenantID {
					t.Errorf("ProviderOffRampPayload.TenantID mismatch: want %s, got %s", LemfiTenantID, p.TenantID)
				}
				if p.FromCurrency != domain.CurrencyUSDT {
					t.Errorf("ProviderOffRampPayload.FromCurrency: want USDT, got %s", p.FromCurrency)
				}
				if p.ToCurrency != domain.CurrencyNGN {
					t.Errorf("ProviderOffRampPayload.ToCurrency: want NGN, got %s", p.ToCurrency)
				}
				hasOffRampIntent = true
			}
		}
		if !hasOffRampIntent {
			t.Error("expected IntentProviderOffRamp in outbox after blockchain confirmation, not found")
		}

		// Verify settlement.completed domain event was published.
		publishedTypes := h.Events.eventTypes()
		hasSettlementCompleted := false
		for _, et := range publishedTypes {
			if et == domain.EventSettlementCompleted {
				hasSettlementCompleted = true
			}
		}
		if !hasSettlementCompleted {
			t.Error("expected settlement.completed domain event to be published")
		}

		t.Logf("confirmation test: transfer=%s advanced to %s, off-ramp intent queued",
			transfer.ID, offRamping.Status)
	})
}
