package worker

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	deposit "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// DepositEngine abstracts the deposit session engine for the worker.
type DepositEngine interface {
	HandleTransactionDetected(ctx context.Context, tenantID, sessionID uuid.UUID, tx domain.IncomingTransaction) error
	HandleTransactionConfirmed(ctx context.Context, tenantID, sessionID uuid.UUID, txHash string, confirmations int32) error
	HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error
	HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error
}

// Compile-time interface check that Engine satisfies DepositEngine.
var _ DepositEngine = (*deposit.Engine)(nil)

// DepositWorker consumes deposit events from the SETTLA_CRYPTO_DEPOSITS stream
// and routes them to the deposit engine.
type DepositWorker struct {
	partition  int
	engine     DepositEngine
	subscriber *messaging.StreamSubscriber
	logger     *slog.Logger
}

// NewDepositWorker creates a deposit worker for a given partition.
func NewDepositWorker(
	partition int,
	engine DepositEngine,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *DepositWorker {
	consumerName := messaging.StreamConsumerName("settla-deposit-worker", partition)
	return &DepositWorker{
		partition: partition,
		engine:    engine,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamCryptoDeposits,
			consumerName,
			opts...,
		),
		logger: logger.With("module", "deposit-worker", "partition", partition),
	}
}

// Start begins consuming deposit events. Blocks until ctx is cancelled.
func (w *DepositWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-deposit-worker: starting", "partition", w.partition)
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixDeposit, w.partition)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop gracefully stops the subscriber.
func (w *DepositWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes deposit events to the appropriate engine method.
func (w *DepositWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.EventDepositTxDetected:
		return w.handleTxDetected(ctx, event)
	case domain.EventDepositTxConfirmed:
		return w.handleTxConfirmed(ctx, event)
	case domain.IntentCreditDeposit:
		return w.handleCreditResult(ctx, event)
	case domain.IntentSettleDeposit:
		return w.handleSettlementResult(ctx, event)
	default:
		w.logger.Debug("settla-deposit-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil // ACK
	}
}

func (w *DepositWorker) handleTxDetected(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.DepositTxDetectedPayload](event)
	if err != nil {
		w.logger.Error("settla-deposit-worker: failed to unmarshal tx.detected payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-deposit-worker: handling tx.detected",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
		"tx_hash", payload.TxHash,
		"amount", payload.Amount,
	)

	incoming := domain.IncomingTransaction{
		Chain:         payload.Chain,
		TxHash:        payload.TxHash,
		TokenContract: payload.Token,
		Amount:        payload.Amount,
		BlockNumber:   payload.BlockNumber,
	}

	return w.engine.HandleTransactionDetected(ctx, payload.TenantID, payload.SessionID, incoming)
}

func (w *DepositWorker) handleTxConfirmed(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.DepositTxConfirmedPayload](event)
	if err != nil {
		w.logger.Error("settla-deposit-worker: failed to unmarshal tx.confirmed payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-deposit-worker: handling tx.confirmed",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
		"tx_hash", payload.TxHash,
		"confirmations", payload.Confirmations,
	)

	return w.engine.HandleTransactionConfirmed(ctx, payload.TenantID, payload.SessionID, payload.TxHash, payload.Confirmations)
}

func (w *DepositWorker) handleCreditResult(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.CreditDepositPayload](event)
	if err != nil {
		w.logger.Error("settla-deposit-worker: failed to unmarshal credit result payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-deposit-worker: handling credit result",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
	)

	// The credit intent is an outgoing instruction. When the credit has been
	// performed externally (by ledger/treasury), the result is fed back here.
	// For now, we auto-succeed (the actual credit worker would handle the
	// ledger+treasury calls and report success/failure).
	result := domain.IntentResult{
		Success: true,
	}

	return w.engine.HandleCreditResult(ctx, payload.TenantID, payload.SessionID, result)
}

func (w *DepositWorker) handleSettlementResult(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.SettleDepositPayload](event)
	if err != nil {
		w.logger.Error("settla-deposit-worker: failed to unmarshal settlement result payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-deposit-worker: handling settlement result",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
	)

	result := domain.IntentResult{
		Success: true,
	}

	return w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.SessionID, result)
}
