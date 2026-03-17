package bankdeposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// Engine is the bank deposit session orchestrator. It coordinates the bank deposit lifecycle
// as a pure state machine: every method validates state, computes the next state
// plus outbox entries, and persists both atomically. The engine makes ZERO network
// calls — all side effects (ledger credits, settlement, refunds, account recycling) are
// expressed as outbox intents for workers to execute.
type Engine struct {
	store       BankDepositStore
	tenantStore TenantStore
	logger      *slog.Logger
}

// NewEngine creates a bank deposit engine wired to the given dependencies.
func NewEngine(store BankDepositStore, tenantStore TenantStore, logger *slog.Logger) *Engine {
	return &Engine{
		store:       store,
		tenantStore: tenantStore,
		logger:      logger.With("module", "core.bankdeposit"),
	}
}

// CreateSessionRequest is the input for creating a new bank deposit session.
type CreateSessionRequest struct {
	IdempotencyKey   string
	Currency         string
	BankingPartnerID string
	AccountType      domain.VirtualAccountType
	ExpectedAmount   decimal.Decimal
	MinAmount        decimal.Decimal
	MaxAmount        decimal.Decimal
	MismatchPolicy   domain.PaymentMismatchPolicy
	SettlementPref   domain.SettlementPreference
	TTLSeconds       int32
	Metadata         map[string]string
}

// CreateSession validates a bank deposit request, dispenses a virtual account from the pool,
// and persists the session with outbox entries for session creation notification.
func (e *Engine) CreateSession(ctx context.Context, tenantID uuid.UUID, req CreateSessionRequest) (*domain.BankDepositSession, error) {
	// a. Load tenant, verify active + bank deposits enabled
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create session: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}
	if !tenant.BankConfig.BankDepositsEnabled {
		return nil, domain.ErrBankDepositsDisabled(tenantID.String())
	}

	// b. Validate currency is supported
	currency := strings.ToUpper(req.Currency)
	currencySupported := false
	for _, c := range tenant.BankConfig.BankSupportedCurrencies {
		if strings.ToUpper(c) == currency {
			currencySupported = true
			break
		}
	}
	if !currencySupported {
		return nil, domain.ErrCurrencyNotSupported(currency, tenantID.String())
	}

	// c. Validate amount > 0
	if !req.ExpectedAmount.IsPositive() {
		return nil, domain.ErrAmountTooLow(req.ExpectedAmount.String(), "0")
	}

	// d. Check idempotency
	if req.IdempotencyKey != "" {
		existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	// e. Dispense virtual account from pool (TEMPORARY accounts only)
	poolAccount, err := e.store.DispenseVirtualAccount(ctx, tenantID, currency)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create session: dispensing virtual account: %w", err)
	}
	if poolAccount == nil {
		return nil, domain.ErrVirtualAccountPoolEmpty(currency, tenantID.String())
	}

	// f. Determine settlement preference (use request or tenant default — fall back to HOLD)
	settlementPref := req.SettlementPref
	if settlementPref == "" {
		settlementPref = domain.SettlementPreferenceHold
	}

	// g. Determine mismatch policy
	mismatchPolicy := req.MismatchPolicy
	if mismatchPolicy == "" {
		mismatchPolicy = tenant.BankConfig.DefaultMismatchPolicy
	}
	if mismatchPolicy == "" {
		mismatchPolicy = domain.PaymentMismatchPolicyAccept
	}

	// h. Determine TTL
	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = tenant.BankConfig.DefaultSessionTTLSecs
	}
	if ttl <= 0 {
		ttl = 3600 // 1 hour default
	}

	// i. Determine min/max amounts
	minAmount := req.MinAmount
	maxAmount := req.MaxAmount
	if minAmount.IsZero() {
		minAmount = req.ExpectedAmount
	}
	if maxAmount.IsZero() {
		maxAmount = req.ExpectedAmount
	}

	// j. Build session
	now := time.Now().UTC()
	session := &domain.BankDepositSession{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		IdempotencyKey:   req.IdempotencyKey,
		Status:           domain.BankDepositSessionStatusPendingPayment,
		Version:          1,
		BankingPartnerID: poolAccount.BankingPartnerID,
		AccountNumber:    poolAccount.AccountNumber,
		AccountName:      poolAccount.AccountName,
		SortCode:         poolAccount.SortCode,
		IBAN:             poolAccount.IBAN,
		AccountType:      domain.VirtualAccountTypeTemporary,
		Currency:         domain.Currency(currency),
		ExpectedAmount:   req.ExpectedAmount,
		MinAmount:        minAmount,
		MaxAmount:        maxAmount,
		ReceivedAmount:   decimal.Zero,
		FeeAmount:        decimal.Zero,
		NetAmount:        decimal.Zero,
		MismatchPolicy:   mismatchPolicy,
		CollectionFeeBPS: tenant.FeeSchedule.BankCollectionBPS,
		SettlementPref:   settlementPref,
		ExpiresAt:        now.Add(time.Duration(ttl) * time.Second),
		CreatedAt:        now,
		UpdatedAt:        now,
		Metadata:         req.Metadata,
	}

	// k. Build outbox entries: session created event
	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusPendingPayment,
		Currency:  currency,
		Amount:    req.ExpectedAmount,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create session: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", session.ID, tenantID, domain.EventBankDepositSessionCreated, eventPayload),
	}

	// Webhook + email notification for session created
	notifEntries, err := bankDepositNotificationEntries(session.ID, tenantID,
		domain.EventBankDepositSessionCreated,
		fmt.Sprintf("Bank deposit session created — awaiting %s %s", req.ExpectedAmount.String(), currency),
		eventPayload,
	)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create session: %w", err)
	}
	entries = append(entries, notifEntries...)

	// l. Persist session + outbox atomically
	if err := e.store.CreateSessionWithOutbox(ctx, session, entries); err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create session: persisting: %w", err)
	}

	e.logger.Info("settla-bank-deposit: session created",
		"session_id", session.ID,
		"tenant_id", tenantID,
		"currency", currency,
		"account_number", session.AccountNumber,
		"expected_amount", req.ExpectedAmount,
	)

	return session, nil
}

// CreateSessionForPermanentAccount creates a session for a permanent virtual account when
// an inbound credit arrives. Unlike CreateSession, it doesn't dispense from pool —
// the permanent account already exists.
func (e *Engine) CreateSessionForPermanentAccount(ctx context.Context, tenantID uuid.UUID, accountNumber, bankingPartnerID string, credit domain.IncomingBankCredit) (*domain.BankDepositSession, error) {
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create permanent session: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}
	if !tenant.BankConfig.BankDepositsEnabled {
		return nil, domain.ErrBankDepositsDisabled(tenantID.String())
	}

	idempotencyKey := fmt.Sprintf("permanent:%s:%s", accountNumber, credit.BankReference)

	// Check idempotency
	existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, idempotencyKey)
	if err == nil && existing != nil {
		return existing, nil
	}

	mismatchPolicy := tenant.BankConfig.DefaultMismatchPolicy
	if mismatchPolicy == "" {
		mismatchPolicy = domain.PaymentMismatchPolicyAccept
	}

	ttl := tenant.BankConfig.DefaultSessionTTLSecs
	if ttl <= 0 {
		ttl = 3600
	}

	now := time.Now().UTC()
	session := &domain.BankDepositSession{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		IdempotencyKey:   idempotencyKey,
		Status:           domain.BankDepositSessionStatusPendingPayment,
		Version:          1,
		BankingPartnerID: bankingPartnerID,
		AccountNumber:    accountNumber,
		AccountType:      domain.VirtualAccountTypePermanent,
		Currency:         credit.Currency,
		ExpectedAmount:   credit.Amount,
		MinAmount:        credit.Amount,
		MaxAmount:        credit.Amount,
		ReceivedAmount:   decimal.Zero,
		FeeAmount:        decimal.Zero,
		NetAmount:        decimal.Zero,
		MismatchPolicy:   mismatchPolicy,
		CollectionFeeBPS: tenant.FeeSchedule.BankCollectionBPS,
		SettlementPref:   domain.SettlementPreferenceAutoConvert,
		ExpiresAt:        now.Add(time.Duration(ttl) * time.Second),
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusPendingPayment,
		Currency:  string(credit.Currency),
		Amount:    credit.Amount,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create permanent session: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", session.ID, tenantID, domain.EventBankDepositSessionCreated, eventPayload),
	}

	if err := e.store.CreateSessionWithOutbox(ctx, session, entries); err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: create permanent session: persisting: %w", err)
	}

	e.logger.Info("settla-bank-deposit: permanent account session created",
		"session_id", session.ID,
		"tenant_id", tenantID,
		"account_number", accountNumber,
		"amount", credit.Amount,
	)

	return session, nil
}

// ListVirtualAccounts returns all virtual accounts for a tenant.
func (e *Engine) ListVirtualAccounts(ctx context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error) {
	accounts, err := e.store.ListVirtualAccountsByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: list virtual accounts for tenant %s: %w", tenantID, err)
	}
	return accounts, nil
}

// HandleBankCreditReceived processes an incoming bank credit notification.
// Transitions PENDING_PAYMENT -> PAYMENT_RECEIVED (or handles late payments from EXPIRED/CANCELLED).
// Then validates amount against min/max + mismatch policy, and if accepted,
// immediately initiates crediting (PAYMENT_RECEIVED -> CREDITING).
func (e *Engine) HandleBankCreditReceived(ctx context.Context, tenantID, sessionID uuid.UUID, credit domain.IncomingBankCredit) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: loading session %s: %w", sessionID, err)
	}

	// Record the transaction (dedup by bank_reference)
	existing, err := e.store.GetBankDepositTxByRef(ctx, credit.BankReference)
	if err == nil && existing != nil {
		e.logger.Info("settla-bank-deposit: duplicate bank credit, skipping",
			"session_id", sessionID,
			"bank_reference", credit.BankReference,
		)
		return nil
	}

	depositTx := &domain.BankDepositTransaction{
		ID:                 uuid.Must(uuid.NewV7()),
		SessionID:          sessionID,
		TenantID:           tenantID,
		BankReference:      credit.BankReference,
		PayerName:          credit.PayerName,
		PayerAccountNumber: credit.PayerAccountNumber,
		Amount:             credit.Amount,
		Currency:           credit.Currency,
		ReceivedAt:         credit.ReceivedAt,
		CreatedAt:          time.Now().UTC(),
	}

	if err := e.store.CreateBankDepositTx(ctx, depositTx); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: recording tx: %w", err)
	}

	// Accumulate received amount
	if err := e.store.AccumulateReceived(ctx, tenantID, sessionID, credit.Amount); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: accumulating received: %w", err)
	}

	// Check for late payment (session expired or cancelled)
	isLatePayment := session.Status == domain.BankDepositSessionStatusExpired ||
		session.Status == domain.BankDepositSessionStatusCancelled

	// Check if session can transition to PAYMENT_RECEIVED
	if !session.CanTransitionTo(domain.BankDepositSessionStatusPaymentReceived) {
		// If already PAYMENT_RECEIVED or further along, just record the tx
		e.logger.Info("settla-bank-deposit: bank credit recorded, session already past PAYMENT_RECEIVED",
			"session_id", sessionID,
			"status", session.Status,
			"bank_reference", credit.BankReference,
		)
		return nil
	}

	// Update session with payer details
	session.ReceivedAmount = session.ReceivedAmount.Add(credit.Amount)
	session.PayerName = credit.PayerName
	session.PayerReference = credit.PayerReference
	session.BankReference = credit.BankReference
	now := time.Now().UTC()
	session.PaymentReceivedAt = &now

	if err := session.TransitionTo(domain.BankDepositSessionStatusPaymentReceived); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: %w", err)
	}

	// Build outbox entries for PAYMENT_RECEIVED
	paymentPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusPaymentReceived,
		Currency:  string(session.Currency),
		Amount:    credit.Amount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositPaymentReceived, paymentPayload),
	}

	// Webhook + email notification for payment received
	notifEntries, err := bankDepositNotificationEntries(sessionID, tenantID,
		domain.EventBankDepositPaymentReceived,
		fmt.Sprintf("Bank payment received — %s %s", credit.Amount.String(), session.Currency),
		paymentPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle bank credit: %w", err)
	}
	entries = append(entries, notifEntries...)

	if isLatePayment {
		latePayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
			SessionID: sessionID,
			TenantID:  tenantID,
			Status:    domain.BankDepositSessionStatusPaymentReceived,
			Currency:  string(session.Currency),
			Amount:    credit.Amount,
			Reason:    "late_payment",
		})
		if err != nil {
			return fmt.Errorf("settla-bank-deposit: handle bank credit: marshalling late payment payload: %w", err)
		}
		entries = append(entries, domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositLatePayment, latePayload))
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle bank credit", sessionID)
	}

	e.logger.Info("settla-bank-deposit: payment received",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"amount", credit.Amount,
		"bank_reference", credit.BankReference,
		"late_payment", isLatePayment,
	)

	// Now validate amount and route accordingly
	return e.validateAndRoutePayment(ctx, tenantID, sessionID)
}

// validateAndRoutePayment checks received amount against min/max bounds and mismatch policy.
// If amount is acceptable, initiates crediting. If REJECT policy and mismatch, fails the session.
func (e *Engine) validateAndRoutePayment(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: validate payment: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusPaymentReceived {
		return fmt.Errorf("settla-bank-deposit: validate payment %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), "amount_validation"))
	}

	received := session.ReceivedAmount

	// Check underpayment
	if received.LessThan(session.MinAmount) {
		if session.MismatchPolicy == domain.PaymentMismatchPolicyReject {
			return e.handleMismatch(ctx, session, domain.BankDepositSessionStatusUnderpaid, "underpaid")
		}
		// ACCEPT policy: proceed with the received amount
	}

	// Check overpayment
	if received.GreaterThan(session.MaxAmount) {
		if session.MismatchPolicy == domain.PaymentMismatchPolicyReject {
			return e.handleMismatch(ctx, session, domain.BankDepositSessionStatusOverpaid, "overpaid")
		}
		// ACCEPT policy: proceed with the received amount
	}

	// Amount is acceptable — initiate credit
	return e.initiateCredit(ctx, tenantID, sessionID)
}

// handleMismatch transitions to UNDERPAID/OVERPAID then FAILED.
func (e *Engine) handleMismatch(ctx context.Context, session *domain.BankDepositSession, mismatchStatus domain.BankDepositSessionStatus, reason string) error {
	// Transition to mismatch status
	if err := session.TransitionTo(mismatchStatus); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle mismatch: %w", err)
	}

	var eventType string
	if mismatchStatus == domain.BankDepositSessionStatusUnderpaid {
		eventType = domain.EventBankDepositUnderpaid
	} else {
		eventType = domain.EventBankDepositOverpaid
	}

	mismatchPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    mismatchStatus,
		Currency:  string(session.Currency),
		Amount:    session.ReceivedAmount,
		Reason:    reason,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle mismatch: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", session.ID, session.TenantID, eventType, mismatchPayload),
	}

	// Webhook + email notification for mismatch
	notifEntries, err := bankDepositNotificationEntries(session.ID, session.TenantID,
		eventType,
		fmt.Sprintf("Bank deposit %s — received %s, expected %s %s",
			reason, session.ReceivedAmount.String(), session.ExpectedAmount.String(), session.Currency),
		mismatchPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle mismatch: %w", err)
	}
	entries = append(entries, notifEntries...)

	// Emit refund intent so the worker can initiate the bank refund
	refundPayload, err := json.Marshal(domain.RefundBankDepositPayload{
		SessionID:     session.ID,
		TenantID:      session.TenantID,
		AccountNumber: session.AccountNumber,
		Amount:        session.ReceivedAmount,
		Currency:      session.Currency,
		Reason:        reason,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle mismatch: marshalling refund payload: %w", err)
	}
	entries = append(entries, domain.MustNewOutboxIntent("bank_deposit", session.ID, session.TenantID, domain.IntentBankDepositRefund, refundPayload))

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle mismatch", session.ID)
	}

	e.logger.Warn("settla-bank-deposit: payment mismatch",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"reason", reason,
		"received", session.ReceivedAmount,
		"expected", session.ExpectedAmount,
	)

	// Now fail the session
	return e.failSession(ctx, session, fmt.Sprintf("payment %s — received %s, expected %s", reason, session.ReceivedAmount.String(), session.ExpectedAmount.String()), "PAYMENT_MISMATCH")
}

// initiateCredit transitions PAYMENT_RECEIVED -> CREDITING and emits IntentBankDepositCredit.
func (e *Engine) initiateCredit(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate credit: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusPaymentReceived {
		return fmt.Errorf("settla-bank-deposit: initiate credit %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusCrediting)))
	}

	// Load tenant for fee calculation
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate credit: loading tenant: %w", err)
	}

	// Calculate collection fee
	feeAmount := CalculateBankCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
	netAmount := session.ReceivedAmount.Sub(feeAmount)

	if err := session.TransitionTo(domain.BankDepositSessionStatusCrediting); err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate credit: %w", err)
	}

	// Build credit intent
	creditPayload, err := json.Marshal(domain.CreditBankDepositPayload{
		SessionID:      sessionID,
		TenantID:       tenantID,
		Currency:       session.Currency,
		GrossAmount:    session.ReceivedAmount,
		FeeAmount:      feeAmount,
		NetAmount:      netAmount,
		BankReference:  session.BankReference,
		IdempotencyKey: fmt.Sprintf("bank-deposit-credit:%s", sessionID),
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate credit: marshalling payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusCrediting,
		Currency:  string(session.Currency),
		Amount:    netAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate credit: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID, domain.IntentBankDepositCredit, creditPayload),
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositSessionCrediting, eventPayload),
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "initiate credit", sessionID)
	}

	e.logger.Info("settla-bank-deposit: credit initiated",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"gross", session.ReceivedAmount,
		"fee", feeAmount,
		"net", netAmount,
	)

	return nil
}

// HandleCreditResult processes the result of ledger + treasury crediting.
// On success: transitions CREDITING -> CREDITED, then routes based on settlement preference.
// On failure: transitions CREDITING -> FAILED.
func (e *Engine) HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle credit result: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusCrediting {
		return fmt.Errorf("settla-bank-deposit: handle credit result %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusCredited)))
	}

	if !result.Success {
		return e.failSession(ctx, session, result.Error, result.ErrorCode)
	}

	// Load tenant for fee recalculation
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle credit result: loading tenant: %w", err)
	}

	feeAmount := CalculateBankCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
	netAmount := session.ReceivedAmount.Sub(feeAmount)

	session.FeeAmount = feeAmount
	session.NetAmount = netAmount
	now := time.Now().UTC()
	session.CreditedAt = &now

	if err := session.TransitionTo(domain.BankDepositSessionStatusCredited); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle credit result: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusCredited,
		Currency:  string(session.Currency),
		Amount:    netAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle credit result: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositSessionCredited, eventPayload),
	}

	// Webhook + email notification for session credited
	notifEntries, err := bankDepositNotificationEntries(sessionID, tenantID,
		domain.EventBankDepositSessionCredited,
		fmt.Sprintf("Bank deposit credited — %s %s (net)", netAmount.String(), session.Currency),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle credit result: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle credit result", sessionID)
	}

	e.logger.Info("settla-bank-deposit: session credited",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"net_amount", netAmount,
	)

	// Route based on settlement preference
	return e.routeAfterCredit(ctx, tenantID, sessionID)
}

// routeAfterCredit decides what happens after CREDITED based on SettlementPref.
func (e *Engine) routeAfterCredit(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: route after credit: loading session %s: %w", sessionID, err)
	}

	switch session.SettlementPref {
	case domain.SettlementPreferenceAutoConvert:
		return e.initiateSettlement(ctx, session)
	case domain.SettlementPreferenceHold, domain.SettlementPreferenceThreshold:
		return e.holdSession(ctx, session)
	default:
		return e.holdSession(ctx, session)
	}
}

// initiateSettlement transitions CREDITED -> SETTLING with IntentBankDepositSettle.
func (e *Engine) initiateSettlement(ctx context.Context, session *domain.BankDepositSession) error {
	if session.Status != domain.BankDepositSessionStatusCredited {
		return fmt.Errorf("settla-bank-deposit: initiate settlement %s: %w",
			session.ID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusSettling)))
	}

	if err := session.TransitionTo(domain.BankDepositSessionStatusSettling); err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate settlement: %w", err)
	}

	settlePayload, err := json.Marshal(domain.SettleBankDepositPayload{
		SessionID:  session.ID,
		TenantID:   session.TenantID,
		Currency:   session.Currency,
		Amount:     session.NetAmount,
		TargetFiat: domain.CurrencyUSD, // default to USD; can be made configurable
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate settlement: marshalling payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.BankDepositSessionStatusSettling,
		Currency:  string(session.Currency),
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: initiate settlement: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxIntent("bank_deposit", session.ID, session.TenantID, domain.IntentBankDepositSettle, settlePayload),
		domain.MustNewOutboxEvent("bank_deposit", session.ID, session.TenantID, domain.EventBankDepositSessionSettling, eventPayload),
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "initiate settlement", session.ID)
	}

	e.logger.Info("settla-bank-deposit: settlement initiated",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"amount", session.NetAmount,
	)

	return nil
}

// holdSession transitions CREDITED -> HELD.
func (e *Engine) holdSession(ctx context.Context, session *domain.BankDepositSession) error {
	if session.Status != domain.BankDepositSessionStatusCredited {
		return fmt.Errorf("settla-bank-deposit: hold session %s: %w",
			session.ID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusHeld)))
	}

	if err := session.TransitionTo(domain.BankDepositSessionStatusHeld); err != nil {
		return fmt.Errorf("settla-bank-deposit: hold session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.BankDepositSessionStatusHeld,
		Currency:  string(session.Currency),
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: hold session: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", session.ID, session.TenantID, domain.EventBankDepositSessionHeld, eventPayload),
	}

	// Webhook + email notification for session held
	notifEntries, err := bankDepositNotificationEntries(session.ID, session.TenantID,
		domain.EventBankDepositSessionHeld,
		fmt.Sprintf("Bank deposit held — %s %s awaiting settlement preference", session.NetAmount.String(), session.Currency),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: hold session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "hold session", session.ID)
	}

	e.logger.Info("settla-bank-deposit: session held",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
	)

	return nil
}

// HandleSettlementResult processes the result of fiat -> stablecoin conversion.
// On success: transitions SETTLING -> SETTLED.
// On failure: transitions SETTLING -> FAILED.
func (e *Engine) HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle settlement result: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusSettling {
		return fmt.Errorf("settla-bank-deposit: handle settlement result %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusSettled)))
	}

	if !result.Success {
		return e.failSession(ctx, session, result.Error, result.ErrorCode)
	}

	now := time.Now().UTC()
	session.SettledAt = &now

	// Link to settlement transfer if provided
	if result.Metadata != nil {
		if transferIDStr, ok := result.Metadata["transfer_id"]; ok {
			if tid, err := uuid.Parse(transferIDStr); err == nil {
				session.SettlementTransferID = &tid
			}
		}
	}

	if err := session.TransitionTo(domain.BankDepositSessionStatusSettled); err != nil {
		return fmt.Errorf("settla-bank-deposit: handle settlement result: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusSettled,
		Currency:  string(session.Currency),
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle settlement result: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositSessionSettled, eventPayload),
	}

	// Webhook + email notification for session settled
	notifEntries, err := bankDepositNotificationEntries(sessionID, tenantID,
		domain.EventBankDepositSessionSettled,
		fmt.Sprintf("Bank deposit settled — %s %s converted", session.NetAmount.String(), session.Currency),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: handle settlement result: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle settlement result", sessionID)
	}

	e.logger.Info("settla-bank-deposit: session settled",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// ExpireSession transitions PENDING_PAYMENT -> EXPIRED for sessions past their TTL.
// For TEMPORARY accounts, emits IntentRecycleVirtualAccount to return the account to the pool.
func (e *Engine) ExpireSession(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: expire session: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusPendingPayment {
		// Already moved past pending — not expirable
		return nil
	}

	now := time.Now().UTC()
	session.ExpiredAt = &now
	if err := session.TransitionTo(domain.BankDepositSessionStatusExpired); err != nil {
		return fmt.Errorf("settla-bank-deposit: expire session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusExpired,
		Reason:    "ttl_exceeded",
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: expire session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositSessionExpired, eventPayload),
	}

	// Recycle virtual account for TEMPORARY accounts
	if session.AccountType == domain.VirtualAccountTypeTemporary {
		recyclePayload, err := json.Marshal(domain.RecycleVirtualAccountPayload{
			AccountNumber:    session.AccountNumber,
			BankingPartnerID: session.BankingPartnerID,
		})
		if err != nil {
			return fmt.Errorf("settla-bank-deposit: expire session: marshalling recycle payload: %w", err)
		}
		entries = append(entries, domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID, domain.IntentRecycleVirtualAccount, recyclePayload))
	}

	// Webhook + email notification for session expired
	notifEntries, err := bankDepositNotificationEntries(sessionID, tenantID,
		domain.EventBankDepositSessionExpired,
		"Bank deposit session expired — no payment received within TTL",
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: expire session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "expire session", sessionID)
	}

	e.logger.Info("settla-bank-deposit: session expired",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// CancelSession transitions PENDING_PAYMENT -> CANCELLED.
// For TEMPORARY accounts, emits IntentRecycleVirtualAccount to return the account to the pool.
func (e *Engine) CancelSession(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: cancel session: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.BankDepositSessionStatusPendingPayment {
		return fmt.Errorf("settla-bank-deposit: cancel session %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.BankDepositSessionStatusCancelled)))
	}

	if err := session.TransitionTo(domain.BankDepositSessionStatusCancelled); err != nil {
		return fmt.Errorf("settla-bank-deposit: cancel session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.BankDepositSessionStatusCancelled,
		Reason:    "user_cancelled",
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: cancel session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", sessionID, tenantID, domain.EventBankDepositSessionCancelled, eventPayload),
	}

	// Recycle virtual account for TEMPORARY accounts
	if session.AccountType == domain.VirtualAccountTypeTemporary {
		recyclePayload, err := json.Marshal(domain.RecycleVirtualAccountPayload{
			AccountNumber:    session.AccountNumber,
			BankingPartnerID: session.BankingPartnerID,
		})
		if err != nil {
			return fmt.Errorf("settla-bank-deposit: cancel session: marshalling recycle payload: %w", err)
		}
		entries = append(entries, domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID, domain.IntentRecycleVirtualAccount, recyclePayload))
	}

	// Webhook + email notification for session cancelled
	notifEntries, err := bankDepositNotificationEntries(sessionID, tenantID,
		domain.EventBankDepositSessionCancelled,
		"Bank deposit session cancelled",
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: cancel session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "cancel session", sessionID)
	}

	e.logger.Info("settla-bank-deposit: session cancelled",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// GetSession retrieves a bank deposit session by tenant and ID.
func (e *Engine) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error) {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: get session %s: %w", sessionID, err)
	}
	return session, nil
}

// ListSessions retrieves bank deposit sessions for a tenant with pagination.
func (e *Engine) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error) {
	sessions, err := e.store.ListSessions(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit: list sessions for tenant %s: %w", tenantID, err)
	}
	return sessions, nil
}

// failSession transitions a session to FAILED with reason and code.
func (e *Engine) failSession(ctx context.Context, session *domain.BankDepositSession, reason, code string) error {
	now := time.Now().UTC()
	session.FailedAt = &now
	session.FailureReason = reason
	session.FailureCode = code

	if err := session.TransitionTo(domain.BankDepositSessionStatusFailed); err != nil {
		return fmt.Errorf("settla-bank-deposit: fail session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.BankDepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.BankDepositSessionStatusFailed,
		Reason:    reason,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: fail session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("bank_deposit", session.ID, session.TenantID, domain.EventBankDepositSessionFailed, eventPayload),
	}

	// Webhook + email notification for session failed
	notifEntries, err := bankDepositNotificationEntries(session.ID, session.TenantID,
		domain.EventBankDepositSessionFailed,
		fmt.Sprintf("Bank deposit failed — %s", reason),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit: fail session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "fail session", session.ID)
	}

	e.logger.Warn("settla-bank-deposit: session failed",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"reason", reason,
		"code", code,
	)

	return nil
}

// bankDepositWebhookEntry builds an IntentWebhookDeliver outbox entry for a bank deposit event.
func bankDepositWebhookEntry(sessionID, tenantID uuid.UUID, eventType string, data []byte) (domain.OutboxEntry, error) {
	payload, err := json.Marshal(domain.WebhookDeliverPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		EventType: eventType,
		Data:      data,
	})
	if err != nil {
		return domain.OutboxEntry{}, fmt.Errorf("marshalling webhook payload: %w", err)
	}
	return domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID, domain.IntentWebhookDeliver, payload), nil
}

// bankDepositEmailEntry builds an IntentEmailNotify outbox entry for a bank deposit event.
func bankDepositEmailEntry(sessionID, tenantID uuid.UUID, eventType, subject string, data []byte) (domain.OutboxEntry, error) {
	payload, err := json.Marshal(domain.EmailNotifyPayload{
		TenantID:  tenantID,
		SessionID: sessionID,
		EventType: eventType,
		Subject:   subject,
		Data:      data,
	})
	if err != nil {
		return domain.OutboxEntry{}, fmt.Errorf("marshalling email payload: %w", err)
	}
	return domain.MustNewOutboxIntent("bank_deposit", sessionID, tenantID, domain.IntentEmailNotify, payload), nil
}

// bankDepositNotificationEntries builds webhook + email outbox entries for a bank deposit lifecycle event.
func bankDepositNotificationEntries(sessionID, tenantID uuid.UUID, eventType, emailSubject string, data []byte) ([]domain.OutboxEntry, error) {
	webhookEntry, err := bankDepositWebhookEntry(sessionID, tenantID, eventType, data)
	if err != nil {
		return nil, err
	}
	emailEntry, err := bankDepositEmailEntry(sessionID, tenantID, eventType, emailSubject, data)
	if err != nil {
		return nil, err
	}
	return []domain.OutboxEntry{webhookEntry, emailEntry}, nil
}

// wrapTransitionError adds context to TransitionWithOutbox errors.
func wrapTransitionError(err error, step string, sessionID uuid.UUID) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOptimisticLock) {
		return fmt.Errorf("settla-bank-deposit: %s: concurrent modification of session %s: %w", step, sessionID, ErrOptimisticLock)
	}
	return fmt.Errorf("settla-bank-deposit: %s %s: %w", step, sessionID, err)
}
