package recovery

import (
	"context"
	"time"

	"github.com/intellect4all/settla/domain"
)

// escalate creates a manual_reviews record for a stuck transfer and logs
// a warning. Escalation is idempotent: if the transfer already has an active
// review, this method returns nil without creating a duplicate.
func (d *Detector) escalate(ctx context.Context, transfer *domain.Transfer, stuckSince time.Time) error {
	// Check if already escalated (idempotent)
	hasReview, err := d.reviewStore.HasActiveReview(ctx, transfer.ID)
	if err != nil {
		return err
	}
	if hasReview {
		return nil // already escalated
	}

	err = d.reviewStore.CreateManualReview(ctx, transfer.ID, transfer.TenantID,
		string(transfer.Status), stuckSince)
	if err != nil {
		return err
	}

	d.logger.Warn("settla-recovery: transfer escalated to manual review",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"status", transfer.Status,
		"stuck_since", stuckSince,
	)

	if d.metrics != nil {
		d.metrics.EscalationsCreated.Inc()
	}

	return nil
}
