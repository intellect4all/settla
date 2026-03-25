package position

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// Engine is the position transaction orchestrator. It follows the same pure
// state machine pattern as core.Engine: every method validates state, computes
// the next state plus outbox entries, and persists both atomically. Zero
// network calls, zero side effects.
//
// Workers pick up outbox intents (IntentPositionCredit, IntentPositionDebit)
// and execute them against the treasury manager, then call back via
// HandleCreditResult / HandleDebitResult.
type Engine struct {
	store    Store
	treasury TreasuryReader
	logger   *slog.Logger
}

// TreasuryReader provides read-only access to treasury positions for validation.
type TreasuryReader interface {
	GetPosition(ctx context.Context, tenantID uuid.UUID, currency domain.Currency, location string) (*domain.Position, error)
}

// NewEngine creates a position transaction engine.
func NewEngine(store Store, treasury TreasuryReader, logger *slog.Logger) *Engine {
	return &Engine{
		store:    store,
		treasury: treasury,
		logger:   logger.With("module", "position-engine"),
	}
}

// RequestTopUp creates a new top-up position transaction. The transaction starts
// in PROCESSING state and emits an IntentPositionCredit outbox entry for the
// treasury worker to execute.
func (e *Engine) RequestTopUp(ctx context.Context, tenantID uuid.UUID, req domain.TopUpRequest) (*domain.PositionTransaction, error) {
	if !req.Amount.IsPositive() {
		return nil, fmt.Errorf("settla-position: top-up amount must be positive")
	}

	// Validate position exists.
	_, err := e.treasury.GetPosition(ctx, tenantID, req.Currency, req.Location)
	if err != nil {
		return nil, fmt.Errorf("settla-position: position not found for %s at %s: %w", req.Currency, req.Location, err)
	}

	now := time.Now().UTC()
	tx := &domain.PositionTransaction{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		Type:      domain.PositionTxTopUp,
		Currency:  req.Currency,
		Location:  req.Location,
		Amount:    req.Amount,
		Status:    domain.PositionTxStatusProcessing,
		Method:    req.Method,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	creditPayload, err := json.Marshal(domain.PositionCreditPayload{
		TenantID:  tenantID,
		Currency:  req.Currency,
		Amount:    req.Amount,
		Location:  req.Location,
		Reference: tx.ID,
		RefType:   "position_transaction",
	})
	if err != nil {
		return nil, fmt.Errorf("settla-position: marshalling credit payload: %w", err)
	}

	creditEntry, err := domain.NewOutboxIntent("position_transaction", tx.ID, tenantID, domain.IntentPositionCredit, creditPayload)
	if err != nil {
		return nil, fmt.Errorf("settla-position: creating outbox intent: %w", err)
	}

	if err := e.store.CreateWithOutbox(ctx, tx, []domain.OutboxEntry{creditEntry}); err != nil {
		return nil, fmt.Errorf("settla-position: creating top-up transaction: %w", err)
	}

	e.logger.Info("settla-position: top-up requested",
		"transaction_id", tx.ID,
		"tenant_id", tenantID,
		"currency", req.Currency,
		"amount", req.Amount.String(),
		"location", req.Location,
	)

	return tx, nil
}

// RequestWithdrawal creates a new withdrawal position transaction. Validates
// that the position has sufficient available balance before proceeding.
// The transaction starts in PROCESSING state and emits an IntentPositionDebit
// outbox entry for the treasury worker to execute.
func (e *Engine) RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, req domain.WithdrawalRequest) (*domain.PositionTransaction, error) {
	if !req.Amount.IsPositive() {
		return nil, fmt.Errorf("settla-position: withdrawal amount must be positive")
	}

	if req.Destination == "" {
		return nil, fmt.Errorf("settla-position: withdrawal destination is required")
	}

	// Validate position exists and has sufficient available balance.
	pos, err := e.treasury.GetPosition(ctx, tenantID, req.Currency, req.Location)
	if err != nil {
		return nil, fmt.Errorf("settla-position: position not found for %s at %s: %w", req.Currency, req.Location, err)
	}

	if pos.Available().LessThan(req.Amount) {
		return nil, domain.ErrInsufficientFunds(string(req.Currency), req.Location)
	}

	now := time.Now().UTC()
	tx := &domain.PositionTransaction{
		ID:          uuid.Must(uuid.NewV7()),
		TenantID:    tenantID,
		Type:        domain.PositionTxWithdrawal,
		Currency:    req.Currency,
		Location:    req.Location,
		Amount:      req.Amount,
		Status:      domain.PositionTxStatusProcessing,
		Method:      req.Method,
		Destination: req.Destination,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	debitPayload, err := json.Marshal(domain.PositionDebitPayload{
		TenantID:    tenantID,
		Currency:    req.Currency,
		Amount:      req.Amount,
		Location:    req.Location,
		Reference:   tx.ID,
		RefType:     "position_transaction",
		Destination: req.Destination,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-position: marshalling debit payload: %w", err)
	}

	debitEntry, err := domain.NewOutboxIntent("position_transaction", tx.ID, tenantID, domain.IntentPositionDebit, debitPayload)
	if err != nil {
		return nil, fmt.Errorf("settla-position: creating outbox intent: %w", err)
	}

	if err := e.store.CreateWithOutbox(ctx, tx, []domain.OutboxEntry{debitEntry}); err != nil {
		return nil, fmt.Errorf("settla-position: creating withdrawal transaction: %w", err)
	}

	e.logger.Info("settla-position: withdrawal requested",
		"transaction_id", tx.ID,
		"tenant_id", tenantID,
		"currency", req.Currency,
		"amount", req.Amount.String(),
		"location", req.Location,
		"destination", req.Destination,
	)

	return tx, nil
}

// HandleCreditResult processes the result of a position credit operation.
// Called by the treasury worker after IntentPositionCredit completes.
func (e *Engine) HandleCreditResult(ctx context.Context, tenantID, txID uuid.UUID, result domain.IntentResult) error {
	tx, err := e.store.Get(ctx, txID, tenantID)
	if err != nil {
		return fmt.Errorf("settla-position: loading transaction %s: %w", txID, err)
	}

	if tx.Status != domain.PositionTxStatusProcessing {
		e.logger.Warn("settla-position: credit result for non-processing transaction, skipping",
			"transaction_id", txID,
			"status", tx.Status,
		)
		return nil
	}

	if result.Success {
		if err := e.store.UpdateStatus(ctx, txID, tenantID, domain.PositionTxStatusCompleted, ""); err != nil {
			return fmt.Errorf("settla-position: completing transaction %s: %w", txID, err)
		}
		e.logger.Info("settla-position: credit completed",
			"transaction_id", txID,
			"tenant_id", tenantID,
		)
	} else {
		reason := result.Error
		if reason == "" {
			reason = "credit operation failed"
		}
		if err := e.store.UpdateStatus(ctx, txID, tenantID, domain.PositionTxStatusFailed, reason); err != nil {
			return fmt.Errorf("settla-position: failing transaction %s: %w", txID, err)
		}
		e.logger.Warn("settla-position: credit failed",
			"transaction_id", txID,
			"tenant_id", tenantID,
			"reason", reason,
		)
	}

	return nil
}

// HandleDebitResult processes the result of a position debit operation.
// Called by the treasury worker after IntentPositionDebit completes.
func (e *Engine) HandleDebitResult(ctx context.Context, tenantID, txID uuid.UUID, result domain.IntentResult) error {
	tx, err := e.store.Get(ctx, txID, tenantID)
	if err != nil {
		return fmt.Errorf("settla-position: loading transaction %s: %w", txID, err)
	}

	if tx.Status != domain.PositionTxStatusProcessing {
		e.logger.Warn("settla-position: debit result for non-processing transaction, skipping",
			"transaction_id", txID,
			"status", tx.Status,
		)
		return nil
	}

	if result.Success {
		if err := e.store.UpdateStatus(ctx, txID, tenantID, domain.PositionTxStatusCompleted, ""); err != nil {
			return fmt.Errorf("settla-position: completing transaction %s: %w", txID, err)
		}
		e.logger.Info("settla-position: debit completed",
			"transaction_id", txID,
			"tenant_id", tenantID,
		)
	} else {
		reason := result.Error
		if reason == "" {
			reason = "debit operation failed"
		}
		if err := e.store.UpdateStatus(ctx, txID, tenantID, domain.PositionTxStatusFailed, reason); err != nil {
			return fmt.Errorf("settla-position: failing transaction %s: %w", txID, err)
		}
		e.logger.Warn("settla-position: debit failed",
			"transaction_id", txID,
			"tenant_id", tenantID,
			"reason", reason,
		)
	}

	return nil
}

// GetTransaction retrieves a position transaction by ID.
func (e *Engine) GetTransaction(ctx context.Context, tenantID, txID uuid.UUID) (*domain.PositionTransaction, error) {
	return e.store.Get(ctx, txID, tenantID)
}

// ListTransactions returns paginated position transactions for a tenant.
func (e *Engine) ListTransactions(ctx context.Context, tenantID uuid.UUID, limit, offset int32) ([]domain.PositionTransaction, error) {
	return e.store.ListByTenant(ctx, tenantID, limit, offset)
}
