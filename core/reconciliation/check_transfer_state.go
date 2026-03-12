package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/intellect4all/settla/domain"
)

// TransferQuerier counts transfers stuck in a given status older than a threshold.
type TransferQuerier interface {
	CountTransfersInStatus(ctx context.Context, status domain.TransferStatus, olderThan time.Time) (int, error)
}

// DefaultTransferThresholds are the maximum expected durations for non-terminal states.
var DefaultTransferThresholds = map[domain.TransferStatus]time.Duration{
	domain.TransferStatusFunded:     30 * time.Minute,
	domain.TransferStatusOnRamping:  2 * time.Hour,
	domain.TransferStatusSettling:   1 * time.Hour,
	domain.TransferStatusOffRamping: 2 * time.Hour,
}

// TransferStateCheck verifies no transfers are stuck in non-terminal states
// beyond configurable time thresholds.
type TransferStateCheck struct {
	store      TransferQuerier
	logger     *slog.Logger
	thresholds map[domain.TransferStatus]time.Duration
}

// NewTransferStateCheck creates a TransferStateCheck with default thresholds.
// Pass nil for thresholds to use defaults.
func NewTransferStateCheck(store TransferQuerier, logger *slog.Logger, thresholds map[domain.TransferStatus]time.Duration) *TransferStateCheck {
	if thresholds == nil {
		thresholds = DefaultTransferThresholds
	}
	return &TransferStateCheck{
		store:      store,
		logger:     logger,
		thresholds: thresholds,
	}
}

// Name returns the check identifier.
func (c *TransferStateCheck) Name() string {
	return "transfer_state_consistency"
}

// Run checks for transfers stuck beyond their expected duration in non-terminal states.
func (c *TransferStateCheck) Run(ctx context.Context) (*CheckResult, error) {
	var totalStuck int
	var details []string

	for status, threshold := range c.thresholds {
		cutoff := time.Now().UTC().Add(-threshold)
		count, err := c.store.CountTransfersInStatus(ctx, status, cutoff)
		if err != nil {
			return nil, fmt.Errorf("settla-reconciliation: counting transfers in %s: %w", status, err)
		}

		if count > 0 {
			totalStuck += count
			details = append(details, fmt.Sprintf(
				"%s: %d transfers stuck > %s",
				status, count, threshold,
			))
			c.logger.Warn("settla-reconciliation: stuck transfers detected",
				slog.String("status", string(status)),
				slog.Int("count", count),
				slog.Duration("threshold", threshold),
			)
		}
	}

	status := "pass"
	if totalStuck > 0 {
		status = "fail"
	}

	return &CheckResult{
		Name:       c.Name(),
		Status:     status,
		Details:    strings.Join(details, "; "),
		Mismatches: totalStuck,
		CheckedAt:  time.Now().UTC(),
	}, nil
}
