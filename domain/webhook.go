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

// ProviderWebhookLog records an inbound webhook from a payment provider.
// Stored before normalization so raw payloads survive normalizer failures.
// Used for deduplication, debugging, issue resolution, and replay.
type ProviderWebhookLog struct {
	ID              uuid.UUID
	ProviderSlug    string
	IdempotencyKey  string
	TransferID      *uuid.UUID        // set after normalization
	TenantID        *uuid.UUID        // set after normalization
	RawBody         []byte
	Normalized      []byte            // JSON of ProviderWebhookPayload, null before normalization
	Status          string            // received, processed, skipped, failed, duplicate
	ErrorMessage    string
	HTTPHeaders     map[string]string
	SourceIP        string
	CreatedAt       time.Time
	ProcessedAt     *time.Time
}

// Webhook log statuses.
const (
	WebhookLogReceived  = "received"
	WebhookLogProcessed = "processed"
	WebhookLogSkipped   = "skipped"  // non-terminal status (e.g., "pending")
	WebhookLogFailed    = "failed"   // normalization or processing error
	WebhookLogDuplicate = "duplicate"
)

// WebhookEventTypes Available webhook event types that tenants can subscribe to.
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
