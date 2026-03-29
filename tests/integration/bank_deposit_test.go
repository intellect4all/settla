//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	bankdeposit "github.com/intellect4all/settla/core/bankdeposit"
	"github.com/intellect4all/settla/domain"
)

// ── Mock Store ──────────────────────────────────────────────────────────────

type mockBDStore struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*domain.BankDepositSession
	txs      map[string]*domain.BankDepositTransaction
	pool     []domain.VirtualAccountPool
	index    map[string]*bankdeposit.VirtualAccountIndex
}

func newMockBDStore() *mockBDStore {
	return &mockBDStore{
		sessions: make(map[uuid.UUID]*domain.BankDepositSession),
		txs:      make(map[string]*domain.BankDepositTransaction),
		index:    make(map[string]*bankdeposit.VirtualAccountIndex),
	}
}

func (s *mockBDStore) CreateSessionWithOutbox(_ context.Context, session *domain.BankDepositSession, _ []domain.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *session
	s.sessions[session.ID] = &cp
	return nil
}

func (s *mockBDStore) GetSession(_ context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.TenantID != tenantID {
		return nil, fmt.Errorf("session not found")
	}
	cp := *sess
	return &cp, nil
}

func (s *mockBDStore) GetSessionByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key string) (*domain.BankDepositSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.TenantID == tenantID && sess.IdempotencyKey == key {
			cp := *sess
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("session not found")
}

func (s *mockBDStore) GetSessionByAccountNumber(_ context.Context, accountNumber string) (*domain.BankDepositSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.AccountNumber == accountNumber && sess.Status == domain.BankDepositSessionStatusPendingPayment {
			cp := *sess
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("session not found")
}

func (s *mockBDStore) ListSessions(_ context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.BankDepositSession
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

func (s *mockBDStore) TransitionWithOutbox(_ context.Context, session *domain.BankDepositSession, _ []domain.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.sessions[session.ID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	if existing.Version != session.Version-1 {
		return bankdeposit.ErrOptimisticLock
	}
	cp := *session
	s.sessions[session.ID] = &cp
	return nil
}

func (s *mockBDStore) DispenseVirtualAccount(_ context.Context, tenantID uuid.UUID, currency string) (*domain.VirtualAccountPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pool {
		if s.pool[i].TenantID == tenantID && string(s.pool[i].Currency) == currency && s.pool[i].Available {
			s.pool[i].Available = false
			cp := s.pool[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *mockBDStore) RecycleVirtualAccount(_ context.Context, accountNumber string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pool {
		if s.pool[i].AccountNumber == accountNumber {
			s.pool[i].Available = true
			return nil
		}
	}
	return nil
}

func (s *mockBDStore) CreateBankDepositTx(_ context.Context, tx *domain.BankDepositTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs[tx.BankReference] = tx
	return nil
}

func (s *mockBDStore) GetBankDepositTxByRef(_ context.Context, bankReference string) (*domain.BankDepositTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.txs[bankReference]
	if !ok {
		return nil, fmt.Errorf("tx not found")
	}
	return tx, nil
}

func (s *mockBDStore) ListSessionTxs(_ context.Context, sessionID uuid.UUID) ([]domain.BankDepositTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.BankDepositTransaction
	for _, tx := range s.txs {
		if tx.SessionID == sessionID {
			result = append(result, *tx)
		}
	}
	return result, nil
}

func (s *mockBDStore) AccumulateReceived(_ context.Context, _, sessionID uuid.UUID, amount decimal.Decimal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	sess.ReceivedAmount = sess.ReceivedAmount.Add(amount)
	return nil
}

func (s *mockBDStore) GetExpiredPendingSessions(_ context.Context, limit int) ([]domain.BankDepositSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var result []domain.BankDepositSession
	for _, sess := range s.sessions {
		if sess.Status == domain.BankDepositSessionStatusPendingPayment && sess.ExpiresAt.Before(now) {
			result = append(result, *sess)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *mockBDStore) ListVirtualAccountsByTenant(_ context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.VirtualAccountPool
	for _, a := range s.pool {
		if a.TenantID == tenantID {
			result = append(result, a)
		}
	}
	return result, nil
}

func (s *mockBDStore) ListVirtualAccountsPaginated(_ context.Context, params bankdeposit.VirtualAccountListParams) ([]domain.VirtualAccountPool, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var filtered []domain.VirtualAccountPool
	for _, a := range s.pool {
		if a.TenantID != params.TenantID {
			continue
		}
		if params.Currency != "" && string(a.Currency) != params.Currency {
			continue
		}
		if params.AccountType != "" && string(a.AccountType) != params.AccountType {
			continue
		}
		filtered = append(filtered, a)
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

func (s *mockBDStore) CountAvailableVirtualAccountsByCurrency(_ context.Context, tenantID uuid.UUID) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]int64)
	for _, a := range s.pool {
		if a.TenantID == tenantID && a.Available {
			result[string(a.Currency)]++
		}
	}
	return result, nil
}

func (s *mockBDStore) GetVirtualAccountIndexByNumber(_ context.Context, accountNumber string) (*bankdeposit.VirtualAccountIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[accountNumber]
	if !ok {
		return nil, fmt.Errorf("account index not found")
	}
	return idx, nil
}

// ── Mock Tenant Store ───────────────────────────────────────────────────────

type mockBDTenantStore struct{}

func (s *mockBDTenantStore) GetTenant(_ context.Context, _ uuid.UUID) (*domain.Tenant, error) {
	return &domain.Tenant{
		ID:        uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		Name:      "Test Tenant",
		Slug:      "test-tenant",
		Status:    domain.TenantStatusActive,
		KYBStatus: domain.KYBStatusVerified,
		FeeSchedule: domain.FeeSchedule{
			BankCollectionBPS: 25,
		},
		BankConfig: domain.TenantBankConfig{
			BankDepositsEnabled:     true,
			BankSupportedCurrencies: []string{"GBP", "EUR"},
			DefaultMismatchPolicy:   domain.PaymentMismatchPolicyReject,
			DefaultSessionTTLSecs:   3600,
		},
	}, nil
}

func seedBDPool(store *mockBDStore, tenantID uuid.UUID, count int) {
	for i := 0; i < count; i++ {
		store.pool = append(store.pool, domain.VirtualAccountPool{
			ID:               uuid.Must(uuid.NewV7()),
			TenantID:         tenantID,
			BankingPartnerID: "mock-partner",
			AccountNumber:    fmt.Sprintf("VA%010d", i+1),
			AccountName:      "Settla Virtual Account",
			SortCode:         "040004",
			IBAN:             fmt.Sprintf("GB29NWBK6016%010d", i+1),
			Currency:         domain.CurrencyGBP,
			AccountType:      domain.VirtualAccountTypeTemporary,
			Available:        true,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		})
	}
}

func TestBankDeposit_FullLifecycle(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 5)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	// 1. Create session
	session, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		IdempotencyKey: "test-lifecycle-001",
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(100.00),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.Status != domain.BankDepositSessionStatusPendingPayment {
		t.Fatalf("expected PENDING_PAYMENT, got %s", session.Status)
	}

	// 2. Receive bank credit
	credit := domain.IncomingBankCredit{
		AccountNumber: session.AccountNumber,
		Amount:        decimal.NewFromFloat(100.00),
		Currency:      domain.CurrencyGBP,
		PayerName:     "John Doe",
		BankReference: "REF-001",
		ReceivedAt:    time.Now().UTC(),
	}
	if err := engine.HandleBankCreditReceived(ctx, tenantID, session.ID, credit); err != nil {
		t.Fatalf("HandleBankCreditReceived: %v", err)
	}

	// 3. Verify CREDITING state
	updated, _ := engine.GetSession(ctx, tenantID, session.ID)
	if updated.Status != domain.BankDepositSessionStatusCrediting {
		t.Fatalf("expected CREDITING, got %s", updated.Status)
	}

	// 4. Handle credit result (success)
	if err := engine.HandleCreditResult(ctx, tenantID, session.ID, domain.IntentResult{Success: true}); err != nil {
		t.Fatalf("HandleCreditResult: %v", err)
	}

	updated, _ = engine.GetSession(ctx, tenantID, session.ID)
	if updated.Status != domain.BankDepositSessionStatusHeld {
		t.Fatalf("expected HELD (default settlement pref), got %s", updated.Status)
	}
}

func TestBankDeposit_Idempotency(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 5)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	s1, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		IdempotencyKey: "idem-001",
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(50.00),
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	s2, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		IdempotencyKey: "idem-001",
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(50.00),
	})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}

	if s1.ID != s2.ID {
		t.Fatalf("idempotency failed: got different session IDs %s and %s", s1.ID, s2.ID)
	}
}

func TestBankDeposit_MismatchReject(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 5)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(100.00),
		MismatchPolicy: domain.PaymentMismatchPolicyReject,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	credit := domain.IncomingBankCredit{
		AccountNumber: session.AccountNumber,
		Amount:        decimal.NewFromFloat(50.00),
		Currency:      domain.CurrencyGBP,
		BankReference: "REF-UNDER",
		ReceivedAt:    time.Now().UTC(),
	}
	if err := engine.HandleBankCreditReceived(ctx, tenantID, session.ID, credit); err != nil {
		t.Fatalf("HandleBankCreditReceived: %v", err)
	}

	updated, _ := engine.GetSession(ctx, tenantID, session.ID)
	if updated.Status != domain.BankDepositSessionStatusFailed {
		t.Fatalf("expected FAILED after REJECT underpayment, got %s", updated.Status)
	}
}

func TestBankDeposit_MismatchAccept(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 5)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(100.00),
		MismatchPolicy: domain.PaymentMismatchPolicyAccept,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	credit := domain.IncomingBankCredit{
		AccountNumber: session.AccountNumber,
		Amount:        decimal.NewFromFloat(50.00),
		Currency:      domain.CurrencyGBP,
		BankReference: "REF-ACCEPT",
		ReceivedAt:    time.Now().UTC(),
	}
	if err := engine.HandleBankCreditReceived(ctx, tenantID, session.ID, credit); err != nil {
		t.Fatalf("HandleBankCreditReceived: %v", err)
	}

	updated, _ := engine.GetSession(ctx, tenantID, session.ID)
	if updated.Status != domain.BankDepositSessionStatusCrediting {
		t.Fatalf("expected CREDITING after ACCEPT underpayment, got %s", updated.Status)
	}
}

func TestBankDeposit_ExpiryAndRecycle(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 5)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	session, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
		Currency:       "GBP",
		ExpectedAmount: decimal.NewFromFloat(100.00),
		TTLSeconds:     1,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	time.Sleep(2 * time.Second)

	if err := engine.ExpireSession(ctx, tenantID, session.ID); err != nil {
		t.Fatalf("ExpireSession: %v", err)
	}

	updated, _ := engine.GetSession(ctx, tenantID, session.ID)
	if updated.Status != domain.BankDepositSessionStatusExpired {
		t.Fatalf("expected EXPIRED, got %s", updated.Status)
	}
}

func TestBankDeposit_ConcurrentDispense(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()
	seedBDPool(store, tenantID, 3)

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := engine.CreateSession(ctx, tenantID, bankdeposit.CreateSessionRequest{
				IdempotencyKey: fmt.Sprintf("concurrent-%d", i),
				Currency:       "GBP",
				ExpectedAmount: decimal.NewFromFloat(100.00),
			})
			results <- err
		}(i)
	}

	wg.Wait()
	close(results)

	var successes, failures int
	for err := range results {
		if err != nil {
			failures++
		} else {
			successes++
		}
	}

	if successes != 3 {
		t.Fatalf("expected 3 successes with 3 pool accounts, got %d (failures: %d)", successes, failures)
	}
}

func TestBankDeposit_PermanentAccountAutoSession(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	store := newMockBDStore()

	engine := bankdeposit.NewEngine(store, &mockBDTenantStore{}, slog.Default())
	ctx := context.Background()

	credit := domain.IncomingBankCredit{
		AccountNumber: "PERM-001",
		Amount:        decimal.NewFromFloat(250.00),
		Currency:      domain.CurrencyGBP,
		PayerName:     "Jane Doe",
		BankReference: "PERM-REF-001",
		ReceivedAt:    time.Now().UTC(),
	}

	session, err := engine.CreateSessionForPermanentAccount(ctx, tenantID, "PERM-001", "partner-001", credit)
	if err != nil {
		t.Fatalf("CreateSessionForPermanentAccount: %v", err)
	}

	if session.AccountType != domain.VirtualAccountTypePermanent {
		t.Fatalf("expected PERMANENT account type, got %s", session.AccountType)
	}
	if session.Status != domain.BankDepositSessionStatusPendingPayment {
		t.Fatalf("expected PENDING_PAYMENT, got %s", session.Status)
	}

	// Verify idempotency
	s2, err := engine.CreateSessionForPermanentAccount(ctx, tenantID, "PERM-001", "partner-001", credit)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if s2.ID != session.ID {
		t.Fatalf("idempotency failed for permanent account session")
	}
}
