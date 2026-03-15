//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/core/recovery"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// newDetector constructs a recovery.Detector wired to the harness's in-memory
// stores. The detector interval is irrelevant for tests because we call
// RunOnce directly.
func newDetector(t *testing.T, h *testHarness, reviewStore *mockReviewStore, checker *mockProviderStatusChecker) *recovery.Detector {
	t.Helper()
	logger := observability.NewLogger("settla-recovery-test", "test")
	stuckStore := &memStuckTransferStore{inner: h.TransferStore}
	return recovery.NewDetector(stuckStore, reviewStore, h.Engine, checker, logger)
}

// makeStuckTransfer creates a transfer for the given tenant and then directly
// sets its Status and UpdatedAt in the in-memory store to simulate a stuck
// transfer that has not been touched for stuckAge.
func makeStuckTransfer(t *testing.T, h *testHarness, tenantID uuid.UUID, status domain.TransferStatus, stuckAge time.Duration) *domain.Transfer {
	t.Helper()
	ctx := context.Background()

	transfer, err := h.Engine.CreateTransfer(ctx, tenantID, core.CreateTransferRequest{
		IdempotencyKey: "stuck-" + uuid.New().String(),
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(500),
		DestCurrency:   domain.CurrencyNGN,
		Recipient: domain.Recipient{
			Name:    "Stuck Recipient",
			Country: "NG",
		},
	})
	if err != nil {
		t.Fatalf("makeStuckTransfer: CreateTransfer failed: %v", err)
	}

	// Directly mutate the in-memory record so it appears stuck.
	h.TransferStore.mu.Lock()
	tr := h.TransferStore.transfers[transfer.ID]
	tr.Status = status
	tr.UpdatedAt = time.Now().UTC().Add(-stuckAge)
	h.TransferStore.transfers[transfer.ID] = tr
	h.TransferStore.mu.Unlock()

	return transfer
}

// TestRecoveryDetector_EscalatesToManualReview verifies that a transfer stuck
// in ON_RAMPING for longer than the escalation threshold (60 min) causes the
// detector to call CreateManualReview exactly once.
func TestRecoveryDetector_EscalatesToManualReview(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a transfer and force it into ON_RAMPING, stuck for 2 hours —
	// well past the 60-minute escalation threshold.
	transfer := makeStuckTransfer(t, h, LemfiTenantID, domain.TransferStatusOnRamping, 2*time.Hour)

	reviewStore := &mockReviewStore{}
	checker := &mockProviderStatusChecker{} // returns "pending" by default
	detector := newDetector(t, h, reviewStore, checker)

	if err := detector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	reviewStore.mu.Lock()
	reviews := reviewStore.reviews
	reviewStore.mu.Unlock()

	if len(reviews) == 0 {
		t.Fatal("expected CreateManualReview to be called for stuck ON_RAMPING transfer, got none")
	}

	var found bool
	for _, r := range reviews {
		if r.transferID == transfer.ID {
			found = true
			if r.tenantID != LemfiTenantID {
				t.Errorf("review tenant mismatch: want %s, got %s", LemfiTenantID, r.tenantID)
			}
			if r.transferStatus != string(domain.TransferStatusOnRamping) {
				t.Errorf("review status mismatch: want %s, got %s", domain.TransferStatusOnRamping, r.transferStatus)
			}
		}
	}
	if !found {
		t.Errorf("no review found for transfer %s; all reviews: %+v", transfer.ID, reviews)
	}
}

// TestRecoveryDetector_SkipsAlreadyReviewedTransfer verifies that the detector
// does not create a duplicate manual review when one already exists for a stuck
// transfer (idempotent escalation).
func TestRecoveryDetector_SkipsAlreadyReviewedTransfer(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Stuck for 2 hours — past the escalation threshold.
	transfer := makeStuckTransfer(t, h, LemfiTenantID, domain.TransferStatusOnRamping, 2*time.Hour)

	// Pre-populate the review store so HasActiveReview returns true.
	reviewStore := &mockReviewStore{}
	reviewStore.reviews = []mockReview{
		{
			transferID:     transfer.ID,
			tenantID:       LemfiTenantID,
			transferStatus: string(domain.TransferStatusOnRamping),
			stuckSince:     transfer.UpdatedAt,
		},
	}

	checker := &mockProviderStatusChecker{}
	detector := newDetector(t, h, reviewStore, checker)

	if err := detector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	reviewStore.mu.Lock()
	reviewCount := len(reviewStore.reviews)
	reviewStore.mu.Unlock()

	// The store was pre-seeded with 1 review; it should still have exactly 1.
	if reviewCount != 1 {
		t.Errorf("expected exactly 1 review (no duplicate), got %d", reviewCount)
	}
}

// TestRecoveryDetector_SkipsCompletedTransfers verifies that COMPLETED and
// FAILED transfers with old UpdatedAt are not escalated — the detector only
// acts on non-terminal in-progress states.
func TestRecoveryDetector_SkipsCompletedTransfers(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// These are terminal states — the detector's DefaultThresholds has no entry
	// for COMPLETED or FAILED, so ListStuckTransfers will never be called for
	// them. We add them directly and verify zero reviews are created.
	_ = makeStuckTransfer(t, h, LemfiTenantID, domain.TransferStatusCompleted, 3*time.Hour)
	_ = makeStuckTransfer(t, h, FincraTenantID, domain.TransferStatusFailed, 3*time.Hour)

	reviewStore := &mockReviewStore{}
	checker := &mockProviderStatusChecker{}
	detector := newDetector(t, h, reviewStore, checker)

	if err := detector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	reviewStore.mu.Lock()
	reviewCount := len(reviewStore.reviews)
	reviewStore.mu.Unlock()

	if reviewCount != 0 {
		t.Errorf("expected 0 reviews for terminal-state transfers, got %d", reviewCount)
	}
}
