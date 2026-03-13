package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// InboundWebhookWorker processes provider webhook callbacks received
// via the SETTLA_PROVIDER_WEBHOOKS stream. When a provider returns "pending"
// during on-ramp or off-ramp execution, the final result arrives asynchronously
// as a webhook. This worker receives the normalized webhook payload from NATS,
// updates the provider transaction record, and reports the result to the engine.
type InboundWebhookWorker struct {
	transferStore ProviderTransferStore
	engine        SettlementEngine
	subscriber    *messaging.StreamSubscriber
	logger        *slog.Logger
	partition     int
}

// NewInboundWebhookWorker creates an inbound webhook worker that subscribes
// to the SETTLA_PROVIDER_WEBHOOKS stream.
func NewInboundWebhookWorker(
	partition int,
	transferStore ProviderTransferStore,
	engine SettlementEngine,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *InboundWebhookWorker {
	consumerName := messaging.StreamConsumerName("settla-inbound-webhook-worker", partition)

	return &InboundWebhookWorker{
		transferStore: transferStore,
		engine:        engine,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamProviderWebhooks,
			consumerName,
			opts...,
		),
		logger:    logger.With("module", "inbound-webhook-worker", "partition", partition),
		partition: partition,
	}
}

// Start begins consuming inbound provider webhook messages. Blocks until ctx is cancelled.
func (w *InboundWebhookWorker) Start(ctx context.Context) error {
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixProviderInbound, w.partition)
	w.logger.Info("settla-inbound-webhook-worker: starting", "filter", filter)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the subscription.
func (w *InboundWebhookWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes inbound provider webhook events to the appropriate handler.
func (w *InboundWebhookWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.EventProviderOnRampWebhook:
		return w.handleOnRampWebhook(ctx, event)
	case domain.EventProviderOffRampWebhook:
		return w.handleOffRampWebhook(ctx, event)
	default:
		w.logger.Debug("settla-inbound-webhook-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// handleOnRampWebhook processes an inbound webhook for an on-ramp transaction.
func (w *InboundWebhookWorker) handleOnRampWebhook(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
	if err != nil {
		w.logger.Error("settla-inbound-webhook-worker: failed to unmarshal on-ramp webhook payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload, retrying won't help
	}

	w.logger.Info("settla-inbound-webhook-worker: processing on-ramp webhook",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"provider_id", payload.ProviderID,
		"status", payload.Status,
	)

	// Look up the existing provider transaction
	existing, err := w.transferStore.GetProviderTransaction(ctx, payload.TenantID, payload.TransferID, "onramp")
	if err != nil {
		return fmt.Errorf("settla-inbound-webhook-worker: looking up provider tx for %s: %w", payload.TransferID, err)
	}

	if existing == nil {
		w.logger.Warn("settla-inbound-webhook-worker: on-ramp webhook for unknown transfer, skipping",
			"transfer_id", payload.TransferID,
		)
		return nil // ACK — webhook for unknown transfer
	}

	// Idempotency: if already completed or confirmed, skip
	if existing.Status == "completed" || existing.Status == "confirmed" {
		w.logger.Info("settla-inbound-webhook-worker: on-ramp already completed, skipping duplicate webhook",
			"transfer_id", payload.TransferID,
			"existing_status", existing.Status,
		)
		return nil // ACK — duplicate webhook
	}

	// Update the provider transaction status
	existing.Status = payload.Status
	if payload.TxHash != "" {
		existing.TxHash = payload.TxHash
	}
	if payload.ProviderRef != "" {
		existing.ExternalID = payload.ProviderRef
	}

	if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "onramp", existing); err != nil {
		if isOptimisticLock(err) {
			w.logger.Info("settla-inbound-webhook-worker: on-ramp provider tx already advanced, skipping",
				"transfer_id", payload.TransferID,
			)
			return nil // ACK — another worker already advanced the state
		}
		w.logger.Warn("settla-inbound-webhook-worker: failed to update provider tx, continuing",
			"transfer_id", payload.TransferID,
			"error", err,
		)
	}

	// Report result to engine
	switch payload.Status {
	case "completed":
		w.logger.Info("settla-inbound-webhook-worker: on-ramp webhook completed",
			"transfer_id", payload.TransferID,
		)
		result := domain.IntentResult{
			Success:     true,
			ProviderRef: payload.ProviderRef,
			TxHash:      payload.TxHash,
		}
		return w.handleEngineError(w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOnRampResult", payload.TransferID)

	case "failed":
		w.logger.Warn("settla-inbound-webhook-worker: on-ramp webhook failed",
			"transfer_id", payload.TransferID,
			"error", payload.Error,
			"error_code", payload.ErrorCode,
		)
		result := domain.IntentResult{
			Success:   false,
			Error:     payload.Error,
			ErrorCode: payload.ErrorCode,
		}
		return w.handleEngineError(w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOnRampResult", payload.TransferID)

	default:
		w.logger.Warn("settla-inbound-webhook-worker: on-ramp webhook with unexpected status, skipping",
			"transfer_id", payload.TransferID,
			"status", payload.Status,
		)
		return nil
	}
}

// handleOffRampWebhook processes an inbound webhook for an off-ramp transaction.
func (w *InboundWebhookWorker) handleOffRampWebhook(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
	if err != nil {
		w.logger.Error("settla-inbound-webhook-worker: failed to unmarshal off-ramp webhook payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload, retrying won't help
	}

	w.logger.Info("settla-inbound-webhook-worker: processing off-ramp webhook",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"provider_id", payload.ProviderID,
		"status", payload.Status,
	)

	// Look up the existing provider transaction
	existing, err := w.transferStore.GetProviderTransaction(ctx, payload.TenantID, payload.TransferID, "offramp")
	if err != nil {
		return fmt.Errorf("settla-inbound-webhook-worker: looking up provider tx for %s: %w", payload.TransferID, err)
	}

	if existing == nil {
		w.logger.Warn("settla-inbound-webhook-worker: off-ramp webhook for unknown transfer, skipping",
			"transfer_id", payload.TransferID,
		)
		return nil // ACK — webhook for unknown transfer
	}

	// Idempotency: if already completed or confirmed, skip
	if existing.Status == "completed" || existing.Status == "confirmed" {
		w.logger.Info("settla-inbound-webhook-worker: off-ramp already completed, skipping duplicate webhook",
			"transfer_id", payload.TransferID,
			"existing_status", existing.Status,
		)
		return nil // ACK — duplicate webhook
	}

	// Update the provider transaction status
	existing.Status = payload.Status
	if payload.TxHash != "" {
		existing.TxHash = payload.TxHash
	}
	if payload.ProviderRef != "" {
		existing.ExternalID = payload.ProviderRef
	}

	if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "offramp", existing); err != nil {
		if isOptimisticLock(err) {
			w.logger.Info("settla-inbound-webhook-worker: off-ramp provider tx already advanced, skipping",
				"transfer_id", payload.TransferID,
			)
			return nil // ACK — another worker already advanced the state
		}
		w.logger.Warn("settla-inbound-webhook-worker: failed to update provider tx, continuing",
			"transfer_id", payload.TransferID,
			"error", err,
		)
	}

	// Report result to engine
	switch payload.Status {
	case "completed":
		w.logger.Info("settla-inbound-webhook-worker: off-ramp webhook completed",
			"transfer_id", payload.TransferID,
		)
		result := domain.IntentResult{
			Success:     true,
			ProviderRef: payload.ProviderRef,
			TxHash:      payload.TxHash,
		}
		return w.handleEngineError(w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOffRampResult", payload.TransferID)

	case "failed":
		w.logger.Warn("settla-inbound-webhook-worker: off-ramp webhook failed",
			"transfer_id", payload.TransferID,
			"error", payload.Error,
			"error_code", payload.ErrorCode,
		)
		result := domain.IntentResult{
			Success:   false,
			Error:     payload.Error,
			ErrorCode: payload.ErrorCode,
		}
		return w.handleEngineError(w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOffRampResult", payload.TransferID)

	default:
		w.logger.Warn("settla-inbound-webhook-worker: off-ramp webhook with unexpected status, skipping",
			"transfer_id", payload.TransferID,
			"status", payload.Status,
		)
		return nil
	}
}

// isOptimisticLock checks if the error is a domain optimistic lock conflict.
func isOptimisticLock(err error) bool {
	var domErr *domain.DomainError
	if errors.As(err, &domErr) {
		return domErr.Code() == "OPTIMISTIC_LOCK"
	}
	return false
}

// handleEngineError logs optimistic lock conflicts and returns the error for
// NATS NAK/retry. Other errors are returned as-is.
func (w *InboundWebhookWorker) handleEngineError(err error, step string, transferID fmt.Stringer) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, core.ErrOptimisticLock) {
		w.logger.Warn("settla-inbound-webhook-worker: optimistic lock conflict, NAKing for retry",
			"step", step,
			"transfer_id", transferID,
		)
	}
	return err
}
