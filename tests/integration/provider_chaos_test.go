//go:build integration

package integration

// TEST-7: Provider chaos tests — multiple failures, state machine integrity,
// and compensation after partial pipeline completion.
//
// Architecture note: in Settla's outbox pattern the engine is a pure state
// machine. After HandleOnRampResult(success=false) the transfer moves to
// REFUNDING — there is no engine-level retry state. Retries happen at the
// worker/outbox level: the worker retries the provider call N times before
// reporting a final failure to the engine. These tests validate:
//   1. The engine produces correct compensation outbox entries on each failure.
//   2. After a fresh transfer's successful on-ramp the pipeline advances.
//   3. Off-ramp failure after blockchain completion queues treasury-release.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestProviderChaos_MultipleFailuresThenSuccess simulates the scenario where
// the on-ramp worker encounters provider errors on the first three attempts
// and succeeds on the fourth. Because the engine's state machine transitions
// ON_RAMPING → REFUNDING on any failure, each "attempt" is modelled as a
// separate transfer whose lifecycle is driven through ON_RAMPING and then
// either failed or succeeded. The key invariants are:
//
//  1. Each failed on-ramp results in exactly one IntentTreasuryRelease and one
//     EventProviderOnRampFailed in the outbox — no state corruption.
//  2. The 4th transfer (representing the successful retry) advances to SETTLING.
//  3. The failed transfers' outbox entries do not leak into the success path.
func TestProviderChaos_MultipleFailuresThenSuccess(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	makeTransferToOnRamping := func(t *testing.T, key string) *domain.Transfer {
		t.Helper()
		transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: key,
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(1000),
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Chaos Sender",
				Email:   "chaos@test.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Chaos Recipient",
				AccountNumber: "1111111111",
				BankName:      "Zenith",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("makeTransferToOnRamping[%s]: CreateTransfer failed: %v", key, err)
		}
		if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
			t.Fatalf("makeTransferToOnRamping[%s]: FundTransfer failed: %v", key, err)
		}
		if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
			t.Fatalf("makeTransferToOnRamping[%s]: InitiateOnRamp failed: %v", key, err)
		}
		got, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
		if got.Status != domain.TransferStatusOnRamping {
			t.Fatalf("makeTransferToOnRamping[%s]: expected ON_RAMPING, got %s", key, got.Status)
		}
		return got
	}

	type failureCase struct {
		errorMsg  string
		errorCode string
	}
	failures := []failureCase{
		{"provider timeout: GBP rails unavailable", "PROVIDER_TIMEOUT"},
		{"liquidity insufficient: GBP pool depleted", "INSUFFICIENT_LIQUIDITY"},
		{"provider rate limit exceeded", "RATE_LIMIT"},
	}

	// ── Attempts 1-3: each one fails ────────────────────────────────────────
	for i, fc := range failures {
		attempt := i + 1
		key := fmt.Sprintf("chaos-onramp-fail-%d", attempt)
		transfer := makeTransferToOnRamping(t, key)

		// Clear outbox from setup steps — we want to inspect only failure entries.
		h.TransferStore.drainOutbox()

		failResult := domain.IntentResult{
			Success:   false,
			Error:     fc.errorMsg,
			ErrorCode: fc.errorCode,
		}

		err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, failResult)
		if err != nil {
			t.Fatalf("attempt %d: HandleOnRampResult(failure) returned unexpected error: %v", attempt, err)
		}

		// State must be REFUNDING — not ON_RAMPING, not FAILED directly.
		got, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
		if err != nil {
			t.Fatalf("attempt %d: GetTransfer failed: %v", attempt, err)
		}
		if got.Status != domain.TransferStatusRefunding {
			t.Errorf("attempt %d: expected REFUNDING after on-ramp failure, got %s", attempt, got.Status)
		}

		// Inspect outbox — must contain treasury-release intent and no off-ramp/blockchain intents.
		entries := h.TransferStore.drainOutbox()

		var hasTreasuryRelease, hasOnRampFailedEvent bool
		var hasBlockchainSend, hasOffRamp bool
		for _, e := range entries {
			switch {
			case e.IsIntent && e.EventType == domain.IntentTreasuryRelease:
				var p domain.TreasuryReleasePayload
				if err := json.Unmarshal(e.Payload, &p); err != nil {
					t.Errorf("attempt %d: failed to unmarshal TreasuryReleasePayload: %v", attempt, err)
					continue
				}
				if p.TransferID != transfer.ID {
					t.Errorf("attempt %d: TreasuryReleasePayload.TransferID mismatch", attempt)
				}
				if p.Reason != "onramp_failure" {
					t.Errorf("attempt %d: TreasuryReleasePayload.Reason: want onramp_failure, got %s", attempt, p.Reason)
				}
				hasTreasuryRelease = true
			case !e.IsIntent && e.EventType == domain.EventProviderOnRampFailed:
				hasOnRampFailedEvent = true
			case e.IsIntent && e.EventType == domain.IntentBlockchainSend:
				hasBlockchainSend = true
			case e.IsIntent && e.EventType == domain.IntentProviderOffRamp:
				hasOffRamp = true
			}
		}

		if !hasTreasuryRelease {
			t.Errorf("attempt %d: expected IntentTreasuryRelease in outbox, not found", attempt)
		}
		if !hasOnRampFailedEvent {
			t.Errorf("attempt %d: expected EventProviderOnRampFailed in outbox, not found", attempt)
		}
		if hasBlockchainSend {
			t.Errorf("attempt %d: unexpected IntentBlockchainSend in outbox after on-ramp failure", attempt)
		}
		if hasOffRamp {
			t.Errorf("attempt %d: unexpected IntentProviderOffRamp in outbox after on-ramp failure", attempt)
		}
	}

	// ── Attempt 4: success ──────────────────────────────────────────────────
	successTransfer := makeTransferToOnRamping(t, "chaos-onramp-success-4")
	h.TransferStore.drainOutbox()
	h.Events.reset()

	successResult := domain.IntentResult{
		Success:     true,
		ProviderRef: "prov-ref-4th-attempt",
	}

	if err := h.Engine.HandleOnRampResult(ctx, successTransfer.TenantID, successTransfer.ID, successResult); err != nil {
		t.Fatalf("4th attempt: HandleOnRampResult(success) failed: %v", err)
	}

	// State must advance to SETTLING.
	settling, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, successTransfer.ID)
	if err != nil {
		t.Fatalf("4th attempt: GetTransfer failed: %v", err)
	}
	if settling.Status != domain.TransferStatusSettling {
		t.Fatalf("4th attempt: expected SETTLING after successful on-ramp, got %s", settling.Status)
	}

	// Verify blockchain-send and ledger-post intents are present.
	entries := h.TransferStore.drainOutbox()
	var hasBlockchainSend, hasLedgerPost bool
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentBlockchainSend {
			var p domain.BlockchainSendPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Errorf("4th attempt: failed to unmarshal BlockchainSendPayload: %v", err)
				continue
			}
			if p.TransferID != successTransfer.ID {
				t.Errorf("4th attempt: BlockchainSendPayload.TransferID mismatch")
			}
			hasBlockchainSend = true
		}
		if e.IsIntent && e.EventType == domain.IntentLedgerPost {
			hasLedgerPost = true
		}
	}
	if !hasBlockchainSend {
		t.Error("4th attempt: expected IntentBlockchainSend in outbox, not found")
	}
	if !hasLedgerPost {
		t.Error("4th attempt: expected IntentLedgerPost in outbox, not found")
	}

	// Verify onramp.completed event was published.
	publishedTypes := h.Events.eventTypes()
	hasOnRampCompleted := false
	for _, et := range publishedTypes {
		if et == domain.EventOnRampCompleted {
			hasOnRampCompleted = true
		}
	}
	if !hasOnRampCompleted {
		t.Error("4th attempt: expected onramp.completed domain event, not found")
	}

	t.Logf("chaos test: 3 failures + 1 success; final transfer=%s in state %s",
		successTransfer.ID, settling.Status)
}

// TestProviderChaos_OffRampFailureAfterBlockchain verifies that when off-ramp
// fails after the blockchain send has already confirmed (transfer in OFF_RAMPING),
// the engine queues treasury-release, ledger-reverse, and webhook-deliver
// intents and transitions the transfer to FAILED.
//
// This is the most financially sensitive failure path because the stablecoin
// transfer has already occurred on-chain. The treasury must be released and
// the on-ramp ledger entries reversed.
func TestProviderChaos_OffRampFailureAfterBlockchain(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// ── Step 1: create transfer and drive to SETTLING ───────────────────────
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "chaos-offramp-fail-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(750),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Chaos OffRamp Sender",
			Email:   "offramp-chaos@test.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Chaos OffRamp Recipient",
			AccountNumber: "2222222222",
			BankName:      "FirstBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}

	// ── Step 2: on-ramp succeeds → SETTLING ─────────────────────────────────
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:     true,
		ProviderRef: "prov-ref-offramp-chaos",
	}); err != nil {
		t.Fatalf("HandleOnRampResult(success) failed: %v", err)
	}

	settling, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if settling.Status != domain.TransferStatusSettling {
		t.Fatalf("expected SETTLING after on-ramp success, got %s", settling.Status)
	}

	// ── Step 3: blockchain confirms → OFF_RAMPING ───────────────────────────
	if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: true,
		TxHash:  "0xdeadbeef1234567890deadbeef1234567890deadbeef1234567890deadbeef12",
	}); err != nil {
		t.Fatalf("HandleSettlementResult(success) failed: %v", err)
	}

	offRamping, _ := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if offRamping.Status != domain.TransferStatusOffRamping {
		t.Fatalf("expected OFF_RAMPING after blockchain confirmation, got %s", offRamping.Status)
	}

	// Clear accumulated outbox entries from setup steps.
	h.TransferStore.drainOutbox()
	h.Events.reset()

	// ── Step 4: off-ramp fails ───────────────────────────────────────────────
	failResult := domain.IntentResult{
		Success:   false,
		Error:     "NGN bank transfer rejected: recipient account not found",
		ErrorCode: "RECIPIENT_ACCOUNT_NOT_FOUND",
	}

	if err := h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, failResult); err != nil {
		t.Fatalf("HandleOffRampResult(failure) failed: %v", err)
	}

	// ── Verify state is FAILED ───────────────────────────────────────────────
	failed, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer after off-ramp failure failed: %v", err)
	}
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after off-ramp failure, got %s", failed.Status)
	}

	// ── Verify compensation outbox entries ───────────────────────────────────
	entries := h.TransferStore.drainOutbox()
	if len(entries) == 0 {
		t.Fatal("expected outbox entries after off-ramp failure, got none")
	}

	var hasTreasuryRelease, hasLedgerReverse, hasWebhookDeliver bool
	for _, e := range entries {
		if !e.IsIntent {
			continue
		}
		switch e.EventType {
		case domain.IntentTreasuryRelease:
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
			// Amount must equal the source amount reserved at fund time.
			if !p.Amount.Equal(decimal.NewFromInt(750)) {
				t.Errorf("TreasuryReleasePayload.Amount: want 750, got %s", p.Amount)
			}
			if p.Reason != "offramp_failure" {
				t.Errorf("TreasuryReleasePayload.Reason: want offramp_failure, got %s", p.Reason)
			}
			hasTreasuryRelease = true

		case domain.IntentLedgerReverse:
			var p domain.LedgerPostPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Errorf("failed to unmarshal LedgerPostPayload (reverse): %v", err)
				continue
			}
			if p.TransferID != transfer.ID {
				t.Errorf("LedgerReversePayload.TransferID mismatch: want %s, got %s", transfer.ID, p.TransferID)
			}
			hasLedgerReverse = true

		case domain.IntentWebhookDeliver:
			var p domain.WebhookDeliverPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Errorf("failed to unmarshal WebhookDeliverPayload: %v", err)
				continue
			}
			if p.TransferID != transfer.ID {
				t.Errorf("WebhookDeliverPayload.TransferID mismatch: want %s, got %s", transfer.ID, p.TransferID)
			}
			if p.EventType != domain.EventTransferFailed {
				t.Errorf("WebhookDeliverPayload.EventType: want %s, got %s", domain.EventTransferFailed, p.EventType)
			}
			hasWebhookDeliver = true
		}
	}

	if !hasTreasuryRelease {
		t.Error("expected IntentTreasuryRelease in outbox after off-ramp failure — funds must be released")
	}
	if !hasLedgerReverse {
		t.Error("expected IntentLedgerReverse in outbox after off-ramp failure — on-ramp entry must be reversed")
	}
	if !hasWebhookDeliver {
		t.Error("expected IntentWebhookDeliver in outbox after off-ramp failure — tenant must be notified")
	}

	// ── Verify domain event published ───────────────────────────────────────
	publishedTypes := h.Events.eventTypes()
	hasOffRampFailed := false
	for _, et := range publishedTypes {
		if et == domain.EventProviderOffRampFailed {
			hasOffRampFailed = true
		}
	}
	if !hasOffRampFailed {
		t.Error("expected provider.offramp.failed domain event to be published")
	}

	t.Logf("off-ramp failure test: transfer=%s failed at OFF_RAMPING with %d compensation intents",
		transfer.ID, len(entries))
}
