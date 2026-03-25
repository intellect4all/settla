package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockTransferStore struct {
	mu                         sync.Mutex
	transfers                  map[uuid.UUID]*domain.Transfer
	outboxEntries              []domain.OutboxEntry // captured from outbox calls
	createFn                   func(ctx context.Context, t *domain.Transfer) error
	getFn                      func(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error)
	getByIdempotencyKeyFn      func(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error)
	updateFn                   func(ctx context.Context, t *domain.Transfer) error
	createEventFn              func(ctx context.Context, e *domain.TransferEvent) error
	getEventsFn                func(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error)
	getDailyVolumeFn           func(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
	getQuoteFn                 func(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error)
	transitionWithOutboxFn     func(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error
	createTransferWithOutboxFn func(ctx context.Context, transfer *domain.Transfer, entries []domain.OutboxEntry) error
}

func newMockTransferStore() *mockTransferStore {
	return &mockTransferStore{
		transfers: make(map[uuid.UUID]*domain.Transfer),
	}
}

func (m *mockTransferStore) CreateTransfer(ctx context.Context, t *domain.Transfer) error {
	if m.createFn != nil {
		return m.createFn(ctx, t)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[t.ID] = t
	return nil
}

func (m *mockTransferStore) GetTransfer(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error) {
	if m.getFn != nil {
		return m.getFn(ctx, tenantID, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.transfers[id]
	if !ok {
		return nil, domain.ErrTransferNotFound(id.String())
	}
	return t, nil
}

func (m *mockTransferStore) GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error) {
	if m.getByIdempotencyKeyFn != nil {
		return m.getByIdempotencyKeyFn(ctx, tenantID, key)
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockTransferStore) UpdateTransfer(ctx context.Context, t *domain.Transfer) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, t)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[t.ID] = t
	return nil
}

func (m *mockTransferStore) CreateTransferEvent(ctx context.Context, e *domain.TransferEvent) error {
	if m.createEventFn != nil {
		return m.createEventFn(ctx, e)
	}
	return nil
}

func (m *mockTransferStore) GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error) {
	if m.getEventsFn != nil {
		return m.getEventsFn(ctx, tenantID, transferID)
	}
	return nil, nil
}

func (m *mockTransferStore) GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error) {
	if m.getDailyVolumeFn != nil {
		return m.getDailyVolumeFn(ctx, tenantID, date)
	}
	return decimal.Zero, nil
}

func (m *mockTransferStore) CreateQuote(ctx context.Context, quote *domain.Quote) error {
	return nil
}

func (m *mockTransferStore) GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error) {
	if m.getQuoteFn != nil {
		return m.getQuoteFn(ctx, tenantID, quoteID)
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockTransferStore) ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	return nil, nil
}

func (m *mockTransferStore) ListTransfersFiltered(_ context.Context, _ uuid.UUID, _, _ string, _ int) ([]domain.Transfer, error) {
	return nil, nil
}

func (m *mockTransferStore) TransitionWithOutbox(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error {
	if m.transitionWithOutboxFn != nil {
		return m.transitionWithOutboxFn(ctx, transferID, newStatus, expectedVersion, entries)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.transfers[transferID]
	if !ok {
		return domain.ErrTransferNotFound(transferID.String())
	}
	if t.Version != expectedVersion {
		return domain.ErrOptimisticLock("transfer", transferID.String())
	}
	t.Status = newStatus
	t.Version++
	m.outboxEntries = append(m.outboxEntries, entries...)
	return nil
}

func (m *mockTransferStore) CreateTransferWithOutbox(ctx context.Context, transfer *domain.Transfer, entries []domain.OutboxEntry) error {
	if m.createTransferWithOutboxFn != nil {
		return m.createTransferWithOutboxFn(ctx, transfer, entries)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[transfer.ID] = transfer
	m.outboxEntries = append(m.outboxEntries, entries...)
	return nil
}

// getOutboxEntries returns captured outbox entries (thread-safe).
func (m *mockTransferStore) GetTransferByExternalRef(_ context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.transfers {
		if t.TenantID == tenantID && t.ExternalRef == externalRef {
			return t, nil
		}
	}
	return nil, fmt.Errorf("transfer not found for external ref %s", externalRef)
}

func (m *mockTransferStore) CountPendingTransfers(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}

func (m *mockTransferStore) getOutboxEntries() []domain.OutboxEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.OutboxEntry, len(m.outboxEntries))
	copy(result, m.outboxEntries)
	return result
}

// clearOutbox resets captured outbox entries.
func (m *mockTransferStore) clearOutbox() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outboxEntries = nil
}

type mockTenantStore struct {
	getFn       func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	getBySlugFn func(ctx context.Context, slug string) (*domain.Tenant, error)
}

func (m *mockTenantStore) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	if m.getFn != nil {
		return m.getFn(ctx, tenantID)
	}
	return nil, domain.ErrTenantNotFound(tenantID.String())
}

func (m *mockTenantStore) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	if m.getBySlugFn != nil {
		return m.getBySlugFn(ctx, slug)
	}
	return nil, fmt.Errorf("not found")
}

type mockRouter struct {
	getQuoteFn func(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error)
}

func (m *mockRouter) GetQuote(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error) {
	if m.getQuoteFn != nil {
		return m.getQuoteFn(ctx, tenantID, req)
	}
	return &domain.Quote{
		ID:             uuid.New(),
		TenantID:       tenantID,
		SourceCurrency: req.SourceCurrency,
		SourceAmount:   req.SourceAmount,
		DestCurrency:   req.DestCurrency,
		DestAmount:     req.SourceAmount.Mul(decimal.NewFromFloat(1.25)),
		StableAmount:   req.SourceAmount.Mul(decimal.NewFromFloat(1.25)),
		FXRate:         decimal.NewFromFloat(1.25),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(2.50),
			OffRampFee:  decimal.NewFromFloat(2.50),
			NetworkFee:  decimal.NewFromFloat(0.10),
			TotalFeeUSD: decimal.NewFromFloat(5.10),
		},
		Route: domain.RouteInfo{
			Chain:           "tron",
			StableCoin:      domain.CurrencyUSDT,
			OnRampProvider:  "mock-onramp",
			OffRampProvider: "mock-offramp",
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (m *mockRouter) GetRoutingOptions(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.RouteResult, error) {
	return &domain.RouteResult{
		ProviderID:       "mock-onramp",
		OffRampProvider:  "mock-offramp",
		BlockchainChain:  "tron",
		Corridor:         "GBP→USDT→NGN",
		Fee:              domain.Money{Amount: decimal.NewFromFloat(5.10), Currency: domain.CurrencyUSD},
		Rate:             decimal.NewFromFloat(1.25),
		StableAmount:     req.SourceAmount.Mul(decimal.NewFromFloat(1.25)),
		EstimatedSeconds: 300,
		Score:            decimal.NewFromFloat(0.85),
		ScoreBreakdown: domain.ScoreBreakdown{
			Cost:        decimal.NewFromFloat(0.95),
			Speed:       decimal.NewFromFloat(0.90),
			Liquidity:   decimal.NewFromFloat(1.0),
			Reliability: decimal.NewFromFloat(1.0),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func activeTenant() *domain.Tenant {
	return &domain.Tenant{
		ID:               uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		Name:             "Lemfi",
		Slug:             "lemfi",
		Status:           domain.TenantStatusActive,
		KYBStatus:        domain.KYBStatusVerified,
		SettlementModel:  domain.SettlementModelPrefunded,
		DailyLimitUSD:    decimal.NewFromFloat(1000000),
		PerTransferLimit: decimal.NewFromFloat(50000),
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  40,
			OffRampBPS: 40,
			MinFeeUSD:  decimal.NewFromFloat(1),
			MaxFeeUSD:  decimal.NewFromFloat(100),
		},
	}
}

func validRequest() CreateTransferRequest {
	return CreateTransferRequest{
		ExternalRef:    "ext-001",
		IdempotencyKey: "idem-001",
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "John Doe",
			Email:   "john@example.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Jane Doe",
			AccountNumber: "0123456789",
			BankName:      "GTBank",
			Country:       "NG",
		},
	}
}

type testHarness struct {
	engine    *Engine
	transfers *mockTransferStore
	tenants   *mockTenantStore
	router    *mockRouter
}

func newTestHarness() *testHarness {
	tenant := activeTenant()
	transfers := newMockTransferStore()
	tenants := &mockTenantStore{
		getFn: func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
			if tenantID == tenant.ID {
				return tenant, nil
			}
			return nil, domain.ErrTenantNotFound(tenantID.String())
		},
	}
	router := &mockRouter{}

	engine := NewEngine(transfers, tenants, router, nil, testLogger(), nil)

	return &testHarness{
		engine:    engine,
		transfers: transfers,
		tenants:   tenants,
		router:    router,
	}
}

// seedTransfer puts a transfer directly into the mock store for step tests.
func (h *testHarness) seedTransfer(t *domain.Transfer) {
	h.transfers.mu.Lock()
	defer h.transfers.mu.Unlock()
	h.transfers.transfers[t.ID] = t
}

// outboxHasIntent checks whether the captured outbox entries contain an intent with the given event type.
func outboxHasIntent(entries []domain.OutboxEntry, eventType string) bool {
	for _, e := range entries {
		if e.IsIntent && e.EventType == eventType {
			return true
		}
	}
	return false
}

// outboxHasEvent checks whether the captured outbox entries contain a non-intent event with the given type.
func outboxHasEvent(entries []domain.OutboxEntry, eventType string) bool {
	for _, e := range entries {
		if !e.IsIntent && e.EventType == eventType {
			return true
		}
	}
	return false
}

// countIntents counts entries matching the given intent type.
func countIntents(entries []domain.OutboxEntry, eventType string) int {
	n := 0
	for _, e := range entries {
		if e.IsIntent && e.EventType == eventType {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 1. CreateTransfer happy path
// ---------------------------------------------------------------------------

func TestCreateTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer, err := h.engine.CreateTransfer(ctx, tenant.ID, validRequest())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if transfer.Status != domain.TransferStatusCreated {
		t.Errorf("expected status CREATED, got %s", transfer.Status)
	}
	if transfer.TenantID != tenant.ID {
		t.Errorf("expected tenant ID %s, got %s", tenant.ID, transfer.TenantID)
	}
	if !transfer.SourceAmount.Equal(decimal.NewFromFloat(1000)) {
		t.Errorf("expected source amount 1000, got %s", transfer.SourceAmount)
	}

	// Verify outbox contains EventTransferCreated
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(entries))
	}
	if entries[0].EventType != domain.EventTransferCreated {
		t.Errorf("expected outbox event type %s, got %s", domain.EventTransferCreated, entries[0].EventType)
	}
	if entries[0].IsIntent {
		t.Error("expected outbox entry to be an event, not an intent")
	}
	if entries[0].TenantID != tenant.ID {
		t.Errorf("expected outbox tenant ID %s, got %s", tenant.ID, entries[0].TenantID)
	}
}

// ---------------------------------------------------------------------------
// 2. FundTransfer — outbox has IntentTreasuryReserve + EventTransferFunded
// ---------------------------------------------------------------------------

func TestFundTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.FundTransfer(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify status transitioned
	if transfer.Status != domain.TransferStatusFunded {
		t.Errorf("expected status FUNDED, got %s", transfer.Status)
	}

	// Verify outbox entries
	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryReserve) {
		t.Error("expected outbox to contain IntentTreasuryReserve")
	}
	if !outboxHasEvent(entries, domain.EventTransferFunded) {
		t.Error("expected outbox to contain EventTransferFunded")
	}

	// Verify reserve payload content
	for _, e := range entries {
		if e.EventType == domain.IntentTreasuryReserve {
			var payload domain.TreasuryReservePayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal reserve payload: %v", err)
			}
			if payload.TransferID != transfer.ID {
				t.Errorf("expected transfer ID %s in payload, got %s", transfer.ID, payload.TransferID)
			}
			if !payload.Amount.Equal(decimal.NewFromFloat(1000)) {
				t.Errorf("expected amount 1000 in payload, got %s", payload.Amount)
			}
			if payload.Location != "bank:gbp" {
				t.Errorf("expected location bank:gbp, got %s", payload.Location)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 3. InitiateOnRamp — outbox has IntentProviderOnRamp
// ---------------------------------------------------------------------------

func TestInitiateOnRamp_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:               uuid.New(),
		TenantID:         tenant.ID,
		Status:           domain.TransferStatusFunded,
		Version:          2,
		SourceCurrency:   domain.CurrencyGBP,
		SourceAmount:     decimal.NewFromFloat(1000),
		StableCoin:       domain.CurrencyUSDT,
		Chain:            "tron",
		OnRampProviderID: "mock-onramp",
	}
	h.seedTransfer(transfer)

	err := h.engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusOnRamping {
		t.Errorf("expected status ON_RAMPING, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentProviderOnRamp) {
		t.Error("expected outbox to contain IntentProviderOnRamp")
	}

	// Verify on-ramp payload
	for _, e := range entries {
		if e.EventType == domain.IntentProviderOnRamp {
			var payload domain.ProviderOnRampPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal on-ramp payload: %v", err)
			}
			if payload.ProviderID != "mock-onramp" {
				t.Errorf("expected provider ID mock-onramp, got %s", payload.ProviderID)
			}
			if payload.FromCurrency != domain.CurrencyGBP {
				t.Errorf("expected from currency GBP, got %s", payload.FromCurrency)
			}
			if payload.ToCurrency != domain.CurrencyUSDT {
				t.Errorf("expected to currency USDT, got %s", payload.ToCurrency)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 3b. InitiateOnRamp includes fallback alternatives from quote
// ---------------------------------------------------------------------------

func TestInitiateOnRamp_IncludesAlternatives(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	quoteID := uuid.New()
	quote := &domain.Quote{
		ID:             quoteID,
		TenantID:       tenant.ID,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		StableAmount:   decimal.NewFromFloat(1245),
		FXRate:         decimal.NewFromFloat(1.25),
		Route: domain.RouteInfo{
			Chain:           "tron",
			StableCoin:      domain.CurrencyUSDT,
			OnRampProvider:  "mock-onramp",
			OffRampProvider: "mock-offramp",
			AlternativeRoutes: []domain.RouteAlternative{
				{
					OnRampProvider:  "alt-onramp",
					OffRampProvider: "alt-offramp",
					Chain:           "ethereum",
					StableCoin:      domain.CurrencyUSDC,
					Fee:             domain.Money{Amount: decimal.NewFromFloat(6.0), Currency: domain.CurrencyUSD},
					Rate:            decimal.NewFromFloat(1.24),
					StableAmount:    decimal.NewFromFloat(1240),
					Score:           decimal.NewFromFloat(0.85),
				},
			},
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	}

	// Configure the mock store to return our quote
	h.transfers.getQuoteFn = func(_ context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		if qid == quoteID {
			return quote, nil
		}
		return nil, fmt.Errorf("not found")
	}

	transfer := &domain.Transfer{
		ID:               uuid.New(),
		TenantID:         tenant.ID,
		Status:           domain.TransferStatusFunded,
		Version:          2,
		SourceCurrency:   domain.CurrencyGBP,
		SourceAmount:     decimal.NewFromFloat(1000),
		StableCoin:       domain.CurrencyUSDT,
		Chain:            "tron",
		OnRampProviderID: "mock-onramp",
		QuoteID:          &quoteID,
	}
	h.seedTransfer(transfer)

	err := h.engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify the outbox payload contains alternatives
	entries := h.transfers.getOutboxEntries()
	for _, e := range entries {
		if e.EventType == domain.IntentProviderOnRamp {
			var payload domain.ProviderOnRampPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal on-ramp payload: %v", err)
			}
			if len(payload.Alternatives) != 1 {
				t.Fatalf("expected 1 alternative, got %d", len(payload.Alternatives))
			}
			alt := payload.Alternatives[0]
			if alt.ProviderID != "alt-onramp" {
				t.Errorf("expected alt provider alt-onramp, got %s", alt.ProviderID)
			}
			if alt.OffRampProvider != "alt-offramp" {
				t.Errorf("expected alt off-ramp alt-offramp, got %s", alt.OffRampProvider)
			}
			if alt.Chain != "ethereum" {
				t.Errorf("expected alt chain ethereum, got %s", alt.Chain)
			}
			if alt.StableCoin != domain.CurrencyUSDC {
				t.Errorf("expected alt stablecoin USDC, got %s", alt.StableCoin)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 4. HandleOnRampResult success — outbox has IntentLedgerPost + IntentBlockchainSend
// ---------------------------------------------------------------------------

func TestHandleOnRampResult_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:               uuid.New(),
		TenantID:         tenant.ID,
		Status:           domain.TransferStatusOnRamping,
		Version:          3,
		SourceCurrency:   domain.CurrencyGBP,
		SourceAmount:     decimal.NewFromFloat(1000),
		StableCoin:       domain.CurrencyUSDT,
		StableAmount:     decimal.NewFromFloat(1250),
		Chain:            "tron",
		OnRampProviderID: "mock-onramp",
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true, ProviderRef: "prov-123"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusSettling {
		t.Errorf("expected status SETTLING, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentLedgerPost) {
		t.Error("expected outbox to contain IntentLedgerPost")
	}
	if !outboxHasIntent(entries, domain.IntentBlockchainSend) {
		t.Error("expected outbox to contain IntentBlockchainSend")
	}
	if !outboxHasEvent(entries, domain.EventOnRampCompleted) {
		t.Error("expected outbox to contain EventOnRampCompleted")
	}

	// Verify blockchain payload
	for _, e := range entries {
		if e.EventType == domain.IntentBlockchainSend {
			var payload domain.BlockchainSendPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal blockchain payload: %v", err)
			}
			if payload.Chain != "tron" {
				t.Errorf("expected chain tron, got %s", payload.Chain)
			}
			if payload.Token != "USDT" {
				t.Errorf("expected token USDT, got %s", payload.Token)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 5. HandleOnRampResult failure — outbox has IntentTreasuryRelease
// ---------------------------------------------------------------------------

func TestHandleOnRampResult_Failure(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOnRamping,
		Version:        3,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "provider unavailable",
		ErrorCode: "PROVIDER_DOWN",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusRefunding {
		t.Errorf("expected status REFUNDING, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
	if !outboxHasEvent(entries, domain.EventProviderOnRampFailed) {
		t.Error("expected outbox to contain EventProviderOnRampFailed")
	}
}

// ---------------------------------------------------------------------------
// 6. HandleSettlementResult success — outbox has IntentProviderOffRamp
// ---------------------------------------------------------------------------

func TestHandleSettlementResult_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:                uuid.New(),
		TenantID:          tenant.ID,
		Status:            domain.TransferStatusSettling,
		Version:           4,
		SourceCurrency:    domain.CurrencyGBP,
		SourceAmount:      decimal.NewFromFloat(1000),
		DestCurrency:      domain.CurrencyNGN,
		StableCoin:        domain.CurrencyUSDT,
		StableAmount:      decimal.NewFromFloat(1250),
		Chain:             "tron",
		OffRampProviderID: "mock-offramp",
		Recipient: domain.Recipient{
			Name:    "Jane Doe",
			Country: "NG",
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true, TxHash: "0xabc"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusOffRamping {
		t.Errorf("expected status OFF_RAMPING, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentProviderOffRamp) {
		t.Error("expected outbox to contain IntentProviderOffRamp")
	}
	if !outboxHasEvent(entries, domain.EventSettlementCompleted) {
		t.Error("expected outbox to contain EventSettlementCompleted")
	}

	// Verify off-ramp payload
	for _, e := range entries {
		if e.EventType == domain.IntentProviderOffRamp {
			var payload domain.ProviderOffRampPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal off-ramp payload: %v", err)
			}
			if payload.ProviderID != "mock-offramp" {
				t.Errorf("expected provider ID mock-offramp, got %s", payload.ProviderID)
			}
			if payload.Recipient.Country != "NG" {
				t.Errorf("expected recipient country NG, got %s", payload.Recipient.Country)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 7. HandleSettlementResult failure
// ---------------------------------------------------------------------------

func TestHandleSettlementResult_Failure(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusSettling,
		Version:        4,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "blockchain timeout",
		ErrorCode: "CHAIN_TIMEOUT",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Error("expected outbox to contain IntentLedgerReverse")
	}
	if !outboxHasEvent(entries, domain.EventBlockchainFailed) {
		t.Error("expected outbox to contain EventBlockchainFailed")
	}
}

// ---------------------------------------------------------------------------
// 8. HandleOffRampResult success — triggers CompleteTransfer
// ---------------------------------------------------------------------------

func TestHandleOffRampResult_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOffRamping,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500000),
		Fees: domain.FeeBreakdown{
			TotalFeeUSD: decimal.NewFromFloat(5.10),
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusCompleted {
		t.Errorf("expected status COMPLETED, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryConsume) {
		t.Error("expected outbox to contain IntentTreasuryConsume")
	}
	if !outboxHasIntent(entries, domain.IntentLedgerPost) {
		t.Error("expected outbox to contain IntentLedgerPost")
	}
	if !outboxHasIntent(entries, domain.IntentWebhookDeliver) {
		t.Error("expected outbox to contain IntentWebhookDeliver")
	}
	if !outboxHasEvent(entries, domain.EventTransferCompleted) {
		t.Error("expected outbox to contain EventTransferCompleted")
	}
}

// ---------------------------------------------------------------------------
// 9. HandleOffRampResult failure
// ---------------------------------------------------------------------------

func TestHandleOffRampResult_Failure(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOffRamping,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success:   false,
		Error:     "payout failed",
		ErrorCode: "PAYOUT_FAILED",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Error("expected outbox to contain IntentLedgerReverse")
	}
	if !outboxHasIntent(entries, domain.IntentWebhookDeliver) {
		t.Error("expected outbox to contain IntentWebhookDeliver")
	}
}

// ---------------------------------------------------------------------------
// 10. CompleteTransfer — outbox has completion intents
// ---------------------------------------------------------------------------

func TestCompleteTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOffRamping,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500000),
		Fees: domain.FeeBreakdown{
			TotalFeeUSD: decimal.NewFromFloat(8.50),
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.CompleteTransfer(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusCompleted {
		t.Errorf("expected status COMPLETED, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryConsume) {
		t.Error("expected outbox to contain IntentTreasuryConsume")
	}
	if !outboxHasIntent(entries, domain.IntentLedgerPost) {
		t.Error("expected outbox to contain IntentLedgerPost")
	}
	if !outboxHasIntent(entries, domain.IntentWebhookDeliver) {
		t.Error("expected outbox to contain IntentWebhookDeliver")
	}
	if !outboxHasEvent(entries, domain.EventTransferCompleted) {
		t.Error("expected outbox to contain EventTransferCompleted")
	}

	// Verify ledger payload has correct entries
	for _, e := range entries {
		if e.EventType == domain.IntentLedgerPost {
			var payload domain.LedgerPostPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal ledger payload: %v", err)
			}
			if len(payload.Lines) != 3 {
				t.Errorf("expected 3 ledger lines, got %d", len(payload.Lines))
			}
			if payload.IdempotencyKey == "" {
				t.Error("expected non-empty idempotency key in ledger payload")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 11. FailTransfer — outbox has IntentTreasuryRelease + IntentWebhookDeliver
// ---------------------------------------------------------------------------

func TestFailTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusSettling,
		Version:        3,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
	}
	h.seedTransfer(transfer)

	err := h.engine.FailTransfer(ctx, transfer.TenantID, transfer.ID, "provider timeout", "TIMEOUT")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
	if !outboxHasIntent(entries, domain.IntentWebhookDeliver) {
		t.Error("expected outbox to contain IntentWebhookDeliver")
	}
	if !outboxHasEvent(entries, domain.EventTransferFailed) {
		t.Error("expected outbox to contain EventTransferFailed")
	}
}

func TestFailTransfer_FromOnRamping_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOnRamping,
		Version:        2,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
	}
	h.seedTransfer(transfer)

	err := h.engine.FailTransfer(ctx, transfer.TenantID, transfer.ID, "provider confirmed failure", "PROVIDER_FAILED")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}
}

// ---------------------------------------------------------------------------
// 12. InitiateRefund — outbox has IntentLedgerReverse + IntentTreasuryRelease
// ---------------------------------------------------------------------------

func TestInitiateRefund_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusFailed,
		Version:        4,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.InitiateRefund(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if transfer.Status != domain.TransferStatusRefunding {
		t.Errorf("expected status REFUNDING, got %s", transfer.Status)
	}

	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Error("expected outbox to contain IntentLedgerReverse")
	}
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
	if !outboxHasEvent(entries, domain.EventRefundInitiated) {
		t.Error("expected outbox to contain EventRefundInitiated")
	}
}

// ---------------------------------------------------------------------------
// 13. Tenant validation — suspended tenant rejected
// ---------------------------------------------------------------------------

func TestCreateTransfer_TenantSuspended(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	suspendedTenantID := uuid.New()
	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		return &domain.Tenant{
			ID:        suspendedTenantID,
			Status:    domain.TenantStatusSuspended,
			KYBStatus: domain.KYBStatusVerified,
		}, nil
	}

	_, err := h.engine.CreateTransfer(ctx, suspendedTenantID, validRequest())
	if err == nil {
		t.Fatal("expected error for suspended tenant")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeTenantSuspended {
		t.Errorf("expected ErrTenantSuspended, got %v", err)
	}
}

func TestCreateTransfer_TenantKYBPending(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	tenantID := uuid.New()
	h.tenants.getFn = func(ctx context.Context, tid uuid.UUID) (*domain.Tenant, error) {
		return &domain.Tenant{
			ID:        tenantID,
			Status:    domain.TenantStatusActive,
			KYBStatus: domain.KYBStatusPending,
		}, nil
	}

	_, err := h.engine.CreateTransfer(ctx, tenantID, validRequest())
	if err == nil {
		t.Fatal("expected error for KYB pending tenant")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeTenantSuspended {
		t.Errorf("expected ErrTenantSuspended, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 14. Daily limit exceeded
// ---------------------------------------------------------------------------

func TestCreateTransfer_DailyLimitExceeded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	tenant.DailyLimitUSD = decimal.NewFromFloat(500)

	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		return tenant, nil
	}

	req := validRequest()
	req.SourceAmount = decimal.NewFromFloat(1000)

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for daily limit exceeded")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeDailyLimitExceeded {
		t.Errorf("expected ErrDailyLimitExceeded, got %v", err)
	}
}

func TestCreateTransfer_PerTransferLimitExceeded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	tenant.PerTransferLimit = decimal.NewFromFloat(500)

	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		return tenant, nil
	}

	req := validRequest()
	req.SourceAmount = decimal.NewFromFloat(1000)

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for per-transfer limit exceeded")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeAmountTooHigh {
		t.Errorf("expected ErrAmountTooHigh, got %v", err)
	}
}

func TestCreateTransfer_PerTransferLimitUsesUSDEquivalent(t *testing.T) {
	// A 400 GBP transfer with a 1.25x FX rate has StableAmount = 500 USDT.
	// With a per-transfer limit of 499 USD, this should be rejected even though
	// the raw source amount (400) is below the limit.
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	tenant.PerTransferLimit = decimal.NewFromFloat(499)

	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		return tenant, nil
	}

	req := validRequest()
	req.SourceAmount = decimal.NewFromFloat(400) // 400 GBP → StableAmount = 500 USDT (mock uses 1.25x)

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error: 400 GBP = 500 USD equivalent exceeds 499 USD limit")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeAmountTooHigh {
		t.Errorf("expected ErrAmountTooHigh, got %v", err)
	}
}

func TestCreateTransfer_DailyLimitUsesUSDEquivalent(t *testing.T) {
	// A 400 GBP transfer with a 1.25x FX rate has StableAmount = 500 USDT.
	// With a daily limit of 499 USD, this should be rejected even though
	// the raw source amount (400) is below the limit.
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	tenant.DailyLimitUSD = decimal.NewFromFloat(499)

	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		return tenant, nil
	}

	req := validRequest()
	req.SourceAmount = decimal.NewFromFloat(400) // 400 GBP → StableAmount = 500 USDT

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error: 400 GBP = 500 USD equivalent exceeds 499 USD daily limit")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeDailyLimitExceeded {
		t.Errorf("expected ErrDailyLimitExceeded, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 15. Idempotency — duplicate returns existing
// ---------------------------------------------------------------------------

func TestCreateTransfer_IdempotentReturn(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	existingTransfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		IdempotencyKey: "idem-001",
		Status:         domain.TransferStatusCreated,
	}

	h.transfers.getByIdempotencyKeyFn = func(ctx context.Context, tid uuid.UUID, key string) (*domain.Transfer, error) {
		if tid == tenant.ID && key == "idem-001" {
			return existingTransfer, nil
		}
		return nil, fmt.Errorf("not found")
	}

	transfer, err := h.engine.CreateTransfer(ctx, tenant.ID, validRequest())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if transfer.ID != existingTransfer.ID {
		t.Errorf("expected existing transfer ID %s, got %s", existingTransfer.ID, transfer.ID)
	}

	// No outbox entries should be created for idempotent return
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries for idempotent return, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// 16. Optimistic lock — concurrent transitions fail
// ---------------------------------------------------------------------------

func TestFundTransfer_OptimisticLockConflict(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	// Simulate version mismatch — return the same sentinel the real store uses
	h.transfers.transitionWithOutboxFn = func(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error {
		return fmt.Errorf("settla-store: transfer %s: %w", transferID, ErrOptimisticLock)
	}

	err := h.engine.FundTransfer(ctx, transfer.TenantID, transfer.ID)
	if err == nil {
		t.Fatal("expected optimistic lock error")
	}

	// The engine wraps the error with "concurrent modification" context
	if !errors.Is(err, ErrOptimisticLock) {
		t.Errorf("expected ErrOptimisticLock in error chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "concurrent modification") {
		t.Errorf("expected 'concurrent modification' in error message, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// State machine validation tests
// ---------------------------------------------------------------------------

func TestFundTransfer_InvalidState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCompleted,
		Version:  5,
	}
	h.seedTransfer(transfer)

	err := h.engine.FundTransfer(ctx, transfer.TenantID, transfer.ID)
	if err == nil {
		t.Fatal("expected error for invalid state")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestCompleteTransfer_InvalidState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCreated,
		Version:  1,
	}
	h.seedTransfer(transfer)

	err := h.engine.CompleteTransfer(ctx, transfer.TenantID, transfer.ID)
	if err == nil {
		t.Fatal("expected error for invalid state")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestFailTransfer_InvalidState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCompleted,
		Version:  6,
	}
	h.seedTransfer(transfer)

	err := h.engine.FailTransfer(ctx, transfer.TenantID, transfer.ID, "test", "TEST")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation tests
// ---------------------------------------------------------------------------

func TestCreateTransfer_ZeroAmount(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	req.SourceAmount = decimal.Zero

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for zero amount")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeAmountTooLow {
		t.Errorf("expected ErrAmountTooLow, got %v", err)
	}
}

func TestCreateTransfer_UnsupportedCurrency(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	req.SourceCurrency = domain.Currency("XYZ")

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestCreateTransfer_MissingRecipient(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	req.Recipient = domain.Recipient{}

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
}

// ---------------------------------------------------------------------------
// Quote validation tests
// ---------------------------------------------------------------------------

func TestCreateTransfer_QuoteFromDifferentTenant(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	quoteID := uuid.New()
	otherTenantID := uuid.New()
	h.transfers.getQuoteFn = func(ctx context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		return &domain.Quote{
			ID:        quoteID,
			TenantID:  otherTenantID,
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}, nil
	}

	req := validRequest()
	req.QuoteID = &quoteID

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for quote from different tenant")
	}
}

func TestCreateTransfer_ExpiredQuote(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	quoteID := uuid.New()
	h.transfers.getQuoteFn = func(ctx context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		return &domain.Quote{
			ID:        quoteID,
			TenantID:  tenant.ID,
			ExpiresAt: time.Now().UTC().Add(-5 * time.Minute),
		}, nil
	}

	req := validRequest()
	req.QuoteID = &quoteID

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for expired quote")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeQuoteExpired {
		t.Errorf("expected ErrQuoteExpired, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ProcessTransfer full pipeline
// ---------------------------------------------------------------------------

func TestProcessTransfer_FullPipeline(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:                uuid.New(),
		TenantID:          tenant.ID,
		Status:            domain.TransferStatusCreated,
		Version:           1,
		SourceCurrency:    domain.CurrencyGBP,
		SourceAmount:      decimal.NewFromFloat(1000),
		DestCurrency:      domain.CurrencyNGN,
		DestAmount:        decimal.NewFromFloat(500000),
		StableCoin:        domain.CurrencyUSDT,
		StableAmount:      decimal.NewFromFloat(1250),
		Chain:             "tron",
		OnRampProviderID:  "mock-onramp",
		OffRampProviderID: "mock-offramp",
		FXRate:            decimal.NewFromFloat(1.25),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(4),
			OffRampFee:  decimal.NewFromFloat(4),
			NetworkFee:  decimal.NewFromFloat(0.50),
			TotalFeeUSD: decimal.NewFromFloat(8.50),
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.ProcessTransfer(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify final status
	if transfer.Status != domain.TransferStatusCompleted {
		t.Errorf("expected status COMPLETED, got %s", transfer.Status)
	}

	// Verify outbox contains all expected entries from the pipeline
	entries := h.transfers.getOutboxEntries()

	// Should have treasury reserve (fund), on-ramp intent, ledger+blockchain (on-ramp result),
	// off-ramp intent (settlement result), treasury release+ledger+webhook (complete)
	if len(entries) == 0 {
		t.Fatal("expected outbox entries from pipeline")
	}

	// Key intents that must appear
	if countIntents(entries, domain.IntentTreasuryReserve) != 1 {
		t.Error("expected exactly 1 IntentTreasuryReserve")
	}
	if countIntents(entries, domain.IntentProviderOnRamp) != 1 {
		t.Error("expected exactly 1 IntentProviderOnRamp")
	}
	if countIntents(entries, domain.IntentBlockchainSend) != 1 {
		t.Error("expected exactly 1 IntentBlockchainSend")
	}
	if countIntents(entries, domain.IntentProviderOffRamp) != 1 {
		t.Error("expected exactly 1 IntentProviderOffRamp")
	}
	if countIntents(entries, domain.IntentTreasuryConsume) != 1 {
		t.Error("expected exactly 1 IntentTreasuryConsume")
	}
	if countIntents(entries, domain.IntentWebhookDeliver) != 1 {
		t.Error("expected exactly 1 IntentWebhookDeliver")
	}
}

// ---------------------------------------------------------------------------
// Multi-tenant isolation
// ---------------------------------------------------------------------------

func TestCreateTransfer_DifferentTenantSameKey(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	tenantA := activeTenant()
	tenantB := &domain.Tenant{
		ID:               uuid.MustParse("b0000000-0000-0000-0000-000000000002"),
		Name:             "Fincra",
		Slug:             "fincra",
		Status:           domain.TenantStatusActive,
		KYBStatus:        domain.KYBStatusVerified,
		DailyLimitUSD:    decimal.NewFromFloat(1000000),
		PerTransferLimit: decimal.NewFromFloat(50000),
	}

	h.tenants.getFn = func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		switch tenantID {
		case tenantA.ID:
			return tenantA, nil
		case tenantB.ID:
			return tenantB, nil
		}
		return nil, domain.ErrTenantNotFound(tenantID.String())
	}

	h.transfers.getByIdempotencyKeyFn = func(ctx context.Context, tid uuid.UUID, key string) (*domain.Transfer, error) {
		return nil, fmt.Errorf("not found")
	}

	req := validRequest()
	transferA, err := h.engine.CreateTransfer(ctx, tenantA.ID, req)
	if err != nil {
		t.Fatalf("tenant A: expected no error, got %v", err)
	}
	transferB, err := h.engine.CreateTransfer(ctx, tenantB.ID, req)
	if err != nil {
		t.Fatalf("tenant B: expected no error, got %v", err)
	}
	if transferA.ID == transferB.ID {
		t.Error("expected different transfer IDs for different tenants with same key")
	}
}

// ---------------------------------------------------------------------------
// Terminal state tests
// ---------------------------------------------------------------------------

func TestTerminalState_CompletedRejectsAllTransitions(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCompleted,
		Version:        6,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	// FundTransfer expects CREATED
	if err := h.engine.FundTransfer(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("FundTransfer: expected error for COMPLETED transfer")
	} else if !strings.Contains(err.Error(), "invalid transition") {
		t.Errorf("FundTransfer: expected 'invalid transition' in error, got: %v", err)
	}

	// InitiateOnRamp expects FUNDED
	if err := h.engine.InitiateOnRamp(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("InitiateOnRamp: expected error for COMPLETED transfer")
	}

	// HandleOnRampResult expects ON_RAMPING
	if err := h.engine.HandleOnRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOnRampResult: expected error for COMPLETED transfer")
	}

	// HandleSettlementResult expects SETTLING
	if err := h.engine.HandleSettlementResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleSettlementResult: expected error for COMPLETED transfer")
	}

	// HandleOffRampResult (success=true) calls CompleteTransfer which expects OFF_RAMPING
	if err := h.engine.HandleOffRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOffRampResult: expected error for COMPLETED transfer")
	}

	// FailTransfer — COMPLETED has no transition to FAILED
	err := h.engine.FailTransfer(ctx, tenant.ID, transfer.ID, "test", "TEST")
	if err == nil {
		t.Error("FailTransfer: expected error for COMPLETED transfer")
	} else {
		var de *domain.DomainError
		if !errors.As(err, &de) {
			t.Errorf("FailTransfer: expected *domain.DomainError, got %T", err)
		} else if de.Code() != domain.CodeInvalidTransition {
			t.Errorf("FailTransfer: expected code %s, got %s", domain.CodeInvalidTransition, de.Code())
		}
	}

	// InitiateRefund — COMPLETED has no transition to REFUNDING
	err = h.engine.InitiateRefund(ctx, tenant.ID, transfer.ID)
	if err == nil {
		t.Error("InitiateRefund: expected error for COMPLETED transfer")
	} else {
		var de *domain.DomainError
		if !errors.As(err, &de) {
			t.Errorf("InitiateRefund: expected *domain.DomainError, got %T", err)
		} else if de.Code() != domain.CodeInvalidTransition {
			t.Errorf("InitiateRefund: expected code %s, got %s", domain.CodeInvalidTransition, de.Code())
		}
	}

	// HandleRefundResult expects REFUNDING
	if err := h.engine.HandleRefundResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleRefundResult: expected error for COMPLETED transfer")
	}

	// Verify transfer is still COMPLETED
	if transfer.Status != domain.TransferStatusCompleted {
		t.Errorf("expected status to remain COMPLETED, got %s", transfer.Status)
	}

	// Verify no outbox entries were written
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries, got %d", len(entries))
	}
}

func TestTerminalState_RefundedRejectsAllTransitions(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusRefunded,
		Version:        7,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	// FundTransfer expects CREATED
	if err := h.engine.FundTransfer(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("FundTransfer: expected error for REFUNDED transfer")
	} else if !strings.Contains(err.Error(), "invalid transition") {
		t.Errorf("FundTransfer: expected 'invalid transition' in error, got: %v", err)
	}

	// InitiateOnRamp expects FUNDED
	if err := h.engine.InitiateOnRamp(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("InitiateOnRamp: expected error for REFUNDED transfer")
	}

	// HandleOnRampResult expects ON_RAMPING
	if err := h.engine.HandleOnRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOnRampResult: expected error for REFUNDED transfer")
	}

	// HandleSettlementResult expects SETTLING
	if err := h.engine.HandleSettlementResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleSettlementResult: expected error for REFUNDED transfer")
	}

	// HandleOffRampResult (success=true) calls CompleteTransfer which expects OFF_RAMPING
	if err := h.engine.HandleOffRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOffRampResult: expected error for REFUNDED transfer")
	}

	// FailTransfer — REFUNDED has no transition to FAILED
	err := h.engine.FailTransfer(ctx, tenant.ID, transfer.ID, "test", "TEST")
	if err == nil {
		t.Error("FailTransfer: expected error for REFUNDED transfer")
	} else {
		var de *domain.DomainError
		if !errors.As(err, &de) {
			t.Errorf("FailTransfer: expected *domain.DomainError, got %T", err)
		} else if de.Code() != domain.CodeInvalidTransition {
			t.Errorf("FailTransfer: expected code %s, got %s", domain.CodeInvalidTransition, de.Code())
		}
	}

	// InitiateRefund — REFUNDED has no transition to REFUNDING
	err = h.engine.InitiateRefund(ctx, tenant.ID, transfer.ID)
	if err == nil {
		t.Error("InitiateRefund: expected error for REFUNDED transfer")
	} else {
		var de *domain.DomainError
		if !errors.As(err, &de) {
			t.Errorf("InitiateRefund: expected *domain.DomainError, got %T", err)
		} else if de.Code() != domain.CodeInvalidTransition {
			t.Errorf("InitiateRefund: expected code %s, got %s", domain.CodeInvalidTransition, de.Code())
		}
	}

	// HandleRefundResult expects REFUNDING
	if err := h.engine.HandleRefundResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleRefundResult: expected error for REFUNDED transfer")
	}

	// Verify transfer is still REFUNDED
	if transfer.Status != domain.TransferStatusRefunded {
		t.Errorf("expected status to remain REFUNDED, got %s", transfer.Status)
	}

	// Verify no outbox entries were written
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries, got %d", len(entries))
	}
}

func TestTerminalState_FailedOnlyAllowsRefunding(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusFailed,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	// All transitions except InitiateRefund should fail.

	if err := h.engine.FundTransfer(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("FundTransfer: expected error for FAILED transfer")
	}

	if err := h.engine.InitiateOnRamp(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("InitiateOnRamp: expected error for FAILED transfer")
	}

	if err := h.engine.HandleOnRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOnRampResult: expected error for FAILED transfer")
	}

	if err := h.engine.HandleSettlementResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleSettlementResult: expected error for FAILED transfer")
	}

	if err := h.engine.HandleOffRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleOffRampResult: expected error for FAILED transfer")
	}

	if err := h.engine.CompleteTransfer(ctx, tenant.ID, transfer.ID); err == nil {
		t.Error("CompleteTransfer: expected error for FAILED transfer")
	}

	if err := h.engine.HandleRefundResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true}); err == nil {
		t.Error("HandleRefundResult: expected error for FAILED transfer")
	}

	// Verify no outbox entries from rejected transitions
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Fatalf("expected 0 outbox entries before InitiateRefund, got %d", len(entries))
	}

	// InitiateRefund should succeed (FAILED → REFUNDING is allowed)
	if err := h.engine.InitiateRefund(ctx, tenant.ID, transfer.ID); err != nil {
		t.Fatalf("InitiateRefund: expected nil error, got %v", err)
	}

	// Verify status transitioned to REFUNDING
	if transfer.Status != domain.TransferStatusRefunding {
		t.Errorf("expected status REFUNDING, got %s", transfer.Status)
	}

	// Verify outbox has IntentLedgerReverse and IntentTreasuryRelease
	entries = h.transfers.getOutboxEntries()
	if len(entries) == 0 {
		t.Fatal("expected outbox entries after InitiateRefund, got 0")
	}
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Error("expected outbox to contain IntentLedgerReverse")
	}
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected outbox to contain IntentTreasuryRelease")
	}
}

// ---------------------------------------------------------------------------
// Atomic state + outbox tests
// ---------------------------------------------------------------------------

func TestAtomicStateOutbox_TransitionAlwaysWritesBoth(t *testing.T) {
	tenant := activeTenant()

	tests := []struct {
		name       string
		fromStatus domain.TransferStatus
		version    int64
		action     func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error
	}{
		{
			name:       "CREATED→FUNDED via FundTransfer",
			fromStatus: domain.TransferStatusCreated,
			version:    1,
			action: func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error {
				return e.FundTransfer(ctx, tenantID, transferID)
			},
		},
		{
			name:       "FUNDED→ON_RAMPING via InitiateOnRamp",
			fromStatus: domain.TransferStatusFunded,
			version:    2,
			action: func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error {
				return e.InitiateOnRamp(ctx, tenantID, transferID)
			},
		},
		{
			name:       "ON_RAMPING→SETTLING via HandleOnRampResult",
			fromStatus: domain.TransferStatusOnRamping,
			version:    3,
			action: func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error {
				return e.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{Success: true})
			},
		},
		{
			name:       "SETTLING→OFF_RAMPING via HandleSettlementResult",
			fromStatus: domain.TransferStatusSettling,
			version:    4,
			action: func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error {
				return e.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{Success: true, TxHash: "0xtest"})
			},
		},
		{
			name:       "OFF_RAMPING→COMPLETED via CompleteTransfer",
			fromStatus: domain.TransferStatusOffRamping,
			version:    5,
			action: func(ctx context.Context, e *Engine, tenantID, transferID uuid.UUID) error {
				return e.CompleteTransfer(ctx, tenantID, transferID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()

			transfer := &domain.Transfer{
				ID:                uuid.New(),
				TenantID:          tenant.ID,
				Status:            tt.fromStatus,
				Version:           tt.version,
				SourceCurrency:    domain.CurrencyGBP,
				SourceAmount:      decimal.NewFromFloat(1000),
				DestCurrency:      domain.CurrencyNGN,
				DestAmount:        decimal.NewFromFloat(500000),
				StableCoin:        domain.CurrencyUSDT,
				StableAmount:      decimal.NewFromFloat(1250),
				Chain:             "tron",
				OnRampProviderID:  "mock-onramp",
				OffRampProviderID: "mock-offramp",
				Fees: domain.FeeBreakdown{
					OnRampFee:   decimal.NewFromFloat(2.50),
					OffRampFee:  decimal.NewFromFloat(2.50),
					NetworkFee:  decimal.NewFromFloat(0.10),
					TotalFeeUSD: decimal.NewFromFloat(5.10),
				},
			}
			h.seedTransfer(transfer)

			if err := tt.action(ctx, h.engine, tenant.ID, transfer.ID); err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			entries := h.transfers.getOutboxEntries()
			if len(entries) < 1 {
				t.Errorf("expected at least 1 outbox entry after transition, got %d", len(entries))
			}
		})
	}
}

func TestAtomicStateOutbox_OptimisticLockConcurrency(t *testing.T) {
	h := newTestHarness()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	// Use a counter to let the first call succeed and the second return optimistic lock.
	var callCount int64
	var callMu sync.Mutex

	// Override GetTransfer to return a copy so concurrent goroutines don't
	// race on the shared transfer pointer fields.
	h.transfers.getFn = func(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error) {
		h.transfers.mu.Lock()
		defer h.transfers.mu.Unlock()
		t, ok := h.transfers.transfers[id]
		if !ok {
			return nil, domain.ErrTransferNotFound(id.String())
		}
		cp := *t
		return &cp, nil
	}

	h.transfers.transitionWithOutboxFn = func(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error {
		callMu.Lock()
		callCount++
		n := callCount
		callMu.Unlock()

		if n == 1 {
			// First call succeeds: update the in-memory transfer
			h.transfers.mu.Lock()
			tr := h.transfers.transfers[transferID]
			tr.Status = newStatus
			tr.Version++
			h.transfers.outboxEntries = append(h.transfers.outboxEntries, entries...)
			h.transfers.mu.Unlock()
			return nil
		}
		// Second call: optimistic lock conflict
		return fmt.Errorf("settla-store: transfer %s: %w", transferID, ErrOptimisticLock)
	}

	var wg sync.WaitGroup
	var successCount, failCount int64
	var resultMu sync.Mutex

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			err := h.engine.FundTransfer(context.Background(), tenant.ID, transfer.ID)
			resultMu.Lock()
			defer resultMu.Unlock()
			if err == nil {
				successCount++
			} else {
				// Either optimistic lock or invalid state transition — both are
				// valid concurrent rejection outcomes depending on goroutine ordering.
				failCount++
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d (failures: %d)", successCount, failCount)
	}
	if failCount != 1 {
		t.Errorf("expected exactly 1 failure, got %d (successes: %d)", failCount, successCount)
	}

	// Verify version incremented exactly once (from 1 to 2).
	// Read directly from the store map under lock to avoid a race.
	h.transfers.mu.Lock()
	finalVersion := h.transfers.transfers[transfer.ID].Version
	h.transfers.mu.Unlock()
	if finalVersion != 2 {
		t.Errorf("expected version 2, got %d", finalVersion)
	}
}

func TestAtomicStateOutbox_FailedTransitionNoOrphanOutbox(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	// CREATED → COMPLETED is not a valid transition
	err := h.engine.CompleteTransfer(ctx, tenant.ID, transfer.ID)
	if err == nil {
		t.Fatal("expected error for invalid transition CREATED→COMPLETED")
	}

	// Verify no orphaned outbox entries
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries after invalid transition, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// HandleOffRampResult failure path includes reversal ledger lines
// ---------------------------------------------------------------------------

func TestHandleOffRampResult_Failure_HasReversalLines(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:                uuid.New(),
		TenantID:          tenant.ID,
		Status:            domain.TransferStatusOffRamping,
		Version:           5,
		SourceCurrency:    domain.CurrencyGBP,
		SourceAmount:      decimal.NewFromFloat(1000),
		StableCoin:        domain.CurrencyUSDT,
		StableAmount:      decimal.NewFromFloat(1250),
		Chain:             "tron",
		OnRampProviderID:  "mock-onramp",
		OffRampProviderID: "mock-offramp",
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(4.00),
			OffRampFee:  decimal.NewFromFloat(3.50),
			NetworkFee:  decimal.NewFromFloat(0.10),
			TotalFeeUSD: decimal.NewFromFloat(7.60),
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleOffRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "provider timeout",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify status transitioned to FAILED
	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}

	// Verify outbox contains a ledger reverse intent
	entries := h.transfers.getOutboxEntries()
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Fatal("expected outbox to contain IntentLedgerReverse")
	}

	// Unmarshal the ledger reverse payload and verify lines
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentLedgerReverse {
			var payload domain.LedgerPostPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal reverse payload: %v", err)
			}
			if len(payload.Lines) != 3 {
				t.Fatalf("expected 3 reversal lines, got %d", len(payload.Lines))
			}

			// Line 0: credit crypto asset
			if payload.Lines[0].AccountCode != "assets:crypto:usdt:tron" {
				t.Errorf("line 0: expected account assets:crypto:usdt:tron, got %s", payload.Lines[0].AccountCode)
			}
			if payload.Lines[0].EntryType != string(domain.EntryTypeCredit) {
				t.Errorf("line 0: expected CREDIT, got %s", payload.Lines[0].EntryType)
			}
			if !payload.Lines[0].Amount.Equal(decimal.NewFromFloat(1250)) {
				t.Errorf("line 0: expected amount 1250, got %s", payload.Lines[0].Amount)
			}

			// Line 1: credit on-ramp fee
			if payload.Lines[1].AccountCode != "expenses:provider:onramp" {
				t.Errorf("line 1: expected account expenses:provider:onramp, got %s", payload.Lines[1].AccountCode)
			}
			if payload.Lines[1].EntryType != string(domain.EntryTypeCredit) {
				t.Errorf("line 1: expected CREDIT, got %s", payload.Lines[1].EntryType)
			}
			if !payload.Lines[1].Amount.Equal(decimal.NewFromFloat(4.00)) {
				t.Errorf("line 1: expected amount 4.00, got %s", payload.Lines[1].Amount)
			}

			// Line 2: debit clearing account
			expectedClearing := "tenant:lemfi:assets:bank:gbp:clearing"
			if payload.Lines[2].AccountCode != expectedClearing {
				t.Errorf("line 2: expected account %s, got %s", expectedClearing, payload.Lines[2].AccountCode)
			}
			if payload.Lines[2].EntryType != string(domain.EntryTypeDebit) {
				t.Errorf("line 2: expected DEBIT, got %s", payload.Lines[2].EntryType)
			}
			if !payload.Lines[2].Amount.Equal(decimal.NewFromFloat(1000)) {
				t.Errorf("line 2: expected amount 1000, got %s", payload.Lines[2].Amount)
			}

			// Verify reference type and idempotency key
			if payload.ReferenceType != "reversal" {
				t.Errorf("expected reference type 'reversal', got %s", payload.ReferenceType)
			}
			expectedKey := fmt.Sprintf("reverse-offramp:%s", transfer.ID)
			if payload.IdempotencyKey != expectedKey {
				t.Errorf("expected idempotency key %s, got %s", expectedKey, payload.IdempotencyKey)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrent state transition test (Issue #27)
// ---------------------------------------------------------------------------

func TestHandleOnRampResult_ConcurrentTransitions(t *testing.T) {
	h := newTestHarness()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:               uuid.New(),
		TenantID:         tenant.ID,
		Status:           domain.TransferStatusOnRamping,
		Version:          3,
		SourceCurrency:   domain.CurrencyGBP,
		SourceAmount:     decimal.NewFromFloat(1000),
		StableCoin:       domain.CurrencyUSDT,
		StableAmount:     decimal.NewFromFloat(1250),
		Chain:            "tron",
		OnRampProviderID: "mock-onramp",
	}
	h.seedTransfer(transfer)

	// Override GetTransfer to return copies (avoid race on shared pointer)
	h.transfers.getFn = func(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error) {
		h.transfers.mu.Lock()
		defer h.transfers.mu.Unlock()
		t, ok := h.transfers.transfers[id]
		if !ok {
			return nil, domain.ErrTransferNotFound(id.String())
		}
		cp := *t
		return &cp, nil
	}

	// Use sync.Once to allow first call to succeed, second returns optimistic lock
	var once sync.Once
	h.transfers.transitionWithOutboxFn = func(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error {
		succeeded := false
		once.Do(func() {
			h.transfers.mu.Lock()
			tr := h.transfers.transfers[transferID]
			tr.Status = newStatus
			tr.Version++
			h.transfers.outboxEntries = append(h.transfers.outboxEntries, entries...)
			h.transfers.mu.Unlock()
			succeeded = true
		})
		if succeeded {
			return nil
		}
		return fmt.Errorf("settla-store: transfer %s: %w", transferID, ErrOptimisticLock)
	}

	var wg sync.WaitGroup
	var successCount, failCount int64
	var mu sync.Mutex

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			err := h.engine.HandleOnRampResult(context.Background(), tenant.ID, transfer.ID, domain.IntentResult{Success: true})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
			} else {
				failCount++
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("expected exactly 1 failure, got %d", failCount)
	}
}

// ---------------------------------------------------------------------------
// Quote expiry boundary test (Issue #28)
// ---------------------------------------------------------------------------

func TestCreateTransfer_QuoteExpiryBoundary(t *testing.T) {
	// Test at exact expiry — should fail
	t.Run("at_exact_expiry", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		tenant := activeTenant()

		quoteID := uuid.New()
		h.transfers.getQuoteFn = func(_ context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
			return &domain.Quote{
				ID:             quoteID,
				TenantID:       tenant.ID,
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   decimal.NewFromFloat(1000),
				DestCurrency:   domain.CurrencyNGN,
				DestAmount:     decimal.NewFromFloat(500000),
				StableAmount:   decimal.NewFromFloat(1250),
				FXRate:         decimal.NewFromFloat(1.25),
				Route: domain.RouteInfo{
					Chain:           "tron",
					StableCoin:      domain.CurrencyUSDT,
					OnRampProvider:  "mock-onramp",
					OffRampProvider: "mock-offramp",
				},
				Fees: domain.FeeBreakdown{
					OnRampFee:   decimal.NewFromFloat(2.50),
					OffRampFee:  decimal.NewFromFloat(2.50),
					NetworkFee:  decimal.NewFromFloat(0.10),
					TotalFeeUSD: decimal.NewFromFloat(5.10),
				},
				ExpiresAt: time.Now().UTC().Add(-time.Millisecond), // just past expiry
				CreatedAt: time.Now().UTC().Add(-5 * time.Minute),
			}, nil
		}

		req := validRequest()
		req.QuoteID = &quoteID

		_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
		if err == nil {
			t.Fatal("expected error for quote at exact expiry")
		}
		var domErr *domain.DomainError
		if !errors.As(err, &domErr) || domErr.Code() != domain.CodeQuoteExpired {
			t.Errorf("expected ErrQuoteExpired, got %v", err)
		}
	})

	// Test 1ms before expiry — should succeed
	t.Run("just_before_expiry", func(t *testing.T) {
		h := newTestHarness()
		ctx := context.Background()
		tenant := activeTenant()

		quoteID := uuid.New()
		h.transfers.getQuoteFn = func(_ context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
			return &domain.Quote{
				ID:             quoteID,
				TenantID:       tenant.ID,
				SourceCurrency: domain.CurrencyGBP,
				SourceAmount:   decimal.NewFromFloat(1000),
				DestCurrency:   domain.CurrencyNGN,
				DestAmount:     decimal.NewFromFloat(500000),
				StableAmount:   decimal.NewFromFloat(1250),
				FXRate:         decimal.NewFromFloat(1.25),
				Route: domain.RouteInfo{
					Chain:           "tron",
					StableCoin:      domain.CurrencyUSDT,
					OnRampProvider:  "mock-onramp",
					OffRampProvider: "mock-offramp",
				},
				Fees: domain.FeeBreakdown{
					OnRampFee:   decimal.NewFromFloat(2.50),
					OffRampFee:  decimal.NewFromFloat(2.50),
					NetworkFee:  decimal.NewFromFloat(0.10),
					TotalFeeUSD: decimal.NewFromFloat(5.10),
				},
				ExpiresAt: time.Now().UTC().Add(1 * time.Second), // still valid
				CreatedAt: time.Now().UTC().Add(-5 * time.Minute),
			}, nil
		}

		req := validRequest()
		req.QuoteID = &quoteID

		transfer, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if transfer.Status != domain.TransferStatusCreated {
			t.Errorf("expected status CREATED, got %s", transfer.Status)
		}
	})
}

// ---------------------------------------------------------------------------
// Idempotency key reuse test (Issue #29)
// ---------------------------------------------------------------------------

func TestCreateTransfer_IdempotencyKeyReuse(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	req.IdempotencyKey = "idem-reuse-test"

	// First call creates the transfer
	transfer1, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err != nil {
		t.Fatalf("first call: expected no error, got %v", err)
	}

	// Wire up the idempotency lookup to return the first transfer
	h.transfers.getByIdempotencyKeyFn = func(_ context.Context, tid uuid.UUID, key string) (*domain.Transfer, error) {
		if tid == tenant.ID && key == "idem-reuse-test" {
			return transfer1, nil
		}
		return nil, fmt.Errorf("not found")
	}

	// Second call with same key should return same transfer
	transfer2, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err != nil {
		t.Fatalf("second call: expected no error, got %v", err)
	}
	if transfer1.ID != transfer2.ID {
		t.Errorf("expected same transfer ID, got %s and %s", transfer1.ID, transfer2.ID)
	}
}

// ---------------------------------------------------------------------------
// HandleRefundResult transitions to REFUNDED (Issue #2 verification)
// ---------------------------------------------------------------------------

func TestCreateTransfer_FeesExceedSourceAmount(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	// Set up a router that returns fees greater than the source amount
	h.router.getQuoteFn = func(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error) {
		return &domain.Quote{
			ID:             uuid.New(),
			TenantID:       tenantID,
			SourceCurrency: req.SourceCurrency,
			SourceAmount:   req.SourceAmount,
			DestCurrency:   req.DestCurrency,
			DestAmount:     decimal.NewFromFloat(10),
			StableAmount:   decimal.NewFromFloat(10),
			FXRate:         decimal.NewFromFloat(1.25),
			Fees: domain.FeeBreakdown{
				OnRampFee:   decimal.NewFromFloat(600),
				OffRampFee:  decimal.NewFromFloat(600),
				NetworkFee:  decimal.NewFromFloat(10),
				TotalFeeUSD: decimal.NewFromFloat(1210),
			},
			Route: domain.RouteInfo{
				Chain:           "tron",
				StableCoin:      domain.CurrencyUSDT,
				OnRampProvider:  "mock-onramp",
				OffRampProvider: "mock-offramp",
			},
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
			CreatedAt: time.Now().UTC(),
		}, nil
	}

	req := validRequest()
	req.SourceAmount = decimal.NewFromFloat(1000) // fees (1210) > source (1000)

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error when total fees exceed source amount")
	}
	if !strings.Contains(err.Error(), "total fees") {
		t.Errorf("expected error about total fees, got: %v", err)
	}
}

func TestHandleRefundResult_TransitionsToRefunded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusRefunding,
		Version:        6,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleRefundResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// CRITICAL: Must transition to REFUNDED, not FAILED
	if transfer.Status != domain.TransferStatusRefunded {
		t.Errorf("expected status REFUNDED, got %s", transfer.Status)
	}

	// Verify webhook event type is EventRefundCompleted, not EventTransferFailed
	entries := h.transfers.getOutboxEntries()
	if !outboxHasEvent(entries, domain.EventRefundCompleted) {
		t.Error("expected outbox to contain EventRefundCompleted")
	}
	for _, e := range entries {
		if e.IsIntent && e.EventType == domain.IntentWebhookDeliver {
			var payload domain.WebhookDeliverPayload
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal webhook payload: %v", err)
			}
			if payload.EventType != domain.EventRefundCompleted {
				t.Errorf("expected webhook event type %s, got %s", domain.EventRefundCompleted, payload.EventType)
			}
		}
	}
}

func TestStateMachine_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from domain.TransferStatus
		to   domain.TransferStatus
	}{
		// CREATED can only go to FUNDED or FAILED
		{"CREATED→OFF_RAMPING", domain.TransferStatusCreated, domain.TransferStatusOffRamping},
		{"CREATED→COMPLETED", domain.TransferStatusCreated, domain.TransferStatusCompleted},
		{"CREATED→ON_RAMPING", domain.TransferStatusCreated, domain.TransferStatusOnRamping},
		{"CREATED→SETTLING", domain.TransferStatusCreated, domain.TransferStatusSettling},
		{"CREATED→REFUNDING", domain.TransferStatusCreated, domain.TransferStatusRefunding},
		{"CREATED→REFUNDED", domain.TransferStatusCreated, domain.TransferStatusRefunded},

		// FUNDED can only go to ON_RAMPING or REFUNDING
		{"FUNDED→FUNDED", domain.TransferStatusFunded, domain.TransferStatusFunded},
		{"FUNDED→COMPLETED", domain.TransferStatusFunded, domain.TransferStatusCompleted},
		{"FUNDED→SETTLING", domain.TransferStatusFunded, domain.TransferStatusSettling},
		{"FUNDED→OFF_RAMPING", domain.TransferStatusFunded, domain.TransferStatusOffRamping},
		{"FUNDED→FAILED", domain.TransferStatusFunded, domain.TransferStatusFailed},

		// ON_RAMPING can only go to SETTLING, REFUNDING, or FAILED
		{"ON_RAMPING→COMPLETED", domain.TransferStatusOnRamping, domain.TransferStatusCompleted},
		{"ON_RAMPING→FUNDED", domain.TransferStatusOnRamping, domain.TransferStatusFunded},
		{"ON_RAMPING→OFF_RAMPING", domain.TransferStatusOnRamping, domain.TransferStatusOffRamping},

		// SETTLING can only go to OFF_RAMPING or FAILED
		{"SETTLING→COMPLETED", domain.TransferStatusSettling, domain.TransferStatusCompleted},
		{"SETTLING→ON_RAMPING", domain.TransferStatusSettling, domain.TransferStatusOnRamping},
		{"SETTLING→FUNDED", domain.TransferStatusSettling, domain.TransferStatusFunded},

		// OFF_RAMPING can only go to COMPLETED or FAILED
		{"OFF_RAMPING→ON_RAMPING", domain.TransferStatusOffRamping, domain.TransferStatusOnRamping},
		{"OFF_RAMPING→SETTLING", domain.TransferStatusOffRamping, domain.TransferStatusSettling},
		{"OFF_RAMPING→FUNDED", domain.TransferStatusOffRamping, domain.TransferStatusFunded},

		// COMPLETED is terminal (no valid transitions defined)
		{"COMPLETED→ON_RAMPING", domain.TransferStatusCompleted, domain.TransferStatusOnRamping},
		{"COMPLETED→FAILED", domain.TransferStatusCompleted, domain.TransferStatusFailed},
		{"COMPLETED→CREATED", domain.TransferStatusCompleted, domain.TransferStatusCreated},

		// FAILED can only go to REFUNDING
		{"FAILED→ON_RAMPING", domain.TransferStatusFailed, domain.TransferStatusOnRamping},
		{"FAILED→COMPLETED", domain.TransferStatusFailed, domain.TransferStatusCompleted},
		{"FAILED→CREATED", domain.TransferStatusFailed, domain.TransferStatusCreated},
		{"FAILED→FUNDED", domain.TransferStatusFailed, domain.TransferStatusFunded},

		// REFUNDING can only go to REFUNDED
		{"REFUNDING→COMPLETED", domain.TransferStatusRefunding, domain.TransferStatusCompleted},
		{"REFUNDING→FAILED", domain.TransferStatusRefunding, domain.TransferStatusFailed},
		{"REFUNDING→CREATED", domain.TransferStatusRefunding, domain.TransferStatusCreated},

		// REFUNDED is terminal (no valid transitions defined)
		{"REFUNDED→CREATED", domain.TransferStatusRefunded, domain.TransferStatusCreated},
		{"REFUNDED→FAILED", domain.TransferStatusRefunded, domain.TransferStatusFailed},
		{"REFUNDED→REFUNDING", domain.TransferStatusRefunded, domain.TransferStatusRefunding},

		// Self-transitions should all be invalid
		{"CREATED→CREATED", domain.TransferStatusCreated, domain.TransferStatusCreated},
		{"COMPLETED→COMPLETED", domain.TransferStatusCompleted, domain.TransferStatusCompleted},
		{"FAILED→FAILED", domain.TransferStatusFailed, domain.TransferStatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transfer := &domain.Transfer{
				ID:       uuid.New(),
				TenantID: uuid.New(),
				Status:   tt.from,
				Version:  1,
			}

			originalStatus := transfer.Status
			originalVersion := transfer.Version

			event, err := transfer.TransitionTo(tt.to)
			if err == nil {
				t.Fatalf("expected error for invalid transition %s → %s, got nil", tt.from, tt.to)
			}
			if event != nil {
				t.Errorf("expected nil event for invalid transition, got %+v", event)
			}
			if transfer.Status != originalStatus {
				t.Errorf("status should remain %s after invalid transition, got %s", originalStatus, transfer.Status)
			}
			if transfer.Version != originalVersion {
				t.Errorf("version should remain %d after invalid transition, got %d", originalVersion, transfer.Version)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Query functions (previously 0% coverage)
// ---------------------------------------------------------------------------

func TestGetRoutingOptions_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	result, err := h.engine.GetRoutingOptions(ctx, tenant.ID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ProviderID == "" {
		t.Error("expected non-empty ProviderID")
	}
}

func TestGetRoutingOptions_SuspendedTenant(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	suspended := &domain.Tenant{
		ID:     uuid.New(),
		Status: domain.TenantStatusSuspended,
	}
	h.tenants.getFn = func(_ context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		if tenantID == suspended.ID {
			return suspended, nil
		}
		return nil, domain.ErrTenantNotFound(tenantID.String())
	}

	_, err := h.engine.GetRoutingOptions(ctx, suspended.ID, domain.QuoteRequest{})
	if err == nil {
		t.Fatal("expected error for suspended tenant")
	}
	var de *domain.DomainError
	if !errors.As(err, &de) || de.Code() != domain.CodeTenantSuspended {
		t.Errorf("expected TenantSuspended error, got: %v", err)
	}
}

func TestGetRoutingOptions_TenantNotFound(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	_, err := h.engine.GetRoutingOptions(ctx, uuid.New(), domain.QuoteRequest{})
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
}

func TestGetQuote_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	quote, err := h.engine.GetQuote(ctx, tenant.ID, domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote == nil {
		t.Fatal("expected non-nil quote")
	}
	if quote.TenantID != tenant.ID {
		t.Errorf("TenantID: got %s, want %s", quote.TenantID, tenant.ID)
	}
}

func TestGetQuote_SuspendedTenant(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	suspended := &domain.Tenant{ID: uuid.New(), Status: domain.TenantStatusSuspended}
	h.tenants.getFn = func(_ context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
		if tenantID == suspended.ID {
			return suspended, nil
		}
		return nil, domain.ErrTenantNotFound(tenantID.String())
	}

	_, err := h.engine.GetQuote(ctx, suspended.ID, domain.QuoteRequest{})
	if err == nil {
		t.Fatal("expected error for suspended tenant")
	}
}

func TestGetQuoteByID_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	quoteID := uuid.New()

	h.transfers.getQuoteFn = func(_ context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		return &domain.Quote{
			ID:        qid,
			TenantID:  tid,
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}, nil
	}

	quote, err := h.engine.GetQuoteByID(ctx, tenant.ID, quoteID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.ID != quoteID {
		t.Errorf("ID: got %s, want %s", quote.ID, quoteID)
	}
}

func TestGetQuoteByID_Expired(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	h.transfers.getQuoteFn = func(_ context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		return &domain.Quote{
			ID:        qid,
			TenantID:  tid,
			ExpiresAt: time.Now().UTC().Add(-1 * time.Minute), // expired
		}, nil
	}

	_, err := h.engine.GetQuoteByID(ctx, tenant.ID, uuid.New())
	if err == nil {
		t.Fatal("expected error for expired quote")
	}
	var de *domain.DomainError
	if !errors.As(err, &de) || de.Code() != domain.CodeQuoteExpired {
		t.Errorf("expected QuoteExpired error, got: %v", err)
	}
}

func TestGetTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCreated,
	}
	h.seedTransfer(transfer)

	got, err := h.engine.GetTransfer(ctx, tenant.ID, transfer.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != transfer.ID {
		t.Errorf("ID: got %s, want %s", got.ID, transfer.ID)
	}
}

func TestGetTransfer_NotFound(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	_, err := h.engine.GetTransfer(ctx, activeTenant().ID, uuid.New())
	if err == nil {
		t.Fatal("expected error for unknown transfer")
	}
}

func TestGetTransferByExternalRef_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	transfer := &domain.Transfer{
		ID:          uuid.New(),
		TenantID:    tenant.ID,
		ExternalRef: "ext-ref-123",
		Status:      domain.TransferStatusCreated,
	}
	h.seedTransfer(transfer)

	got, err := h.engine.GetTransferByExternalRef(ctx, tenant.ID, "ext-ref-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != transfer.ID {
		t.Errorf("ID: got %s, want %s", got.ID, transfer.ID)
	}
}

func TestListTransfers_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfers, err := h.engine.ListTransfers(ctx, tenant.ID, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mock returns nil slice, which is acceptable.
	_ = transfers
}

func TestListTransfersFiltered_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfers, err := h.engine.ListTransfersFiltered(ctx, tenant.ID, "COMPLETED", "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = transfers
}

func TestGetTransferEvents_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	events, err := h.engine.GetTransferEvents(ctx, tenant.ID, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = events
}

// ---------------------------------------------------------------------------
// HandleRefundResult failure path
// ---------------------------------------------------------------------------

func TestHandleRefundResult_Failure_LogsAndReturnsNil(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusRefunding,
		Version:        6,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	// Failure result — should log warning but return nil (awaiting recovery).
	err := h.engine.HandleRefundResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{
		Success: false,
		Error:   "refund provider error",
	})
	if err != nil {
		t.Fatalf("HandleRefundResult failure should return nil, got: %v", err)
	}

	// Transfer should remain in REFUNDING state.
	if transfer.Status != domain.TransferStatusRefunding {
		t.Errorf("expected status to remain REFUNDING, got %s", transfer.Status)
	}

	// No outbox entries should be written for failure.
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries for refund failure, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// InitiateRefund from FAILED state
// ---------------------------------------------------------------------------

func TestInitiateRefund_FromFailed_WithStableAmount(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusFailed,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromFloat(1250),
		Chain:          "tron",
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(2.50),
			OffRampFee:  decimal.NewFromFloat(2.50),
			NetworkFee:  decimal.NewFromFloat(0.10),
			TotalFeeUSD: decimal.NewFromFloat(5.10),
		},
	}
	h.seedTransfer(transfer)

	err := h.engine.InitiateRefund(ctx, tenant.ID, transfer.ID)
	if err != nil {
		t.Fatalf("InitiateRefund from FAILED: %v", err)
	}

	entries := h.transfers.getOutboxEntries()

	// Should have ledger reverse, treasury release, and refund initiated event.
	if !outboxHasIntent(entries, domain.IntentLedgerReverse) {
		t.Error("expected IntentLedgerReverse")
	}
	if !outboxHasIntent(entries, domain.IntentTreasuryRelease) {
		t.Error("expected IntentTreasuryRelease")
	}
	if !outboxHasEvent(entries, domain.EventRefundInitiated) {
		t.Error("expected EventRefundInitiated")
	}
}

func TestInitiateRefund_InvalidState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	// COMPLETED transfers cannot be refunded.
	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCompleted,
		Version:  6,
	}
	h.seedTransfer(transfer)

	err := h.engine.InitiateRefund(ctx, tenant.ID, transfer.ID)
	if err == nil {
		t.Fatal("expected error for refund from COMPLETED state")
	}
}

// ---------------------------------------------------------------------------
// ProcessTransfer error propagation
// ---------------------------------------------------------------------------

func TestProcessTransfer_FundTransferFailure(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	// Create a transfer in CREATED state but make TransitionWithOutbox fail.
	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	h.transfers.transitionWithOutboxFn = func(_ context.Context, _ uuid.UUID, _ domain.TransferStatus, _ int64, _ []domain.OutboxEntry) error {
		return fmt.Errorf("simulated DB failure")
	}

	err := h.engine.ProcessTransfer(ctx, tenant.ID, transfer.ID)
	if err == nil {
		t.Fatal("expected error when FundTransfer fails")
	}
	if !strings.Contains(err.Error(), "simulated DB failure") {
		t.Errorf("expected DB failure in error chain, got: %v", err)
	}
}

func TestProcessTransfer_TransferNotFound(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	err := h.engine.ProcessTransfer(ctx, tenant.ID, uuid.New())
	if err == nil {
		t.Fatal("expected error for non-existent transfer")
	}
}

// ---------------------------------------------------------------------------
// NATS replay detection (advanced past states)
// ---------------------------------------------------------------------------

func TestHandleOnRampResult_NATSReplay_SkipsWhenAdvancedPast(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	// Transfer has already advanced to SETTLING (past ON_RAMPING).
	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusSettling,
		Version:        4,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	// Should silently skip (NATS replay).
	err := h.engine.HandleOnRampResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("expected nil for NATS replay, got: %v", err)
	}

	// No outbox entries written.
	entries := h.transfers.getOutboxEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 outbox entries for NATS replay, got %d", len(entries))
	}
}

func TestHandleSettlementResult_NATSReplay_SkipsWhenAdvancedPast(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOffRamping,
		Version:        5,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}
	h.seedTransfer(transfer)

	err := h.engine.HandleSettlementResult(ctx, tenant.ID, transfer.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("expected nil for NATS replay, got: %v", err)
	}
}
