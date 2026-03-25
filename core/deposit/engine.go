package deposit

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

// Engine is the deposit session orchestrator. It coordinates the deposit lifecycle
// as a pure state machine: every method validates state, computes the next state
// plus outbox entries, and persists both atomically. The engine makes ZERO network
// calls — all side effects (chain monitoring, ledger credits, settlement) are
// expressed as outbox intents for workers to execute.
type Engine struct {
	store       DepositStore
	tenantStore TenantStore
	logger      *slog.Logger
}

// NewEngine creates a deposit engine wired to the given dependencies.
func NewEngine(store DepositStore, tenantStore TenantStore, logger *slog.Logger) *Engine {
	return &Engine{
		store:       store,
		tenantStore: tenantStore,
		logger:      logger.With("module", "core.deposit"),
	}
}

// CreateSessionRequest is the input for creating a new deposit session.
type CreateSessionRequest struct {
	IdempotencyKey domain.IdempotencyKey
	Chain          domain.CryptoChain
	Token          string
	ExpectedAmount decimal.Decimal
	SettlementPref domain.SettlementPreference
	TTLSeconds     int32
	Metadata       map[string]string
}

// CreateSession validates a deposit request, dispenses an address from the pool,
// and persists the session with an outbox intent to start chain monitoring.
func (e *Engine) CreateSession(ctx context.Context, tenantID uuid.UUID, req CreateSessionRequest) (*domain.DepositSession, error) {
	// a. Load tenant, verify active + crypto enabled
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}
	if !tenant.CryptoConfig.CryptoEnabled {
		return nil, domain.ErrCryptoDisabled(tenantID.String())
	}

	// b. Validate chain is supported

	if !tenant.ChainSupported(req.Chain) {
		return nil, domain.ErrChainNotSupported(string(req.Chain), tenantID.String())
	}

	// c. Validate amount > 0
	if !req.ExpectedAmount.IsPositive() {
		return nil, domain.ErrAmountTooLow(req.ExpectedAmount.String(), "0")
	}

	// d. Check per-tenant pending deposit session limit (same pattern as MaxPendingTransfers)
	if tenant.MaxPendingTransfers > 0 {
		count, err := e.store.CountPendingSessions(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("settla-deposit: create session: counting pending sessions: %w", err)
		}
		if count >= tenant.MaxPendingTransfers {
			return nil, fmt.Errorf("settla-deposit: create session: tenant %s exceeded max pending sessions (%d)", tenantID, tenant.MaxPendingTransfers)
		}
	}

	// e. Check idempotency
	if req.IdempotencyKey != "" {
		existing, err := e.store.GetSessionByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	// e. Dispense address from pool
	poolAddr, err := e.store.DispenseAddress(ctx, tenantID, string(req.Chain), uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: dispensing address: %w", err)
	}
	if poolAddr == nil {
		return nil, domain.ErrAddressPoolEmpty(string(req.Chain), tenantID.String())
	}

	// f. Determine settlement preference (use request or tenant default)
	settlementPref := req.SettlementPref
	if settlementPref == "" {
		settlementPref = tenant.CryptoConfig.DefaultSettlementPref
	}

	// g. Determine TTL
	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = tenant.CryptoConfig.DefaultSessionTTLSecs
	}
	if ttl <= 0 {
		ttl = 3600 // 1 hour default
	}

	// h. Map token to currency
	currency := domain.Currency(strings.ToUpper(req.Token))

	// i. Build session
	now := time.Now().UTC()
	session := &domain.DepositSession{
		ID:               uuid.Must(uuid.NewV7()),
		TenantID:         tenantID,
		IdempotencyKey:   req.IdempotencyKey,
		Status:           domain.DepositSessionStatusPendingPayment,
		Version:          1,
		Chain:            req.Chain,
		Token:            req.Token,
		DepositAddress:   poolAddr.Address,
		ExpectedAmount:   req.ExpectedAmount,
		ReceivedAmount:   decimal.Zero,
		Currency:         currency,
		CollectionFeeBPS: tenant.FeeSchedule.CryptoCollectionBPS,
		FeeAmount:        decimal.Zero,
		NetAmount:        decimal.Zero,
		SettlementPref:   settlementPref,
		DerivationIndex:  poolAddr.DerivationIndex,
		ExpiresAt:        now.Add(time.Duration(ttl) * time.Second),
		CreatedAt:        now,
		UpdatedAt:        now,
		Metadata:         req.Metadata,
	}

	// j. Build outbox entries: monitor address intent + session created event
	monitorPayload, err := json.Marshal(domain.MonitorAddressPayload{
		SessionID: session.ID,
		TenantID:  tenantID,
		Chain:     req.Chain,
		Address:   poolAddr.Address,
		Token:     req.Token,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: marshalling monitor payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusPendingPayment,
		Chain:     req.Chain,
		Token:     req.Token,
		Amount:    req.ExpectedAmount,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxIntent("deposit", session.ID, tenantID, domain.IntentMonitorAddress, monitorPayload),
		domain.MustNewOutboxEvent("deposit", session.ID, tenantID, domain.EventDepositSessionCreated, eventPayload),
	}

	// Webhook + email notification for session created
	notifEntries, err := depositNotificationEntries(session.ID, tenantID,
		domain.EventDepositSessionCreated,
		fmt.Sprintf("Deposit session created — awaiting %s %s", req.ExpectedAmount.String(), req.Token),
		eventPayload,
	)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: %w", err)
	}
	entries = append(entries, notifEntries...)

	// k. Persist session + outbox atomically
	// Update poolAddr session_id now that we have the session ID
	session.DerivationIndex = poolAddr.DerivationIndex
	if err := e.store.CreateSessionWithOutbox(ctx, session, entries); err != nil {
		return nil, fmt.Errorf("settla-deposit: create session: persisting: %w", err)
	}

	e.logger.Info("settla-deposit: session created",
		"session_id", session.ID,
		"tenant_id", tenantID,
		"chain", req.Chain,
		"token", req.Token,
		"address", poolAddr.Address,
	)

	return session, nil
}

// HandleTransactionDetected processes a newly detected on-chain transaction.
// Transitions PENDING_PAYMENT → DETECTED (or EXPIRED/CANCELLED → DETECTED for late payments).
func (e *Engine) HandleTransactionDetected(ctx context.Context, tenantID, sessionID uuid.UUID, tx domain.IncomingTransaction) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx detected: loading session %s: %w", sessionID, err)
	}

	// Record the transaction
	depositTx := &domain.DepositTransaction{
		ID:              uuid.Must(uuid.NewV7()),
		SessionID:       sessionID,
		TenantID:        tenantID,
		Chain:           tx.Chain,
		TxHash:          tx.TxHash,
		FromAddress:     tx.FromAddress,
		ToAddress:       tx.ToAddress,
		TokenContract:   tx.TokenContract,
		Amount:          tx.Amount,
		BlockNumber:     tx.BlockNumber,
		BlockHash:       tx.BlockHash,
		Confirmations:   0,
		RequiredConfirm: e.requiredConfirmations(session),
		Confirmed:       false,
		DetectedAt:      time.Now().UTC(),
	}

	// Check for duplicate tx
	existing, err := e.store.GetDepositTxByHash(ctx, string(tx.Chain), tx.TxHash)
	if err == nil && existing != nil {
		e.logger.Info("settla-deposit: duplicate tx detected, skipping",
			"session_id", sessionID,
			"tx_hash", tx.TxHash,
		)
		return nil
	}

	if err := e.store.RecordDepositTx(ctx, depositTx, tenantID, sessionID, tx.Amount); err != nil {
		return fmt.Errorf("settla-deposit: handle tx detected: recording tx and accumulating: %w", err)
	}

	// Check for late payment (session expired or cancelled)
	isLatePayment := session.Status == domain.DepositSessionStatusExpired ||
		session.Status == domain.DepositSessionStatusCancelled

	// Transition to DETECTED
	if !session.CanTransitionTo(domain.DepositSessionStatusDetected) {
		// If already DETECTED or further along, just record the tx
		e.logger.Info("settla-deposit: tx recorded, session already past DETECTED",
			"session_id", sessionID,
			"status", session.Status,
			"tx_hash", tx.TxHash,
		)
		return nil
	}

	session.ReceivedAmount = session.ReceivedAmount.Add(tx.Amount)
	now := time.Now().UTC()
	session.DetectedAt = &now
	if err := session.TransitionTo(domain.DepositSessionStatusDetected); err != nil {
		return fmt.Errorf("settla-deposit: handle tx detected: %w", err)
	}

	// Build outbox entries
	txPayload, err := json.Marshal(domain.DepositTxDetectedPayload{
		SessionID:   sessionID,
		TenantID:    tenantID,
		TxHash:      tx.TxHash,
		Chain:       tx.Chain,
		Token:       session.Token,
		Amount:      tx.Amount,
		BlockNumber: tx.BlockNumber,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx detected: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositTxDetected, txPayload),
	}

	// Webhook + email notification for tx detected
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositTxDetected,
		fmt.Sprintf("Payment detected — %s %s on %s", tx.Amount.String(), session.Token, tx.Chain),
		txPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx detected: %w", err)
	}
	entries = append(entries, notifEntries...)

	if isLatePayment {
		latePayload, err := json.Marshal(domain.DepositSessionEventPayload{
			SessionID: sessionID,
			TenantID:  tenantID,
			Status:    domain.DepositSessionStatusDetected,
			Chain:     tx.Chain,
			Token:     session.Token,
			Amount:    tx.Amount,
			Reason:    "late_payment",
		})
		if err != nil {
			return fmt.Errorf("settla-deposit: handle tx detected: marshalling late payment payload: %w", err)
		}
		entries = append(entries, domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositLatePayment, latePayload))
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle tx detected", sessionID)
	}

	e.logger.Info("settla-deposit: transaction detected",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"tx_hash", tx.TxHash,
		"amount", tx.Amount,
		"late_payment", isLatePayment,
	)

	return nil
}

// HandleTransactionConfirmed processes a confirmed on-chain transaction.
// Transitions DETECTED → CONFIRMED → CREDITING with an IntentCreditDeposit.
func (e *Engine) HandleTransactionConfirmed(ctx context.Context, tenantID, sessionID uuid.UUID, txHash string, confirmations int32) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx confirmed: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusDetected {
		return fmt.Errorf("settla-deposit: handle tx confirmed %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusConfirmed)))
	}

	// Transition to CONFIRMED
	now := time.Now().UTC()
	session.ConfirmedAt = &now
	if err := session.TransitionTo(domain.DepositSessionStatusConfirmed); err != nil {
		return fmt.Errorf("settla-deposit: handle tx confirmed: %w", err)
	}

	// Build confirmed event
	confirmedPayload, err := json.Marshal(domain.DepositTxConfirmedPayload{
		SessionID:     sessionID,
		TenantID:      tenantID,
		TxHash:        txHash,
		Chain:         session.Chain,
		Token:         session.Token,
		Amount:        session.ReceivedAmount,
		Confirmations: confirmations,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx confirmed: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositTxConfirmed, confirmedPayload),
	}

	// Webhook + email notification for tx confirmed
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositTxConfirmed,
		fmt.Sprintf("Payment confirmed — %s %s (%d confirmations)", session.ReceivedAmount.String(), session.Token, confirmations),
		confirmedPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle tx confirmed: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle tx confirmed", sessionID)
	}

	// Immediately transition CONFIRMED → CREDITING with credit intent
	return e.initiateCredit(ctx, tenantID, sessionID, txHash)
}

// initiateCredit transitions CONFIRMED → CREDITING and emits IntentCreditDeposit.
func (e *Engine) initiateCredit(ctx context.Context, tenantID, sessionID uuid.UUID, txHash string) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate credit: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusConfirmed {
		return fmt.Errorf("settla-deposit: initiate credit %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusCrediting)))
	}

	// Load tenant for fee calculation
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate credit: loading tenant: %w", err)
	}

	// Calculate collection fee
	feeAmount := CalculateCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
	netAmount := session.ReceivedAmount.Sub(feeAmount)

	if err := session.TransitionTo(domain.DepositSessionStatusCrediting); err != nil {
		return fmt.Errorf("settla-deposit: initiate credit: %w", err)
	}

	// Build credit intent
	creditPayload, err := json.Marshal(domain.CreditDepositPayload{
		SessionID:      sessionID,
		TenantID:       tenantID,
		Chain:          session.Chain,
		Token:          session.Token,
		GrossAmount:    session.ReceivedAmount,
		FeeAmount:      feeAmount,
		NetAmount:      netAmount,
		TxHash:         txHash,
		IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("deposit-credit:%s", sessionID)),
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate credit: marshalling payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusCrediting,
		Chain:     session.Chain,
		Token:     session.Token,
		Amount:    netAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate credit: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxIntent("deposit", sessionID, tenantID, domain.IntentCreditDeposit, creditPayload),
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositSessionCrediting, eventPayload),
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "initiate credit", sessionID)
	}

	e.logger.Info("settla-deposit: credit initiated",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"gross", session.ReceivedAmount,
		"fee", feeAmount,
		"net", netAmount,
	)

	return nil
}

// HandleCreditResult processes the result of ledger + treasury crediting.
// On success: transitions CREDITING → CREDITED, then routes based on settlement preference.
// On failure: transitions CREDITING → FAILED.
func (e *Engine) HandleCreditResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle credit result: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusCrediting {
		return fmt.Errorf("settla-deposit: handle credit result %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusCredited)))
	}

	if !result.Success {
		return e.failSession(ctx, session, result.Error, result.ErrorCode)
	}

	// Load tenant for fee recalculation (in case store didn't persist it yet)
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle credit result: loading tenant: %w", err)
	}

	feeAmount := CalculateCollectionFee(session.ReceivedAmount, tenant.FeeSchedule)
	netAmount := session.ReceivedAmount.Sub(feeAmount)

	session.FeeAmount = feeAmount
	session.NetAmount = netAmount
	now := time.Now().UTC()
	session.CreditedAt = &now

	if err := session.TransitionTo(domain.DepositSessionStatusCredited); err != nil {
		return fmt.Errorf("settla-deposit: handle credit result: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusCredited,
		Chain:     session.Chain,
		Token:     session.Token,
		Amount:    netAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: handle credit result: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositSessionCredited, eventPayload),
	}

	// Webhook + email notification for session credited
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositSessionCredited,
		fmt.Sprintf("Deposit credited — %s %s (net)", netAmount.String(), session.Token),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle credit result: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle credit result", sessionID)
	}

	e.logger.Info("settla-deposit: session credited",
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
		return fmt.Errorf("settla-deposit: route after credit: loading session %s: %w", sessionID, err)
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

// initiateSettlement transitions CREDITED → SETTLING with IntentSettleDeposit.
func (e *Engine) initiateSettlement(ctx context.Context, session *domain.DepositSession) error {
	if session.Status != domain.DepositSessionStatusCredited {
		return fmt.Errorf("settla-deposit: initiate settlement %s: %w",
			session.ID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusSettling)))
	}

	if err := session.TransitionTo(domain.DepositSessionStatusSettling); err != nil {
		return fmt.Errorf("settla-deposit: initiate settlement: %w", err)
	}

	settlePayload, err := json.Marshal(domain.SettleDepositPayload{
		SessionID:  session.ID,
		TenantID:   session.TenantID,
		Chain:      session.Chain,
		Token:      session.Token,
		Amount:     session.NetAmount,
		TargetFiat: domain.CurrencyUSD, // default to USD; can be made configurable
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate settlement: marshalling payload: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.DepositSessionStatusSettling,
		Chain:     session.Chain,
		Token:     session.Token,
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: initiate settlement: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxIntent("deposit", session.ID, session.TenantID, domain.IntentSettleDeposit, settlePayload),
		domain.MustNewOutboxEvent("deposit", session.ID, session.TenantID, domain.EventDepositSessionSettling, eventPayload),
	}

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "initiate settlement", session.ID)
	}

	e.logger.Info("settla-deposit: settlement initiated",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"amount", session.NetAmount,
	)

	return nil
}

// holdSession transitions CREDITED → HELD.
func (e *Engine) holdSession(ctx context.Context, session *domain.DepositSession) error {
	if session.Status != domain.DepositSessionStatusCredited {
		return fmt.Errorf("settla-deposit: hold session %s: %w",
			session.ID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusHeld)))
	}

	if err := session.TransitionTo(domain.DepositSessionStatusHeld); err != nil {
		return fmt.Errorf("settla-deposit: hold session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.DepositSessionStatusHeld,
		Chain:     session.Chain,
		Token:     session.Token,
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: hold session: marshalling event payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", session.ID, session.TenantID, domain.EventDepositSessionHeld, eventPayload),
	}

	// Webhook + email notification for session held
	notifEntries, err := depositNotificationEntries(session.ID, session.TenantID,
		domain.EventDepositSessionHeld,
		fmt.Sprintf("Deposit held — %s %s awaiting settlement preference", session.NetAmount.String(), session.Token),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: hold session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "hold session", session.ID)
	}

	e.logger.Info("settla-deposit: session held",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
	)

	return nil
}

// HandleSettlementResult processes the result of crypto → fiat conversion.
// On success: transitions SETTLING → SETTLED.
// On failure: transitions SETTLING → FAILED.
func (e *Engine) HandleSettlementResult(ctx context.Context, tenantID, sessionID uuid.UUID, result domain.IntentResult) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle settlement result: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusSettling {
		return fmt.Errorf("settla-deposit: handle settlement result %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusSettled)))
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

	if err := session.TransitionTo(domain.DepositSessionStatusSettled); err != nil {
		return fmt.Errorf("settla-deposit: handle settlement result: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusSettled,
		Chain:     session.Chain,
		Token:     session.Token,
		Amount:    session.NetAmount,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: handle settlement result: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositSessionSettled, eventPayload),
	}

	// Webhook + email notification for session settled
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositSessionSettled,
		fmt.Sprintf("Deposit settled — %s %s converted to fiat", session.NetAmount.String(), session.Token),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: handle settlement result: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "handle settlement result", sessionID)
	}

	e.logger.Info("settla-deposit: session settled",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// ExpireSession transitions PENDING_PAYMENT → EXPIRED for sessions past their TTL.
func (e *Engine) ExpireSession(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: expire session: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusPendingPayment {
		// Already moved past pending — not expirable
		return nil
	}

	now := time.Now().UTC()
	session.ExpiredAt = &now
	if err := session.TransitionTo(domain.DepositSessionStatusExpired); err != nil {
		return fmt.Errorf("settla-deposit: expire session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusExpired,
		Reason:    "ttl_exceeded",
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: expire session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositSessionExpired, eventPayload),
	}

	// Webhook + email notification for session expired
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositSessionExpired,
		"Deposit session expired — no payment received within TTL",
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: expire session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "expire session", sessionID)
	}

	e.logger.Info("settla-deposit: session expired",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// CancelSession transitions PENDING_PAYMENT → CANCELLED.
func (e *Engine) CancelSession(ctx context.Context, tenantID, sessionID uuid.UUID) error {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("settla-deposit: cancel session: loading session %s: %w", sessionID, err)
	}

	if session.Status != domain.DepositSessionStatusPendingPayment {
		return fmt.Errorf("settla-deposit: cancel session %s: %w",
			sessionID, domain.ErrInvalidTransition(string(session.Status), string(domain.DepositSessionStatusCancelled)))
	}

	if err := session.TransitionTo(domain.DepositSessionStatusCancelled); err != nil {
		return fmt.Errorf("settla-deposit: cancel session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		Status:    domain.DepositSessionStatusCancelled,
		Reason:    "user_cancelled",
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: cancel session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", sessionID, tenantID, domain.EventDepositSessionCancelled, eventPayload),
	}

	// Webhook + email notification for session cancelled
	notifEntries, err := depositNotificationEntries(sessionID, tenantID,
		domain.EventDepositSessionCancelled,
		"Deposit session cancelled",
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: cancel session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "cancel session", sessionID)
	}

	e.logger.Info("settla-deposit: session cancelled",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	return nil
}

// GetSession retrieves a deposit session by tenant and ID.
func (e *Engine) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.DepositSession, error) {
	session, err := e.store.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: get session %s: %w", sessionID, err)
	}
	return session, nil
}

// GetSessionByTxHash retrieves a deposit session by on-chain transaction hash.
func (e *Engine) GetSessionByTxHash(ctx context.Context, tenantID uuid.UUID, chain, txHash string) (*domain.DepositSession, error) {
	session, err := e.store.GetSessionByTxHash(ctx, tenantID, chain, txHash)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: get session by tx hash %s:%s: %w", chain, txHash, err)
	}
	return session, nil
}

// GetSessionPublicStatus retrieves a deposit session by ID without tenant filtering.
// Returns only the session data; callers should expose only public-safe fields.
func (e *Engine) GetSessionPublicStatus(ctx context.Context, sessionID uuid.UUID) (*domain.DepositSession, error) {
	session, err := e.store.GetSessionByIDOnly(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: get public status %s: %w", sessionID, err)
	}
	return session, nil
}

// ListSessions retrieves deposit sessions for a tenant with pagination.
func (e *Engine) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.DepositSession, error) {
	sessions, err := e.store.ListSessions(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit: list sessions for tenant %s: %w", tenantID, err)
	}
	return sessions, nil
}

// failSession transitions a session to FAILED with reason and code.
func (e *Engine) failSession(ctx context.Context, session *domain.DepositSession, reason, code string) error {
	now := time.Now().UTC()
	session.FailedAt = &now
	session.FailureReason = reason
	session.FailureCode = code

	if err := session.TransitionTo(domain.DepositSessionStatusFailed); err != nil {
		return fmt.Errorf("settla-deposit: fail session: %w", err)
	}

	eventPayload, err := json.Marshal(domain.DepositSessionEventPayload{
		SessionID: session.ID,
		TenantID:  session.TenantID,
		Status:    domain.DepositSessionStatusFailed,
		Reason:    reason,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit: fail session: marshalling payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		domain.MustNewOutboxEvent("deposit", session.ID, session.TenantID, domain.EventDepositSessionFailed, eventPayload),
	}

	// Webhook + email notification for session failed
	notifEntries, err := depositNotificationEntries(session.ID, session.TenantID,
		domain.EventDepositSessionFailed,
		fmt.Sprintf("Deposit failed — %s", reason),
		eventPayload,
	)
	if err != nil {
		return fmt.Errorf("settla-deposit: fail session: %w", err)
	}
	entries = append(entries, notifEntries...)

	if err := e.store.TransitionWithOutbox(ctx, session, entries); err != nil {
		return wrapTransitionError(err, "fail session", session.ID)
	}

	e.logger.Warn("settla-deposit: session failed",
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"reason", reason,
		"code", code,
	)

	return nil
}

// TODO: confirm these confirmation valies
// requiredConfirmations returns the min confirmations for a session's chain,
// using tenant config defaults.
func (e *Engine) requiredConfirmations(session *domain.DepositSession) int32 {
	// Default confirmations per chain
	switch session.Chain {
	case domain.ChainTron:
		return 19
	case domain.ChainEthereum:
		return 12
	case domain.ChainBase:
		return 12
	case domain.ChainPolygon:
		return 12
	case domain.ChainSolana:
		return 12
	default:
		return 20
	}
}

// depositWebhookEntry builds an IntentWebhookDeliver outbox entry for a deposit event.
func depositWebhookEntry(sessionID, tenantID uuid.UUID, eventType string, data []byte) (domain.OutboxEntry, error) {
	payload, err := json.Marshal(domain.WebhookDeliverPayload{
		SessionID: sessionID,
		TenantID:  tenantID,
		EventType: eventType,
		Data:      data,
	})
	if err != nil {
		return domain.OutboxEntry{}, fmt.Errorf("marshalling webhook payload: %w", err)
	}
	return domain.MustNewOutboxIntent("deposit", sessionID, tenantID, domain.IntentWebhookDeliver, payload), nil
}

// depositEmailEntry builds an IntentEmailNotify outbox entry for a deposit event.
func depositEmailEntry(sessionID, tenantID uuid.UUID, eventType, subject string, data []byte) (domain.OutboxEntry, error) {
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
	return domain.MustNewOutboxIntent("deposit", sessionID, tenantID, domain.IntentEmailNotify, payload), nil
}

// depositNotificationEntries builds webhook + email outbox entries for a deposit lifecycle event.
func depositNotificationEntries(sessionID, tenantID uuid.UUID, eventType, emailSubject string, data []byte) ([]domain.OutboxEntry, error) {
	webhookEntry, err := depositWebhookEntry(sessionID, tenantID, eventType, data)
	if err != nil {
		return nil, err
	}
	emailEntry, err := depositEmailEntry(sessionID, tenantID, eventType, emailSubject, data)
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
		return fmt.Errorf("settla-deposit: %s: concurrent modification of session %s: %w", step, sessionID, ErrOptimisticLock)
	}
	return fmt.Errorf("settla-deposit: %s %s: %w", step, sessionID, err)
}
