package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/resilience"
)

// Compile-time checks.
var _ domain.EventPublisher = (*Publisher)(nil)
var _ domain.EventPublisher = (*CircuitBreakerPublisher)(nil)

// Publisher publishes domain events to NATS JetStream with partition routing.
// Events are routed to partitions by hashing the tenant ID, ensuring all
// events for a given tenant land on the same partition for ordered processing.
type Publisher struct {
	js            jetstream.JetStream
	numPartitions int
	logger        *slog.Logger
}

// NewPublisher creates a partitioned event publisher.
func NewPublisher(client *Client) *Publisher {
	return &Publisher{
		js:            client.JS,
		numPartitions: client.NumPartitions,
		logger:        client.Logger.With("component", "publisher"),
	}
}

// Publish routes an event to the correct stream and subject based on event type.
// The event.ID is used as the NATS message dedup ID (Nats-Msg-Id header)
// to guarantee exactly-once publishing within the stream's dedup window.
//
// Transfer-related events use partitioned subjects:
//
//	settla.transfer.partition.{N}.{event_type}
//	where N = fnv32a(tenant_id) % num_partitions
//
// Other events route to their respective stream subjects via SubjectForEventType.
func (p *Publisher) Publish(ctx context.Context, event domain.Event) error {
	subject := SubjectForEventType(event.Type, event.TenantID, p.numPartitions)

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("settla-messaging: marshalling event %s: %w", event.ID, err)
	}

	// Publish with dedup via Nats-Msg-Id header (event.ID)
	_, err = p.js.Publish(ctx, subject, payload,
		jetstream.WithMsgID(event.ID.String()),
	)
	if err != nil {
		return fmt.Errorf("settla-messaging: publishing event %s to %s: %w", event.ID, subject, err)
	}

	p.logger.Debug("settla-messaging: published event",
		"event_id", event.ID,
		"event_type", event.Type,
		"tenant_id", event.TenantID,
		"subject", subject,
	)

	return nil
}

// CircuitBreakerPublisher wraps a domain.EventPublisher with circuit breaker
// protection. When the downstream publisher (typically NATS) is failing, the
// circuit opens and rejects requests immediately instead of piling up.
type CircuitBreakerPublisher struct {
	inner domain.EventPublisher
	cb    *resilience.CircuitBreaker
}

// NewCircuitBreakerPublisher wraps an EventPublisher with a circuit breaker.
func NewCircuitBreakerPublisher(inner domain.EventPublisher, cb *resilience.CircuitBreaker) *CircuitBreakerPublisher {
	return &CircuitBreakerPublisher{inner: inner, cb: cb}
}

// Publish delegates to the inner publisher through the circuit breaker.
func (p *CircuitBreakerPublisher) Publish(ctx context.Context, event domain.Event) error {
	return p.cb.Execute(ctx, func(ctx context.Context) error {
		return p.inner.Publish(ctx, event)
	})
}
