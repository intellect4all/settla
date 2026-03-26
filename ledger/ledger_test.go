package ledger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// ── Mock TBClient ────────────────────────────────────────────────────

type mockTBClient struct {
	mu        sync.Mutex
	accounts  map[ID128]TBAccount
	transfers map[ID128]TBTransfer

	createAccountsCalls  int
	createTransfersCalls int
	lookupAccountsCalls  int
}

func newMockTBClient() *mockTBClient {
	return &mockTBClient{
		accounts:  make(map[ID128]TBAccount),
		transfers: make(map[ID128]TBTransfer),
	}
}

func (m *mockTBClient) CreateAccounts(accounts []TBAccount) ([]TBCreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createAccountsCalls++

	var results []TBCreateResult
	for i, acc := range accounts {
		if _, exists := m.accounts[acc.ID]; exists {
			results = append(results, TBCreateResult{Index: uint32(i), Result: TBResultExists})
		} else {
			m.accounts[acc.ID] = acc
		}
	}
	return results, nil
}

func (m *mockTBClient) CreateTransfers(transfers []TBTransfer) ([]TBCreateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createTransfersCalls++

	var results []TBCreateResult
	for i, t := range transfers {
		if _, exists := m.transfers[t.ID]; exists {
			results = append(results, TBCreateResult{Index: uint32(i), Result: TBResultExists})
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

func (m *mockTBClient) LookupAccounts(ids []ID128) ([]TBAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookupAccountsCalls++

	var found []TBAccount
	for _, id := range ids {
		if acc, ok := m.accounts[id]; ok {
			found = append(found, acc)
		}
	}
	return found, nil
}

func (m *mockTBClient) Close() {}

// ── Mock EventPublisher ──────────────────────────────────────────────

type mockPublisher struct {
	mu     sync.Mutex
	events []domain.Event
}

func (m *mockPublisher) Publish(_ context.Context, event domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockPublisher) Events() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]domain.Event, len(m.events))
	copy(copied, m.events)
	return copied
}

// ── Helpers ──────────────────────────────────────────────────────────

func newTestService(tb TBClient, opts ...Option) (*Service, *mockPublisher) {
	pub := &mockPublisher{}
	allOpts := append([]Option{WithNoBatching()}, opts...)
	svc := NewService(tb, nil, pub, slogDiscard(), nil, allOpts...)
	return svc, pub
}

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func balancedEntry(tenantSlug string) domain.JournalEntry {
	tenantID := uuid.New()
	return domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &tenantID,
		IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("idem-%s", uuid.New())),
		Description:    "Test balanced entry",
		ReferenceType:  "transfer",
		Lines: []domain.EntryLine{
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: domain.AccountCode(domain.TenantAccountCode(tenantSlug, "assets:bank:gbp:clearing")),
					EntryType:   domain.EntryTypeDebit,
					Amount:      decimal.NewFromFloat(1000.00),
					Currency:    domain.CurrencyGBP,
				},
			},
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: domain.AccountCode(domain.TenantAccountCode(tenantSlug, "liabilities:customer:pending")),
					EntryType:   domain.EntryTypeCredit,
					Amount:      decimal.NewFromFloat(1000.00),
					Currency:    domain.CurrencyGBP,
				},
			},
		},
	}
}

func multiLineEntry() domain.JournalEntry {
	tenantID := uuid.New()
	return domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &tenantID,
		IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("idem-%s", uuid.New())),
		Description:    "Multi-line settlement entry",
		Lines: []domain.EntryLine{
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: "assets:crypto:usdt:tron",
					EntryType:   domain.EntryTypeDebit,
					Amount:      decimal.NewFromFloat(950.00),
					Currency:    domain.CurrencyUSDT,
				},
			},
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: "expenses:provider:onramp",
					EntryType:   domain.EntryTypeDebit,
					Amount:      decimal.NewFromFloat(50.00),
					Currency:    domain.CurrencyUSDT,
				},
			},
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: "tenant:lemfi:assets:bank:gbp:clearing",
					EntryType:   domain.EntryTypeCredit,
					Amount:      decimal.NewFromFloat(1000.00),
					Currency:    domain.CurrencyUSDT,
				},
			},
		},
	}
}

// ── Tests ────────────────────────────────────────────────────────────

func TestPostEntries_Balanced(t *testing.T) {
	tb := newMockTBClient()
	svc, pub := newTestService(tb)

	entry := balancedEntry("lemfi")
	result, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("PostEntries failed: %v", err)
	}

	if result.ID == uuid.Nil {
		t.Error("expected non-nil entry ID")
	}

	// Verify TB got the write.
	if tb.createTransfersCalls != 1 {
		t.Errorf("expected 1 CreateTransfers call, got %d", tb.createTransfersCalls)
	}

	// Verify accounts were ensured.
	if tb.createAccountsCalls != 1 {
		t.Errorf("expected 1 CreateAccounts call, got %d", tb.createAccountsCalls)
	}
	if len(tb.accounts) != 2 {
		t.Errorf("expected 2 TB accounts, got %d", len(tb.accounts))
	}

	// Verify event was published.
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "ledger.entry.posted" {
		t.Errorf("expected event type 'ledger.entry.posted', got %q", events[0].Type)
	}
}

func TestPostEntries_Imbalanced(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := domain.JournalEntry{
		ID:             uuid.New(),
		IdempotencyKey: domain.IdempotencyKey("test-imbalanced"),
		Lines: []domain.EntryLine{
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: "assets:bank:gbp",
					EntryType:   domain.EntryTypeDebit,
					Amount:      decimal.NewFromFloat(1000.00),
					Currency:    domain.CurrencyGBP,
				},
			},
			{
				ID: uuid.New(),
				Posting: domain.Posting{
					AccountCode: "liabilities:pending",
					EntryType:   domain.EntryTypeCredit,
					Amount:      decimal.NewFromFloat(999.00),
					Currency:    domain.CurrencyGBP,
				},
			},
		},
	}

	_, err := svc.PostEntries(context.Background(), entry)
	if err == nil {
		t.Fatal("expected error for imbalanced entry")
	}

	// TB should never be called for invalid entries.
	if tb.createTransfersCalls != 0 {
		t.Errorf("TB should not be called for imbalanced entries, got %d calls", tb.createTransfersCalls)
	}
}

func TestPostEntries_EmptyLines(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := domain.JournalEntry{
		ID:             uuid.New(),
		IdempotencyKey: domain.IdempotencyKey("test-empty"),
		Lines:          nil,
	}

	_, err := svc.PostEntries(context.Background(), entry)
	if err == nil {
		t.Fatal("expected error for empty lines")
	}
}

func TestPostEntries_MultiLine(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := multiLineEntry()
	_, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("PostEntries failed: %v", err)
	}

	// Multi-line entry with 2 debits + 1 credit → 2 TB transfers.
	if len(tb.transfers) != 2 {
		t.Errorf("expected 2 TB transfers for multi-line entry, got %d", len(tb.transfers))
	}
}

func TestPostEntries_Idempotency(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := balancedEntry("lemfi")

	// First call.
	result1, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("first PostEntries failed: %v", err)
	}

	// Second call with same entry (same ID, same idempotency key).
	result2, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("second PostEntries failed: %v", err)
	}

	if result1.ID != result2.ID {
		t.Error("idempotent calls should return same entry ID")
	}

	// TB should have handled idempotency (TBResultExists).
	// The mock increments createTransfersCalls but returns TBResultExists.
	if tb.createTransfersCalls != 2 {
		t.Errorf("expected 2 CreateTransfers calls (TB handles idempotency), got %d", tb.createTransfersCalls)
	}
}

func TestGetBalance_FromTB(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := balancedEntry("lemfi")
	_, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("PostEntries failed: %v", err)
	}

	// Check debit account balance (debits_posted - credits_posted).
	debitCode := entry.Lines[0].AccountCode
	balance, err := svc.GetBalance(context.Background(), string(debitCode))
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	// For a debit account with 1000 posted: credits(0) - debits(1000) = -1000
	expected := decimal.NewFromFloat(-1000.00)
	if !balance.Equal(expected) {
		t.Errorf("expected balance %s, got %s", expected, balance)
	}

	// Check credit account balance.
	creditCode := entry.Lines[1].AccountCode
	balance, err = svc.GetBalance(context.Background(), string(creditCode))
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	expected = decimal.NewFromFloat(1000.00)
	if !balance.Equal(expected) {
		t.Errorf("expected balance %s, got %s", expected, balance)
	}
}

func TestGetBalance_AccountNotFound(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	_, err := svc.GetBalance(context.Background(), "nonexistent:account")
	if err == nil {
		t.Fatal("expected error for nonexistent account")
	}
}

func TestGetBalance_StubMode(t *testing.T) {
	// No TB client → stub mode.
	svc := NewService(nil, nil, nil, slogDiscard(), nil)

	balance, err := svc.GetBalance(context.Background(), "any:account")
	if err != nil {
		t.Fatalf("stub GetBalance should not error: %v", err)
	}
	if !balance.IsZero() {
		t.Errorf("stub GetBalance should return zero, got %s", balance)
	}
}

func TestPostEntries_StubMode(t *testing.T) {
	// No TB client → stub mode.
	svc := NewService(nil, nil, nil, slogDiscard(), nil)

	entry := balancedEntry("test")
	result, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("stub PostEntries should not error: %v", err)
	}
	if result == nil {
		t.Fatal("stub PostEntries should return entry")
	}
}

func TestPostEntries_AssignsIDs(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	entry := domain.JournalEntry{
		IdempotencyKey: domain.IdempotencyKey("auto-id-test"),
		Description:    "Test auto ID assignment",
		Lines: []domain.EntryLine{
			{
				Posting: domain.Posting{
					AccountCode: "assets:test:a",
					EntryType:   domain.EntryTypeDebit,
					Amount:      decimal.NewFromFloat(100),
					Currency:    domain.CurrencyUSD,
				},
			},
			{
				Posting: domain.Posting{
					AccountCode: "liabilities:test:b",
					EntryType:   domain.EntryTypeCredit,
					Amount:      decimal.NewFromFloat(100),
					Currency:    domain.CurrencyUSD,
				},
			},
		},
	}

	result, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("PostEntries failed: %v", err)
	}

	if result.ID == uuid.Nil {
		t.Error("entry ID should be auto-assigned")
	}
	if result.PostedAt.IsZero() {
		t.Error("posted_at should be auto-assigned")
	}
	for i, line := range result.Lines {
		if line.ID == uuid.Nil {
			t.Errorf("line %d ID should be auto-assigned", i)
		}
	}
}

func TestPostEntries_BalanceUpdatesCorrectly(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	// Post two entries to the same accounts.
	entry1 := balancedEntry("fincra")
	entry1.Lines[0].Amount = decimal.NewFromFloat(500)
	entry1.Lines[1].Amount = decimal.NewFromFloat(500)

	entry2 := balancedEntry("fincra")
	entry2.Lines[0].AccountCode = entry1.Lines[0].AccountCode
	entry2.Lines[1].AccountCode = entry1.Lines[1].AccountCode
	entry2.Lines[0].Amount = decimal.NewFromFloat(300)
	entry2.Lines[1].Amount = decimal.NewFromFloat(300)

	if _, err := svc.PostEntries(context.Background(), entry1); err != nil {
		t.Fatalf("entry1 failed: %v", err)
	}
	if _, err := svc.PostEntries(context.Background(), entry2); err != nil {
		t.Fatalf("entry2 failed: %v", err)
	}

	// Debit account: debited 500 + 300 = 800, credits 0. Balance = 0 - 800 = -800.
	balance, err := svc.GetBalance(context.Background(), string(entry1.Lines[0].AccountCode))
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}
	if !balance.Equal(decimal.NewFromFloat(-800)) {
		t.Errorf("expected -800, got %s", balance)
	}

	// Credit account: credited 500 + 300 = 800, debits 0. Balance = 800 - 0 = 800.
	balance, err = svc.GetBalance(context.Background(), string(entry1.Lines[1].AccountCode))
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}
	if !balance.Equal(decimal.NewFromFloat(800)) {
		t.Errorf("expected 800, got %s", balance)
	}
}

func TestPostEntries_ConcurrentAccess(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	const goroutines = 100
	var wg sync.WaitGroup
	var errCount atomic.Int32

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			entry := domain.JournalEntry{
				ID:             uuid.New(),
				IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("concurrent-%d", idx)),
				Description:    fmt.Sprintf("Concurrent entry %d", idx),
				Lines: []domain.EntryLine{
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: domain.AccountCode(fmt.Sprintf("assets:concurrent:%d", idx)),
							EntryType:   domain.EntryTypeDebit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: domain.AccountCode(fmt.Sprintf("liabilities:concurrent:%d", idx)),
							EntryType:   domain.EntryTypeCredit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
				},
			}
			if _, err := svc.PostEntries(context.Background(), entry); err != nil {
				errCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("expected 0 errors, got %d", errCount.Load())
	}

	// All 100 entries should have been posted.
	if len(tb.transfers) != 100 {
		t.Errorf("expected 100 transfers, got %d", len(tb.transfers))
	}
}

func TestPostEntries_HotKeyContention(t *testing.T) {
	tb := newMockTBClient()
	svc, _ := newTestService(tb)

	// All 100 goroutines post to the SAME account pair (hot-key contention).
	const goroutines = 100
	debitCode := domain.AccountCode("assets:hotkey:contention:debit")
	creditCode := domain.AccountCode("liabilities:hotkey:contention:credit")

	var wg sync.WaitGroup
	var errCount atomic.Int32

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			tenantID := uuid.New()
			entry := domain.JournalEntry{
				ID:             uuid.New(),
				TenantID:       &tenantID,
				IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("hotkey-%d", idx)),
				Description:    fmt.Sprintf("Hot key entry %d", idx),
				Lines: []domain.EntryLine{
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: debitCode,
							EntryType:   domain.EntryTypeDebit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: creditCode,
							EntryType:   domain.EntryTypeCredit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
				},
			}
			if _, err := svc.PostEntries(context.Background(), entry); err != nil {
				errCount.Add(1)
				t.Logf("hotkey entry %d failed: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("expected 0 errors, got %d", errCount.Load())
	}

	// All 100 entries should have been posted (100 TB transfers to the same accounts).
	tb.mu.Lock()
	transferCount := len(tb.transfers)
	tb.mu.Unlock()
	if transferCount != goroutines {
		t.Errorf("expected %d transfers, got %d", goroutines, transferCount)
	}

	// Check balances: 100 × $100 = $10,000 each side.
	debitBalance, err := svc.GetBalance(context.Background(), string(debitCode))
	if err != nil {
		t.Fatalf("GetBalance debit failed: %v", err)
	}
	expectedDebit := decimal.NewFromFloat(-10000)
	if !debitBalance.Equal(expectedDebit) {
		t.Errorf("debit balance = %s, want %s", debitBalance, expectedDebit)
	}

	creditBalance, err := svc.GetBalance(context.Background(), string(creditCode))
	if err != nil {
		t.Fatalf("GetBalance credit failed: %v", err)
	}
	expectedCredit := decimal.NewFromFloat(10000)
	if !creditBalance.Equal(expectedCredit) {
		t.Errorf("credit balance = %s, want %s", creditBalance, expectedCredit)
	}
}

// ── TigerBeetle Backend Tests ────────────────────────────────────────

func TestTBBackend_BuildTransfers_Simple(t *testing.T) {
	tb := newTBBackend(newMockTBClient(), 1, slogDiscard())

	entry := balancedEntry("test")
	var debits, credits []domain.EntryLine
	for _, l := range entry.Lines {
		if l.EntryType == domain.EntryTypeDebit {
			debits = append(debits, l)
		} else {
			credits = append(credits, l)
		}
	}

	transfers, err := tb.buildTransfers(entry, debits, credits)
	if err != nil {
		t.Fatalf("buildTransfers failed: %v", err)
	}

	if len(transfers) != 1 {
		t.Fatalf("expected 1 transfer, got %d", len(transfers))
	}

	// Single transfer should NOT be linked.
	if transfers[0].Flags&TBFlagLinked != 0 {
		t.Error("single transfer should not be linked")
	}
}

func TestTBBackend_BuildTransfers_MultiLine(t *testing.T) {
	tb := newTBBackend(newMockTBClient(), 1, slogDiscard())

	entry := multiLineEntry()
	var debits, credits []domain.EntryLine
	for _, l := range entry.Lines {
		if l.EntryType == domain.EntryTypeDebit {
			debits = append(debits, l)
		} else {
			credits = append(credits, l)
		}
	}

	transfers, err := tb.buildTransfers(entry, debits, credits)
	if err != nil {
		t.Fatalf("buildTransfers failed: %v", err)
	}

	if len(transfers) != 2 {
		t.Fatalf("expected 2 transfers, got %d", len(transfers))
	}

	// First transfer should be linked, last should not.
	if transfers[0].Flags&TBFlagLinked == 0 {
		t.Error("first transfer should be linked")
	}
	if transfers[1].Flags&TBFlagLinked != 0 {
		t.Error("last transfer should not be linked")
	}

	// Verify amounts: 950 matched to 950 of credit, then 50 matched to remaining 50.
	if transfers[0].Amount != 95000000000 { // 950 * 10^8
		t.Errorf("expected first transfer amount 95000000000, got %d", transfers[0].Amount)
	}
	if transfers[1].Amount != 5000000000 { // 50 * 10^8
		t.Errorf("expected second transfer amount 5000000000, got %d", transfers[1].Amount)
	}
}

func TestTBBackend_DeterministicIDs(t *testing.T) {
	// Same account code → same TB account ID.
	code := "tenant:lemfi:assets:bank:gbp:clearing"
	id1 := AccountIDFromCode(code)
	id2 := AccountIDFromCode(code)
	if id1 != id2 {
		t.Error("AccountIDFromCode should be deterministic")
	}

	// Different codes → different IDs.
	id3 := AccountIDFromCode("tenant:fincra:assets:bank:gbp:clearing")
	if id1 == id3 {
		t.Error("different codes should produce different IDs")
	}
}

func TestTBBackend_AmountConversion(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		wantValue string // expected value after truncation (empty = same as input)
	}{
		{"integer", "1000", false, ""},
		{"8 decimals", "1000.12345678", false, ""},
		{"zero", "0", false, ""},
		{"negative", "-100", true, ""},
		{"truncates beyond 8 dp", "1.123456789", false, "1.12345678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := decimal.NewFromString(tt.input)
			tbAmount, err := DecimalToTBAmount(d)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Round-trip should match the (possibly truncated) value.
			expected := d
			if tt.wantValue != "" {
				expected, _ = decimal.NewFromString(tt.wantValue)
			}
			roundTrip := TBAmountToDecimal(tbAmount)
			if !roundTrip.Equal(expected) {
				t.Errorf("round-trip failed: %s → %d → %s (expected %s)", d, tbAmount, roundTrip, expected)
			}
		})
	}
}

// ── Batcher Tests ────────────────────────────────────────────────────

func TestBatcher_BatchesMultipleEntries(t *testing.T) {
	tb := newMockTBClient()
	pub := &mockPublisher{}
	svc := NewService(tb, nil, pub, slogDiscard(), nil,
		WithBatchWindow(50*time.Millisecond),
		WithBatchMaxSize(10),
	)
	svc.Start()
	defer svc.Stop()

	const count = 10
	var wg sync.WaitGroup
	var errCount atomic.Int32

	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(idx int) {
			defer wg.Done()
			entry := domain.JournalEntry{
				ID:             uuid.New(),
				IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("batch-%d", idx)),
				Description:    fmt.Sprintf("Batch entry %d", idx),
				Lines: []domain.EntryLine{
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: domain.AccountCode(fmt.Sprintf("assets:batch:%d", idx)),
							EntryType:   domain.EntryTypeDebit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
					{
						ID: uuid.New(),
						Posting: domain.Posting{
							AccountCode: domain.AccountCode(fmt.Sprintf("liabilities:batch:%d", idx)),
							EntryType:   domain.EntryTypeCredit,
							Amount:      decimal.NewFromFloat(100),
							Currency:    domain.CurrencyUSD,
						},
					},
				},
			}
			if _, err := svc.PostEntries(context.Background(), entry); err != nil {
				errCount.Add(1)
				t.Logf("batch entry %d failed: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("expected 0 errors, got %d", errCount.Load())
	}

	// With max batch size 10, all 10 should go in fewer CreateTransfers calls
	// than 10 individual calls (ideally 1 batched call).
	tb.mu.Lock()
	calls := tb.createTransfersCalls
	tb.mu.Unlock()
	if calls >= count {
		t.Errorf("batcher should reduce TB calls: got %d calls for %d entries", calls, count)
	}
}

func TestBatcher_WindowFlush(t *testing.T) {
	tb := newMockTBClient()
	pub := &mockPublisher{}
	svc := NewService(tb, nil, pub, slogDiscard(), nil,
		WithBatchWindow(20*time.Millisecond),
		WithBatchMaxSize(1000), // High max so only timer triggers flush.
	)
	svc.Start()
	defer svc.Stop()

	entry := balancedEntry("timer-test")
	_, err := svc.PostEntries(context.Background(), entry)
	if err != nil {
		t.Fatalf("PostEntries failed: %v", err)
	}

	// Entry should have been flushed after the 20ms window.
	tb.mu.Lock()
	transfers := len(tb.transfers)
	tb.mu.Unlock()
	if transfers != 1 {
		t.Errorf("expected 1 transfer after window flush, got %d", transfers)
	}
}

// ── Sync Consumer Tests ──────────────────────────────────────────────

func TestSyncConsumer_Enqueue(t *testing.T) {
	// Create sync consumer without a real PG backend.
	sc := newSyncConsumer(nil, 100, 100*time.Millisecond, slogDiscard(), 10000)

	entry := balancedEntry("sync-test")
	sc.Enqueue(entry)

	if sc.Pending() != 1 {
		t.Errorf("expected 1 pending entry, got %d", sc.Pending())
	}
}

func TestSyncConsumer_QueueFull(t *testing.T) {
	sc := newSyncConsumer(nil, 100, 100*time.Millisecond, slogDiscard(), 10000)
	sc.queue = make(chan domain.JournalEntry, 1) // Override with tiny buffer.

	// First enqueue succeeds.
	sc.Enqueue(balancedEntry("a"))

	// Second should be dropped (non-blocking).
	sc.Enqueue(balancedEntry("b"))

	if sc.Pending() != 1 {
		t.Errorf("expected 1 pending (second dropped), got %d", sc.Pending())
	}
}

// ── Options Tests ────────────────────────────────────────────────────

func TestOptions(t *testing.T) {
	cfg := defaultConfig()
	if cfg.batchWindow != 10*time.Millisecond {
		t.Errorf("default batch window should be 10ms, got %v", cfg.batchWindow)
	}

	WithBatchWindow(50 * time.Millisecond)(&cfg)
	if cfg.batchWindow != 50*time.Millisecond {
		t.Errorf("batch window should be 50ms after option, got %v", cfg.batchWindow)
	}

	WithNoBatching()(&cfg)
	if cfg.batchWindow != 0 {
		t.Errorf("batch window should be 0 after WithNoBatching, got %v", cfg.batchWindow)
	}
}

// ── Degradation Tests ────────────────────────────────────────────────

func TestGetEntries_NilPGReturnsNil(t *testing.T) {
	svc := NewService(newMockTBClient(), nil, nil, slogDiscard(), nil, WithNoBatching())

	entries, err := svc.GetEntries(context.Background(), "any", time.Time{}, time.Now(), 10, 0)
	if err != nil {
		t.Fatalf("GetEntries with nil PG should not error: %v", err)
	}
	if entries != nil {
		t.Error("GetEntries with nil PG should return nil")
	}
}

func TestReverseEntry_NilPGErrors(t *testing.T) {
	svc := NewService(newMockTBClient(), nil, nil, slogDiscard(), nil, WithNoBatching())

	_, err := svc.ReverseEntry(context.Background(), uuid.New(), "test")
	if err == nil {
		t.Fatal("ReverseEntry with nil PG should error")
	}
}
