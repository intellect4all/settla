package treasury

import (
	"context"
	"fmt"
	"time"
)

// LoadPositions reads all positions from Postgres into the in-memory map.
// Must be called before Start and before accepting traffic.
//
// Crash recovery: any reserved amounts from a previous run are discarded.
// The DB stores balance and locked (which includes committed reservations).
// Uncommitted reservations are lost on crash — the corresponding transfers
// will fail/timeout and naturally release the funds.
func (m *Manager) LoadPositions(ctx context.Context) error {
	start := time.Now()

	positions, err := m.store.LoadAllPositions(ctx)
	if err != nil {
		return fmt.Errorf("settla-treasury: loading positions from store: %w", err)
	}

	for _, pos := range positions {
		m.addPosition(pos)
	}

	m.logger.Info("settla-treasury: loaded positions into memory",
		"count", len(positions),
		"duration", time.Since(start),
	)

	return nil
}
