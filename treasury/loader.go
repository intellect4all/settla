package treasury

import (
	"context"
	"fmt"
	"time"
)

// LoadPositions reads all positions from Postgres into the in-memory map.
// Must be called before Start and before accepting traffic.
//
// Crash recovery: if the store supports ReserveOpStore, uncommitted reserve
// operations are replayed against the in-memory state. The idempotency map
// prevents double-application if NATS also redelivers the same operations.
func (m *Manager) LoadPositions(ctx context.Context) error {
	start := time.Now()

	positions, err := m.store.LoadAllPositions(ctx)
	if err != nil {
		return fmt.Errorf("settla-treasury: loading positions from store: %w", err)
	}

	// Clear idempotency map on reload (fresh start).
	m.idempotencyMu.Lock()
	m.idempotencyMap = make(map[string]time.Time)
	m.idempotencyMu.Unlock()

	for _, pos := range positions {
		m.addPosition(pos)
	}

	// Replay uncommitted reserve ops from DB (crash recovery).
	replayed := 0
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

	m.logger.Info("settla-treasury: loaded positions into memory",
		"count", len(positions),
		"replayed_ops", replayed,
		"duration", time.Since(start),
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
	default:
		return fmt.Errorf("settla-treasury: unknown op type: %s", op.OpType)
	}
}
