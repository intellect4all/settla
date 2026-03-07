package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
)

// Compile-time check: Publisher implements domain.EventPublisher.
var _ domain.EventPublisher = (*Publisher)(nil)

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

// Publish routes an event to its partition and publishes to JetStream.
// The event.ID is used as the NATS message dedup ID (Nats-Msg-Id header)
// to guarantee exactly-once publishing within the stream's dedup window.
//
// Subject format: settla.transfer.partition.{N}.{event_type}
// where N = fnv32a(tenant_id) % num_partitions
func (p *Publisher) Publish(ctx context.Context, event domain.Event) error {
	partition := TenantPartition(event.TenantID, p.numPartitions)
	subject := PartitionSubject(partition, event.Type)

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
		"partition", partition,
		"subject", subject,
	)

	return nil
}
