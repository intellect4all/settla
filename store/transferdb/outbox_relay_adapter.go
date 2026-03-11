package transferdb

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/node/outbox"
)

// Compile-time check that OutboxRelayAdapter satisfies outbox.OutboxStore.
var _ outbox.OutboxStore = (*OutboxRelayAdapter)(nil)

// OutboxRelayAdapter adapts SQLC-generated Queries to the outbox.OutboxStore interface
// used by the outbox relay.
type OutboxRelayAdapter struct {
	q *Queries
}

// NewOutboxRelayAdapter creates a new adapter for the outbox relay.
func NewOutboxRelayAdapter(q *Queries) *OutboxRelayAdapter {
	return &OutboxRelayAdapter{q: q}
}

func (a *OutboxRelayAdapter) GetUnpublishedEntries(ctx context.Context, limit int32) ([]outbox.OutboxRow, error) {
	rows, err := a.q.GetUnpublishedEntries(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]outbox.OutboxRow, len(rows))
	for i, row := range rows {
		result[i] = outbox.OutboxRow{
			ID:            row.ID,
			AggregateType: row.AggregateType,
			AggregateID:   row.AggregateID,
			TenantID:      row.TenantID,
			EventType:     row.EventType,
			Payload:       row.Payload,
			IsIntent:      row.IsIntent,
			Published:     row.Published,
			RetryCount:    row.RetryCount,
			MaxRetries:    row.MaxRetries,
			CreatedAt:     row.CreatedAt,
		}
	}
	return result, nil
}

func (a *OutboxRelayAdapter) MarkPublished(ctx context.Context, id uuid.UUID, createdAt time.Time) error {
	return a.q.MarkPublished(ctx, MarkPublishedParams{
		ID:        id,
		CreatedAt: createdAt,
	})
}

func (a *OutboxRelayAdapter) MarkFailed(ctx context.Context, id uuid.UUID, createdAt time.Time) error {
	return a.q.MarkFailed(ctx, MarkFailedParams{
		ID:        id,
		CreatedAt: createdAt,
	})
}
