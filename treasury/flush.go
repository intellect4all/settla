package treasury

import (
	"context"
	"time"
)

// Start begins the background flush goroutine. It runs every flushInterval
// and persists dirty positions to Postgres. Call Stop to shut down gracefully.
func (m *Manager) Start() {
	go m.flushLoop()
}

// Stop signals the flush goroutine to exit, performs a final flush, and
// blocks until it completes.
func (m *Manager) Stop() {
	close(m.stopCh)
	<-m.doneCh
}

// idempotencyCleanupInterval defines how often old idempotency entries are purged.
const idempotencyCleanupInterval = 60 // every 60 flush ticks

// idempotencyMaxAge is how long idempotency entries are kept. 10 minutes covers
// the NATS redelivery window (backoff schedule max = 10 min).
const idempotencyMaxAge = 10 * time.Minute

// reserveOpsCleanupInterval defines how often old completed reserve ops are purged.
const reserveOpsCleanupInterval = 600 // every 600 flush ticks (~60s at 100ms interval)

// reserveOpsMaxAge is how long completed reserve ops are kept before deletion.
const reserveOpsMaxAge = 1 * time.Hour

// flushLoop is the background goroutine that periodically writes dirty
// in-memory positions to Postgres.
func (m *Manager) flushLoop() {
	defer close(m.doneCh)

	ticker := time.NewTicker(m.flushInterval)
	defer ticker.Stop()

	tickCount := 0
	for {
		select {
		case <-ticker.C:
			m.flushOnce()
			tickCount++
			if tickCount%idempotencyCleanupInterval == 0 {
				m.cleanupIdempotencyMap(idempotencyMaxAge)
			}
			if tickCount%reserveOpsCleanupInterval == 0 {
				m.cleanupOldReserveOps()
			}
		case <-m.stopCh:
			// Final flush before exit.
			m.flushOnce()
			return
		}
	}
}

// drainPendingOps drains the pendingOps channel. When the store implements
// ReserveOpStore, ops are already WAL-logged synchronously by logOp(), so we
// only need to drain the channel here (no batch re-insert needed). When the
// store does NOT implement ReserveOpStore, we simply drain and discard.
func (m *Manager) drainPendingOps(_ context.Context) {
	for {
		select {
		case <-m.pendingOps:
		default:
			return
		}
	}
}

// flushOnce persists all dirty positions to Postgres. Crash recovery model:
//
//  1. WAL (logOp): Each reserve/release/commit op is written synchronously to
//     the DB via ReserveOpStore.LogReserveOp BEFORE the in-memory CAS result is
//     considered committed. On restart, GetUncommittedOps replays these.
//  2. Channel drain: Batch-inserts any ops still in the pendingOps channel
//     (these are duplicates of WAL entries — LogReserveOps is idempotent).
//  3. Position flush: Iterates dirty positions and writes balance+locked to DB.
//     Only committed locked is flushed (NOT reserved) to avoid double-counting
//     on restart when reserve ops are replayed.
//  4. Cleanup: Old completed/matched reserve ops are periodically deleted.
//
// On failure, logs the error and retries on the next interval — in-memory
// state is authoritative and won't be lost.
func (m *Manager) flushOnce() {
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drain pending reserve ops to DB for crash recovery.
	m.drainPendingOps(ctx)

	dirty := m.dirtyPositions()
	if len(dirty) == 0 {
		return
	}

	flushed := 0
	hadError := false
	var failedPositionIDs []string
	for _, ps := range dirty {
		balance := fromMicro(ps.balanceMicro.Load())
		// Flush only committed locked — NOT reserved. Reserved amounts are
		// reconstructed from reserve_ops on crash recovery. This prevents
		// double-counting: if we flushed locked+reserved and then replayed
		// reserve ops on restart, the reserved amount would be counted twice.
		locked := fromMicro(ps.lockedMicro.Load())

		if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
			hadError = true
			failedPositionIDs = append(failedPositionIDs, ps.ID.String())
			m.logger.Error("settla-treasury: flush position update failed",
				"position_id", ps.ID,
				"tenant_id", ps.TenantID,
				"currency", ps.Currency,
				"location", ps.Location,
				"error", err,
			)
			// Don't clear dirty flag — retry next interval.
			continue
		}

		if err := m.store.RecordHistory(ctx, ps.ID, ps.TenantID, balance, locked, "flush"); err != nil {
			m.logger.Error("settla-treasury: flush history write failed",
				"position_id", ps.ID,
				"tenant_id", ps.TenantID,
				"error", err,
			)
			// Non-fatal: position was updated, history is best-effort.
		}

		ps.dirty.Store(false)
		flushed++

		// Update treasury gauges.
		if m.metrics != nil {
			balF, _ := balance.Float64()
			lockedF, _ := locked.Float64()
			m.metrics.TreasuryBalance.WithLabelValues(ps.TenantID.String(), string(ps.Currency), ps.Location).Set(balF)
			m.metrics.TreasuryLocked.WithLabelValues(ps.TenantID.String(), string(ps.Currency), ps.Location).Set(lockedF)
		}
	}

	// Track consecutive flush failures for persistent DB outage detection.
	// Store failing position IDs so Reserve can include them in rejection errors.
	if hadError {
		m.failedPositionsMu.Lock()
		m.failedPositionIDs = failedPositionIDs
		m.failedPositionsMu.Unlock()

		failures := m.consecutiveFlushFailures.Add(1)
		if failures >= 5 {
			m.logger.Error("settla-treasury: persistent flush failures — DB may be unavailable",
				"consecutive_failures", failures,
				"dirty_positions", len(dirty),
				"flushed", flushed,
				"failed_positions", failedPositionIDs,
			)
			if m.metrics != nil {
				m.metrics.TreasuryConsecutiveFlushFailures.Set(float64(failures))
			}
		}
	} else {
		m.failedPositionsMu.Lock()
		m.failedPositionIDs = nil
		m.failedPositionsMu.Unlock()
		m.consecutiveFlushFailures.Store(0)
		if m.metrics != nil {
			m.metrics.TreasuryConsecutiveFlushFailures.Set(0)
		}
	}

	if m.metrics != nil {
		m.metrics.TreasuryFlushDuration.Observe(time.Since(start).Seconds())
		m.metrics.TreasuryFlushLag.Set(time.Since(start).Seconds())
	}

	if flushed > 0 {
		m.logger.Debug("settla-treasury: flushed positions",
			"count", flushed,
			"total_dirty", len(dirty),
		)
	}
}

// cleanupOldReserveOps removes old completed/matched reserve ops from the DB.
// "Completed" means a reserve op has a matching commit or release in the ops table.
func (m *Manager) cleanupOldReserveOps() {
	opStore, ok := m.store.(ReserveOpStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-reserveOpsMaxAge)
	if err := opStore.CleanupOldOps(ctx, cutoff); err != nil {
		m.logger.Warn("settla-treasury: failed to cleanup old reserve ops", "error", err)
	}
}

// exportAllPositionMetrics exports gauge metrics for all positions (called periodically).
func (m *Manager) exportAllPositionMetrics() {
	if m.metrics == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ps := range m.positions {
		balance := fromMicro(ps.balanceMicro.Load())
		locked := fromMicro(ps.lockedMicro.Load() + ps.reservedMicro.Load())
		balF, _ := balance.Float64()
		lockedF, _ := locked.Float64()
		m.metrics.TreasuryBalance.WithLabelValues(ps.TenantID.String(), string(ps.Currency), ps.Location).Set(balF)
		m.metrics.TreasuryLocked.WithLabelValues(ps.TenantID.String(), string(ps.Currency), ps.Location).Set(lockedF)
	}
}

