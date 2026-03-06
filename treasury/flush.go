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

// flushLoop is the background goroutine that periodically writes dirty
// in-memory positions to Postgres.
func (m *Manager) flushLoop() {
	defer close(m.doneCh)

	ticker := time.NewTicker(m.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.flushOnce()
		case <-m.stopCh:
			// Final flush before exit.
			m.flushOnce()
			return
		}
	}
}

// flushOnce persists all dirty positions to Postgres. On failure, logs the
// error and retries on the next interval — in-memory state is authoritative
// and won't be lost.
func (m *Manager) flushOnce() {
	start := time.Now()

	dirty := m.dirtyPositions()
	if len(dirty) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	flushed := 0
	for _, ps := range dirty {
		balance := fromMicro(ps.balanceMicro.Load())
		locked := fromMicro(ps.lockedMicro.Load() + ps.reservedMicro.Load())

		if err := m.store.UpdatePosition(ctx, ps.ID, balance, locked); err != nil {
			m.logger.Error("settla-treasury: flush position update failed",
				"position_id", ps.ID,
				"tenant_id", ps.TenantID,
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

