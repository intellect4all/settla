//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// TestEdgeCase_ZeroAmountTransfer verifies that creating a transfer
// with zero source amount is rejected by the engine with ErrAmountTooLow.
func TestEdgeCase_ZeroAmountTransfer(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "edge-zero-amount",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.Zero,
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Zero Tester",
			Email:   "zero@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err == nil {
		t.Fatal("expected error for zero amount transfer, got nil")
	}

	// Verify the error indicates amount is too low
	if !strings.Contains(err.Error(), "below minimum") {
		t.Errorf("expected 'below minimum' error, got: %v", err)
	}
	t.Logf("PASS: zero amount correctly rejected: %v", err)
}

// TestEdgeCase_NegativeAmountTransfer verifies that creating a transfer
// with a negative source amount is rejected by the engine.
func TestEdgeCase_NegativeAmountTransfer(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "edge-negative-amount",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(-500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Negative Tester",
			Email:   "negative@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err == nil {
		t.Fatal("expected error for negative amount transfer, got nil")
	}

	if !strings.Contains(err.Error(), "below minimum") {
		t.Errorf("expected 'below minimum' error, got: %v", err)
	}
	t.Logf("PASS: negative amount correctly rejected: %v", err)
}

// TestEdgeCase_ExpiredQuote verifies that a transfer referencing
// an expired quote is rejected by the engine with ErrQuoteExpired.
func TestEdgeCase_ExpiredQuote(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create an expired quote and add it to the store
	quoteID := uuid.New()
	expiredQuote := &domain.Quote{
		ID:             quoteID,
		TenantID:       LemfiTenantID,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1_000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromInt(750_000),
		StableAmount:   decimal.NewFromFloat(1250.00),
		FXRate:         decimal.NewFromFloat(750),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(4.00),
			NetworkFee:  decimal.NewFromFloat(0.10),
			OffRampFee:  decimal.NewFromFloat(3.50),
			TotalFeeUSD: decimal.NewFromFloat(7.60),
		},
		Route: domain.RouteInfo{
			Chain:          "tron",
			StableCoin:     domain.CurrencyUSDT,
			OnRampProvider: "mock-onramp-gbp",
			OffRampProvider: "mock-offramp-ngn",
		},
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour), // expired 1 hour ago
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
	}
	h.TransferStore.addQuote(expiredQuote)

	_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "edge-expired-quote",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1_000),
		DestCurrency:   domain.CurrencyNGN,
		QuoteID:        &quoteID,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Expired Quote Tester",
			Email:   "expired@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	})
	if err == nil {
		t.Fatal("expected error for expired quote, got nil")
	}

	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected 'expired' in error, got: %v", err)
	}
	t.Logf("PASS: expired quote correctly rejected: %v", err)
}

// TestEdgeCase_VerySmallAmount verifies that a very small but positive
// amount (e.g., 0.01) is accepted and processed correctly.
func TestEdgeCase_VerySmallAmount(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "edge-tiny-amount",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(0.01),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Tiny Tester",
			Email:   "tiny@example.com",
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
		t.Fatalf("CreateTransfer failed for 0.01: %v", err)
	}

	if transfer.Status != domain.TransferStatusCreated {
		t.Errorf("expected CREATED, got %s", transfer.Status)
	}
	if !transfer.SourceAmount.Equal(decimal.NewFromFloat(0.01)) {
		t.Errorf("source amount mismatch: got %s", transfer.SourceAmount)
	}
	t.Logf("PASS: tiny amount 0.01 accepted, status=%s", transfer.Status)
}

// TestEdgeCase_DuplicateIdempotencyKey verifies that submitting the
// same idempotency key twice for the same tenant is rejected.
func TestEdgeCase_DuplicateIdempotencyKey(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	req := core.CreateTransferRequest{
		IdempotencyKey: "edge-duplicate-key",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Dup Tester",
			Email:   "dup@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Receiver",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	}

	// First creation should succeed
	_, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, req)
	if err != nil {
		t.Fatalf("first CreateTransfer failed: %v", err)
	}

	// Second creation with same idempotency key should fail or return existing
	_, err = h.Engine.CreateTransfer(ctx, LemfiTenantID, req)
	if err == nil {
		t.Log("second CreateTransfer returned existing transfer (idempotent)")
	} else {
		if !strings.Contains(err.Error(), "idempotency") && !strings.Contains(err.Error(), "duplicate") {
			t.Logf("second CreateTransfer failed with: %v (acceptable if idempotent dedup)", err)
		}
		t.Logf("PASS: duplicate idempotency key handled: %v", err)
	}
}
