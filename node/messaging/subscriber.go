package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
)

// EventHandler processes a domain event. Return nil to ack, error to nack.
type EventHandler func(ctx context.Context, event domain.Event) error

// SubscriberOption configures subscriber behavior.
type SubscriberOption func(*subscriberConfig)

type subscriberConfig struct {
	poolSize int
}

// WithPoolSize sets the worker pool size for concurrent message processing.
// Default is 1 (serial processing, backward-compatible).
func WithPoolSize(n int) SubscriberOption {
	return func(c *subscriberConfig) {
		if n > 0 {
			c.poolSize = n
		}
	}
}

func defaultSubscriberConfig() subscriberConfig {
	return subscriberConfig{poolSize: 1}
}

// Subscriber consumes events from a specific NATS JetStream partition.
// It creates a durable consumer filtered to the partition's subject pattern,
// with manual ack and exponential backoff on redelivery.
type Subscriber struct {
	client      *Client
	js          jetstream.JetStream
	partition   int
	poolSize    int
	workerPool  chan struct{}
	tenantLocks sync.Map // map[string]*sync.Mutex — per-tenant ordering
	logger      *slog.Logger
	cancel      context.CancelFunc
	draining    atomic.Bool
	inflight    sync.WaitGroup
}

// NewSubscriber creates a subscriber for a specific transfer partition.
func NewSubscriber(client *Client, partition int, opts ...SubscriberOption) *Subscriber {
	cfg := defaultSubscriberConfig()
	for _, o := range opts {
		o(&cfg)
	}

	var pool chan struct{}
	if cfg.poolSize > 1 {
		pool = make(chan struct{}, cfg.poolSize)
	}

	return &Subscriber{
		client:     client,
		js:         client.JS,
		partition:  partition,
		poolSize:   cfg.poolSize,
		workerPool: pool,
		logger:     client.Logger.With("component", "subscriber", "partition", partition),
	}
}

// tenantMutexEntry pairs a mutex with a last-used timestamp for idle eviction.
type tenantMutexEntry struct {
	mu       sync.Mutex
	lastUsed atomic.Int64 // unix seconds
}

// getTenantMutex returns the per-tenant mutex, creating one if needed.
func (s *Subscriber) getTenantMutex(tenantID string) *sync.Mutex {
	v, _ := s.tenantLocks.LoadOrStore(tenantID, &tenantMutexEntry{})
	entry := v.(*tenantMutexEntry)
	entry.lastUsed.Store(time.Now().Unix())
	return &entry.mu
}

// cleanupTenantLocks periodically evicts tenant mutexes idle for more than 5 minutes.
func (s *Subscriber) cleanupTenantLocks(ctx context.Context) {
	const idleTimeout = 5 * time.Minute
	ticker := time.NewTicker(idleTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-idleTimeout).Unix()
			s.tenantLocks.Range(func(key, value any) bool {
				entry := value.(*tenantMutexEntry)
				if entry.lastUsed.Load() < cutoff {
					s.tenantLocks.Delete(key)
				}
				return true
			})
		}
	}
}

// Subscribe creates a durable consumer for this partition and begins consuming
// messages. The handler is called for each event. Events are manually acked
// on success, or nacked with exponential backoff on failure.
//
// After MaxDeliver (6) exhausted, the message is published to the DLQ subject,
// acked, and removed from the work queue.
//
// Subscribe blocks until the context is cancelled.
func (s *Subscriber) Subscribe(ctx context.Context, handler EventHandler) error {
	filter := PartitionFilter(s.partition)
	consumerName := ConsumerName(s.partition)

	return s.subscribeInternal(ctx, StreamTransfers, consumerName, filter, handler)
}

// subscribeInternal is the shared implementation for partition and stream subscriptions.
func (s *Subscriber) subscribeInternal(ctx context.Context, streamName, consumerName, filterSubject string, handler EventHandler) error {
	stream, err := s.js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("settla-messaging: getting stream %s: %w", streamName, err)
	}

	maxAckPending := s.poolSize * 4
	if maxAckPending < 100 {
		maxAckPending = 100
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       AckWait,
		MaxDeliver:    MaxRetries,
		BackOff:       BackoffSchedule,
		MaxAckPending: maxAckPending,
	})
	if err != nil {
		return fmt.Errorf("settla-messaging: creating consumer %s: %w", consumerName, err)
	}

	s.logger.Info("settla-messaging: consumer started",
		"consumer", consumerName,
		"stream", streamName,
		"filter", filterSubject,
		"pool_size", s.poolSize,
	)

	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Start tenant lock cleanup goroutine to evict idle entries
	go s.cleanupTenantLocks(subCtx)

	// Consume messages with the handler
	cons, err := consumer.Consume(func(msg jetstream.Msg) {
		if s.poolSize <= 1 {
			s.handleMessage(subCtx, msg, handler, streamName)
			return
		}
		s.dispatchPooled(subCtx, msg, handler, streamName)
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

// handleMessage deserialises the event, calls the handler, and acks or nacks (serial path).
func (s *Subscriber) handleMessage(ctx context.Context, msg jetstream.Msg, handler EventHandler, streamName string) {
	if s.draining.Load() {
		_ = msg.Nak()
		return
	}
	s.inflight.Add(1)
	defer s.inflight.Done()

	processMessage(ctx, msg, handler, streamName, s.client, s.logger, nil)
}

// dispatchPooled dispatches message processing to a pooled goroutine with per-tenant ordering.
// The semaphore slot is acquired synchronously in the NATS callback to provide backpressure.
func (s *Subscriber) dispatchPooled(ctx context.Context, msg jetstream.Msg, handler EventHandler, streamName string) {
	// Acquire semaphore slot synchronously — blocks the NATS callback for backpressure.
	select {
	case s.workerPool <- struct{}{}:
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		s.logger.Error("settla-messaging: pool slot acquisition timeout", "stream", streamName)
		_ = msg.Nak()
		return
	}
	if s.draining.Load() {
		<-s.workerPool
		_ = msg.Nak()
		return
	}
	s.inflight.Add(1)

	go func() {
		defer s.inflight.Done()
		defer func() { <-s.workerPool }()

		// Extract tenant ID for per-tenant ordering.
		event, err := unmarshalEvent(msg.Data())
		if err != nil {
			// Let processMessage handle the error (DLQ routing, etc.)
			processMessage(ctx, msg, handler, streamName, s.client, s.logger, nil)
			return
		}

		tenantID := event.TenantID.String()
		mu := s.getTenantMutex(tenantID)
		mu.Lock()
		defer mu.Unlock()

		processMessage(ctx, msg, handler, streamName, s.client, s.logger, &event)
	}()
}

// processMessage is the shared message processing logic for both serial and pooled paths.
// When preParsed is non-nil the unmarshal step is skipped (the caller already parsed the event).
func processMessage(ctx context.Context, msg jetstream.Msg, handler EventHandler, streamName string, client *Client, logger *slog.Logger, preParsed *domain.Event) {
	var event domain.Event
	if preParsed != nil {
		event = *preParsed
	} else {
		var err error
		event, err = unmarshalEvent(msg.Data())
		if err != nil {
			logger.Error("settla-messaging: failed to unmarshal event, routing to DLQ",
				"subject", msg.Subject(),
				"error", err,
			)
			if client != nil {
				if dlqErr := client.PublishDLQ(ctx, streamName, "unmarshal_error", msg.Data()); dlqErr != nil {
					logger.Error("settla-messaging: DLQ publish failed for unmarshal error, NAKing",
						"error", dlqErr)
					_ = msg.Nak()
					return
				}
			}
			_ = msg.Ack()
			return
		}
	}

	metadata, _ := msg.Metadata()
	deliveryCount := uint64(1)
	if metadata != nil {
		deliveryCount = metadata.NumDelivered
	}

	if deliveryCount >= uint64(MaxRetries) {
		logger.Error("settla-messaging: dead-lettering event after max retries",
			"event_id", event.ID,
			"event_type", event.Type,
			"tenant_id", event.TenantID,
			"stream", streamName,
			"deliveries", deliveryCount,
		)
		if client != nil {
			if dlqErr := client.PublishDLQ(ctx, streamName, event.Type, msg.Data()); dlqErr != nil {
				logger.Error("settla-messaging: DLQ publish failed after max retries, NAKing for redelivery",
					"error", dlqErr, "event_id", event.ID)
				_ = msg.Nak()
				return
			}
		}
		_ = msg.Ack()
		return
	}

	if err := handler(ctx, event); err != nil {
		logger.Warn("settla-messaging: handler failed, nacking for retry",
			"event_id", event.ID,
			"event_type", event.Type,
			"tenant_id", event.TenantID,
			"stream", streamName,
			"delivery", deliveryCount,
			"error", err,
		)
		_ = msg.NakWithDelay(nakDelay(deliveryCount))
		return
	}

	_ = msg.Ack()

	logger.Debug("settla-messaging: event processed",
		"event_id", event.ID,
		"event_type", event.Type,
		"tenant_id", event.TenantID,
		"stream", streamName,
	)
}

// wireEvent is the JSON shape published by the outbox relay.
// It differs from domain.Event in field names (event_type vs type, payload vs data).
type wireEvent struct {
	ID        uuid.UUID       `json:"id"`
	TenantID  uuid.UUID       `json:"tenant_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// unmarshalEvent deserialises a NATS message into a domain.Event.
// It handles both the outbox wire format (event_type/payload) and the direct
// publisher format (type/data) for backwards compatibility.
func unmarshalEvent(data []byte) (domain.Event, error) {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return domain.Event{}, fmt.Errorf("settla-messaging: unmarshal wire event: %w", err)
	}

	// Determine which format was used.
	eventType := w.EventType
	if eventType == "" {
		// Fallback: try domain.Event-style keys (type/data).
		var fallback struct {
			Type string    `json:"type"`
			Ts   time.Time `json:"timestamp"`
		}
		_ = json.Unmarshal(data, &fallback)
		eventType = fallback.Type
		if fallback.Ts.IsZero() {
			w.CreatedAt = fallback.Ts
		}
	}

	// Unmarshal payload into any (produces map[string]any for JSON objects).
	var payload any
	if len(w.Payload) > 0 {
		if err := json.Unmarshal(w.Payload, &payload); err != nil {
			return domain.Event{}, fmt.Errorf("settla-messaging: unmarshal event payload: %w", err)
		}
	}

	return domain.Event{
		ID:        w.ID,
		TenantID:  w.TenantID,
		Type:      eventType,
		Timestamp: w.CreatedAt,
		Data:      payload,
	}, nil
}

// nakDelay returns the delay for a NAK based on the delivery attempt.
// This supplements NATS native BackOff for explicit retry control.
func nakDelay(deliveryCount uint64) time.Duration {
	if int(deliveryCount) > len(BackoffSchedule) {
		return BackoffSchedule[len(BackoffSchedule)-1]
	}
	if deliveryCount < 1 {
		return BackoffSchedule[0]
	}
	return BackoffSchedule[deliveryCount-1]
}

// Stop drains in-flight handlers then cancels the subscription.
func (s *Subscriber) Stop() {
	s.draining.Store(true)

	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		if s.logger != nil {
			s.logger.Info("settla-messaging: all in-flight handlers completed")
		}
	case <-time.After(30 * time.Second):
		if s.logger != nil {
			s.logger.Warn("settla-messaging: shutdown timeout — some handlers may still be running")
		}
	}

	if s.cancel != nil {
		s.cancel()
	}
}

// StreamSubscriber consumes events from any named JetStream stream.
// Unlike Subscriber (which is partition-aware for transfers), StreamSubscriber
// works with any of the 7 Settla streams.
type StreamSubscriber struct {
	client       *Client
	js           jetstream.JetStream
	streamName   string
	consumerName string
	poolSize     int
	workerPool   chan struct{}
	logger       *slog.Logger
	cancel       context.CancelFunc
	draining     atomic.Bool
	inflight     sync.WaitGroup
}

// NewStreamSubscriber creates a subscriber for a named stream with a durable consumer.
// The filterSubject can be empty to consume all subjects in the stream,
// or a specific filter like "settla.ledger.entry.created".
func NewStreamSubscriber(client *Client, streamName string, consumerName string, opts ...SubscriberOption) *StreamSubscriber {
	cfg := defaultSubscriberConfig()
	for _, o := range opts {
		o(&cfg)
	}

	var pool chan struct{}
	if cfg.poolSize > 1 {
		pool = make(chan struct{}, cfg.poolSize)
	}

	return &StreamSubscriber{
		client:       client,
		js:           client.JS,
		streamName:   streamName,
		consumerName: consumerName,
		poolSize:     cfg.poolSize,
		workerPool:   pool,
		logger: client.Logger.With(
			"component", "stream-subscriber",
			"stream", streamName,
			"consumer", consumerName,
		),
	}
}

// SubscribeStream creates a durable consumer on the named stream and begins
// consuming messages. An optional filterSubject narrows the consumed subjects
// (pass "" to consume everything on the stream).
//
// SubscribeStream blocks until the context is cancelled.
func (ss *StreamSubscriber) SubscribeStream(ctx context.Context, filterSubject string, handler EventHandler) error {
	stream, err := ss.js.Stream(ctx, ss.streamName)
	if err != nil {
		return fmt.Errorf("settla-messaging: getting stream %s: %w", ss.streamName, err)
	}

	maxAckPending := ss.poolSize * 4
	if maxAckPending < 100 {
		maxAckPending = 100
	}
	consumerCfg := jetstream.ConsumerConfig{
		Name:          ss.consumerName,
		Durable:       ss.consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       AckWait,
		MaxDeliver:    MaxRetries,
		BackOff:       BackoffSchedule,
		MaxAckPending: maxAckPending,
	}
	if filterSubject != "" {
		consumerCfg.FilterSubject = filterSubject
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, consumerCfg)
	if err != nil {
		return fmt.Errorf("settla-messaging: creating consumer %s on %s: %w", ss.consumerName, ss.streamName, err)
	}

	ss.logger.Info("settla-messaging: stream consumer started",
		"filter", filterSubject,
		"pool_size", ss.poolSize,
	)

	subCtx, cancel := context.WithCancel(ctx)
	ss.cancel = cancel

	cons, err := consumer.Consume(func(msg jetstream.Msg) {
		if ss.poolSize <= 1 {
			ss.handleMessage(subCtx, msg, handler)
			return
		}
		ss.dispatchPooled(subCtx, msg, handler)
	})
	if err != nil {
		cancel()
		return fmt.Errorf("settla-messaging: starting consume on %s/%s: %w", ss.streamName, ss.consumerName, err)
	}

	<-subCtx.Done()
	cons.Stop()

	ss.logger.Info("settla-messaging: stream consumer stopped")
	return nil
}

// handleMessage deserialises the event, calls the handler, and acks or nacks (serial path).
func (ss *StreamSubscriber) handleMessage(ctx context.Context, msg jetstream.Msg, handler EventHandler) {
	if ss.draining.Load() {
		_ = msg.Nak()
		return
	}
	ss.inflight.Add(1)
	defer ss.inflight.Done()

	processMessage(ctx, msg, handler, ss.streamName, ss.client, ss.logger, nil)
}

// dispatchPooled dispatches message processing to a pooled goroutine (no per-tenant ordering).
// The semaphore slot is acquired synchronously in the NATS callback to provide backpressure.
func (ss *StreamSubscriber) dispatchPooled(ctx context.Context, msg jetstream.Msg, handler EventHandler) {
	// Acquire semaphore slot synchronously — blocks the NATS callback for backpressure.
	select {
	case ss.workerPool <- struct{}{}:
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		ss.logger.Error("settla-messaging: pool slot acquisition timeout", "stream", ss.streamName)
		_ = msg.Nak()
		return
	}
	if ss.draining.Load() {
		<-ss.workerPool
		_ = msg.Nak()
		return
	}
	ss.inflight.Add(1)

	go func() {
		defer ss.inflight.Done()
		defer func() { <-ss.workerPool }()

		processMessage(ctx, msg, handler, ss.streamName, ss.client, ss.logger, nil)
	}()
}

// Stop drains in-flight handlers then cancels the stream subscription.
func (ss *StreamSubscriber) Stop() {
	ss.draining.Store(true)

	done := make(chan struct{})
	go func() {
		ss.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		if ss.logger != nil {
			ss.logger.Info("settla-messaging: all in-flight handlers completed")
		}
	case <-time.After(30 * time.Second):
		if ss.logger != nil {
			ss.logger.Warn("settla-messaging: shutdown timeout — some handlers may still be running")
		}
	}

	if ss.cancel != nil {
		ss.cancel()
	}
}
