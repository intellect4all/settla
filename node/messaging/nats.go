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
)

const (
	// StreamName is the JetStream stream for all transfer events.
	StreamName = "SETTLA_TRANSFERS"

	// SubjectPrefix is the base subject for all transfer events.
	SubjectPrefix = "settla.transfer"

	// DefaultPartitions is the default number of partitions for tenant sharding.
	DefaultPartitions = 8

	// MaxRetries is the maximum delivery attempts before dead-lettering.
	MaxRetries = 5

	// AckWait is how long NATS waits for an ack before redelivering.
	AckWait = 30 * time.Second
)

// BackoffSchedule is the exponential backoff between redelivery attempts.
// 1s, 5s, 15s, 30s, 60s — then dead-lettered.
var BackoffSchedule = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
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

// ConsumerName builds the durable consumer name for a partition.
func ConsumerName(partition int) string {
	return fmt.Sprintf("settla-worker-partition-%d", partition)
}

// TenantPartition deterministically maps a tenant ID to a partition number
// using FNV-1a hashing. All events for the same tenant always land on the
// same partition, guaranteeing per-tenant ordering.
func TenantPartition(tenantID uuid.UUID, numPartitions int) int {
	h := fnv.New32a()
	h.Write(tenantID[:])
	return int(h.Sum32() % uint32(numPartitions))
}

// Client wraps a NATS connection and JetStream context for Settla messaging.
type Client struct {
	Conn          *nats.Conn
	JS            jetstream.JetStream
	Logger        *slog.Logger
	NumPartitions int
}

// NewClient connects to NATS and initialises JetStream.
func NewClient(url string, numPartitions int, logger *slog.Logger) (*Client, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // unlimited reconnects
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("settla-messaging: NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("settla-messaging: NATS reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("settla-messaging: connecting to NATS %s: %w", url, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("settla-messaging: creating JetStream context: %w", err)
	}

	return &Client{
		Conn:          nc,
		JS:            js,
		Logger:        logger.With("module", "messaging"),
		NumPartitions: numPartitions,
	}, nil
}

// EnsureStream creates or updates the SETTLA_TRANSFERS stream.
// The stream captures all subjects under settla.transfer.> with WorkQueue
// retention so each message is consumed exactly once per consumer group.
//
// Dev: Replicas=1 (single NATS node).
// Production: set Replicas=3 for fault tolerance across a 3-node NATS cluster.
func (c *Client) EnsureStream(ctx context.Context) error {
	cfg := jetstream.StreamConfig{
		Name:     StreamName,
		Subjects: []string{SubjectPrefix + ".>"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:  jetstream.FileStorage,
		MaxAge:   7 * 24 * time.Hour, // retain for 7 days
		Duplicates: 5 * time.Minute,  // dedup window
		// Replicas: 1 for dev, 3 for production (requires 3-node NATS cluster)
		Replicas: 1,
	}

	_, err := c.JS.CreateOrUpdateStream(ctx, cfg)
	if err != nil {
		return fmt.Errorf("settla-messaging: ensuring stream %s: %w", StreamName, err)
	}

	c.Logger.Info("settla-messaging: stream ensured", "stream", StreamName, "partitions", c.NumPartitions)
	return nil
}

// Close drains the NATS connection gracefully.
func (c *Client) Close() error {
	return c.Conn.Drain()
}
