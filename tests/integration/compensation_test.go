//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/core/compensation"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// ─── Mock Compensation Store ────────────────────────────────────────────────

type memCompensationStore struct {
	mu      sync.Mutex
	records map[uuid.UUID]*compensationRecord
	// byTransfer tracks records by transfer ID for idempotency checks.
	byTransfer map[uuid.UUID]uuid.UUID
}

type compensationRecord struct {
	ID             uuid.UUID
	TransferID     uuid.UUID
	TenantID       uuid.UUID
	Strategy       string
	RefundAmount   decimal.Decimal
	RefundCurrency string
	StepsCompleted []byte
	StepsFailed    []byte
	FXLoss         decimal.Decimal
	Status         string
}

var _ compensation.CompensationStore = (*memCompensationStore)(nil)

func newMemCompensationStore() *memCompensationStore {
	return &memCompensationStore{
		records:    make(map[uuid.UUID]*compensationRecord),
		byTransfer: make(map[uuid.UUID]uuid.UUID),
	}
}

func (s *memCompensationStore) CreateCompensationRecord(ctx context.Context, params compensation.CreateCompensationParams) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce unique constraint: one compensation record per transfer.
	if existingID, ok := s.byTransfer[params.TransferID]; ok {
		return existingID, nil
	}

	id := uuid.New()
	s.records[id] = &compensationRecord{
		ID:             id,
		TransferID:     params.TransferID,
		TenantID:       params.TenantID,
		Strategy:       params.Strategy,
		RefundAmount:   params.RefundAmount,
		RefundCurrency: params.RefundCurrency,
		Status:         "in_progress",
	}
	s.byTransfer[params.TransferID] = id
	return id, nil
}

func (s *memCompensationStore) UpdateCompensationRecord(ctx context.Context, id uuid.UUID, stepsCompleted, stepsFailed []byte, fxLoss decimal.Decimal, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return nil
	}
	rec.StepsCompleted = stepsCompleted
	rec.StepsFailed = stepsFailed
	rec.FXLoss = fxLoss
	rec.Status = status
	return nil
}

func (s *memCompensationStore) recordCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func (s *memCompensationStore) getByTransfer(transferID uuid.UUID) *compensationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byTransfer[transferID]
	if !ok {
		return nil
	}
	return s.records[id]
}

// ─── Helper: create and advance a transfer to a given state ─────────────────

func createTransferAtStatus(t *testing.T, h *testHarness, ctx context.Context, status domain.TransferStatus) *domain.Transfer {
	t.Helper()

	transfer, err := h.Engine.CreateTransfer(ctx, LemfiTenantID, core.CreateTransferRequest{
		IdempotencyKey: uuid.New().String(),
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
	if status == domain.TransferStatusCreated {
		return transfer
	}

	// CREATED → FUNDED
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer failed: %v", err)
	}
	h.executeOutbox(ctx)
	if status == domain.TransferStatusFunded {
		return reloadTransfer(t, h, ctx, transfer.ID)
	}

	// FUNDED → ON_RAMPING
	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp failed: %v", err)
	}
	if status == domain.TransferStatusOnRamping {
		return reloadTransfer(t, h, ctx, transfer.ID)
	}

	// ON_RAMPING → SETTLING
	if err := h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("HandleOnRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)
	if status == domain.TransferStatusSettling {
		return reloadTransfer(t, h, ctx, transfer.ID)
	}

	// SETTLING → OFF_RAMPING
	if err := h.Engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true, TxHash: "0xtest"}); err != nil {
		t.Fatalf("HandleSettlementResult failed: %v", err)
	}
	h.executeOutbox(ctx)
	if status == domain.TransferStatusOffRamping {
		return reloadTransfer(t, h, ctx, transfer.ID)
	}

	// OFF_RAMPING → COMPLETED
	if err := h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("HandleOffRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)
	if status == domain.TransferStatusCompleted {
		return reloadTransfer(t, h, ctx, transfer.ID)
	}

	t.Fatalf("unsupported target status: %s", status)
	return nil
}

func reloadTransfer(t *testing.T, h *testHarness, ctx context.Context, id uuid.UUID) *domain.Transfer {
	t.Helper()
	tr, err := h.TransferStore.GetTransfer(ctx, uuid.Nil, id)
	if err != nil {
		t.Fatalf("GetTransfer failed: %v", err)
	}
	return tr
}

// ─── Test: Simple Refund Compensation ───────────────────────────────────────

// TestCompensationSimpleRefund verifies the simple refund compensation flow:
// Create → Fund → OnRamp succeeds → OffRamp fails → compensation selects
// SIMPLE_REFUND → engine.FailTransfer → engine.InitiateRefund → treasury
// released → ledger reversed.
func TestCompensationSimpleRefund(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	compStore := newMemCompensationStore()

	// Advance transfer to OFF_RAMPING state.
	transfer := createTransferAtStatus(t, h, ctx, domain.TransferStatusOffRamping)

	// Simulate off-ramp failure.
	err := h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "provider_timeout",
	})
	if err != nil {
		t.Fatalf("HandleOffRampResult failed: %v", err)
	}

	// Execute outbox to process treasury release and ledger reverse intents.
	h.executeOutbox(ctx)

	// Reload the transfer — it should be FAILED now.
	failed := reloadTransfer(t, h, ctx, transfer.ID)
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED, got %s", failed.Status)
	}

	// Determine compensation plan.
	tenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}

	// The off-ramp failed after on-ramp completed. With no on-ramp in
	// completedSteps and the transfer already FAILED, DetermineCompensation
	// selects SIMPLE_REFUND.
	plan := compensation.DetermineCompensation(failed, tenant, []string{
		compensation.StepFunded,
	}, compensation.ExternalStatus{})

	if plan.Strategy != compensation.StrategySimpleRefund {
		t.Fatalf("expected SIMPLE_REFUND strategy, got %s", plan.Strategy)
	}
	if !plan.RefundAmount.Equal(transfer.SourceAmount) {
		t.Fatalf("expected refund amount %s, got %s", transfer.SourceAmount, plan.RefundAmount)
	}

	// Execute the compensation plan.
	executor := compensation.NewExecutor(compStore, h.Engine, observability.NewLogger("settla-compensation-test", "test"))
	if err := executor.Execute(ctx, plan); err != nil {
		t.Fatalf("Executor.Execute failed: %v", err)
	}

	// Verify compensation record was created.
	rec := compStore.getByTransfer(transfer.ID)
	if rec == nil {
		t.Fatal("expected compensation record to be created")
	}
	if rec.Strategy != string(compensation.StrategySimpleRefund) {
		t.Fatalf("expected strategy SIMPLE_REFUND, got %s", rec.Strategy)
	}
	if rec.Status != "completed" {
		t.Fatalf("expected compensation status completed, got %s", rec.Status)
	}

	// Verify transfer transitioned to REFUNDING via InitiateRefund.
	refunding := reloadTransfer(t, h, ctx, transfer.ID)
	if refunding.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING, got %s", refunding.Status)
	}

	// Execute outbox to process the refund's treasury release and ledger reverse intents.
	h.executeOutbox(ctx)

	// Verify outbox entries include treasury release and ledger reverse intents.
	events, err := h.TransferStore.GetTransferEvents(ctx, LemfiTenantID, transfer.ID)
	if err != nil {
		t.Fatalf("GetTransferEvents failed: %v", err)
	}

	statusSet := make(map[domain.TransferStatus]bool)
	for _, e := range events {
		statusSet[e.ToStatus] = true
	}
	if !statusSet[domain.TransferStatusFailed] {
		t.Error("expected transition to FAILED in events")
	}
	if !statusSet[domain.TransferStatusRefunding] {
		t.Error("expected transition to REFUNDING in events")
	}

	t.Logf("Simple refund compensation completed for transfer %s", transfer.ID)
}

// ─── Test: Reverse OnRamp Compensation ──────────────────────────────────────

// TestCompensationReverseOnRamp verifies that when on-ramp completed but
// off-ramp fails, the compensation system selects REVERSE_ONRAMP strategy.
func TestCompensationReverseOnRamp(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	compStore := newMemCompensationStore()

	// Advance transfer to OFF_RAMPING (on-ramp and settlement completed).
	transfer := createTransferAtStatus(t, h, ctx, domain.TransferStatusOffRamping)

	// Simulate off-ramp failure.
	err := h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "beneficiary_account_closed",
	})
	if err != nil {
		t.Fatalf("HandleOffRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Reload the failed transfer.
	failed := reloadTransfer(t, h, ctx, transfer.ID)
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED, got %s", failed.Status)
	}

	// Determine compensation with on-ramp completed in the step list.
	tenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}

	plan := compensation.DetermineCompensation(failed, tenant, []string{
		compensation.StepFunded,
		compensation.StepOnRampCompleted,
	}, compensation.ExternalStatus{})

	if plan.Strategy != compensation.StrategyReverseOnRamp {
		t.Fatalf("expected REVERSE_ONRAMP strategy, got %s", plan.Strategy)
	}
	if plan.RefundCurrency != transfer.SourceCurrency {
		t.Fatalf("expected refund currency %s, got %s", transfer.SourceCurrency, plan.RefundCurrency)
	}

	// Execute the compensation plan.
	executor := compensation.NewExecutor(compStore, h.Engine, observability.NewLogger("settla-compensation-test", "test"))

	// The executor calls FailTransfer, but the transfer is already FAILED.
	// FailTransfer should fail since FAILED→FAILED is not a valid transition.
	// However, executeReverseOnRamp calls FailTransfer which will error.
	// This tests the actual behavior: the executor returns an error when
	// trying to fail an already-failed transfer.
	execErr := executor.Execute(ctx, plan)

	// The compensation record should still be created even if execution fails.
	rec := compStore.getByTransfer(transfer.ID)
	if rec == nil {
		t.Fatal("expected compensation record to be created")
	}
	if rec.Strategy != string(compensation.StrategyReverseOnRamp) {
		t.Fatalf("expected strategy REVERSE_ONRAMP, got %s", rec.Strategy)
	}

	// If the transfer is already FAILED, the executor's FailTransfer call
	// will error because FAILED→FAILED is not a valid transition.
	// This is expected behavior — in production, the plan would be created
	// with TransferStatus=OFF_RAMPING (before the explicit HandleOffRampResult).
	if execErr != nil {
		t.Logf("Expected error from executing REVERSE_ONRAMP on already-failed transfer: %v", execErr)
		if rec.Status != "failed" {
			t.Fatalf("expected compensation status 'failed', got %s", rec.Status)
		}
	}

	// Now test the happy path: create a plan BEFORE the transfer transitions to FAILED.
	h2 := newTestHarness(t)
	compStore2 := newMemCompensationStore()

	transfer2 := createTransferAtStatus(t, h2, ctx, domain.TransferStatusOffRamping)
	tenant2, _ := h2.TenantStore.GetTenant(ctx, LemfiTenantID)

	// Build the plan while transfer is still OFF_RAMPING.
	tr2 := reloadTransfer(t, h2, ctx, transfer2.ID)
	plan2 := compensation.DetermineCompensation(tr2, tenant2, []string{
		compensation.StepFunded,
		compensation.StepOnRampCompleted,
	}, compensation.ExternalStatus{})

	if plan2.Strategy != compensation.StrategyReverseOnRamp {
		t.Fatalf("expected REVERSE_ONRAMP strategy for pre-failure plan, got %s", plan2.Strategy)
	}

	// Execute: FailTransfer transitions OFF_RAMPING → FAILED.
	executor2 := compensation.NewExecutor(compStore2, h2.Engine, observability.NewLogger("settla-compensation-test", "test"))
	if err := executor2.Execute(ctx, plan2); err != nil {
		t.Fatalf("Executor.Execute (pre-failure) failed: %v", err)
	}

	rec2 := compStore2.getByTransfer(transfer2.ID)
	if rec2 == nil {
		t.Fatal("expected compensation record to be created")
	}
	if rec2.Status != "completed" {
		t.Fatalf("expected compensation status completed, got %s", rec2.Status)
	}

	failed2 := reloadTransfer(t, h2, ctx, transfer2.ID)
	if failed2.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after REVERSE_ONRAMP compensation, got %s", failed2.Status)
	}

	t.Logf("Reverse on-ramp compensation verified for transfers %s and %s", transfer.ID, transfer2.ID)
}

// ─── Test: Manual Review Compensation ───────────────────────────────────────

// TestCompensationManualReview verifies that a transfer stuck in ON_RAMPING
// without known provider status escalates to MANUAL_REVIEW strategy.
func TestCompensationManualReview(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	compStore := newMemCompensationStore()
	reviewStore := &mockReviewStore{}

	// Advance transfer to ON_RAMPING.
	transfer := createTransferAtStatus(t, h, ctx, domain.TransferStatusOnRamping)

	// Simulate the transfer being stuck (set UpdatedAt to 2 minutes ago).
	h.TransferStore.mu.Lock()
	tr := h.TransferStore.transfers[transfer.ID]
	tr.UpdatedAt = time.Now().UTC().Add(-2 * time.Minute)
	h.TransferStore.mu.Unlock()

	// Reload to get current state.
	stuck := reloadTransfer(t, h, ctx, transfer.ID)
	if stuck.Status != domain.TransferStatusOnRamping {
		t.Fatalf("expected ON_RAMPING, got %s", stuck.Status)
	}

	// Determine compensation with no external status (ambiguous).
	tenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}

	plan := compensation.DetermineCompensation(stuck, tenant, []string{
		compensation.StepFunded,
	}, compensation.ExternalStatus{}) // No provider status — ambiguous

	if plan.Strategy != compensation.StrategyManualReview {
		t.Fatalf("expected MANUAL_REVIEW strategy, got %s", plan.Strategy)
	}

	// Execute the compensation plan.
	executor := compensation.NewExecutor(compStore, h.Engine, observability.NewLogger("settla-compensation-test", "test"))
	if err := executor.Execute(ctx, plan); err != nil {
		t.Fatalf("Executor.Execute failed: %v", err)
	}

	// Verify compensation record was created with pending_review status.
	rec := compStore.getByTransfer(transfer.ID)
	if rec == nil {
		t.Fatal("expected compensation record to be created")
	}
	if rec.Strategy != string(compensation.StrategyManualReview) {
		t.Fatalf("expected strategy MANUAL_REVIEW, got %s", rec.Strategy)
	}
	if rec.Status != "pending_review" {
		t.Fatalf("expected compensation status pending_review, got %s", rec.Status)
	}

	// Simulate creating a manual review record (as the recovery detector would).
	err = reviewStore.CreateManualReview(ctx, stuck.ID, stuck.TenantID, string(stuck.Status), stuck.UpdatedAt)
	if err != nil {
		t.Fatalf("CreateManualReview failed: %v", err)
	}

	// Verify manual review record exists.
	hasReview, err := reviewStore.HasActiveReview(ctx, stuck.ID)
	if err != nil {
		t.Fatalf("HasActiveReview failed: %v", err)
	}
	if !hasReview {
		t.Fatal("expected manual review record to exist")
	}

	// Verify the transfer is still in ON_RAMPING (manual review does not
	// transition the state — a human must resolve it).
	stillStuck := reloadTransfer(t, h, ctx, transfer.ID)
	if stillStuck.Status != domain.TransferStatusOnRamping {
		t.Fatalf("expected ON_RAMPING (manual review should not change state), got %s", stillStuck.Status)
	}

	t.Logf("Manual review compensation verified for transfer %s", transfer.ID)
}

// ─── Test: Compensation Idempotency ─────────────────────────────────────────

// TestCompensationIdempotency verifies that executing the same compensation
// plan twice creates only one compensation record (idempotency via unique
// constraint on transfer_id).
func TestCompensationIdempotency(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()
	compStore := newMemCompensationStore()

	// Advance transfer to OFF_RAMPING, then fail the off-ramp.
	transfer := createTransferAtStatus(t, h, ctx, domain.TransferStatusOffRamping)

	err := h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "network_error",
	})
	if err != nil {
		t.Fatalf("HandleOffRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)

	failed := reloadTransfer(t, h, ctx, transfer.ID)
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED, got %s", failed.Status)
	}

	// Build compensation plan.
	tenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}

	plan := compensation.DetermineCompensation(failed, tenant, []string{
		compensation.StepFunded,
	}, compensation.ExternalStatus{})

	if plan.Strategy != compensation.StrategySimpleRefund {
		t.Fatalf("expected SIMPLE_REFUND, got %s", plan.Strategy)
	}

	// Execute compensation plan the first time.
	executor := compensation.NewExecutor(compStore, h.Engine, observability.NewLogger("settla-compensation-test", "test"))
	if err := executor.Execute(ctx, plan); err != nil {
		t.Fatalf("First Executor.Execute failed: %v", err)
	}

	firstCount := compStore.recordCount()
	if firstCount != 1 {
		t.Fatalf("expected 1 compensation record after first execution, got %d", firstCount)
	}

	// Verify transfer is now REFUNDING.
	refunding := reloadTransfer(t, h, ctx, transfer.ID)
	if refunding.Status != domain.TransferStatusRefunding {
		t.Fatalf("expected REFUNDING after first compensation, got %s", refunding.Status)
	}

	// Execute the same compensation plan again. The store should return the
	// existing record ID (idempotent create). The engine call will fail
	// because the transfer is already in REFUNDING (not FAILED), but the
	// compensation record count should remain 1.
	err = executor.Execute(ctx, plan)
	// We expect an error because InitiateRefund requires FAILED status, and
	// the transfer is already REFUNDING.
	if err == nil {
		t.Log("Second execution succeeded (idempotent engine calls)")
	} else {
		t.Logf("Second execution returned expected error: %v", err)
	}

	secondCount := compStore.recordCount()
	if secondCount != 1 {
		t.Fatalf("expected 1 compensation record after second execution (idempotency), got %d", secondCount)
	}

	t.Logf("Compensation idempotency verified for transfer %s", transfer.ID)
}

// ─── Test: Credit Stablecoin Compensation ───────────────────────────────────

// TestCompensationCreditStablecoin verifies that when on-ramp completed but
// off-ramp fails and the tenant has auto_refund_currency=stablecoin, the
// compensation system selects CREDIT_STABLECOIN strategy.
func TestCompensationCreditStablecoin(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Set up tenant metadata so DetermineCompensation selects CREDIT_STABLECOIN.
	tenant, err := h.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}
	tenant.Metadata = map[string]string{"auto_refund_currency": "stablecoin"}

	// Advance transfer to OFF_RAMPING (on-ramp and settlement completed).
	transfer := createTransferAtStatus(t, h, ctx, domain.TransferStatusOffRamping)

	// Simulate off-ramp failure.
	err = h.Engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "beneficiary_bank_rejected",
	})
	if err != nil {
		t.Fatalf("HandleOffRampResult failed: %v", err)
	}
	h.executeOutbox(ctx)

	// Reload the transfer — it should be FAILED now.
	failed := reloadTransfer(t, h, ctx, transfer.ID)
	if failed.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED, got %s", failed.Status)
	}

	// Determine compensation plan on the already-failed transfer.
	plan := compensation.DetermineCompensation(failed, tenant, []string{
		compensation.StepFunded,
		compensation.StepOnRampCompleted,
	}, compensation.ExternalStatus{OnRampStatus: "completed"})

	if plan.Strategy != compensation.StrategyCreditStablecoin {
		t.Fatalf("expected CREDIT_STABLECOIN strategy, got %s", plan.Strategy)
	}
	if !plan.RefundAmount.Equal(failed.StableAmount) {
		t.Fatalf("expected refund amount %s, got %s", failed.StableAmount, plan.RefundAmount)
	}
	if plan.RefundCurrency != failed.StableCoin {
		t.Fatalf("expected refund currency %s, got %s", failed.StableCoin, plan.RefundCurrency)
	}

	// Now test the execution happy path: create a fresh harness with the
	// tenant metadata set BEFORE failing, so FailTransfer transitions
	// OFF_RAMPING → FAILED successfully.
	h2 := newTestHarness(t)
	compStore2 := newMemCompensationStore()

	// Set up tenant metadata on the second harness.
	tenant2, err := h2.TenantStore.GetTenant(ctx, LemfiTenantID)
	if err != nil {
		t.Fatalf("GetTenant (h2) failed: %v", err)
	}
	tenant2.Metadata = map[string]string{"auto_refund_currency": "stablecoin"}

	// Advance to OFF_RAMPING.
	transfer2 := createTransferAtStatus(t, h2, ctx, domain.TransferStatusOffRamping)

	// Build the plan while the transfer is still OFF_RAMPING.
	tr2 := reloadTransfer(t, h2, ctx, transfer2.ID)
	plan2 := compensation.DetermineCompensation(tr2, tenant2, []string{
		compensation.StepFunded,
		compensation.StepOnRampCompleted,
	}, compensation.ExternalStatus{OnRampStatus: "completed"})

	if plan2.Strategy != compensation.StrategyCreditStablecoin {
		t.Fatalf("expected CREDIT_STABLECOIN strategy for pre-failure plan, got %s", plan2.Strategy)
	}

	// Execute compensation: FailTransfer transitions OFF_RAMPING → FAILED.
	executor2 := compensation.NewExecutor(compStore2, h2.Engine, observability.NewLogger("settla-compensation-test", "test"))
	if err := executor2.Execute(ctx, plan2); err != nil {
		t.Fatalf("Executor.Execute (pre-failure) failed: %v", err)
	}
	h2.executeOutbox(ctx)

	// Verify compensation record was created with correct strategy and status.
	rec2 := compStore2.getByTransfer(transfer2.ID)
	if rec2 == nil {
		t.Fatal("expected compensation record to be created")
	}
	if rec2.Strategy != string(compensation.StrategyCreditStablecoin) {
		t.Fatalf("expected strategy CREDIT_STABLECOIN, got %s", rec2.Strategy)
	}
	if rec2.Status != "completed" {
		t.Fatalf("expected compensation status completed, got %s", rec2.Status)
	}

	// Verify the transfer reached FAILED terminal state.
	failed2 := reloadTransfer(t, h2, ctx, transfer2.ID)
	if failed2.Status != domain.TransferStatusFailed {
		t.Fatalf("expected FAILED after CREDIT_STABLECOIN compensation, got %s", failed2.Status)
	}

	t.Logf("Credit stablecoin compensation verified for transfers %s and %s", transfer.ID, transfer2.ID)
}

// ─── TEST-34: Concurrent Compensation Race ───────────────────────────────────

// TestCompensationConcurrentRace verifies that two concurrent compensation
// triggers on the same transfer both reach a terminal state without leaving the
// transfer in an inconsistent intermediate state.
func TestCompensationConcurrentRace(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	// Create a transfer and advance to ON_RAMPING state.
	transfer := createMinimalTransfer(t, h, LemfiTenantID, "comp-race-1")
	if err := h.Engine.FundTransfer(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("FundTransfer: %v", err)
	}
	h.executeOutbox(ctx)

	if err := h.Engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID); err != nil {
		t.Fatalf("InitiateOnRamp: %v", err)
	}
	h.executeOutbox(ctx)

	// Concurrently trigger two competing failure paths.
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:   false,
			ErrorMsg:  "concurrent-fail-1",
		})
	}()
	go func() {
		defer wg.Done()
		errs[1] = h.Engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:   false,
			ErrorMsg:  "concurrent-fail-2",
		})
	}()
	wg.Wait()

	// At least one should succeed; the other may fail with a state transition error.
	successCount := 0
	for _, err := range errs {
		if err == nil {
			successCount++
		}
	}
	if successCount == 0 {
		t.Fatalf("both concurrent HandleOnRampResult failed: err[0]=%v, err[1]=%v", errs[0], errs[1])
	}

	// The transfer must be in a terminal or compensation state — never stuck in ON_RAMPING.
	final := reloadTransfer(t, h, ctx, transfer.ID)
	switch final.Status {
	case domain.TransferStatusFailed, domain.TransferStatusCompensating:
		// Expected terminal/compensation states.
	default:
		t.Errorf("expected terminal state after concurrent failures, got %s", final.Status)
	}

	t.Logf("Concurrent compensation race: %d succeeded, final state=%s", successCount, final.Status)
}
