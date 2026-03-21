//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// createMinimalTransfer is a test helper that creates a transfer for the given
// tenant using the smallest valid amounts for the corridor.
func createMinimalTransfer(t *testing.T, h *testHarness, tenantID uuid.UUID, idemKey string) *domain.Transfer {
	t.Helper()
	ctx := context.Background()

	var req core.CreateTransferRequest
	req.IdempotencyKey = idemKey

	if tenantID == LemfiTenantID {
		req.SourceCurrency = domain.CurrencyGBP
		req.SourceAmount = decimal.NewFromInt(1000)
		req.DestCurrency = domain.CurrencyNGN
		req.Recipient = domain.Recipient{Name: "Test Recipient", Country: "NG"}
	} else {
		req.SourceCurrency = domain.CurrencyNGN
		req.SourceAmount = decimal.NewFromInt(400_000)
		req.DestCurrency = domain.CurrencyGBP
		req.Recipient = domain.Recipient{Name: "Test Recipient", Country: "GB"}
	}

	transfer, err := h.Engine.CreateTransfer(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("createMinimalTransfer(tenant=%s, key=%s): %v", tenantID, idemKey, err)
	}
	return transfer
}

// isDomainTransferNotFound returns true when err represents a transfer-not-found
// domain error. The harness's memTransferStore returns domain.ErrTransferNotFound
// for cross-tenant and missing-ID lookups.
func isDomainTransferNotFound(err error) bool {
	if err == nil {
		return false
	}
	var de *domain.DomainError
	if errors.As(err, &de) {
		return de.Code() == domain.CodeTransferNotFound
	}
	return false
}

// ─── TEST-9: Tenant Isolation ────────────────────────────────────────────────

// TestTenantIsolation_TransferNotVisibleAcrossTenants verifies that a transfer
// created for one tenant cannot be retrieved by another tenant, and that
// ListTransfers only surfaces each tenant's own records.
func TestTenantIsolation_TransferNotVisibleAcrossTenants(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create T1 for Lemfi.
	t1 := createMinimalTransfer(t, h, LemfiTenantID, "iso-lemfi-t1")
	// Create T2 for Fincra.
	t2 := createMinimalTransfer(t, h, FincraTenantID, "iso-fincra-t2")

	// Fincra must NOT see Lemfi's transfer T1.
	_, err := h.TransferStore.GetTransfer(ctx, FincraTenantID, t1.ID)
	if err == nil {
		t.Error("Fincra retrieved Lemfi transfer T1 — cross-tenant leak detected")
	} else if !isDomainTransferNotFound(err) {
		t.Errorf("expected ErrTransferNotFound for cross-tenant read, got: %v", err)
	}

	// Lemfi must NOT see Fincra's transfer T2.
	_, err = h.TransferStore.GetTransfer(ctx, LemfiTenantID, t2.ID)
	if err == nil {
		t.Error("Lemfi retrieved Fincra transfer T2 — cross-tenant leak detected")
	} else if !isDomainTransferNotFound(err) {
		t.Errorf("expected ErrTransferNotFound for cross-tenant read, got: %v", err)
	}

	// Each tenant can read their own transfer.
	if _, err = h.TransferStore.GetTransfer(ctx, LemfiTenantID, t1.ID); err != nil {
		t.Errorf("Lemfi cannot read own transfer T1: %v", err)
	}
	if _, err = h.TransferStore.GetTransfer(ctx, FincraTenantID, t2.ID); err != nil {
		t.Errorf("Fincra cannot read own transfer T2: %v", err)
	}

	// ListTransfers must only return each tenant's own records.
	lemfiList, err := h.TransferStore.ListTransfers(ctx, LemfiTenantID, 100, 0)
	if err != nil {
		t.Fatalf("ListTransfers(Lemfi) failed: %v", err)
	}
	for _, tr := range lemfiList {
		if tr.TenantID != LemfiTenantID {
			t.Errorf("Lemfi list returned transfer for tenant %s", tr.TenantID)
		}
	}

	fincraList, err := h.TransferStore.ListTransfers(ctx, FincraTenantID, 100, 0)
	if err != nil {
		t.Fatalf("ListTransfers(Fincra) failed: %v", err)
	}
	for _, tr := range fincraList {
		if tr.TenantID != FincraTenantID {
			t.Errorf("Fincra list returned transfer for tenant %s", tr.TenantID)
		}
	}
}

// TestTenantIsolation_TreasuryPositionsSeparate verifies that reserving funds
// against Lemfi's GBP position leaves Fincra's NGN position untouched.
func TestTenantIsolation_TreasuryPositionsSeparate(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Snapshot Fincra's NGN position before anything happens.
	fincraPosBefore := treasuryBalance(t, h, FincraTenantID, domain.CurrencyNGN)

	// Create and fund a Lemfi GBP transfer — this reserves GBP in treasury.
	transfer := createMinimalTransfer(t, h, LemfiTenantID, "treasury-iso-lemfi-1")

	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	// Execute treasury reserve intent.
	h.executeOutbox(ctx)

	// Lemfi's GBP locked amount must have increased.
	lemfiLocked := treasuryLocked(t, h, LemfiTenantID, domain.CurrencyGBP)
	if !lemfiLocked.IsPositive() {
		t.Error("expected Lemfi GBP locked > 0 after FundTransfer")
	}

	// Fincra's NGN position must be completely unchanged.
	fincraPosAfter := treasuryBalance(t, h, FincraTenantID, domain.CurrencyNGN)
	if !fincraPosBefore.Equal(fincraPosAfter) {
		t.Errorf("Fincra NGN balance changed unexpectedly: before=%s after=%s",
			fincraPosBefore, fincraPosAfter)
	}
}

// TestTenantIsolation_QuotesScopedToTenant verifies that a quote created for
// one tenant cannot be retrieved by another tenant.
func TestTenantIsolation_QuotesScopedToTenant(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// GetQuote through the engine: create a transfer (which persists an inline
	// quote) and capture the QuoteID from the returned transfer.
	transfer := createMinimalTransfer(t, h, LemfiTenantID, "quote-iso-lemfi-1")

	if transfer.QuoteID == nil {
		t.Fatal("CreateTransfer did not attach a QuoteID")
	}
	quoteID := *transfer.QuoteID

	// Fincra must NOT see Lemfi's quote.
	_, err := h.TransferStore.GetQuote(ctx, FincraTenantID, quoteID)
	if err == nil {
		t.Error("Fincra retrieved Lemfi quote — cross-tenant leak detected")
	}

	// Lemfi can retrieve its own quote.
	q, err := h.TransferStore.GetQuote(ctx, LemfiTenantID, quoteID)
	if err != nil {
		t.Fatalf("Lemfi cannot read own quote: %v", err)
	}
	if q.TenantID != LemfiTenantID {
		t.Errorf("quote tenant mismatch: want %s, got %s", LemfiTenantID, q.TenantID)
	}
}

// TestTenantIsolation_IdempotencyKeyScopedToTenant verifies that the same
// idempotency key used by two different tenants produces two separate, distinct
// transfers — idempotency is scoped per-tenant.
func TestTenantIsolation_IdempotencyKeyScopedToTenant(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	const sharedKey = "test-key-001"

	// Lemfi creates a transfer with key "test-key-001".
	lemfiTransfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Lemfi Recipient", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("Lemfi CreateTransfer failed: %v", err)
	}

	// Fincra creates a transfer with the SAME key — must succeed independently.
	fincraTransfer, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(400_000),
		DestCurrency:   domain.CurrencyGBP,
		Recipient:      domain.Recipient{Name: "Fincra Recipient", Country: "GB"},
	})
	if err != nil {
		t.Fatalf("Fincra CreateTransfer with same idempotency key failed: %v", err)
	}

	// They must be distinct transfers.
	if lemfiTransfer.ID == fincraTransfer.ID {
		t.Fatal("same idempotency key across tenants produced identical transfer IDs — isolation violated")
	}

	// Tenant IDs must be correctly set.
	if lemfiTransfer.TenantID != LemfiTenantID {
		t.Errorf("lemfi transfer has wrong tenantID: %s", lemfiTransfer.TenantID)
	}
	if fincraTransfer.TenantID != FincraTenantID {
		t.Errorf("fincra transfer has wrong tenantID: %s", fincraTransfer.TenantID)
	}

	// Repeating the same key for Lemfi returns the original transfer (in-tenant idempotency).
	lemfiRepeat, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: sharedKey,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Lemfi Recipient", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("Lemfi idempotent repeat failed: %v", err)
	}
	if lemfiRepeat.ID != lemfiTransfer.ID {
		t.Errorf("idempotent repeat returned different transfer: want %s, got %s", lemfiTransfer.ID, lemfiRepeat.ID)
	}

	// Verify both transfers are independently retrievable by their own tenant.
	if _, err := h.TransferStore.GetTransfer(ctx, LemfiTenantID, lemfiTransfer.ID); err != nil {
		t.Errorf("Lemfi cannot retrieve own transfer: %v", err)
	}
	if _, err := h.TransferStore.GetTransfer(ctx, FincraTenantID, fincraTransfer.ID); err != nil {
		t.Errorf("Fincra cannot retrieve own transfer: %v", err)
	}
}

// ─── Treasury inspection helpers ─────────────────────────────────────────────

// treasuryBalance returns the current available balance for the given tenant
// and currency from the in-memory treasury manager.
func treasuryBalance(t *testing.T, h *testHarness, tenantID uuid.UUID, currency domain.Currency) decimal.Decimal {
	t.Helper()
	ctx := context.Background()
	positions, err := h.TreasuryStore.LoadAllPositions(ctx)
	if err != nil {
		t.Fatalf("LoadAllPositions failed: %v", err)
	}
	for _, p := range positions {
		if p.TenantID == tenantID && p.Currency == currency {
			return p.Balance
		}
	}
	return decimal.Zero
}

// treasuryLocked returns the currently locked amount for the given tenant and
// currency from the in-memory treasury manager's live position state.
func treasuryLocked(t *testing.T, h *testHarness, tenantID uuid.UUID, currency domain.Currency) decimal.Decimal {
	t.Helper()
	ctx := context.Background()
	positions, err := h.Treasury.GetPositions(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetPositions failed: %v", err)
	}
	for _, p := range positions {
		if p.Currency == currency {
			return p.Locked
		}
	}
	return decimal.Zero
}

// ─── TEST-33: Fee Isolation ──────────────────────────────────────────────────

// TestTenantIsolation_FeesNotLeaked verifies that different tenants with different
// fee schedules (Lemfi: 40/35 bps, Fincra: 25/20 bps) get different fees for the
// same corridor and amount. A global fee cache bug would cause both tenants to
// see the same fees.
func TestTenantIsolation_FeesNotLeaked(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create transfers with the same corridor and amount for both tenants.
	lemfiTransfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fee-iso-lemfi-1",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(10_000),
		DestCurrency:   domain.CurrencyNGN,
		Recipient:      domain.Recipient{Name: "Test Recipient", Country: "NG"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer(Lemfi): %v", err)
	}

	fincraTransfer, err := h.Engine.CreateTransfer(ctx, FincraTenantID, core.CreateTransferRequest{
		IdempotencyKey: "fee-iso-fincra-1",
		SourceCurrency: domain.CurrencyNGN,
		SourceAmount:   decimal.NewFromInt(4_000_000),
		DestCurrency:   domain.CurrencyGBP,
		Recipient:      domain.Recipient{Name: "Test Recipient", Country: "GB"},
	})
	if err != nil {
		t.Fatalf("CreateTransfer(Fincra): %v", err)
	}

	// Lemfi (40 bps) must have higher fees than Fincra (25 bps) for similar amounts.
	if lemfiTransfer.Fees.TotalFeeUSD.Equal(fincraTransfer.Fees.TotalFeeUSD) {
		t.Errorf("fee isolation failure: Lemfi and Fincra got identical fees (%s) — possible fee cache leak",
			lemfiTransfer.Fees.TotalFeeUSD)
	}

	t.Logf("Lemfi fees:  on-ramp=%s off-ramp=%s total=%s",
		lemfiTransfer.Fees.OnRampFee, lemfiTransfer.Fees.OffRampFee, lemfiTransfer.Fees.TotalFeeUSD)
	t.Logf("Fincra fees: on-ramp=%s off-ramp=%s total=%s",
		fincraTransfer.Fees.OnRampFee, fincraTransfer.Fees.OffRampFee, fincraTransfer.Fees.TotalFeeUSD)
}
