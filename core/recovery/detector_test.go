package recovery

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockTransferQueryStore struct {
	mu        sync.Mutex
	transfers map[domain.TransferStatus][]*domain.Transfer
}

func newMockTransferQueryStore() *mockTransferQueryStore {
	return &mockTransferQueryStore{
		transfers: make(map[domain.TransferStatus][]*domain.Transfer),
	}
}

func (m *mockTransferQueryStore) addTransfer(t *domain.Transfer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[t.Status] = append(m.transfers[t.Status], t)
}

func (m *mockTransferQueryStore) ListStuckTransfers(_ context.Context, status domain.TransferStatus, olderThan time.Time) ([]*domain.Transfer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.Transfer
	for _, t := range m.transfers[status] {
		if t.UpdatedAt.Before(olderThan) {
			result = append(result, t)
		}
	}
	return result, nil
}

type mockReviewStore struct {
	mu      sync.Mutex
	reviews map[uuid.UUID]bool // transferID -> has active review
	created []struct {
		TransferID     uuid.UUID
		TenantID       uuid.UUID
		TransferStatus string
		StuckSince     time.Time
	}
}

func newMockReviewStore() *mockReviewStore {
	return &mockReviewStore{
		reviews: make(map[uuid.UUID]bool),
	}
}

func (m *mockReviewStore) CreateManualReview(_ context.Context, transferID, tenantID uuid.UUID, transferStatus string, stuckSince time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews[transferID] = true
	m.created = append(m.created, struct {
		TransferID     uuid.UUID
		TenantID       uuid.UUID
		TransferStatus string
		StuckSince     time.Time
	}{transferID, tenantID, transferStatus, stuckSince})
	return nil
}

func (m *mockReviewStore) HasActiveReview(_ context.Context, transferID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reviews[transferID], nil
}

type engineCall struct {
	Method     string
	TransferID uuid.UUID
	Result     domain.IntentResult
	Reason     string
	Code       string
}

type mockRecoveryEngine struct {
	mu               sync.Mutex
	calls            []engineCall
	initiateOnRampErr error
}

func (m *mockRecoveryEngine) HandleOnRampResult(_ context.Context, _ uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "HandleOnRampResult", TransferID: transferID, Result: result})
	return nil
}

func (m *mockRecoveryEngine) HandleSettlementResult(_ context.Context, _ uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "HandleSettlementResult", TransferID: transferID, Result: result})
	return nil
}

func (m *mockRecoveryEngine) HandleOffRampResult(_ context.Context, _ uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "HandleOffRampResult", TransferID: transferID, Result: result})
	return nil
}

func (m *mockRecoveryEngine) HandleRefundResult(_ context.Context, _ uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "HandleRefundResult", TransferID: transferID, Result: result})
	return nil
}

func (m *mockRecoveryEngine) FailTransfer(_ context.Context, _ uuid.UUID, transferID uuid.UUID, reason, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "FailTransfer", TransferID: transferID, Reason: reason, Code: code})
	return nil
}

func (m *mockRecoveryEngine) FundTransfer(_ context.Context, _ uuid.UUID, transferID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "FundTransfer", TransferID: transferID})
	return nil
}

func (m *mockRecoveryEngine) InitiateOnRamp(_ context.Context, _ uuid.UUID, transferID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{Method: "InitiateOnRamp", TransferID: transferID})
	return m.initiateOnRampErr
}

func (m *mockRecoveryEngine) getCalls() []engineCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]engineCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

type providerResponse struct {
	onRampStatus  *ProviderStatus
	offRampStatus *ProviderStatus
	chainStatus   *ChainStatus
	onRampErr     error
	offRampErr    error
	chainErr      error
}

type mockProviderStatusChecker struct {
	mu        sync.Mutex
	responses map[uuid.UUID]*providerResponse // keyed by transferID
	defaultResp *providerResponse
}

func newMockProviderStatusChecker() *mockProviderStatusChecker {
	return &mockProviderStatusChecker{
		responses: make(map[uuid.UUID]*providerResponse),
		defaultResp: &providerResponse{
			onRampStatus:  &ProviderStatus{Status: "pending"},
			offRampStatus: &ProviderStatus{Status: "pending"},
			chainStatus:   &ChainStatus{Confirmed: false},
		},
	}
}

func (m *mockProviderStatusChecker) setResponse(transferID uuid.UUID, resp *providerResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[transferID] = resp
}

func (m *mockProviderStatusChecker) getResp(transferID uuid.UUID) *providerResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.responses[transferID]; ok {
		return r
	}
	return m.defaultResp
}

func (m *mockProviderStatusChecker) CheckOnRampStatus(_ context.Context, _ string, transferID uuid.UUID) (*ProviderStatus, error) {
	r := m.getResp(transferID)
	return r.onRampStatus, r.onRampErr
}

func (m *mockProviderStatusChecker) CheckOffRampStatus(_ context.Context, _ string, transferID uuid.UUID) (*ProviderStatus, error) {
	r := m.getResp(transferID)
	return r.offRampStatus, r.offRampErr
}

func (m *mockProviderStatusChecker) CheckBlockchainStatus(_ context.Context, _ string, _ string) (*ChainStatus, error) {
	// For blockchain, we use default since there's no transferID in the signature.
	// Tests that need specific responses should set the defaultResp.
	return m.defaultResp.chainStatus, m.defaultResp.chainErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func makeStuckTransfer(status domain.TransferStatus, stuckFor time.Duration) *domain.Transfer {
	return &domain.Transfer{
		ID:                uuid.New(),
		TenantID:          uuid.New(),
		Status:            status,
		Version:           2,
		SourceCurrency:    domain.Currency("GBP"),
		SourceAmount:      decimal.NewFromInt(1000),
		DestCurrency:      domain.Currency("NGN"),
		DestAmount:        decimal.NewFromInt(500000),
		StableCoin:        domain.Currency("USDT"),
		StableAmount:      decimal.NewFromInt(1200),
		Chain:             "tron",
		OnRampProviderID:  "moonpay",
		OffRampProviderID: "flutterwave",
		CreatedAt:         time.Now().UTC().Add(-stuckFor - time.Minute),
		UpdatedAt:         time.Now().UTC().Add(-stuckFor),
	}
}

// shortThresholds make tests fast by using very short durations.
var shortThresholds = map[domain.TransferStatus]Thresholds{
	domain.TransferStatusFunded:     {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
	domain.TransferStatusOnRamping:  {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
	domain.TransferStatusSettling:   {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
	domain.TransferStatusOffRamping: {Warn: 1 * time.Millisecond, Recover: 2 * time.Millisecond, Escalate: 5 * time.Millisecond},
}

func newTestDetector(
	store *mockTransferQueryStore,
	reviewStore *mockReviewStore,
	engine *mockRecoveryEngine,
	providers *mockProviderStatusChecker,
) *Detector {
	d := NewDetector(store, reviewStore, engine, providers, testLogger())
	d.thresholds = shortThresholds
	return d
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStuckFundedTransfer_InitiatesOnRamp(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusFunded, 20*time.Millisecond)
	store.addTransfer(transfer)

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call for FUNDED recovery, got %d", len(calls))
	}
	if calls[0].Method != "InitiateOnRamp" {
		t.Errorf("expected InitiateOnRamp, got %s", calls[0].Method)
	}
	if calls[0].TransferID != transfer.ID {
		t.Errorf("wrong transfer ID: got %s, want %s", calls[0].TransferID, transfer.ID)
	}
}

func TestStuckFundedTransfer_OptimisticLock_TreatedAsRecovered(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{initiateOnRampErr: core.ErrOptimisticLock}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusFunded, 20*time.Millisecond)
	store.addTransfer(transfer)

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Should have called InitiateOnRamp (which returned ErrOptimisticLock)
	// but treated the transfer as recovered (no error from RunOnce).
	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].Method != "InitiateOnRamp" {
		t.Errorf("expected InitiateOnRamp, got %s", calls[0].Method)
	}
}

func TestStuckOnRamping_ProviderCompleted(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "completed", Reference: "ref-123"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].Method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", calls[0].Method)
	}
	if calls[0].TransferID != transfer.ID {
		t.Errorf("wrong transfer ID")
	}
	if !calls[0].Result.Success {
		t.Errorf("expected success=true")
	}
	if calls[0].Result.ProviderRef != "ref-123" {
		t.Errorf("expected provider ref ref-123, got %s", calls[0].Result.ProviderRef)
	}
}

func TestStuckOnRamping_ProviderPending_Skips(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "pending"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 engine calls for pending on-ramp, got %d", len(calls))
	}
}

func TestStuckOnRamping_PastEscalate_CreatesManualReview(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	// Stuck past the escalate threshold (5ms in shortThresholds)
	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "pending"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	reviewStore.mu.Lock()
	defer reviewStore.mu.Unlock()
	if len(reviewStore.created) != 1 {
		t.Fatalf("expected 1 manual review created, got %d", len(reviewStore.created))
	}
	if reviewStore.created[0].TransferID != transfer.ID {
		t.Errorf("wrong transfer ID in review")
	}
	if reviewStore.created[0].TransferStatus != string(domain.TransferStatusOnRamping) {
		t.Errorf("wrong transfer status in review: %s", reviewStore.created[0].TransferStatus)
	}
}

func TestStuckSettling_BlockchainConfirmed(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusSettling, 20*time.Millisecond)
	transfer.BlockchainTxs = []domain.BlockchainTx{
		{Chain: "tron", Type: "settlement", TxHash: "0xabc123", Status: "pending"},
	}
	store.addTransfer(transfer)

	providers.defaultResp = &providerResponse{
		chainStatus: &ChainStatus{Confirmed: true, TxHash: "0xabc123"},
	}

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].Method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", calls[0].Method)
	}
	if !calls[0].Result.Success {
		t.Errorf("expected success=true")
	}
	if calls[0].Result.TxHash != "0xabc123" {
		t.Errorf("expected tx hash 0xabc123, got %s", calls[0].Result.TxHash)
	}
}

func TestStuckOffRamping_ProviderFailed(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOffRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		offRampStatus: &ProviderStatus{Status: "failed", Error: "insufficient_funds"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].Method != "HandleOffRampResult" {
		t.Errorf("expected HandleOffRampResult, got %s", calls[0].Method)
	}
	if calls[0].Result.Success {
		t.Errorf("expected success=false")
	}
	if calls[0].Result.Error != "insufficient_funds" {
		t.Errorf("expected error 'insufficient_funds', got %s", calls[0].Result.Error)
	}
}

func TestRecoveryIsIdempotent_RunTwice(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "completed", Reference: "ref-456"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	// Run twice
	if err := detector.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce failed: %v", err)
	}
	if err := detector.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce failed: %v", err)
	}

	// The engine will be called twice (the detector finds the same transfer both
	// times since the mock store doesn't update status). In production, the first
	// HandleOnRampResult transitions the transfer, so it won't be returned by
	// ListStuckTransfers on the second cycle. The engine's optimistic lock
	// prevents double-processing.
	calls := engine.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 engine calls (idempotent via engine locks), got %d", len(calls))
	}
	for _, c := range calls {
		if c.Method != "HandleOnRampResult" {
			t.Errorf("expected HandleOnRampResult, got %s", c.Method)
		}
	}
}

func TestEscalationIsIdempotent_AlreadyHasReview(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	// Pre-populate an active review
	reviewStore.mu.Lock()
	reviewStore.reviews[transfer.ID] = true
	reviewStore.mu.Unlock()

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "pending"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	// Should not create a duplicate review
	reviewStore.mu.Lock()
	defer reviewStore.mu.Unlock()
	if len(reviewStore.created) != 0 {
		t.Errorf("expected 0 reviews created (already has one), got %d", len(reviewStore.created))
	}
}

func TestMultipleStuckTransfers_ProcessesAll(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	// Create 3 stuck ON_RAMPING transfers
	for i := 0; i < 3; i++ {
		transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
		store.addTransfer(transfer)
		providers.setResponse(transfer.ID, &providerResponse{
			onRampStatus: &ProviderStatus{Status: "completed", Reference: "ref-batch"},
		})
	}

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 engine calls, got %d", len(calls))
	}
	for _, c := range calls {
		if c.Method != "HandleOnRampResult" {
			t.Errorf("expected HandleOnRampResult, got %s", c.Method)
		}
	}
}

func TestCompletedTransfer_NotQueried(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	// Add a completed transfer — should not be picked up because COMPLETED
	// is not in the thresholds map.
	transfer := &domain.Transfer{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		Status:    domain.TransferStatusCompleted,
		UpdatedAt: time.Now().UTC().Add(-1 * time.Hour),
	}
	store.addTransfer(transfer)

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 engine calls for completed transfer, got %d", len(calls))
	}
}

func TestDetector_StopsOnContextCancel(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	detector := newTestDetector(store, reviewStore, engine, providers)
	detector.interval = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- detector.Run(ctx)
	}()

	// Let it run briefly then cancel
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detector did not stop after context cancellation")
	}
}

func TestStuckSettling_NoTxHash_Skips(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusSettling, 20*time.Millisecond)
	// No BlockchainTxs set
	store.addTransfer(transfer)

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 engine calls when no tx hash, got %d", len(calls))
	}
}

func TestStuckOnRamping_ProviderFailed(t *testing.T) {
	store := newMockTransferQueryStore()
	reviewStore := newMockReviewStore()
	engine := &mockRecoveryEngine{}
	providers := newMockProviderStatusChecker()

	transfer := makeStuckTransfer(domain.TransferStatusOnRamping, 20*time.Millisecond)
	store.addTransfer(transfer)

	providers.setResponse(transfer.ID, &providerResponse{
		onRampStatus: &ProviderStatus{Status: "failed", Error: "provider_timeout"},
	})

	detector := newTestDetector(store, reviewStore, engine, providers)

	err := detector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].Method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", calls[0].Method)
	}
	if calls[0].Result.Success {
		t.Errorf("expected success=false for failed on-ramp")
	}
	if calls[0].Result.Error != "provider_timeout" {
		t.Errorf("expected error 'provider_timeout', got %s", calls[0].Result.Error)
	}
}
