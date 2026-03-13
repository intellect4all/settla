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

// mockLedger records PostEntries and ReverseEntry calls.
type mockLedger struct {
	mu           sync.Mutex
	postCalls    []domain.JournalEntry
	reverseCalls []reverseCall
	failPost     bool
	failReverse  bool
}

type reverseCall struct {
	entryID uuid.UUID
	reason  string
}

func (m *mockLedger) PostEntries(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postCalls = append(m.postCalls, entry)
	if m.failPost {
		return nil, fmt.Errorf("ledger post failed")
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reverseCalls = append(m.reverseCalls, reverseCall{entryID: entryID, reason: reason})
	if m.failReverse {
		return nil, fmt.Errorf("ledger reverse failed")
	}
	return &domain.JournalEntry{}, nil
}

func (m *mockLedger) getPostCalls() []domain.JournalEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]domain.JournalEntry, len(m.postCalls))
	copy(cp, m.postCalls)
	return cp
}

func (m *mockLedger) getReverseCalls() []reverseCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]reverseCall, len(m.reverseCalls))
	copy(cp, m.reverseCalls)
	return cp
}

func ledgerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestLedgerWorker_PostSuccess(t *testing.T) {
	ledger := &mockLedger{}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.LedgerPostPayload{
		TransferID:     transferID,
		TenantID:       tenantID,
		IdempotencyKey: fmt.Sprintf("onramp:%s", transferID),
		Description:    "On-ramp transfer",
		ReferenceType:  "transfer",
		Lines: []domain.LedgerLineEntry{
			{
				AccountCode: "assets:crypto:usdt:tron",
				EntryType:   "DEBIT",
				Amount:      decimal.NewFromInt(950),
				Currency:    "GBP",
				Description: "Debit crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   "DEBIT",
				Amount:      decimal.NewFromInt(50),
				Currency:    "GBP",
				Description: "Debit on-ramp fee",
			},
			{
				AccountCode: "tenant:lemfi:assets:bank:gbp:clearing",
				EntryType:   "CREDIT",
				Amount:      decimal.NewFromInt(1000),
				Currency:    "GBP",
				Description: "Credit clearing account",
			},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentLedgerPost,
		Data:     &payload,
	}

	err := w.handlePost(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	posts := ledger.getPostCalls()
	if len(posts) != 1 {
		t.Fatalf("expected 1 post call, got %d", len(posts))
	}
	if posts[0].IdempotencyKey != fmt.Sprintf("onramp:%s", transferID) {
		t.Errorf("expected idempotency key onramp:%s, got %s", transferID, posts[0].IdempotencyKey)
	}
	if len(posts[0].Lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(posts[0].Lines))
	}
	if posts[0].ReferenceType != "transfer" {
		t.Errorf("expected reference type 'transfer', got %s", posts[0].ReferenceType)
	}
}

func TestLedgerWorker_PostFailure(t *testing.T) {
	ledger := &mockLedger{failPost: true}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.LedgerPostPayload{
		TransferID:     transferID,
		TenantID:       tenantID,
		IdempotencyKey: "test-key",
		Lines: []domain.LedgerLineEntry{
			{AccountCode: "a", EntryType: "DEBIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
			{AccountCode: "b", EntryType: "CREDIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentLedgerPost,
		Data:     &payload,
	}

	err := w.handlePost(context.Background(), event)
	if err == nil {
		t.Fatal("expected error from failed ledger post")
	}
}

func TestLedgerWorker_ReverseSuccess(t *testing.T) {
	ledger := &mockLedger{}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.LedgerPostPayload{
		TransferID:     transferID,
		TenantID:       tenantID,
		IdempotencyKey: fmt.Sprintf("reverse:%s", transferID),
		Description:    "Reverse settlement for transfer",
		ReferenceType:  "reversal",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentLedgerReverse,
		Data:     &payload,
	}

	err := w.handleReverse(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	reversals := ledger.getReverseCalls()
	if len(reversals) != 1 {
		t.Fatalf("expected 1 reverse call, got %d", len(reversals))
	}
	if reversals[0].entryID != transferID {
		t.Errorf("expected entry ID %s, got %s", transferID, reversals[0].entryID)
	}
}

func TestLedgerWorker_ReverseFailure(t *testing.T) {
	ledger := &mockLedger{failReverse: true}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.LedgerPostPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Description: "Reverse",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentLedgerReverse,
		Data:     &payload,
	}

	err := w.handleReverse(context.Background(), event)
	if err == nil {
		t.Fatal("expected error from failed reverse")
	}
}

func TestLedgerWorker_EventRouting(t *testing.T) {
	ledger := &mockLedger{}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	tests := []struct {
		eventType    string
		expectPost   bool
		expectReverse bool
	}{
		{domain.IntentLedgerPost, true, false},
		{domain.IntentLedgerReverse, false, true},
		{"some.unknown", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			ledger.mu.Lock()
			ledger.postCalls = nil
			ledger.reverseCalls = nil
			ledger.mu.Unlock()

			payload := domain.LedgerPostPayload{
				TransferID: uuid.New(),
				TenantID:   uuid.New(),
				Lines: []domain.LedgerLineEntry{
					{AccountCode: "a", EntryType: "DEBIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
					{AccountCode: "b", EntryType: "CREDIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
				},
			}

			event := domain.Event{
				ID:       uuid.New(),
				TenantID: uuid.New(),
				Type:     tt.eventType,
				Data:     &payload,
			}

			_ = w.handleEvent(context.Background(), event)

			posts := ledger.getPostCalls()
			reversals := ledger.getReverseCalls()

			if tt.expectPost && len(posts) == 0 {
				t.Error("expected post call but got none")
			}
			if !tt.expectPost && len(posts) > 0 {
				t.Errorf("expected no post call, got %d", len(posts))
			}
			if tt.expectReverse && len(reversals) == 0 {
				t.Error("expected reverse call but got none")
			}
			if !tt.expectReverse && len(reversals) > 0 {
				t.Errorf("expected no reverse call, got %d", len(reversals))
			}
		})
	}
}

func TestLedgerWorker_PostIdempotency(t *testing.T) {
	// Verify that the same idempotency key is passed through to TigerBeetle
	ledger := &mockLedger{}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	idempotencyKey := fmt.Sprintf("onramp:%s", transferID)

	payload := domain.LedgerPostPayload{
		TransferID:     transferID,
		TenantID:       uuid.New(),
		IdempotencyKey: idempotencyKey,
		Lines: []domain.LedgerLineEntry{
			{AccountCode: "a", EntryType: "DEBIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
			{AccountCode: "b", EntryType: "CREDIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
		},
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.IntentLedgerPost,
		Data:     &payload,
	}

	// Post twice — TigerBeetle should handle idempotency
	_ = w.handlePost(context.Background(), event)
	_ = w.handlePost(context.Background(), event)

	posts := ledger.getPostCalls()
	if len(posts) != 2 {
		t.Fatalf("expected 2 post calls (TB handles idempotency), got %d", len(posts))
	}
	for _, post := range posts {
		if post.IdempotencyKey != idempotencyKey {
			t.Errorf("expected idempotency key %s, got %s", idempotencyKey, post.IdempotencyKey)
		}
	}
}
