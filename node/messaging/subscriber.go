package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
)

// EventHandler processes a domain event. Return nil to ack, error to nack.
type EventHandler func(ctx context.Context, event domain.Event) error

// Subscriber consumes events from a specific NATS JetStream partition.
// It creates a durable consumer filtered to the partition's subject pattern,
// with manual ack and exponential backoff on redelivery.
type Subscriber struct {
	js        jetstream.JetStream
	partition int
	logger    *slog.Logger
	cancel    context.CancelFunc
}

// NewSubscriber creates a subscriber for a specific partition.
func NewSubscriber(client *Client, partition int) *Subscriber {
	return &Subscriber{
		js:        client.JS,
		partition: partition,
		logger:    client.Logger.With("component", "subscriber", "partition", partition),
	}
}

// Subscribe creates a durable consumer for this partition and begins consuming
// messages. The handler is called for each event. Events are manually acked
// on success, or nacked with exponential backoff on failure.
//
// After MaxRetries (5) failed deliveries, the message is acked and logged
// as dead-lettered (NATS JetStream does not have native DLQ; the message
// is effectively discarded after exhausting retries with WorkQueue retention).
//
// Subscribe blocks until the context is cancelled.
func (s *Subscriber) Subscribe(ctx context.Context, handler EventHandler) error {
	filter := PartitionFilter(s.partition)
	consumerName := ConsumerName(s.partition)

	stream, err := s.js.Stream(ctx, StreamName)
	if err != nil {
		return fmt.Errorf("settla-messaging: getting stream %s: %w", StreamName, err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       AckWait,
		MaxDeliver:    MaxRetries,
		BackOff:       BackoffSchedule,
	})
	if err != nil {
		return fmt.Errorf("settla-messaging: creating consumer %s: %w", consumerName, err)
	}

	s.logger.Info("settla-messaging: consumer started",
		"consumer", consumerName,
		"filter", filter,
	)

	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Consume messages with the handler
	cons, err := consumer.Consume(func(msg jetstream.Msg) {
		s.handleMessage(subCtx, msg, handler)
	})
	if err != nil {
		cancel()
		return fmt.Errorf("settla-messaging: starting consume on %s: %w", consumerName, err)
	}

	// Block until context cancelled
	<-subCtx.Done()
	cons.Stop()

	s.logger.Info("settla-messaging: consumer stopped", "consumer", consumerName)
	return nil
}

// handleMessage deserialises the event, calls the handler, and acks or nacks.
func (s *Subscriber) handleMessage(ctx context.Context, msg jetstream.Msg, handler EventHandler) {
	var event domain.Event
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		s.logger.Error("settla-messaging: failed to unmarshal event, acking to discard",
			"subject", msg.Subject(),
			"error", err,
		)
		_ = msg.Ack()
		return
	}

	metadata, _ := msg.Metadata()
	deliveryCount := uint64(1)
	if metadata != nil {
		deliveryCount = metadata.NumDelivered
	}

	if deliveryCount >= uint64(MaxRetries) {
		// Dead-letter: log and ack to remove from the queue
		s.logger.Error("settla-messaging: dead-lettering event after max retries",
			"event_id", event.ID,
			"event_type", event.Type,
			"tenant_id", event.TenantID,
			"partition", s.partition,
			"deliveries", deliveryCount,
		)
		_ = msg.Ack()
		return
	}

	if err := handler(ctx, event); err != nil {
		s.logger.Warn("settla-messaging: handler failed, nacking for retry",
			"event_id", event.ID,
			"event_type", event.Type,
			"tenant_id", event.TenantID,
			"partition", s.partition,
			"delivery", deliveryCount,
			"error", err,
		)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()

	s.logger.Debug("settla-messaging: event processed",
		"event_id", event.ID,
		"event_type", event.Type,
		"tenant_id", event.TenantID,
		"partition", s.partition,
	)
}

// Stop cancels the subscription.
func (s *Subscriber) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}
