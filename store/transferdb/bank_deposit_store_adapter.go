package transferdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	bankdeposit "github.com/intellect4all/settla/core/bankdeposit"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// Compile-time interface check.
var _ bankdeposit.BankDepositStore = (*BankDepositStoreAdapter)(nil)

// BankDepositStoreAdapter implements bankdeposit.BankDepositStore using SQLC-generated
// queries for reads and raw SQL for composite transactional writes.
type BankDepositStoreAdapter struct {
	q          *Queries
	pool       TxBeginner
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured; false means RLS is bypassed
}

// NewBankDepositStoreAdapter creates a new BankDepositStoreAdapter.
func NewBankDepositStoreAdapter(q *Queries, pool TxBeginner) *BankDepositStoreAdapter {
	a := &BankDepositStoreAdapter{q: q, pool: pool}
	a.rlsEnabled = a.appPool != nil
	if !a.rlsEnabled {
		slog.Warn("settla-store: BankDepositStoreAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithBankDepositAppPool configures the RLS-enforced pool for tenant-scoped reads.
func (s *BankDepositStoreAdapter) WithBankDepositAppPool(pool *pgxpool.Pool) *BankDepositStoreAdapter {
	s.appPool = pool
	s.rlsEnabled = pool != nil
	return s
}


// bankDepositSessionFromRow converts a SQLC BankDepositSession to a domain.BankDepositSession.
func bankDepositSessionFromRow(row BankDepositSession) domain.BankDepositSession {
	sess := domain.BankDepositSession{
		ID:               row.ID,
		TenantID:         row.TenantID,
		IdempotencyKey:   domain.IdempotencyKey(row.IdempotencyKey.String),
		Status:           domain.BankDepositSessionStatus(string(row.Status)),
		Version:          row.Version,
		BankingPartnerID: row.BankingPartnerID,
		AccountNumber:    row.AccountNumber,
		AccountName:      row.AccountName,
		SortCode:         row.SortCode,
		IBAN:             row.Iban,
		AccountType:      domain.VirtualAccountType(string(row.AccountType)),
		Currency:         domain.Currency(row.Currency),
		ExpectedAmount:   decimalFromNumeric(row.ExpectedAmount),
		MinAmount:        decimalFromNumeric(row.MinAmount),
		MaxAmount:        decimalFromNumeric(row.MaxAmount),
		ReceivedAmount:   decimalFromNumeric(row.ReceivedAmount),
		FeeAmount:        decimalFromNumeric(row.FeeAmount),
		NetAmount:        decimalFromNumeric(row.NetAmount),
		MismatchPolicy:   domain.PaymentMismatchPolicy(string(row.MismatchPolicy)),
		CollectionFeeBPS: int(row.CollectionFeeBps),
		SettlementPref:   domain.SettlementPreference(string(row.SettlementPref)),
		PayerName:        row.PayerName,
		PayerReference:   row.PayerReference,
		BankReference:    row.BankReference,
		ExpiresAt:        row.ExpiresAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		FailureReason:    row.FailureReason.String,
		FailureCode:      row.FailureCode.String,
	}

	if row.SettlementTransferID.Valid {
		id := uuid.UUID(row.SettlementTransferID.Bytes)
		sess.SettlementTransferID = &id
	}
	if row.PaymentReceivedAt.Valid {
		t := row.PaymentReceivedAt.Time
		sess.PaymentReceivedAt = &t
	}
	if row.CreditedAt.Valid {
		t := row.CreditedAt.Time
		sess.CreditedAt = &t
	}
	if row.SettledAt.Valid {
		t := row.SettledAt.Time
		sess.SettledAt = &t
	}
	if row.ExpiredAt.Valid {
		t := row.ExpiredAt.Time
		sess.ExpiredAt = &t
	}
	if row.FailedAt.Valid {
		t := row.FailedAt.Time
		sess.FailedAt = &t
	}
	if row.Metadata != nil {
		if err := json.Unmarshal(row.Metadata, &sess.Metadata); err != nil {
			slog.Warn("settla-bank-deposit-store: failed to unmarshal metadata", "session_id", row.ID, "error", err)
		}
	}

	return sess
}

// virtualAccountPoolFromRow converts a SQLC VirtualAccountPool to a domain.VirtualAccountPool.
func virtualAccountPoolFromRow(row VirtualAccountPool) domain.VirtualAccountPool {
	va := domain.VirtualAccountPool{
		ID:               row.ID,
		TenantID:         row.TenantID,
		BankingPartnerID: row.BankingPartnerID.String(),
		AccountNumber:    row.AccountNumber,
		AccountName:      row.AccountName,
		SortCode:         row.SortCode,
		IBAN:             row.Iban,
		Currency:         domain.Currency(row.Currency),
		AccountType:      domain.VirtualAccountType(string(row.AccountType)),
		Available:        row.Available,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	if row.SessionID.Valid {
		id := uuid.UUID(row.SessionID.Bytes)
		va.SessionID = &id
	}
	return va
}

// bankDepositTxFromRow converts a SQLC BankDepositTransaction to a domain.BankDepositTransaction.
func bankDepositTxFromRow(row BankDepositTransaction) domain.BankDepositTransaction {
	return domain.BankDepositTransaction{
		ID:                 row.ID,
		SessionID:          row.SessionID,
		TenantID:           row.TenantID,
		BankReference:      row.BankReference,
		PayerName:          row.PayerName,
		PayerAccountNumber: row.PayerAccountNumber,
		Amount:             decimalFromNumeric(row.Amount),
		Currency:           domain.Currency(row.Currency),
		ReceivedAt:         row.ReceivedAt,
		CreatedAt:          row.CreatedAt,
	}
}


// CreateSessionWithOutbox atomically creates a bank deposit session, registers the
// virtual account in the account index, and inserts outbox entries.
func (s *BankDepositStoreAdapter) CreateSessionWithOutbox(ctx context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-bank-deposit-store: CreateSessionWithOutbox requires a TxBeginner")
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if s.appPool != nil && session.TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, session.TenantID); err != nil {
			return fmt.Errorf("settla-bank-deposit-store: set tenant context: %w", err)
		}
	}

	// 1. INSERT bank deposit session
	metadataJSON, _ := json.Marshal(session.Metadata)
	if metadataJSON == nil {
		metadataJSON = []byte("{}")
	}

	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateBankDepositSessionFull(ctx, CreateBankDepositSessionFullParams{
		ID:               session.ID,
		TenantID:         session.TenantID,
		IdempotencyKey:   textFromString(string(session.IdempotencyKey)),
		Status:           BankDepositSessionStatusEnum(session.Status),
		Version:          session.Version,
		BankingPartnerID: session.BankingPartnerID,
		AccountNumber:    session.AccountNumber,
		AccountName:      session.AccountName,
		SortCode:         session.SortCode,
		Iban:             session.IBAN,
		AccountType:      VirtualAccountTypeEnum(session.AccountType),
		Currency:         string(session.Currency),
		ExpectedAmount:   numericFromDecimal(session.ExpectedAmount),
		MinAmount:        numericFromDecimal(session.MinAmount),
		MaxAmount:        numericFromDecimal(session.MaxAmount),
		ReceivedAmount:   numericFromDecimal(session.ReceivedAmount),
		FeeAmount:        numericFromDecimal(session.FeeAmount),
		NetAmount:        numericFromDecimal(session.NetAmount),
		MismatchPolicy:   PaymentMismatchPolicyEnum(session.MismatchPolicy),
		CollectionFeeBps: int32(session.CollectionFeeBPS),
		SettlementPref:   SettlementPreferenceEnum(session.SettlementPref),
		ExpiresAt:        session.ExpiresAt,
		Metadata:         metadataJSON,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: creating session: %w", err)
	}
	session.CreatedAt = row.CreatedAt
	session.UpdatedAt = row.UpdatedAt

	// 2. INSERT virtual account index entry (upsert)
	err = qtx.UpsertVirtualAccountIndex(ctx, UpsertVirtualAccountIndexParams{
		AccountNumber: session.AccountNumber,
		TenantID:      session.TenantID,
		SessionID:     pgtype.UUID{Bytes: session.ID, Valid: true},
		AccountType:   VirtualAccountTypeEnum(session.AccountType),
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: inserting virtual account index: %w", err)
	}

	// 3. Batch INSERT outbox entries
	if len(entries) > 0 {
		for i := range entries {
			if entries[i].AggregateID == uuid.Nil {
				entries[i].AggregateID = session.ID
			}
		}
		params := outboxEntriesToParams(entries)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-bank-deposit-store: insert outbox entries: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-bank-deposit-store: commit tx: %w", err)
	}
	return nil
}

// TransitionWithOutbox atomically updates session status and inserts outbox entries.
func (s *BankDepositStoreAdapter) TransitionWithOutbox(ctx context.Context, session *domain.BankDepositSession, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-bank-deposit-store: TransitionWithOutbox requires a TxBeginner")
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if s.appPool != nil && session.TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, session.TenantID); err != nil {
			return fmt.Errorf("settla-bank-deposit-store: set tenant context: %w", err)
		}
	}

	// 1. UPDATE session with optimistic lock + status-specific timestamps
	qtx := s.q.WithTx(tx)
	statusStr := string(session.Status)
	tag, err := qtx.TransitionBankDepositSession(ctx, TransitionBankDepositSessionParams{
		NewStatus:            BankDepositSessionStatusEnum(session.Status),
		ID:                   session.ID,
		NewVersion:           session.Version,
		ReceivedAmount:       numericFromDecimal(session.ReceivedAmount),
		FeeAmount:            numericFromDecimal(session.FeeAmount),
		NetAmount:            numericFromDecimal(session.NetAmount),
		SettlementTransferID: uuidFromPtr(session.SettlementTransferID),
		PayerName:            session.PayerName,
		PayerReference:       session.PayerReference,
		BankReferenceVal:     session.BankReference,
		StatusText:           statusStr,
		FailureReason:        session.FailureReason,
		FailureCode:          session.FailureCode,
		ExpectedVersion:      session.Version - 1,
	})
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: update session %s: %w", session.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("settla-bank-deposit-store: session %s: %w", session.ID, bankdeposit.ErrOptimisticLock)
	}

	// 2. Batch INSERT outbox entries
	if len(entries) > 0 {
		params := outboxEntriesToParams(entries)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-bank-deposit-store: insert outbox entries for session %s: %w", session.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-bank-deposit-store: commit tx for session %s: %w", session.ID, err)
	}
	return nil
}


// GetSession retrieves a bank deposit session by tenant and ID.
func (s *BankDepositStoreAdapter) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error) {
	if s.appPool != nil {
		var result *domain.BankDepositSession
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetBankDepositSession(ctx, GetBankDepositSessionParams{
				ID:       sessionID,
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}
			sess := bankDepositSessionFromRow(row)
			result = &sess
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: getting session %s: %w", sessionID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetSession", "tenant_id", tenantID)
	row, err := s.q.GetBankDepositSession(ctx, GetBankDepositSessionParams{
		ID:       sessionID,
		TenantID: tenantID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-bank-deposit-store: session %s not found: %w", sessionID, err)
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting session %s: %w", sessionID, err)
	}
	sess := bankDepositSessionFromRow(row)
	return &sess, nil
}

// GetSessionByIdempotencyKey retrieves a session by tenant and idempotency key.
func (s *BankDepositStoreAdapter) GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key domain.IdempotencyKey) (*domain.BankDepositSession, error) {
	if s.appPool != nil {
		var result *domain.BankDepositSession
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetBankDepositSessionByIdempotencyKey(ctx, GetBankDepositSessionByIdempotencyKeyParams{
				TenantID:       tenantID,
				IdempotencyKey: textFromString(string(key)),
			})
			if err != nil {
				return err
			}
			sess := bankDepositSessionFromRow(row)
			result = &sess
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: getting session by idempotency key: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetSessionByIdempotencyKey", "tenant_id", tenantID)
	row, err := s.q.GetBankDepositSessionByIdempotencyKey(ctx, GetBankDepositSessionByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: textFromString(string(key)),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("settla-bank-deposit-store: session for idempotency key not found")
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting session by idempotency key: %w", err)
	}
	sess := bankDepositSessionFromRow(row)
	return &sess, nil
}

// GetSessionByAccountNumber retrieves the most recent active session for a virtual account.
func (s *BankDepositStoreAdapter) GetSessionByAccountNumber(ctx context.Context, accountNumber string) (*domain.BankDepositSession, error) {
	row, err := s.q.GetBankDepositSessionByAccountNumber(ctx, accountNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("settla-bank-deposit-store: session for account %s not found", accountNumber)
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting session by account number: %w", err)
	}
	sess := bankDepositSessionFromRow(row)
	return &sess, nil
}

// ListSessions retrieves bank deposit sessions for a tenant with pagination.
func (s *BankDepositStoreAdapter) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error) {
	if s.appPool != nil {
		var sessions []domain.BankDepositSession
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListBankDepositSessionsByTenantFirst(ctx, ListBankDepositSessionsByTenantFirstParams{
				TenantID: tenantID,
				Limit:    int32(limit),
			})
			if err != nil {
				return err
			}
			sessions = make([]domain.BankDepositSession, 0, len(rows))
			for _, row := range rows {
				sessions = append(sessions, bankDepositSessionFromRow(row))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: listing sessions: %w", err)
		}
		return sessions, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListSessions", "tenant_id", tenantID)
	rows, err := s.q.ListBankDepositSessionsByTenantFirst(ctx, ListBankDepositSessionsByTenantFirstParams{
		TenantID: tenantID,
		Limit:    int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: listing sessions: %w", err)
	}
	sessions := make([]domain.BankDepositSession, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, bankDepositSessionFromRow(row))
	}
	return sessions, nil
}

// ListSessionsCursor retrieves bank deposit sessions using cursor-based pagination (created_at < cursor, DESC).
func (s *BankDepositStoreAdapter) ListSessionsCursor(ctx context.Context, tenantID uuid.UUID, pageSize int, cursor time.Time) ([]domain.BankDepositSession, error) {
	params := ListBankDepositSessionsByTenantCursorParams{
		TenantID:        tenantID,
		CursorCreatedAt: cursor,
		PageSize:        int32(pageSize),
	}

	if s.appPool != nil {
		var sessions []domain.BankDepositSession
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListBankDepositSessionsByTenantCursor(ctx, params)
			if err != nil {
				return err
			}
			sessions = make([]domain.BankDepositSession, 0, len(rows))
			for _, row := range rows {
				sessions = append(sessions, bankDepositSessionFromRow(row))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: listing sessions cursor: %w", err)
		}
		return sessions, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListSessionsCursor", "tenant_id", tenantID)
	rows, err := s.q.ListBankDepositSessionsByTenantCursor(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: listing sessions cursor: %w", err)
	}
	sessions := make([]domain.BankDepositSession, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, bankDepositSessionFromRow(row))
	}
	return sessions, nil
}

// DispenseVirtualAccount obtains a virtual account from the pre-provisioned pool.
func (s *BankDepositStoreAdapter) DispenseVirtualAccount(ctx context.Context, tenantID uuid.UUID, currency string) (*domain.VirtualAccountPool, error) {
	if s.appPool != nil {
		var result *domain.VirtualAccountPool
		err := rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).DispenseVirtualAccount(ctx, DispenseVirtualAccountParams{
				TenantID:  tenantID,
				Currency:  currency,
				SessionID: pgtype.UUID{Valid: false},
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil // no available accounts
				}
				return err
			}
			va := virtualAccountPoolFromRow(row)
			result = &va
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: dispensing virtual account: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "DispenseVirtualAccount", "tenant_id", tenantID)
	row, err := s.q.DispenseVirtualAccount(ctx, DispenseVirtualAccountParams{
		TenantID:  tenantID,
		Currency:  currency,
		SessionID: pgtype.UUID{Valid: false},
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no available accounts
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: dispensing virtual account: %w", err)
	}
	va := virtualAccountPoolFromRow(row)
	return &va, nil
}

// RecycleVirtualAccount marks a virtual account as available for reuse.
func (s *BankDepositStoreAdapter) RecycleVirtualAccount(ctx context.Context, accountNumber string) error {
	if err := s.q.RecycleVirtualAccount(ctx, accountNumber); err != nil {
		return fmt.Errorf("settla-bank-deposit-store: recycling virtual account %s: %w", accountNumber, err)
	}
	return nil
}

// CreateBankDepositTx records a bank credit transaction linked to a session.
func (s *BankDepositStoreAdapter) CreateBankDepositTx(ctx context.Context, dtx *domain.BankDepositTransaction) error {
	params := CreateBankDepositTransactionParams{
		SessionID:          dtx.SessionID,
		TenantID:           dtx.TenantID,
		BankReference:      dtx.BankReference,
		PayerName:          dtx.PayerName,
		PayerAccountNumber: dtx.PayerAccountNumber,
		Amount:             numericFromDecimal(dtx.Amount),
		Currency:           string(dtx.Currency),
		ReceivedAt:         dtx.ReceivedAt,
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, dtx.TenantID, func(tx pgx.Tx) error {
			result, err := s.q.WithTx(tx).CreateBankDepositTransaction(ctx, params)
			if err != nil {
				return fmt.Errorf("settla-bank-deposit-store: creating bank deposit tx: %w", err)
			}
			dtx.ID = result.ID
			dtx.CreatedAt = result.CreatedAt
			return nil
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "CreateBankDepositTx", "tenant_id", dtx.TenantID)
	result, err := s.q.CreateBankDepositTransaction(ctx, params)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: creating bank deposit tx: %w", err)
	}
	dtx.ID = result.ID
	dtx.CreatedAt = result.CreatedAt
	return nil
}

// GetBankDepositTxByRef retrieves a bank deposit transaction by bank reference (dedup key).
func (s *BankDepositStoreAdapter) GetBankDepositTxByRef(ctx context.Context, bankReference string) (*domain.BankDepositTransaction, error) {
	row, err := s.q.GetBankDepositTransactionByRef(ctx, bankReference)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("settla-bank-deposit-store: tx ref %s not found", bankReference)
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting tx by ref: %w", err)
	}
	dtx := bankDepositTxFromRow(row)
	return &dtx, nil
}

// ListSessionTxs retrieves all transactions for a session.
func (s *BankDepositStoreAdapter) ListSessionTxs(ctx context.Context, sessionID uuid.UUID) ([]domain.BankDepositTransaction, error) {
	rows, err := s.q.ListBankDepositTransactionsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: listing txs: %w", err)
	}
	txs := make([]domain.BankDepositTransaction, 0, len(rows))
	for _, row := range rows {
		txs = append(txs, bankDepositTxFromRow(row))
	}
	return txs, nil
}

// AccumulateReceived adds an amount to the session's received_amount.
func (s *BankDepositStoreAdapter) AccumulateReceived(ctx context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	params := AccumulateBankReceivedAmountParams{
		ID:             sessionID,
		TenantID:       tenantID,
		ReceivedAmount: numericFromDecimal(amount),
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			return s.q.WithTx(tx).AccumulateBankReceivedAmount(ctx, params)
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "AccumulateReceived", "tenant_id", tenantID)
	return s.q.AccumulateBankReceivedAmount(ctx, params)
}

// RecordBankDepositTx atomically creates a bank deposit transaction and accumulates
// the received amount on the session in a single database transaction.
func (s *BankDepositStoreAdapter) RecordBankDepositTx(ctx context.Context, dtx *domain.BankDepositTransaction, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	createParams := CreateBankDepositTransactionParams{
		SessionID:          dtx.SessionID,
		TenantID:           dtx.TenantID,
		BankReference:      dtx.BankReference,
		PayerName:          dtx.PayerName,
		PayerAccountNumber: dtx.PayerAccountNumber,
		Amount:             numericFromDecimal(dtx.Amount),
		Currency:           string(dtx.Currency),
		ReceivedAt:         dtx.ReceivedAt,
	}
	accumParams := AccumulateBankReceivedAmountParams{
		ID:             sessionID,
		TenantID:       tenantID,
		ReceivedAmount: numericFromDecimal(amount),
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			qtx := s.q.WithTx(tx)
			result, err := qtx.CreateBankDepositTransaction(ctx, createParams)
			if err != nil {
				return fmt.Errorf("settla-bank-deposit-store: record bank deposit tx: create: %w", err)
			}
			dtx.ID = result.ID
			dtx.CreatedAt = result.CreatedAt
			if err := qtx.AccumulateBankReceivedAmount(ctx, accumParams); err != nil {
				return fmt.Errorf("settla-bank-deposit-store: record bank deposit tx: accumulate: %w", err)
			}
			return nil
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "RecordBankDepositTx", "tenant_id", tenantID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: begin record-bank-deposit-tx: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)
	result, err := qtx.CreateBankDepositTransaction(ctx, createParams)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: record bank deposit tx: create: %w", err)
	}
	dtx.ID = result.ID
	dtx.CreatedAt = result.CreatedAt
	if err := qtx.AccumulateBankReceivedAmount(ctx, accumParams); err != nil {
		return fmt.Errorf("settla-bank-deposit-store: record bank deposit tx: accumulate: %w", err)
	}
	return tx.Commit(ctx)
}

// GetExpiredPendingSessions returns sessions in PENDING_PAYMENT with expires_at < now().
func (s *BankDepositStoreAdapter) GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.BankDepositSession, error) {
	rows, err := s.q.GetExpiredPendingBankSessions(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: getting expired sessions: %w", err)
	}
	sessions := make([]domain.BankDepositSession, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, bankDepositSessionFromRow(row))
	}
	return sessions, nil
}

// ListVirtualAccountsByTenant returns all virtual accounts for a tenant.
func (s *BankDepositStoreAdapter) ListVirtualAccountsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error) {
	if s.appPool != nil {
		var accounts []domain.VirtualAccountPool
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListVirtualAccountsByTenant(ctx, tenantID)
			if err != nil {
				return err
			}
			accounts = make([]domain.VirtualAccountPool, 0, len(rows))
			for _, row := range rows {
				accounts = append(accounts, virtualAccountPoolFromRow(row))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: listing virtual accounts: %w", err)
		}
		return accounts, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListVirtualAccountsByTenant", "tenant_id", tenantID)
	rows, err := s.q.ListVirtualAccountsByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: listing virtual accounts: %w", err)
	}
	accounts := make([]domain.VirtualAccountPool, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, virtualAccountPoolFromRow(row))
	}
	return accounts, nil
}

// ListVirtualAccountsPaginated returns a paginated, filtered list of virtual accounts plus total count.
func (s *BankDepositStoreAdapter) ListVirtualAccountsPaginated(ctx context.Context, params bankdeposit.VirtualAccountListParams) ([]domain.VirtualAccountPool, int64, error) {
	if s.appPool != nil {
		var accounts []domain.VirtualAccountPool
		var total int64
		err := rls.WithTenantReadTx(ctx, s.appPool, params.TenantID, func(tx pgx.Tx) error {
			qtx := s.q.WithTx(tx)
			rows, err := qtx.ListVirtualAccountsByTenantPaginated(ctx, ListVirtualAccountsByTenantPaginatedParams{
				TenantID:    params.TenantID,
				Currency:    params.Currency,
				AccountType: params.AccountType,
				PageLimit:   params.Limit,
				PageOffset:  params.Offset,
			})
			if err != nil {
				return err
			}
			accounts = make([]domain.VirtualAccountPool, 0, len(rows))
			for _, row := range rows {
				accounts = append(accounts, virtualAccountPoolFromRow(row))
			}
			total, err = qtx.CountVirtualAccountsByTenant(ctx, CountVirtualAccountsByTenantParams{
				TenantID:    params.TenantID,
				Currency:    params.Currency,
				AccountType: params.AccountType,
			})
			return err
		})
		if err != nil {
			return nil, 0, fmt.Errorf("settla-bank-deposit-store: listing virtual accounts paginated: %w", err)
		}
		return accounts, total, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListVirtualAccountsPaginated", "tenant_id", params.TenantID)
	rows, err := s.q.ListVirtualAccountsByTenantPaginated(ctx, ListVirtualAccountsByTenantPaginatedParams{
		TenantID:    params.TenantID,
		Currency:    params.Currency,
		AccountType: params.AccountType,
		PageLimit:   params.Limit,
		PageOffset:  params.Offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("settla-bank-deposit-store: listing virtual accounts paginated: %w", err)
	}
	accounts := make([]domain.VirtualAccountPool, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, virtualAccountPoolFromRow(row))
	}

	total, err := s.q.CountVirtualAccountsByTenant(ctx, CountVirtualAccountsByTenantParams{
		TenantID:    params.TenantID,
		Currency:    params.Currency,
		AccountType: params.AccountType,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("settla-bank-deposit-store: counting virtual accounts: %w", err)
	}

	return accounts, total, nil
}

// ListVirtualAccountsCursor returns virtual accounts using cursor-based pagination (created_at > cursor, ASC).
func (s *BankDepositStoreAdapter) ListVirtualAccountsCursor(ctx context.Context, params bankdeposit.VirtualAccountCursorParams) ([]domain.VirtualAccountPool, error) {
	const query = `SELECT id, tenant_id, banking_partner_id, account_number, account_name, sort_code, iban, currency, account_type, available, session_id, created_at, updated_at
FROM virtual_account_pool
WHERE tenant_id = $1
  AND ($2::text = '' OR currency = $2)
  AND ($3::text = '' OR account_type = $3::virtual_account_type_enum)
  AND created_at > $4
ORDER BY created_at ASC
LIMIT $5`

	scanRows := func(db DBTX) ([]domain.VirtualAccountPool, error) {
		rows, err := db.Query(ctx, query, params.TenantID, params.Currency, params.AccountType, params.Cursor, params.PageSize)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var accounts []domain.VirtualAccountPool
		for rows.Next() {
			var row VirtualAccountPool
			if err := rows.Scan(
				&row.ID, &row.TenantID, &row.BankingPartnerID, &row.AccountNumber,
				&row.AccountName, &row.SortCode, &row.Iban, &row.Currency,
				&row.AccountType, &row.Available, &row.SessionID,
				&row.CreatedAt, &row.UpdatedAt,
			); err != nil {
				return nil, err
			}
			accounts = append(accounts, virtualAccountPoolFromRow(row))
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return accounts, nil
	}

	if s.appPool != nil {
		var accounts []domain.VirtualAccountPool
		err := rls.WithTenantReadTx(ctx, s.appPool, params.TenantID, func(tx pgx.Tx) error {
			var txErr error
			accounts, txErr = scanRows(tx)
			return txErr
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: list virtual accounts cursor: %w", err)
		}
		return accounts, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListVirtualAccountsCursor", "tenant_id", params.TenantID)
	accounts, err := scanRows(s.q.db)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: list virtual accounts cursor: %w", err)
	}
	return accounts, nil
}

// CountAvailableVirtualAccountsByCurrency returns available account counts grouped by currency.
func (s *BankDepositStoreAdapter) CountAvailableVirtualAccountsByCurrency(ctx context.Context, tenantID uuid.UUID) (map[string]int64, error) {
	if s.appPool != nil {
		var result map[string]int64
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).CountAvailableVirtualAccountsByCurrency(ctx, tenantID)
			if err != nil {
				return err
			}
			result = make(map[string]int64, len(rows))
			for _, row := range rows {
				result[row.Currency] = row.AvailableCount
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-bank-deposit-store: counting available virtual accounts: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "CountAvailableVirtualAccountsByCurrency", "tenant_id", tenantID)
	rows, err := s.q.CountAvailableVirtualAccountsByCurrency(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-bank-deposit-store: counting available virtual accounts: %w", err)
	}
	result := make(map[string]int64, len(rows))
	for _, row := range rows {
		result[row.Currency] = row.AvailableCount
	}
	return result, nil
}

// GetVirtualAccountIndexByNumber retrieves the account index entry by account number.
func (s *BankDepositStoreAdapter) GetVirtualAccountIndexByNumber(ctx context.Context, accountNumber string) (*bankdeposit.VirtualAccountIndex, error) {
	row, err := s.q.GetVirtualAccountIndexByNumber(ctx, accountNumber)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-bank-deposit-store: account index for %s not found: %w", accountNumber, err)
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting account index for %s: %w", accountNumber, err)
	}
	idx := &bankdeposit.VirtualAccountIndex{
		AccountNumber: row.AccountNumber,
		TenantID:      row.TenantID,
		AccountType:   domain.VirtualAccountType(string(row.AccountType)),
	}
	if row.SessionID.Valid {
		id := uuid.UUID(row.SessionID.Bytes)
		idx.SessionID = &id
	}
	return idx, nil
}
