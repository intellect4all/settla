package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/resilience"
)

// startEmbeddedNATSForWorkerTest starts an in-process NATS server for worker tests.
func startEmbeddedNATSForWorkerTest(t *testing.T) *server.Server {
	t.Helper()

	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}

	ns, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("failed to create embedded NATS: %v", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded NATS not ready in time")
	}

	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})

	return ns
}

// TestWebhookDLQ verifies that after MaxDeliver exhausted, the webhook message
// lands in the DLQ stream.
func TestWebhookDLQ(t *testing.T) {
	ns := startEmbeddedNATSForWorkerTest(t)
	logger := observability.NewLogger("settla-webhook-dlq-test", "test")

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

	// Create a webhook endpoint that always returns 500.
	var endpointCalls atomic.Int64
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpointCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	transferID := uuid.New()

	// Set up tenant with failing webhook URL.
	tenantStore := newMockTenantWebhookStore()
	tenantStore.tenants[tenantID] = &domain.Tenant{
		ID:            tenantID,
		Name:          "Test Tenant",
		Slug:          "test-tenant",
		WebhookURL:    failServer.URL,
		WebhookSecret: "test-secret-key",
	}

	// Create a webhook worker with a very short ack wait and max deliver = 3.
	// We'll create the consumer manually with these settings.
	maxDeliver := 3
	shortAckWait := 1 * time.Second

	// Create a consumer on the webhooks stream with short ack wait.
	stream, err := nc.JS.Stream(ctx, messaging.StreamWebhooks)
	if err != nil {
		t.Fatalf("get webhooks stream: %v", err)
	}

	partition := messaging.TenantPartition(tenantID, 8)
	consumerName := messaging.StreamConsumerName("settla-webhook-dlq-test", partition)
	filterSubject := messaging.StreamPartitionFilter(messaging.SubjectPrefixWebhook, partition)

	_, err = stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       shortAckWait,
		MaxDeliver:    maxDeliver,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	// Publish a webhook intent.
	webhookPayload := domain.WebhookDeliverPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		EventType:  "transfer.completed",
	}
	payloadJSON, _ := json.Marshal(webhookPayload)

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
		EventType: domain.IntentWebhookDeliver,
		Payload:   payloadJSON,
		IsIntent:  true,
		CreatedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(wireMsg)

	subject := messaging.WebhookSubject(tenantID, 8, domain.IntentWebhookDeliver)
	if err := nc.PublishToStream(ctx, subject, wireMsg.ID.String(), data); err != nil {
		t.Fatalf("publish webhook intent: %v", err)
	}
	t.Logf("published webhook intent for transfer %s", transferID)

	// Start consuming with a handler that simulates the webhook worker.
	// The webhook worker will attempt delivery and return an error (NAK) on 500 responses.
	cb := resilience.NewCircuitBreaker("webhook-dlq-test",
		resilience.WithFailureThreshold(100), // High threshold so CB doesn't open
		resilience.WithResetTimeout(30*time.Second),
	)

	ww := NewWebhookWorker(partition, tenantStore, nc, logger, cb)

	// Start the webhook worker's subscriber with the custom consumer.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	// Instead of using the built-in Start(), consume with our custom consumer directly.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       shortAckWait,
		MaxDeliver:    maxDeliver,
		BackOff:       []time.Duration{200 * time.Millisecond, 500 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("update consumer: %v", err)
	}

	var deliveryAttempts atomic.Int64
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		deliveryAttempts.Add(1)
		delivery := deliveryAttempts.Load()
		t.Logf("webhook delivery attempt #%d", delivery)

		metadata, _ := msg.Metadata()
		if metadata != nil && metadata.NumDelivered >= uint64(maxDeliver) {
			// Max retries exhausted — publish to DLQ.
			t.Logf("max deliveries exhausted, routing to DLQ")
			_ = nc.PublishDLQ(ctx, messaging.StreamWebhooks, domain.IntentWebhookDeliver, msg.Data())
			_ = msg.Ack()
			return
		}

		// Process as webhook worker handler.
		event, err := unmarshalEventForTest(msg.Data())
		if err != nil {
			t.Logf("unmarshal error: %v", err)
			_ = msg.Nak()
			return
		}

		if err := ww.handleEvent(subCtx, event); err != nil {
			t.Logf("webhook delivery failed: %v", err)
			_ = msg.NakWithDelay(200 * time.Millisecond)
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}

	// Wait for all delivery attempts + DLQ routing.
	time.Sleep(8 * time.Second)
	cc.Stop()
	subCancel()

	// Assertions.
	calls := endpointCalls.Load()
	attempts := deliveryAttempts.Load()

	t.Logf("endpoint calls=%d, delivery attempts=%d", calls, attempts)

	// The endpoint should have been called at least once (up to maxDeliver times).
	if calls == 0 {
		t.Error("expected at least 1 endpoint call")
	}

	// Verify DLQ has 1 message.
	dlqStream, err := nc.JS.Stream(ctx, messaging.StreamNameDLQ)
	if err != nil {
		t.Fatalf("get DLQ stream: %v", err)
	}

	info, err := dlqStream.Info(ctx)
	if err != nil {
		t.Fatalf("DLQ stream info: %v", err)
	}

	t.Logf("DLQ messages: %d", info.State.Msgs)
	if info.State.Msgs == 0 {
		t.Error("expected at least 1 message in DLQ after max retries exhausted")
	}

	t.Log("webhook DLQ test passed")
}

// unmarshalEventForTest unmarshals a NATS message into a domain.Event.
func unmarshalEventForTest(data []byte) (domain.Event, error) {
	var w struct {
		ID        uuid.UUID       `json:"id"`
		TenantID  uuid.UUID       `json:"tenant_id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt time.Time       `json:"created_at"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return domain.Event{}, err
	}

	var payload any
	if len(w.Payload) > 0 {
		_ = json.Unmarshal(w.Payload, &payload)
	}

	return domain.Event{
		ID:        w.ID,
		TenantID:  w.TenantID,
		Type:      w.EventType,
		Timestamp: w.CreatedAt,
		Data:      payload,
	}, nil
}
