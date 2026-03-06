package treasury

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// Compile-time check: Manager implements domain.TreasuryManager.
var _ domain.TreasuryManager = (*Manager)(nil)

// microScale is the fixed-point multiplier for converting decimal amounts to
// int64 micro-units. 10^8 gives 8 decimal places of precision — more than
// enough for any fiat or stablecoin currency.
const microScale int64 = 100_000_000

// positionKey uniquely identifies a treasury position in memory.
type positionKey struct {
	TenantID uuid.UUID
	Currency string
	Location string
}

// PositionState holds the in-memory state for a single treasury position.
// Balance and locked are stored as atomic int64 micro-units so that Reserve
// can run a lock-free CAS loop without holding any mutex.
type PositionState struct {
	// Immutable metadata (set once during load, never mutated).
	ID            uuid.UUID
	TenantID      uuid.UUID
	Currency      domain.Currency
	Location      string
	MinBalance    decimal.Decimal
	TargetBalance decimal.Decimal

	// Atomic micro-unit counters — the hot path.
	// balance: total funds (updated by ledger sync / admin top-up)
	// locked:  committed for in-flight transfers (incremented by CommitReservation)
	// reserved: tentatively held (incremented by Reserve, decremented by Release/Commit)
	balanceMicro  atomic.Int64
	lockedMicro   atomic.Int64
	reservedMicro atomic.Int64

	// dirty is set when the position has been modified since the last flush.
	dirty atomic.Bool
}

// Available returns balance - locked - reserved in decimal.
func (ps *PositionState) Available() decimal.Decimal {
	b := ps.balanceMicro.Load()
	l := ps.lockedMicro.Load()
	r := ps.reservedMicro.Load()
	return fromMicro(b - l - r)
}

// snapshot returns the current position as a domain.Position for reads.
func (ps *PositionState) snapshot() domain.Position {
	return domain.Position{
		ID:            ps.ID,
		TenantID:      ps.TenantID,
		Currency:      ps.Currency,
		Location:      ps.Location,
		Balance:       fromMicro(ps.balanceMicro.Load()),
		Locked:        fromMicro(ps.lockedMicro.Load() + ps.reservedMicro.Load()),
		MinBalance:    ps.MinBalance,
		TargetBalance: ps.TargetBalance,
		UpdatedAt:     time.Now().UTC(),
	}
}

// Manager orchestrates position tracking and liquidity reservations.
//
// Reserve and Release operate on in-memory atomic counters with nanosecond
// latency — they never hit the database. A background goroutine flushes
// dirty positions to Postgres every flushInterval (default 100ms).
type Manager struct {
	positions map[positionKey]*PositionState
	mu        sync.RWMutex // protects map reads/writes, NOT individual positions

	store         Store
	publisher     domain.EventPublisher
	flushInterval time.Duration
	logger        *slog.Logger
	metrics       *observability.Metrics

	// Flush lifecycle.
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewManager creates a treasury manager. Call LoadPositions before Start.
func NewManager(
	store Store,
	publisher domain.EventPublisher,
	logger *slog.Logger,
	metrics *observability.Metrics,
	opts ...Option,
) *Manager {
	m := &Manager{
		positions:     make(map[positionKey]*PositionState),
		store:         store,
		publisher:     publisher,
		flushInterval: 100 * time.Millisecond,
		logger:        logger.With("module", "treasury.manager"),
		metrics:       metrics,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Option configures the Manager.
type Option func(*Manager)

// WithFlushInterval overrides the default 100ms flush interval.
func WithFlushInterval(d time.Duration) Option {
	return func(m *Manager) {
		m.flushInterval = d
	}
}

// Reserve atomically decrements available balance for the given position.
// Uses a CAS loop on the reserved counter — no mutex, no DB, nanosecond latency.
func (m *Manager) Reserve(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	start := time.Now()

	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return err
	}

	amountMicro := toMicro(amount)
	balance := ps.balanceMicro.Load()
	locked := ps.lockedMicro.Load()

	// CAS loop: atomically check available and increment reserved.
	for {
		currentReserved := ps.reservedMicro.Load()
		available := balance - locked - currentReserved
		if available < amountMicro {
			return domain.ErrInsufficientFunds(string(currency), location)
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, currentReserved+amountMicro) {
			break
		}
		// CAS failed — another goroutine changed reserved. Re-read and retry.
		// Re-read balance/locked too in case they changed.
		balance = ps.balanceMicro.Load()
		locked = ps.lockedMicro.Load()
	}

	ps.dirty.Store(true)

	if m.metrics != nil {
		m.metrics.TreasuryReserveTotal.WithLabelValues(tenantID.String(), string(currency)).Inc()
		m.metrics.TreasuryReserveLatency.Observe(time.Since(start).Seconds())
	}

	// Check if remaining available is below minimum threshold.
	remaining := fromMicro(ps.balanceMicro.Load() - ps.lockedMicro.Load() - ps.reservedMicro.Load())
	if remaining.LessThan(ps.MinBalance) && !ps.MinBalance.IsZero() {
		m.publishLiquidityAlert(ctx, ps)
	}

	return nil
}

// Release atomically decrements reserved balance (e.g., when a transfer fails).
func (m *Manager) Release(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return err
	}

	amountMicro := toMicro(amount)

	// CAS loop: atomically decrement reserved.
	for {
		currentReserved := ps.reservedMicro.Load()
		if currentReserved < amountMicro {
			return domain.ErrReservationFailed("release amount exceeds reserved")
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, currentReserved-amountMicro) {
			break
		}
	}

	ps.dirty.Store(true)
	return nil
}

// CommitReservation moves amount from reserved to locked. Called after a
// transfer is confirmed. Order matters: locked increments first so we are
// briefly conservative (reject borderline reserves) rather than briefly
// permissive (approve over-limit reserves).
func (m *Manager) CommitReservation(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return err
	}

	amountMicro := toMicro(amount)

	// Increment locked first (conservative ordering).
	ps.lockedMicro.Add(amountMicro)

	// Then decrement reserved.
	for {
		currentReserved := ps.reservedMicro.Load()
		newReserved := currentReserved - amountMicro
		if newReserved < 0 {
			newReserved = 0
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, newReserved) {
			break
		}
	}

	ps.dirty.Store(true)
	return nil
}

// GetPositions returns all treasury positions for a tenant (from in-memory state).
func (m *Manager) GetPositions(ctx context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []domain.Position
	for k, ps := range m.positions {
		if k.TenantID == tenantID {
			result = append(result, ps.snapshot())
		}
	}
	return result, nil
}

// GetPosition returns a specific position from in-memory state.
func (m *Manager) GetPosition(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string) (*domain.Position, error) {
	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return nil, err
	}
	snap := ps.snapshot()
	return &snap, nil
}

// GetLiquidityReport generates a summary of all positions for a tenant.
func (m *Manager) GetLiquidityReport(ctx context.Context, tenantID uuid.UUID) (*domain.LiquidityReport, error) {
	positions, err := m.GetPositions(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	report := &domain.LiquidityReport{
		TenantID:       tenantID,
		Positions:      positions,
		TotalAvailable: make(map[domain.Currency]decimal.Decimal),
		GeneratedAt:    time.Now().UTC(),
	}

	for _, p := range positions {
		avail := p.Available()
		report.TotalAvailable[p.Currency] = report.TotalAvailable[p.Currency].Add(avail)
		if !p.MinBalance.IsZero() && p.Balance.LessThan(p.MinBalance) {
			report.AlertPositions = append(report.AlertPositions, p)
		}
	}

	return report, nil
}

// UpdateBalance sets the balance for a position. Called by ledger sync when
// external deposits/withdrawals change the total balance.
func (m *Manager) UpdateBalance(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, newBalance decimal.Decimal) error {
	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return err
	}
	ps.balanceMicro.Store(toMicro(newBalance))
	ps.dirty.Store(true)
	return nil
}

// PositionCount returns the number of positions loaded in memory.
func (m *Manager) PositionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.positions)
}

// getPosition looks up a PositionState by key. Returns an error if not found.
func (m *Manager) getPosition(tenantID uuid.UUID, currency domain.Currency, location string) (*PositionState, error) {
	key := positionKey{TenantID: tenantID, Currency: string(currency), Location: location}
	m.mu.RLock()
	ps, ok := m.positions[key]
	m.mu.RUnlock()
	if !ok {
		return nil, domain.ErrInsufficientFunds(string(currency), location)
	}
	return ps, nil
}

// addPosition inserts a PositionState into the map. Used during loading.
func (m *Manager) addPosition(pos domain.Position) {
	key := positionKey{
		TenantID: pos.TenantID,
		Currency: string(pos.Currency),
		Location: pos.Location,
	}
	ps := &PositionState{
		ID:            pos.ID,
		TenantID:      pos.TenantID,
		Currency:      pos.Currency,
		Location:      pos.Location,
		MinBalance:    pos.MinBalance,
		TargetBalance: pos.TargetBalance,
	}
	ps.balanceMicro.Store(toMicro(pos.Balance))
	ps.lockedMicro.Store(toMicro(pos.Locked))
	ps.reservedMicro.Store(0)

	m.mu.Lock()
	m.positions[key] = ps
	m.mu.Unlock()
}

func (m *Manager) publishLiquidityAlert(ctx context.Context, ps *PositionState) {
	if m.publisher == nil {
		return
	}
	event := domain.Event{
		ID:        uuid.New(),
		TenantID:  ps.TenantID,
		Type:      domain.EventLiquidityAlert,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"position_id": ps.ID.String(),
			"currency":    string(ps.Currency),
			"location":    ps.Location,
			"available":   ps.Available().StringFixed(2),
			"min_balance": ps.MinBalance.StringFixed(2),
		},
	}
	if err := m.publisher.Publish(ctx, event); err != nil {
		m.logger.Warn("settla-treasury: failed to publish liquidity alert",
			"position_id", ps.ID,
			"error", err,
		)
	}
}

// toMicro converts a decimal to int64 micro-units (amount × 10^8).
func toMicro(d decimal.Decimal) int64 {
	return d.Mul(decimal.NewFromInt(microScale)).IntPart()
}

// fromMicro converts int64 micro-units back to decimal.
func fromMicro(v int64) decimal.Decimal {
	return decimal.NewFromInt(v).Div(decimal.NewFromInt(microScale))
}

// dirtyPositions returns all positions that have been modified since the last flush.
func (m *Manager) dirtyPositions() []*PositionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var dirty []*PositionState
	for _, ps := range m.positions {
		if ps.dirty.Load() {
			dirty = append(dirty, ps)
		}
	}
	return dirty
}

// allPositionStates returns a snapshot of all position states (for flush).
func (m *Manager) allPositionStates() []*PositionState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]*PositionState, 0, len(m.positions))
	for _, ps := range m.positions {
		states = append(states, ps)
	}
	return states
}
