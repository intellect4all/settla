package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Event type constants follow past-tense convention for completed actions
// and present-tense for initiated actions.
const (
	EventTransferCreated       = "transfer.created"
	EventTransferFunded        = "transfer.funded"
	EventOnRampInitiated       = "onramp.initiated"
	EventOnRampCompleted       = "onramp.completed"
	EventSettlementStarted     = "settlement.started"
	EventSettlementCompleted   = "settlement.completed"
	EventOffRampInitiated      = "offramp.initiated"
	EventOffRampCompleted      = "offramp.completed"
	EventTransferCompleted     = "transfer.completed"
	EventTransferFailed        = "transfer.failed"
	EventRefundInitiated       = "refund.initiated"
	EventRefundCompleted       = "refund.completed"
	EventPositionUpdated       = "position.updated"
	EventLiquidityAlert        = "liquidity.alert"
)

// Event is the envelope for all domain events published via NATS JetStream.
// Events are partitioned by tenant hash: settla.transfer.partition.{N}.{event_type}
type Event struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Type      string
	Timestamp time.Time
	Data      any
}

// EventPublisher emits domain events for async processing by Settla Node.
// Events are published to NATS JetStream, partitioned by tenant hash
// across 8 partitions by default.
type EventPublisher interface {
	// Publish sends an event to the message bus.
	Publish(ctx context.Context, event Event) error
}

// EventSubscriber consumes domain events from NATS JetStream.
type EventSubscriber interface {
	// Subscribe registers a handler for a specific event type.
	Subscribe(ctx context.Context, eventType string, handler func(Event) error) error
}
