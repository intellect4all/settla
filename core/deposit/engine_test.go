package deposit

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

// ── Mock Store ───────────────────────────────────────────────────────────────

type mockDepositStore struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]*domain.DepositSession
	txs      map[string]*domain.DepositTransaction // key: chain:txHash
	idempMap map[string]*domain.DepositSession      // key: tenantID:idempKey
	addrMap  map[string]*domain.DepositSession       // key: address
	outbox   []domain.OutboxEntry
	pool     []*domain.CryptoAddressPool

	// For testing error injection
	createErr     error
	transitionErr error
	dispenseFail  bool
}

func newMockStore() *mockDepositStore {
	return &mockDepositStore{
		sessions: make(map[uuid.UUID]*domain.DepositSession),
		txs:      make(map[string]*domain.DepositTransaction),
		idempMap: make(map[string]*domain.DepositSession),
		addrMap:  make(map[string]*domain.DepositSession),
	}
}

func (m *mockDepositStore) seedPool(tenantID uuid.UUID, chain string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < count; i++ {
		m.pool = append(m.pool, &domain.CryptoAddressPool{
			ID:              uuid.New(),
			TenantID:        tenantID,
			Chain:           chain,
			Address:         fmt.Sprintf("T%s%04d", chain, i),
			DerivationIndex: int64(i),
		})
	}
}

func (m *mockDepositStore) CreateSessionWithOutbox(_ context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	s := *session
	m.sessions[s.ID] = &s
	if s.IdempotencyKey != "" {
		m.idempMap[s.TenantID.String()+":"+s.IdempotencyKey] = &s
	}
	m.addrMap[s.DepositAddress] = &s
	m.outbox = append(m.outbox, entries...)
	return nil
}

func (m *mockDepositStore) GetSession(_ context.Context, tenantID, sessionID uuid.UUID) (*domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return nil, fmt.Errorf("session not found")
	}
	cp := *s
	return &cp, nil
}

func (m *mockDepositStore) GetSessionByAddress(_ context.Context, address string) (*domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.addrMap[address]
	if !ok {
		return nil, fmt.Errorf("session not found for address")
	}
	cp := *s
	return &cp, nil
}

func (m *mockDepositStore) GetSessionByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key string) (*domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.idempMap[tenantID.String()+":"+key]
	if !ok {
		return nil, fmt.Errorf("session not found for idempotency key")
	}
	cp := *s
	return &cp, nil
}

func (m *mockDepositStore) ListSessions(_ context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.DepositSession
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

func (m *mockDepositStore) TransitionWithOutbox(_ context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.transitionErr != nil {
		return m.transitionErr
	}
	s := *session
	m.sessions[s.ID] = &s
	if s.IdempotencyKey != "" {
		m.idempMap[s.TenantID.String()+":"+s.IdempotencyKey] = &s
	}
	m.outbox = append(m.outbox, entries...)
	return nil
}

func (m *mockDepositStore) DispenseAddress(_ context.Context, tenantID uuid.UUID, chain string, _ uuid.UUID) (*domain.CryptoAddressPool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dispenseFail {
		return nil, nil
	}
	for i, p := range m.pool {
		if p.TenantID == tenantID && p.Chain == chain && !p.Dispensed {
			m.pool[i].Dispensed = true
			now := time.Now().UTC()
			m.pool[i].DispensedAt = &now
			cp := *m.pool[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockDepositStore) CreateDepositTx(_ context.Context, tx *domain.DepositTransaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := *tx
	m.txs[tx.Chain+":"+tx.TxHash] = &t
	return nil
}

func (m *mockDepositStore) GetDepositTxByHash(_ context.Context, chain, txHash string) (*domain.DepositTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.txs[chain+":"+txHash]
	if !ok {
		return nil, fmt.Errorf("tx not found")
	}
	cp := *t
	return &cp, nil
}

func (m *mockDepositStore) ListSessionTxs(_ context.Context, sessionID uuid.UUID) ([]domain.DepositTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.DepositTransaction
	for _, t := range m.txs {
		if t.SessionID == sessionID {
			result = append(result, *t)
		}
	}
	return result, nil
}

func (m *mockDepositStore) AccumulateReceived(_ context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.ReceivedAmount = s.ReceivedAmount.Add(amount)
	return nil
}

func (m *mockDepositStore) GetExpiredPendingSessions(_ context.Context, limit int) ([]domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.DepositSession
	now := time.Now().UTC()
	for _, s := range m.sessions {
		if s.Status == domain.DepositSessionStatusPendingPayment && s.ExpiresAt.Before(now) {
			result = append(result, *s)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockDepositStore) GetSessionByIDOnly(_ context.Context, sessionID uuid.UUID) (*domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	cp := *s
	return &cp, nil
}

func (m *mockDepositStore) GetSessionByTxHash(_ context.Context, tenantID uuid.UUID, chain, txHash string) (*domain.DepositSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := chain + ":" + txHash
	tx, ok := m.txs[key]
	if !ok {
		return nil, fmt.Errorf("tx %s not found", key)
	}
	sess, ok := m.sessions[tx.SessionID]
	if !ok || sess.TenantID != tenantID {
		return nil, fmt.Errorf("session not found for tx %s", key)
	}
	return sess, nil
}

func (m *mockDepositStore) outboxEntries() []domain.OutboxEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]domain.OutboxEntry, len(m.outbox))
	copy(cp, m.outbox)
	return cp
}

// ── Mock Tenant Store ────────────────────────────────────────────────────────

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

// ── Test Helpers ─────────────────────────────────────────────────────────────

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
			OnRampBPS:                 40,
			OffRampBPS:                25,
			CryptoCollectionBPS:       30,
			CryptoCollectionMaxFeeUSD: decimal.NewFromInt(250),
			MinFeeUSD:                 decimal.NewFromFloat(0.50),
			MaxFeeUSD:                 decimal.NewFromInt(500),
		},
		KYBStatus: domain.KYBStatusVerified,
		CryptoConfig: domain.TenantCryptoConfig{
			CryptoEnabled:         true,
			DefaultSettlementPref: domain.SettlementPreferenceHold,
			SupportedChains:       []string{"tron", "ethereum"},
			DefaultSessionTTLSecs: 3600,
			PaymentToleranceBPS:   50,
		},
	}
}

func setupEngine() (*Engine, *mockDepositStore, *mockTenantStore) {
	store := newMockStore()
	tenantStore := newMockTenantStore()
	tenant := testTenant()
	tenantStore.tenants[tenant.ID] = tenant
	store.seedPool(tenant.ID, "tron", 10)
	engine := NewEngine(store, tenantStore, testLogger)
	return engine, store, tenantStore
}

func createTestSession(t *testing.T, engine *Engine) *domain.DepositSession {
	t.Helper()
	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return session
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestCreateSession_HappyPath(t *testing.T) {
	engine, store, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		IdempotencyKey: "dep-001",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if session.Status != domain.DepositSessionStatusPendingPayment {
		t.Errorf("expected PENDING_PAYMENT, got %s", session.Status)
	}
	if session.Chain != "tron" {
		t.Errorf("expected chain tron, got %s", session.Chain)
	}
	if session.DepositAddress == "" {
		t.Error("expected non-empty deposit address")
	}
	if session.SettlementPref != domain.SettlementPreferenceAutoConvert {
		t.Errorf("expected AUTO_CONVERT, got %s", session.SettlementPref)
	}
	if session.CollectionFeeBPS != 30 {
		t.Errorf("expected 30 bps, got %d", session.CollectionFeeBPS)
	}

	// Verify outbox entries: IntentMonitorAddress + EventDepositSessionCreated + IntentWebhookDeliver + IntentEmailNotify
	entries := store.outboxEntries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 outbox entries, got %d", len(entries))
	}
	if entries[0].EventType != domain.IntentMonitorAddress {
		t.Errorf("expected IntentMonitorAddress, got %s", entries[0].EventType)
	}
	if entries[1].EventType != domain.EventDepositSessionCreated {
		t.Errorf("expected EventDepositSessionCreated, got %s", entries[1].EventType)
	}
	if entries[2].EventType != domain.IntentWebhookDeliver {
		t.Errorf("expected IntentWebhookDeliver, got %s", entries[2].EventType)
	}
	if entries[3].EventType != domain.IntentEmailNotify {
		t.Errorf("expected IntentEmailNotify, got %s", entries[3].EventType)
	}
}

func TestCreateSession_Idempotency(t *testing.T) {
	engine, _, _ := setupEngine()
	ctx := context.Background()

	req := CreateSessionRequest{
		IdempotencyKey: "dep-idem-001",
		Chain:          "tron",
		Token:          "USDT",
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

func TestCreateSession_CryptoDisabled(t *testing.T) {
	engine, _, tenantStore := setupEngine()
	tenant := testTenant()
	tenant.CryptoConfig.CryptoEnabled = false
	tenantStore.tenants[tenant.ID] = tenant

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for disabled crypto")
	}
}

func TestCreateSession_UnsupportedChain(t *testing.T) {
	engine, _, _ := setupEngine()

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "solana",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for unsupported chain")
	}
}

func TestCreateSession_EmptyPool(t *testing.T) {
	engine, store, _ := setupEngine()
	store.dispenseFail = true

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
	})
	if err == nil {
		t.Fatal("expected error for empty pool")
	}
}

func TestCreateSession_ZeroAmount(t *testing.T) {
	engine, _, _ := setupEngine()

	_, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.Zero,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestCreateSession_DefaultTTL(t *testing.T) {
	engine, _, _ := setupEngine()

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
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
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
		// No SettlementPref → should use tenant default (HOLD)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.SettlementPref != domain.SettlementPreferenceHold {
		t.Errorf("expected HOLD (tenant default), got %s", session.SettlementPref)
	}
}

func TestHandleTransactionDetected_HappyPath(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)

	err := engine.HandleTransactionDetected(context.Background(), testTenantID(), session.ID, domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xabc123",
		FromAddress: "TsenderXYZ",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(100),
		BlockNumber: 12345,
		BlockHash:   "0xblock",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify session transitioned to DETECTED
	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusDetected {
		t.Errorf("expected DETECTED, got %s", s.Status)
	}
	if s.DetectedAt == nil {
		t.Error("expected DetectedAt to be set")
	}

	// Verify tx recorded
	tx, err := store.GetDepositTxByHash(context.Background(), "tron", "0xabc123")
	if err != nil {
		t.Fatalf("tx not found: %v", err)
	}
	if !tx.Amount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("expected tx amount 100, got %s", tx.Amount)
	}
}

func TestHandleTransactionDetected_DuplicateTx(t *testing.T) {
	engine, _, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	tx := domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xdup",
		FromAddress: "Tsender",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(100),
		BlockNumber: 100,
		BlockHash:   "0xb",
	}

	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, tx); err != nil {
		t.Fatalf("first detect failed: %v", err)
	}

	// Second detection of same tx should be a no-op
	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, tx); err != nil {
		t.Fatalf("duplicate detect should not error: %v", err)
	}
}

func TestHandleTransactionConfirmed_HappyPath(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Detect
	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xconfirm",
		FromAddress: "Tsender",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(100),
		BlockNumber: 200,
		BlockHash:   "0xbh",
	}); err != nil {
		t.Fatalf("detect failed: %v", err)
	}

	// Confirm → should transition through CONFIRMED → CREDITING
	if err := engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xconfirm", 19); err != nil {
		t.Fatalf("confirm failed: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusCrediting {
		t.Errorf("expected CREDITING, got %s", s.Status)
	}
	if s.ConfirmedAt == nil {
		t.Error("expected ConfirmedAt to be set")
	}

	// Verify IntentCreditDeposit was emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentCreditDeposit {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentCreditDeposit in outbox")
	}
}

func TestHandleCreditResult_Success_HoldPref(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Move through: PENDING → DETECTED → CONFIRMED → CREDITING
	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xcredit", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 300, BlockHash: "0xbh",
	}); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if err := engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xcredit", 19); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// Credit result success → CREDITED → HELD (default pref is HOLD)
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusHeld {
		t.Errorf("expected HELD, got %s", s.Status)
	}

	// Fee: 100 * 30/10000 = 0.30, but MinFeeUSD = 0.50, so fee is clamped to 0.50
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
	engine, store, tenantStore := setupEngine()

	// Override settlement pref to AUTO_CONVERT
	tenant := testTenant()
	tenant.CryptoConfig.DefaultSettlementPref = domain.SettlementPreferenceAutoConvert
	tenantStore.tenants[tenant.ID] = tenant

	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ctx := context.Background()

	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xauto", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(1000), BlockNumber: 400, BlockHash: "0xbh",
	}); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if err := engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xauto", 19); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusSettling {
		t.Errorf("expected SETTLING, got %s", s.Status)
	}

	// Verify IntentSettleDeposit emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.IntentSettleDeposit {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IntentSettleDeposit in outbox")
	}
}

func TestHandleCreditResult_Failure(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// PENDING → DETECTED → CONFIRMED → CREDITING
	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xfail", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 500, BlockHash: "0xbh",
	}); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if err := engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xfail", 19); err != nil {
		t.Fatalf("confirm: %v", err)
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
	if s.Status != domain.DepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}
	if s.FailureReason != "ledger unavailable" {
		t.Errorf("expected failure reason 'ledger unavailable', got %q", s.FailureReason)
	}
}

func TestHandleSettlementResult_Success(t *testing.T) {
	engine, store, tenantStore := setupEngine()

	tenant := testTenant()
	tenant.CryptoConfig.DefaultSettlementPref = domain.SettlementPreferenceAutoConvert
	tenantStore.tenants[tenant.ID] = tenant

	session, _ := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain: "tron", Token: "USDT", ExpectedAmount: decimal.NewFromInt(500),
	})
	ctx := context.Background()

	// Drive through to SETTLING
	engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xsettle", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(500), BlockNumber: 600, BlockHash: "0xbh",
	})
	engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xsettle", 19)
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
	if s.Status != domain.DepositSessionStatusSettled {
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
	engine, store, tenantStore := setupEngine()

	tenant := testTenant()
	tenant.CryptoConfig.DefaultSettlementPref = domain.SettlementPreferenceAutoConvert
	tenantStore.tenants[tenant.ID] = tenant

	session, _ := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain: "tron", Token: "USDT", ExpectedAmount: decimal.NewFromInt(500),
	})
	ctx := context.Background()

	engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xsfail", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(500), BlockNumber: 700, BlockHash: "0xbh",
	})
	engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xsfail", 19)
	engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	if err := engine.HandleSettlementResult(ctx, testTenantID(), session.ID, domain.IntentResult{
		Success: false, Error: "conversion failed",
	}); err != nil {
		t.Fatalf("settlement result: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusFailed {
		t.Errorf("expected FAILED, got %s", s.Status)
	}
}

func TestExpireSession(t *testing.T) {
	engine, store, _ := setupEngine()

	// Create session with very short TTL
	session, err := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain:          "tron",
		Token:          "USDT",
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
	if s.Status != domain.DepositSessionStatusExpired {
		t.Errorf("expected EXPIRED, got %s", s.Status)
	}
}

func TestExpireSession_AlreadyDetected(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Detect a tx first
	engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xpreexpiry", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 800, BlockHash: "0xbh",
	})

	// Try to expire — should be no-op since it's already DETECTED
	if err := engine.ExpireSession(ctx, testTenantID(), session.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusDetected {
		t.Errorf("expected DETECTED (unchanged), got %s", s.Status)
	}
}

func TestCancelSession(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)

	if err := engine.CancelSession(context.Background(), testTenantID(), session.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", s.Status)
	}
}

func TestCancelSession_NotPending(t *testing.T) {
	engine, _, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	// Detect → now DETECTED, cancel should fail
	engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xcancel", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 900, BlockHash: "0xbh",
	})

	err := engine.CancelSession(ctx, testTenantID(), session.ID)
	if err == nil {
		t.Fatal("expected error cancelling non-pending session")
	}
}

func TestLatePayment_AfterExpiry(t *testing.T) {
	engine, store, _ := setupEngine()

	session, _ := engine.CreateSession(context.Background(), testTenantID(), CreateSessionRequest{
		Chain: "tron", Token: "USDT", ExpectedAmount: decimal.NewFromInt(100), TTLSeconds: 1,
	})

	time.Sleep(1100 * time.Millisecond)

	// Expire the session
	engine.ExpireSession(context.Background(), testTenantID(), session.ID)

	// Late payment arrives
	err := engine.HandleTransactionDetected(context.Background(), testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xlate", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 1000, BlockHash: "0xbh",
	})
	if err != nil {
		t.Fatalf("late payment detect: %v", err)
	}

	s, _ := store.GetSession(context.Background(), testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusDetected {
		t.Errorf("expected DETECTED after late payment, got %s", s.Status)
	}

	// Verify late payment event emitted
	entries := store.outboxEntries()
	found := false
	for _, e := range entries {
		if e.EventType == domain.EventDepositLatePayment {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventDepositLatePayment in outbox")
	}
}

func TestLatePayment_AfterCancel(t *testing.T) {
	engine, store, _ := setupEngine()
	session := createTestSession(t, engine)
	ctx := context.Background()

	engine.CancelSession(ctx, testTenantID(), session.ID)

	// Late payment
	err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xlatecancel", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 1100, BlockHash: "0xbh",
	})
	if err != nil {
		t.Fatalf("late payment after cancel: %v", err)
	}

	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusDetected {
		t.Errorf("expected DETECTED, got %s", s.Status)
	}
}

func TestFeeCalculation(t *testing.T) {
	schedule := domain.FeeSchedule{
		CryptoCollectionBPS:       30,
		CryptoCollectionMaxFeeUSD: decimal.NewFromInt(250),
	}

	tests := []struct {
		name     string
		amount   decimal.Decimal
		expected decimal.Decimal
	}{
		{"small amount", decimal.NewFromInt(100), decimal.NewFromFloat(0.30)},
		{"medium amount", decimal.NewFromInt(10000), decimal.NewFromInt(30)},
		{"large amount - capped", decimal.NewFromInt(1000000), decimal.NewFromInt(250)},
		{"zero amount", decimal.Zero, decimal.Zero},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee := CalculateCollectionFee(tt.amount, schedule)
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
			Chain:          "tron",
			Token:          "USDT",
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

	// 2. Detect tx
	if err := engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xfull", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(100), BlockNumber: 1200, BlockHash: "0xbh",
	}); err != nil {
		t.Fatalf("detect: %v", err)
	}

	// 3. Confirm tx
	if err := engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xfull", 19); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// 4. Credit result
	if err := engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("credit: %v", err)
	}

	// Final state should be HELD (default preference)
	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusHeld {
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
	engine, store, tenantStore := setupEngine()
	ctx := context.Background()

	tenant := testTenant()
	tenant.CryptoConfig.DefaultSettlementPref = domain.SettlementPreferenceAutoConvert
	tenantStore.tenants[tenant.ID] = tenant

	session, _ := engine.CreateSession(ctx, testTenantID(), CreateSessionRequest{
		Chain: "tron", Token: "USDT", ExpectedAmount: decimal.NewFromInt(1000),
	})

	engine.HandleTransactionDetected(ctx, testTenantID(), session.ID, domain.IncomingTransaction{
		Chain: "tron", TxHash: "0xfullac", FromAddress: "T1", ToAddress: session.DepositAddress,
		Amount: decimal.NewFromInt(1000), BlockNumber: 1300, BlockHash: "0xbh",
	})
	engine.HandleTransactionConfirmed(ctx, testTenantID(), session.ID, "0xfullac", 19)
	engine.HandleCreditResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	// Should be SETTLING now
	s, _ := store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusSettling {
		t.Fatalf("expected SETTLING, got %s", s.Status)
	}

	// Complete settlement
	engine.HandleSettlementResult(ctx, testTenantID(), session.ID, domain.IntentResult{Success: true})

	s, _ = store.GetSession(ctx, testTenantID(), session.ID)
	if s.Status != domain.DepositSessionStatusSettled {
		t.Errorf("expected SETTLED, got %s", s.Status)
	}
}
