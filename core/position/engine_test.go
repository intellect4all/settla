package position

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ── Mock Store ──────────────────────────────────────────────────────────────

type mockStore struct {
	transactions map[uuid.UUID]*domain.PositionTransaction
	lastOutbox   []domain.OutboxEntry
	failCreate   bool
}

func newMockStore() *mockStore {
	return &mockStore{
		transactions: make(map[uuid.UUID]*domain.PositionTransaction),
	}
}

func (s *mockStore) CreateWithOutbox(_ context.Context, tx *domain.PositionTransaction, entries []domain.OutboxEntry) error {
	if s.failCreate {
		return errMock
	}
	s.transactions[tx.ID] = tx
	s.lastOutbox = entries
	return nil
}

func (s *mockStore) UpdateStatus(_ context.Context, id, _ uuid.UUID, status domain.PositionTxStatus, reason string) error {
	tx, ok := s.transactions[id]
	if !ok {
		return errMock
	}
	tx.Status = status
	tx.FailureReason = reason
	return nil
}

func (s *mockStore) Get(_ context.Context, id, _ uuid.UUID) (*domain.PositionTransaction, error) {
	tx, ok := s.transactions[id]
	if !ok {
		return nil, errMock
	}
	return tx, nil
}

func (s *mockStore) ListByTenant(_ context.Context, _ uuid.UUID, _, _ int32) ([]domain.PositionTransaction, error) {
	var txns []domain.PositionTransaction
	for _, tx := range s.transactions {
		txns = append(txns, *tx)
	}
	return txns, nil
}

// ── Mock Treasury Reader ────────────────────────────────────────────────────

type mockTreasuryReader struct {
	positions map[string]*domain.Position
}

func (m *mockTreasuryReader) GetPosition(_ context.Context, tenantID uuid.UUID, currency domain.Currency, location string) (*domain.Position, error) {
	key := tenantID.String() + ":" + string(currency) + ":" + location
	pos, ok := m.positions[key]
	if !ok {
		return nil, domain.ErrInsufficientFunds(string(currency), location)
	}
	return pos, nil
}

var errMock = domain.ErrInsufficientFunds("MOCK", "mock")

// ── Helpers ─────────────────────────────────────────────────────────────────

func newTestEngine(store *mockStore, treasury *mockTreasuryReader) *Engine {
	logger := slog.Default()
	return NewEngine(store, treasury, logger)
}

func testTreasury(tenantID uuid.UUID) *mockTreasuryReader {
	return &mockTreasuryReader{
		positions: map[string]*domain.Position{
			tenantID.String() + ":GBP:bank:gbp": {
				ID:       uuid.New(),
				TenantID: tenantID,
				Currency: domain.CurrencyGBP,
				Location: "bank:gbp",
				Balance:  decimal.NewFromInt(10000),
				Locked:   decimal.Zero,
			},
			tenantID.String() + ":USDT:crypto:tron:usdt": {
				ID:       uuid.New(),
				TenantID: tenantID,
				Currency: domain.CurrencyUSDT,
				Location: "crypto:tron:usdt",
				Balance:  decimal.NewFromInt(5000),
				Locked:   decimal.NewFromInt(1000),
			},
		},
	}
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestRequestTopUp(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, err := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.NewFromInt(5000),
		Method:   "bank_transfer",
	})
	if err != nil {
		t.Fatalf("RequestTopUp: %v", err)
	}

	if tx.Status != domain.PositionTxStatusProcessing {
		t.Errorf("expected status PROCESSING, got %s", tx.Status)
	}
	if tx.Type != domain.PositionTxTopUp {
		t.Errorf("expected type TOP_UP, got %s", tx.Type)
	}
	if !tx.Amount.Equal(decimal.NewFromInt(5000)) {
		t.Errorf("expected amount 5000, got %s", tx.Amount)
	}
	if len(store.lastOutbox) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(store.lastOutbox))
	}
	if store.lastOutbox[0].EventType != domain.IntentPositionCredit {
		t.Errorf("expected IntentPositionCredit, got %s", store.lastOutbox[0].EventType)
	}
}

func TestRequestTopUp_InvalidAmount(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	_, err := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.Zero,
		Method:   "bank_transfer",
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

func TestRequestTopUp_PositionNotFound(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	_, err := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyNGN,
		Location: "bank:ngn",
		Amount:   decimal.NewFromInt(1000),
		Method:   "bank_transfer",
	})
	if err == nil {
		t.Error("expected error for non-existent position")
	}
}

func TestRequestWithdrawal(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, err := engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency:    domain.CurrencyGBP,
		Location:    "bank:gbp",
		Amount:      decimal.NewFromInt(5000),
		Method:      "bank_transfer",
		Destination: "GB82WEST12345698765432",
	})
	if err != nil {
		t.Fatalf("RequestWithdrawal: %v", err)
	}

	if tx.Status != domain.PositionTxStatusProcessing {
		t.Errorf("expected status PROCESSING, got %s", tx.Status)
	}
	if tx.Type != domain.PositionTxWithdrawal {
		t.Errorf("expected type WITHDRAWAL, got %s", tx.Type)
	}
	if tx.Destination != "GB82WEST12345698765432" {
		t.Errorf("expected destination set, got %s", tx.Destination)
	}
	if len(store.lastOutbox) != 1 {
		t.Fatalf("expected 1 outbox entry, got %d", len(store.lastOutbox))
	}
	if store.lastOutbox[0].EventType != domain.IntentPositionDebit {
		t.Errorf("expected IntentPositionDebit, got %s", store.lastOutbox[0].EventType)
	}
}

func TestRequestWithdrawal_InsufficientFunds(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	// USDT position has 5000 balance, 1000 locked → 4000 available
	_, err := engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency:    domain.CurrencyUSDT,
		Location:    "crypto:tron:usdt",
		Amount:      decimal.NewFromInt(4500), // more than 4000 available
		Method:      "crypto",
		Destination: "TXyz123...",
	})
	if err == nil {
		t.Error("expected error for withdrawal exceeding available balance")
	}
}

func TestRequestWithdrawal_MissingDestination(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	_, err := engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.NewFromInt(1000),
		Method:   "bank_transfer",
		// Destination intentionally empty
	})
	if err == nil {
		t.Error("expected error for missing destination")
	}
}

func TestHandleCreditResult_Success(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, _ := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.NewFromInt(1000),
		Method:   "bank_transfer",
	})

	err := engine.HandleCreditResult(ctx, tenantID, tx.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleCreditResult: %v", err)
	}

	updated, _ := store.Get(ctx, tx.ID, tenantID)
	if updated.Status != domain.PositionTxStatusCompleted {
		t.Errorf("expected COMPLETED, got %s", updated.Status)
	}
}

func TestHandleCreditResult_Failure(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, _ := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.NewFromInt(1000),
		Method:   "bank_transfer",
	})

	err := engine.HandleCreditResult(ctx, tenantID, tx.ID, domain.IntentResult{
		Success: false,
		Error:   "position not found",
	})
	if err != nil {
		t.Fatalf("HandleCreditResult: %v", err)
	}

	updated, _ := store.Get(ctx, tx.ID, tenantID)
	if updated.Status != domain.PositionTxStatusFailed {
		t.Errorf("expected FAILED, got %s", updated.Status)
	}
	if updated.FailureReason != "position not found" {
		t.Errorf("expected failure reason 'position not found', got %s", updated.FailureReason)
	}
}

func TestHandleDebitResult_Success(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, _ := engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency:    domain.CurrencyGBP,
		Location:    "bank:gbp",
		Amount:      decimal.NewFromInt(1000),
		Method:      "bank_transfer",
		Destination: "GB82WEST12345698765432",
	})

	err := engine.HandleDebitResult(ctx, tenantID, tx.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleDebitResult: %v", err)
	}

	updated, _ := store.Get(ctx, tx.ID, tenantID)
	if updated.Status != domain.PositionTxStatusCompleted {
		t.Errorf("expected COMPLETED, got %s", updated.Status)
	}
}

func TestHandleResult_IgnoresNonProcessing(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	tx, _ := engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP,
		Location: "bank:gbp",
		Amount:   decimal.NewFromInt(1000),
		Method:   "bank_transfer",
	})

	// Complete it first
	_ = engine.HandleCreditResult(ctx, tenantID, tx.ID, domain.IntentResult{Success: true})

	// Second call should be ignored (already COMPLETED)
	err := engine.HandleCreditResult(ctx, tenantID, tx.ID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("expected no error for duplicate result, got %v", err)
	}
}

func TestListTransactions(t *testing.T) {
	tenantID := uuid.New()
	store := newMockStore()
	engine := newTestEngine(store, testTreasury(tenantID))
	ctx := context.Background()

	// Create two transactions
	_, _ = engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: domain.CurrencyGBP, Location: "bank:gbp",
		Amount: decimal.NewFromInt(1000), Method: "bank_transfer",
	})
	_, _ = engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency: domain.CurrencyGBP, Location: "bank:gbp",
		Amount: decimal.NewFromInt(500), Method: "bank_transfer",
		Destination: "GB82WEST12345698765432",
	})

	txns, err := engine.ListTransactions(ctx, tenantID, 10, 0)
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(txns) != 2 {
		t.Errorf("expected 2 transactions, got %d", len(txns))
	}
}
