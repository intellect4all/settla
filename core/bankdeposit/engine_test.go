package bankdeposit

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


type mockBankDepositStore struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]*domain.BankDepositSession
	txs      map[string]*domain.BankDepositTransaction // key: bankReference
	idempMap map[string]*domain.BankDepositSession      // key: tenantID:idempKey
	acctMap  map[string]*domain.BankDepositSession       // key: accountNumber
	outbox   []domain.OutboxEntry
	pool     []*domain.VirtualAccountPool

	// For testing error injection
	createErr     error
	transitionErr error
	dispenseFail  bool
}

func newMockStore() *mockBankDepositStore {
	return &mockBankDepositStore{
		sessions: make(map[uuid.UUID]*domain.BankDepositSession),
		txs:      make(map[string]*domain.BankDepositTransaction),
		idempMap: make(map[string]*domain.BankDepositSession),
		acctMap:  make(map[string]*domain.BankDepositSession),
	}
}

func (m *mockBankDepositStore) seedPool(tenantID uuid.UUID, currency string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < count; i++ {
		m.pool = append(m.pool, &domain.VirtualAccountPool{
			ID:               uuid.New(),
			TenantID:         tenantID,
			BankingPartnerID: "settla-bank",
			AccountNumber:    fmt.Sprintf("VA%s%04d", currency, i),
			AccountName:      fmt.Sprintf("TestCorp %s %d", currency, i),
			SortCode:         "000000",
			IBAN:             fmt.Sprintf("GB00TEST%s%04d", currency, i),
			Currency:         domain.Currency(currency),
			AccountType:      domain.VirtualAccountTypeTemporary,
			Available:        true,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		})
	}
}

func (m *mockBankDepositStore) CreateSessionWithOutbox(_ context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	s := *session
	m.sessions[s.ID] = &s
	if s.IdempotencyKey != "" {
		m.idempMap[s.TenantID.String()+":"+string(s.IdempotencyKey)] = &s
	}
	m.acctMap[s.AccountNumber] = &s
	m.outbox = append(m.outbox, entries...)
	return nil
}

func (m *mockBankDepositStore) GetSession(_ context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return nil, fmt.Errorf("session not found")
	}
	cp := *s
	return &cp, nil
}

func (m *mockBankDepositStore) GetSessionByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key domain.IdempotencyKey) (*domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.idempMap[tenantID.String()+":"+string(key)]
	if !ok {
		return nil, fmt.Errorf("session not found for idempotency key")
	}
	cp := *s
	return &cp, nil
}

func (m *mockBankDepositStore) GetSessionByAccountNumber(_ context.Context, accountNumber string) (*domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.acctMap[accountNumber]
	if !ok {
		return nil, fmt.Errorf("session not found for account number")
	}
	cp := *s
	return &cp, nil
}

func (m *mockBankDepositStore) ListSessions(_ context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.BankDepositSession
	for _, s := range m.sessions {
		if s.TenantID == tenantID {
			result = append(result, *s)
		}
	}
	if offset > len(result) {
		return nil, nil
	}
	result = result[offset:]
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockBankDepositStore) ListSessionsCursor(_ context.Context, tenantID uuid.UUID, pageSize int, cursor time.Time) ([]domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.BankDepositSession
	for _, s := range m.sessions {
		if s.TenantID == tenantID && s.CreatedAt.Before(cursor) {
			result = append(result, *s)
		}
	}
	if pageSize > 0 && len(result) > pageSize {
		result = result[:pageSize]
	}
	return result, nil
}

func (m *mockBankDepositStore) TransitionWithOutbox(_ context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.transitionErr != nil {
		return m.transitionErr
	}
	s := *session
	m.sessions[s.ID] = &s
	if s.IdempotencyKey != "" {
		m.idempMap[s.TenantID.String()+":"+string(s.IdempotencyKey)] = &s
	}
	m.outbox = append(m.outbox, entries...)
	return nil
}

func (m *mockBankDepositStore) DispenseVirtualAccount(_ context.Context, tenantID uuid.UUID, currency string) (*domain.VirtualAccountPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dispenseFail {
		return nil, nil
	}
	for i, p := range m.pool {
		if p.TenantID == tenantID && string(p.Currency) == currency && p.Available {
			m.pool[i].Available = false
			now := time.Now().UTC()
			m.pool[i].UpdatedAt = now
			cp := *m.pool[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockBankDepositStore) RecycleVirtualAccount(_ context.Context, accountNumber string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.pool {
		if p.AccountNumber == accountNumber {
			m.pool[i].Available = true
			m.pool[i].SessionID = nil
			return nil
		}
	}
	return fmt.Errorf("account not found")
}

func (m *mockBankDepositStore) CreateBankDepositTx(_ context.Context, tx *domain.BankDepositTransaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := *tx
	m.txs[tx.BankReference] = &t
	return nil
}

func (m *mockBankDepositStore) GetBankDepositTxByRef(_ context.Context, bankReference string) (*domain.BankDepositTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.txs[bankReference]
	if !ok {
		return nil, fmt.Errorf("tx not found")
	}
	cp := *t
	return &cp, nil
}

func (m *mockBankDepositStore) ListSessionTxs(_ context.Context, sessionID uuid.UUID) ([]domain.BankDepositTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.BankDepositTransaction
	for _, t := range m.txs {
		if t.SessionID == sessionID {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (m *mockBankDepositStore) AccumulateReceived(_ context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.ReceivedAmount = s.ReceivedAmount.Add(amount)
	return nil
}

func (m *mockBankDepositStore) RecordBankDepositTx(_ context.Context, tx *domain.BankDepositTransaction, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := *tx
	m.txs[tx.BankReference] = &t
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.ReceivedAmount = s.ReceivedAmount.Add(amount)
	return nil
}

func (m *mockBankDepositStore) GetExpiredPendingSessions(_ context.Context, limit int) ([]domain.BankDepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.BankDepositSession
	now := time.Now().UTC()
	for _, s := range m.sessions {
		if s.Status == domain.BankDepositSessionStatusPendingPayment && s.ExpiresAt.Before(now) {
			result = append(result, *s)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockBankDepositStore) ListVirtualAccountsByTenant(_ context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.VirtualAccountPool
	for _, a := range m.pool {
		if a.TenantID == tenantID {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (m *mockBankDepositStore) ListVirtualAccountsPaginated(_ context.Context, params VirtualAccountListParams) ([]domain.VirtualAccountPool, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var filtered []domain.VirtualAccountPool
	for _, a := range m.pool {
		if a.TenantID != params.TenantID {
			continue
		}
		if params.Currency != "" && string(a.Currency) != params.Currency {
			continue
		}
		if params.AccountType != "" && string(a.AccountType) != params.AccountType {
			continue
		}
		filtered = append(filtered, *a)
	}
	total := int64(len(filtered))
	start := int(params.Offset)
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + int(params.Limit)
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], total, nil
}

func (m *mockBankDepositStore) ListVirtualAccountsCursor(_ context.Context, params VirtualAccountCursorParams) ([]domain.VirtualAccountPool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var filtered []domain.VirtualAccountPool
	for _, a := range m.pool {
		if a.TenantID != params.TenantID {
			continue
		}
		if params.Currency != "" && string(a.Currency) != params.Currency {
			continue
		}
		if params.AccountType != "" && string(a.AccountType) != params.AccountType {
			continue
		}
		if !a.CreatedAt.After(params.Cursor) {
			continue
		}
		filtered = append(filtered, *a)
	}
	if int32(len(filtered)) > params.PageSize {
		filtered = filtered[:params.PageSize]
	}
	return filtered, nil
}

func (m *mockBankDepositStore) CountAvailableVirtualAccountsByCurrency(_ context.Context, tenantID uuid.UUID) (map[string]int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]int64)
	for _, a := range m.pool {
		if a.TenantID == tenantID && a.Available {
			result[string(a.Currency)]++
		}
	}
	return result, nil
}

func (m *mockBankDepositStore) GetVirtualAccountIndexByNumber(_ context.Context, accountNumber string) (*VirtualAccountIndex, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *mockBankDepositStore) outboxEntries() []domain.OutboxEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]domain.OutboxEntry, len(m.outbox))
	copy(cp, m.outbox)
	return cp
}


type mockTenantStore struct {
	tenants map[uuid.UUID]*domain.Tenant
}

func newMockTenantStore() *mockTenantStore {
	return &mockTenantStore{tenants: make(map[uuid.UUID]*domain.Tenant)}
}

func (m *mockTenantStore) GetTenant(_ context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	t, ok := m.tenants[tenantID]
	if !ok {
		return nil, fmt.Errorf("tenant not found")
	}
	return t, nil
}


var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

func testTenantID() uuid.UUID {
	return uuid.MustParse("a0000000-0000-0000-0000-000000000001")
}

func testTenant() *domain.Tenant {
	return &domain.Tenant{
		ID:     testTenantID(),
		Name:   "TestCorp",
		Slug:   "testcorp",
		Status: domain.TenantStatusActive,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:               40,
			OffRampBPS:              25,
			BankCollectionBPS:       25,
			BankCollectionMinFeeUSD: decimal.NewFromFloat(0.50),
			BankCollectionMaxFeeUSD: decimal.NewFromInt(100),
			MinFeeUSD:               decimal.NewFromFloat(0.50),
			MaxFeeUSD:               decimal.NewFromInt(500),
		},
		KYBStatus: domain.KYBStatusVerified,
		BankConfig: domain.TenantBankConfig{
			BankDepositsEnabled:     true,
			DefaultBankingPartner:   "settla-bank",
			BankSupportedCurrencies: []domain.Currency{"GBP", "EUR", "USD"},
			DefaultMismatchPolicy:   domain.PaymentMismatchPolicyAccept,
			DefaultSessionTTLSecs:   3600,
		},
	}
}

func setupEngine() (*Engine, *mockBankDepositStore, *mockTenantStore) {
	store := newMockStore()
	tenantStore := newMockTenantStore()
	tenant := testTenant()
	tenantStore.tenants[tenant.ID] = tenant
	store.seedPool(tenant.ID, "GBP", 10)
	engine := NewEngine(store, tenantStore, testLogger)
	return engine, store, tenantStore
}

func createTestSession(t *testing.T, engine *Engine) *domain.BankDepositSession {
	t.Helper()
	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return session
}

func testBankCredit(amount decimal.Decimal, ref string) domain.IncomingBankCredit {
	return domain.IncomingBankCredit{
		AccountNumber:      "VA-GBP-0001",
		Amount:             amount,
		Currency:           domain.Currency("GBP"),
		PayerName:          "John Doe",
		PayerAccountNumber: "12345678",
		PayerReference:     "INV-001",
		BankReference:      ref,
		ReceivedAt:         time.Now().UTC(),
	}
}


func TestCreateSession_HappyPath(t *testing.T) {
	engine, store, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		IdempotencyKey: "bdep-001",
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if session.Status != domain.BankDepositSessionStatusPendingPayment {
		t.Errorf("expected PENDING_PAYMENT, got %s", session.Status)
	}
	if session.Currency != domain.Currency("GBP") {
		t.Errorf("expected currency GBP, got %s", session.Currency)
	}
	if session.AccountNumber == "" {
		t.Error("expected non-empty account number")
	}
	if session.SettlementPref != domain.SettlementPreferenceAutoConvert {
		t.Errorf("expected AUTO_CONVERT, got %s", session.SettlementPref)
	}
	if session.CollectionFeeBPS != 25 {
		t.Errorf("expected 25 bps, got %d", session.CollectionFeeBPS)
	}
	if session.AccountType != domain.VirtualAccountTypeTemporary {
		t.Errorf("expected TEMPORARY, got %s", session.AccountType)
	}

	// Verify outbox entries: EventBankDepositSessionCreated + IntentWebhookDeliver + IntentEmailNotify
	entries := store.outboxEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 outbox entries, got %d", len(entries))
	}
	if entries[0].EventType != domain.EventBankDepositSessionCreated {
		t.Errorf("expected EventBankDepositSessionCreated, got %s", entries[0].EventType)
	}
	if entries[1].EventType != domain.IntentWebhookDeliver {
		t.Errorf("expected IntentWebhookDeliver, got %s", entries[1].EventType)
	}
	if entries[2].EventType != domain.IntentEmailNotify {
		t.Errorf("expected IntentEmailNotify, got %s", entries[2].EventType)
	}
}

func TestCreateSession_Idempotency(t *testing.T) {
	engine, _, _ := setupEngine()
	ctx := context.Background()

	req := CreateSessionRequest{
		IdempotencyKey: "bdep-idem-001",
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(50),
	}

	s1, err := engine.CreateSession(ctx, testTenantID(), req)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	s2, err := engine.CreateSession(ctx, testTenantID(), req)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}

	if s1.ID != s2.ID {
		t.Error("expected same session ID for idempotent request")
	}
}

func TestCreateSession_BankDepositsDisabled(t *testing.T) {
	engine, _, tenantStore := setupEngine()
	tenant := testTenant()
	tenant.BankConfig.BankDepositsEnabled = false
	tenantStore.tenants[tenant.ID] = tenant

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for disabled bank deposits")
	}
}

func TestCreateSession_CurrencyNotSupported(t *testing.T) {
	engine, _, _ := setupEngine()

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "JPY",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestCreateSession_EmptyPool(t *testing.T) {
	engine, store, _ := setupEngine()
	store.dispenseFail = true

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestCreateSession_ZeroAmount(t *testing.T) {
	engine, _, _ := setupEngine()

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.Zero,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestCreateSession_DefaultTTL(t *testing.T) {
	engine, _, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Default TTL is 3600s from tenant config
	expectedExpiry := session.CreatedAt.Add(3600 * time.Second)
	diff := session.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expiry time off by %v", diff)
	}
}

func TestCreateSession_DefaultSettlementPref(t *testing.T) {
	engine, _, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		// No SettlementPref -> should default to HOLD
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.SettlementPref != domain.SettlementPreferenceHold {
		t.Errorf("expected HOLD (default), got %s", session.SettlementPref)
	}
}

func TestHandleBankCreditReceived_HappyPath(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)

	credit := testBankCredit(decimal.NewFromInt(100), "REF-001")

	err := engine.HandleBankCreditReceived(context.Background(), testTenantID(), session.ID, credit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Session should now be in CREDITING (PENDING_PAYMENT -> PAYMENT_RECEIVED -> CREDITING)
	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusCrediting {
		t.Errorf("expected CREDITING, got %s", s.Status)
	}
	if s.PaymentReceivedAt == nil {
		t.Error("expected PaymentReceivedAt to be set")
	}
	if s.PayerName != "John Doe" {
		t.Errorf("expected payer name 'John Doe', got %q", s.PayerName)
	}

	// Verify IntentBankDepositCredit was emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentBankDepositCredit {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentBankDepositCredit in outbox")
	}
}

func TestHandleBankCreditReceived_DuplicateRef(t *testing.T) {
	engine, _, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	credit := testBankCredit(decimal.NewFromInt(100), "REF-DUP")

	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("first credit failed: %v", err)
	}

	// Second credit with same reference should be a no-op
	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("duplicate credit should not error: %v", err)
	}
}

func TestHandleBankCreditReceived_Underpaid_RejectPolicy(t *testing.T) {
	engine, store, tenantStore := setupEngine()

	// Set REJECT mismatch policy
	tenant := testTenant()
	tenant.BankConfig.DefaultMismatchPolicy = domain.PaymentMismatchPolicyReject
	tenantStore.tenants[tenant.ID] = tenant

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		MinAmount:      decimal.NewFromInt(95),
		MaxAmount:      decimal.NewFromInt(105),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Send underpayment
	credit := testBankCredit(decimal.NewFromInt(50), "REF-UNDER")
	if err := engine.HandleBankCreditReceived(context.Background(), testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}

	// Verify underpaid event was emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.EventBankDepositUnderpaid {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventBankDepositUnderpaid in outbox")
	}
}

func TestHandleBankCreditReceived_Overpaid_RejectPolicy(t *testing.T) {
	engine, store, tenantStore := setupEngine()

	tenant := testTenant()
	tenant.BankConfig.DefaultMismatchPolicy = domain.PaymentMismatchPolicyReject
	tenantStore.tenants[tenant.ID] = tenant

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		MinAmount:      decimal.NewFromInt(95),
		MaxAmount:      decimal.NewFromInt(105),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Send overpayment
	credit := testBankCredit(decimal.NewFromInt(200), "REF-OVER")
	if err := engine.HandleBankCreditReceived(context.Background(), testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}

	// Verify overpaid event was emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.EventBankDepositOverpaid {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventBankDepositOverpaid in outbox")
	}
}

func TestHandleBankCreditReceived_Underpaid_AcceptPolicy(t *testing.T) {
	engine, store, _ := setupEngine()

	// Default tenant has ACCEPT mismatch policy
	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		MinAmount:      decimal.NewFromInt(95),
		MaxAmount:      decimal.NewFromInt(105),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Send underpayment — ACCEPT policy should still proceed
	credit := testBankCredit(decimal.NewFromInt(50), "REF-ACCEPT-UNDER")
	if err := engine.HandleBankCreditReceived(context.Background(), testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusCrediting {
		t.Errorf("expected CREDITING (ACCEPT policy), got %s", s.Status)
	}
}

func TestHandleBankCreditReceived_LatePayment_AfterExpiry(t *testing.T) {
	engine, store, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		TTLSeconds:     1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	// Expire the session
	if err := engine.ExpireSession(context.Background(), testTenantID(), session.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	// Late payment arrives
	credit := testBankCredit(decimal.NewFromInt(100), "REF-LATE")
	if err := engine.HandleBankCreditReceived(context.Background(), testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("late payment credit: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	// Should be CREDITING because payment was received and initiated credit
	if s.Status != domain.BankDepositSessionStatusCrediting {
		t.Errorf("expected CREDITING after late payment, got %s", s.Status)
	}

	// Verify late payment event emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.EventBankDepositLatePayment {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventBankDepositLatePayment in outbox")
	}
}

func TestHandleCreditResult_Success_HoldPref(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Receive payment -> PAYMENT_RECEIVED -> CREDITING
	credit := testBankCredit(decimal.NewFromInt(100), "REF-HOLD")
	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit received: %v", err)
	}

	// Credit result success -> CREDITED -> HELD (default pref is HOLD)
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusHeld {
		t.Errorf("expected HELD, got %s", s.Status)
	}

	// Fee: 100 * 25/10000 = 0.25, but BankCollectionMinFeeUSD = 0.50, so fee is clamped to 0.50
	expectedFee := decimal.NewFromFloat(0.50)
	if !s.FeeAmount.Equal(expectedFee) {
		t.Errorf("expected fee %s, got %s", expectedFee, s.FeeAmount)
	}
	expectedNet := decimal.NewFromFloat(99.50)
	if !s.NetAmount.Equal(expectedNet) {
		t.Errorf("expected net %s, got %s", expectedNet, s.NetAmount)
	}
}

func TestHandleCreditResult_Success_AutoConvert(t *testing.T) {
	engine, store, _ := setupEngine()
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(1000),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Receive payment
	credit := testBankCredit(decimal.NewFromInt(1000), "REF-AUTO")
	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit received: %v", err)
	}

	// Credit result success -> CREDITED -> SETTLING (AUTO_CONVERT)
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusSettling {
		t.Errorf("expected SETTLING, got %s", s.Status)
	}

	// Verify IntentBankDepositSettle emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentBankDepositSettle {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentBankDepositSettle in outbox")
	}
}

func TestHandleCreditResult_Failure(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Receive payment
	credit := testBankCredit(decimal.NewFromInt(100), "REF-CFAIL")
	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit received: %v", err)
	}

	// Credit fails
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{
		Success:   false,
		Error:     "ledger unavailable",
		ErrorCode: "LEDGER_ERROR",
	}); err != nil {
		t.Fatalf("credit result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}
	if s.FailureReason != "ledger unavailable" {
		t.Errorf("expected failure reason 'ledger unavailable', got %q", s.FailureReason)
	}
}

func TestHandleSettlementResult_Success(t *testing.T) {
	engine, store, _ := setupEngine()
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(500),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Drive through to SETTLING
	credit := testBankCredit(decimal.NewFromInt(500), "REF-SETTLE")
	engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit)
	engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	// Settlement succeeds
	transferID := uuid.New()
	if err := engine.HandleSettlementResult(ctx, testTenantID(), session.ID, domain.IntentResult{
		Success:  true,
		Metadata: map[string]string{"transfer_id": transferID.String()},
	}); err != nil {
		t.Fatalf("settlement result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusSettled {
		t.Errorf("expected SETTLED, got %s", s.Status)
	}
	if s.SettledAt == nil {
		t.Error("expected SettledAt to be set")
	}
	if s.SettlementTransferID == nil || *s.SettlementTransferID != transferID {
		t.Error("expected settlement transfer ID to be linked")
	}
}

func TestHandleSettlementResult_Failure(t *testing.T) {
	engine, store, _ := setupEngine()
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(500),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	credit := testBankCredit(decimal.NewFromInt(500), "REF-SFAIL")
	engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit)
	engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	if err := engine.HandleSettlementResult(ctx, testTenantID(), session.ID, domain.IntentResult{
		Success: false, Error: "conversion failed",
	}); err != nil {
		t.Fatalf("settlement result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}
}

func TestExpireSession(t *testing.T) {
	engine, store, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(100),
		TTLSeconds:     1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Wait for expiry
	time.Sleep(1100 * time.Millisecond)

	if err := engine.ExpireSession(context.Background(), testTenantID(), session.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusExpired {
		t.Errorf("expected EXPIRED, got %s", s.Status)
	}

	// Verify IntentRecycleVirtualAccount emitted for TEMPORARY account
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentRecycleVirtualAccount {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentRecycleVirtualAccount in outbox for TEMPORARY account")
	}
}

func TestCancelSession(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)

	if err := engine.CancelSession(context.Background(), testTenantID(), session.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", s.Status)
	}

	// Verify IntentRecycleVirtualAccount emitted for TEMPORARY account
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentRecycleVirtualAccount {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentRecycleVirtualAccount in outbox for TEMPORARY account")
	}
}

func TestCancelSession_NotPending(t *testing.T) {
	engine, _, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Receive payment -> now past PENDING_PAYMENT
	credit := testBankCredit(decimal.NewFromInt(100), "REF-CANCEL-FAIL")
	engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit)

	err := engine.CancelSession(ctx, testTenantID(), session.ID)
	if err == nil {
		t.Fatal("expected error cancelling non-pending session")
	}
}

func TestFeeCalculation(t *testing.T) {
	schedule := domain.FeeSchedule{
		BankCollectionBPS:       25,
		BankCollectionMinFeeUSD: decimal.NewFromFloat(0.50),
		BankCollectionMaxFeeUSD: decimal.NewFromInt(100),
	}

	tests := []struct {
		name     string
		amount   decimal.Decimal
		expected decimal.Decimal
	}{
		{"small amount - clamped to min", decimal.NewFromInt(100), decimal.NewFromFloat(0.50)},
		{"medium amount", decimal.NewFromInt(10000), decimal.NewFromInt(25)},
		{"large amount - capped", decimal.NewFromInt(1000000), decimal.NewFromInt(100)},
		{"zero amount - min fee applied", decimal.Zero, decimal.NewFromFloat(0.50)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee := CalculateBankCollectionFee(tt.amount, schedule)
			if !fee.Equal(tt.expected) {
				t.Errorf("expected fee %s, got %s", tt.expected, fee)
			}
		})
	}
}

func TestGetSession(t *testing.T) {
	engine, _, _ := setupEngine()
	session := createTestSession(t, engine)

	got, err := engine.GetSession(context.Background(), testTenantID(), session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.ID != session.ID {
		t.Errorf("expected session ID %s, got %s", session.ID, got.ID)
	}
}

func TestListSessions(t *testing.T) {
	engine, _, _ := setupEngine()

	// Create 3 sessions
	for i := 0; i < 3; i++ {
		_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
			Currency:       "GBP",
			ExpectedAmount: decimal.NewFromInt(int64(100 + i)),
		})
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
	}

	sessions, err := engine.ListSessions(context.Background(), testTenantID(), 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestFullHappyPath_Hold(t *testing.T) {
	engine, store, _ := setupEngine()
	ctx := context.Background()

	// 1. Create session
	session := createTestSession(t, engine)

	// 2. Receive payment
	credit := testBankCredit(decimal.NewFromInt(100), "REF-FULL-HOLD")
	if err := engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit); err != nil {
		t.Fatalf("credit: %v", err)
	}

	// 3. Credit result
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit result: %v", err)
	}

	// Final state should be HELD (default preference)
	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusHeld {
		t.Errorf("expected HELD, got %s", s.Status)
	}
	if s.CreditedAt == nil {
		t.Error("expected CreditedAt to be set")
	}
	if s.FeeAmount.IsZero() {
		t.Error("expected non-zero fee")
	}
	if s.NetAmount.IsZero() {
		t.Error("expected non-zero net amount")
	}
}

func TestFullHappyPath_AutoConvert(t *testing.T) {
	engine, store, _ := setupEngine()
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, testTenantID(), CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromInt(1000),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	credit := testBankCredit(decimal.NewFromInt(1000), "REF-FULL-AC")
	engine.HandleBankCreditReceived(ctx, testTenantID(), session.ID, credit)
	engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	// Should be SETTLING now
	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusSettling {
		t.Fatalf("expected SETTLING, got %s", s.Status)
	}

	// Complete settlement
	engine.HandleSettlementResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	s, _ = store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.BankDepositSessionStatusSettled {
		t.Errorf("expected SETTLED, got %s", s.Status)
	}
}
