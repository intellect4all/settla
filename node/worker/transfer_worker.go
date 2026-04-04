package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
)

// SettlementEngine is the interface the worker needs from the core engine.
// It's a subset of core.Engine methods so the worker doesn't depend on concrete types.
//
// With the outbox pattern, the engine is a pure state machine. Workers process
// outbox intents (treasury reserve, provider calls, etc.) and report results
// back via Handle*Result methods. The event-driven routing below bridges the
// old event-based flow to the new outbox-based flow.
type SettlementEngine interface {
	FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
	InitiateOnRamp(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
	HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	HandleSettlementResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	HandleOffRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	CompleteTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
	FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason, code string) error
}

// TransferWorker consumes events from a single NATS partition and routes them
// to the settlement engine. Each partition handles one or more tenants, with
// events processed in order within a partition.
type TransferWorker struct {
	partition  int
	engine     SettlementEngine
	subscriber *messaging.Subscriber
	logger     *slog.Logger
	metrics    *observability.Metrics
}

// NewTransferWorker creates a worker bound to a specific partition.
func NewTransferWorker(partition int, engine SettlementEngine, client *messaging.Client, logger *slog.Logger, metrics *observability.Metrics, opts ...messaging.SubscriberOption) *TransferWorker {
	return &TransferWorker{
		partition:  partition,
		engine:     engine,
		subscriber: messaging.NewSubscriber(client, partition, opts...),
		logger:     logger.With("module", "worker", "partition", partition),
		metrics:    metrics,
	}
}

// Start begins consuming events from the partition. Blocks until ctx is cancelled.
func (w *TransferWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-worker: starting", "partition", w.partition)
	return w.subscriber.Subscribe(ctx, w.handleEvent)
}

// Stop cancels the subscription.
func (w *TransferWorker) Stop() {
	w.subscriber.Stop()
}

// LastProcessedAt returns the time the last message was processed (for liveness checks).
func (w *TransferWorker) LastProcessedAt() time.Time {
	return w.subscriber.LastProcessedAt()
}

// handleEvent routes a domain event to the appropriate engine method.
func (w *TransferWorker) handleEvent(ctx context.Context, event domain.Event) error {
	start := time.Now()
	partStr := strconv.Itoa(w.partition)

	transferID, err := w.extractTransferID(event)
	if err != nil {
		w.logger.Warn("settla-worker: cannot extract transfer ID, skipping",
			"event_id", event.ID,
			"event_type", event.Type,
			"error", err,
		)
		if w.metrics != nil {
			w.metrics.NATSMessagesTotal.WithLabelValues(partStr, "skipped").Inc()
		}
		return nil // ack — we can't process this event
	}

	w.logger.Info("settla-worker: processing event",
		"event_id", event.ID,
		"event_type", event.Type,
		"tenant_id", event.TenantID,
		"transfer_id", transferID,
	)

	defer func() {
		status := "ok"
		if err != nil {
			status = "error"
		}
		if w.metrics != nil {
			w.metrics.NATSMessagesTotal.WithLabelValues(partStr, status).Inc()
		}
		_ = start // suppress unused warning
	}()

	tenantID := event.TenantID

	switch event.Type {
	case domain.EventTransferCreated:
		return w.callEngine(ctx, "FundTransfer", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.FundTransfer(ctx, tenantID, id)
		})

	case domain.EventTransferFunded:
		return w.callEngine(ctx, "InitiateOnRamp", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.InitiateOnRamp(ctx, tenantID, id)
		})

	// Worker result events — treasury worker reports these after executing intents
	case domain.EventTreasuryReserved:
		// Treasury reserve succeeded — initiate on-ramp
		return w.callEngine(ctx, "InitiateOnRamp", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.InitiateOnRamp(ctx, tenantID, id)
		})

	case domain.EventTreasuryFailed:
		// Treasury reserve failed — fail the transfer
		errMsg := w.extractErrorFromEvent(event)
		return w.callEngine(ctx, "FailTransfer", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.FailTransfer(ctx, tenantID, id, errMsg, "TREASURY_FAILED")
		})

	// Provider worker result events
	case domain.EventProviderOnRampDone:
		return w.callEngineResult(ctx, "HandleOnRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOnRampResult(ctx, tenantID, id, domain.IntentResult{Success: true})
		})

	case domain.EventProviderOnRampFailed:
		errMsg := w.extractErrorFromEvent(event)
		return w.callEngineResult(ctx, "HandleOnRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOnRampResult(ctx, tenantID, id, domain.IntentResult{
				Success:   false,
				Error:     errMsg,
				ErrorCode: "ONRAMP_FAILED",
			})
		})

	// Blockchain worker result events
	case domain.EventBlockchainConfirmed:
		txHash := w.extractTxHashFromEvent(event)
		return w.callEngineResult(ctx, "HandleSettlementResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleSettlementResult(ctx, tenantID, id, domain.IntentResult{
				Success: true,
				TxHash:  txHash,
			})
		})

	case domain.EventBlockchainFailed:
		errMsg := w.extractErrorFromEvent(event)
		return w.callEngineResult(ctx, "HandleSettlementResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleSettlementResult(ctx, tenantID, id, domain.IntentResult{
				Success:   false,
				Error:     errMsg,
				ErrorCode: "BLOCKCHAIN_FAILED",
			})
		})

	// Provider off-ramp result events
	case domain.EventProviderOffRampDone:
		return w.callEngineResult(ctx, "HandleOffRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOffRampResult(ctx, tenantID, id, domain.IntentResult{Success: true})
		})

	case domain.EventProviderOffRampFailed:
		errMsg := w.extractErrorFromEvent(event)
		return w.callEngineResult(ctx, "HandleOffRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOffRampResult(ctx, tenantID, id, domain.IntentResult{
				Success:   false,
				Error:     errMsg,
				ErrorCode: "OFFRAMP_FAILED",
			})
		})

	// Legacy events — still handled for backward compatibility
	case domain.EventOnRampCompleted:
		return w.callEngineResult(ctx, "HandleOnRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOnRampResult(ctx, tenantID, id, domain.IntentResult{Success: true})
		})

	case domain.EventSettlementCompleted:
		return w.callEngineResult(ctx, "HandleSettlementResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleSettlementResult(ctx, tenantID, id, domain.IntentResult{Success: true})
		})

	case domain.EventOffRampCompleted:
		return w.callEngineResult(ctx, "HandleOffRampResult", transferID, func(ctx context.Context, id uuid.UUID) error {
			return w.engine.HandleOffRampResult(ctx, tenantID, id, domain.IntentResult{Success: true})
		})

	case domain.EventTransferFailed:
		// Already in FAILED state — no further action needed.
		// A separate refund workflow can be triggered by an operator.
		w.logger.Info("settla-worker: transfer failed, no automatic action",
			"transfer_id", transferID,
		)
		return nil

	case domain.EventTreasuryReleased, domain.EventLedgerPosted, domain.EventLedgerReversed:
		// Acknowledgment events from workers — no engine action needed
		w.logger.Debug("settla-worker: worker acknowledgment event, no action",
			"event_type", event.Type,
			"transfer_id", transferID,
		)
		return nil

	case domain.EventRefundCompleted:
		w.logger.Info("settla-worker: refund completed",
			"transfer_id", transferID,
		)
		return nil

	default:
		w.logger.Debug("settla-worker: unhandled event type, skipping",
			"event_type", event.Type,
			"transfer_id", transferID,
		)
		return nil
	}
}

// callEngine invokes an engine step and classifies errors:
//   - Invalid transition → ACK (transfer already advanced past this step)
//   - Optimistic lock conflict → return error for NAK/retry (concurrent modification, retryable)
//   - Other errors → return error for NAK/retry
func (w *TransferWorker) callEngine(ctx context.Context, step string, transferID uuid.UUID, fn func(context.Context, uuid.UUID) error) error {
	err := fn(ctx, transferID)
	if err == nil {
		return nil
	}
	var domErr *domain.DomainError
	if errors.As(err, &domErr) && domErr.Code() == domain.CodeInvalidTransition {
		w.logger.Info("settla-worker: skipping already-transitioned transfer",
			"step", step,
			"transfer_id", transferID,
			"error", err,
		)
		return nil // ack — retrying won't help
	}
	if errors.Is(err, core.ErrOptimisticLock) {
		w.logger.Warn("settla-worker: optimistic lock conflict, NAKing for retry",
			"step", step,
			"transfer_id", transferID,
		)
	}
	return err // NAK — NATS backoff will handle retry
}

// callEngineResult is the same as callEngine but for methods that use the result pattern.
func (w *TransferWorker) callEngineResult(ctx context.Context, step string, transferID uuid.UUID, fn func(context.Context, uuid.UUID) error) error {
	return w.callEngine(ctx, step, transferID, fn)
}

// extractErrorFromEvent pulls an error message from the event data map.
func (w *TransferWorker) extractErrorFromEvent(event domain.Event) string {
	if data, ok := event.Data.(map[string]any); ok {
		if errMsg, ok := data["error"]; ok {
			if s, ok := errMsg.(string); ok {
				return s
			}
		}
	}
	return "unknown error"
}

// extractTxHashFromEvent pulls a blockchain tx hash from the event data map.
func (w *TransferWorker) extractTxHashFromEvent(event domain.Event) string {
	if data, ok := event.Data.(map[string]any); ok {
		if hash, ok := data["tx_hash"]; ok {
			if s, ok := hash.(string); ok {
				return s
			}
		}
	}
	return ""
}

// extractTransferID pulls the transfer ID from the event data.
// Event.Data can be *domain.Transfer, map[string]any, or a struct with TransferID.
func (w *TransferWorker) extractTransferID(event domain.Event) (uuid.UUID, error) {
	switch data := event.Data.(type) {
	case *domain.Transfer:
		return data.ID, nil

	case domain.Transfer:
		return data.ID, nil

	case map[string]any:
		if id, ok := data["transfer_id"]; ok {
			switch v := id.(type) {
			case uuid.UUID:
				return v, nil
			case string:
				return uuid.Parse(v)
			}
		}
		if id, ok := data["ID"]; ok {
			switch v := id.(type) {
			case uuid.UUID:
				return v, nil
			case string:
				return uuid.Parse(v)
			}
		}

	case map[string]string:
		if id, ok := data["transfer_id"]; ok {
			return uuid.Parse(id)
		}
	}

	return uuid.Nil, fmt.Errorf("settla-worker: no transfer_id in event data of type %T", event.Data)
}
