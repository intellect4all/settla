package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// mockOnRampProvider implements domain.OnRampProvider for testing.
type mockOnRampProvider struct {
	id     string
	result *domain.ProviderTx
	err    error
}

func (m *mockOnRampProvider) ID() string                                                    { return m.id }
func (m *mockOnRampProvider) SupportedPairs() []domain.CurrencyPair                         { return nil }
func (m *mockOnRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (m *mockOnRampProvider) Execute(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	return m.result, m.err
}
func (m *mockOnRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	return m.result, m.err
}

// mockOffRampProvider implements domain.OffRampProvider for testing.
type mockOffRampProvider struct {
	id     string
	result *domain.ProviderTx
	err    error
}

func (m *mockOffRampProvider) ID() string                                                    { return m.id }
func (m *mockOffRampProvider) SupportedPairs() []domain.CurrencyPair                         { return nil }
func (m *mockOffRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (m *mockOffRampProvider) Execute(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	return m.result, m.err
}
func (m *mockOffRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	return m.result, m.err
}

// mockProviderTransferStore implements ProviderTransferStore for testing.
type mockProviderTransferStore struct {
	mu    sync.Mutex
	txs   map[string]*domain.ProviderTx // key: transferID+txType
	calls []providerStoreCall
}

type providerStoreCall struct {
	method     string
	transferID uuid.UUID
	txType     string
}

func newMockProviderTransferStore() *mockProviderTransferStore {
	return &mockProviderTransferStore{
		txs: make(map[string]*domain.ProviderTx),
	}
}

func (m *mockProviderTransferStore) key(transferID uuid.UUID, txType string) string {
	return transferID.String() + ":" + txType
}

func (m *mockProviderTransferStore) GetProviderTransaction(ctx context.Context, _ uuid.UUID, transferID uuid.UUID, txType string) (*domain.ProviderTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"GetProviderTransaction", transferID, txType})
	tx, ok := m.txs[m.key(transferID, txType)]
	if !ok {
		return nil, nil
	}
	return tx, nil
}

func (m *mockProviderTransferStore) CreateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"CreateProviderTransaction", transferID, txType})
	m.txs[m.key(transferID, txType)] = tx
	return nil
}

func (m *mockProviderTransferStore) UpdateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"UpdateProviderTransaction", transferID, txType})
	m.txs[m.key(transferID, txType)] = tx
	return nil
}

func (m *mockProviderTransferStore) ClaimProviderTransaction(_ context.Context, params ClaimProviderTransactionParams) (*uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"ClaimProviderTransaction", params.TransferID, params.TxType})
	k := m.key(params.TransferID, params.TxType)
	if existing, ok := m.txs[k]; ok {
		switch existing.Status {
		case "completed", "confirmed", "pending", "claiming":
			return nil, nil
		}
		delete(m.txs, k)
	}
	id := uuid.New()
	m.txs[k] = &domain.ProviderTx{ID: id.String(), Status: "claiming"}
	return &id, nil
}

func (m *mockProviderTransferStore) UpdateTransferRoute(_ context.Context, transferID uuid.UUID, onRamp, offRamp, chain string, stableCoin domain.Currency) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"UpdateTransferRoute", transferID, ""})
	return nil
}

func (m *mockProviderTransferStore) DeleteProviderTransaction(_ context.Context, transferID uuid.UUID, txType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, providerStoreCall{"DeleteProviderTransaction", transferID, txType})
	delete(m.txs, m.key(transferID, txType))
	return nil
}

func (m *mockProviderTransferStore) setTx(transferID uuid.UUID, txType string, tx *domain.ProviderTx) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txs[m.key(transferID, txType)] = tx
}

func (m *mockProviderTransferStore) getCalls() []providerStoreCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]providerStoreCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func providerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestProviderWorker_OnRamp_CheckBeforeCall_AlreadyCompleted(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-123",
		Status: "completed",
	})

	engine := &mockEngine{}
	provider := &mockOnRampProvider{
		id:     "test-provider",
		result: &domain.ProviderTx{ID: "tx-123", Status: "completed"},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should NOT have been called — already completed
	engineCalls := engine.getCalls()
	if len(engineCalls) != 0 {
		t.Errorf("expected no engine calls for already-completed tx, got %d", len(engineCalls))
	}
}

func TestProviderWorker_OnRamp_CheckBeforeCall_Pending(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-456",
		Status: "pending",
	})

	engine := &mockEngine{}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should NOT have been called — pending awaiting webhook
	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for pending tx")
	}
}

func TestProviderWorker_OnRamp_ExecuteSuccess(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &mockOnRampProvider{
		id: "test-provider",
		result: &domain.ProviderTx{
			ID:         "tx-789",
			ExternalID: "ext-789",
			Status:     "completed",
			TxHash:     "0xabc",
		},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should have been called with success result
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", engineCalls[0].method)
	}

	// Store should have the transaction claimed
	storeCalls := store.getCalls()
	hasClaim := false
	for _, c := range storeCalls {
		if c.method == "ClaimProviderTransaction" && c.txType == "onramp" {
			hasClaim = true
		}
	}
	if !hasClaim {
		t.Error("expected ClaimProviderTransaction call for onramp")
	}
}

func TestProviderWorker_OnRamp_ExecuteFailure(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &mockOnRampProvider{
		id:  "test-provider",
		err: fmt.Errorf("provider unavailable"),
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (engine handles failure), got %v", err)
	}

	// Engine should have been called with failure result
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", engineCalls[0].method)
	}
}

func TestProviderWorker_OnRamp_UnknownProvider(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{}, // empty
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "nonexistent-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (engine handles unknown provider), got %v", err)
	}

	// Engine should have been called with failure
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", engineCalls[0].method)
	}
}

func TestProviderWorker_OnRamp_AsyncPending(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &mockOnRampProvider{
		id: "test-provider",
		result: &domain.ProviderTx{
			ID:     "tx-async",
			Status: "pending", // async provider
		},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should NOT be called — async provider, awaiting webhook
	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for async pending provider")
	}

	// But the transaction should be claimed
	storeCalls := store.getCalls()
	hasClaim := false
	for _, c := range storeCalls {
		if c.method == "ClaimProviderTransaction" {
			hasClaim = true
		}
	}
	if !hasClaim {
		t.Error("expected ClaimProviderTransaction for pending tx")
	}
}

func TestProviderWorker_OffRamp_ExecuteSuccess(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &mockOffRampProvider{
		id: "offramp-provider",
		result: &domain.ProviderTx{
			ID:         "tx-off-1",
			ExternalID: "ext-off-1",
			Status:     "completed",
		},
	}

	w := &ProviderWorker{
		offRampProviders: map[string]domain.OffRampProvider{"offramp-provider": provider},
		transferStore:    store,
		engine:           engine,
		logger:           providerTestLogger(),
	}

	payload := domain.ProviderOffRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "offramp-provider",
		Amount:       decimal.NewFromInt(900),
		FromCurrency: domain.Currency("USDT"),
		ToCurrency:   domain.Currency("NGN"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOffRamp,
		Data:     &payload,
	}

	err := w.handleOffRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOffRampResult" {
		t.Errorf("expected HandleOffRampResult, got %s", engineCalls[0].method)
	}
}

func TestProviderWorker_EventRouting(t *testing.T) {
	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	w := &ProviderWorker{
		onRampProviders:  map[string]domain.OnRampProvider{},
		offRampProviders: map[string]domain.OffRampProvider{},
		transferStore:    store,
		engine:           engine,
		logger:           providerTestLogger(),
	}

	// Unknown event type should be silently skipped
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

func TestProviderWorker_OnRamp_FallbackSuccess(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	// Primary provider fails, fallback succeeds
	primaryProvider := &mockOnRampProvider{
		id:  "primary-provider",
		err: fmt.Errorf("primary unavailable"),
	}
	fallbackProvider := &mockOnRampProvider{
		id: "fallback-provider",
		result: &domain.ProviderTx{
			ID:         "tx-fallback",
			ExternalID: "ext-fallback",
			Status:     "completed",
			TxHash:     "0xfallback",
		},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{
			"primary-provider":  primaryProvider,
			"fallback-provider": fallbackProvider,
		},
		transferStore: store,
		engine:        engine,
		logger:        providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "primary-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
		Alternatives: []domain.OnRampFallback{
			{
				ProviderID:      "fallback-provider",
				OffRampProvider: "offramp-b",
				Chain:           "tron",
				StableCoin:      domain.CurrencyUSDT,
				Fee:             domain.Money{Amount: decimal.NewFromFloat(5.0), Currency: domain.CurrencyUSD},
				Rate:            decimal.NewFromFloat(1.25),
				StableAmount:    decimal.NewFromFloat(1245),
			},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should have been called with success (from fallback)
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", engineCalls[0].method)
	}

	// Should have DeleteProviderTransaction + UpdateTransferRoute calls
	storeCalls := store.getCalls()
	hasDelete := false
	hasRouteUpdate := false
	for _, c := range storeCalls {
		if c.method == "DeleteProviderTransaction" && c.txType == "onramp" {
			hasDelete = true
		}
		if c.method == "UpdateTransferRoute" {
			hasRouteUpdate = true
		}
	}
	if !hasDelete {
		t.Error("expected DeleteProviderTransaction call for fallback")
	}
	if !hasRouteUpdate {
		t.Error("expected UpdateTransferRoute call for fallback")
	}
}

func TestProviderWorker_OnRamp_AllAlternativesExhausted(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	// Both primary and fallback fail
	primaryProvider := &mockOnRampProvider{
		id:  "primary-provider",
		err: fmt.Errorf("primary unavailable"),
	}
	fallbackProvider := &mockOnRampProvider{
		id:  "fallback-provider",
		err: fmt.Errorf("fallback also unavailable"),
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{
			"primary-provider":  primaryProvider,
			"fallback-provider": fallbackProvider,
		},
		transferStore: store,
		engine:        engine,
		logger:        providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "primary-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
		Alternatives: []domain.OnRampFallback{
			{
				ProviderID:      "fallback-provider",
				OffRampProvider: "offramp-b",
				Chain:           "tron",
				StableCoin:      domain.CurrencyUSDT,
				Fee:             domain.Money{Amount: decimal.NewFromFloat(5.0), Currency: domain.CurrencyUSD},
				Rate:            decimal.NewFromFloat(1.25),
				StableAmount:    decimal.NewFromFloat(1245),
			},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (engine handles all-failed), got %v", err)
	}

	// Engine should have been called with failure (all alternatives exhausted)
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", engineCalls[0].method)
	}
}

func TestProviderWorker_OffRamp_FallbackSuccess(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}

	primaryProvider := &mockOffRampProvider{
		id:  "offramp-primary",
		err: fmt.Errorf("primary off-ramp unavailable"),
	}
	fallbackProvider := &mockOffRampProvider{
		id: "offramp-fallback",
		result: &domain.ProviderTx{
			ID:         "tx-offramp-fallback",
			ExternalID: "ext-offramp-fallback",
			Status:     "completed",
		},
	}

	w := &ProviderWorker{
		offRampProviders: map[string]domain.OffRampProvider{
			"offramp-primary":  primaryProvider,
			"offramp-fallback": fallbackProvider,
		},
		transferStore: store,
		engine:        engine,
		logger:        providerTestLogger(),
	}

	payload := domain.ProviderOffRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "offramp-primary",
		Amount:       decimal.NewFromInt(900),
		FromCurrency: domain.Currency("USDT"),
		ToCurrency:   domain.Currency("NGN"),
		Reference:    transferID.String(),
		Alternatives: []domain.OffRampFallback{
			{
				ProviderID: "offramp-fallback",
				Fee:        domain.Money{Amount: decimal.NewFromFloat(3.0), Currency: domain.CurrencyUSD},
				Rate:       decimal.NewFromFloat(1.20),
			},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOffRamp,
		Data:     &payload,
	}

	err := w.handleOffRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should have been called with success (from fallback)
	engineCalls := engine.getCalls()
	if len(engineCalls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(engineCalls))
	}
	if engineCalls[0].method != "HandleOffRampResult" {
		t.Errorf("expected HandleOffRampResult, got %s", engineCalls[0].method)
	}

	// Should have DeleteProviderTransaction call
	storeCalls := store.getCalls()
	hasDelete := false
	for _, c := range storeCalls {
		if c.method == "DeleteProviderTransaction" && c.txType == "offramp" {
			hasDelete = true
		}
	}
	if !hasDelete {
		t.Error("expected DeleteProviderTransaction call for off-ramp fallback")
	}
}

// ---------------------------------------------------------------------------
// Concurrent provider wrappers for redelivery tests
// ---------------------------------------------------------------------------

// concurrentOnRampProvider wraps mockOnRampProvider with an atomic execution counter.
type concurrentOnRampProvider struct {
	mockOnRampProvider
	execCount atomic.Int64
}

func (c *concurrentOnRampProvider) Execute(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	c.execCount.Add(1)
	time.Sleep(1 * time.Millisecond) // widen race window
	return c.mockOnRampProvider.Execute(ctx, req)
}

// concurrentOffRampProvider wraps mockOffRampProvider with an atomic execution counter.
type concurrentOffRampProvider struct {
	mockOffRampProvider
	execCount atomic.Int64
}

func (c *concurrentOffRampProvider) Execute(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	c.execCount.Add(1)
	time.Sleep(1 * time.Millisecond) // widen race window
	return c.mockOffRampProvider.Execute(ctx, req)
}

func TestProviderWorker_ConcurrentRedelivery_OnlyOneExecutes(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &concurrentOnRampProvider{
		mockOnRampProvider: mockOnRampProvider{
			id: "test-provider",
			result: &domain.ProviderTx{
				ID:         "tx-concurrent",
				ExternalID: "ext-concurrent",
				Status:     "completed",
				TxHash:     "0xconcurrent",
			},
		},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.handleOnRamp(ctx, event)
		}()
	}
	wg.Wait()

	execCount := provider.execCount.Load()
	if execCount > 1 {
		t.Errorf("expected provider Execute called at most 1 time, got %d", execCount)
	}

	engineCalls := engine.getCalls()
	handleCount := 0
	for _, c := range engineCalls {
		if c.method == "HandleOnRampResult" {
			handleCount++
		}
	}
	if handleCount > 1 {
		t.Errorf("expected HandleOnRampResult called at most 1 time, got %d", handleCount)
	}
}

func TestProviderWorker_OffRamp_DoubleExecPrevented(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	engine := &mockEngine{}
	provider := &concurrentOffRampProvider{
		mockOffRampProvider: mockOffRampProvider{
			id: "offramp-provider",
			result: &domain.ProviderTx{
				ID:         "tx-offramp-concurrent",
				ExternalID: "ext-offramp-concurrent",
				Status:     "completed",
			},
		},
	}

	w := &ProviderWorker{
		offRampProviders: map[string]domain.OffRampProvider{"offramp-provider": provider},
		transferStore:    store,
		engine:           engine,
		logger:           providerTestLogger(),
	}

	payload := domain.ProviderOffRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "offramp-provider",
		Amount:       decimal.NewFromInt(900),
		FromCurrency: domain.Currency("USDT"),
		ToCurrency:   domain.Currency("NGN"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOffRamp,
		Data:     &payload,
	}

	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.handleOffRamp(ctx, event)
		}()
	}
	wg.Wait()

	execCount := provider.execCount.Load()
	if execCount > 1 {
		t.Errorf("expected provider Execute called at most 1 time, got %d", execCount)
	}

	engineCalls := engine.getCalls()
	handleCount := 0
	for _, c := range engineCalls {
		if c.method == "HandleOffRampResult" {
			handleCount++
		}
	}
	if handleCount > 1 {
		t.Errorf("expected HandleOffRampResult called at most 1 time, got %d", handleCount)
	}
}

func TestProviderWorker_RedeliveryAfterAsyncPending_Skips(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	// Pre-seed a pending transaction for this transfer — simulates an async
	// provider that returned "pending" and is now awaiting a webhook callback.
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-pending-async",
		Status: "pending",
	})

	engine := &mockEngine{}
	provider := &concurrentOnRampProvider{
		mockOnRampProvider: mockOnRampProvider{
			id:     "test-provider",
			result: &domain.ProviderTx{ID: "should-not-be-called", Status: "completed"},
		},
	}

	w := &ProviderWorker{
		onRampProviders: map[string]domain.OnRampProvider{"test-provider": provider},
		transferStore:   store,
		engine:          engine,
		logger:          providerTestLogger(),
	}

	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "test-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.Currency("GBP"),
		ToCurrency:   domain.Currency("USDT"),
		Reference:    transferID.String(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentProviderOnRamp,
		Data:     &payload,
	}

	err := w.handleOnRamp(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Provider Execute must NOT have been called — tx is already pending.
	if got := provider.execCount.Load(); got != 0 {
		t.Errorf("expected provider Execute not called, got %d calls", got)
	}

	// Engine must NOT have been called.
	if calls := engine.getCalls(); len(calls) != 0 {
		t.Errorf("expected no engine calls, got %d: %v", len(calls), calls)
	}
}
