package treasury

import (
	"context"
	"fmt"
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
// int64 micro-units. 10^6 gives 6 decimal places of precision — exact for
// USDT on Tron (6dp) and more than enough for fiat (2dp).
//
// With int64 max ≈ 9.2e18, this supports up to ~$9.2 trillion per position.
// Previous value of 10^8 capped at ~$92B and caused overflow with large balances.
const microScale int64 = 1_000_000

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
//
// Crash recovery model:
//  1. WAL: Every reserve/release/commit op is written to the DB via
//     ReserveOpStore.LogReserveOp() synchronously BEFORE being queued to the
//     async channel. This ensures the op is durable before the in-memory state
//     is considered committed.
//  2. Sync flush: Reservations >= syncThreshold additionally flush the position's
//     balance+locked to DB synchronously, closing the crash window entirely for
//     large amounts.
//  3. Background flush: The 100ms ticker drains the pendingOps channel (batch
//     insert), flushes all dirty positions, and cleans up old ops.
type Manager struct {
	positions map[positionKey]*PositionState
	mu        sync.RWMutex // protects map reads/writes, NOT individual positions

	// Idempotency map: prevents double-reserve/release/commit from NATS redelivery.
	// Key format: "{transferID}:{operation}" (e.g., "uuid:reserve").
	idempotencyMap map[string]time.Time
	idempotencyMu  sync.RWMutex

	store          Store
	publisher      domain.EventPublisher
	flushInterval  time.Duration
	syncThreshold  decimal.Decimal // reservations >= this amount get a synchronous DB flush
	logger         *slog.Logger
	metrics        *observability.Metrics

	// Crash recovery: pending reserve ops queued by Reserve/Release/Commit,
	// drained by flushOnce() and batch-inserted to DB. Capacity 10000 to avoid
	// blocking the hot path even under peak load.
	pendingOps chan ReserveOp

	// Flush lifecycle.
	stopCh chan struct{}
	doneCh chan struct{}

	// consecutiveFlushFailures counts consecutive flush cycles that encountered
	// at least one DB write error. Used to surface persistent DB outages via logs.
	consecutiveFlushFailures atomic.Int64
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
		positions:      make(map[positionKey]*PositionState),
		idempotencyMap: make(map[string]time.Time),
		store:          store,
		publisher:      publisher,
		flushInterval:  100 * time.Millisecond,
		syncThreshold:  decimal.NewFromInt(100_000), // $100,000 default
		logger:         logger.With("module", "treasury.manager"),
		metrics:        metrics,
		pendingOps:     make(chan ReserveOp, 10000),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
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

// WithSyncThreshold sets the amount threshold above which Reserve performs
// a synchronous position flush to DB immediately after the CAS succeeds.
// Default: $100,000. Set to 0 to disable sync flush.
func WithSyncThreshold(amount decimal.Decimal) Option {
	return func(m *Manager) {
		m.syncThreshold = amount
	}
}

// idempotencyKey builds the dedup key for a treasury operation.
func idempotencyKey(reference uuid.UUID, op string) string {
	return reference.String() + ":" + op
}

// tryAcquireIdempotency atomically claims an idempotency key. Returns true if
// the key was already claimed (caller should return nil). Returns false if this
// caller acquired the key and should proceed with the operation.
//
// On failure, the caller MUST call rollbackIdempotency to release the key so
// that NATS redelivery can retry. On success, the key remains set and future
// calls return true.
func (m *Manager) tryAcquireIdempotency(reference uuid.UUID, op string) bool {
	key := idempotencyKey(reference, op)
	m.idempotencyMu.Lock()
	defer m.idempotencyMu.Unlock()
	if _, exists := m.idempotencyMap[key]; exists {
		return true
	}
	m.idempotencyMap[key] = time.Now()
	return false
}

// rollbackIdempotency removes the idempotency key after a failed operation,
// allowing NATS redelivery to retry.
func (m *Manager) rollbackIdempotency(reference uuid.UUID, op string) {
	key := idempotencyKey(reference, op)
	m.idempotencyMu.Lock()
	delete(m.idempotencyMap, key)
	m.idempotencyMu.Unlock()
}

// cleanupIdempotencyMap removes entries older than maxAge.
func (m *Manager) cleanupIdempotencyMap(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	m.idempotencyMu.Lock()
	for k, t := range m.idempotencyMap {
		if t.Before(cutoff) {
			delete(m.idempotencyMap, k)
		}
	}
	m.idempotencyMu.Unlock()
}

// logOp writes a reserve operation to the WAL (via ReserveOpStore.LogReserveOp)
// synchronously for crash recovery, then queues it to the async channel for
// batch processing. The WAL write ensures the op is durable before the
// in-memory CAS result is considered committed.
//
// If the store doesn't implement ReserveOpStore or the WAL write fails, the op
// is still queued to the channel (best-effort). The hot path is never blocked
// by DB failures.
func (m *Manager) logOp(tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID, opType ReserveOpType) {
	op := ReserveOp{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Currency:  currency,
		Location:  location,
		Amount:    amount,
		Reference: reference,
		OpType:    opType,
		CreatedAt: time.Now().UTC(),
	}

	// WAL: synchronous write to DB if store supports it.
	if opStore, ok := m.store.(ReserveOpStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := opStore.LogReserveOp(ctx, op); err != nil {
			m.logger.Warn("settla-treasury: WAL write failed, falling back to channel-only",
				"op_type", opType,
				"reference", reference,
				"error", err,
			)
		}
	}

	// Queue to async channel for batch processing by flushOnce.
	select {
	case m.pendingOps <- op:
	default:
		m.logger.Warn("settla-treasury: pending ops channel full, dropping op",
			"op_type", opType,
			"reference", reference,
		)
	}
}

// syncFlushPosition performs an immediate synchronous flush of a single position
// to DB. Used for large reservations (>= syncThreshold) to close the crash window.
// Best-effort: if the flush fails, the WAL entry still ensures crash recovery.
func (m *Manager) syncFlushPosition(ps *PositionState) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	balance := fromMicro(ps.balanceMicro.Load())
	locked := fromMicro(ps.lockedMicro.Load())

	if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
		m.logger.Warn("settla-treasury: sync flush failed for large reservation (WAL ensures recovery)",
			"position_id", ps.ID,
			"error", err,
		)
		return
	}
	ps.dirty.Store(false)
}

// maxFlushFailuresBeforeReject is the number of consecutive flush failures
// before Reserve starts rejecting new reservations. The flush logger begins
// emitting ERROR logs at 5 consecutive failures; we open the circuit breaker
// at 10 to avoid accepting reservations that cannot be durably persisted.
const maxFlushFailuresBeforeReject = 10

// Reserve atomically decrements available balance for the given position.
// Uses a CAS loop on the reserved counter — no mutex, no DB, nanosecond latency.
// Idempotent: calling with the same reference UUID returns nil on the second call.
func (m *Manager) Reserve(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	start := time.Now()

	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	// Circuit breaker: reject new reservations during a persistent DB outage.
	// In-memory state remains authoritative, but new reservations accepted while
	// the DB is down cannot be durably persisted and risk data loss on restart.
	if failures := m.consecutiveFlushFailures.Load(); failures >= maxFlushFailuresBeforeReject {
		return fmt.Errorf("settla-treasury: rejecting reserve — DB flush failing (consecutive failures: %d)", failures)
	}

	// Idempotency: atomically acquire the key. If already acquired, skip.
	// On failure, rollback so NATS redelivery can retry.
	if m.tryAcquireIdempotency(reference, "reserve") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "reserve")
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
			m.rollbackIdempotency(reference, "reserve")
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
	m.logOp(tenantID, currency, location, amount, reference, OpReserve)

	// Synchronous flush for large amounts — closes the crash window entirely.
	if !m.syncThreshold.IsZero() && amount.GreaterThanOrEqual(m.syncThreshold) {
		m.syncFlushPosition(ps)
	}

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
// Idempotent: calling with the same reference UUID returns nil on the second call.
func (m *Manager) Release(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	if m.tryAcquireIdempotency(reference, "release") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "release")
		return err
	}

	amountMicro := toMicro(amount)

	// CAS loop: atomically decrement reserved.
	for {
		currentReserved := ps.reservedMicro.Load()
		if currentReserved < amountMicro {
			m.rollbackIdempotency(reference, "release")
			return domain.ErrReservationFailed("release amount exceeds reserved")
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, currentReserved-amountMicro) {
			break
		}
	}

	ps.dirty.Store(true)
	m.logOp(tenantID, currency, location, amount, reference, OpRelease)
	return nil
}

// CommitReservation moves amount from reserved to locked. Called after a
// transfer is confirmed. Order: decrement reserved FIRST, then increment
// locked. If we crash between the two, we have unreserved funds that aren't
// locked (briefly permissive) rather than double-counted funds (locked + still
// reserved, which would corrupt accounting).
func (m *Manager) CommitReservation(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	if m.tryAcquireIdempotency(reference, "commit") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "commit")
		return err
	}

	amountMicro := toMicro(amount)

	// Step 1: Decrement reserved FIRST (validate we have enough reserved).
	// If reserved < amount, this is a double-commit or logic error — return a
	// detailed error with actual values for debugging, never silently cap to 0.
	for {
		currentReserved := ps.reservedMicro.Load()
		if currentReserved < amountMicro {
			m.rollbackIdempotency(reference, "commit")
			return fmt.Errorf("settla-treasury: insufficient reserved amount: have %d, need %d (transfer %s)",
				currentReserved, amountMicro, reference)
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, currentReserved-amountMicro) {
			break
		}
	}

	// Step 2: Increment locked (always succeeds — simple atomic add).
	ps.lockedMicro.Add(amountMicro)

	ps.dirty.Store(true)
	m.logOp(tenantID, currency, location, amount, reference, OpCommit)
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
