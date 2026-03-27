package messaging

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// StreamName is the JetStream stream for transfer events (kept for backward compatibility).
	// Prefer using StreamTransfers from streams.go for new code.
	StreamName = StreamTransfers

	// SubjectPrefix is the base subject for transfer events (kept for backward compatibility).
	SubjectPrefix = SubjectPrefixTransfer

	// DefaultPartitions is the default number of partitions for tenant sharding.
	// 256 partitions supports 100-200K tenants (~400-800 tenants/partition),
	// minimizing head-of-line blocking from slow tenants.
	// Override via SETTLA_NODE_PARTITIONS env var (range 1-256).
	DefaultPartitions = 256

	// MaxRetries is the maximum delivery attempts before dead-lettering.
	// Total deliveries = MaxRetries (1 initial + 5 retries = 6).
	MaxRetries = 6

	// AckWait is how long NATS waits for an ack before redelivering.
	AckWait = 30 * time.Second
)

// BackoffSchedule is the native NATS backoff between redelivery attempts.
// 1s, 5s, 30s, 2m, 10m — then dead-lettered after MaxDeliver exhausted.
var BackoffSchedule = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// PartitionSubject builds the NATS subject for a given partition and event type.
// Format: settla.transfer.partition.{N}.{event_type}
func PartitionSubject(partition int, eventType string) string {
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefix, partition, eventType)
}

// PartitionFilter builds the filter subject for a consumer subscribing to
// all events on a given partition.
// Format: settla.transfer.partition.{N}.>
func PartitionFilter(partition int) string {
	return fmt.Sprintf("%s.partition.%d.>", SubjectPrefix, partition)
}

// ConsumerName builds the durable consumer name for a transfer partition.
// Format: settla-transfer-worker-{N}
func ConsumerName(partition int) string {
	return fmt.Sprintf("settla-transfer-worker-%d", partition)
}

// TenantPartition deterministically maps a tenant ID to a partition number
// using FNV-1a hashing. All events for the same tenant always land on the
// same partition, guaranteeing per-tenant ordering.
func TenantPartition(tenantID uuid.UUID, numPartitions int) int {
	h := fnv.New32a()
	h.Write(tenantID[:])
	return int(h.Sum32() % uint32(numPartitions))
}

// StreamPartitionFilter builds the filter subject for a consumer subscribing to
// all events on a given partition for any stream prefix.
// Format: {subjectPrefix}.partition.{N}.>
func StreamPartitionFilter(subjectPrefix string, partition int) string {
	return fmt.Sprintf("%s.partition.%d.>", subjectPrefix, partition)
}

// StreamConsumerName builds the durable consumer name for a partitioned stream worker.
// Format: {baseName}-{N}
func StreamConsumerName(baseName string, partition int) string {
	return fmt.Sprintf("%s-%d", baseName, partition)
}

// Client wraps a NATS connection and JetStream context for Settla messaging.
type Client struct {
	Conn          *nats.Conn
	JS            jetstream.JetStream
	Logger        *slog.Logger
	NumPartitions int
	// Replicas controls the number of stream replicas. 1 for dev, 3 for production.
	Replicas int
	// DLQCounter is an optional counter incremented each time a message is
	// routed to the dead letter queue. Labels: source_stream, event_type.
	// When nil, DLQ counting is silently skipped.
	DLQCounter *prometheus.CounterVec

	// Authentication fields (set via WithNATSToken or WithNATSUserInfo).
	natsToken    string
	natsUser     string
	natsPassword string
}

// ClientOption configures the NATS client.
type ClientOption func(*Client)

// WithReplicas sets the number of stream replicas (1 for dev, 3 for production).
func WithReplicas(n int) ClientOption {
	return func(c *Client) {
		c.Replicas = n
	}
}

// WithNATSToken configures token-based authentication for the NATS connection.
func WithNATSToken(token string) ClientOption {
	return func(c *Client) { c.natsToken = token }
}

// WithNATSUserInfo configures username/password authentication for the NATS connection.
func WithNATSUserInfo(user, password string) ClientOption {
	return func(c *Client) { c.natsUser = user; c.natsPassword = password }
}

// NewClient connects to NATS, initialises JetStream, and creates all 7 streams.
func NewClient(url string, numPartitions int, logger *slog.Logger, opts ...ClientOption) (*Client, error) {
	// Apply options first to collect auth credentials before connecting.
	c := &Client{
		Logger:        logger.With("module", "messaging"),
		NumPartitions: numPartitions,
		Replicas:      1, // default: dev mode
	}
	for _, opt := range opts {
		opt(c)
	}

	// Build NATS connection options including auth credentials.
	natsOpts := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // unlimited reconnects
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("settla-messaging: NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("settla-messaging: NATS reconnected", "url", nc.ConnectedUrl())
		}),
	}
	if c.natsToken != "" {
		natsOpts = append(natsOpts, nats.Token(c.natsToken))
	} else if c.natsUser != "" {
		natsOpts = append(natsOpts, nats.UserInfo(c.natsUser, c.natsPassword))
	}

	nc, err := nats.Connect(url, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("settla-messaging: connecting to NATS %s: %w", url, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("settla-messaging: creating JetStream context: %w", err)
	}

	c.Conn = nc
	c.JS = js
	return c, nil
}

// EnsureStreams creates or updates all 7 Settla JetStream streams idempotently.
// This should be called once on startup (NewClient does NOT call it automatically
// so callers can control the context and timing).
func (c *Client) EnsureStreams(ctx context.Context) error {
	if err := CreateStreams(ctx, c.JS, c.Replicas); err != nil {
		return err
	}
	c.Logger.Info("settla-messaging: all streams ensured",
		"stream_count", len(AllStreams()),
		"partitions", c.NumPartitions,
		"replicas", c.Replicas,
	)
	return nil
}

// EnsureStream creates or updates the SETTLA_TRANSFERS stream only.
// Deprecated: Use EnsureStreams to create all 7 streams. Kept for backward compatibility.
func (c *Client) EnsureStream(ctx context.Context) error {
	return c.EnsureStreams(ctx)
}

// PublishToStream publishes a message to a specific subject with dedup ID.
// The msgID is used as the Nats-Msg-Id header for exactly-once semantics
// within the stream's duplicate window.
func (c *Client) PublishToStream(ctx context.Context, subject string, msgID string, data []byte) error {
	_, err := c.JS.Publish(ctx, subject, data,
		jetstream.WithMsgID(msgID),
	)
	if err != nil {
		return fmt.Errorf("settla-messaging: publishing to %s (msgID=%s): %w", subject, msgID, err)
	}
	return nil
}

// PublishDLQ publishes a failed message to the dead letter queue using JetStream
// for durable persistence. Format: settla.dlq.{streamName}.{eventType}
func (c *Client) PublishDLQ(ctx context.Context, streamName string, eventType string, data []byte) error {
	subject := DLQSubject(streamName, eventType)
	msgID := fmt.Sprintf("dlq-%s-%s-%d", streamName, eventType, time.Now().UnixNano())
	_, err := c.JS.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	if err != nil {
		c.Logger.Error("settla-messaging: CRITICAL - failed to publish to DLQ",
			"stream", streamName, "event_type", eventType, "error", err)
	}
	return err
}

// Close drains the NATS connection gracefully.
func (c *Client) Close() error {
	return c.Conn.Drain()
}
