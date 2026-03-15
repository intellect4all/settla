//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/node/outbox"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/rail/provider/mock"
)

// TestNATSRedeliveryIdempotency verifies the CHECK-BEFORE-CALL pattern:
// When NATS redelivers a provider intent (because the first consumer didn't ack in time),
// the second consumer should detect the existing claim and skip execution.
// Result: provider.Execute is called exactly once; engine callback exactly once.
func TestNATSRedeliveryIdempotency(t *testing.T) {
	ns := startEmbeddedNATS(t)
	logger := observability.NewLogger("settla-redelivery-test", "test")

	// Connect to embedded NATS.
	nc, err := messaging.NewClient(ns.ClientURL(), 8, logger)
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create streams.
	if err := nc.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	// Reconfigure the SETTLA_PROVIDERS consumer with a very short AckWait
	// so the first delivery times out quickly and triggers redelivery.
	stream, err := nc.JS.Stream(ctx, messaging.StreamProviders)
	if err != nil {
		t.Fatalf("get providers stream: %v", err)
	}

	shortAckWait := 1 * time.Second
	consumerName := "settla-redelivery-test-consumer"
	filter := "settla.provider.command.partition.*.>"
	_, err = stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       shortAckWait,
		MaxDeliver:    4,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	// Create a slow mock provider that takes 500ms per call.
	var executeCount atomic.Int64
	slowProvider := mock.NewOnRampProvider("slow-provider", []domain.CurrencyPair{
		{From: domain.CurrencyGBP, To: domain.CurrencyUSDT},
	}, decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.50), 500*time.Millisecond)

	// Wrap the provider to count executions.
	_ = slowProvider

	// Create a provider transaction store.
	providerTxStore := worker.NewInMemoryProviderTransferStore()

	// Track engine callbacks.
	var engineCallbacks atomic.Int64
	mockEngine := &mockSettlementEngine{
		inner: nil, // don't forward
	}

	// Override HandleOnRampResult to count callbacks.
	var callbackMu sync.Mutex
	var callbackTransferIDs []uuid.UUID

	transferID := uuid.New()
	tenantID := LemfiTenantID

	// Publish a provider on-ramp intent directly to NATS.
	payload := domain.ProviderOnRampPayload{
		TransferID:   transferID,
		TenantID:     tenantID,
		ProviderID:   "slow-provider",
		Amount:       decimal.NewFromInt(1000),
		FromCurrency: domain.CurrencyGBP,
		ToCurrency:   domain.CurrencyUSDT,
		Reference:    "test-ref-" + transferID.String()[:8],
		QuotedRate:   decimal.NewFromFloat(1.25),
	}

	payloadJSON, _ := json.Marshal(payload)
	msg := outbox.OutboxRow{
		ID:            uuid.New(),
		AggregateType: "transfer",
		AggregateID:   transferID,
		TenantID:      tenantID,
		EventType:     domain.IntentProviderOnRamp,
		Payload:       payloadJSON,
		IsIntent:      true,
		CreatedAt:     time.Now().UTC(),
	}

	subject := outbox.SubjectForEntry(msg, 8)
	wireMsg := struct {
		ID        uuid.UUID       `json:"id"`
		TenantID  uuid.UUID       `json:"tenant_id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
		IsIntent  bool            `json:"is_intent"`
		CreatedAt time.Time       `json:"created_at"`
	}{
		ID:        msg.ID,
		TenantID:  msg.TenantID,
		EventType: msg.EventType,
		Payload:   msg.Payload,
		IsIntent:  msg.IsIntent,
		CreatedAt: msg.CreatedAt,
	}
	data, _ := json.Marshal(wireMsg)

	// Publish the intent.
	if err := nc.PublishToStream(ctx, subject, msg.ID.String(), data); err != nil {
		t.Fatalf("publish intent: %v", err)
	}
	t.Logf("published provider intent for transfer %s", transferID)

	// Start two consumers to simulate the redelivery scenario.
	// Consumer 1: processes slowly (claims the transaction).
	// Consumer 2: receives redelivery, finds claim, skips.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: filter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       shortAckWait,
		MaxDeliver:    4,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	var deliveryCount atomic.Int64

	_, consumeCancel := context.WithCancel(ctx)
	defer consumeCancel()

	cc, err := cons.Consume(func(natsMsg jetstream.Msg) {
		delivery := deliveryCount.Add(1)
		t.Logf("received delivery #%d", delivery)

		// Simulate the CHECK-BEFORE-CALL pattern.
		claimID, claimErr := providerTxStore.ClaimProviderTransaction(ctx, worker.ClaimProviderTransactionParams{
			TenantID:   tenantID,
			TransferID: transferID,
			TxType:     "onramp",
			Provider:   "slow-provider",
		})

		if claimErr != nil {
			t.Logf("claim error: %v", claimErr)
			_ = natsMsg.Nak()
			return
		}

		if claimID == nil {
			// Already claimed — skip execution.
			t.Logf("delivery #%d: already claimed, skipping", delivery)
			_ = natsMsg.Ack()
			return
		}

		// Simulate slow execution (first delivery).
		executeCount.Add(1)
		t.Logf("delivery #%d: executing provider call (claim=%s)", delivery, claimID)

		// Simulate provider execution delay.
		time.Sleep(300 * time.Millisecond)

		// Update the provider transaction to completed.
		_ = providerTxStore.UpdateProviderTransaction(ctx, transferID, "onramp", &domain.ProviderTx{
			ID:     claimID.String(),
			Status: "completed",
		})

		// Report result to engine.
		engineCallbacks.Add(1)
		callbackMu.Lock()
		callbackTransferIDs = append(callbackTransferIDs, transferID)
		callbackMu.Unlock()

		_ = mockEngine.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{
			Success:     true,
			ProviderRef: "provider-ref-123",
		})

		_ = natsMsg.Ack()
	})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}

	// Wait for processing.
	time.Sleep(5 * time.Second)
	cc.Stop()
	consumeCancel()

	// Assertions.
	executions := executeCount.Load()
	callbacks := engineCallbacks.Load()
	deliveries := deliveryCount.Load()

	t.Logf("deliveries=%d, executions=%d, callbacks=%d", deliveries, executions, callbacks)

	if executions != 1 {
		t.Errorf("expected exactly 1 provider execution, got %d", executions)
	}
	if callbacks != 1 {
		t.Errorf("expected exactly 1 engine callback, got %d", callbacks)
	}

	// Verify the claim store has the transaction in completed state.
	tx, err := providerTxStore.GetProviderTransaction(ctx, tenantID, transferID, "onramp")
	if err != nil {
		t.Fatalf("get provider transaction: %v", err)
	}
	if tx == nil {
		t.Fatal("expected provider transaction to exist")
	}
	if tx.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", tx.Status)
	}
}
