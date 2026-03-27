package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	bankdeposit "github.com/intellect4all/settla/core/bankdeposit"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
)

// BankDepositEngine abstracts the bank deposit session engine for the worker.
type BankDepositEngine interface {
	HandleBankCreditReceived(ctx context.Context, tenantID, sessionID uuid.UUID, credit domain.IncomingBankCredit) error
	HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error
	HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error
	CreateSessionForPermanentAccount(ctx context.Context, tenantID uuid.UUID, accountNumber, bankingPartnerID string, credit domain.IncomingBankCredit) (*domain.BankDepositSession, error)
}

// BankDepositAccountRecycler abstracts virtual account recycling.
type BankDepositAccountRecycler interface {
	RecycleVirtualAccount(ctx context.Context, accountNumber string) error
}

// BankDepositInboundStore provides account index lookups for routing inbound bank credits.
type BankDepositInboundStore interface {
	GetVirtualAccountIndexByNumber(ctx context.Context, accountNumber string) (*bankdeposit.VirtualAccountIndex, error)
	GetSessionByAccountNumber(ctx context.Context, accountNumber string) (*domain.BankDepositSession, error)
}

// BankingPartnerRegistry provides access to banking partner details.
type BankingPartnerRegistry interface {
	Get(id string) (domain.BankingPartner, error)
}

// Compile-time interface check that bankdeposit.Engine satisfies BankDepositEngine.
var _ BankDepositEngine = (*bankdeposit.Engine)(nil)

// BankDepositWorker consumes bank deposit events from the SETTLA_BANK_DEPOSITS stream
// and routes them to the bank deposit engine.
type BankDepositWorker struct {
	partition    int
	engine       BankDepositEngine
	treasury     domain.TreasuryManager
	recycler     BankDepositAccountRecycler
	inboundStore BankDepositInboundStore
	partnerReg   BankingPartnerRegistry
	subscriber   *messaging.StreamSubscriber
	inboundSub   *messaging.StreamSubscriber
	logger       *slog.Logger
	metrics      *observability.Metrics
}

// NewBankDepositWorker creates a bank deposit worker for a given partition.
func NewBankDepositWorker(
	partition int,
	engine BankDepositEngine,
	treasury domain.TreasuryManager,
	recycler BankDepositAccountRecycler,
	inboundStore BankDepositInboundStore,
	partnerReg BankingPartnerRegistry,
	client *messaging.Client,
	logger *slog.Logger,
	metrics *observability.Metrics,
	opts ...messaging.SubscriberOption,
) *BankDepositWorker {
	consumerName := messaging.StreamConsumerName("settla-bank-deposit-worker", partition)
	return &BankDepositWorker{
		partition:    partition,
		engine:       engine,
		treasury:     treasury,
		recycler:     recycler,
		inboundStore: inboundStore,
		partnerReg:   partnerReg,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamBankDeposits,
			consumerName,
			opts...,
		),
		inboundSub: messaging.NewStreamSubscriber(
			client,
			messaging.StreamBankDeposits,
			"settla-bank-deposit-inbound",
			opts...,
		),
		logger:  logger.With("module", "bank-deposit-worker", "partition", partition),
		metrics: metrics,
	}
}

// Start begins consuming bank deposit events. Blocks until ctx is cancelled.
func (w *BankDepositWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-bank-deposit-worker: starting", "partition", w.partition)

	// Start inbound bank credit consumer (non-partitioned)
	go func() {
		if err := w.inboundSub.SubscribeStream(ctx, "settla.inbound.bank.>", w.handleInboundBankCredit); err != nil {
			w.logger.Error("settla-bank-deposit-worker: inbound consumer failed", "error", err)
		}
	}()

	// Start partitioned consumer
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixBankDeposit, w.partition)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop gracefully stops the subscriber.
func (w *BankDepositWorker) Stop() {
	w.subscriber.Stop()
	w.inboundSub.Stop()
}

// handleEvent routes bank deposit events to the appropriate engine method.
func (w *BankDepositWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.EventBankDepositPaymentReceived:
		return w.handlePaymentReceived(ctx, event)
	case domain.IntentBankDepositCredit:
		return w.handleCreditResult(ctx, event)
	case domain.IntentBankDepositSettle:
		return w.handleSettlementResult(ctx, event)
	case domain.IntentRecycleVirtualAccount:
		return w.handleRecycleAccount(ctx, event)
	case domain.IntentBankDepositRefund:
		return w.handleRefund(ctx, event)
	default:
		w.logger.Debug("settla-bank-deposit-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil // ACK
	}
}

func (w *BankDepositWorker) handlePaymentReceived(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.BankDepositSessionEventPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal payment.received payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-bank-deposit-worker: handling payment.received",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
		"amount", payload.Amount,
		"currency", payload.Currency,
	)

	incoming := domain.IncomingBankCredit{
		Amount:   payload.Amount,
		Currency: domain.Currency(payload.Currency),
	}

	if err := w.engine.HandleBankCreditReceived(ctx, payload.TenantID, payload.SessionID, incoming); err != nil {
		return err
	}
	if w.metrics != nil {
		w.metrics.BankDepositSessionsTotal.WithLabelValues(payload.TenantID.String(), "PAYMENT_RECEIVED").Inc()
	}
	return nil
}

func (w *BankDepositWorker) handleCreditResult(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.CreditBankDepositPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal credit result payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-bank-deposit-worker: handling credit",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
		"currency", payload.Currency,
		"net_amount", payload.NetAmount.String(),
	)

	// Derive treasury position location from currency.
	location := fmt.Sprintf("bank:%s", strings.ToLower(string(payload.Currency)))

	// Credit the tenant's treasury position with the net amount (gross minus fees).
	err = w.treasury.CreditBalance(
		ctx,
		payload.TenantID,
		payload.Currency,
		location,
		payload.NetAmount,
		payload.SessionID,
		"bank_deposit",
	)

	var result domain.IntentResult
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: treasury credit failed",
			"session_id", payload.SessionID,
			"tenant_id", payload.TenantID,
			"error", err,
		)
		result = domain.IntentResult{
			Success: false,
			Error:   fmt.Sprintf("treasury credit failed: %v", err),
		}
	} else {
		w.logger.Info("settla-bank-deposit-worker: treasury credit succeeded",
			"session_id", payload.SessionID,
			"tenant_id", payload.TenantID,
			"amount", payload.NetAmount.String(),
			"location", location,
		)
		result = domain.IntentResult{
			Success: true,
		}
	}

	if err := w.engine.HandleCreditResult(ctx, payload.TenantID, payload.SessionID, result); err != nil {
		return err
	}
	if w.metrics != nil {
		w.metrics.BankDepositSessionsTotal.WithLabelValues(payload.TenantID.String(), "CREDITED").Inc()
	}
	return nil
}

func (w *BankDepositWorker) handleSettlementResult(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.SettleBankDepositPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal settlement result payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-bank-deposit-worker: handling settlement result",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
	)

	result := domain.IntentResult{
		Success: true,
	}

	if err := w.engine.HandleSettlementResult(ctx, payload.TenantID, payload.SessionID, result); err != nil {
		return err
	}
	if w.metrics != nil {
		w.metrics.BankDepositSessionsTotal.WithLabelValues(payload.TenantID.String(), "SETTLED").Inc()
	}
	return nil
}

func (w *BankDepositWorker) handleRecycleAccount(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.RecycleVirtualAccountPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal recycle account payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-bank-deposit-worker: recycling virtual account",
		"account_number", payload.AccountNumber,
		"banking_partner", payload.BankingPartnerID,
	)

	return w.recycler.RecycleVirtualAccount(ctx, payload.AccountNumber)
}

// handleInboundBankCredit processes raw bank credit webhooks from the gateway.
// It routes credits to the correct session based on virtual account index.
func (w *BankDepositWorker) handleInboundBankCredit(ctx context.Context, event domain.Event) error {
	// The gateway publishes a JSON payload with account_number, amount, etc.
	type inboundPayload struct {
		PartnerID          string `json:"partnerId"`
		AccountNumber      string `json:"accountNumber"`
		Amount             string `json:"amount"`
		Currency           string `json:"currency"`
		PayerName          string `json:"payerName"`
		PayerAccountNumber string `json:"payerAccountNumber"`
		PayerReference     string `json:"payerReference"`
		BankReference      string `json:"bankReference"`
		ReceivedAt         string `json:"receivedAt"`
	}

	payload, err := unmarshalEventData[inboundPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal inbound bank credit",
			"event_id", event.ID, "error", err)
		return nil // ACK malformed
	}

	if payload.AccountNumber == "" || payload.BankReference == "" {
		w.logger.Error("settla-bank-deposit-worker: inbound bank credit missing required fields",
			"event_id", event.ID)
		return nil // ACK malformed
	}

	// Look up account index
	index, err := w.inboundStore.GetVirtualAccountIndexByNumber(ctx, payload.AccountNumber)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: account not found in index",
			"account_number", payload.AccountNumber, "error", err)
		return nil // ACK — unknown account
	}

	amount, err := decimal.NewFromString(payload.Amount)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: invalid amount in inbound credit",
			"amount", payload.Amount, "error", err)
		return nil // ACK malformed
	}

	receivedAt := time.Now().UTC()
	if payload.ReceivedAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.ReceivedAt); err == nil {
			receivedAt = t
		}
	}

	credit := domain.IncomingBankCredit{
		AccountNumber:      payload.AccountNumber,
		Amount:             amount,
		Currency:           domain.Currency(payload.Currency),
		PayerName:          payload.PayerName,
		PayerAccountNumber: payload.PayerAccountNumber,
		PayerReference:     payload.PayerReference,
		BankReference:      payload.BankReference,
		ReceivedAt:         receivedAt,
	}

	var session *domain.BankDepositSession

	if index.AccountType == domain.VirtualAccountTypePermanent {
		// PERMANENT account: auto-create session via engine
		session, err = w.engine.CreateSessionForPermanentAccount(ctx, index.TenantID, payload.AccountNumber, payload.PartnerID, credit)
		if err != nil {
			return fmt.Errorf("settla-bank-deposit-worker: create session for permanent account: %w", err)
		}
	} else {
		// TEMPORARY account: find existing PENDING_PAYMENT session
		session, err = w.inboundStore.GetSessionByAccountNumber(ctx, payload.AccountNumber)
		if err != nil {
			w.logger.Error("settla-bank-deposit-worker: no pending session for temporary account",
				"account_number", payload.AccountNumber, "error", err)
			return nil // ACK — no session
		}
	}

	w.logger.Info("settla-bank-deposit-worker: routing inbound bank credit",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"account_number", payload.AccountNumber,
		"amount", payload.Amount,
		"account_type", index.AccountType,
	)

	if err := w.engine.HandleBankCreditReceived(ctx, session.TenantID, session.ID, credit); err != nil {
		return err
	}
	if w.metrics != nil {
		w.metrics.BankDepositSessionsTotal.WithLabelValues(session.TenantID.String(), "INBOUND_CREDIT").Inc()
	}
	return nil
}

func (w *BankDepositWorker) handleRefund(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.RefundBankDepositPayload](event)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: failed to unmarshal refund payload",
			"event_id", event.ID, "error", err)
		return nil // ACK malformed
	}

	w.logger.Info("settla-bank-deposit-worker: handling refund",
		"session_id", payload.SessionID,
		"tenant_id", payload.TenantID,
		"amount", payload.Amount,
	)

	if w.partnerReg == nil {
		w.logger.Warn("settla-bank-deposit-worker: no banking partner registry, skipping refund",
			"session_id", payload.SessionID)
		return nil // ACK — can't refund without partner
	}

	// Look up the session to get the banking partner ID
	// We use the account number from the payload to find the right partner
	partner, err := w.partnerReg.Get(payload.AccountNumber)
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: banking partner not found for refund",
			"session_id", payload.SessionID, "error", err)
		return nil // ACK — can't find partner
	}

	result, err := partner.RefundPayment(ctx, domain.RefundPaymentRequest{
		SessionID:     payload.SessionID,
		TenantID:      payload.TenantID,
		AccountNumber: payload.AccountNumber,
		Amount:        payload.Amount,
		Currency:      payload.Currency,
		Reason:        payload.Reason,
	})
	if err != nil {
		w.logger.Error("settla-bank-deposit-worker: refund failed",
			"session_id", payload.SessionID, "error", err)
		return fmt.Errorf("settla-bank-deposit-worker: refund payment: %w", err)
	}

	w.logger.Info("settla-bank-deposit-worker: refund completed",
		"session_id", payload.SessionID,
		"refund_ref", result.RefundReference,
		"success", result.Success,
	)

	return nil
}
