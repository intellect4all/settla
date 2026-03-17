package transferdb

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// --- helpers for building test fixtures ---

func newTestTransfer(tenantID uuid.UUID) *domain.Transfer {
	return &domain.Transfer{
		TenantID:       tenantID,
		ExternalRef:    "ext-ref-" + uuid.NewString()[:8],
		IdempotencyKey: "idem-" + uuid.NewString()[:8],
		Status:         domain.TransferStatusCreated,
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestAmount:     decimal.NewFromInt(500000),
		StableCoin:     domain.CurrencyUSDT,
		StableAmount:   decimal.NewFromInt(1200),
		Chain:          "tron",
		FXRate:         decimal.NewFromFloat(1.2),
		Sender: domain.Sender{
			ID:      uuid.New(),
			Name:    "Test Sender",
			Email:   "sender@test.com",
			Country: "GB",
		},
		Recipient: domain.Recipient{
			Name:          "Test Recipient",
			AccountNumber: "1234567890",
			BankName:      "Test Bank",
			Country:       "NG",
		},
	}
}

func newTestOutboxEntry(aggregateID, tenantID uuid.UUID, eventType string, isIntent bool) domain.OutboxEntry {
	if isIntent {
		return domain.MustNewOutboxIntent("transfer", aggregateID, tenantID, eventType, []byte(`{"test": true}`))
	}
	return domain.MustNewOutboxEvent("transfer", aggregateID, tenantID, eventType, []byte(`{"test": true}`))
}

// --- outboxEntriesToParams unit test (no DB needed) ---

func TestOutboxEntriesToParams(t *testing.T) {
	tenantID := uuid.New()
	aggID := uuid.New()

	entries := []domain.OutboxEntry{
		newTestOutboxEntry(aggID, tenantID, domain.IntentTreasuryReserve, true),
		newTestOutboxEntry(aggID, tenantID, "transfer.created", false),
	}

	params := outboxEntriesToParams(entries)

	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(params))
	}

	// First entry: intent
	if params[0].IsIntent != true {
		t.Error("expected first param to be intent")
	}
	if params[0].EventType != domain.IntentTreasuryReserve {
		t.Errorf("expected event type %s, got %s", domain.IntentTreasuryReserve, params[0].EventType)
	}
	if params[0].AggregateID != aggID {
		t.Error("aggregate ID mismatch")
	}
	if params[0].TenantID != tenantID {
		t.Error("tenant ID mismatch")
	}

	// Second entry: event (not intent)
	if params[1].IsIntent != false {
		t.Error("expected second param to not be intent")
	}
	if params[1].EventType != "transfer.created" {
		t.Errorf("expected event type transfer.created, got %s", params[1].EventType)
	}
}

func TestOutboxEntriesToParams_ZeroCreatedAt(t *testing.T) {
	entry := domain.OutboxEntry{
		ID:            uuid.New(),
		AggregateType: "transfer",
		AggregateID:   uuid.New(),
		TenantID:      uuid.New(),
		EventType:     "test.event",
		Payload:       []byte(`{}`),
		MaxRetries:    5,
		CreatedAt:     time.Time{}, // zero value
	}

	params := outboxEntriesToParams([]domain.OutboxEntry{entry})

	if params[0].CreatedAt.IsZero() {
		t.Error("expected zero CreatedAt to be replaced with current time")
	}
}

func TestOutboxEntriesToParams_EmptySlice(t *testing.T) {
	params := outboxEntriesToParams(nil)
	if len(params) != 0 {
		t.Fatalf("expected 0 params for nil input, got %d", len(params))
	}

	params = outboxEntriesToParams([]domain.OutboxEntry{})
	if len(params) != 0 {
		t.Fatalf("expected 0 params for empty input, got %d", len(params))
	}
}

// --- TransitionWithOutbox nil-pool guard ---

func TestTransitionWithOutbox_NilPool(t *testing.T) {
	adapter := &TransferStoreAdapter{q: nil, pool: nil}
	err := adapter.TransitionWithOutbox(context.Background(), uuid.New(), domain.TransferStatusFunded, 1, nil)
	if err == nil {
		t.Fatal("expected error when pool is nil")
	}
}

func TestCreateTransferWithOutbox_NilPool(t *testing.T) {
	adapter := &TransferStoreAdapter{q: nil, pool: nil}
	err := adapter.CreateTransferWithOutbox(context.Background(), &domain.Transfer{}, nil)
	if err == nil {
		t.Fatal("expected error when pool is nil")
	}
}
