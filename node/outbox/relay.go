package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/intellect4all/settla/node/messaging"
)

// Default relay settings.
const (
	DefaultPollInterval = 20 * time.Millisecond
	DefaultBatchSize    = int32(500)
	DefaultPartitions   = 8
)

// OutboxRow is a simple type representing an outbox entry fetched from the database.
// It maps cleanly from the SQLC-generated transferdb.Outbox model without coupling
// the relay to the SQLC package directly.
type OutboxRow struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	TenantID      uuid.UUID
	EventType     string
	Payload       []byte
	IsIntent      bool
	Published     bool
	RetryCount    int32
	MaxRetries    int32
	CreatedAt     time.Time
}

// OutboxStore is the interface the relay needs from the database.
type OutboxStore interface {
	GetUnpublishedEntries(ctx context.Context, limit int32) ([]OutboxRow, error)
	MarkPublished(ctx context.Context, id uuid.UUID, createdAt time.Time) error
	MarkFailed(ctx context.Context, id uuid.UUID, createdAt time.Time) error
}

// Publisher is the interface for NATS publishing.
type Publisher interface {
	Publish(ctx context.Context, subject string, msgID string, data []byte) error
}

// RelayMetrics holds Prometheus metrics for the outbox relay.
type RelayMetrics struct {
	PublishedTotal   *prometheus.CounterVec
	RelayLatency     prometheus.Histogram
	UnpublishedGauge prometheus.Gauge
	PollBatchSize    prometheus.Histogram
	FailedTotal      prometheus.Counter
	MarkFailedTotal  prometheus.Counter // NATS publish succeeded but DB mark failed
}

// NewRelayMetrics registers and returns outbox relay Prometheus metrics.
func NewRelayMetrics() *RelayMetrics {
	return &RelayMetrics{
		PublishedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_outbox_published_total",
			Help: "Total outbox entries published to NATS, by event_type prefix.",
		}, []string{"event_prefix"}),

		RelayLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_outbox_relay_latency_seconds",
			Help:    "Time from outbox entry creation to NATS publish confirmation.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 5},
		}),

		UnpublishedGauge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_outbox_unpublished_gauge",
			Help: "Number of unpublished outbox entries in the last poll batch.",
		}),

		PollBatchSize: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_outbox_poll_batch_size",
			Help:    "Number of entries fetched per poll cycle.",
			Buckets: []float64{0, 1, 5, 10, 25, 50, 100},
		}),

		FailedTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_outbox_failed_total",
			Help: "Total outbox publish failures (retry_count incremented).",
		}),

		MarkFailedTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_outbox_mark_failed_total",
			Help: "Total times MarkPublished failed after successful NATS publish. Entry will be re-polled and NATS dedup prevents duplicate delivery.",
		}),
	}
}

// Relay polls the outbox table and publishes entries to NATS JetStream.
// It bridges the transactional outbox (Postgres) to the event infrastructure (NATS).
//
// The relay runs in a loop:
//  1. Query unpublished entries (batch of 100)
//  2. For each entry, determine the NATS subject based on event_type
//  3. Publish to NATS with message ID = outbox entry UUID (dedup)
//  4. Mark entry as published
//  5. On publish failure, increment retry_count
//  6. Sleep 50ms, repeat
type Relay struct {
	store         OutboxStore
	publisher     Publisher
	logger        *slog.Logger
	metrics       *RelayMetrics
	pollInterval  time.Duration
	batchSize     int32
	numPartitions int
}

// RelayOption configures the Relay.
type RelayOption func(*Relay)

// WithPollInterval sets the polling interval (default 50ms).
func WithPollInterval(d time.Duration) RelayOption {
	return func(r *Relay) {
		r.pollInterval = d
	}
}

// WithBatchSize sets the number of entries fetched per poll (default 100).
func WithBatchSize(n int32) RelayOption {
	return func(r *Relay) {
		r.batchSize = n
	}
}

// WithNumPartitions sets the number of NATS partitions for tenant routing (default 8).
func WithNumPartitions(n int) RelayOption {
	return func(r *Relay) {
		r.numPartitions = n
	}
}

// WithMetrics attaches Prometheus metrics to the relay.
func WithMetrics(m *RelayMetrics) RelayOption {
	return func(r *Relay) {
		r.metrics = m
	}
}

// NewRelay creates an outbox relay.
func NewRelay(store OutboxStore, publisher Publisher, logger *slog.Logger, opts ...RelayOption) *Relay {
	r := &Relay{
		store:         store,
		publisher:     publisher,
		logger:        logger.With("component", "outbox-relay"),
		pollInterval:  DefaultPollInterval,
		batchSize:     DefaultBatchSize,
		numPartitions: DefaultPartitions,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// outboxMessage is the JSON payload published to NATS for each outbox entry.
type outboxMessage struct {
	ID            uuid.UUID       `json:"id"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	IsIntent      bool            `json:"is_intent"`
	CreatedAt     time.Time       `json:"created_at"`
}

// maxBackoff is the ceiling for exponential backoff on DB failures.
const maxBackoff = 5 * time.Second

// Run starts the relay loop. It blocks until ctx is cancelled.
// On poll failure, the interval backs off exponentially up to maxBackoff.
// On success, the interval resets to pollInterval.
func (r *Relay) Run(ctx context.Context) error {
	r.logger.Info("settla-outbox: relay started",
		"poll_interval", r.pollInterval,
		"batch_size", r.batchSize,
		"partitions", r.numPartitions,
	)

	consecutiveFailures := 0
	timer := time.NewTimer(r.pollInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("settla-outbox: relay stopped")
			return ctx.Err()
		case <-timer.C:
			if err := r.poll(ctx); err != nil {
				consecutiveFailures++
				shift := min(consecutiveFailures, 6)
				backoff := r.pollInterval * time.Duration(1<<shift)
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				r.logger.Error("settla-outbox: poll cycle failed, backing off",
					"error", err,
					"consecutive_failures", consecutiveFailures,
					"next_poll_in", backoff,
				)
				timer.Reset(backoff)
			} else {
				consecutiveFailures = 0
				timer.Reset(r.pollInterval)
			}
		}
	}
}

// poll fetches a batch of unpublished entries and publishes each to NATS.
func (r *Relay) poll(ctx context.Context) error {
	entries, err := r.store.GetUnpublishedEntries(ctx, r.batchSize)
	if err != nil {
		return fmt.Errorf("settla-outbox: fetching unpublished entries: %w", err)
	}

	if r.metrics != nil {
		r.metrics.UnpublishedGauge.Set(float64(len(entries)))
		r.metrics.PollBatchSize.Observe(float64(len(entries)))
	}

	if len(entries) == 0 {
		return nil
	}

	for _, entry := range entries {
		if err := r.publishEntry(ctx, entry); err != nil {
			// Log per-entry failures but continue processing the batch.
			r.logger.Warn("settla-outbox: failed to publish entry",
				"entry_id", entry.ID,
				"event_type", entry.EventType,
				"tenant_id", entry.TenantID,
				"retry_count", entry.RetryCount,
				"error", err,
			)
		}
	}

	return nil
}

// publishEntry publishes a single outbox entry to NATS and marks it accordingly.
func (r *Relay) publishEntry(ctx context.Context, entry OutboxRow) error {
	subject := SubjectForEntry(entry, r.numPartitions)

	msg := outboxMessage{
		ID:            entry.ID,
		AggregateType: entry.AggregateType,
		AggregateID:   entry.AggregateID,
		TenantID:      entry.TenantID,
		EventType:     entry.EventType,
		Payload:       entry.Payload,
		IsIntent:      entry.IsIntent,
		CreatedAt:     entry.CreatedAt,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("settla-outbox: marshalling entry %s: %w", entry.ID, err)
	}

	// Publish with outbox entry UUID as NATS dedup message ID.
	if err := r.publisher.Publish(ctx, subject, entry.ID.String(), data); err != nil {
		// Mark failed — increments retry_count.
		if markErr := r.store.MarkFailed(ctx, entry.ID, entry.CreatedAt); markErr != nil {
			r.logger.Error("settla-outbox: failed to mark entry as failed",
				"entry_id", entry.ID,
				"mark_error", markErr,
				"publish_error", err,
			)
		}
		if r.metrics != nil {
			r.metrics.FailedTotal.Inc()
		}
		return fmt.Errorf("settla-outbox: publishing entry %s to %s: %w", entry.ID, subject, err)
	}

	// Mark published. If this fails, the entry stays unpublished in DB and will be
	// re-polled on the next cycle. NATS dedup (5-minute window) catches the duplicate
	// publish, so correctness is maintained. We do NOT return an error here because the
	// message is already in NATS — returning an error would cause the caller to log a
	// misleading "failed to publish" warning.
	if err := r.store.MarkPublished(ctx, entry.ID, entry.CreatedAt); err != nil {
		r.logger.Error("settla-outbox: failed to mark entry as published (message already delivered to NATS, will retry mark on next poll)",
			"entry_id", entry.ID,
			"event_type", entry.EventType,
			"tenant_id", entry.TenantID,
			"error", err,
		)
		if r.metrics != nil {
			r.metrics.MarkFailedTotal.Inc()
		}
		// Don't return error — message IS published. The re-poll + NATS dedup handles it.
		return nil
	}

	// Record metrics.
	if r.metrics != nil {
		latency := time.Since(entry.CreatedAt).Seconds()
		r.metrics.RelayLatency.Observe(latency)
		r.metrics.PublishedTotal.WithLabelValues(eventPrefix(entry.EventType)).Inc()
	}

	r.logger.Debug("settla-outbox: published entry",
		"entry_id", entry.ID,
		"event_type", entry.EventType,
		"tenant_id", entry.TenantID,
		"subject", subject,
		"is_intent", entry.IsIntent,
	)

	return nil
}

// SubjectForEntry determines the NATS subject for an outbox entry.
// It delegates to the messaging package's SubjectForEventType which handles
// all routing logic including transfer partitioning, provider command vs inbound, etc.
func SubjectForEntry(entry OutboxRow, numPartitions int) string {
	return messaging.SubjectForEventType(entry.EventType, entry.TenantID, numPartitions)
}

// eventPrefix extracts the first segment before the dot from an event type.
// Used for metrics labeling.
func eventPrefix(eventType string) string {
	for i := 0; i < len(eventType); i++ {
		if eventType[i] == '.' {
			return eventType[:i]
		}
	}
	return eventType
}
