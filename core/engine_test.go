package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	createFn              func(ctx context.Context, t *domain.Transfer) error
	getFn                 func(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error)
	getByIdempotencyKeyFn func(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error)
	updateFn              func(ctx context.Context, t *domain.Transfer) error
	createEventFn         func(ctx context.Context, e *domain.TransferEvent) error
	getEventsFn           func(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error)
	getDailyVolumeFn      func(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
	getQuoteFn            func(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error)
}

func (m *mockTransferStore) CreateTransfer(ctx context.Context, t *domain.Transfer) error {
	if m.createFn != nil {
		return m.createFn(ctx, t)
	}
	return nil
}
func (m *mockTransferStore) GetTransfer(ctx context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error) {
	if m.getFn != nil {
		return m.getFn(ctx, tenantID, id)
	}
	return nil, domain.ErrTransferNotFound(id.String())
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

type mockLedger struct {
	postFn    func(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error)
	reverseFn func(ctx context.Context, entryID uuid.UUID, reason string) (*domain.JournalEntry, error)
	entries   []domain.JournalEntry // records all posted entries for assertions
}

func (m *mockLedger) PostEntries(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error) {
	m.entries = append(m.entries, entry)
	if m.postFn != nil {
		return m.postFn(ctx, entry)
	}
	return &entry, nil
}
func (m *mockLedger) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (m *mockLedger) GetEntries(ctx context.Context, accountCode string, from, to time.Time, limit, offset int) ([]domain.EntryLine, error) {
	return nil, nil
}
func (m *mockLedger) ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*domain.JournalEntry, error) {
	if m.reverseFn != nil {
		return m.reverseFn(ctx, entryID, reason)
	}
	return &domain.JournalEntry{ID: uuid.New()}, nil
}

type mockTreasury struct {
	reserveFn func(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, ref uuid.UUID) error
	releaseFn func(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, ref uuid.UUID) error
	reserved  int // count of Reserve calls
	released  int // count of Release calls
}

func (m *mockTreasury) Reserve(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, ref uuid.UUID) error {
	m.reserved++
	if m.reserveFn != nil {
		return m.reserveFn(ctx, tenantID, currency, location, amount, ref)
	}
	return nil
}
func (m *mockTreasury) Release(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, ref uuid.UUID) error {
	m.released++
	if m.releaseFn != nil {
		return m.releaseFn(ctx, tenantID, currency, location, amount, ref)
	}
	return nil
}
func (m *mockTreasury) GetPositions(ctx context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	return nil, nil
}
func (m *mockTreasury) GetPosition(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string) (*domain.Position, error) {
	return nil, nil
}
func (m *mockTreasury) GetLiquidityReport(ctx context.Context, tenantID uuid.UUID) (*domain.LiquidityReport, error) {
	return nil, nil
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
		DestAmount:     req.SourceAmount.Mul(decimal.NewFromFloat(1.25)), // mock rate
		FXRate:         decimal.NewFromFloat(1.25),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(2.50),
			OffRampFee:  decimal.NewFromFloat(2.50),
			NetworkFee:  decimal.NewFromFloat(0.10),
			TotalFeeUSD: decimal.NewFromFloat(5.10),
		},
		Route: domain.RouteInfo{
			Chain:          "tron",
			StableCoin:     domain.CurrencyUSDT,
			OnRampProvider: "mock-onramp",
			OffRampProvider: "mock-offramp",
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		CreatedAt: time.Now().UTC(),
	}, nil
}

type mockProviderRegistry struct {
	onRamp     domain.OnRampProvider
	offRamp    domain.OffRampProvider
	blockchain domain.BlockchainClient
}

func (m *mockProviderRegistry) GetOnRampProvider(id string) domain.OnRampProvider  { return m.onRamp }
func (m *mockProviderRegistry) GetOffRampProvider(id string) domain.OffRampProvider { return m.offRamp }
func (m *mockProviderRegistry) GetBlockchainClient(chain string) domain.BlockchainClient {
	return m.blockchain
}

type mockOnRampProvider struct {
	executeFn func(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error)
}

func (m *mockOnRampProvider) ID() string                          { return "mock-onramp" }
func (m *mockOnRampProvider) SupportedPairs() []domain.CurrencyPair { return nil }
func (m *mockOnRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (m *mockOnRampProvider) Execute(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, req)
	}
	return &domain.ProviderTx{ID: "onramp-tx-1", Status: "completed"}, nil
}
func (m *mockOnRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	return &domain.ProviderTx{ID: txID, Status: "completed"}, nil
}

type mockOffRampProvider struct {
	executeFn func(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error)
}

func (m *mockOffRampProvider) ID() string                          { return "mock-offramp" }
func (m *mockOffRampProvider) SupportedPairs() []domain.CurrencyPair { return nil }
func (m *mockOffRampProvider) GetQuote(ctx context.Context, req domain.QuoteRequest) (*domain.ProviderQuote, error) {
	return nil, nil
}
func (m *mockOffRampProvider) Execute(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, req)
	}
	return &domain.ProviderTx{ID: "offramp-tx-1", Status: "completed"}, nil
}
func (m *mockOffRampProvider) GetStatus(ctx context.Context, txID string) (*domain.ProviderTx, error) {
	return &domain.ProviderTx{ID: txID, Status: "completed"}, nil
}

type mockBlockchainClient struct {
	sendFn func(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error)
}

func (m *mockBlockchainClient) Chain() string { return "tron" }
func (m *mockBlockchainClient) GetBalance(ctx context.Context, address, token string) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (m *mockBlockchainClient) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	return decimal.NewFromFloat(0.50), nil
}
func (m *mockBlockchainClient) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	if m.sendFn != nil {
		return m.sendFn(ctx, req)
	}
	return &domain.ChainTx{Hash: "0xabc123", Status: "confirmed", Confirmations: 20, Fee: decimal.NewFromFloat(0.50)}, nil
}
func (m *mockBlockchainClient) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	return &domain.ChainTx{Hash: hash, Status: "confirmed"}, nil
}
func (m *mockBlockchainClient) SubscribeTransactions(ctx context.Context, address string, ch chan<- domain.ChainTx) error {
	return nil
}

type mockPublisher struct {
	events []domain.Event
}

func (m *mockPublisher) Publish(ctx context.Context, event domain.Event) error {
	m.events = append(m.events, event)
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func activeTenant() *domain.Tenant {
	return &domain.Tenant{
		ID:              uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		Name:            "Lemfi",
		Slug:            "lemfi",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		SettlementModel: domain.SettlementModelPrefunded,
		DailyLimitUSD:   decimal.NewFromFloat(1000000),
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
	engine     *Engine
	transfers  *mockTransferStore
	tenants    *mockTenantStore
	ledger     *mockLedger
	treasury   *mockTreasury
	router     *mockRouter
	providers  *mockProviderRegistry
	publisher  *mockPublisher
}

func newTestHarness() *testHarness {
	tenant := activeTenant()
	transfers := &mockTransferStore{}
	tenants := &mockTenantStore{
		getFn: func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
			if tenantID == tenant.ID {
				return tenant, nil
			}
			return nil, domain.ErrTenantNotFound(tenantID.String())
		},
	}
	ledger := &mockLedger{}
	treasury := &mockTreasury{}
	router := &mockRouter{}
	onRamp := &mockOnRampProvider{}
	offRamp := &mockOffRampProvider{}
	blockchain := &mockBlockchainClient{}
	providers := &mockProviderRegistry{
		onRamp:     onRamp,
		offRamp:    offRamp,
		blockchain: blockchain,
	}
	publisher := &mockPublisher{}

	engine := NewEngine(transfers, tenants, ledger, treasury, router, providers, publisher, testLogger(), nil)

	return &testHarness{
		engine:    engine,
		transfers: transfers,
		tenants:   tenants,
		ledger:    ledger,
		treasury:  treasury,
		router:    router,
		providers: providers,
		publisher: publisher,
	}
}

// ---------------------------------------------------------------------------
// Happy path tests
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

	// Verify event was published
	if len(h.publisher.events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(h.publisher.events))
	}
	if h.publisher.events[0].Type != domain.EventTransferCreated {
		t.Errorf("expected event type %s, got %s", domain.EventTransferCreated, h.publisher.events[0].Type)
	}
	if h.publisher.events[0].TenantID != tenant.ID {
		t.Errorf("expected event tenant ID %s, got %s", tenant.ID, h.publisher.events[0].TenantID)
	}
}

func TestFundTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transferID := uuid.New()
	transfer := &domain.Transfer{
		ID:             transferID,
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.FundTransfer(ctx, transferID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify treasury Reserve was called
	if h.treasury.reserved != 1 {
		t.Errorf("expected 1 treasury reserve call, got %d", h.treasury.reserved)
	}

	// Verify ledger entries posted
	if len(h.ledger.entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(h.ledger.entries))
	}
	entry := h.ledger.entries[0]
	if len(entry.Lines) != 2 {
		t.Fatalf("expected 2 entry lines, got %d", len(entry.Lines))
	}
	// DR clearing, CR customer:pending
	if entry.Lines[0].EntryType != domain.EntryTypeDebit {
		t.Errorf("expected first line to be DEBIT, got %s", entry.Lines[0].EntryType)
	}
	if !strings.Contains(entry.Lines[0].AccountCode, "assets:bank:gbp:clearing") {
		t.Errorf("expected clearing account code, got %s", entry.Lines[0].AccountCode)
	}
	if entry.Lines[1].EntryType != domain.EntryTypeCredit {
		t.Errorf("expected second line to be CREDIT, got %s", entry.Lines[1].EntryType)
	}
	if !strings.Contains(entry.Lines[1].AccountCode, "liabilities:customer:pending") {
		t.Errorf("expected customer pending account code, got %s", entry.Lines[1].AccountCode)
	}

	// Verify status transitioned to FUNDED
	if transfer.Status != domain.TransferStatusFunded {
		t.Errorf("expected status FUNDED, got %s", transfer.Status)
	}

	// Verify event published
	found := false
	for _, e := range h.publisher.events {
		if e.Type == domain.EventTransferFunded {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected transfer.funded event to be published")
	}
}

func TestProcessTransfer_FullPipeline(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transferID := uuid.New()
	transfer := &domain.Transfer{
		ID:             transferID,
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500000),
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromFloat(1250),
		Chain:          "tron",
		FXRate:         decimal.NewFromFloat(1.25),
		Fees: domain.FeeBreakdown{
			OnRampFee:   decimal.NewFromFloat(4),
			OffRampFee:  decimal.NewFromFloat(4),
			NetworkFee:  decimal.NewFromFloat(0.50),
			TotalFeeUSD: decimal.NewFromFloat(8.50),
		},
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.ProcessTransfer(ctx, transferID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify final status
	if transfer.Status != domain.TransferStatusCompleted {
		t.Errorf("expected status COMPLETED, got %s", transfer.Status)
	}

	// Verify treasury: reserve on fund, release on complete
	if h.treasury.reserved != 1 {
		t.Errorf("expected 1 reserve call, got %d", h.treasury.reserved)
	}
	if h.treasury.released != 1 {
		t.Errorf("expected 1 release call, got %d", h.treasury.released)
	}

	// Verify ledger entries posted (fund + onramp + settle + offramp + complete = 5)
	if len(h.ledger.entries) != 5 {
		t.Errorf("expected 5 ledger entries, got %d", len(h.ledger.entries))
	}

	// Verify events published (funded + onramp_initiated + onramp_completed + settlement_completed + offramp_initiated + transfer_completed)
	if len(h.publisher.events) < 4 {
		t.Errorf("expected at least 4 published events, got %d", len(h.publisher.events))
	}

	// Verify tenant account codes use tenant slug
	for _, entry := range h.ledger.entries {
		for _, line := range entry.Lines {
			if strings.HasPrefix(line.AccountCode, "tenant:") && !strings.HasPrefix(line.AccountCode, "tenant:lemfi:") {
				t.Errorf("expected tenant account code to use slug 'lemfi', got %s", line.AccountCode)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tenant validation tests
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

func TestCreateTransfer_DailyLimitExceeded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()
	tenant.DailyLimitUSD = decimal.NewFromFloat(500) // Low limit

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
	tenant.PerTransferLimit = decimal.NewFromFloat(500) // Low limit

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

func TestCreateTransfer_QuoteFromDifferentTenant(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	quoteID := uuid.New()
	otherTenantID := uuid.New()
	h.transfers.getQuoteFn = func(ctx context.Context, tid, qid uuid.UUID) (*domain.Quote, error) {
		return &domain.Quote{
			ID:        quoteID,
			TenantID:  otherTenantID, // Different tenant
			ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}, nil
	}

	req := validRequest()
	req.QuoteID = &quoteID

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for quote from different tenant")
	}
	if !strings.Contains(err.Error(), "different tenant") {
		t.Errorf("expected 'different tenant' in error, got %v", err)
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
			ExpiresAt: time.Now().UTC().Add(-5 * time.Minute), // Expired
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
// Idempotency tests
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
}

func TestCreateTransfer_DifferentTenantSameKey(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	tenantA := activeTenant()
	tenantB := &domain.Tenant{
		ID:              uuid.MustParse("b0000000-0000-0000-0000-000000000002"),
		Name:            "Fincra",
		Slug:            "fincra",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		DailyLimitUSD:   decimal.NewFromFloat(1000000),
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

	// Idempotency key is tenant-scoped, so same key for different tenants creates different transfers
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
// Failure handling tests
// ---------------------------------------------------------------------------

func TestFailTransfer_Success(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusSettling, // Valid source for FAILED
		Version:  3,
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.FailTransfer(ctx, transfer.ID, "provider timeout", "TIMEOUT")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if transfer.Status != domain.TransferStatusFailed {
		t.Errorf("expected status FAILED, got %s", transfer.Status)
	}
	if transfer.FailureReason != "provider timeout" {
		t.Errorf("expected failure reason 'provider timeout', got %s", transfer.FailureReason)
	}

	// Verify event published
	found := false
	for _, e := range h.publisher.events {
		if e.Type == domain.EventTransferFailed {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected transfer.failed event")
	}
}

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

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.InitiateRefund(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if transfer.Status != domain.TransferStatusRefunded {
		t.Errorf("expected status REFUNDED, got %s", transfer.Status)
	}

	// Verify treasury release was called
	if h.treasury.released != 1 {
		t.Errorf("expected 1 release call, got %d", h.treasury.released)
	}

	// Verify refund.completed event
	found := false
	for _, e := range h.publisher.events {
		if e.Type == domain.EventRefundCompleted {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected refund.completed event")
	}
}

func TestOnRampProviderFailure_TransferStaysFunded(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusFunded,
		Version:        2,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	// Make on-ramp fail
	h.providers.onRamp = &mockOnRampProvider{
		executeFn: func(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
			return nil, errors.New("provider unavailable")
		},
	}

	err := h.engine.InitiateOnRamp(ctx, transfer.ID)
	if err == nil {
		t.Fatal("expected error from failed on-ramp")
	}
	// Transfer should still be in FUNDED state (not transitioned)
	if transfer.Status != domain.TransferStatusFunded {
		t.Errorf("expected status to remain FUNDED, got %s", transfer.Status)
	}
}

func TestOffRampFailure_TransferFails(t *testing.T) {
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
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500000),
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromFloat(1250),
		Chain:          "tron",
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	// Make off-ramp fail
	h.providers.offRamp = &mockOffRampProvider{
		executeFn: func(ctx context.Context, req domain.OffRampRequest) (*domain.ProviderTx, error) {
			return nil, errors.New("payout failed")
		},
	}

	err := h.engine.InitiateOffRamp(ctx, transfer.ID)
	if err == nil {
		t.Fatal("expected error from failed off-ramp")
	}
	// Transfer should stay in SETTLING (not transitioned since error occurred before transition)
	if transfer.Status != domain.TransferStatusSettling {
		t.Errorf("expected status to remain SETTLING, got %s", transfer.Status)
	}
}

// ---------------------------------------------------------------------------
// State machine tests
// ---------------------------------------------------------------------------

func TestFundTransfer_InvalidState(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Status:   domain.TransferStatusCompleted, // Cannot fund a completed transfer
		Version:  5,
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.FundTransfer(ctx, transfer.ID)
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
		Status:   domain.TransferStatusCreated, // Cannot complete from CREATED
		Version:  1,
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.CompleteTransfer(ctx, transfer.ID)
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
		Status:   domain.TransferStatusCompleted, // Cannot fail a completed transfer
		Version:  6,
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.FailTransfer(ctx, transfer.ID, "test", "TEST")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Provider timeout test
// ---------------------------------------------------------------------------

func TestInitiateOnRamp_Timeout(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusFunded,
		Version:        2,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		StableCoin:     domain.CurrencyUSDT,
		Chain:          "tron",
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	// Simulate a provider that respects context cancellation
	h.providers.onRamp = &mockOnRampProvider{
		executeFn: func(ctx context.Context, req domain.OnRampRequest) (*domain.ProviderTx, error) {
			// Check if context is already cancelled (which happens when parent ctx is cancelled)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, context.DeadlineExceeded
			}
		},
	}

	// Use an already-cancelled context to simulate timeout
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := h.engine.InitiateOnRamp(cancelledCtx, transfer.ID)
	if err == nil {
		t.Fatal("expected error from timeout")
	}
	// Transfer should remain in FUNDED
	if transfer.Status != domain.TransferStatusFunded {
		t.Errorf("expected status to remain FUNDED, got %s", transfer.Status)
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
	if !strings.Contains(err.Error(), "unsupported currency") {
		t.Errorf("expected 'unsupported currency' in error, got %v", err)
	}
}

func TestCreateTransfer_MissingRecipient(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	req.Recipient = domain.Recipient{} // Missing required fields

	_, err := h.engine.CreateTransfer(ctx, tenant.ID, req)
	if err == nil {
		t.Fatal("expected error for missing recipient")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("expected 'recipient' in error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Ledger entry verification tests
// ---------------------------------------------------------------------------

func TestSettleOnChain_LedgerEntries(t *testing.T) {
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
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromFloat(1250),
		Chain:          "tron",
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.SettleOnChain(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(h.ledger.entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(h.ledger.entries))
	}

	entry := h.ledger.entries[0]
	if len(entry.Lines) != 3 {
		t.Fatalf("expected 3 entry lines, got %d", len(entry.Lines))
	}

	// Verify settlement in transit debit
	if !strings.Contains(entry.Lines[0].AccountCode, "assets:settlement:in_transit") {
		t.Errorf("expected settlement:in_transit account, got %s", entry.Lines[0].AccountCode)
	}
	// Verify gas fee debit
	if !strings.Contains(entry.Lines[1].AccountCode, "expenses:network:tron:gas") {
		t.Errorf("expected network gas account, got %s", entry.Lines[1].AccountCode)
	}
	// Verify crypto credit
	if !strings.Contains(entry.Lines[2].AccountCode, "assets:crypto:usdt:tron") {
		t.Errorf("expected crypto asset account, got %s", entry.Lines[2].AccountCode)
	}
}

func TestCompleteTransfer_LedgerEntries(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusOffRamping,
		Version:        6,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromFloat(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromFloat(500000),
		Fees: domain.FeeBreakdown{
			TotalFeeUSD: decimal.NewFromFloat(8.50),
		},
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	err := h.engine.CompleteTransfer(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(h.ledger.entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(h.ledger.entries))
	}

	entry := h.ledger.entries[0]
	// Verify closing entries: DR customer pending, CR recipient payable, CR fee revenue
	if len(entry.Lines) != 3 {
		t.Fatalf("expected 3 entry lines, got %d", len(entry.Lines))
	}
	if !strings.Contains(entry.Lines[0].AccountCode, "liabilities:customer:pending") {
		t.Errorf("expected customer pending account, got %s", entry.Lines[0].AccountCode)
	}
	if !strings.Contains(entry.Lines[1].AccountCode, "liabilities:payable:recipient") {
		t.Errorf("expected recipient payable account, got %s", entry.Lines[1].AccountCode)
	}
	if !strings.Contains(entry.Lines[2].AccountCode, "revenue:fees:settlement") {
		t.Errorf("expected fee revenue account, got %s", entry.Lines[2].AccountCode)
	}
}

// ---------------------------------------------------------------------------
// Blockchain timeout test
// ---------------------------------------------------------------------------

func TestSettleOnChain_BlockchainTimeout(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	tenant := activeTenant()

	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenant.ID,
		Status:         domain.TransferStatusSettling,
		Version:        4,
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromFloat(1250),
		Chain:          "tron",
	}

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	h.providers.blockchain = &mockBlockchainClient{
		sendFn: func(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
			return nil, errors.New("blockchain timeout")
		},
	}

	err := h.engine.SettleOnChain(ctx, transfer.ID)
	if err == nil {
		t.Fatal("expected error from blockchain timeout")
	}
	// Transfer should remain in SETTLING
	if transfer.Status != domain.TransferStatusSettling {
		t.Errorf("expected status to remain SETTLING, got %s", transfer.Status)
	}
}

// ---------------------------------------------------------------------------
// Optimistic lock conflict test
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

	h.transfers.getFn = func(ctx context.Context, tid, id uuid.UUID) (*domain.Transfer, error) {
		return transfer, nil
	}

	// Simulate optimistic lock failure
	h.transfers.updateFn = func(ctx context.Context, t *domain.Transfer) error {
		return domain.ErrOptimisticLock("transfer", t.ID.String())
	}

	err := h.engine.FundTransfer(ctx, transfer.ID)
	if err == nil {
		t.Fatal("expected error from optimistic lock conflict")
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) || domErr.Code() != domain.CodeOptimisticLock {
		t.Errorf("expected ErrOptimisticLock, got %v", err)
	}
}
