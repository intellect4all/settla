package reconciliation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// OutboxQuerier queries the outbox for health metrics.
type OutboxQuerier interface {
	// CountUnpublishedOlderThan returns the number of outbox entries that are
	// unpublished and were created before olderThan.
	CountUnpublishedOlderThan(ctx context.Context, olderThan time.Time) (int, error)
	// CountDefaultPartitionRows returns the number of rows in the default
	// partition, which should always be zero in normal operation.
	CountDefaultPartitionRows(ctx context.Context) (int, error)
}

// OutboxCheck verifies the health of the transactional outbox by checking for
// stale unpublished entries and rows leaked into the default partition.
type OutboxCheck struct {
	store  OutboxQuerier
	logger *slog.Logger
	maxAge time.Duration
}

// NewOutboxCheck creates an OutboxCheck. Pass 0 for maxAge to use the default of 5 minutes.
func NewOutboxCheck(store OutboxQuerier, logger *slog.Logger, maxAge time.Duration) *OutboxCheck {
	if maxAge == 0 {
		maxAge = 5 * time.Minute
	}
	return &OutboxCheck{
		store:  store,
		logger: logger,
		maxAge: maxAge,
	}
}

// Name returns the check identifier.
func (c *OutboxCheck) Name() string {
	return "outbox_health"
}

// Optional returns false.
func (c *OutboxCheck) Optional() bool { return false }

// Run checks for stale unpublished outbox entries and default partition leaks.
func (c *OutboxCheck) Run(ctx context.Context) (*CheckResult, error) {
	cutoff := time.Now().UTC().Add(-c.maxAge)

	unpublished, err := c.store.CountUnpublishedOlderThan(ctx, cutoff)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting unpublished outbox entries: %w", err)
	}

	defaultRows, err := c.store.CountDefaultPartitionRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation: counting default partition rows: %w", err)
	}

	var issues int
	var details []string

	if unpublished > 0 {
		issues += unpublished
		details = append(details, fmt.Sprintf(
			"%d unpublished entries older than %s", unpublished, c.maxAge,
		))
		c.logger.Warn("settla-reconciliation: stale outbox entries",
			slog.Int("count", unpublished),
			slog.Duration("max_age", c.maxAge),
		)
	}

	if defaultRows > 0 {
		issues += defaultRows
		details = append(details, fmt.Sprintf(
			"%d rows in default partition (expected 0)", defaultRows,
		))
		c.logger.Warn("settla-reconciliation: rows in default partition",
			slog.Int("count", defaultRows),
		)
	}

	status := "pass"
	if issues > 0 {
		status = "fail"
	}

	return &CheckResult{
		Name:       c.Name(),
		Status:     status,
		Details:    strings.Join(details, "; "),
		Mismatches: issues,
		CheckedAt:  time.Now().UTC(),
	}, nil
}
