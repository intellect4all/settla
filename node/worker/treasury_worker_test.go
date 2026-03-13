package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// mockTreasury records reserve/release calls.
type mockTreasury struct {
	mu       sync.Mutex
	calls    []treasuryCall
	failOn   string // "reserve" or "release" to return error
}

type treasuryCall struct {
	method   string
	tenantID uuid.UUID
	currency domain.Currency
	location string
	amount   decimal.Decimal
}

func (m *mockTreasury) Reserve(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, treasuryCall{
		method: "Reserve", tenantID: tenantID, currency: currency, location: location, amount: amount,
	})
	if m.failOn == "reserve" {
		return fmt.Errorf("insufficient funds")
	}
	return nil
}

func (m *mockTreasury) Release(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, treasuryCall{
		method: "Release", tenantID: tenantID, currency: currency, location: location, amount: amount,
	})
	if m.failOn == "release" {
		return fmt.Errorf("release failed")
	}
	return nil
}

func (m *mockTreasury) GetPositions(ctx context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	return nil, nil
}
func (m *mockTreasury) GetPosition(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string) (*domain.Position, error) {
	return nil, nil
}
func (m *mockTreasury) GetLiquidityReport(ctx context.Context, tenantID uuid.UUID) (*domain.LiquidityReport, error) {
	return nil, nil
}

func (m *mockTreasury) getCalls() []treasuryCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]treasuryCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// mockPublisher records published events.
type mockPublisher struct {
	mu     sync.Mutex
	events []domain.Event
}

func (m *mockPublisher) Publish(ctx context.Context, event domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockPublisher) getEvents() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]domain.Event, len(m.events))
	copy(cp, m.events)
	return cp
}

func TestTreasuryWorker_ReserveSuccess(t *testing.T) {
	treasury := &mockTreasury{}
	pub := &mockPublisher{}
	w := &TreasuryWorker{
		treasury:  treasury,
		publisher: pub,
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.TreasuryReservePayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Currency:   domain.Currency("GBP"),
		Amount:     decimal.NewFromInt(1000),
		Location:   "bank:gbp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentTreasuryReserve,
		Data:     &payload,
	}

	err := w.handleReserve(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify treasury was called
	calls := treasury.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 treasury call, got %d", len(calls))
	}
	if calls[0].method != "Reserve" {
		t.Errorf("expected Reserve, got %s", calls[0].method)
	}
	if calls[0].tenantID != tenantID {
		t.Errorf("expected tenant %s, got %s", tenantID, calls[0].tenantID)
	}
	if !calls[0].amount.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("expected amount 1000, got %s", calls[0].amount)
	}

	// Verify result event was published
	events := pub.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(events))
	}
	if events[0].Type != domain.EventTreasuryReserved {
		t.Errorf("expected event type %s, got %s", domain.EventTreasuryReserved, events[0].Type)
	}
}

func TestTreasuryWorker_ReserveFailure(t *testing.T) {
	treasury := &mockTreasury{failOn: "reserve"}
	pub := &mockPublisher{}
	w := &TreasuryWorker{
		treasury:  treasury,
		publisher: pub,
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.TreasuryReservePayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Currency:   domain.Currency("GBP"),
		Amount:     decimal.NewFromInt(1000),
		Location:   "bank:gbp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentTreasuryReserve,
		Data:     &payload,
	}

	err := w.handleReserve(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error (failure published as event), got %v", err)
	}

	calls := treasury.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 treasury call, got %d", len(calls))
	}
	if calls[0].method != "Reserve" {
		t.Errorf("expected Reserve, got %s", calls[0].method)
	}

	// Verify failure event was published
	events := pub.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(events))
	}
	if events[0].Type != domain.EventTreasuryFailed {
		t.Errorf("expected event type %s, got %s", domain.EventTreasuryFailed, events[0].Type)
	}
}

func TestTreasuryWorker_ReleaseSuccess(t *testing.T) {
	treasury := &mockTreasury{}
	pub := &mockPublisher{}
	w := &TreasuryWorker{
		treasury:  treasury,
		publisher: pub,
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	transferID := uuid.New()
	tenantID := uuid.New()

	payload := domain.TreasuryReleasePayload{
		TransferID: transferID,
		TenantID:   tenantID,
		Currency:   domain.Currency("GBP"),
		Amount:     decimal.NewFromInt(500),
		Location:   "bank:gbp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentTreasuryRelease,
		Data:     &payload,
	}

	err := w.handleRelease(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := treasury.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 treasury call, got %d", len(calls))
	}
	if calls[0].method != "Release" {
		t.Errorf("expected Release, got %s", calls[0].method)
	}
	if !calls[0].amount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("expected amount 500, got %s", calls[0].amount)
	}
}

func TestTreasuryWorker_EventRouting(t *testing.T) {
	treasury := &mockTreasury{}
	pub := &mockPublisher{}
	w := &TreasuryWorker{
		treasury:  treasury,
		publisher: pub,
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	tenantID := uuid.New()

	// Unknown event type should be silently skipped
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     "some.unknown.event",
		Data:     map[string]any{},
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for unknown event, got %v", err)
	}

	calls := treasury.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no treasury calls for unknown event, got %d", len(calls))
	}
}

func TestTreasuryWorker_MalformedPayload(t *testing.T) {
	treasury := &mockTreasury{}
	pub := &mockPublisher{}
	w := &TreasuryWorker{
		treasury:  treasury,
		publisher: pub,
		logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	// Event with data that can't be unmarshalled to TreasuryReservePayload:
	// use a channel which json.Marshal cannot handle
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.IntentTreasuryReserve,
		Data:     make(chan int),
	}

	// handleReserve should ACK (return nil) for malformed payloads
	err := w.handleReserve(context.Background(), event)
	if err != nil {
		t.Fatalf("expected nil error for malformed payload, got %v", err)
	}

	calls := treasury.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no treasury calls for malformed payload, got %d", len(calls))
	}
}
