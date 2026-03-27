package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// WebhookLogStore persists inbound provider webhooks for deduplication,
// debugging, and audit. Implemented by store/transferdb.ProviderWebhookLogAdapter.
type WebhookLogStore interface {
	InsertRaw(ctx context.Context, slug, idempotencyKey string, rawBody []byte, headers map[string]string, sourceIP string) (id uuid.UUID, createdAt time.Time, isDuplicate bool, err error)
	CheckDuplicate(ctx context.Context, slug, idempotencyKey string) (bool, error)
	MarkProcessed(ctx context.Context, id uuid.UUID, createdAt time.Time, transferID, tenantID *uuid.UUID, normalized []byte, status, errMsg string) error
}

// NormalizerLookup returns the WebhookNormalizer for a provider slug, or nil.
type NormalizerLookup func(slug string) domain.WebhookNormalizer

// InboundWebhookWorker processes provider webhook callbacks received via the
// SETTLA_PROVIDER_WEBHOOKS stream.
//
// Two event paths:
//  1. EventProviderRawWebhook — raw bytes from TS HTTP receiver. Worker stores
//     the payload, normalizes in Go, then processes.
//  2. EventProviderOnRampWebhook / EventProviderOffRampWebhook — already normalized.
//     Used by ProviderListeners (WebSocket/polling providers) and legacy path.
type InboundWebhookWorker struct {
	transferStore   ProviderTransferStore
	engine          SettlementEngine
	normalizers     NormalizerLookup
	webhookLogStore WebhookLogStore
	subscriber      *messaging.StreamSubscriber
	logger          *slog.Logger
	partition       int
}

// NewInboundWebhookWorker creates an inbound webhook worker that subscribes
// to the SETTLA_PROVIDER_WEBHOOKS stream.
//
// normalizers and webhookLogStore may be nil for backward compatibility (tests,
// environments without the webhook log table). When nil, raw webhook events are
// rejected with a log warning.
func NewInboundWebhookWorker(
	partition int,
	transferStore ProviderTransferStore,
	engine SettlementEngine,
	client *messaging.Client,
	logger *slog.Logger,
	normalizers NormalizerLookup,
	webhookLogStore WebhookLogStore,
	opts ...messaging.SubscriberOption,
) *InboundWebhookWorker {
	consumerName := messaging.StreamConsumerName("settla-inbound-webhook-worker", partition)

	return &InboundWebhookWorker{
		transferStore:   transferStore,
		engine:          engine,
		normalizers:     normalizers,
		webhookLogStore: webhookLogStore,
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
	case domain.EventProviderRawWebhook:
		return w.handleRawWebhook(ctx, event)
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

func (w *InboundWebhookWorker) handleRawWebhook(ctx context.Context, event domain.Event) error {
	if w.normalizers == nil || w.webhookLogStore == nil {
		w.logger.Error("settla-inbound-webhook-worker: raw webhook received but normalizer/store not configured")
		return nil // ACK — can't process without infrastructure
	}

	raw, err := unmarshalEventData[domain.RawWebhookPayload](event)
	if err != nil {
		w.logger.Error("settla-inbound-webhook-worker: failed to unmarshal raw webhook",
			"event_id", event.ID, "error", err)
		return nil // ACK — malformed, retrying won't help
	}

	w.logger.Info("settla-inbound-webhook-worker: received raw webhook",
		"provider", raw.ProviderSlug,
		"idempotency_key", raw.IdempotencyKey,
	)

	// Step 1: Store raw payload BEFORE normalization.
	logID, logCreatedAt, isDuplicate, err := w.webhookLogStore.InsertRaw(
		ctx, raw.ProviderSlug, raw.IdempotencyKey, raw.RawBody, raw.HTTPHeaders, raw.SourceIP,
	)
	if err != nil {
		return fmt.Errorf("settla-inbound-webhook-worker: storing raw webhook: %w", err)
	}
	if isDuplicate {
		w.logger.Info("settla-inbound-webhook-worker: duplicate webhook, skipping",
			"provider", raw.ProviderSlug, "idempotency_key", raw.IdempotencyKey)
		return nil // ACK — already stored
	}

	// Step 2: Check if already processed (belt-and-suspenders with INSERT dedup).
	alreadyProcessed, err := w.webhookLogStore.CheckDuplicate(ctx, raw.ProviderSlug, raw.IdempotencyKey)
	if err != nil {
		w.logger.Warn("settla-inbound-webhook-worker: dedup check failed, continuing",
			"error", err)
	}
	if alreadyProcessed {
		_ = w.webhookLogStore.MarkProcessed(ctx, logID, logCreatedAt, nil, nil, nil, domain.WebhookLogDuplicate, "")
		return nil
	}

	// Step 3: Look up normalizer.
	normalizer := w.normalizers(raw.ProviderSlug)
	if normalizer == nil {
		errMsg := fmt.Sprintf("no WebhookNormalizer registered for provider %q", raw.ProviderSlug)
		w.logger.Error("settla-inbound-webhook-worker: "+errMsg, "provider", raw.ProviderSlug)
		_ = w.webhookLogStore.MarkProcessed(ctx, logID, logCreatedAt, nil, nil, nil, domain.WebhookLogFailed, errMsg)
		return nil // ACK — no normalizer, can't retry
	}

	// Step 4: Normalize.
	normalized, err := normalizer.NormalizeWebhook(raw.ProviderSlug, raw.RawBody)
	if err != nil {
		errMsg := fmt.Sprintf("normalization failed: %v", err)
		w.logger.Error("settla-inbound-webhook-worker: "+errMsg,
			"provider", raw.ProviderSlug, "error", err)
		_ = w.webhookLogStore.MarkProcessed(ctx, logID, logCreatedAt, nil, nil, nil, domain.WebhookLogFailed, errMsg)
		return nil // ACK — normalizer error, log for ops to investigate
	}
	if normalized == nil {
		// Non-terminal status (e.g., "pending") — skip.
		_ = w.webhookLogStore.MarkProcessed(ctx, logID, logCreatedAt, nil, nil, nil, domain.WebhookLogSkipped, "non-terminal status")
		return nil
	}

	// Step 5: Update log with normalization result.
	normalizedJSON, _ := json.Marshal(normalized)
	_ = w.webhookLogStore.MarkProcessed(ctx, logID, logCreatedAt,
		&normalized.TransferID, &normalized.TenantID,
		normalizedJSON, domain.WebhookLogProcessed, "")

	// Step 6: Process the normalized payload (same as handleOnRampWebhook / handleOffRampWebhook).
	w.logger.Info("settla-inbound-webhook-worker: normalized webhook",
		"provider", raw.ProviderSlug,
		"transfer_id", normalized.TransferID,
		"status", normalized.Status,
		"tx_type", normalized.TxType,
	)

	switch normalized.TxType {
	case string(domain.ProviderTxTypeOnRamp):
		return w.processNormalizedOnRamp(ctx, normalized)
	case string(domain.ProviderTxTypeOffRamp):
		return w.processNormalizedOffRamp(ctx, normalized)
	default:
		w.logger.Warn("settla-inbound-webhook-worker: unknown tx_type in normalized payload",
			"tx_type", normalized.TxType, "transfer_id", normalized.TransferID)
		return nil
	}
}

func (w *InboundWebhookWorker) handleOnRampWebhook(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
	if err != nil {
		w.logger.Error("settla-inbound-webhook-worker: failed to unmarshal on-ramp webhook payload",
			"event_id", event.ID, "error", err)
		return nil
	}

	w.logger.Info("settla-inbound-webhook-worker: processing on-ramp webhook",
		"transfer_id", payload.TransferID, "tenant_id", payload.TenantID,
		"provider_id", payload.ProviderID, "status", payload.Status)

	return w.processNormalizedOnRamp(ctx, payload)
}

func (w *InboundWebhookWorker) handleOffRampWebhook(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderWebhookPayload](event)
	if err != nil {
		w.logger.Error("settla-inbound-webhook-worker: failed to unmarshal off-ramp webhook payload",
			"event_id", event.ID, "error", err)
		return nil
	}

	w.logger.Info("settla-inbound-webhook-worker: processing off-ramp webhook",
		"transfer_id", payload.TransferID, "tenant_id", payload.TenantID,
		"provider_id", payload.ProviderID, "status", payload.Status)

	return w.processNormalizedOffRamp(ctx, payload)
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared processing logic for normalized payloads
// ──────────────────────────────────────────────────────────────────────────────

func (w *InboundWebhookWorker) processNormalizedOnRamp(ctx context.Context, payload *domain.ProviderWebhookPayload) error {
	txType := string(domain.ProviderTxTypeOnRamp)
	existing, err := w.transferStore.GetProviderTransaction(ctx, payload.TenantID, payload.TransferID, txType)
	if err != nil {
		return fmt.Errorf("settla-inbound-webhook-worker: looking up provider tx for %s: %w", payload.TransferID, err)
	}
	if existing == nil {
		w.logger.Warn("settla-inbound-webhook-worker: on-ramp webhook for unknown transfer, skipping",
			"transfer_id", payload.TransferID)
		return nil
	}
	if domain.ProviderTxStatus(existing.Status).IsTerminal() {
		w.logger.Info("settla-inbound-webhook-worker: on-ramp already terminal, skipping duplicate",
			"transfer_id", payload.TransferID, "existing_status", existing.Status)
		return nil
	}

	existing.Status = payload.Status
	if payload.TxHash != "" {
		existing.TxHash = payload.TxHash
	}
	if payload.ProviderRef != "" {
		existing.ExternalID = payload.ProviderRef
	}

	if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, txType, existing); err != nil {
		if isOptimisticLock(err) {
			w.logger.Info("settla-inbound-webhook-worker: on-ramp provider tx already advanced, skipping",
				"transfer_id", payload.TransferID)
			return nil
		}
		w.logger.Warn("settla-inbound-webhook-worker: failed to update provider tx, continuing",
			"transfer_id", payload.TransferID, "error", err)
	}

	switch domain.ProviderTxStatus(payload.Status) {
	case domain.ProviderTxStatusCompleted, domain.ProviderTxStatusConfirmed:
		result := domain.IntentResult{
			Success: true, ProviderRef: payload.ProviderRef, TxHash: payload.TxHash,
		}
		return w.handleEngineError(w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOnRampResult", payload.TransferID)
	case domain.ProviderTxStatusFailed:
		result := domain.IntentResult{
			Success: false, Error: payload.Error, ErrorCode: payload.ErrorCode,
		}
		return w.handleEngineError(w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOnRampResult", payload.TransferID)
	default:
		w.logger.Warn("settla-inbound-webhook-worker: on-ramp webhook with unexpected status, skipping",
			"transfer_id", payload.TransferID, "status", payload.Status)
		return nil
	}
}

func (w *InboundWebhookWorker) processNormalizedOffRamp(ctx context.Context, payload *domain.ProviderWebhookPayload) error {
	txType := string(domain.ProviderTxTypeOffRamp)
	existing, err := w.transferStore.GetProviderTransaction(ctx, payload.TenantID, payload.TransferID, txType)
	if err != nil {
		return fmt.Errorf("settla-inbound-webhook-worker: looking up provider tx for %s: %w", payload.TransferID, err)
	}
	if existing == nil {
		w.logger.Warn("settla-inbound-webhook-worker: off-ramp webhook for unknown transfer, skipping",
			"transfer_id", payload.TransferID)
		return nil
	}
	if domain.ProviderTxStatus(existing.Status).IsTerminal() {
		w.logger.Info("settla-inbound-webhook-worker: off-ramp already terminal, skipping duplicate",
			"transfer_id", payload.TransferID, "existing_status", existing.Status)
		return nil
	}

	existing.Status = payload.Status
	if payload.TxHash != "" {
		existing.TxHash = payload.TxHash
	}
	if payload.ProviderRef != "" {
		existing.ExternalID = payload.ProviderRef
	}

	if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, txType, existing); err != nil {
		if isOptimisticLock(err) {
			w.logger.Info("settla-inbound-webhook-worker: off-ramp provider tx already advanced, skipping",
				"transfer_id", payload.TransferID)
			return nil
		}
		w.logger.Warn("settla-inbound-webhook-worker: failed to update provider tx, continuing",
			"transfer_id", payload.TransferID, "error", err)
	}

	switch domain.ProviderTxStatus(payload.Status) {
	case domain.ProviderTxStatusCompleted, domain.ProviderTxStatusConfirmed:
		result := domain.IntentResult{
			Success: true, ProviderRef: payload.ProviderRef, TxHash: payload.TxHash,
		}
		return w.handleEngineError(w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOffRampResult", payload.TransferID)
	case domain.ProviderTxStatusFailed:
		result := domain.IntentResult{
			Success: false, Error: payload.Error, ErrorCode: payload.ErrorCode,
		}
		return w.handleEngineError(w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result), "HandleOffRampResult", payload.TransferID)
	default:
		w.logger.Warn("settla-inbound-webhook-worker: off-ramp webhook with unexpected status, skipping",
			"transfer_id", payload.TransferID, "status", payload.Status)
		return nil
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

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
			"step", step, "transfer_id", transferID)
	}
	return err
}

// Compile-time check that uuid.UUID implements fmt.Stringer.
var _ fmt.Stringer = uuid.UUID{}
