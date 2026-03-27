package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// mockBlockchainReviewStore implements BlockchainReviewStore for testing.
type mockBlockchainReviewStore struct {
	mu      sync.Mutex
	reviews []reviewCall
	active  map[uuid.UUID]bool
}

type reviewCall struct {
	transferID     uuid.UUID
	tenantID       uuid.UUID
	transferStatus string
	stuckSince     time.Time
}

func newMockBlockchainReviewStore() *mockBlockchainReviewStore {
	return &mockBlockchainReviewStore{
		active: make(map[uuid.UUID]bool),
	}
}

func (m *mockBlockchainReviewStore) CreateManualReview(_ context.Context, transferID, tenantID uuid.UUID, transferStatus string, stuckSince time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews = append(m.reviews, reviewCall{transferID, tenantID, transferStatus, stuckSince})
	m.active[transferID] = true
	return nil
}

func (m *mockBlockchainReviewStore) HasActiveReview(_ context.Context, transferID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[transferID], nil
}

func (m *mockBlockchainReviewStore) getReviews() []reviewCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]reviewCall{}, m.reviews...)
}

// mockBlockchainClient implements domain.BlockchainClient for testing.
type mockBlockchainClient struct {
	chain    string
	sendTx   *domain.ChainTx
	sendErr  error
	getTx    *domain.ChainTx
	getErr   error
}

func (m *mockBlockchainClient) Chain() domain.CryptoChain { return domain.CryptoChain(m.chain) }
func (m *mockBlockchainClient) GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (m *mockBlockchainClient) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (m *mockBlockchainClient) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	return m.sendTx, m.sendErr
}
func (m *mockBlockchainClient) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	return m.getTx, m.getErr
}
func (m *mockBlockchainClient) SubscribeTransactions(ctx context.Context, address string, ch chan<- domain.ChainTx) error {
	return nil
}

func blockchainTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBlockchainWorker_SendSuccess_Confirmed(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain: "tron",
		sendTx: &domain.ChainTx{
			Hash:          "0xabc123",
			Status:        "confirmed",
			Confirmations: 6,
		},
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	payload := domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		From:       "TFrom123",
		To:         "TTo456",
		Token:      "USDT",
		Amount:     decimal.NewFromInt(1000),
		Memo:       "settlement:" + transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentBlockchainSend,
		Data:     &payload,
	}

	err := w.handleSend(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", engineCalls[0].method)
	}
}

func TestBlockchainWorker_SendFailure(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain:   "tron",
		sendErr: fmt.Errorf("network error"),
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	payload := domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(1000),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentBlockchainSend,
		Data:     &payload,
	}

	err := w.handleSend(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (engine handles failure), got %v", err)
	}

	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", engineCalls[0].method)
	}
}

func TestBlockchainWorker_CheckBeforeCall_AlreadyConfirmed(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "blockchain", &domain.ProviderTx{
		ID:     "0xexisting",
		Status: "confirmed",
		TxHash: "0xexisting",
	})

	engine := &mockEngine{}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	payload := domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(1000),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentBlockchainSend,
		Data:     &payload,
	}

	err := w.handleSend(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should NOT have been called — tx already confirmed
	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for already-confirmed tx")
	}
}

func TestBlockchainWorker_CheckBeforeCall_PendingNowConfirmed(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "blockchain", &domain.ProviderTx{
		ID:     "0xpending",
		Status: "pending",
		TxHash: "0xpending",
	})

	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain: "tron",
		getTx: &domain.ChainTx{
			Hash:          "0xpending",
			Status:        "confirmed",
			Confirmations: 10,
		},
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	payload := domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(1000),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentBlockchainSend,
		Data:     &payload,
	}

	err := w.handleSend(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should have been called with success
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", engineCalls[0].method)
	}
}

func TestBlockchainWorker_UnknownChain(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{}, // empty
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	payload := domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "unknown-chain",
		Amount:     decimal.NewFromInt(1000),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentBlockchainSend,
		Data:     &payload,
	}

	err := w.handleSend(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (engine handles unknown chain), got %v", err)
	}

	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
}

func TestBlockchainWorker_EventRouting(t *testing.T) {
	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     "some.unknown.event",
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for unknown event, got %v", err)
	}
}

func TestBlockchainWorker_PendingPoller_ConfirmedAfterDelay(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain: "tron",
		getTx: &domain.ChainTx{
			Hash:          "0xpendingHash",
			Status:        "confirmed",
			Confirmations: 12,
		},
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
		pendingTxMap:      make(map[uuid.UUID]pendingEntry),
		escalationTimeout: pendingTxEscalationTimeout,
	}

	payload := &domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(500),
	}

	// Simulate a pending entry tracked 10 seconds ago (well within the 1h timeout)
	w.pendingMu.Lock()
	w.pendingTxMap[transferID] = pendingEntry{
		payload:  payload,
		txHash:   "0xpendingHash",
		tenantID: tenantID,
		addedAt:  time.Now().UTC().Add(-10 * time.Second),
	}
	w.pendingTxCount++
	w.pendingMu.Unlock()

	w.pollPendingTransactions(context.Background())

	// Engine should have been called with success (confirmed)
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", engineCalls[0].method)
	}

	// Entry should have been removed from pending map
	w.pendingMu.Lock()
	_, stillExists := w.pendingTxMap[transferID]
	w.pendingMu.Unlock()
	if stillExists {
		t.Error("expected pending entry to be deleted after confirmation")
	}
}

func TestBlockchainWorker_PendingPoller_FailedOnPoll(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain: "tron",
		getTx: &domain.ChainTx{
			Hash:   "0xfailedHash",
			Status: "failed",
		},
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
		pendingTxMap:      make(map[uuid.UUID]pendingEntry),
		escalationTimeout: pendingTxEscalationTimeout,
	}

	payload := &domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(500),
	}

	w.pendingMu.Lock()
	w.pendingTxMap[transferID] = pendingEntry{
		payload:  payload,
		txHash:   "0xfailedHash",
		tenantID: tenantID,
		addedAt:  time.Now().UTC().Add(-10 * time.Second),
	}
	w.pendingTxCount++
	w.pendingMu.Unlock()

	w.pollPendingTransactions(context.Background())

	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleSettlementResult" {
		t.Errorf("expected HandleSettlementResult, got %s", engineCalls[0].method)
	}

	// Entry should have been removed from pending map
	w.pendingMu.Lock()
	_, stillExists := w.pendingTxMap[transferID]
	w.pendingMu.Unlock()
	if stillExists {
		t.Error("expected pending entry to be deleted after failure")
	}
}

func TestBlockchainWorker_PendingPoller_EscalatesAfter1Hour(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	reviewStore := newMockBlockchainReviewStore()

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": &mockBlockchainClient{chain: "tron"}},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
		reviewStore:       reviewStore,
		pendingTxMap:      make(map[uuid.UUID]pendingEntry),
		escalationTimeout: pendingTxEscalationTimeout,
	}

	payload := &domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(500),
	}

	stuckSince := time.Now().UTC().Add(-2 * time.Hour) // well past the 1h timeout
	w.pendingMu.Lock()
	w.pendingTxMap[transferID] = pendingEntry{
		payload:  payload,
		txHash:   "0xstuckHash",
		tenantID: tenantID,
		addedAt:  stuckSince,
	}
	w.pendingTxCount++
	w.pendingMu.Unlock()

	w.pollPendingTransactions(context.Background())

	// Engine should NOT have been called — escalated instead
	if len(engine.getCalls()) != 0 {
		t.Errorf("expected no engine calls for escalated tx, got %d", len(engine.getCalls()))
	}

	// Review store should have been called
	reviews := reviewStore.getReviews()
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review created, got %d", len(reviews))
	}
	if reviews[0].transferID != transferID {
		t.Errorf("expected review for transfer %s, got %s", transferID, reviews[0].transferID)
	}
	if reviews[0].tenantID != tenantID {
		t.Errorf("expected review for tenant %s, got %s", tenantID, reviews[0].tenantID)
	}
	if reviews[0].transferStatus != "BLOCKCHAIN_PENDING" {
		t.Errorf("expected status BLOCKCHAIN_PENDING, got %s", reviews[0].transferStatus)
	}

	// Entry should have been removed from pending map
	w.pendingMu.Lock()
	_, stillExists := w.pendingTxMap[transferID]
	w.pendingMu.Unlock()
	if stillExists {
		t.Error("expected pending entry to be deleted after escalation")
	}
}

func TestBlockchainWorker_PendingPoller_StillPending(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	client := &mockBlockchainClient{
		chain: "tron",
		getTx: &domain.ChainTx{
			Hash:   "0xstillPending",
			Status: "pending",
		},
	}

	w := &BlockchainWorker{
		blockchainClients: map[string]domain.BlockchainClient{"tron": client},
		transferStore:     store,
		engine:            engine,
		logger:            blockchainTestLogger(),
		pendingTxMap:      make(map[uuid.UUID]pendingEntry),
		escalationTimeout: pendingTxEscalationTimeout,
	}

	payload := &domain.BlockchainSendPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Chain:      "tron",
		Amount:     decimal.NewFromInt(500),
	}

	w.pendingMu.Lock()
	w.pendingTxMap[transferID] = pendingEntry{
		payload:  payload,
		txHash:   "0xstillPending",
		tenantID: tenantID,
		addedAt:  time.Now().UTC().Add(-5 * time.Minute),
	}
	w.pendingTxCount++
	w.pendingMu.Unlock()

	w.pollPendingTransactions(context.Background())

	// Engine should NOT have been called — still pending
	if len(engine.getCalls()) != 0 {
		t.Errorf("expected no engine calls for still-pending tx, got %d", len(engine.getCalls()))
	}

	// Entry should still be in the pending map
	w.pendingMu.Lock()
	_, exists := w.pendingTxMap[transferID]
	w.pendingMu.Unlock()
	if !exists {
		t.Error("expected pending entry to remain in map")
	}
}
