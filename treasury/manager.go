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
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
	"github.com/shopspring/decimal"
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
const microScale int64 = 1_000_000

// positionKey uniquely identifies a treasury position in memory.
type positionKey struct {
	TenantID uuid.UUID
	Currency string
	Location string
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
	positions   map[positionKey]*PositionState
	tenantIndex map[uuid.UUID][]*PositionState // O(1) tenant lookup, avoids full-scan
	mu          sync.RWMutex                   // protects map reads/writes, NOT individual positions

	// dirtySet tracks positions modified since the last flush. Only these
	// positions are written to Postgres — avoids scanning all positions.
	dirtySet map[positionKey]*PositionState
	dirtyMu  sync.Mutex // separate lock so Reserve/Release don't contend with flush

	// Idempotency shards: 16 shards to reduce lock contention at peak TPS.
	// Key format: "{transferID}:{operation}" (e.g., "uuid:reserve").
	// Shard selected by fnv32a hash of the key.
	idempotencyShards [16]idempotencyShard

	store         Store
	publisher     domain.EventPublisher
	flushInterval time.Duration

	// syncThresholds holds per-currency thresholds above which Reserve
	// performs a synchronous DB flush. Defaults approximate $100,000 USD
	// equivalent per currency. syncThresholdDefault is used for currencies
	// not in the map (e.g. newly added currencies).
	syncThresholds       map[domain.Currency]decimal.Decimal
	syncThresholdDefault decimal.Decimal

	logger  *slog.Logger
	metrics *observability.Metrics

	// Crash recovery: pending reserve ops queued by Reserve/Release/Commit,
	// drained by flushOnce() and batch-inserted to DB. Capacity 10000 to avoid
	// blocking the hot path even under peak load.
	pendingOps chan ReserveOp

	// pendingEvents queues position events for batch insertion by the event
	// writer goroutine. All treasury operations (Reserve, Release, Credit, Debit,
	// Consume) queue events here. The writer drains every 10ms and batch-inserts.
	// Capacity 100,000 to cover ~5 seconds of peak load without blocking.
	pendingEvents chan domain.PositionEvent

	// eventWriterDone signals that the event writer goroutine has exited.
	eventWriterDone chan struct{}

	// Flush lifecycle.
	stopCh chan struct{}
	doneCh chan struct{}

	// replaying is true during startup crash recovery replay. When set, Reserve/
	// Release/Commit/etc skip logOp() since ops are already WAL-logged. This
	// prevents the pendingOps channel from filling up when there are thousands of
	// uncommitted ops to replay before the flush loop starts.
	replaying atomic.Bool

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
		positions:            make(map[positionKey]*PositionState),
		tenantIndex:          make(map[uuid.UUID][]*PositionState),
		dirtySet:             make(map[positionKey]*PositionState),
		store:                store,
		publisher:            publisher,
		flushInterval:        100 * time.Millisecond,
		syncThresholds:       DefaultSyncThresholds(),
		syncThresholdDefault: decimal.NewFromInt(100_000), // USD-equivalent fallback
		logger:               logger.With("module", "treasury.manager"),
		metrics:              metrics,
		pendingOps:           make(chan ReserveOp, 10000),
		pendingEvents:        make(chan domain.PositionEvent, 100_000),
		eventWriterDone:      make(chan struct{}),
		stopCh:               make(chan struct{}),
		doneCh:               make(chan struct{}),
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

// DefaultSyncThresholds returns per-currency sync flush thresholds that
// approximate $100,000 USD equivalent. These are conservative estimates —
// operators should tune via SETTLA_TREASURY_SYNC_THRESHOLDS for live rates.
//
// Approximate rates used (as of 2025):
//
//	USD/USDT/USDC: $100,000  (1:1)
//	GBP:           £80,000   (~$100K at 1.25)
//	EUR:           €92,000   (~$100K at 1.09)
//	NGN:          ₦160,000,000  (~$100K at 1600)
//	GHS:          ₵1,500,000    (~$100K at 15)
//	KES:          KSh13,000,000 (~$100K at 130)
func DefaultSyncThresholds() map[domain.Currency]decimal.Decimal {
	return map[domain.Currency]decimal.Decimal{
		domain.CurrencyUSD:  decimal.NewFromInt(100_000),
		domain.CurrencyUSDT: decimal.NewFromInt(100_000),
		domain.CurrencyUSDC: decimal.NewFromInt(100_000),
		domain.CurrencyGBP:  decimal.NewFromInt(80_000),
		domain.CurrencyEUR:  decimal.NewFromInt(92_000),
		domain.CurrencyNGN:  decimal.NewFromInt(160_000_000),
		domain.CurrencyGHS:  decimal.NewFromInt(1_500_000),
		domain.CurrencyKES:  decimal.NewFromInt(13_000_000),
	}
}

// syncThresholdFor returns the sync flush threshold for the given currency.
// Returns the per-currency threshold if configured, otherwise the default.
// Returns zero (sync flush disabled) if the default is also zero.
func (m *Manager) syncThresholdFor(currency domain.Currency) decimal.Decimal {
	if threshold, ok := m.syncThresholds[currency]; ok {
		return threshold
	}
	return m.syncThresholdDefault
}

// WithSyncThresholds sets per-currency sync flush thresholds. Each entry
// defines the amount above which a Reserve for that currency triggers an
// immediate synchronous DB flush. Merges with (and overrides) defaults.
func WithSyncThresholds(thresholds map[domain.Currency]decimal.Decimal) Option {
	return func(m *Manager) {
		for currency, amount := range thresholds {
			m.syncThresholds[currency] = amount
		}
	}
}

// WithSyncThresholdDefault sets the fallback sync threshold for currencies
// not in the per-currency map. Set to 0 to disable sync flush for unknown currencies.
func WithSyncThresholdDefault(amount decimal.Decimal) Option {
	return func(m *Manager) {
		m.syncThresholdDefault = amount
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
	// During startup replay, ops are already WAL-logged — skip both the DB
	// write and the channel enqueue to avoid saturating the pendingOps channel
	// before the flush loop starts.
	if m.replaying.Load() {
		return nil
	}

	op := ReserveOp{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		Currency:  currency,
		Location:  location,
		Amount:    amount,
		Reference: reference,
		OpType:    opType,
		CreatedAt: time.Now().UTC(),
	}

	// WAL: synchronous write to DB if store supports it.
	// Hard-fail if the WAL write fails — proceeding without a durable record
	// risks data loss on crash. The caller rolls back the in-memory state.
	if opStore, ok := m.store.(ReserveOpStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := opStore.LogReserveOp(ctx, op); err != nil {
			m.logger.Error("settla-treasury: WAL write failed, rejecting operation",
				"op_type", opType,
				"reference", reference,
				"error", err,
			)
			return fmt.Errorf("settla-treasury: WAL write failed for %s %s: %w", opType, reference, err)
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
	key := positionKey{TenantID: ps.TenantID, Currency: string(ps.Currency), Location: ps.Location}
	m.dirtyMu.Lock()
	delete(m.dirtySet, key)
	m.dirtyMu.Unlock()
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

	// TODO: Look into this
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

	m.markDirty(ps)
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
	if threshold := m.syncThresholdFor(currency); !threshold.IsZero() && amount.GreaterThanOrEqual(threshold) {
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

	m.markDirty(ps)
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

	m.markDirty(ps)
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
// Uses the tenantIndex for O(tenant positions) lookup instead of scanning all positions.
func (m *Manager) GetPositions(ctx context.Context, tenantID uuid.UUID) ([]domain.Position, error) {
	m.mu.RLock()
	states := m.tenantIndex[tenantID]
	m.mu.RUnlock()

	result := make([]domain.Position, 0, len(states))
	for _, ps := range states {
		result = append(result, ps.snapshot())
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

// UpdateBalance directly sets the absolute balance for a position. This is an
// escape hatch for reconciliation and ledger sync — not for normal operations.
// Normal balance changes flow through CreditBalance/DebitBalance/Reserve/etc.
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
	m.markDirty(ps)
	return nil
}

// queueEvent sends a position event to the batch writer channel. Non-blocking:
// if the channel is full, logs a warning and drops the event. The event is
// best-effort for audit; the in-memory CAS is already committed and the
// reserve_ops WAL provides crash recovery for Reserve/Release/Commit/Consume.
func (m *Manager) queueEvent(ps *PositionState, eventType domain.PositionEventType, amount decimal.Decimal, reference uuid.UUID, refType string) {
	balance := fromMicro(ps.balanceMicro.Load())
	locked := fromMicro(ps.lockedMicro.Load() + ps.reservedMicro.Load())
	event := domain.PositionEvent{
		ID:             uuid.Must(uuid.NewV7()),
		PositionID:     ps.ID,
		TenantID:       ps.TenantID,
		EventType:      eventType,
		Amount:         amount,
		BalanceAfter:   balance,
		LockedAfter:    locked,
		ReferenceID:    reference,
		ReferenceType:  refType,
		IdempotencyKey: idempotencyKey(reference, string(eventType)),
		RecordedAt:     time.Now().UTC(),
	}
	select {
	case m.pendingEvents <- event:
	default:
		m.logger.Warn("settla-treasury: event channel full, dropping position event",
			"position_id", ps.ID,
			"event_type", eventType,
			"reference", reference,
		)
	}
}

// CreditBalance atomically increases the balance for a position.
// Used when money enters the tenant's position: deposit confirmations, payment
// link payments, stablecoin compensation, manual top-ups, and rebalancing.
//
// Idempotent: calling with the same reference UUID returns nil on repeat.
func (m *Manager) CreditBalance(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID, refType string) error {
	if !amount.IsPositive() {
		return fmt.Errorf("settla-treasury: credit amount must be positive")
	}

	if err := validateMicroRange(amount); err != nil {
		return err
	}

	// Circuit breaker: reject during persistent DB outage.
	if failures := m.consecutiveFlushFailures.Load(); failures >= maxFlushFailuresBeforeReject {
		return fmt.Errorf("settla-treasury: rejecting credit — DB flush failing (consecutive failures: %d)", failures)
	}

	if m.tryAcquireIdempotency(reference, "credit") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "credit")
		return err
	}

	amountMicro := toMicro(amount)

	// Atomic add — credits always succeed (no contention with other ops on balanceMicro
	// because balance only increases here; Reserve/Release only touch reservedMicro).
	ps.balanceMicro.Add(amountMicro)

	m.markDirty(ps)
	m.queueEvent(ps, domain.PosEventCredit, amount, reference, refType)

	if err := m.logOp(tenantID, currency, location, amount, reference, OpCredit); err != nil {
		// Roll back the credit.
		ps.balanceMicro.Add(-amountMicro)
		m.rollbackIdempotency(reference, "credit")
		return err
	}

	// Sync flush for large credits — roll back if it fails to avoid accepting
	// a large credit that cannot be durably persisted (consistent with Reserve).
	threshold := m.syncThresholdFor(currency)
	if !threshold.IsZero() && amount.GreaterThanOrEqual(threshold) {
		if err := m.syncFlushPosition(ps); err != nil {
			ps.balanceMicro.Add(-amountMicro)
			m.rollbackIdempotency(reference, "credit")
			return fmt.Errorf("settla-treasury: rejecting large credit (%s) — sync flush failed: %w", amount.String(), err)
		}
	}

	return nil
}

// DebitBalance atomically decreases the balance for a position.
// Used for manual withdrawals and internal rebalancing (source side).
// Rejects if available balance (balance - locked - reserved) is less than amount.
//
// Idempotent: calling with the same reference UUID returns nil on repeat.
func (m *Manager) DebitBalance(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID, refType string) error {
	if !amount.IsPositive() {
		return fmt.Errorf("settla-treasury: debit amount must be positive")
	}

	if err := validateMicroRange(amount); err != nil {
		return err
	}

	if failures := m.consecutiveFlushFailures.Load(); failures >= maxFlushFailuresBeforeReject {
		return fmt.Errorf("settla-treasury: rejecting debit — DB flush failing (consecutive failures: %d)", failures)
	}

	if m.tryAcquireIdempotency(reference, "debit") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "debit")
		return err
	}

	amountMicro := toMicro(amount)

	// CAS loop: check available (balance - locked - reserved) >= amount, then subtract.
	for {
		currentBalance := ps.balanceMicro.Load()
		locked := ps.lockedMicro.Load()
		reserved := ps.reservedMicro.Load()
		available := currentBalance - locked - reserved

		if available < amountMicro {
			m.rollbackIdempotency(reference, "debit")
			return domain.ErrInsufficientFunds(string(currency), location)
		}

		if ps.balanceMicro.CompareAndSwap(currentBalance, currentBalance-amountMicro) {
			break
		}
	}

	m.markDirty(ps)
	m.queueEvent(ps, domain.PosEventDebit, amount, reference, refType)

	if err := m.logOp(tenantID, currency, location, amount, reference, OpDebit); err != nil {
		// Roll back the debit.
		ps.balanceMicro.Add(amountMicro)
		m.rollbackIdempotency(reference, "debit")
		return err
	}

	// Sync flush for large debits.
	threshold := m.syncThresholdFor(currency)
	if !threshold.IsZero() && amount.GreaterThanOrEqual(threshold) {
		if err := m.syncFlushPosition(ps); err != nil {
			m.logger.Error("settla-treasury: sync flush after large debit failed",
				"position_id", ps.ID,
				"amount", amount.StringFixed(2),
				"error", err,
			)
		}
	}

	return nil
}

// ConsumeReservation atomically decrements both reservedMicro and balanceMicro.
// Called when a transfer completes — the reserved funds are consumed because
// money physically left the tenant's position.
//
// Uses mu.Lock() because two fields must change atomically (same pattern as
// CommitReservation). Without the lock, a concurrent snapshot() could observe
// an intermediate state where reserved decreased but balance hasn't yet.
//
// Idempotent: calling with the same reference UUID returns nil on repeat.
func (m *Manager) ConsumeReservation(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string, amount decimal.Decimal, reference uuid.UUID) error {
	if !amount.IsPositive() {
		return domain.ErrReservationFailed("consume amount must be positive")
	}

	if err := validateMicroRange(amount); err != nil {
		return err
	}

	if m.tryAcquireIdempotency(reference, "consume") {
		return nil
	}

	ps, err := m.getPosition(tenantID, currency, location)
	if err != nil {
		m.rollbackIdempotency(reference, "consume")
		return err
	}

	amountMicro := toMicro(amount)

	// Lock ensures that decrementing reserved and balance happen atomically
	// from the perspective of snapshot() readers.
	ps.mu.Lock()

	currentReserved := ps.reservedMicro.Load()
	if currentReserved < amountMicro {
		ps.mu.Unlock()
		m.rollbackIdempotency(reference, "consume")
		return fmt.Errorf("settla-treasury: insufficient reserved amount for consume: have %d, need %d (ref %s)",
			currentReserved, amountMicro, reference)
	}

	currentBalance := ps.balanceMicro.Load()
	if currentBalance < amountMicro {
		ps.mu.Unlock()
		m.rollbackIdempotency(reference, "consume")
		return fmt.Errorf("settla-treasury: insufficient balance for consume: have %d, need %d (ref %s)",
			currentBalance, amountMicro, reference)
	}

	// Atomic: decrease reserved first (briefly permissive on crash), then balance.
	ps.reservedMicro.Store(currentReserved - amountMicro)
	ps.balanceMicro.Store(currentBalance - amountMicro)

	ps.mu.Unlock()

	m.markDirty(ps)
	m.queueEvent(ps, domain.PosEventConsume, amount, reference, "transfer")

	if err := m.logOp(tenantID, currency, location, amount, reference, OpConsume); err != nil {
		// Roll back: re-add both reserved and balance.
		ps.mu.Lock()
		ps.reservedMicro.Add(amountMicro)
		ps.balanceMicro.Add(amountMicro)
		ps.mu.Unlock()
		m.rollbackIdempotency(reference, "consume")
		return err
	}

	// Sync flush for large consumptions.
	threshold := m.syncThresholdFor(currency)
	if !threshold.IsZero() && amount.GreaterThanOrEqual(threshold) {
		if err := m.syncFlushPosition(ps); err != nil {
			m.logger.Error("settla-treasury: sync flush after large consume failed",
				"position_id", ps.ID,
				"amount", amount.StringFixed(2),
				"error", err,
			)
		}
	}

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
	m.tenantIndex[pos.TenantID] = append(m.tenantIndex[pos.TenantID], ps)
	m.mu.Unlock()
}

// markDirty flags a position as modified and adds it to the dirty set for flush.
// Uses a separate mutex from the positions map to avoid contention on the hot path.
func (m *Manager) markDirty(ps *PositionState) {
	ps.dirty.Store(true)
	key := positionKey{TenantID: ps.TenantID, Currency: string(ps.Currency), Location: ps.Location}
	m.dirtyMu.Lock()
	m.dirtySet[key] = ps
	m.dirtyMu.Unlock()
}

func (m *Manager) publishLiquidityAlert(ctx context.Context, ps *PositionState) {
	if m.publisher == nil {
		return
	}
	event := domain.Event{
		ID:        uuid.Must(uuid.NewV7()),
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

// dirtyPositions drains the dirty set and returns only positions modified since
// the last flush. O(dirty count) instead of O(all positions) — critical at 500K+ positions.
func (m *Manager) dirtyPositions() []*PositionState {
	m.dirtyMu.Lock()
	if len(m.dirtySet) == 0 {
		m.dirtyMu.Unlock()
		return nil
	}
	dirty := make([]*PositionState, 0, len(m.dirtySet))
	for _, ps := range m.dirtySet {
		dirty = append(dirty, ps)
	}
	// Swap to a fresh map — avoids deleting keys one by one.
	m.dirtySet = make(map[positionKey]*PositionState)
	m.dirtyMu.Unlock()
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
