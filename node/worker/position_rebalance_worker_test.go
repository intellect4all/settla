package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ── Mocks ───────────────────────────────────────────────────────────────────

type mockRebalanceTreasury struct {
	positions map[uuid.UUID][]domain.Position
}

func (m *mockRebalanceTreasury) GetPositions(_ context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	return m.positions[tenantID], nil
}

type mockRebalanceTenantLister struct {
	tenantIDs []uuid.UUID
}

func (m *mockRebalanceTenantLister) ListActiveTenantIDs(_ context.Context) ([]uuid.UUID, error) {
	return m.tenantIDs, nil
}

type rebalanceCall struct {
	TenantID uuid.UUID
	Type     string // "topup" or "withdrawal"
	Currency domain.Currency
	Location string
	Amount   decimal.Decimal
}

type mockRebalanceEngine struct {
	mu    sync.Mutex
	calls []rebalanceCall
}

func (m *mockRebalanceEngine) RequestTopUp(_ context.Context, tenantID uuid.UUID, req domain.TopUpRequest) (*domain.PositionTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, rebalanceCall{
		TenantID: tenantID,
		Type:     "topup",
		Currency: req.Currency,
		Location: req.Location,
		Amount:   req.Amount,
	})
	return &domain.PositionTransaction{ID: uuid.New(), Status: domain.PositionTxStatusProcessing}, nil
}

func (m *mockRebalanceEngine) RequestWithdrawal(_ context.Context, tenantID uuid.UUID, req domain.WithdrawalRequest) (*domain.PositionTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, rebalanceCall{
		TenantID: tenantID,
		Type:     "withdrawal",
		Currency: req.Currency,
		Location: req.Location,
		Amount:   req.Amount,
	})
	return &domain.PositionTransaction{ID: uuid.New(), Status: domain.PositionTxStatusProcessing}, nil
}

func (m *mockRebalanceEngine) getCalls() []rebalanceCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]rebalanceCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

type mockRebalancePublisher struct {
	mu     sync.Mutex
	events []domain.Event
}

func (m *mockRebalancePublisher) Publish(_ context.Context, event domain.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockRebalancePublisher) getEvents() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]domain.Event, len(m.events))
	copy(cp, m.events)
	return cp
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestRebalanceWorker_TriggersRebalance(t *testing.T) {
	tenantID := uuid.New()
	posLow := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:barclays",
		Balance:       decimal.NewFromInt(500),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}
	posSurplus := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:hsbc",
		Balance:       decimal.NewFromInt(20000),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}

	treasury := &mockRebalanceTreasury{
		positions: map[uuid.UUID][]domain.Position{tenantID: {posLow, posSurplus}},
	}
	lister := &mockRebalanceTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	engine := &mockRebalanceEngine{}
	publisher := &mockRebalancePublisher{}

	w := NewPositionRebalanceWorker(treasury, lister, engine, publisher, slog.Default())
	w.cooldown = 1 * time.Second // short cooldown for testing

	ctx := context.Background()
	w.runCycle(ctx)

	calls := engine.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 engine calls (withdrawal + topup), got %d", len(calls))
	}

	// First call should be withdrawal from surplus
	if calls[0].Type != "withdrawal" {
		t.Errorf("expected first call to be withdrawal, got %s", calls[0].Type)
	}
	if calls[0].Location != "bank:hsbc" {
		t.Errorf("expected withdrawal from bank:hsbc, got %s", calls[0].Location)
	}

	// Second call should be topup to deficit
	if calls[1].Type != "topup" {
		t.Errorf("expected second call to be topup, got %s", calls[1].Type)
	}
	if calls[1].Location != "bank:barclays" {
		t.Errorf("expected topup to bank:barclays, got %s", calls[1].Location)
	}

	// Amount should be the deficit (target - balance = 5000 - 500 = 4500)
	expectedAmount := decimal.NewFromInt(4500)
	if !calls[0].Amount.Equal(expectedAmount) {
		t.Errorf("expected amount %s, got %s", expectedAmount, calls[0].Amount)
	}

	// No alerts should be published (rebalance succeeded)
	events := publisher.getEvents()
	if len(events) != 0 {
		t.Errorf("expected no alerts, got %d", len(events))
	}
}

func TestRebalanceWorker_PublishesAlertWhenNoSource(t *testing.T) {
	tenantID := uuid.New()
	posLow := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:barclays",
		Balance:       decimal.NewFromInt(500),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}

	treasury := &mockRebalanceTreasury{
		positions: map[uuid.UUID][]domain.Position{tenantID: {posLow}},
	}
	lister := &mockRebalanceTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	engine := &mockRebalanceEngine{}
	publisher := &mockRebalancePublisher{}

	w := NewPositionRebalanceWorker(treasury, lister, engine, publisher, slog.Default())
	w.cooldown = 1 * time.Second

	ctx := context.Background()
	w.runCycle(ctx)

	// No rebalance calls — no surplus position
	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no engine calls, got %d", len(calls))
	}

	// Alert should be published
	events := publisher.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(events))
	}
	if events[0].Type != domain.EventLiquidityAlert {
		t.Errorf("expected EventLiquidityAlert, got %s", events[0].Type)
	}
}

func TestRebalanceWorker_SkipsPositionsAboveMin(t *testing.T) {
	tenantID := uuid.New()
	posHealthy := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:barclays",
		Balance:       decimal.NewFromInt(5000),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}

	treasury := &mockRebalanceTreasury{
		positions: map[uuid.UUID][]domain.Position{tenantID: {posHealthy}},
	}
	lister := &mockRebalanceTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	engine := &mockRebalanceEngine{}
	publisher := &mockRebalancePublisher{}

	w := NewPositionRebalanceWorker(treasury, lister, engine, publisher, slog.Default())

	ctx := context.Background()
	w.runCycle(ctx)

	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no rebalance for healthy position, got %d calls", len(calls))
	}
	events := publisher.getEvents()
	if len(events) != 0 {
		t.Errorf("expected no alerts for healthy position, got %d", len(events))
	}
}

func TestRebalanceWorker_RespectsCooldown(t *testing.T) {
	tenantID := uuid.New()
	posLow := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:barclays",
		Balance:       decimal.NewFromInt(500),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}

	treasury := &mockRebalanceTreasury{
		positions: map[uuid.UUID][]domain.Position{tenantID: {posLow}},
	}
	lister := &mockRebalanceTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	engine := &mockRebalanceEngine{}
	publisher := &mockRebalancePublisher{}

	w := NewPositionRebalanceWorker(treasury, lister, engine, publisher, slog.Default())
	w.cooldown = 1 * time.Hour // long cooldown

	ctx := context.Background()

	// First cycle: should publish alert
	w.runCycle(ctx)
	events := publisher.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 alert on first cycle, got %d", len(events))
	}

	// Second cycle: should be skipped (cooldown)
	w.runCycle(ctx)
	events = publisher.getEvents()
	if len(events) != 1 {
		t.Errorf("expected still 1 alert after cooldown skip, got %d", len(events))
	}
}

func TestRebalanceWorker_SkipsCrossCurrencyRebalance(t *testing.T) {
	tenantID := uuid.New()
	posLowGBP := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyGBP,
		Location:      "bank:barclays",
		Balance:       decimal.NewFromInt(500),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}
	posSurplusUSD := domain.Position{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Currency:      domain.CurrencyUSD,
		Location:      "bank:chase",
		Balance:       decimal.NewFromInt(50000),
		Locked:        decimal.Zero,
		MinBalance:    decimal.NewFromInt(1000),
		TargetBalance: decimal.NewFromInt(5000),
	}

	treasury := &mockRebalanceTreasury{
		positions: map[uuid.UUID][]domain.Position{tenantID: {posLowGBP, posSurplusUSD}},
	}
	lister := &mockRebalanceTenantLister{tenantIDs: []uuid.UUID{tenantID}}
	engine := &mockRebalanceEngine{}
	publisher := &mockRebalancePublisher{}

	w := NewPositionRebalanceWorker(treasury, lister, engine, publisher, slog.Default())
	w.cooldown = 1 * time.Second

	ctx := context.Background()
	w.runCycle(ctx)

	// No rebalance (different currencies) — should publish alert instead
	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no cross-currency rebalance, got %d calls", len(calls))
	}
	events := publisher.getEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 alert for unfulfilled deficit, got %d", len(events))
	}
}
