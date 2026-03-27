package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// RebalanceTreasuryReader provides read-only access to tenant positions.
type RebalanceTreasuryReader interface {
	GetPositions(ctx context.Context, tenantID uuid.UUID) ([]domain.Position, error)
}

// RebalanceTenantLister provides a list of active tenant IDs for scanning.
type RebalanceTenantLister interface {
	ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error)
}

// RebalancePositionEngine creates internal rebalance transactions.
type RebalancePositionEngine interface {
	RequestTopUp(ctx context.Context, tenantID uuid.UUID, req domain.TopUpRequest) (*domain.PositionTransaction, error)
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, req domain.WithdrawalRequest) (*domain.PositionTransaction, error)
}

// RebalanceAlertPublisher publishes position alert events.
type RebalanceAlertPublisher interface {
	Publish(ctx context.Context, event domain.Event) error
}

// PositionRebalanceWorker periodically scans all tenant positions and:
//   - For positions below MinBalance: finds surplus positions in the same tenant
//     and triggers an internal rebalance (debit source, credit destination).
//   - For positions below MinBalance with no internal source: publishes a
//     low-balance alert event to the webhook stream for tenant notification.
//
// Rate-limited: max 1 rebalance per position per cooldown period (default 5 min).
type PositionRebalanceWorker struct {
	treasury     RebalanceTreasuryReader
	tenantLister RebalanceTenantLister
	engine       RebalancePositionEngine
	publisher    RebalanceAlertPublisher
	interval     time.Duration
	cooldown     time.Duration
	logger       *slog.Logger

	// recentRebalances tracks position IDs that were recently rebalanced
	// to prevent over-triggering. Keyed by position ID, value is last rebalance time.
	recentMu          sync.Mutex
	recentRebalances  map[uuid.UUID]time.Time
}

// NewPositionRebalanceWorker creates a rebalance worker.
func NewPositionRebalanceWorker(
	treasury RebalanceTreasuryReader,
	tenantLister RebalanceTenantLister,
	engine RebalancePositionEngine,
	publisher RebalanceAlertPublisher,
	logger *slog.Logger,
) *PositionRebalanceWorker {
	return &PositionRebalanceWorker{
		treasury:         treasury,
		tenantLister:     tenantLister,
		engine:           engine,
		publisher:        publisher,
		interval:         30 * time.Second,
		cooldown:         5 * time.Minute,
		logger:           logger.With("module", "position-rebalance-worker"),
		recentRebalances: make(map[uuid.UUID]time.Time),
	}
}

// Run starts the periodic rebalance loop. Blocks until ctx is cancelled.
func (w *PositionRebalanceWorker) Run(ctx context.Context) error {
	w.logger.Info("settla-rebalance: starting", "interval", w.interval.String())

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("settla-rebalance: stopping")
			return ctx.Err()
		case <-ticker.C:
			w.runCycle(ctx)
		}
	}
}

func (w *PositionRebalanceWorker) runCycle(ctx context.Context) {
	tenantIDs, err := w.tenantLister.ListActiveTenantIDs(ctx)
	if err != nil {
		w.logger.Error("settla-rebalance: failed to list tenants", "error", err)
		return
	}

	for _, tenantID := range tenantIDs {
		if ctx.Err() != nil {
			return
		}
		w.checkTenant(ctx, tenantID)
	}

	// Cleanup expired cooldowns.
	w.cleanupCooldowns()
}

func (w *PositionRebalanceWorker) checkTenant(ctx context.Context, tenantID uuid.UUID) {
	positions, err := w.treasury.GetPositions(ctx, tenantID)
	if err != nil {
		w.logger.Warn("settla-rebalance: failed to get positions",
			"tenant_id", tenantID,
			"error", err,
		)
		return
	}

	for i := range positions {
		pos := &positions[i]

		// Skip positions without a min_balance threshold.
		if pos.MinBalance.IsZero() {
			continue
		}

		// Skip positions that are above the minimum.
		if pos.IsAboveMinimum() {
			continue
		}

		// Skip if recently rebalanced (cooldown).
		if w.isOnCooldown(pos.ID) {
			continue
		}

		deficit := pos.TargetBalance.Sub(pos.Balance)
		if !deficit.IsPositive() {
			// Balance is below min but target is also below balance — nothing to target.
			deficit = pos.MinBalance.Sub(pos.Balance)
		}
		if !deficit.IsPositive() {
			continue
		}

		// Look for a surplus position in the same tenant to rebalance from.
		source := w.findSurplusPosition(positions, pos, deficit)
		if source != nil {
			w.triggerRebalance(ctx, tenantID, source, pos, deficit)
		} else {
			w.publishAlert(ctx, tenantID, pos)
		}
	}
}

// findSurplusPosition looks for a position in the same tenant that has excess
// available balance above its own target (or min) balance. Returns nil if none found.
func (w *PositionRebalanceWorker) findSurplusPosition(
	positions []domain.Position,
	deficit *domain.Position,
	amount decimal.Decimal,
) *domain.Position {
	for i := range positions {
		candidate := &positions[i]

		// Skip the deficit position itself.
		if candidate.ID == deficit.ID {
			continue
		}

		// Skip if on cooldown.
		if w.isOnCooldown(candidate.ID) {
			continue
		}

		// Must have the same currency for a direct rebalance.
		if candidate.Currency != deficit.Currency {
			continue
		}

		// Check if the candidate has surplus above its own target.
		threshold := candidate.TargetBalance
		if threshold.IsZero() {
			threshold = candidate.MinBalance
		}

		surplus := candidate.Available().Sub(threshold)
		if surplus.GreaterThanOrEqual(amount) {
			return candidate
		}
	}
	return nil
}

func (w *PositionRebalanceWorker) triggerRebalance(
	ctx context.Context,
	tenantID uuid.UUID,
	source, dest *domain.Position,
	amount decimal.Decimal,
) {
	w.logger.Info("settla-rebalance: triggering internal rebalance",
		"tenant_id", tenantID,
		"source_location", source.Location,
		"dest_location", dest.Location,
		"currency", dest.Currency,
		"amount", amount.String(),
	)

	// Debit from source position.
	_, err := w.engine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency:    source.Currency,
		Location:    source.Location,
		Amount:      amount,
		Method:      "internal",
		Destination: string(dest.Currency) + ":" + dest.Location, // internal reference
	})
	if err != nil {
		w.logger.Error("settla-rebalance: debit from source failed",
			"tenant_id", tenantID,
			"source", source.Location,
			"error", err,
		)
		return
	}

	// Credit to destination position.
	_, err = w.engine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
		Currency: dest.Currency,
		Location: dest.Location,
		Amount:   amount,
		Method:   "internal",
	})
	if err != nil {
		w.logger.Error("settla-rebalance: credit to dest failed",
			"tenant_id", tenantID,
			"dest", dest.Location,
			"error", err,
		)
		// The withdrawal already went through — the position engine will handle
		// eventual consistency via the outbox pattern.
		return
	}

	// Mark both positions as recently rebalanced.
	w.setCooldown(source.ID)
	w.setCooldown(dest.ID)

	w.logger.Info("settla-rebalance: rebalance triggered",
		"tenant_id", tenantID,
		"source", source.Location,
		"dest", dest.Location,
		"amount", amount.String(),
	)
}

func (w *PositionRebalanceWorker) publishAlert(ctx context.Context, tenantID uuid.UUID, pos *domain.Position) {
	w.logger.Warn("settla-rebalance: low balance alert — no internal source available",
		"tenant_id", tenantID,
		"currency", pos.Currency,
		"location", pos.Location,
		"balance", pos.Balance.String(),
		"min_balance", pos.MinBalance.String(),
	)

	// Publish alert event for webhook delivery to tenant.
	event := domain.Event{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		Type:      domain.EventLiquidityAlert,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"position_id": pos.ID,
			"currency":    pos.Currency,
			"location":    pos.Location,
			"balance":     pos.Balance.String(),
			"available":   pos.Available().String(),
			"min_balance": pos.MinBalance.String(),
			"deficit":     pos.MinBalance.Sub(pos.Balance).String(),
		},
	}

	if err := w.publisher.Publish(ctx, event); err != nil {
		w.logger.Error("settla-rebalance: failed to publish alert",
			"tenant_id", tenantID,
			"error", err,
		)
	}

	// Set cooldown to avoid alert spam.
	w.setCooldown(pos.ID)
}

func (w *PositionRebalanceWorker) isOnCooldown(positionID uuid.UUID) bool {
	w.recentMu.Lock()
	defer w.recentMu.Unlock()
	lastTime, ok := w.recentRebalances[positionID]
	if !ok {
		return false
	}
	return time.Since(lastTime) < w.cooldown
}

func (w *PositionRebalanceWorker) setCooldown(positionID uuid.UUID) {
	w.recentMu.Lock()
	defer w.recentMu.Unlock()
	w.recentRebalances[positionID] = time.Now()
}

func (w *PositionRebalanceWorker) cleanupCooldowns() {
	w.recentMu.Lock()
	defer w.recentMu.Unlock()
	for id, t := range w.recentRebalances {
		if time.Since(t) > w.cooldown*2 {
			delete(w.recentRebalances, id)
		}
	}
}
