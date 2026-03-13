package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/resilience"
)

var (
	pendingTxCount = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "settla",
		Subsystem: "blockchain_worker",
		Name:      "pending_tx_count",
		Help:      "Number of blockchain transactions currently tracked as pending.",
	})
	pendingTxRecovered = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "blockchain_worker",
		Name:      "pending_tx_recovered_total",
		Help:      "Number of pending blockchain transactions recovered (confirmed or failed) by the poller.",
	})
	pendingTxEscalated = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "settla",
		Subsystem: "blockchain_worker",
		Name:      "pending_tx_escalated_total",
		Help:      "Number of pending blockchain transactions escalated to manual review.",
	})
)

// pendingEntry tracks a blockchain transaction that returned "pending" status.
type pendingEntry struct {
	payload  *domain.BlockchainSendPayload
	txHash   string
	tenantID uuid.UUID
	addedAt  time.Time
}

// pendingTxEscalationTimeout is how long a transaction can stay pending before
// being escalated to manual review.
const pendingTxEscalationTimeout = 1 * time.Hour

// pendingPollInterval is how often the pending poller checks tracked transactions.
const pendingPollInterval = 30 * time.Second

// BlockchainWorker consumes blockchain intent messages from NATS and executes
// on-chain transactions. It uses the CHECK-BEFORE-CALL pattern with
// ProviderTransferStore to prevent double-sends.
type BlockchainWorker struct {
	partition         int
	blockchainClients map[string]domain.BlockchainClient
	transferStore     ProviderTransferStore
	engine            SettlementEngine
	publisher         *messaging.Publisher
	subscriber        *messaging.StreamSubscriber
	logger            *slog.Logger
	blockchainCBs     map[string]*resilience.CircuitBreaker
	pendingTxs        sync.Map // key: uuid.UUID (transferID), value: pendingEntry
	reviewStore       BlockchainReviewStore
	cancelPoller      context.CancelFunc
	pollerWg          sync.WaitGroup
}

// NewBlockchainWorker creates a blockchain worker that subscribes to the blockchain stream.
// reviewStore is optional — pass nil to disable manual review escalation for stuck txs.
func NewBlockchainWorker(
	partition int,
	blockchainClients map[string]domain.BlockchainClient,
	transferStore ProviderTransferStore,
	engine SettlementEngine,
	client *messaging.Client,
	logger *slog.Logger,
	reviewStore BlockchainReviewStore,
	opts ...messaging.SubscriberOption,
) *BlockchainWorker {
	blockchainCBs := make(map[string]*resilience.CircuitBreaker)
	for chain := range blockchainClients {
		blockchainCBs[chain] = resilience.NewCircuitBreaker(
			"blockchain-"+chain,
			resilience.WithFailureThreshold(3),
			resilience.WithResetTimeout(60*time.Second),
			resilience.WithHalfOpenMax(2),
		)
	}

	consumerName := messaging.StreamConsumerName("settla-blockchain-worker", partition)

	return &BlockchainWorker{
		partition:         partition,
		blockchainClients: blockchainClients,
		transferStore:     transferStore,
		engine:            engine,
		publisher:         messaging.NewPublisher(client),
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamBlockchain,
			consumerName,
			opts...,
		),
		logger:        logger.With("module", "blockchain-worker", "partition", partition),
		blockchainCBs: blockchainCBs,
		reviewStore:   reviewStore,
	}
}

// Start begins consuming blockchain intent messages. Blocks until ctx is cancelled.
func (w *BlockchainWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-blockchain-worker: starting", "partition", w.partition)
	pollerCtx, cancel := context.WithCancel(ctx)
	w.cancelPoller = cancel
	w.pollerWg.Add(1)
	go func() {
		defer w.pollerWg.Done()
		w.startPendingPoller(pollerCtx)
	}()
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixBlockchain, w.partition)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the pending poller, waits for it to finish, then stops the subscriber.
func (w *BlockchainWorker) Stop() {
	if w.cancelPoller != nil {
		w.cancelPoller()
	}
	w.pollerWg.Wait()
	w.subscriber.Stop()
}

// handleEvent routes blockchain intent events to the appropriate handler.
func (w *BlockchainWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentBlockchainSend:
		return w.handleSend(ctx, event)
	default:
		w.logger.Debug("settla-blockchain-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// executeBlockchain wraps the blockchain client call with a circuit breaker.
func (w *BlockchainWorker) executeBlockchain(ctx context.Context, chain string, client domain.BlockchainClient, req domain.TxRequest) (*domain.ChainTx, error) {
	cb, ok := w.blockchainCBs[chain]
	if !ok {
		return client.SendTransaction(ctx, req)
	}
	var result *domain.ChainTx
	err := cb.Execute(ctx, func(ctx context.Context) error {
		var execErr error
		result, execErr = client.SendTransaction(ctx, req)
		return execErr
	})
	return result, err
}

// handleSend implements the atomic CLAIM-CALL-UPDATE pattern for blockchain transactions.
func (w *BlockchainWorker) handleSend(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.BlockchainSendPayload](event)
	if err != nil {
		w.logger.Error("settla-blockchain-worker: failed to unmarshal send payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	budget := resilience.NewTimeoutBudget(ctx, 60*time.Second)

	w.logger.Info("settla-blockchain-worker: processing send intent",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"chain", payload.Chain,
		"amount", payload.Amount.String(),
	)

	claimCtx, claimCancel := budget.Allocate(5 * time.Second)
	claimID, err := w.transferStore.ClaimProviderTransaction(claimCtx, ClaimProviderTransactionParams{
		TenantID:   payload.TenantID,
		TransferID: payload.TransferID,
		TxType:     "blockchain",
		Provider:   payload.Chain,
	})
	claimCancel()
	if err != nil {
		return fmt.Errorf("settla-blockchain-worker: claim blockchain for %s: %w", payload.TransferID, err)
	}
	if claimID == nil {
		// Another worker already claimed — check if there's a pending tx to poll
		existing, err := w.transferStore.GetProviderTransaction(ctx, payload.TenantID, payload.TransferID, "blockchain")
		if err != nil {
			return fmt.Errorf("settla-blockchain-worker: checking tx for %s: %w", payload.TransferID, err)
		}
		if existing != nil && strings.ToLower(existing.Status) == "pending" {
			return w.checkPendingTx(ctx, payload, existing)
		}
		w.logger.Info("settla-blockchain-worker: blockchain already claimed, skipping",
			"transfer_id", payload.TransferID)
		return nil
	}

	client, ok := w.blockchainClients[payload.Chain]
	if !ok {
		w.logger.Error("settla-blockchain-worker: unknown chain",
			"chain", payload.Chain,
			"transfer_id", payload.TransferID,
		)
		failedTx := &domain.ProviderTx{Status: "failed"}
		_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "blockchain", failedTx)
		result := domain.IntentResult{
			Success:   false,
			Error:     fmt.Sprintf("unknown blockchain chain: %s", payload.Chain),
			ErrorCode: "CHAIN_NOT_FOUND",
		}
		if err := w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
			if errors.Is(err, core.ErrOptimisticLock) {
				w.logger.Warn("settla-blockchain-worker: optimistic lock on settlement result, NAKing for retry",
					"transfer_id", payload.TransferID)
			}
			return err
		}
		return nil
	}

	callCtx, callCancel := budget.Allocate(45 * time.Second)
	chainTx, sendErr := w.executeBlockchain(callCtx, payload.Chain, client, domain.TxRequest{
		From:   payload.From,
		To:     payload.To,
		Token:  payload.Token,
		Amount: payload.Amount,
		Memo:   payload.Memo,
	})
	callCancel()

	if sendErr != nil {
		// Check for circuit open — return immediately to NAK (don't report failure to engine)
		if errors.Is(sendErr, resilience.ErrCircuitOpen) {
			w.logger.Warn("settla-blockchain-worker: circuit breaker open, NAKing for retry",
				"chain", payload.Chain, "transfer_id", payload.TransferID)
			return sendErr // NAK
		}

		w.logger.Warn("settla-blockchain-worker: send failed",
			"transfer_id", payload.TransferID,
			"chain", payload.Chain,
			"error", sendErr,
		)
		failedTx := &domain.ProviderTx{Status: "failed"}
		_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "blockchain", failedTx)

		result := domain.IntentResult{
			Success:   false,
			Error:     sendErr.Error(),
			ErrorCode: "BLOCKCHAIN_SEND_FAILED",
		}
		if err := w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.TransferID, result); err != nil {
			if errors.Is(err, core.ErrOptimisticLock) {
				w.logger.Warn("settla-blockchain-worker: optimistic lock on settlement result, NAKing for retry",
					"transfer_id", payload.TransferID)
			}
			return err
		}
		return nil
	}

	providerTx := &domain.ProviderTx{
		ID:     chainTx.Hash,
		Status: chainTx.Status,
		TxHash: chainTx.Hash,
	}
	if err := w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "blockchain", providerTx); err != nil {
		w.logger.Warn("settla-blockchain-worker: failed to record tx, continuing",
			"transfer_id", payload.TransferID,
			"error", err,
		)
	}

	chainStatus := strings.ToLower(chainTx.Status)
	if chainStatus == "pending" {
		w.logger.Info("settla-blockchain-worker: tx pending, tracking for poll recovery",
			"transfer_id", payload.TransferID,
			"tx_hash", chainTx.Hash,
		)
		w.trackPending(payload, chainTx.Hash)
		return nil
	}

	reportCtx, reportCancel := budget.Allocate(10 * time.Second)

	if chainStatus == "confirmed" {
		w.logger.Info("settla-blockchain-worker: tx confirmed",
			"transfer_id", payload.TransferID,
			"tx_hash", chainTx.Hash,
			"confirmations", chainTx.Confirmations,
		)

		result := domain.IntentResult{
			Success: true,
			TxHash:  chainTx.Hash,
		}
		err = w.engine.HandleSettlementResult(reportCtx, payload.TenantID, payload.TransferID, result)
		reportCancel()
		if err != nil {
			if errors.Is(err, core.ErrOptimisticLock) {
				w.logger.Warn("settla-blockchain-worker: optimistic lock on settlement result, NAKing for retry",
					"transfer_id", payload.TransferID)
			}
			return err
		}
		return nil
	}

	// If status is "failed" from the chain
	result := domain.IntentResult{
		Success:   false,
		TxHash:    chainTx.Hash,
		Error:     fmt.Sprintf("blockchain tx status: %s", chainTx.Status),
		ErrorCode: "BLOCKCHAIN_TX_FAILED",
	}
	err = w.engine.HandleSettlementResult(reportCtx, payload.TenantID, payload.TransferID, result)
	reportCancel()
	if err != nil {
		if errors.Is(err, core.ErrOptimisticLock) {
			w.logger.Warn("settla-blockchain-worker: optimistic lock on settlement result, NAKing for retry",
				"transfer_id", payload.TransferID)
		}
		return err
	}
	return nil
}

// checkPendingTx queries the chain for the current status of a pending transaction.
func (w *BlockchainWorker) checkPendingTx(ctx context.Context, payload *domain.BlockchainSendPayload, existing *domain.ProviderTx) error {
	client, ok := w.blockchainClients[payload.Chain]
	if !ok {
		return fmt.Errorf("settla-blockchain-worker: unknown chain %s for pending tx check", payload.Chain)
	}

	chainTx, err := client.GetTransaction(ctx, existing.TxHash)
	if err != nil {
		w.logger.Warn("settla-blockchain-worker: failed to check pending tx status",
			"transfer_id", payload.TransferID,
			"tx_hash", existing.TxHash,
			"error", err,
		)
		// Don't fail — the tx might still be pending. ACK and let redelivery check again.
		return nil
	}

	if strings.EqualFold(chainTx.Status, "confirmed") {
		w.logger.Info("settla-blockchain-worker: pending tx now confirmed",
			"transfer_id", payload.TransferID,
			"tx_hash", chainTx.Hash,
			"confirmations", chainTx.Confirmations,
		)

		existing.Status = "confirmed"
		_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "blockchain", existing)
		w.pendingTxs.Delete(payload.TransferID)
		pendingTxRecovered.Inc()

		result := domain.IntentResult{
			Success: true,
			TxHash:  chainTx.Hash,
		}
		return w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.TransferID, result)
	}

	if strings.EqualFold(chainTx.Status, "failed") {
		w.logger.Warn("settla-blockchain-worker: pending tx failed on chain",
			"transfer_id", payload.TransferID,
			"tx_hash", chainTx.Hash,
		)

		existing.Status = "failed"
		_ = w.transferStore.UpdateProviderTransaction(ctx, payload.TransferID, "blockchain", existing)
		w.pendingTxs.Delete(payload.TransferID)
		pendingTxRecovered.Inc()

		result := domain.IntentResult{
			Success:   false,
			TxHash:    chainTx.Hash,
			Error:     "blockchain transaction failed",
			ErrorCode: "BLOCKCHAIN_TX_FAILED",
		}
		return w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.TransferID, result)
	}

	// Still pending — track for poll recovery
	w.trackPending(payload, existing.TxHash)
	w.logger.Debug("settla-blockchain-worker: tx still pending",
		"transfer_id", payload.TransferID,
		"tx_hash", existing.TxHash,
	)
	return nil
}

// trackPending adds a transfer to the pending map if not already tracked.
func (w *BlockchainWorker) trackPending(payload *domain.BlockchainSendPayload, txHash string) {
	if _, loaded := w.pendingTxs.LoadOrStore(payload.TransferID, pendingEntry{
		payload:  payload,
		txHash:   txHash,
		tenantID: payload.TenantID,
		addedAt:  time.Now().UTC(),
	}); !loaded {
		pendingTxCount.Inc()
	}
}

// startPendingPoller runs a 30s ticker that checks all tracked pending transactions.
// Transactions confirmed or failed are reported to the engine. Transactions pending
// longer than 1 hour are escalated to manual review.
func (w *BlockchainWorker) startPendingPoller(ctx context.Context) {
	ticker := time.NewTicker(pendingPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollPendingTransactions(ctx)
		}
	}
}

// maxPendingChecksPerPoll caps how many pending transactions are checked per
// poll cycle to avoid blocking on RPC calls when the pending set is large.
const maxPendingChecksPerPoll = 100

// pollPendingTransactions iterates tracked pending transactions and checks their status.
// Escalations are processed first (cheap, no RPC). RPC checks are capped at
// maxPendingChecksPerPoll per cycle to bound latency.
func (w *BlockchainWorker) pollPendingTransactions(ctx context.Context) {
	checked := 0
	w.pendingTxs.Range(func(key, value any) bool {
		transferID := key.(uuid.UUID)
		entry := value.(pendingEntry)

		// Check if stuck long enough for escalation — use LoadAndDelete to
		// prevent double-escalation if two poll cycles overlap.
		if time.Since(entry.addedAt) > pendingTxEscalationTimeout {
			if _, deleted := w.pendingTxs.LoadAndDelete(key); deleted {
				w.escalatePendingTx(ctx, transferID, entry)
			}
			return true
		}

		// Cap RPC checks per cycle to avoid slow iterations at scale.
		if checked >= maxPendingChecksPerPoll {
			return false
		}
		checked++

		// Re-check on chain via the existing checkPendingTx path
		existing := &domain.ProviderTx{
			TxHash: entry.txHash,
			Status: "pending",
		}
		if err := w.checkPendingTx(ctx, entry.payload, existing); err != nil {
			w.logger.Warn("settla-blockchain-worker: pending poller check failed",
				"transfer_id", transferID,
				"error", err,
			)
		}
		return true
	})
}

// escalatePendingTx creates a manual review for a pending transaction that has
// exceeded the escalation timeout. If no reviewStore is configured, it only logs.
func (w *BlockchainWorker) escalatePendingTx(ctx context.Context, transferID uuid.UUID, entry pendingEntry) {
	if w.reviewStore == nil {
		w.logger.Warn("settla-blockchain-worker: pending tx exceeded timeout but no review store configured",
			"transfer_id", transferID,
			"tx_hash", entry.txHash,
			"pending_since", entry.addedAt,
		)
		return
	}

	hasReview, err := w.reviewStore.HasActiveReview(ctx, transferID)
	if err != nil {
		w.logger.Error("settla-blockchain-worker: failed to check active review",
			"transfer_id", transferID,
			"error", err,
		)
		return
	}
	if hasReview {
		return // already escalated
	}

	if err := w.reviewStore.CreateManualReview(ctx, transferID, entry.tenantID, "BLOCKCHAIN_PENDING", entry.addedAt); err != nil {
		w.logger.Error("settla-blockchain-worker: failed to create manual review",
			"transfer_id", transferID,
			"error", err,
		)
		return
	}

	// pendingTxs entry already removed by caller (LoadAndDelete in pollPendingTransactions).
	pendingTxCount.Dec()
	pendingTxEscalated.Inc()

	w.logger.Warn("settla-blockchain-worker: pending tx escalated to manual review",
		"transfer_id", transferID,
		"tenant_id", entry.tenantID,
		"tx_hash", entry.txHash,
		"pending_since", entry.addedAt,
	)
}

