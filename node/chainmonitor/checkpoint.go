package chainmonitor

import (
	"context"
	"fmt"

	"github.com/intellect4all/settla/domain"
)

// CheckpointStore abstracts the block checkpoint persistence.
type CheckpointStore interface {
	GetCheckpoint(ctx context.Context, chain string) (*domain.BlockCheckpoint, error)
	UpsertCheckpoint(ctx context.Context, chain string, blockNumber int64, blockHash string) error
}

// CheckpointManager loads and saves per-chain block checkpoints.
type CheckpointManager struct {
	store CheckpointStore
}

// NewCheckpointManager creates a checkpoint manager.
func NewCheckpointManager(store CheckpointStore) *CheckpointManager {
	return &CheckpointManager{store: store}
}

// Load retrieves the last saved checkpoint for a chain.
// Returns (0, "") if no checkpoint exists.
func (m *CheckpointManager) Load(ctx context.Context, chain string) (blockNumber int64, blockHash string, err error) {
	cp, err := m.store.GetCheckpoint(ctx, chain)
	if err != nil {
		return 0, "", fmt.Errorf("settla-chainmonitor: loading checkpoint for %s: %w", chain, err)
	}
	if cp == nil {
		return 0, "", nil
	}
	return cp.BlockNumber, cp.BlockHash, nil
}

// Save persists the block checkpoint.
func (m *CheckpointManager) Save(ctx context.Context, chain string, blockNumber int64, blockHash string) error {
	if err := m.store.UpsertCheckpoint(ctx, chain, blockNumber, blockHash); err != nil {
		return fmt.Errorf("settla-chainmonitor: saving checkpoint for %s at block %d: %w", chain, blockNumber, err)
	}
	return nil
}
