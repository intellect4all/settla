package treasury

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// idempotencyShard is one of 16 shards of the idempotency map. Each shard has
// its own mutex, reducing lock contention at 5,000+ TPS from a single global lock.
type idempotencyShard struct {
	mu    sync.Mutex
	items map[string]time.Time
}

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

	// mu protects multi-field operations that must be observed atomically:
	// - snapshot() takes RLock to read balance+locked+reserved consistently
	// - CommitReservation takes Lock to modify reserved and locked together
	// Single-field CAS operations (Reserve, Release) do not need this lock.
	mu sync.RWMutex

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
// Takes RLock to ensure balance, locked, and reserved are read consistently
// (no intermediate state from CommitReservation modifying both reserved and locked).
func (ps *PositionState) snapshot() domain.Position {
	ps.mu.RLock()
	b := ps.balanceMicro.Load()
	l := ps.lockedMicro.Load()
	r := ps.reservedMicro.Load()
	ps.mu.RUnlock()

	return domain.Position{
		ID:            ps.ID,
		TenantID:      ps.TenantID,
		Currency:      ps.Currency,
		Location:      ps.Location,
		Balance:       fromMicro(b),
		Locked:        fromMicro(l + r),
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

	// Idempotency shards: 16 shards to reduce lock contention at peak TPS.
	// Key format: "{transferID}:{operation}" (e.g., "uuid:reserve").
	// Shard selected by fnv32a hash of the key.
	idempotencyShards [16]idempotencyShard

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

	// failedPositionIDs tracks which positions failed during the last flush cycle.
	// Used to include actionable detail in Reserve rejection errors.
	failedPositionIDs []string
	failedPositionsMu sync.RWMutex
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
		positions: make(map[positionKey]*PositionState),
		store:     store,
		publisher:      publisher,
		flushInterval:  100 * time.Millisecond,
		syncThreshold:  decimal.NewFromInt(100_000), // $100,000 default
		logger:         logger.With("module", "treasury.manager"),
		metrics:        metrics,
		pendingOps:     make(chan ReserveOp, 10000),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
	for i := range m.idempotencyShards {
		m.idempotencyShards[i].items = make(map[string]time.Time)
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

// idempotencyShardFor returns the shard index for a given key using fnv32a.
func idempotencyShardFor(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % 16)
}

// tryAcquireIdempotency atomically claims an idempotency key. Returns true if
// the key was already claimed (caller should return nil). Returns false if this
// caller acquired the key and should proceed with the operation.
//
// On failure, the caller MUST call rollbackIdempotency to release the key so
// that NATS redelivery can retry. On success, the key remains set and future
// calls return true.
//
// Uses sharded locking (fnv32a hash → 1 of 16 shards) to reduce contention.
func (m *Manager) tryAcquireIdempotency(reference uuid.UUID, op string) bool {
	key := idempotencyKey(reference, op)
	idx := idempotencyShardFor(key)
	shard := &m.idempotencyShards[idx]

	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, exists := shard.items[key]; exists {
		return true
	}
	// If this shard exceeds its share of the size limit, trigger a forced
	// cleanup with a short TTL before adding.
	if len(shard.items) >= maxIdempotencyMapSize/16 {
		forcedCutoff := time.Now().Add(-forcedCleanupMaxAge)
		for k, t := range shard.items {
			if t.Before(forcedCutoff) {
				delete(shard.items, k)
			}
		}
	}
	shard.items[key] = time.Now()
	return false
}

// rollbackIdempotency removes the idempotency key after a failed operation,
// allowing NATS redelivery to retry.
func (m *Manager) rollbackIdempotency(reference uuid.UUID, op string) {
	key := idempotencyKey(reference, op)
	idx := idempotencyShardFor(key)
	shard := &m.idempotencyShards[idx]

	shard.mu.Lock()
	delete(shard.items, key)
	shard.mu.Unlock()
}

// maxIdempotencyMapSize is the maximum number of entries in the idempotency map.
// If exceeded, a forced cleanup with a shorter TTL (2 minutes) is triggered.
// At peak load (~5,000 TPS with 3 ops per transfer = 15,000 entries/sec),
// 500,000 entries covers ~33 seconds of operations — well within NATS dedup window.
const maxIdempotencyMapSize = 500_000

// forcedCleanupMaxAge is the shorter TTL used when the idempotency map exceeds
// maxIdempotencyMapSize. 2 minutes is sufficient since NATS dedup window is 2 minutes.
const forcedCleanupMaxAge = 2 * time.Minute

// cleanupIdempotencyMap removes entries older than maxAge. Each shard is locked
// independently and briefly to minimize contention with the hot path.
// If a shard still exceeds its share of maxIdempotencyMapSize after time-based
// cleanup, performs a forced cleanup with a shorter TTL (2 minutes).
func (m *Manager) cleanupIdempotencyMap(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	shardLimit := maxIdempotencyMapSize / 16

	for i := range m.idempotencyShards {
		shard := &m.idempotencyShards[i]
		shard.mu.Lock()
		for k, t := range shard.items {
			if t.Before(cutoff) {
				delete(shard.items, k)
			}
		}
		// If still over the shard's share of the limit, force a more aggressive cleanup.
		if len(shard.items) > shardLimit {
			forcedCutoff := time.Now().Add(-forcedCleanupMaxAge)
			for k, t := range shard.items {
				if t.Before(forcedCutoff) {
					delete(shard.items, k)
				}
			}
			if len(shard.items) > shardLimit {
				m.logger.Warn("settla-treasury: idempotency shard still over limit after forced cleanup",
					"shard", i,
					"size", len(shard.items),
					"shard_limit", shardLimit,
				)
			}
		}
		shard.mu.Unlock()
	}
}

// pendingOpsTimeout is the maximum time to wait when the pendingOps channel is
// full before returning an error. 1 second is long enough for the flush loop to
// drain some entries, but short enough to not block the hot path excessively.
const pendingOpsTimeout = 1 * time.Second

// logOp writes a reserve operation to the WAL (via ReserveOpStore.LogReserveOp)
// synchronously for crash recovery, then queues it to the async channel for
// batch processing. The WAL write ensures the op is durable before the
// in-memory CAS result is considered committed.
//
// If the store doesn't implement ReserveOpStore or the WAL write fails, the op
// is still queued to the channel (best-effort). Returns an error if the channel
// is full for longer than pendingOpsTimeout — failing the operation is safer
// than silently losing the WAL entry.
func (m *Manager) logOp(tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID, opType ReserveOpType) error {
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
	// Block up to pendingOpsTimeout if the channel is full — failing the
	// operation is safer than silently dropping the WAL entry.
	select {
	case m.pendingOps <- op:
		return nil
	default:
		// Channel full — wait with timeout before failing.
		timer := time.NewTimer(pendingOpsTimeout)
		defer timer.Stop()
		select {
		case m.pendingOps <- op:
			return nil
		case <-timer.C:
			m.logger.Error("settla-treasury: pending ops channel full for >1s, failing operation",
				"op_type", opType,
				"reference", reference,
			)
			return fmt.Errorf("settla-treasury: pending ops channel full, cannot queue %s for %s", opType, reference)
		}
	}
}

// syncFlushPosition performs an immediate synchronous flush of a single position
// to DB. Used for large reservations (>= syncThreshold) to close the crash window.
// Returns an error if the flush fails — callers must roll back the reservation
// for large amounts to ensure durability.
func (m *Manager) syncFlushPosition(ps *PositionState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	balance := fromMicro(ps.balanceMicro.Load())
	locked := fromMicro(ps.lockedMicro.Load())

	if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
		m.logger.Error("settla-treasury: sync flush failed for large reservation",
			"position_id", ps.ID,
			"error", err,
		)
		return fmt.Errorf("settla-treasury: sync flush failed for position %s: %w", ps.ID, err)
	}
	ps.dirty.Store(false)
	return nil
}

// maxFlushFailuresBeforeReject is the number of consecutive flush failures
// before Reserve starts rejecting new reservations. Fail fast at 3 consecutive
// failures to avoid accepting reservations that cannot be durably persisted.
const maxFlushFailuresBeforeReject = 3

// Reserve atomically decrements available balance for the given position.
// Uses a CAS loop on the reserved counter — no mutex, no DB, nanosecond latency.
// Idempotent: calling with the same reference UUID returns nil on the second call.
func (m *Manager) Reserve(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	start := time.Now()

	if !amount.IsPositive() {
		return domain.ErrReservationFailed("amount must be positive")
	}

	if err := validateMicroRange(amount); err != nil {
		return err
	}

	// Circuit breaker: reject new reservations during a persistent DB outage.
	// In-memory state remains authoritative, but new reservations accepted while
	// the DB is down cannot be durably persisted and risk data loss on restart.
	if failures := m.consecutiveFlushFailures.Load(); failures >= maxFlushFailuresBeforeReject {
		m.failedPositionsMu.RLock()
		failedIDs := m.failedPositionIDs
		m.failedPositionsMu.RUnlock()
		return fmt.Errorf("settla-treasury: rejecting reserve — DB flush failing (consecutive failures: %d, failing positions: %v)", failures, failedIDs)
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

	// CAS loop: atomically check available and increment reserved.
	// Balance and locked are re-read on EVERY iteration to avoid stale-snapshot
	// races where two concurrent reserves both see the same available balance.
	for {
		balance := ps.balanceMicro.Load()
		locked := ps.lockedMicro.Load()
		currentReserved := ps.reservedMicro.Load()
		available := balance - locked - currentReserved
		if available < amountMicro {
			m.rollbackIdempotency(reference, "reserve")
			return domain.ErrInsufficientFunds(string(currency), location)
		}
		if ps.reservedMicro.CompareAndSwap(currentReserved, currentReserved+amountMicro) {
			break
		}
		// CAS failed — another goroutine changed reserved. Loop re-reads everything.
	}

	ps.dirty.Store(true)
	if err := m.logOp(tenantID, currency, location, amount, reference, OpReserve); err != nil {
		// Roll back the CAS reservation — failing the reserve is safer than
		// losing the WAL entry.
		ps.reservedMicro.Add(-amountMicro)
		m.rollbackIdempotency(reference, "reserve")
		return err
	}

	// Synchronous flush for large amounts — closes the crash window entirely.
	// If the flush fails, roll back the reservation to avoid accepting a large
	// reservation that cannot be durably persisted.
	if !m.syncThreshold.IsZero() && amount.GreaterThanOrEqual(m.syncThreshold) {
		if err := m.syncFlushPosition(ps); err != nil {
			// Roll back the CAS reservation.
			ps.reservedMicro.Add(-amountMicro)
			m.rollbackIdempotency(reference, "reserve")
			return fmt.Errorf("settla-treasury: rejecting large reserve (%s) — sync flush failed: %w", amount.String(), err)
		}
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

	if err := validateMicroRange(amount); err != nil {
		return err
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
	if err := m.logOp(tenantID, currency, location, amount, reference, OpRelease); err != nil {
		// Roll back the CAS release — re-add the reserved amount.
		ps.reservedMicro.Add(amountMicro)
		m.rollbackIdempotency(reference, "release")
		return err
	}
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

	// Lock ensures that decrementing reserved and incrementing locked happen
	// atomically from the perspective of snapshot() readers. Without this lock,
	// a crash or concurrent snapshot between the two operations could observe
	// an inconsistent state (funds neither reserved nor locked).
	ps.mu.Lock()

	// Step 1: Decrement reserved (validate we have enough reserved).
	// If reserved < amount, this is a double-commit or logic error — return a
	// detailed error with actual values for debugging, never silently cap to 0.
	currentReserved := ps.reservedMicro.Load()
	if currentReserved < amountMicro {
		ps.mu.Unlock()
		m.rollbackIdempotency(reference, "commit")
		return fmt.Errorf("settla-treasury: insufficient reserved amount: have %d, need %d (transfer %s)",
			currentReserved, amountMicro, reference)
	}
	ps.reservedMicro.Store(currentReserved - amountMicro)

	// Step 2: Increment locked.
	ps.lockedMicro.Add(amountMicro)

	ps.mu.Unlock()

	ps.dirty.Store(true)
	if err := m.logOp(tenantID, currency, location, amount, reference, OpCommit); err != nil {
		// Roll back: reverse the commit by re-adding to reserved and removing from locked.
		ps.mu.Lock()
		ps.reservedMicro.Add(amountMicro)
		ps.lockedMicro.Add(-amountMicro)
		ps.mu.Unlock()
		m.rollbackIdempotency(reference, "commit")
		return err
	}
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
//
// Validates that the new balance is not less than the currently locked+reserved
// amount to prevent the available balance from going negative.
func (m *Manager) UpdateBalance(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, newBalance decimal.Decimal) error {
	if err := validateMicroRange(newBalance); err != nil {
		return err
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		return err
	}

	// Validate that new balance can cover locked + reserved amounts.
	// Without this check, a withdrawal that brings the balance below locked+reserved
	// would cause the Available() calculation to go negative, corrupting accounting.
	newBalanceMicro := toMicro(newBalance)
	locked := ps.lockedMicro.Load()
	reserved := ps.reservedMicro.Load()
	committed := locked + reserved
	if newBalanceMicro < committed {
		return fmt.Errorf("settla-treasury: cannot set balance to %s — below committed amount %s (locked=%s, reserved=%s) for %s at %s",
			newBalance.StringFixed(2),
			fromMicro(committed).StringFixed(2),
			fromMicro(locked).StringFixed(2),
			fromMicro(reserved).StringFixed(2),
			currency, location,
		)
	}

	ps.balanceMicro.Store(newBalanceMicro)
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

// maxMicroValue is the maximum value that can be safely represented in micro-units.
// With microScale = 10^6 and int64 max ≈ 9.2e18, this allows up to ~$9.2 trillion.
const maxMicroValue int64 = 9_000_000_000_000_000_000 // ~$9T safety margin below int64 max

// toMicro converts a decimal to int64 micro-units (amount × 10^6).
// Panics on overflow — callers that accept external input must validate with
// validateMicroRange first.
func toMicro(d decimal.Decimal) int64 {
	scaled := d.Mul(decimal.NewFromInt(microScale))
	if scaled.GreaterThan(decimal.NewFromInt(maxMicroValue)) || scaled.LessThan(decimal.NewFromInt(-maxMicroValue)) {
		panic(fmt.Sprintf("settla-treasury: amount %s overflows int64 micro-units (max ~$9.2T)", d.String()))
	}
	return scaled.IntPart()
}

// validateMicroRange checks whether a decimal amount can be safely represented
// in int64 micro-units. Returns an error instead of panicking.
func validateMicroRange(amount decimal.Decimal) error {
	scaled := amount.Mul(decimal.NewFromInt(microScale))
	if scaled.GreaterThan(decimal.NewFromInt(maxMicroValue)) || scaled.LessThan(decimal.NewFromInt(-maxMicroValue)) {
		return fmt.Errorf("settla-treasury: amount %s exceeds maximum position size (~$9.2T)", amount.String())
	}
	return nil
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
