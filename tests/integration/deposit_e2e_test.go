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

	depositcore "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// ─── In-Memory Deposit Store ────────────────────────────────────────────────

type memDepositStore struct {
	mu               sync.RWMutex
	sessions         map[uuid.UUID]*domain.DepositSession
	sessionsByAddr   map[string]uuid.UUID
	sessionsByIdemKey map[string]uuid.UUID // key: "tenantID:idempotencyKey"
	txs              map[string]*domain.DepositTransaction // key: "chain:txHash"
	sessionTxs       map[uuid.UUID][]domain.DepositTransaction
	outboxEntries    []domain.OutboxEntry
	addresses        []domain.CryptoAddressPool // pre-seeded pool
}

var _ depositcore.DepositStore = (*memDepositStore)(nil)

func newMemDepositStore(addresses []domain.CryptoAddressPool) *memDepositStore {
	return &memDepositStore{
		sessions:          make(map[uuid.UUID]*domain.DepositSession),
		sessionsByAddr:    make(map[string]uuid.UUID),
		sessionsByIdemKey: make(map[string]uuid.UUID),
		txs:               make(map[string]*domain.DepositTransaction),
		sessionTxs:        make(map[uuid.UUID][]domain.DepositTransaction),
		addresses:         addresses,
	}
}

func (s *memDepositStore) CreateSessionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check idempotency
	if session.IdempotencyKey != "" {
		key := fmt.Sprintf("%s:%s", session.TenantID, session.IdempotencyKey)
		if _, exists := s.sessionsByIdemKey[key]; exists {
			return fmt.Errorf("duplicate idempotency key %s for tenant %s", session.IdempotencyKey, session.TenantID)
		}
	}

	s.sessions[session.ID] = session
	s.sessionsByAddr[session.DepositAddress] = session.ID
	if session.IdempotencyKey != "" {
		key := fmt.Sprintf("%s:%s", session.TenantID, session.IdempotencyKey)
		s.sessionsByIdemKey[key] = session.ID
	}
	s.outboxEntries = append(s.outboxEntries, entries...)
	return nil
}

func (s *memDepositStore) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("settla-deposit: session %s not found", sessionID)
	}
	if session.TenantID != tenantID {
		return nil, fmt.Errorf("settla-deposit: session %s not found for tenant %s", sessionID, tenantID)
	}
	// Return a copy to avoid data races on caller mutations
	cp := *session
	return &cp, nil
}

func (s *memDepositStore) GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessionID, ok := s.sessionsByAddr[address]
	if !ok {
		return nil, fmt.Errorf("settla-deposit: no session for address %s", address)
	}
	session := s.sessions[sessionID]
	cp := *session
	return &cp, nil
}

func (s *memDepositStore) GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ikey := fmt.Sprintf("%s:%s", tenantID, key)
	sessionID, ok := s.sessionsByIdemKey[ikey]
	if !ok {
		return nil, fmt.Errorf("settla-deposit: session not found for idempotency key %s", key)
	}
	session := s.sessions[sessionID]
	cp := *session
	return &cp, nil
}

func (s *memDepositStore) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []domain.DepositSession
	for _, sess := range s.sessions {
		if sess.TenantID == tenantID {
			result = append(result, *sess)
		}
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (s *memDepositStore) TransitionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.sessions[session.ID]
	if !ok {
		return fmt.Errorf("settla-deposit: session %s not found", session.ID)
	}
	// Optimistic lock: the session.Version was already incremented by TransitionTo,
	// so the expected previous version is session.Version - 1.
	if existing.Version != session.Version-1 {
		return depositcore.ErrOptimisticLock
	}

	s.sessions[session.ID] = session
	s.outboxEntries = append(s.outboxEntries, entries...)
	return nil
}

func (s *memDepositStore) DispenseAddress(ctx context.Context, tenantID uuid.UUID, chain string, sessionID uuid.UUID) (*domain.CryptoAddressPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, addr := range s.addresses {
		if !addr.Dispensed && addr.Chain == chain {
			now := time.Now().UTC()
			s.addresses[i].Dispensed = true
			s.addresses[i].DispensedAt = &now
			s.addresses[i].TenantID = tenantID
			if sessionID != uuid.Nil {
				s.addresses[i].SessionID = &sessionID
			}
			return &s.addresses[i], nil
		}
	}
	return nil, fmt.Errorf("settla-deposit: address pool empty for chain %s", chain)
}

func (s *memDepositStore) CreateDepositTx(ctx context.Context, tx *domain.DepositTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s:%s", tx.Chain, tx.TxHash)
	s.txs[key] = tx
	s.sessionTxs[tx.SessionID] = append(s.sessionTxs[tx.SessionID], *tx)
	return nil
}

func (s *memDepositStore) GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", chain, txHash)
	tx, ok := s.txs[key]
	if !ok {
		return nil, fmt.Errorf("settla-deposit: tx %s not found", key)
	}
	return tx, nil
}

func (s *memDepositStore) ListSessionTxs(ctx context.Context, sessionID uuid.UUID) ([]domain.DepositTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.sessionTxs[sessionID], nil
}

func (s *memDepositStore) AccumulateReceived(ctx context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("settla-deposit: session %s not found", sessionID)
	}
	if session.TenantID != tenantID {
		return fmt.Errorf("settla-deposit: session %s not found for tenant %s", sessionID, tenantID)
	}
	session.ReceivedAmount = session.ReceivedAmount.Add(amount)
	return nil
}

func (s *memDepositStore) GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()
	var result []domain.DepositSession
	for _, sess := range s.sessions {
		if sess.Status == domain.DepositSessionStatusPendingPayment && sess.ExpiresAt.Before(now) {
			result = append(result, *sess)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *memDepositStore) GetSessionByIDOnly(_ context.Context, sessionID uuid.UUID) (*domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("settla-deposit: session %s not found", sessionID)
	}
	cp := *session
	return &cp, nil
}

func (s *memDepositStore) GetSessionByTxHash(_ context.Context, tenantID uuid.UUID, chain, txHash string) (*domain.DepositSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := chain + ":" + txHash
	tx, ok := s.txs[key]
	if !ok {
		return nil, fmt.Errorf("tx %s not found", key)
	}
	sess, ok := s.sessions[tx.SessionID]
	if !ok || sess.TenantID != tenantID {
		return nil, fmt.Errorf("session not found for tx %s", key)
	}
	cp := *sess
	return &cp, nil
}

// drainOutbox returns and clears all accumulated outbox entries.
func (s *memDepositStore) drainOutbox() []domain.OutboxEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.outboxEntries
	s.outboxEntries = nil
	return entries
}

// getSessionDirect returns the session without copying (for assertions on final state).
func (s *memDepositStore) getSessionDirect(sessionID uuid.UUID) *domain.DepositSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// ─── Deposit Test Harness ───────────────────────────────────────────────────

type depositTestHarness struct {
	Engine       *depositcore.Engine
	DepositStore *memDepositStore
	TenantStore  *memTenantStore
}

func newDepositTestHarness(t *testing.T) *depositTestHarness {
	t.Helper()

	logger := observability.NewLogger("settla-deposit-integration-test", "test")

	// Pre-seed address pool with multiple addresses
	addresses := make([]domain.CryptoAddressPool, 10)
	for i := range addresses {
		addresses[i] = domain.CryptoAddressPool{
			ID:              uuid.New(),
			Chain:           "tron",
			Address:         fmt.Sprintf("TAddr%04d", i+1),
			DerivationIndex: int64(i),
			Dispensed:       false,
			CreatedAt:       time.Now().UTC(),
		}
	}

	depositStore := newMemDepositStore(addresses)
	tenantStore := newMemTenantStore()

	// Seed tenant with crypto config
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
			OnRampBPS:              40,
			OffRampBPS:             35,
			CryptoCollectionBPS:    30,
			CryptoCollectionMaxFeeUSD: decimal.NewFromInt(50),
		},
		DailyLimitUSD:    decimal.NewFromInt(10_000_000),
		PerTransferLimit: decimal.NewFromInt(1_000_000),
		CryptoConfig: domain.TenantCryptoConfig{
			CryptoEnabled:         true,
			DefaultSettlementPref: domain.SettlementPreferenceAutoConvert,
			SupportedChains:       []string{"tron"},
			MinConfirmationsTron:  19,
			PaymentToleranceBPS:   50,
			DefaultSessionTTLSecs: 3600,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	engine := depositcore.NewEngine(depositStore, tenantStore, logger)

	return &depositTestHarness{
		Engine:       engine,
		DepositStore: depositStore,
		TenantStore:  tenantStore,
	}
}

// ─── Helper: check outbox for event type ────────────────────────────────────

func outboxContains(entries []domain.OutboxEntry, eventType string) bool {
	for _, e := range entries {
		if e.EventType == eventType {
			return true
		}
	}
	return false
}

func outboxIntentCount(entries []domain.OutboxEntry, eventType string) int {
	count := 0
	for _, e := range entries {
		if e.EventType == eventType && e.IsIntent {
			count++
		}
	}
	return count
}

func outboxEventCount(entries []domain.OutboxEntry, eventType string) int {
	count := 0
	for _, e := range entries {
		if e.EventType == eventType && !e.IsIntent {
			count++
		}
	}
	return count
}

// ─── Test: Happy Path ───────────────────────────────────────────────────────

func TestDepositE2E_HappyPath(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	// 1. Create session (PENDING_PAYMENT)
	req := depositcore.CreateSessionRequest{
		IdempotencyKey: "happy-path-1",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
		TTLSeconds:     3600,
	}

	session, err := h.Engine.CreateSession(ctx, LemfiTenantID, req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.Status != domain.DepositSessionStatusPendingPayment {
		t.Fatalf("expected PENDING_PAYMENT, got %s", session.Status)
	}
	if session.DepositAddress == "" {
		t.Fatal("expected deposit address to be assigned")
	}

	// Verify outbox entries from creation: monitor intent, session created event, webhook, email
	entries := h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.IntentMonitorAddress) {
		t.Error("expected IntentMonitorAddress in outbox after CreateSession")
	}
	if !outboxContains(entries, domain.EventDepositSessionCreated) {
		t.Error("expected EventDepositSessionCreated in outbox after CreateSession")
	}
	if !outboxContains(entries, domain.IntentWebhookDeliver) {
		t.Error("expected IntentWebhookDeliver in outbox after CreateSession")
	}
	if !outboxContains(entries, domain.IntentEmailNotify) {
		t.Error("expected IntentEmailNotify in outbox after CreateSession")
	}

	// 2. HandleTransactionDetected (→ DETECTED)
	incomingTx := domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xabc123",
		FromAddress: "TSenderAddr",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(100),
		BlockNumber: 50_000_000,
		BlockHash:   "0xblockhash1",
		Timestamp:   time.Now().UTC(),
	}

	err = h.Engine.HandleTransactionDetected(ctx, LemfiTenantID, session.ID, incomingTx)
	if err != nil {
		t.Fatalf("HandleTransactionDetected: %v", err)
	}

	// Reload session
	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after detected: %v", err)
	}
	if session.Status != domain.DepositSessionStatusDetected {
		t.Fatalf("expected DETECTED, got %s", session.Status)
	}
	if session.DetectedAt == nil {
		t.Error("expected DetectedAt to be set")
	}

	entries = h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositTxDetected) {
		t.Error("expected EventDepositTxDetected in outbox after HandleTransactionDetected")
	}
	if !outboxContains(entries, domain.IntentWebhookDeliver) {
		t.Error("expected IntentWebhookDeliver in outbox after HandleTransactionDetected")
	}

	// 3. HandleTransactionConfirmed (→ CONFIRMED → CREDITING)
	err = h.Engine.HandleTransactionConfirmed(ctx, LemfiTenantID, session.ID, "0xabc123", 19)
	if err != nil {
		t.Fatalf("HandleTransactionConfirmed: %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after confirmed: %v", err)
	}
	if session.Status != domain.DepositSessionStatusCrediting {
		t.Fatalf("expected CREDITING, got %s", session.Status)
	}
	if session.ConfirmedAt == nil {
		t.Error("expected ConfirmedAt to be set")
	}

	entries = h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositTxConfirmed) {
		t.Error("expected EventDepositTxConfirmed in outbox after HandleTransactionConfirmed")
	}
	if outboxIntentCount(entries, domain.IntentCreditDeposit) != 1 {
		t.Error("expected exactly one IntentCreditDeposit in outbox")
	}

	// 4. HandleCreditResult with Success=true (→ CREDITED → SETTLING)
	err = h.Engine.HandleCreditResult(ctx, LemfiTenantID, session.ID, domain.IntentResult{
		Success: true,
	})
	if err != nil {
		t.Fatalf("HandleCreditResult: %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after credit result: %v", err)
	}
	if session.Status != domain.DepositSessionStatusSettling {
		t.Fatalf("expected SETTLING (auto-convert), got %s", session.Status)
	}
	if session.CreditedAt == nil {
		t.Error("expected CreditedAt to be set")
	}
	if !session.FeeAmount.IsPositive() {
		t.Error("expected positive fee amount after credit")
	}
	if !session.NetAmount.IsPositive() {
		t.Error("expected positive net amount after credit")
	}

	entries = h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositSessionCredited) {
		t.Error("expected EventDepositSessionCredited in outbox after HandleCreditResult")
	}
	if outboxIntentCount(entries, domain.IntentSettleDeposit) != 1 {
		t.Error("expected exactly one IntentSettleDeposit in outbox")
	}

	// 5. HandleSettlementResult with Success=true (→ SETTLED)
	err = h.Engine.HandleSettlementResult(ctx, LemfiTenantID, session.ID, domain.IntentResult{
		Success: true,
	})
	if err != nil {
		t.Fatalf("HandleSettlementResult: %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after settlement result: %v", err)
	}
	if session.Status != domain.DepositSessionStatusSettled {
		t.Fatalf("expected SETTLED, got %s", session.Status)
	}
	if session.SettledAt == nil {
		t.Error("expected SettledAt to be set")
	}

	entries = h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositSessionSettled) {
		t.Error("expected EventDepositSessionSettled in outbox after HandleSettlementResult")
	}

	// Verify all timestamps populated on the final session
	final := h.DepositStore.getSessionDirect(session.ID)
	if final.CreatedAt.IsZero() {
		t.Error("CreatedAt should be populated")
	}
	if final.DetectedAt == nil {
		t.Error("DetectedAt should be populated")
	}
	if final.ConfirmedAt == nil {
		t.Error("ConfirmedAt should be populated")
	}
	if final.CreditedAt == nil {
		t.Error("CreditedAt should be populated")
	}
	if final.SettledAt == nil {
		t.Error("SettledAt should be populated")
	}
	if final.IsTerminal() {
		// SETTLED is not terminal per the domain model (SETTLED, HELD, FAILED, CANCELLED are)
		// Actually checking the code: SETTLED is terminal. Good.
	}
}

// ─── Test: Expiry ───────────────────────────────────────────────────────────

func TestDepositE2E_Expiry(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	session, err := h.Engine.CreateSession(ctx, LemfiTenantID, depositcore.CreateSessionRequest{
		IdempotencyKey: "expiry-1",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(50),
		TTLSeconds:     3600,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	h.DepositStore.drainOutbox() // clear creation outbox

	// Expire the session
	err = h.Engine.ExpireSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("ExpireSession: %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after expiry: %v", err)
	}
	if session.Status != domain.DepositSessionStatusExpired {
		t.Fatalf("expected EXPIRED, got %s", session.Status)
	}
	if session.ExpiredAt == nil {
		t.Error("expected ExpiredAt to be set")
	}

	entries := h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositSessionExpired) {
		t.Error("expected EventDepositSessionExpired in outbox")
	}
}

// ─── Test: Cancellation ─────────────────────────────────────────────────────

func TestDepositE2E_Cancellation(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	session, err := h.Engine.CreateSession(ctx, LemfiTenantID, depositcore.CreateSessionRequest{
		IdempotencyKey: "cancel-1",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(75),
		TTLSeconds:     3600,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	h.DepositStore.drainOutbox()

	// Cancel before payment
	err = h.Engine.CancelSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("CancelSession: %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after cancel: %v", err)
	}
	if session.Status != domain.DepositSessionStatusCancelled {
		t.Fatalf("expected CANCELLED, got %s", session.Status)
	}

	entries := h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.EventDepositSessionCancelled) {
		t.Error("expected EventDepositSessionCancelled in outbox")
	}
}

// ─── Test: Underpayment within tolerance ────────────────────────────────────

func TestDepositE2E_Underpayment(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	// Create session with expected_amount=100
	session, err := h.Engine.CreateSession(ctx, LemfiTenantID, depositcore.CreateSessionRequest{
		IdempotencyKey: "underpay-1",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
		TTLSeconds:     3600,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	h.DepositStore.drainOutbox()

	// Detect with 99.5 (within default 50bps = 0.5% tolerance)
	incomingTx := domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xunderpay1",
		FromAddress: "TSenderAddr",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromFloat(99.5),
		BlockNumber: 50_000_001,
		BlockHash:   "0xblockhash_under",
		Timestamp:   time.Now().UTC(),
	}

	err = h.Engine.HandleTransactionDetected(ctx, LemfiTenantID, session.ID, incomingTx)
	if err != nil {
		t.Fatalf("HandleTransactionDetected (underpayment): %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after detection: %v", err)
	}
	if session.Status != domain.DepositSessionStatusDetected {
		t.Fatalf("expected DETECTED even with slight underpayment, got %s", session.Status)
	}
	h.DepositStore.drainOutbox()

	// Confirm transaction — should proceed to CREDITING
	err = h.Engine.HandleTransactionConfirmed(ctx, LemfiTenantID, session.ID, "0xunderpay1", 19)
	if err != nil {
		t.Fatalf("HandleTransactionConfirmed (underpayment): %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after confirmed: %v", err)
	}
	if session.Status != domain.DepositSessionStatusCrediting {
		t.Fatalf("expected CREDITING, got %s", session.Status)
	}
	h.DepositStore.drainOutbox()

	// Credit succeeds
	err = h.Engine.HandleCreditResult(ctx, LemfiTenantID, session.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleCreditResult (underpayment): %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after credit: %v", err)
	}
	if session.Status != domain.DepositSessionStatusSettling {
		t.Fatalf("expected SETTLING, got %s", session.Status)
	}
	h.DepositStore.drainOutbox()

	// Settlement succeeds
	err = h.Engine.HandleSettlementResult(ctx, LemfiTenantID, session.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleSettlementResult (underpayment): %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession final: %v", err)
	}
	if session.Status != domain.DepositSessionStatusSettled {
		t.Fatalf("expected SETTLED, got %s", session.Status)
	}
}

// ─── Test: Idempotent Creation ──────────────────────────────────────────────

func TestDepositE2E_IdempotentCreation(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	req := depositcore.CreateSessionRequest{
		IdempotencyKey: "idem-test-key",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(200),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
		TTLSeconds:     3600,
	}

	// First creation
	session1, err := h.Engine.CreateSession(ctx, LemfiTenantID, req)
	if err != nil {
		t.Fatalf("CreateSession (first): %v", err)
	}
	h.DepositStore.drainOutbox()

	// Second creation with same idempotency key — should return the same session
	session2, err := h.Engine.CreateSession(ctx, LemfiTenantID, req)
	if err != nil {
		t.Fatalf("CreateSession (second): %v", err)
	}

	if session1.ID != session2.ID {
		t.Fatalf("expected same session ID on idempotent create: got %s and %s", session1.ID, session2.ID)
	}
	if session1.DepositAddress != session2.DepositAddress {
		t.Fatalf("expected same deposit address on idempotent create: got %s and %s", session1.DepositAddress, session2.DepositAddress)
	}

	// Verify no additional outbox entries were created for the idempotent call
	entries := h.DepositStore.drainOutbox()
	if len(entries) != 0 {
		t.Errorf("expected no new outbox entries for idempotent creation, got %d", len(entries))
	}
}

// ─── Test: Concurrent Detections (multiple txs for same session) ────────────

func TestDepositE2E_ConcurrentDetections(t *testing.T) {
	h := newDepositTestHarness(t)
	ctx := context.Background()

	session, err := h.Engine.CreateSession(ctx, LemfiTenantID, depositcore.CreateSessionRequest{
		IdempotencyKey: "concurrent-1",
		Chain:          "tron",
		Token:          "USDT",
		ExpectedAmount: decimal.NewFromInt(100),
		SettlementPref: domain.SettlementPreferenceAutoConvert,
		TTLSeconds:     3600,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	h.DepositStore.drainOutbox()

	// First transaction detected — transitions to DETECTED
	tx1 := domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xtx1_concurrent",
		FromAddress: "TSenderAddr1",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(60),
		BlockNumber: 50_000_010,
		BlockHash:   "0xblock_c1",
		Timestamp:   time.Now().UTC(),
	}

	err = h.Engine.HandleTransactionDetected(ctx, LemfiTenantID, session.ID, tx1)
	if err != nil {
		t.Fatalf("HandleTransactionDetected (tx1): %v", err)
	}

	session, err = h.Engine.GetSession(ctx, LemfiTenantID, session.ID)
	if err != nil {
		t.Fatalf("GetSession after tx1: %v", err)
	}
	if session.Status != domain.DepositSessionStatusDetected {
		t.Fatalf("expected DETECTED after tx1, got %s", session.Status)
	}
	h.DepositStore.drainOutbox()

	// Second transaction detected — session already DETECTED, should just record tx
	tx2 := domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xtx2_concurrent",
		FromAddress: "TSenderAddr2",
		ToAddress:   session.DepositAddress,
		Amount:      decimal.NewFromInt(40),
		BlockNumber: 50_000_011,
		BlockHash:   "0xblock_c2",
		Timestamp:   time.Now().UTC(),
	}

	err = h.Engine.HandleTransactionDetected(ctx, LemfiTenantID, session.ID, tx2)
	if err != nil {
		t.Fatalf("HandleTransactionDetected (tx2): %v", err)
	}

	// Verify both transactions are recorded
	txs, err := h.DepositStore.ListSessionTxs(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListSessionTxs: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("expected 2 transactions recorded, got %d", len(txs))
	}

	// Verify received_amount accumulated both txs via AccumulateReceived
	// The store accumulates directly, and the engine also sets ReceivedAmount on the session
	// for the first transition. Check the store's version.
	storedSession := h.DepositStore.getSessionDirect(session.ID)
	// AccumulateReceived was called for both tx1 (60) and tx2 (40).
	// The engine also sets session.ReceivedAmount for the first tx during TransitionTo.
	// Due to the engine calling AccumulateReceived AND setting ReceivedAmount on the session object,
	// the store-level ReceivedAmount = 60 (AccumulateReceived tx1) + 40 (AccumulateReceived tx2) = 100
	// But the session object written via TransitionWithOutbox also had ReceivedAmount = 60.
	// After tx2, AccumulateReceived adds 40 → total in store = 100. But the session object
	// from TransitionWithOutbox was not re-written for tx2 (already past DETECTED).
	// So storedSession.ReceivedAmount should be 100 from AccumulateReceived calls.
	expectedTotal := decimal.NewFromInt(100)
	if !storedSession.ReceivedAmount.Equal(expectedTotal) {
		t.Errorf("expected accumulated received_amount of %s, got %s", expectedTotal, storedSession.ReceivedAmount)
	}
}
