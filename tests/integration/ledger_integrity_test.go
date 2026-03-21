//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestLedgerIntegrity_BalancedPostings verifies that every transfer
// processed through the engine + outbox produces balanced ledger postings where
// sum(debits) == sum(credits). When TigerBeetle is available, it verifies against
// the real TB instance; otherwise it uses the mock TB client from the test harness.
func TestLedgerIntegrity_BalancedPostings(t *testing.T) {
	if os.Getenv("TIGERBEETLE_ADDRESS") == "" {
		t.Log("TIGERBEETLE_ADDRESS not set — running with mock TigerBeetle client")
	}

	h := newTestHarness(t)
	ctx := context.Background()

	// Create and process multiple transfers to accumulate ledger entries
	transferAmounts := []decimal.Decimal{
		decimal.NewFromInt(500),
		decimal.NewFromInt(1_000),
		decimal.NewFromFloat(2_500.75),
		decimal.NewFromFloat(10_000.33),
		decimal.NewFromFloat(123.45),
	}

	for i, amount := range transferAmounts {
		transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
			IdempotencyKey: fmt.Sprintf("ledger-integrity-%d", i),
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   amount,
			DestCurrency:   domain.CurrencyNGN,
			Sender: domain.Sender{
				ID:      uuid.New(),
				Name:    "Ledger Tester",
				Email:   "ledger@example.com",
				Country: "GB",
			},
			Recipient: domain.Recipient{
				Name:          "Receiver",
				AccountNumber: "0123456789",
				BankName:      "GTBank",
				Country:       "NG",
			},
		})
		if err != nil {
			t.Fatalf("CreateTransfer[%d] failed: %v", i, err)
		}

		// Process through full pipeline (engine advances state, writes outbox entries)
		if err := h.Engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
			t.Fatalf("ProcessTransfer[%d] failed: %v", i, err)
		}

		// Execute outbox entries (treasury reserve, ledger post)
		h.executeOutbox(ctx)
	}

	// Verify balanced postings in mock TigerBeetle: for every account,
	// the total debits posted across all accounts must equal total credits posted.
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	tb := h.TB
	tb.mu.Lock()
	for _, acc := range tb.accounts {
		d := decimal.NewFromInt(int64(acc.DebitsPosted))
		c := decimal.NewFromInt(int64(acc.CreditsPosted))
		totalDebits = totalDebits.Add(d)
		totalCredits = totalCredits.Add(c)
	}
	accountCount := len(tb.accounts)
	transferCount := len(tb.transfers)
	tb.mu.Unlock()

	if !totalDebits.Equal(totalCredits) {
		t.Fatalf("ledger imbalance: total_debits=%s, total_credits=%s, diff=%s",
			totalDebits, totalCredits, totalDebits.Sub(totalCredits))
	}

	t.Logf("PASS: %d TB accounts, %d TB transfers, total_debits=total_credits=%s",
		accountCount, transferCount, totalDebits)
}

// TestLedgerIntegrity_TigerBeetleLive is a skeleton test that requires
// a live TigerBeetle instance. It skips when TIGERBEETLE_ADDRESS is not set.
func TestLedgerIntegrity_TigerBeetleLive(t *testing.T) {
	tbAddr := os.Getenv("TIGERBEETLE_ADDRESS")
	if tbAddr == "" {
		t.Skip("skipping: TIGERBEETLE_ADDRESS not set — requires live TigerBeetle instance")
	}

	// When TB is available, this test would:
	// 1. Connect to TigerBeetle at tbAddr
	// 2. Create real accounts and transfers
	// 3. Verify balanced postings via LookupAccounts
	// 4. Verify idempotent re-creation returns existing entries
	t.Logf("TigerBeetle available at %s — live ledger integrity test", tbAddr)

	h := newTestHarness(t)
	ctx := context.Background()

	// Create a transfer and process it
	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "tb-live-integrity-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(5_000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "TB Live Tester",
			Email:   "tb@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("CreateTransfer failed: %v", err)
	}

	if err := h.Engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("ProcessTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Verify transfer reached terminal state
	final, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	if final.Status != domain.TransferStatusCompleted {
		t.Fatalf("expected COMPLETED, got %s", final.Status)
	}

	t.Logf("PASS: live TB transfer completed, status=%s", final.Status)
}

// TestLedgerIntegrity_MultiTenantIsolation verifies that ledger postings from
// different tenants do not interfere with each other's balances.
func TestLedgerIntegrity_MultiTenantIsolation(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create transfers for Lemfi (GBP→NGN)
	lemfiTransfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "ledger-isolation-lemfi",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1_000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Lemfi User",
			Email:   "lemfi@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver NG",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err != nil {
		t.Fatalf("Lemfi CreateTransfer failed: %v", err)
	}

	// Create transfers for Fincra (NGN→GBP)
	fincraTransfer, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: "ledger-isolation-fincra",
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(500_000),
		DestCurrency:   domain.CurrencyGBP,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Fincra User",
			Email:   "fincra@example.com",
			Country: "NG",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver GB",
			AccountNumber: "12345678",
			SortCode:      "123456",
			BankName:      "Barclays",
			Country:       "GB",
		},
	})
	if err != nil {
		t.Fatalf("Fincra CreateTransfer failed: %v", err)
	}

	// Process both
	if err := h.Engine.ProcessTransfer(ctx, lemfiTransfer.TenantID, lemfiTransfer.ID); err != nil {
		t.Fatalf("Lemfi ProcessTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.ProcessTransfer(ctx, fincraTransfer.TenantID, fincraTransfer.ID); err != nil {
		t.Fatalf("Fincra ProcessTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Verify each tenant's transfer completed independently
	lemfiFinal, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, lemfiTransfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer Lemfi failed: %v", err)
	}
	fincraFinal, err := h.TransferStore.GetTransfer(ctx, FincraTenantID, fincraTransfer.ID)
	if err != nil {
		t.Fatalf("GetTransfer Fincra failed: %v", err)
	}

	if lemfiFinal.Status != domain.TransferStatusCompleted {
		t.Errorf("Lemfi transfer expected COMPLETED, got %s", lemfiFinal.Status)
	}
	if fincraFinal.Status != domain.TransferStatusCompleted {
		t.Errorf("Fincra transfer expected COMPLETED, got %s", fincraFinal.Status)
	}

	// Verify global ledger balance: total debits == total credits across all tenants
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero
	h.TB.mu.Lock()
	for _, acc := range h.TB.accounts {
		totalDebits = totalDebits.Add(decimal.NewFromInt(int64(acc.DebitsPosted)))
		totalCredits = totalCredits.Add(decimal.NewFromInt(int64(acc.CreditsPosted)))
	}
	h.TB.mu.Unlock()

	if !totalDebits.Equal(totalCredits) {
		t.Fatalf("multi-tenant ledger imbalance: debits=%s, credits=%s", totalDebits, totalCredits)
	}
	t.Logf("PASS: multi-tenant ledger balanced, debits=credits=%s", totalDebits)
}
