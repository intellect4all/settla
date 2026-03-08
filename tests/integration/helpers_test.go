//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/treasury"
)

// ─── Demo Tenant IDs ────────────────────────────────────────────────────────

var (
	LemfiTenantID  = uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	FincraTenantID = uuid.MustParse("b0000000-0000-0000-0000-000000000002")
)

// ─── In-Memory Transfer Store ───────────────────────────────────────────────

type memTransferStore struct {
	mu         sync.RWMutex
	transfers  map[uuid.UUID]*domain.Transfer
	idempotent map[string]*domain.Transfer // key: "tenantID:idempotencyKey"
	events     map[uuid.UUID][]domain.TransferEvent
	quotes     map[uuid.UUID]*domain.Quote
}

var _ core.TransferStore = (*memTransferStore)(nil)

func newMemTransferStore() *memTransferStore {
	return &memTransferStore{
		transfers:  make(map[uuid.UUID]*domain.Transfer),
		idempotent: make(map[string]*domain.Transfer),
		events:     make(map[uuid.UUID][]domain.TransferEvent),
		quotes:     make(map[uuid.UUID]*domain.Quote),
	}
}

func (s *memTransferStore) CreateTransfer(ctx context.Context, t *domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	t.Version = 1
	s.transfers[t.ID] = t
	if t.IdempotencyKey != "" {
		key := fmt.Sprintf("%s:%s", t.TenantID, t.IdempotencyKey)
		s.idempotent[key] = t
	}
	return nil
}

func (s *memTransferStore) GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.transfers[transferID]
	if !ok {
		return nil, domain.ErrTransferNotFound(transferID.String())
	}
	// uuid.Nil sentinel: skip tenant check (used by loadTransfer in engine)
	if tenantID != uuid.Nil && t.TenantID != tenantID {
		return nil, domain.ErrTransferNotFound(transferID.String())
	}
	return t, nil
}

func (s *memTransferStore) GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ikey := fmt.Sprintf("%s:%s", tenantID, key)
	t, ok := s.idempotent[ikey]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return t, nil
}

func (s *memTransferStore) UpdateTransfer(ctx context.Context, t *domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transfers[t.ID] = t
	return nil
}

func (s *memTransferStore) CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	s.events[event.TransferID] = append(s.events[event.TransferID], *event)
	return nil
}

func (s *memTransferStore) GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.events[transferID], nil
}

func (s *memTransferStore) GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	startOfDay := date.Truncate(24 * time.Hour)
	endOfDay := startOfDay.Add(24 * time.Hour)
	total := decimal.Zero
	for _, t := range s.transfers {
		if t.TenantID == tenantID && !t.CreatedAt.Before(startOfDay) && t.CreatedAt.Before(endOfDay) {
			total = total.Add(t.SourceAmount)
		}
	}
	return total, nil
}

func (s *memTransferStore) CreateQuote(ctx context.Context, quote *domain.Quote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if quote.ID == uuid.Nil {
		quote.ID = uuid.New()
	}
	s.quotes[quote.ID] = quote
	return nil
}

func (s *memTransferStore) GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.quotes[quoteID]
	if !ok {
		return nil, fmt.Errorf("quote not found")
	}
	if q.TenantID != tenantID {
		return nil, fmt.Errorf("quote not found")
	}
	return q, nil
}

func (s *memTransferStore) ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []domain.Transfer
	for _, t := range s.transfers {
		if t.TenantID == tenantID {
			result = append(result, *t)
		}
	}
	// Simple pagination
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (s *memTransferStore) addQuote(q *domain.Quote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes[q.ID] = q
}

func (s *memTransferStore) allTransfers() []*domain.Transfer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*domain.Transfer
	for _, t := range s.transfers {
		result = append(result, t)
	}
	return result
}

// ─── In-Memory Tenant Store ─────────────────────────────────────────────────

type memTenantStore struct {
	mu      sync.RWMutex
	tenants map[uuid.UUID]*domain.Tenant
	slugs   map[string]*domain.Tenant
}

var _ core.TenantStore = (*memTenantStore)(nil)

func newMemTenantStore() *memTenantStore {
	return &memTenantStore{
		tenants: make(map[uuid.UUID]*domain.Tenant),
		slugs:   make(map[string]*domain.Tenant),
	}
}

func (s *memTenantStore) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return nil, domain.ErrTenantNotFound(tenantID.String())
	}
	return t, nil
}

func (s *memTenantStore) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.slugs[slug]
	if !ok {
		return nil, domain.ErrTenantNotFound(slug)
	}
	return t, nil
}

func (s *memTenantStore) addTenant(t *domain.Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.ID] = t
	s.slugs[t.Slug] = t
}

// ─── In-Memory Treasury Store ───────────────────────────────────────────────

type memTreasuryStore struct {
	mu        sync.RWMutex
	positions map[uuid.UUID]domain.Position
}

var _ treasury.Store = (*memTreasuryStore)(nil)

func newMemTreasuryStore() *memTreasuryStore {
	return &memTreasuryStore{
		positions: make(map[uuid.UUID]domain.Position),
	}
}

func (s *memTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []domain.Position
	for _, p := range s.positions {
		result = append(result, p)
	}
	return result, nil
}

func (s *memTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.positions[id]; ok {
		p.Balance = balance
		p.Locked = locked
		p.UpdatedAt = time.Now().UTC()
		s.positions[id] = p
	}
	return nil
}

func (s *memTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	return nil // no-op for integration tests
}

func (s *memTreasuryStore) addPosition(p domain.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions[p.ID] = p
}

// ─── Mock TigerBeetle Client ────────────────────────────────────────────────

// mockTBClient implements ledger.TBClient for integration tests, tracking all
// writes so tests can verify TigerBeetle receives ledger entries.
type mockTBClient struct {
	mu        sync.Mutex
	accounts  map[ledger.ID128]ledger.TBAccount
	transfers map[ledger.ID128]ledger.TBTransfer

	createAccountsCalls  int
	createTransfersCalls int
}

var _ ledger.TBClient = (*mockTBClient)(nil)

func newMockTBClient() *mockTBClient {
	return &mockTBClient{
		accounts:  make(map[ledger.ID128]ledger.TBAccount),
		transfers: make(map[ledger.ID128]ledger.TBTransfer),
	}
}

func (m *mockTBClient) CreateAccounts(accounts []ledger.TBAccount) ([]ledger.TBCreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createAccountsCalls++

	var results []ledger.TBCreateResult
	for i, acc := range accounts {
		if _, exists := m.accounts[acc.ID]; exists {
			results = append(results, ledger.TBCreateResult{Index: uint32(i), Result: ledger.TBResultExists})
		} else {
			m.accounts[acc.ID] = acc
		}
	}
	return results, nil
}

func (m *mockTBClient) CreateTransfers(transfers []ledger.TBTransfer) ([]ledger.TBCreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createTransfersCalls++

	var results []ledger.TBCreateResult
	for i, t := range transfers {
		if _, exists := m.transfers[t.ID]; exists {
			results = append(results, ledger.TBCreateResult{Index: uint32(i), Result: ledger.TBResultExists})
			continue
		}
		m.transfers[t.ID] = t

		// Update account balances.
		debit := m.accounts[t.DebitAccountID]
		debit.DebitsPosted += t.Amount
		m.accounts[t.DebitAccountID] = debit

		credit := m.accounts[t.CreditAccountID]
		credit.CreditsPosted += t.Amount
		m.accounts[t.CreditAccountID] = credit
	}
	return results, nil
}

func (m *mockTBClient) LookupAccounts(ids []ledger.ID128) ([]ledger.TBAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var found []ledger.TBAccount
	for _, id := range ids {
		if acc, ok := m.accounts[id]; ok {
			found = append(found, acc)
		}
	}
	return found, nil
}

func (m *mockTBClient) Close() {}

// transferCount returns the total number of TB transfers recorded.
func (m *mockTBClient) transferCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.transfers)
}

// accountCount returns the total number of TB accounts created.
func (m *mockTBClient) accountCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.accounts)
}

// getBalance returns the balance (credits - debits) for an account code.
func (m *mockTBClient) getBalance(accountCode string) decimal.Decimal {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ledger.AccountIDFromCode(accountCode)
	acc, ok := m.accounts[id]
	if !ok {
		return decimal.Zero
	}
	return ledger.TBAmountToDecimal(acc.CreditsPosted).Sub(ledger.TBAmountToDecimal(acc.DebitsPosted))
}

// ─── Event Collector ────────────────────────────────────────────────────────

type eventCollector struct {
	mu     sync.Mutex
	events []domain.Event
}

var _ domain.EventPublisher = (*eventCollector)(nil)

func newEventCollector() *eventCollector {
	return &eventCollector{}
}

func (c *eventCollector) Publish(ctx context.Context, event domain.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *eventCollector) allEvents() []domain.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]domain.Event, len(c.events))
	copy(cp, c.events)
	return cp
}

func (c *eventCollector) eventTypes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	types := make([]string, len(c.events))
	for i, e := range c.events {
		types[i] = e.Type
	}
	return types
}

func (c *eventCollector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}

// ─── Registry Adapter (core.ProviderRegistry) ───────────────────────────────

// coreRegistryAdapter wraps provider.Registry to satisfy core.ProviderRegistry.
// core.ProviderRegistry returns nil on not-found (no error), while
// provider.Registry returns (T, error).
type coreRegistryAdapter struct {
	reg *provider.Registry
}

var _ core.ProviderRegistry = (*coreRegistryAdapter)(nil)

func (a *coreRegistryAdapter) GetOnRampProvider(id string) domain.OnRampProvider {
	p, err := a.reg.GetOnRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetOffRampProvider(id string) domain.OffRampProvider {
	p, err := a.reg.GetOffRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetBlockchainClient(chain string) domain.BlockchainClient {
	c, err := a.reg.GetBlockchainClient(chain)
	if err != nil {
		return nil
	}
	return c
}

// ─── Test Harness ───────────────────────────────────────────────────────────

type testHarness struct {
	Engine        *core.Engine
	TransferStore *memTransferStore
	TenantStore   *memTenantStore
	TreasuryStore *memTreasuryStore
	Treasury      *treasury.Manager
	Ledger        *ledger.Service
	TB            *mockTBClient
	Events        *eventCollector
	Registry      *provider.Registry
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	logger := observability.NewLogger("settla-integration-test", "test")
	// Use nil metrics to avoid duplicate Prometheus registration across tests.
	// All modules nil-check metrics before use.
	var metrics *observability.Metrics

	// Stores
	transferStore := newMemTransferStore()
	tenantStore := newMemTenantStore()
	treasuryStore := newMemTreasuryStore()
	events := newEventCollector()

	// Seed Lemfi tenant
	now := time.Now().UTC()
	kybVerified := now.Add(-30 * 24 * time.Hour)
	tenantStore.addTenant(&domain.Tenant{
		ID:              LemfiTenantID,
		Name:            "Lemfi",
		Slug:            "lemfi",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		KYBVerifiedAt:   &kybVerified,
		SettlementModel: domain.SettlementModelPrefunded,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  40,
			OffRampBPS: 35,
		},
		DailyLimitUSD:    decimal.NewFromInt(10_000_000),
		PerTransferLimit: decimal.NewFromInt(1_000_000),
		CreatedAt:        now,
		UpdatedAt:        now,
	})

	// Seed Fincra tenant
	tenantStore.addTenant(&domain.Tenant{
		ID:              FincraTenantID,
		Name:            "Fincra",
		Slug:            "fincra",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		KYBVerifiedAt:   &kybVerified,
		SettlementModel: domain.SettlementModelPrefunded,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  25,
			OffRampBPS: 20,
		},
		DailyLimitUSD:    decimal.NewFromInt(5_000_000),
		PerTransferLimit: decimal.NewFromInt(500_000),
		CreatedAt:        now,
		UpdatedAt:        now,
	})

	// Seed treasury positions for Lemfi
	lemfiGBPPosID := uuid.New()
	treasuryStore.addPosition(domain.Position{
		ID:       lemfiGBPPosID,
		TenantID: LemfiTenantID,
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Balance:  decimal.NewFromInt(1_000_000),
		Locked:   decimal.Zero,
	})

	// Seed treasury positions for Fincra
	fincraNGNPosID := uuid.New()
	treasuryStore.addPosition(domain.Position{
		ID:       fincraNGNPosID,
		TenantID: FincraTenantID,
		Currency: domain.CurrencyNGN,
		Location: "bank:ngn",
		Balance:  decimal.NewFromInt(500_000_000), // 500M NGN
		Locked:   decimal.Zero,
	})

	// Provider registry with mocks
	reg := provider.NewRegistry()

	// On-ramp: GBP→USDT (for Lemfi GBP→NGN corridor)
	onRampGBP := mock.NewOnRampProvider("mock-onramp-gbp", []domain.CurrencyPair{
		{From: domain.CurrencyGBP, To: domain.CurrencyUSDT},
	}, decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.50), 10*time.Millisecond)
	reg.RegisterOnRamp(onRampGBP)

	// On-ramp: NGN→USDT (for Fincra NGN→GBP corridor)
	onRampNGN := mock.NewOnRampProvider("mock-onramp-ngn", []domain.CurrencyPair{
		{From: domain.CurrencyNGN, To: domain.CurrencyUSDT},
	}, decimal.NewFromFloat(0.00065), decimal.NewFromFloat(100), 10*time.Millisecond)
	reg.RegisterOnRamp(onRampNGN)

	// Off-ramp: USDT→NGN
	offRampNGN := mock.NewOffRampProvider("mock-offramp-ngn", []domain.CurrencyPair{
		{From: domain.CurrencyUSDT, To: domain.CurrencyNGN},
	}, decimal.NewFromFloat(1550), decimal.NewFromFloat(200), 10*time.Millisecond)
	reg.RegisterOffRamp(offRampNGN)

	// Off-ramp: USDT→GBP
	offRampGBP := mock.NewOffRampProvider("mock-offramp-gbp", []domain.CurrencyPair{
		{From: domain.CurrencyUSDT, To: domain.CurrencyGBP},
	}, decimal.NewFromFloat(0.80), decimal.NewFromFloat(0.30), 10*time.Millisecond)
	reg.RegisterOffRamp(offRampGBP)

	// Blockchain: Tron
	tronClient := mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.10))
	reg.RegisterBlockchainClient(tronClient)

	// Treasury manager
	treasurySvc := treasury.NewManager(treasuryStore, events, logger, metrics,
		treasury.WithFlushInterval(50*time.Millisecond),
	)
	ctx := context.Background()
	if err := treasurySvc.LoadPositions(ctx); err != nil {
		t.Fatalf("failed to load treasury positions: %v", err)
	}
	treasurySvc.Start()
	t.Cleanup(treasurySvc.Stop)

	// Ledger with mock TigerBeetle (verifies TB receives writes)
	tbClient := newMockTBClient()
	ledgerSvc := ledger.NewService(tbClient, nil, events, logger, metrics, ledger.WithNoBatching())

	// Router
	railRouter := router.NewRouter(reg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine
	coreAdapter := &coreRegistryAdapter{reg: reg}
	engine := core.NewEngine(
		transferStore,
		tenantStore,
		ledgerSvc,
		treasurySvc,
		coreRouterAdapter,
		coreAdapter,
		events,
		logger,
		metrics,
	)

	return &testHarness{
		Engine:        engine,
		TransferStore: transferStore,
		TenantStore:   tenantStore,
		TreasuryStore: treasuryStore,
		Treasury:      treasurySvc,
		Ledger:        ledgerSvc,
		TB:            tbClient,
		Events:        events,
		Registry:      reg,
	}
}
