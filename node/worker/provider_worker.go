package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/resilience"
)

// ProviderTransferStore persists provider transaction records for the
// CHECK-BEFORE-CALL pattern. This prevents double-execution of provider calls
// when messages are redelivered.
type ProviderTransferStore interface {
	// GetProviderTransaction looks up an existing provider transaction by transfer ID and type.
	// tenantID scopes the query for tenant isolation.
	// Returns nil, nil if not found.
	GetProviderTransaction(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, txType string) (*domain.ProviderTx, error)
	// CreateProviderTransaction records a new provider transaction.
	CreateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error
	// UpdateProviderTransaction updates an existing provider transaction (e.g., pending → completed).
	UpdateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error
	// ClaimProviderTransaction atomically claims a provider transaction slot using
	// INSERT ON CONFLICT DO NOTHING semantics. Returns a non-nil UUID if the claim
	// succeeded (this worker owns execution), or nil if already claimed by another worker.
	// Returns nil, nil (not an error) if the transaction already has a terminal status
	// (completed/confirmed/pending), indicating the work is done and the caller should ACK.
	ClaimProviderTransaction(ctx context.Context, params ClaimProviderTransactionParams) (*uuid.UUID, error)
	// UpdateTransferRoute updates the provider IDs, chain, and stablecoin on a transfer
	// during fallback routing. This keeps the transfer record in sync with the actual
	// provider being used after a fallback.
	UpdateTransferRoute(ctx context.Context, transferID uuid.UUID, onRampProvider, offRampProvider, chain string, stableCoin domain.Currency) error
	// DeleteProviderTransaction removes a provider transaction record, allowing the
	// fallback provider to re-claim the slot.
	DeleteProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string) error
}

// ProviderWorker consumes provider intent messages from NATS and executes
// on-ramp and off-ramp operations against provider adapters. It uses the
// CHECK-BEFORE-CALL pattern to prevent double-execution on redelivery.
type ProviderWorker struct {
	onRampProviders  map[string]domain.OnRampProvider
	offRampProviders map[string]domain.OffRampProvider
	transferStore    ProviderTransferStore
	engine           SettlementEngine
	publisher        *messaging.Publisher
	subscriber       *messaging.StreamSubscriber
	logger           *slog.Logger
	onRampCBs        map[string]*resilience.CircuitBreaker
	offRampCBs       map[string]*resilience.CircuitBreaker
	partition        int
}

// NewProviderWorker creates a provider worker that subscribes to the providers stream.
func NewProviderWorker(
	partition int,
	onRampProviders map[string]domain.OnRampProvider,
	offRampProviders map[string]domain.OffRampProvider,
	transferStore ProviderTransferStore,
	engine SettlementEngine,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *ProviderWorker {
	onRampCBs := make(map[string]*resilience.CircuitBreaker)
	for id := range onRampProviders {
		onRampCBs[id] = resilience.NewCircuitBreaker(
			"provider-onramp-"+id,
			resilience.WithFailureThreshold(15),
			resilience.WithResetTimeout(10*time.Second),
			resilience.WithHalfOpenMax(2),
		)
	}
	offRampCBs := make(map[string]*resilience.CircuitBreaker)
	for id := range offRampProviders {
		offRampCBs[id] = resilience.NewCircuitBreaker(
			"provider-offramp-"+id,
			resilience.WithFailureThreshold(15),
			resilience.WithResetTimeout(10*time.Second),
			resilience.WithHalfOpenMax(2),
		)
	}

	consumerName := messaging.StreamConsumerName("settla-provider-worker", partition)

	return &ProviderWorker{
		onRampProviders:  onRampProviders,
		offRampProviders: offRampProviders,
		transferStore:    transferStore,
		engine:           engine,
		publisher:        messaging.NewPublisher(client),
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamProviders,
			consumerName,
			opts...,
		),
		logger:     logger.With("module", "provider-worker", "partition", partition),
		onRampCBs:  onRampCBs,
		offRampCBs: offRampCBs,
		partition:  partition,
	}
}

// Start begins consuming provider intent messages. Blocks until ctx is cancelled.
func (w *ProviderWorker) Start(ctx context.Context) error {
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixProvider, w.partition)
	w.logger.Info("settla-provider-worker: starting", "filter", filter)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the subscription.
func (w *ProviderWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes provider intent events to the appropriate handler.
func (w *ProviderWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentProviderOnRamp:
		return w.handleOnRamp(ctx, event)
	case domain.IntentProviderOffRamp:
		return w.handleOffRamp(ctx, event)
	default:
		w.logger.Debug("settla-provider-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// shouldRetryProviderError returns true if the error is retryable.
// Circuit-open and context-cancelled errors are not retried.
func shouldRetryProviderError(err error) bool {
	if errors.Is(err, resilience.ErrCircuitOpen) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// executeOnRampWithCB wraps the on-ramp provider call with a circuit breaker.
func (w *ProviderWorker) executeOnRampWithCB(ctx context.Context, providerID string, provider domain.OnRampProvider, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	cb, ok := w.onRampCBs[providerID]
	if !ok {
		return provider.Execute(ctx, req)
	}
	var result *domain.ProviderTx
	err := cb.Execute(ctx, func(ctx context.Context) error {
		var execErr error
		result, execErr = provider.Execute(ctx, req)
		return execErr
	})
	return result, err
}

// executeOnRamp wraps the CB call with retry (3 attempts, 200ms initial, 2x backoff).
func (w *ProviderWorker) executeOnRamp(ctx context.Context, providerID string, provider domain.OnRampProvider, req domain.OnRampRequest) (*domain.ProviderTx, error) {
	var result *domain.ProviderTx
	err := resilience.Retry(ctx, resilience.RetryConfig{
		Operation:    "provider-onramp-" + providerID,
		MaxAttempts:  3,
		InitialDelay: 200 * time.Millisecond,
		Multiplier:   2.0,
	}, shouldRetryProviderError, func(ctx context.Context) error {
		var retryErr error
		result, retryErr = w.executeOnRampWithCB(ctx, providerID, provider, req)
		return retryErr
	})
	return result, err
}

// executeOffRampWithCB wraps the off-ramp provider call with a circuit breaker.
func (w *ProviderWorker) executeOffRampWithCB(ctx context.Context, providerID string, provider domain.OffRampProvider, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	cb, ok := w.offRampCBs[providerID]
	if !ok {
		return provider.Execute(ctx, req)
	}
	var result *domain.ProviderTx
	err := cb.Execute(ctx, func(ctx context.Context) error {
		var execErr error
		result, execErr = provider.Execute(ctx, req)
		return execErr
	})
	return result, err
}

// executeOffRamp wraps the CB call with retry (3 attempts, 200ms initial, 2x backoff).
func (w *ProviderWorker) executeOffRamp(ctx context.Context, providerID string, provider domain.OffRampProvider, req domain.OffRampRequest) (*domain.ProviderTx, error) {
	var result *domain.ProviderTx
	err := resilience.Retry(ctx, resilience.RetryConfig{
		Operation:    "provider-offramp-" + providerID,
		MaxAttempts:  3,
		InitialDelay: 200 * time.Millisecond,
		Multiplier:   2.0,
	}, shouldRetryProviderError, func(ctx context.Context) error {
		var retryErr error
		result, retryErr = w.executeOffRampWithCB(ctx, providerID, provider, req)
		return retryErr
	})
	return result, err
}

// handleOnRamp implements the atomic CLAIM-CALL-UPDATE pattern for on-ramp execution.
// Uses an iterative loop for fallback routing instead of recursion to avoid unbounded stack growth.
func (w *ProviderWorker) handleOnRamp(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderOnRampPayload](event)
	if err != nil {
		w.logger.Error("settla-provider-worker: failed to unmarshal on-ramp payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	budget := resilience.NewTimeoutBudget(ctx, 30*time.Second)

	for {
		w.logger.Info("settla-provider-worker: processing on-ramp intent",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"provider_id", payload.ProviderID,
		)

		claimCtx, claimCancel := budget.Allocate(5 * time.Second)
		claimID, err := w.transferStore.ClaimProviderTransaction(claimCtx, ClaimProviderTransactionParams{
			TenantID:   payload.TenantID,
			TransferID: payload.TransferID,
			TxType:     "onramp",
			Provider:   payload.ProviderID,
		})
		claimCancel()
		if err != nil {
			return fmt.Errorf("settla-provider-worker: claim onramp for %s: %w", payload.TransferID, err)
		}
		if claimID == nil {
			w.logger.Info("settla-provider-worker: on-ramp already claimed, skipping",
				"transfer_id", payload.TransferID)
			return nil
		}

		provider, ok := w.onRampProviders[payload.ProviderID]
		if !ok {
			w.logger.Error("settla-provider-worker: unknown on-ramp provider",
				"provider_id", payload.ProviderID,
				"transfer_id", payload.TransferID,
			)
			failedTx := &domain.ProviderTx{Status: "failed"}
			_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "onramp", failedTx)
			result := domain.IntentResult{
				Success:   false,
				Error:     fmt.Sprintf("unknown on-ramp provider: %s", payload.ProviderID),
				ErrorCode: "PROVIDER_NOT_FOUND",
			}
			if err := w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
				if errors.Is(err, core.ErrOptimisticLock) {
					w.logger.Warn("settla-provider-worker: optimistic lock on on-ramp result, NAKing for retry",
						"transfer_id", payload.TransferID)
				}
				return err
			}
			return nil
		}

		callCtx, callCancel := budget.Allocate(20 * time.Second)
		tx, execErr := w.executeOnRamp(callCtx, payload.ProviderID, provider, domain.OnRampRequest{
			Amount:       payload.Amount,
			FromCurrency: payload.FromCurrency,
			ToCurrency:   payload.ToCurrency,
			Reference:    payload.Reference,
			QuotedRate:   payload.QuotedRate,
		})
		callCancel()

		if execErr != nil {
			if errors.Is(execErr, resilience.ErrCircuitOpen) {
				w.logger.Warn("settla-provider-worker: circuit breaker open, NAKing for retry",
					"provider_id", payload.ProviderID, "transfer_id", payload.TransferID)
				return execErr // NAK
			}

			w.logger.Warn("settla-provider-worker: on-ramp execution failed",
				"transfer_id", payload.TransferID,
				"provider_id", payload.ProviderID,
				"error", execErr,
			)

			// Try fallback alternative if available
			if len(payload.Alternatives) > 0 {
				alt := payload.Alternatives[0]
				remaining := payload.Alternatives[1:]

				w.logger.Info("settla-provider-worker: attempting on-ramp fallback",
					"transfer_id", payload.TransferID,
					"fallback_provider", alt.ProviderID,
					"remaining_alternatives", len(remaining),
				)

				if err := w.transferStore.DeleteProviderTransaction(ctx, payload.TransferID, "onramp"); err != nil {
					w.logger.Warn("settla-provider-worker: failed to delete provider tx for fallback",
						"transfer_id", payload.TransferID, "error", err)
				}
				if err := w.transferStore.UpdateTransferRoute(ctx, payload.TransferID,
					alt.ProviderID, alt.OffRampProvider, alt.Chain, alt.StableCoin); err != nil {
					w.logger.Warn("settla-provider-worker: failed to update transfer route for fallback",
						"transfer_id", payload.TransferID, "error", err)
					// Fall through to report failure to engine
				} else {
					// Iterate with the fallback provider instead of recursing
					payload.ProviderID = alt.ProviderID
					payload.ToCurrency = alt.StableCoin
					payload.Alternatives = remaining
					continue
				}
			}

			failedTx := &domain.ProviderTx{Status: "failed"}
			_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "onramp", failedTx)

			result := domain.IntentResult{
				Success:   false,
				Error:     execErr.Error(),
				ErrorCode: "ONRAMP_EXECUTION_FAILED",
			}
			if err := w.engine.HandleOnRampResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
				if errors.Is(err, core.ErrOptimisticLock) {
					w.logger.Warn("settla-provider-worker: optimistic lock on on-ramp result, NAKing for retry",
						"transfer_id", payload.TransferID)
				}
				return err
			}
			return nil
		}

		// Record the transaction result
		if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "onramp", tx); err != nil {
			w.logger.Warn("settla-provider-worker: failed to record provider tx, continuing",
				"transfer_id", payload.TransferID,
				"error", err,
			)
		}

		if tx.Status == "pending" {
			w.logger.Info("settla-provider-worker: on-ramp pending, awaiting webhook",
				"transfer_id", payload.TransferID,
				"provider_tx_id", tx.ID,
			)
			return nil
		}

		w.logger.Info("settla-provider-worker: on-ramp completed synchronously",
			"transfer_id", payload.TransferID,
			"provider_tx_id", tx.ID,
		)

		result := domain.IntentResult{
			Success:     true,
			ProviderRef: tx.ExternalID,
			TxHash:      tx.TxHash,
		}
		reportCtx, reportCancel := budget.Allocate(5 * time.Second)
		err = w.engine.HandleOnRampResult(reportCtx, payload.TenantID, payload.TransferID, result)
		reportCancel()
		if err != nil {
			if errors.Is(err, core.ErrOptimisticLock) {
				w.logger.Warn("settla-provider-worker: optimistic lock on on-ramp result, NAKing for retry",
					"transfer_id", payload.TransferID)
			}
			return err
		}
		return nil
	}
}

// handleOffRamp implements the atomic CLAIM-CALL-UPDATE pattern for off-ramp execution.
// Uses an iterative loop for fallback routing instead of recursion to avoid unbounded stack growth.
func (w *ProviderWorker) handleOffRamp(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.ProviderOffRampPayload](event)
	if err != nil {
		w.logger.Error("settla-provider-worker: failed to unmarshal off-ramp payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	budget := resilience.NewTimeoutBudget(ctx, 30*time.Second)

	for {
		w.logger.Info("settla-provider-worker: processing off-ramp intent",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"provider_id", payload.ProviderID,
		)

		claimCtx, claimCancel := budget.Allocate(5 * time.Second)
		claimID, err := w.transferStore.ClaimProviderTransaction(claimCtx, ClaimProviderTransactionParams{
			TenantID:   payload.TenantID,
			TransferID: payload.TransferID,
			TxType:     "offramp",
			Provider:   payload.ProviderID,
		})
		claimCancel()
		if err != nil {
			return fmt.Errorf("settla-provider-worker: claim offramp for %s: %w", payload.TransferID, err)
		}
		if claimID == nil {
			w.logger.Info("settla-provider-worker: off-ramp already claimed, skipping",
				"transfer_id", payload.TransferID)
			return nil
		}

		provider, ok := w.offRampProviders[payload.ProviderID]
		if !ok {
			w.logger.Error("settla-provider-worker: unknown off-ramp provider",
				"provider_id", payload.ProviderID,
				"transfer_id", payload.TransferID,
			)
			failedTx := &domain.ProviderTx{Status: "failed"}
			_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "offramp", failedTx)
			result := domain.IntentResult{
				Success:   false,
				Error:     fmt.Sprintf("unknown off-ramp provider: %s", payload.ProviderID),
				ErrorCode: "PROVIDER_NOT_FOUND",
			}
			if err := w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
				if errors.Is(err, core.ErrOptimisticLock) {
					w.logger.Warn("settla-provider-worker: optimistic lock on off-ramp result, NAKing for retry",
						"transfer_id", payload.TransferID)
				}
				return err
			}
			return nil
		}

		callCtx, callCancel := budget.Allocate(20 * time.Second)
		tx, execErr := w.executeOffRamp(callCtx, payload.ProviderID, provider, domain.OffRampRequest{
			Amount:       payload.Amount,
			FromCurrency: payload.FromCurrency,
			ToCurrency:   payload.ToCurrency,
			Recipient:    payload.Recipient,
			Reference:    payload.Reference,
			QuotedRate:   payload.QuotedRate,
		})
		callCancel()

		if execErr != nil {
			if errors.Is(execErr, resilience.ErrCircuitOpen) {
				w.logger.Warn("settla-provider-worker: circuit breaker open, NAKing for retry",
					"provider_id", payload.ProviderID, "transfer_id", payload.TransferID)
				return execErr // NAK
			}

			w.logger.Warn("settla-provider-worker: off-ramp execution failed",
				"transfer_id", payload.TransferID,
				"provider_id", payload.ProviderID,
				"error", execErr,
			)

			// Try fallback alternative if available
			if len(payload.Alternatives) > 0 {
				alt := payload.Alternatives[0]
				remaining := payload.Alternatives[1:]

				w.logger.Info("settla-provider-worker: attempting off-ramp fallback",
					"transfer_id", payload.TransferID,
					"fallback_provider", alt.ProviderID,
					"remaining_alternatives", len(remaining),
				)

				if err := w.transferStore.DeleteProviderTransaction(ctx, payload.TransferID, "offramp"); err != nil {
					w.logger.Warn("settla-provider-worker: failed to delete provider tx for fallback",
						"transfer_id", payload.TransferID, "error", err)
				}
				if err := w.transferStore.UpdateTransferRoute(ctx, payload.TransferID,
					"", alt.ProviderID, "", ""); err != nil {
					w.logger.Warn("settla-provider-worker: failed to update transfer route for off-ramp fallback",
						"transfer_id", payload.TransferID, "error", err)
				} else {
					// Iterate with the fallback provider instead of recursing
					payload.ProviderID = alt.ProviderID
					payload.Alternatives = remaining
					continue
				}
			}

			failedTx := &domain.ProviderTx{Status: "failed"}
			_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "offramp", failedTx)

			result := domain.IntentResult{
				Success:   false,
				Error:     execErr.Error(),
				ErrorCode: "OFFRAMP_EXECUTION_FAILED",
			}
			if err := w.engine.HandleOffRampResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
				if errors.Is(err, core.ErrOptimisticLock) {
					w.logger.Warn("settla-provider-worker: optimistic lock on off-ramp result, NAKing for retry",
						"transfer_id", payload.TransferID)
				}
				return err
			}
			return nil
		}

		if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "offramp", tx); err != nil {
			w.logger.Warn("settla-provider-worker: failed to record provider tx, continuing",
				"transfer_id", payload.TransferID,
				"error", err,
			)
		}

		if tx.Status == "pending" {
			w.logger.Info("settla-provider-worker: off-ramp pending, awaiting webhook",
				"transfer_id", payload.TransferID,
				"provider_tx_id", tx.ID,
			)
			return nil
		}

		w.logger.Info("settla-provider-worker: off-ramp completed synchronously",
			"transfer_id", payload.TransferID,
			"provider_tx_id", tx.ID,
		)

		result := domain.IntentResult{
			Success:     true,
			ProviderRef: tx.ExternalID,
		}
		reportCtx, reportCancel := budget.Allocate(5 * time.Second)
		err = w.engine.HandleOffRampResult(reportCtx, payload.TenantID, payload.TransferID, result)
		reportCancel()
		if err != nil {
			if errors.Is(err, core.ErrOptimisticLock) {
				w.logger.Warn("settla-provider-worker: optimistic lock on off-ramp result, NAKing for retry",
					"transfer_id", payload.TransferID)
			}
			return err
		}
		return nil
	}
}
