package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/intellect4all/settla/domain"
)

// WebhookManagementStore provides webhook delivery and subscription management.
type WebhookManagementStore interface {
	// Delivery logs
	InsertWebhookDelivery(ctx context.Context, d *domain.WebhookDelivery) error
	ListWebhookDeliveries(ctx context.Context, tenantID uuid.UUID, eventType, status string, pageSize, pageOffset int32) ([]domain.WebhookDelivery, int64, error)
	GetWebhookDelivery(ctx context.Context, tenantID, deliveryID uuid.UUID) (*domain.WebhookDelivery, error)
	GetWebhookDeliveryStats(ctx context.Context, tenantID uuid.UUID, since time.Time) (*domain.WebhookDeliveryStats, error)

	// Event subscriptions
	ListWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID) ([]domain.WebhookEventSubscription, error)
	UpdateWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID, eventTypes []string) ([]domain.WebhookEventSubscription, error)
}

// WebhookAdapter implements WebhookManagementStore using SQLC-generated queries.
type WebhookAdapter struct {
	q *Queries
}

// NewWebhookAdapter creates a new WebhookAdapter.
func NewWebhookAdapter(q *Queries) *WebhookAdapter {
	return &WebhookAdapter{q: q}
}

func (a *WebhookAdapter) InsertWebhookDelivery(ctx context.Context, d *domain.WebhookDelivery) error {
	var transferID pgtype.UUID
	if d.TransferID != nil {
		transferID = pgtype.UUID{Bytes: *d.TransferID, Valid: true}
	}
	var statusCode pgtype.Int4
	if d.StatusCode != nil {
		statusCode = pgtype.Int4{Int32: *d.StatusCode, Valid: true}
	}
	var durationMs pgtype.Int4
	if d.DurationMs != nil {
		durationMs = pgtype.Int4{Int32: *d.DurationMs, Valid: true}
	}
	var deliveredAt pgtype.Timestamptz
	if d.DeliveredAt != nil {
		deliveredAt = pgtype.Timestamptz{Time: *d.DeliveredAt, Valid: true}
	}
	var nextRetryAt pgtype.Timestamptz
	if d.NextRetryAt != nil {
		nextRetryAt = pgtype.Timestamptz{Time: *d.NextRetryAt, Valid: true}
	}

	row, err := a.q.InsertWebhookDelivery(ctx, InsertWebhookDeliveryParams{
		TenantID:     d.TenantID,
		EventType:    d.EventType,
		TransferID:   transferID,
		DeliveryID:   d.DeliveryID,
		WebhookUrl:   d.WebhookURL,
		Status:       d.Status,
		StatusCode:   statusCode,
		Attempt:      d.Attempt,
		MaxAttempts:  d.MaxAttempts,
		ErrorMessage: textFromString(d.ErrorMessage),
		RequestBody:  d.RequestBody,
		DurationMs:   durationMs,
		DeliveredAt:  deliveredAt,
		NextRetryAt:  nextRetryAt,
	})
	if err != nil {
		return fmt.Errorf("settla-webhook: inserting delivery log: %w", err)
	}
	d.ID = row.ID
	d.CreatedAt = row.CreatedAt
	return nil
}

func (a *WebhookAdapter) ListWebhookDeliveries(ctx context.Context, tenantID uuid.UUID, eventType, status string, pageSize, pageOffset int32) ([]domain.WebhookDelivery, int64, error) {
	rows, err := a.q.ListWebhookDeliveries(ctx, ListWebhookDeliveriesParams{
		TenantID:     tenantID,
		EventType:    eventType,
		StatusFilter: status,
		PageSize:     pageSize,
		PageOffset:   pageOffset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("settla-webhook: listing deliveries: %w", err)
	}

	count, err := a.q.CountWebhookDeliveries(ctx, CountWebhookDeliveriesParams{
		TenantID:     tenantID,
		EventType:    eventType,
		StatusFilter: status,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("settla-webhook: counting deliveries: %w", err)
	}

	deliveries := make([]domain.WebhookDelivery, len(rows))
	for i, row := range rows {
		deliveries[i] = domain.WebhookDelivery{
			ID:           row.ID,
			TenantID:     row.TenantID,
			EventType:    row.EventType,
			DeliveryID:   row.DeliveryID,
			WebhookURL:   row.WebhookUrl,
			Status:       row.Status,
			Attempt:      row.Attempt,
			MaxAttempts:  row.MaxAttempts,
			ErrorMessage: stringFromText(row.ErrorMessage),
			CreatedAt:    row.CreatedAt,
		}
		if row.TransferID.Valid {
			id := uuid.UUID(row.TransferID.Bytes)
			deliveries[i].TransferID = &id
		}
		if row.StatusCode.Valid {
			sc := row.StatusCode.Int32
			deliveries[i].StatusCode = &sc
		}
		if row.DurationMs.Valid {
			dm := row.DurationMs.Int32
			deliveries[i].DurationMs = &dm
		}
		if row.DeliveredAt.Valid {
			t := row.DeliveredAt.Time
			deliveries[i].DeliveredAt = &t
		}
	}

	return deliveries, count, nil
}

func (a *WebhookAdapter) GetWebhookDelivery(ctx context.Context, tenantID, deliveryID uuid.UUID) (*domain.WebhookDelivery, error) {
	row, err := a.q.GetWebhookDelivery(ctx, GetWebhookDeliveryParams{
		DeliveryID: deliveryID,
		TenantID:   tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-webhook: getting delivery %s: %w", deliveryID, err)
	}

	d := &domain.WebhookDelivery{
		ID:           row.ID,
		TenantID:     row.TenantID,
		EventType:    row.EventType,
		DeliveryID:   row.DeliveryID,
		WebhookURL:   row.WebhookUrl,
		Status:       row.Status,
		Attempt:      row.Attempt,
		MaxAttempts:  row.MaxAttempts,
		ErrorMessage: stringFromText(row.ErrorMessage),
		RequestBody:  row.RequestBody,
		CreatedAt:    row.CreatedAt,
	}
	if row.TransferID.Valid {
		id := uuid.UUID(row.TransferID.Bytes)
		d.TransferID = &id
	}
	if row.StatusCode.Valid {
		sc := row.StatusCode.Int32
		d.StatusCode = &sc
	}
	if row.DurationMs.Valid {
		dm := row.DurationMs.Int32
		d.DurationMs = &dm
	}
	if row.DeliveredAt.Valid {
		t := row.DeliveredAt.Time
		d.DeliveredAt = &t
	}
	if row.NextRetryAt.Valid {
		t := row.NextRetryAt.Time
		d.NextRetryAt = &t
	}

	return d, nil
}

func (a *WebhookAdapter) GetWebhookDeliveryStats(ctx context.Context, tenantID uuid.UUID, since time.Time) (*domain.WebhookDeliveryStats, error) {
	row, err := a.q.GetWebhookDeliveryStats(ctx, GetWebhookDeliveryStatsParams{
		TenantID: tenantID,
		Since:    since,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-webhook: getting delivery stats: %w", err)
	}

	return &domain.WebhookDeliveryStats{
		TotalDeliveries: row.TotalDeliveries,
		Successful:      row.Successful,
		Failed:          row.Failed,
		DeadLettered:    row.DeadLettered,
		Pending:         row.Pending,
		AvgLatencyMs:    row.AvgLatencyMs,
		P95LatencyMs:    row.P95LatencyMs,
	}, nil
}

func (a *WebhookAdapter) ListWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID) ([]domain.WebhookEventSubscription, error) {
	rows, err := a.q.ListWebhookEventSubscriptions(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-webhook: listing event subscriptions: %w", err)
	}

	subs := make([]domain.WebhookEventSubscription, len(rows))
	for i, row := range rows {
		subs[i] = domain.WebhookEventSubscription{
			ID:        row.ID,
			TenantID:  row.TenantID,
			EventType: row.EventType,
			CreatedAt: row.CreatedAt,
		}
	}
	return subs, nil
}

func (a *WebhookAdapter) UpdateWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID, eventTypes []string) ([]domain.WebhookEventSubscription, error) {
	// Clear all existing subscriptions, then re-insert the new set
	if err := a.q.DeleteAllWebhookEventSubscriptions(ctx, tenantID); err != nil {
		return nil, fmt.Errorf("settla-webhook: clearing event subscriptions: %w", err)
	}

	subs := make([]domain.WebhookEventSubscription, 0, len(eventTypes))
	for _, et := range eventTypes {
		row, err := a.q.UpsertWebhookEventSubscription(ctx, UpsertWebhookEventSubscriptionParams{
			TenantID:  tenantID,
			EventType: et,
		})
		if err != nil {
			return nil, fmt.Errorf("settla-webhook: upserting subscription %s: %w", et, err)
		}
		subs = append(subs, domain.WebhookEventSubscription{
			ID:        row.ID,
			TenantID:  row.TenantID,
			EventType: row.EventType,
			CreatedAt: row.CreatedAt,
		})
	}

	// Update the denormalized webhook_events array on tenants table
	if err := a.q.UpdateTenantWebhookEvents(ctx, UpdateTenantWebhookEventsParams{
		TenantID: tenantID,
		Events:   eventTypes,
	}); err != nil {
		return nil, fmt.Errorf("settla-webhook: updating tenant webhook events: %w", err)
	}

	return subs, nil
}

