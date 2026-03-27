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
	if string(posts[0].IdempotencyKey) != fmt.Sprintf("onramp:%s", transferID) {
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
		Lines: []domain.LedgerLineEntry{
			{AccountCode: "a", EntryType: "DEBIT", Amount: decimal.NewFromInt(100), Currency: "GBP", Description: "debit a"},
			{AccountCode: "b", EntryType: "CREDIT", Amount: decimal.NewFromInt(100), Currency: "GBP", Description: "credit b"},
		},
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

	// handleReverse now uses PostEntries with swapped debit/credit lines
	posts := ledger.getPostCalls()
	if len(posts) != 1 {
		t.Fatalf("expected 1 post call for reversal, got %d", len(posts))
	}
	if string(posts[0].IdempotencyKey) != fmt.Sprintf("reverse:%s", transferID) {
		t.Errorf("expected idempotency key reverse:%s, got %s", transferID, posts[0].IdempotencyKey)
	}
	if posts[0].ReferenceType != "reversal" {
		t.Errorf("expected reference type 'reversal', got %s", posts[0].ReferenceType)
	}
	if len(posts[0].Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(posts[0].Lines))
	}
	// Verify debit/credit swap: original DEBIT→CREDIT, original CREDIT→DEBIT
	if posts[0].Lines[0].EntryType != domain.EntryTypeCredit {
		t.Errorf("expected first line swapped to CREDIT, got %s", posts[0].Lines[0].EntryType)
	}
	if posts[0].Lines[1].EntryType != domain.EntryTypeDebit {
		t.Errorf("expected second line swapped to DEBIT, got %s", posts[0].Lines[1].EntryType)
	}
}

func TestLedgerWorker_ReverseFailure(t *testing.T) {
	// handleReverse now uses PostEntries, so failPost triggers the failure
	ledger := &mockLedger{failPost: true}
	w := &LedgerWorker{
		ledger: ledger,
		logger: ledgerTestLogger(),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.LedgerPostPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		Description: "Reverse",
		Lines: []domain.LedgerLineEntry{
			{AccountCode: "a", EntryType: "DEBIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
			{AccountCode: "b", EntryType: "CREDIT", Amount: decimal.NewFromInt(100), Currency: "GBP"},
		},
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
		eventType  string
		expectPost int // number of PostEntries calls expected (reverse also uses PostEntries now)
	}{
		{domain.IntentLedgerPost, 1},
		{domain.IntentLedgerReverse, 1}, // reverse now builds a reversal entry and calls PostEntries
		{"some.unknown", 0},
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

			if len(posts) != tt.expectPost {
				t.Errorf("expected %d post call(s), got %d", tt.expectPost, len(posts))
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
		if string(post.IdempotencyKey) != idempotencyKey {
			t.Errorf("expected idempotency key %s, got %s", idempotencyKey, post.IdempotencyKey)
		}
	}
}
