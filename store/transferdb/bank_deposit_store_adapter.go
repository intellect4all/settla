package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: BankDepositStoreAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithBankDepositAppPool configures the RLS-enforced pool for tenant-scoped reads.
func (s *BankDepositStoreAdapter) WithBankDepositAppPool(pool *pgxpool.Pool) *BankDepositStoreAdapter {
	s.appPool = pool
	s.rlsEnabled = (pool != nil)
	return s
}

// ── Row conversion helpers ───────────────────────────────────────────────────

// bankDepositSessionFromRow converts a SQLC BankDepositSession to a domain.BankDepositSession.
func bankDepositSessionFromRow(row BankDepositSession) domain.BankDepositSession {
	sess := domain.BankDepositSession{
		ID:               row.ID,
		TenantID:         row.TenantID,
		IdempotencyKey:   row.IdempotencyKey.String,
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

// ── Composite transactional writes (raw SQL) ─────────────────────────────────

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

	err = tx.QueryRow(ctx,
		`INSERT INTO bank_deposit_sessions (
			id, tenant_id, idempotency_key, status, version,
			banking_partner_id, account_number, account_name, sort_code, iban, account_type,
			currency, expected_amount, min_amount, max_amount, received_amount,
			fee_amount, net_amount, mismatch_policy, collection_fee_bps,
			settlement_pref, expires_at, metadata
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, $18, $19, $20,
			$21, $22, $23
		) RETURNING created_at, updated_at`,
		session.ID, session.TenantID, textFromString(session.IdempotencyKey),
		string(session.Status), session.Version,
		session.BankingPartnerID, session.AccountNumber, session.AccountName,
		session.SortCode, session.IBAN, string(session.AccountType),
		string(session.Currency), numericFromDecimal(session.ExpectedAmount),
		numericFromDecimal(session.MinAmount), numericFromDecimal(session.MaxAmount),
		numericFromDecimal(session.ReceivedAmount),
		numericFromDecimal(session.FeeAmount), numericFromDecimal(session.NetAmount),
		string(session.MismatchPolicy), int32(session.CollectionFeeBPS),
		string(session.SettlementPref), session.ExpiresAt, metadataJSON,
	).Scan(&session.CreatedAt, &session.UpdatedAt)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: creating session: %w", err)
	}

	// 2. INSERT virtual account index entry
	_, err = tx.Exec(ctx,
		`INSERT INTO virtual_account_index (account_number, tenant_id, session_id, account_type)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (account_number) DO UPDATE SET session_id = $3, account_type = $4`,
		session.AccountNumber, session.TenantID,
		pgtype.UUID{Bytes: session.ID, Valid: true},
		string(session.AccountType),
	)
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
		qtx := s.q.WithTx(tx)
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
	statusStr := string(session.Status)
	tag, err := tx.Exec(ctx,
		`UPDATE bank_deposit_sessions
		 SET status = $1::bank_deposit_session_status_enum,
		     version = $3,
		     updated_at = now(),
		     received_amount = $4,
		     fee_amount = $5,
		     net_amount = $6,
		     settlement_transfer_id = $7,
		     payer_name = COALESCE(NULLIF($8, ''), payer_name),
		     payer_reference = COALESCE(NULLIF($9, ''), payer_reference),
		     bank_reference = COALESCE(NULLIF($10, ''), bank_reference),
		     payment_received_at = CASE WHEN $14 = 'PAYMENT_RECEIVED' AND payment_received_at IS NULL THEN now() ELSE payment_received_at END,
		     credited_at  = CASE WHEN $14 = 'CREDITED'  AND credited_at IS NULL THEN now() ELSE credited_at END,
		     settled_at   = CASE WHEN $14 = 'SETTLED'   AND settled_at IS NULL THEN now() ELSE settled_at END,
		     expired_at   = CASE WHEN $14 = 'EXPIRED'   AND expired_at IS NULL THEN now() ELSE expired_at END,
		     failed_at    = CASE WHEN $14 = 'FAILED'    AND failed_at IS NULL THEN now() ELSE failed_at END,
		     failure_reason = COALESCE(NULLIF($11, ''), failure_reason),
		     failure_code   = COALESCE(NULLIF($12, ''), failure_code)
		 WHERE id = $2 AND version = $13`,
		statusStr,
		session.ID,
		session.Version,
		numericFromDecimal(session.ReceivedAmount),
		numericFromDecimal(session.FeeAmount),
		numericFromDecimal(session.NetAmount),
		uuidFromPtr(session.SettlementTransferID),
		session.PayerName,
		session.PayerReference,
		session.BankReference,
		session.FailureReason,
		session.FailureCode,
		session.Version-1, // expected version is pre-increment
		statusStr,         // $14: duplicate as plain text for CASE comparisons
	)
	if err != nil {
		return fmt.Errorf("settla-bank-deposit-store: update session %s: %w", session.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("settla-bank-deposit-store: session %s: %w", session.ID, bankdeposit.ErrOptimisticLock)
	}

	// 2. Batch INSERT outbox entries
	if len(entries) > 0 {
		qtx := s.q.WithTx(tx)
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

// ── SQLC-backed read methods ─────────────────────────────────────────────────

// GetSession retrieves a bank deposit session by tenant and ID.
func (s *BankDepositStoreAdapter) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.BankDepositSession, error) {
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
func (s *BankDepositStoreAdapter) GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.BankDepositSession, error) {
	row, err := s.q.GetBankDepositSessionByIdempotencyKey(ctx, GetBankDepositSessionByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: textFromString(key),
	})
	if err != nil {
		if err == pgx.ErrNoRows {
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
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-bank-deposit-store: session for account %s not found", accountNumber)
		}
		return nil, fmt.Errorf("settla-bank-deposit-store: getting session by account number: %w", err)
	}
	sess := bankDepositSessionFromRow(row)
	return &sess, nil
}

// ListSessions retrieves bank deposit sessions for a tenant with pagination.
func (s *BankDepositStoreAdapter) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.BankDepositSession, error) {
	rows, err := s.q.ListBankDepositSessionsByTenant(ctx, ListBankDepositSessionsByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
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

// DispenseVirtualAccount obtains a virtual account from the pre-provisioned pool.
func (s *BankDepositStoreAdapter) DispenseVirtualAccount(ctx context.Context, tenantID uuid.UUID, currency string) (*domain.VirtualAccountPool, error) {
	row, err := s.q.DispenseVirtualAccount(ctx, DispenseVirtualAccountParams{
		TenantID: tenantID,
		Currency: currency,
		SessionID: pgtype.UUID{Valid: false}, // no session yet
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
	result, err := s.q.CreateBankDepositTransaction(ctx, CreateBankDepositTransactionParams{
		SessionID:          dtx.SessionID,
		TenantID:           dtx.TenantID,
		BankReference:      dtx.BankReference,
		PayerName:          dtx.PayerName,
		PayerAccountNumber: dtx.PayerAccountNumber,
		Amount:             numericFromDecimal(dtx.Amount),
		Currency:           string(dtx.Currency),
		ReceivedAt:         dtx.ReceivedAt,
	})
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
		if err == pgx.ErrNoRows {
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
	if err := s.q.AccumulateBankReceivedAmount(ctx, AccumulateBankReceivedAmountParams{
		ID:             sessionID,
		TenantID:       tenantID,
		ReceivedAmount: numericFromDecimal(amount),
	}); err != nil {
		return fmt.Errorf("settla-bank-deposit-store: accumulating received: %w", err)
	}
	return nil
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
