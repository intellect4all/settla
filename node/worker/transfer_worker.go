package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
)

// SettlementEngine is the interface the worker needs from the core engine.
// It's a subset of core.Engine methods so the worker doesn't depend on concrete types.
type SettlementEngine interface {
	FundTransfer(ctx context.Context, transferID uuid.UUID) error
	InitiateOnRamp(ctx context.Context, transferID uuid.UUID) error
	SettleOnChain(ctx context.Context, transferID uuid.UUID) error
	InitiateOffRamp(ctx context.Context, transferID uuid.UUID) error
	CompleteTransfer(ctx context.Context, transferID uuid.UUID) error
	FailTransfer(ctx context.Context, transferID uuid.UUID, reason, code string) error
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
func NewTransferWorker(partition int, engine SettlementEngine, client *messaging.Client, logger *slog.Logger, metrics *observability.Metrics) *TransferWorker {
	return &TransferWorker{
		partition:  partition,
		engine:     engine,
		subscriber: messaging.NewSubscriber(client, partition),
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

	switch event.Type {
	case domain.EventTransferCreated:
		return w.callEngine(ctx, "FundTransfer", transferID, w.engine.FundTransfer)

	case domain.EventTransferFunded:
		return w.callEngine(ctx, "InitiateOnRamp", transferID, w.engine.InitiateOnRamp)

	case domain.EventOnRampCompleted:
		return w.callEngine(ctx, "SettleOnChain", transferID, w.engine.SettleOnChain)

	case domain.EventSettlementCompleted:
		return w.callEngine(ctx, "InitiateOffRamp", transferID, w.engine.InitiateOffRamp)

	case domain.EventOffRampCompleted:
		return w.callEngine(ctx, "CompleteTransfer", transferID, w.engine.CompleteTransfer)

	case domain.EventTransferFailed:
		// Already in FAILED state — no further action needed.
		// A separate refund workflow can be triggered by an operator.
		w.logger.Info("settla-worker: transfer failed, no automatic action",
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

// callEngine invokes an engine step and treats state-transition errors as
// non-retryable (the transfer already advanced past this step).
func (w *TransferWorker) callEngine(ctx context.Context, step string, transferID uuid.UUID, fn func(context.Context, uuid.UUID) error) error {
	err := fn(ctx, transferID)
	if err != nil && strings.Contains(err.Error(), "invalid transition") {
		w.logger.Info("settla-worker: skipping already-transitioned transfer",
			"step", step,
			"transfer_id", transferID,
			"error", err,
		)
		return nil // ack — retrying won't help
	}
	return err
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
