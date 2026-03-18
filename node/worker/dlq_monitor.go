package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/intellect4all/settla/node/messaging"
)

const (
	dlqConsumerName  = "settla-dlq-monitor"
	dlqMaxBufferSize = 10000
)

// DLQEntry represents a dead-lettered message stored in the monitor's ring buffer.
type DLQEntry struct {
	ID           string    `json:"id"`
	SourceStream string    `json:"source_stream"`
	EventType    string    `json:"event_type"`
	Subject      string    `json:"subject"`
	TenantID     string    `json:"tenant_id,omitempty"`
	EventID      string    `json:"event_id,omitempty"`
	RawData      []byte    `json:"-"`
	DataPreview  string    `json:"data_preview"`
	ReceivedAt   time.Time `json:"received_at"`
}

// DLQStats holds aggregate statistics about the dead letter queue.
type DLQStats struct {
	TotalReceived  int64            `json:"total_received"`
	BufferedCount  int              `json:"buffered_count"`
	BySourceStream map[string]int64 `json:"by_source_stream"`
	ByEventType    map[string]int64 `json:"by_event_type"`
	OldestEntry    *time.Time       `json:"oldest_entry,omitempty"`
	NewestEntry    *time.Time       `json:"newest_entry,omitempty"`
}

// DLQMetrics holds Prometheus metrics for the DLQ monitor.
type DLQMetrics struct {
	MessagesTotal *prometheus.CounterVec
	DepthGauge    prometheus.Gauge
	ReplayTotal   *prometheus.CounterVec
}

// NewDLQMetrics registers and returns DLQ Prometheus metrics.
func NewDLQMetrics() *DLQMetrics {
	return &DLQMetrics{
		MessagesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_dlq_messages_total",
			Help: "Total messages received in the dead letter queue",
		}, []string{"source_stream", "event_type"}),
		DepthGauge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_dlq_buffer_depth",
			Help: "Current number of DLQ entries in the in-memory buffer",
		}),
		ReplayTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_dlq_replay_total",
			Help: "Total DLQ messages replayed back to source streams",
		}, []string{"source_stream", "result"}),
	}
}

// DLQMonitor consumes messages from the SETTLA_DLQ stream, logs them with
// full context, records Prometheus metrics, and stores recent entries in a
// bounded ring buffer for inspection via the ops API.
type DLQMonitor struct {
	client  *messaging.Client
	logger  *slog.Logger
	metrics *DLQMetrics

	mu      sync.RWMutex
	entries []DLQEntry
	head    int // ring buffer write position
	count   int // total entries in buffer (capped at dlqMaxBufferSize)

	// Aggregate counters for stats (not reset on buffer wrap).
	totalReceived  int64
	bySourceStream map[string]int64
	byEventType    map[string]int64

	cancel context.CancelFunc
}

// NewDLQMonitor creates a new DLQ monitor.
func NewDLQMonitor(client *messaging.Client, logger *slog.Logger, metrics *DLQMetrics) *DLQMonitor {
	return &DLQMonitor{
		client:         client,
		logger:         logger.With("component", "dlq-monitor"),
		metrics:        metrics,
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}
}

// Start begins consuming from the SETTLA_DLQ stream. Blocks until ctx is cancelled.
func (m *DLQMonitor) Start(ctx context.Context) error {
	stream, err := m.client.JS.Stream(ctx, messaging.StreamNameDLQ)
	if err != nil {
		return fmt.Errorf("settla-dlq-monitor: getting DLQ stream: %w", err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:       dlqConsumerName,
		Durable:    dlqConsumerName,
		AckPolicy:  jetstream.AckExplicitPolicy,
		AckWait:    30 * time.Second,
		MaxDeliver: 3, // DLQ messages themselves get 3 attempts
	})
	if err != nil {
		return fmt.Errorf("settla-dlq-monitor: creating consumer: %w", err)
	}

	m.logger.Info("settla-dlq-monitor: started", "consumer", dlqConsumerName)

	subCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	cons, err := consumer.Consume(func(msg jetstream.Msg) {
		m.handleDLQMessage(msg)
	})
	if err != nil {
		cancel()
		return fmt.Errorf("settla-dlq-monitor: starting consume: %w", err)
	}

	<-subCtx.Done()
	cons.Stop()

	m.logger.Info("settla-dlq-monitor: stopped")
	return nil
}

// Stop cancels the DLQ monitor.
func (m *DLQMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

// handleDLQMessage processes a single DLQ message: parse, log, record metrics, buffer.
func (m *DLQMonitor) handleDLQMessage(msg jetstream.Msg) {
	subject := msg.Subject()
	sourceStream, eventType := parseDLQSubject(subject)

	// Try to extract identifiers from the raw payload.
	tenantID, eventID := extractIDs(msg.Data())

	entry := DLQEntry{
		ID:           fmt.Sprintf("dlq-%d", time.Now().UnixNano()),
		SourceStream: sourceStream,
		EventType:    eventType,
		Subject:      subject,
		TenantID:     tenantID,
		EventID:      eventID,
		RawData:      msg.Data(),
		DataPreview:  truncate(string(msg.Data()), 500),
		ReceivedAt:   time.Now().UTC(),
	}

	// Log at ERROR level — DLQ messages are always noteworthy in a payment system.
	m.logger.Error("settla-dlq-monitor: DEAD-LETTERED MESSAGE",
		"source_stream", sourceStream,
		"event_type", eventType,
		"tenant_id", tenantID,
		"event_id", eventID,
		"subject", subject,
		"data_size", len(msg.Data()),
	)

	// Record metrics.
	if m.metrics != nil {
		m.metrics.MessagesTotal.WithLabelValues(sourceStream, eventType).Inc()
	}

	// Store in ring buffer.
	m.mu.Lock()
	m.entries[m.head] = entry
	m.head = (m.head + 1) % dlqMaxBufferSize
	if m.count < dlqMaxBufferSize {
		m.count++
	}
	m.totalReceived++
	m.bySourceStream[sourceStream]++
	m.byEventType[eventType]++
	if m.metrics != nil {
		m.metrics.DepthGauge.Set(float64(m.count))
	}
	m.mu.Unlock()

	// ACK to remove from NATS. The entry is now tracked in-memory.
	_ = msg.Ack()
}

// ListEntries returns the most recent DLQ entries, newest first.
// limit controls the maximum number returned (0 = all buffered).
func (m *DLQMonitor) ListEntries(limit int) []DLQEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.count == 0 {
		return []DLQEntry{}
	}

	n := m.count
	if limit > 0 && limit < n {
		n = limit
	}

	result := make([]DLQEntry, 0, n)
	// Read backwards from head to get newest first.
	for i := range n {
		idx := (m.head - 1 - i + dlqMaxBufferSize) % dlqMaxBufferSize
		result = append(result, m.entries[idx])
	}
	return result
}

// GetEntry returns a single DLQ entry by ID, or nil if not found.
func (m *DLQMonitor) GetEntry(id string) *DLQEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i := range m.count {
		idx := (m.head - 1 - i + dlqMaxBufferSize) % dlqMaxBufferSize
		if m.entries[idx].ID == id {
			e := m.entries[idx]
			return &e
		}
	}
	return nil
}

// Stats returns aggregate DLQ statistics.
func (m *DLQMonitor) Stats() DLQStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := DLQStats{
		TotalReceived:  m.totalReceived,
		BufferedCount:  m.count,
		BySourceStream: make(map[string]int64, len(m.bySourceStream)),
		ByEventType:    make(map[string]int64, len(m.byEventType)),
	}

	maps.Copy(stats.BySourceStream, m.bySourceStream)
	maps.Copy(stats.ByEventType, m.byEventType)

	if m.count > 0 {
		// Oldest entry is at the tail of the ring buffer.
		oldestIdx := m.head % dlqMaxBufferSize
		if m.count < dlqMaxBufferSize {
			oldestIdx = 0
		}
		oldest := m.entries[oldestIdx].ReceivedAt
		stats.OldestEntry = &oldest

		// Newest is one before head.
		newestIdx := (m.head - 1 + dlqMaxBufferSize) % dlqMaxBufferSize
		newest := m.entries[newestIdx].ReceivedAt
		stats.NewestEntry = &newest
	}

	return stats
}

// Replay re-publishes a DLQ entry back to its original stream subject.
// The message is published with a new dedup ID to avoid being filtered
// by the stream's duplicate window.
func (m *DLQMonitor) Replay(ctx context.Context, entryID string) error {
	entry := m.GetEntry(entryID)
	if entry == nil {
		return fmt.Errorf("settla-dlq-monitor: entry %s not found", entryID)
	}

	if len(entry.RawData) == 0 {
		return fmt.Errorf("settla-dlq-monitor: entry %s has no data to replay", entryID)
	}

	// Determine the original subject from the event data.
	originalSubject, err := m.resolveReplaySubject(entry)
	if err != nil {
		if m.metrics != nil {
			m.metrics.ReplayTotal.WithLabelValues(entry.SourceStream, "error").Inc()
		}
		return fmt.Errorf("settla-dlq-monitor: resolving replay subject: %w", err)
	}

	// Publish with a new dedup ID (replay-prefixed) to bypass duplicate detection.
	replayMsgID := fmt.Sprintf("replay-%s-%d", entryID, time.Now().UnixNano())
	if err := m.client.PublishToStream(ctx, originalSubject, replayMsgID, entry.RawData); err != nil {
		if m.metrics != nil {
			m.metrics.ReplayTotal.WithLabelValues(entry.SourceStream, "error").Inc()
		}
		return fmt.Errorf("settla-dlq-monitor: replaying to %s: %w", originalSubject, err)
	}

	if m.metrics != nil {
		m.metrics.ReplayTotal.WithLabelValues(entry.SourceStream, "success").Inc()
	}

	m.logger.Info("settla-dlq-monitor: replayed DLQ entry",
		"entry_id", entryID,
		"source_stream", entry.SourceStream,
		"event_type", entry.EventType,
		"replay_subject", originalSubject,
	)

	return nil
}

// resolveReplaySubject determines the NATS subject to replay a DLQ entry to.
func (m *DLQMonitor) resolveReplaySubject(entry *DLQEntry) (string, error) {
	// Try to parse tenant ID from the event to use partitioned routing.
	tenantID, _ := extractTenantUUID(entry.RawData)

	if entry.EventType == "unmarshal_error" {
		// For unmarshal errors, we can't determine the original event type.
		// Route back to the transfer stream's default partition.
		// Use uuid.Nil for deterministic partition (always partition 0 via FNV hash).
		if tenantID == uuid.Nil {
			tenantID = uuid.Nil
		}
		return messaging.SubjectForEventType("transfer.retry", tenantID, m.client.NumPartitions), nil
	}

	if tenantID == uuid.Nil {
		tenantID = uuid.Nil // deterministic partition 0 for non-tenant events
	}

	return messaging.SubjectForEventType(entry.EventType, tenantID, m.client.NumPartitions), nil
}

// parseDLQSubject extracts the source stream name and event type from a DLQ subject.
// Format: settla.dlq.{streamName}.{eventType}
func parseDLQSubject(subject string) (sourceStream, eventType string) {
	// Remove "settla.dlq." prefix
	const prefix = "settla.dlq."
	if !strings.HasPrefix(subject, prefix) {
		return "unknown", "unknown"
	}
	rest := subject[len(prefix):]

	// First segment is the stream name (e.g., SETTLA_TRANSFERS),
	// everything after the next dot is the event type.
	streamName, evtType, found := strings.Cut(rest, ".")
	if !found {
		return streamName, "unknown"
	}
	return streamName, evtType
}

// extractIDs attempts to extract tenant_id and event id from raw event JSON.
func extractIDs(data []byte) (tenantID, eventID string) {
	var parsed struct {
		ID       uuid.UUID `json:"id"`
		TenantID uuid.UUID `json:"tenant_id"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", ""
	}
	if parsed.TenantID != uuid.Nil {
		tenantID = parsed.TenantID.String()
	}
	if parsed.ID != uuid.Nil {
		eventID = parsed.ID.String()
	}
	return tenantID, eventID
}

// extractTenantUUID extracts tenant_id as a UUID from raw JSON.
func extractTenantUUID(data []byte) (uuid.UUID, error) {
	var parsed struct {
		TenantID uuid.UUID `json:"tenant_id"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return uuid.Nil, err
	}
	return parsed.TenantID, nil
}

// truncate returns the first n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
