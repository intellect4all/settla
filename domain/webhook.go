package domain

import (
	"time"

	"github.com/google/uuid"
)

// WebhookDelivery represents a single webhook delivery attempt log entry.
type WebhookDelivery struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	EventType    string
	TransferID   *uuid.UUID
	DeliveryID   string
	WebhookURL   string
	Status       string // pending, delivered, failed, dead_letter
	StatusCode   *int32
	Attempt      int32
	MaxAttempts  int32
	ErrorMessage string
	RequestBody  []byte
	DurationMs   *int32
	CreatedAt    time.Time
	DeliveredAt  *time.Time
	NextRetryAt  *time.Time
}

// WebhookDeliveryStats contains aggregated delivery statistics for a tenant.
type WebhookDeliveryStats struct {
	TotalDeliveries int64
	Successful      int64
	Failed          int64
	DeadLettered    int64
	Pending         int64
	AvgLatencyMs    int32
	P95LatencyMs    int32
}

// WebhookEventSubscription represents a tenant's subscription to a specific event type.
type WebhookEventSubscription struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	EventType string
	CreatedAt time.Time
}

// Available webhook event types that tenants can subscribe to.
var WebhookEventTypes = []string{
	"transfer.created",
	"transfer.funded",
	"transfer.on_ramping",
	"transfer.settling",
	"transfer.off_ramping",
	"transfer.completing",
	"transfer.completed",
	"transfer.failed",
	"transfer.refunding",
	"transfer.refunded",
}
