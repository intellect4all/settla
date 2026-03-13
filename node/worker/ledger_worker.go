package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// LedgerWorker consumes ledger intent messages from NATS and executes
// journal entry posts and reversals against the Ledger service.
// TigerBeetle handles idempotency at the engine level via IdempotencyKey.
type LedgerWorker struct {
	ledger     domain.Ledger
	subscriber *messaging.StreamSubscriber
	logger     *slog.Logger
	partition  int
}

// NewLedgerWorker creates a ledger worker that subscribes to the ledger stream.
func NewLedgerWorker(
	partition int,
	ledger domain.Ledger,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *LedgerWorker {
	consumerName := messaging.StreamConsumerName("settla-ledger-worker", partition)

	return &LedgerWorker{
		ledger: ledger,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamLedger,
			consumerName,
			opts...,
		),
		logger:    logger.With("module", "ledger-worker", "partition", partition),
		partition: partition,
	}
}

// Start begins consuming ledger intent messages. Blocks until ctx is cancelled.
func (w *LedgerWorker) Start(ctx context.Context) error {
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixLedger, w.partition)
	w.logger.Info("settla-ledger-worker: starting", "filter", filter)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the subscription.
func (w *LedgerWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes ledger intent events to the appropriate handler.
func (w *LedgerWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentLedgerPost:
		return w.handlePost(ctx, event)
	case domain.IntentLedgerReverse:
		return w.handleReverse(ctx, event)
	default:
		w.logger.Debug("settla-ledger-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// handlePost builds a journal entry from the payload and posts it to the ledger.
func (w *LedgerWorker) handlePost(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.LedgerPostPayload](event)
	if err != nil {
		w.logger.Error("settla-ledger-worker: failed to unmarshal post payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-ledger-worker: posting journal entry",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"idempotency_key", payload.IdempotencyKey,
		"lines_count", len(payload.Lines),
	)

	// Build the journal entry from the payload
	entry := domain.JournalEntry{
		ID:             uuid.Must(uuid.NewV7()),
		TenantID:       &payload.TenantID,
		IdempotencyKey: payload.IdempotencyKey,
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    payload.Description,
		ReferenceType:  payload.ReferenceType,
		ReferenceID:    &payload.TransferID,
		Lines:          make([]domain.EntryLine, len(payload.Lines)),
	}

	for i, line := range payload.Lines {
		entry.Lines[i] = domain.EntryLine{
			ID: uuid.Must(uuid.NewV7()),
			Posting: domain.Posting{
				AccountCode: line.AccountCode,
				EntryType:   domain.EntryType(line.EntryType),
				Amount:      line.Amount,
				Currency:    domain.Currency(line.Currency),
				Description: line.Description,
			},
		}
	}

	// Post to ledger — TigerBeetle handles idempotency via IdempotencyKey
	_, err = w.ledger.PostEntries(ctx, entry)
	if err != nil {
		w.logger.Error("settla-ledger-worker: failed to post journal entry",
			"transfer_id", payload.TransferID,
			"idempotency_key", payload.IdempotencyKey,
			"error", err,
		)
		return fmt.Errorf("settla-ledger-worker: posting entry for transfer %s: %w", payload.TransferID, err)
	}

	w.logger.Info("settla-ledger-worker: journal entry posted",
		"transfer_id", payload.TransferID,
		"idempotency_key", payload.IdempotencyKey,
	)

	return nil
}

// handleReverse creates a reversal journal entry for the given transfer.
func (w *LedgerWorker) handleReverse(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.LedgerPostPayload](event)
	if err != nil {
		w.logger.Error("settla-ledger-worker: failed to unmarshal reverse payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-ledger-worker: reversing entry",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"idempotency_key", payload.IdempotencyKey,
	)

	// ReverseEntry creates counter-entries that zero out the original posting.
	// The transfer_id is used to find the original entry to reverse.
	_, err = w.ledger.ReverseEntry(ctx, payload.TransferID, payload.Description)
	if err != nil {
		w.logger.Error("settla-ledger-worker: failed to reverse entry",
			"transfer_id", payload.TransferID,
			"error", err,
		)
		return fmt.Errorf("settla-ledger-worker: reversing entry for transfer %s: %w", payload.TransferID, err)
	}

	w.logger.Info("settla-ledger-worker: entry reversed",
		"transfer_id", payload.TransferID,
	)

	return nil
}
