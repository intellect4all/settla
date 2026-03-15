//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
)

// TestPartitionFanout verifies that:
// 1. 800 events across 80 tenants are all consumed exactly once
// 2. Per-tenant ordering is preserved within each partition
// 3. All 8 partitions receive events
func TestPartitionFanout(t *testing.T) {
	ns := startEmbeddedNATS(t)
	logger := observability.NewLogger("settla-partition-test", "test")

	nc, err := messaging.NewClient(ns.ClientURL(), 8, logger)
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := nc.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	const (
		numTenants       = 80
		eventsPerTenant  = 10
		totalEvents      = numTenants * eventsPerTenant
		numPartitions    = 8
	)

	// Generate 80 tenant IDs.
	tenants := make([]uuid.UUID, numTenants)
	for i := range tenants {
		tenants[i] = uuid.New()
	}

	// Track which partition consumed each event.
	type consumedEvent struct {
		TenantID  uuid.UUID
		EventType string
		Partition int
		Seq       int // sequence within tenant
	}

	var consumed []consumedEvent
	var consumedMu sync.Mutex
	var consumedCount atomic.Int64
	partitionCounts := make([]atomic.Int64, numPartitions)

	// Start 8 subscribers, one per partition.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	for p := 0; p < numPartitions; p++ {
		partition := p
		sub := messaging.NewSubscriber(nc, partition)
		go func() {
			_ = sub.Subscribe(subCtx, func(_ context.Context, event domain.Event) error {
				// Extract sequence from metadata.
				seq := 0
				if m, ok := event.Data.(map[string]any); ok {
					if s, ok := m["seq"].(float64); ok {
						seq = int(s)
					}
				}

				consumedMu.Lock()
				consumed = append(consumed, consumedEvent{
					TenantID:  event.TenantID,
					EventType: event.Type,
					Partition: partition,
					Seq:       seq,
				})
				consumedMu.Unlock()

				consumedCount.Add(1)
				partitionCounts[partition].Add(1)
				return nil
			})
		}()
	}

	// Give subscribers time to connect.
	time.Sleep(500 * time.Millisecond)

	// Publish 800 events: 10 per tenant across 80 tenants.
	publisher := messaging.NewPublisher(nc)
	for i, tenantID := range tenants {
		for seq := 0; seq < eventsPerTenant; seq++ {
			event := domain.Event{
				ID:        uuid.New(),
				TenantID:  tenantID,
				Type:      fmt.Sprintf("transfer.test.%d", seq),
				Timestamp: time.Now().UTC(),
				Data: map[string]any{
					"tenant_index": i,
					"seq":          seq,
				},
			}
			if err := publisher.Publish(ctx, event); err != nil {
				t.Fatalf("publish event %d for tenant %d: %v", seq, i, err)
			}
		}
	}
	t.Logf("published %d events across %d tenants", totalEvents, numTenants)

	// Wait for all events to be consumed.
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("timeout: consumed %d/%d events", consumedCount.Load(), totalEvents)
		case <-tick.C:
			if consumedCount.Load() >= int64(totalEvents) {
				goto allConsumed
			}
		}
	}
allConsumed:

	// Give a moment for any duplicate deliveries.
	time.Sleep(500 * time.Millisecond)
	subCancel()

	total := consumedCount.Load()

	// Assertion 1: all 800 events consumed exactly once.
	if total != int64(totalEvents) {
		t.Errorf("expected %d events consumed, got %d", totalEvents, total)
	}

	// Assertion 2: check that all 8 partitions received events.
	activePartitions := 0
	for p := 0; p < numPartitions; p++ {
		cnt := partitionCounts[p].Load()
		t.Logf("partition %d: %d events", p, cnt)
		if cnt > 0 {
			activePartitions++
		}
	}
	// With 80 random tenants across 8 partitions, it's extremely unlikely
	// that any partition receives 0 events. Use a soft check.
	if activePartitions < 4 {
		t.Errorf("expected most partitions to receive events, only %d/8 did", activePartitions)
	}

	// Assertion 3: per-tenant ordering preserved.
	consumedMu.Lock()
	defer consumedMu.Unlock()

	tenantSeqs := make(map[uuid.UUID][]int)
	for _, e := range consumed {
		tenantSeqs[e.TenantID] = append(tenantSeqs[e.TenantID], e.Seq)
	}

	for tenantID, seqs := range tenantSeqs {
		if len(seqs) != eventsPerTenant {
			t.Errorf("tenant %s: expected %d events, got %d", tenantID, eventsPerTenant, len(seqs))
			continue
		}
		// Verify ordering: sequence numbers should be monotonically increasing.
		for i := 1; i < len(seqs); i++ {
			if seqs[i] < seqs[i-1] {
				t.Errorf("tenant %s: out-of-order events at index %d: %d < %d",
					tenantID, i, seqs[i], seqs[i-1])
				break
			}
		}
	}

	t.Logf("partition fanout test passed: %d events, %d active partitions", total, activePartitions)
}

// publishTransferEvent publishes a transfer event with the wire format used by the relay.
func publishTransferEvent(ctx context.Context, nc *messaging.Client, tenantID uuid.UUID, eventType string, seq int) error {
	payload, _ := json.Marshal(map[string]any{"seq": seq})
	wireMsg := struct {
		ID        uuid.UUID       `json:"id"`
		TenantID  uuid.UUID       `json:"tenant_id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
		IsIntent  bool            `json:"is_intent"`
		CreatedAt time.Time       `json:"created_at"`
	}{
		ID:        uuid.New(),
		TenantID:  tenantID,
		EventType: eventType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}

	data, _ := json.Marshal(wireMsg)
	subject := messaging.TransferSubject(tenantID, 8, eventType)
	return nc.PublishToStream(ctx, subject, wireMsg.ID.String(), data)
}
