package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// ResultPublisher publishes result events after intent execution.
type ResultPublisher interface {
	Publish(ctx context.Context, event domain.Event) error
}

// TreasuryWorker consumes treasury intent messages from NATS and executes
// reserve/release operations against the in-memory TreasuryManager.
// After execution, it publishes a result event back to NATS so the transfer
// worker can advance the saga.
type TreasuryWorker struct {
	treasury   domain.TreasuryManager
	publisher  ResultPublisher
	subscriber *messaging.StreamSubscriber
	logger     *slog.Logger
}

// NewTreasuryWorker creates a treasury worker that subscribes to the treasury stream.
func NewTreasuryWorker(
	treasury domain.TreasuryManager,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *TreasuryWorker {
	return &TreasuryWorker{
		treasury:  treasury,
		publisher: messaging.NewPublisher(client),
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamTreasury,
			"settla-treasury-worker",
			opts...,
		),
		logger: logger.With("module", "treasury-worker"),
	}
}

// Start begins consuming treasury intent messages. Blocks until ctx is cancelled.
func (w *TreasuryWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-treasury-worker: starting")
	return w.subscriber.SubscribeStream(ctx, "", w.handleEvent)
}

// Stop cancels the subscription.
func (w *TreasuryWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes treasury intent events to the appropriate handler.
func (w *TreasuryWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentTreasuryReserve:
		return w.handleReserve(ctx, event)
	case domain.IntentTreasuryRelease:
		return w.handleRelease(ctx, event)
	default:
		w.logger.Debug("settla-treasury-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// handleReserve executes a treasury reserve and publishes the result.
func (w *TreasuryWorker) handleReserve(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.TreasuryReservePayload](event)
	if err != nil {
		w.logger.Error("settla-treasury-worker: failed to unmarshal reserve payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload, retrying won't help
	}

	w.logger.Info("settla-treasury-worker: executing reserve",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"currency", payload.Currency,
		"amount", payload.Amount.String(),
	)

	err = w.treasury.Reserve(
		ctx,
		payload.TenantID,
		payload.Currency,
		payload.Location,
		payload.Amount,
		payload.TransferID, // use transfer ID as reference for idempotency
	)
	if err != nil {
		w.logger.Warn("settla-treasury-worker: reserve failed",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"error", err,
		)
		return w.publishResult(ctx, payload.TransferID, payload.TenantID, domain.EventTreasuryFailed, err.Error())
	}

	w.logger.Info("settla-treasury-worker: reserve succeeded",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
	)
	return w.publishResult(ctx, payload.TransferID, payload.TenantID, domain.EventTreasuryReserved, "")
}

// handleRelease executes a treasury release and publishes the result.
func (w *TreasuryWorker) handleRelease(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.TreasuryReleasePayload](event)
	if err != nil {
		w.logger.Error("settla-treasury-worker: failed to unmarshal release payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	// Build a deterministic, per-scenario idempotency reference so that
	// different release reasons (e.g. "settlement_failure" vs "transfer_complete")
	// for the same transfer get distinct dedup keys in the treasury manager.
	releaseRef := payload.TransferID
	if payload.Reason != "" {
		releaseRef = uuid.NewSHA1(payload.TransferID, []byte(payload.Reason))
	}

	w.logger.Info("settla-treasury-worker: executing release",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"currency", payload.Currency,
		"amount", payload.Amount.String(),
		"reason", payload.Reason,
	)

	err = w.treasury.Release(
		ctx,
		payload.TenantID,
		payload.Currency,
		payload.Location,
		payload.Amount,
		releaseRef,
	)
	if err != nil {
		w.logger.Warn("settla-treasury-worker: release failed",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"error", err,
		)
		// Release failures are logged but we still ACK — release is best-effort
		// on failure paths. The funds remain locked but can be manually reconciled.
	}

	w.logger.Info("settla-treasury-worker: release completed",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
	)
	return w.publishResult(ctx, payload.TransferID, payload.TenantID, domain.EventTreasuryReleased, "")
}

// publishResult publishes a treasury result event to NATS for the transfer worker.
func (w *TreasuryWorker) publishResult(ctx context.Context, transferID, tenantID uuid.UUID, eventType string, errMsg string) error {
	data := map[string]any{
		"transfer_id": transferID,
		"error":       errMsg,
	}

	event := domain.Event{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}

	if err := w.publisher.Publish(ctx, event); err != nil {
		w.logger.Error("settla-treasury-worker: failed to publish result event",
			"event_type", eventType,
			"transfer_id", transferID,
			"error", err,
		)
		return fmt.Errorf("settla-treasury-worker: publishing %s for transfer %s: %w", eventType, transferID, err)
	}

	return nil
}

// unmarshalEventData extracts and unmarshals the typed payload from an event.
// Event.Data can be raw JSON bytes, a map, or already the desired type.
func unmarshalEventData[T any](event domain.Event) (*T, error) {
	var result T

	switch data := event.Data.(type) {
	case *T:
		return data, nil
	case T:
		return &data, nil
	case []byte:
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("unmarshalling []byte payload: %w", err)
		}
		return &result, nil
	case json.RawMessage:
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("unmarshalling RawMessage payload: %w", err)
		}
		return &result, nil
	case map[string]any:
		// Re-marshal and unmarshal to handle map[string]any → struct conversion
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("re-marshalling map payload: %w", err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("unmarshalling re-marshalled payload: %w", err)
		}
		return &result, nil
	default:
		// Try marshalling whatever it is and then unmarshalling
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshalling unknown payload type %T: %w", data, err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("unmarshalling from type %T: %w", data, err)
		}
		return &result, nil
	}
}
