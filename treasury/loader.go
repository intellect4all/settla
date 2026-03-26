package treasury

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LoadPositions reads all positions from Postgres into the in-memory map.
// Must be called before Start and before accepting traffic.
//
// Crash recovery: if the store supports ReserveOpStore, uncommitted reserve
// operations are replayed against the in-memory state. The idempotency map
// prevents double-application if NATS also redelivers the same operations.
func (m *Manager) LoadPositions(ctx context.Context) error {
	totalStart := time.Now()

	// Phase 1: Load positions from DB in batches.
	loadStart := time.Now()

	// Clear all indexes on reload (fresh start).
	m.mu.Lock()
	m.positions = make(map[positionKey]*PositionState)
	m.tenantIndex = make(map[uuid.UUID][]*PositionState)
	m.mu.Unlock()
	m.dirtyMu.Lock()
	m.dirtySet = make(map[positionKey]*PositionState)
	m.dirtyMu.Unlock()
	for i := range m.idempotencyShards {
		shard := &m.idempotencyShards[i]
		shard.mu.Lock()
		shard.items = make(map[string]time.Time)
		shard.mu.Unlock()
	}

	// Stream positions in batches of 1000 to avoid a single multi-million-element allocation.
	const positionBatchSize int32 = 1000
	var totalLoaded int
	var offset int32
	for {
		positions, err := m.store.LoadPositionsPaginated(ctx, positionBatchSize, offset)
		if err != nil {
			return fmt.Errorf("settla-treasury: loading positions batch at offset %d: %w", offset, err)
		}
		for _, pos := range positions {
			m.addPosition(pos)
		}
		totalLoaded += len(positions)
		if int32(len(positions)) < positionBatchSize {
			break
		}
		offset += positionBatchSize
	}
	loadDuration := time.Since(loadStart)

	// Phase 2: Replay uncommitted reserve ops from DB (crash recovery).
	// Set replaying=true so that Reserve/Release/Commit skip logOp() — the ops
	// are already WAL-logged and we don't want to fill the pendingOps channel
	// before the flush loop starts.
	replayStart := time.Now()
	replayed := 0
	m.replaying.Store(true)
	if opStore, ok := m.store.(ReserveOpStore); ok {
		ops, err := opStore.GetUncommittedOps(ctx)
		if err != nil {
			m.logger.Error("settla-treasury: failed to load uncommitted ops for replay", "error", err)
			// Non-fatal: positions are loaded, ops will be retried by NATS workers.
		} else {
			for _, op := range ops {
				if err := m.replayOp(ctx, op); err != nil {
					m.logger.Warn("settla-treasury: failed to replay op",
						"op_id", op.ID,
						"op_type", op.OpType,
						"reference", op.Reference,
						"error", err,
					)
				} else {
					replayed++
				}
			}
		}
	}
	m.replaying.Store(false)
	replayDuration := time.Since(replayStart)

	// Record recovery metrics.
	if m.metrics != nil {
		m.metrics.TreasuryRecoveryDuration.WithLabelValues("load_positions").Observe(loadDuration.Seconds())
		m.metrics.TreasuryRecoveryDuration.WithLabelValues("replay_ops").Observe(replayDuration.Seconds())
		m.metrics.TreasuryRecoveryDuration.WithLabelValues("total").Observe(time.Since(totalStart).Seconds())
		m.metrics.TreasuryPositionsRecoveredTotal.Add(float64(totalLoaded))
	}

	m.logger.Info("settla-treasury: loaded positions into memory",
		"count", totalLoaded,
		"replayed_ops", replayed,
		"load_duration", loadDuration,
		"replay_duration", replayDuration,
		"total_duration", time.Since(totalStart),
	)

	return nil
}

// replayOp applies a single reserve operation to in-memory state.
// Uses the idempotency map so it's safe to replay the same op twice.
func (m *Manager) replayOp(ctx context.Context, op ReserveOp) error {
	switch op.OpType {
	case OpReserve:
		return m.Reserve(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference)
	case OpRelease:
		return m.Release(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference)
	case OpCommit:
		return m.CommitReservation(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference)
	case OpConsume:
		return m.ConsumeReservation(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference)
	case OpCredit:
		return m.CreditBalance(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference, "recovery")
	case OpDebit:
		return m.DebitBalance(ctx, op.TenantID, op.Currency, op.Location, op.Amount, op.Reference, "recovery")
	default:
		return fmt.Errorf("settla-treasury: unknown op type: %s", op.OpType)
	}
}
