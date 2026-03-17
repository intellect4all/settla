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

	deposit "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// Compile-time interface check.
var _ deposit.DepositStore = (*DepositStoreAdapter)(nil)

// DepositStoreAdapter implements deposit.DepositStore using SQLC-generated queries.
type DepositStoreAdapter struct {
	q          *Queries
	pool       TxBeginner
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured; false means RLS is bypassed
}

// NewDepositStoreAdapter creates a new DepositStoreAdapter.
func NewDepositStoreAdapter(q *Queries, pool TxBeginner) *DepositStoreAdapter {
	a := &DepositStoreAdapter{q: q, pool: pool}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: DepositStoreAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithDepositAppPool configures the RLS-enforced pool for tenant-scoped reads.
func (s *DepositStoreAdapter) WithDepositAppPool(pool *pgxpool.Pool) *DepositStoreAdapter {
	s.appPool = pool
	s.rlsEnabled = (pool != nil)
	return s
}

// CreateSessionWithOutbox atomically creates a deposit session, registers the
// address in the address index, and inserts outbox entries.
func (s *DepositStoreAdapter) CreateSessionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-deposit-store: CreateSessionWithOutbox requires a TxBeginner")
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-deposit-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if s.appPool != nil && session.TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, session.TenantID); err != nil {
			return fmt.Errorf("settla-deposit-store: set tenant context: %w", err)
		}
	}

	qtx := s.q.WithTx(tx)

	// 1. INSERT deposit session
	metadataJSON, _ := json.Marshal(session.Metadata)
	if metadataJSON == nil {
		metadataJSON = []byte("{}")
	}

	row, err := qtx.CreateDepositSession(ctx, CreateDepositSessionParams{
		TenantID:        session.TenantID,
		IdempotencyKey:  textFromString(session.IdempotencyKey),
		Status:          DepositSessionStatusEnum(session.Status),
		Chain:           session.Chain,
		Token:           session.Token,
		DepositAddress:  session.DepositAddress,
		ExpectedAmount:  numericFromDecimal(session.ExpectedAmount),
		Currency:        string(session.Currency),
		CollectionFeeBps: int32(session.CollectionFeeBPS),
		SettlementPref:  SettlementPreferenceEnum(session.SettlementPref),
		DerivationIndex: session.DerivationIndex,
		ExpiresAt:       session.ExpiresAt,
		Metadata:        metadataJSON,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit-store: creating session: %w", err)
	}

	session.ID = row.ID
	session.CreatedAt = row.CreatedAt
	session.UpdatedAt = row.UpdatedAt

	// 2. INSERT address index entry
	_, err = qtx.InsertAddressIndex(ctx, InsertAddressIndexParams{
		Chain:     session.Chain,
		Address:   session.DepositAddress,
		TenantID:  session.TenantID,
		SessionID: session.ID,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit-store: inserting address index: %w", err)
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
			return fmt.Errorf("settla-deposit-store: insert outbox entries: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-deposit-store: commit tx: %w", err)
	}
	return nil
}

// GetSession retrieves a deposit session by tenant and ID.
func (s *DepositStoreAdapter) GetSession(ctx context.Context, tenantID, sessionID uuid.UUID) (*domain.DepositSession, error) {
	row, err := s.q.GetDepositSession(ctx, GetDepositSessionParams{
		ID:       sessionID,
		TenantID: tenantID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: session %s not found: %w", sessionID, err)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting session %s: %w", sessionID, err)
	}
	return depositSessionFromRow(row), nil
}

// GetSessionByAddress retrieves the most recent deposit session for an address.
// Uses the non-tenant-scoped query because deposit addresses are globally unique
// (HD wallet derived) and the caller discovers tenant_id from the result.
func (s *DepositStoreAdapter) GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error) {
	row, err := s.q.GetDepositSessionByAddressOnly(ctx, address)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: session for address %s not found", address)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting session by address: %w", err)
	}
	return depositSessionFromRow(row), nil
}

// GetSessionByIdempotencyKey retrieves a session by tenant and idempotency key.
func (s *DepositStoreAdapter) GetSessionByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.DepositSession, error) {
	row, err := s.q.GetDepositSessionByIdempotencyKey(ctx, GetDepositSessionByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: pgtype.Text{String: key, Valid: true},
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: session for idempotency key not found")
		}
		return nil, fmt.Errorf("settla-deposit-store: getting session by idempotency key: %w", err)
	}
	return depositSessionFromRow(row), nil
}

// ListSessions retrieves deposit sessions for a tenant with pagination.
func (s *DepositStoreAdapter) ListSessions(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.DepositSession, error) {
	rows, err := s.q.ListDepositSessionsByTenant(ctx, ListDepositSessionsByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-deposit-store: listing sessions: %w", err)
	}
	sessions := make([]domain.DepositSession, len(rows))
	for i, row := range rows {
		sessions[i] = *depositSessionFromRow(row)
	}
	return sessions, nil
}

// TransitionWithOutbox atomically updates session status and inserts outbox entries.
func (s *DepositStoreAdapter) TransitionWithOutbox(ctx context.Context, session *domain.DepositSession, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-deposit-store: TransitionWithOutbox requires a TxBeginner")
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-deposit-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if s.appPool != nil && session.TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, session.TenantID); err != nil {
			return fmt.Errorf("settla-deposit-store: set tenant context: %w", err)
		}
	}

	// 1. UPDATE session with optimistic lock + status-specific timestamps
	tag, err := tx.Exec(ctx,
		`UPDATE crypto_deposit_sessions
		 SET status = $1::deposit_session_status_enum,
		     version = $3,
		     updated_at = now(),
		     received_amount = $4,
		     fee_amount = $5,
		     net_amount = $6,
		     settlement_transfer_id = $7,
		     detected_at    = CASE WHEN $1::text = 'DETECTED'    AND detected_at IS NULL THEN now() ELSE detected_at END,
		     confirmed_at   = CASE WHEN $1::text = 'CONFIRMED'   AND confirmed_at IS NULL THEN now() ELSE confirmed_at END,
		     credited_at    = CASE WHEN $1::text = 'CREDITED'    AND credited_at IS NULL THEN now() ELSE credited_at END,
		     settled_at     = CASE WHEN $1::text = 'SETTLED'     AND settled_at IS NULL THEN now() ELSE settled_at END,
		     expired_at     = CASE WHEN $1::text = 'EXPIRED'     AND expired_at IS NULL THEN now() ELSE expired_at END,
		     failed_at      = CASE WHEN $1::text = 'FAILED'      AND failed_at IS NULL THEN now() ELSE failed_at END,
		     failure_reason = COALESCE(NULLIF($8, ''), failure_reason),
		     failure_code   = COALESCE(NULLIF($9, ''), failure_code)
		 WHERE id = $2 AND version = $10`,
		string(session.Status),
		session.ID,
		session.Version,
		numericFromDecimal(session.ReceivedAmount),
		numericFromDecimal(session.FeeAmount),
		numericFromDecimal(session.NetAmount),
		uuidFromPtr(session.SettlementTransferID),
		session.FailureReason,
		session.FailureCode,
		session.Version-1, // expected version is pre-increment
	)
	if err != nil {
		return fmt.Errorf("settla-deposit-store: update session %s: %w", session.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("settla-deposit-store: session %s: %w", session.ID, deposit.ErrOptimisticLock)
	}

	// 2. Batch INSERT outbox entries
	if len(entries) > 0 {
		qtx := s.q.WithTx(tx)
		params := outboxEntriesToParams(entries)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-deposit-store: insert outbox entries for session %s: %w", session.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-deposit-store: commit tx for session %s: %w", session.ID, err)
	}
	return nil
}

// DispenseAddress obtains a deposit address from the pre-generated pool.
func (s *DepositStoreAdapter) DispenseAddress(ctx context.Context, tenantID uuid.UUID, chain string, sessionID uuid.UUID) (*domain.CryptoAddressPool, error) {
	row, err := s.q.DispensePoolAddress(ctx, DispensePoolAddressParams{
		TenantID:  tenantID,
		Chain:     chain,
		SessionID: pgtype.UUID{Bytes: sessionID, Valid: sessionID != uuid.Nil},
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no available addresses
		}
		return nil, fmt.Errorf("settla-deposit-store: dispensing address: %w", err)
	}
	return cryptoAddressPoolFromRow(row), nil
}

// CreateDepositTx records an on-chain transaction linked to a session.
func (s *DepositStoreAdapter) CreateDepositTx(ctx context.Context, dtx *domain.DepositTransaction) error {
	row, err := s.q.CreateDepositTransaction(ctx, CreateDepositTransactionParams{
		SessionID:       dtx.SessionID,
		TenantID:        dtx.TenantID,
		Chain:           dtx.Chain,
		TxHash:          dtx.TxHash,
		FromAddress:     dtx.FromAddress,
		ToAddress:       dtx.ToAddress,
		TokenContract:   dtx.TokenContract,
		Amount:          numericFromDecimal(dtx.Amount),
		BlockNumber:     dtx.BlockNumber,
		BlockHash:       dtx.BlockHash,
		Confirmations:   dtx.Confirmations,
		RequiredConfirm: dtx.RequiredConfirm,
	})
	if err != nil {
		return fmt.Errorf("settla-deposit-store: creating deposit tx: %w", err)
	}
	dtx.ID = row.ID
	dtx.CreatedAt = row.CreatedAt
	return nil
}

// GetSessionByTxHash retrieves a deposit session by looking up the transaction
// hash and then loading the associated session.
func (s *DepositStoreAdapter) GetSessionByTxHash(ctx context.Context, tenantID uuid.UUID, chain, txHash string) (*domain.DepositSession, error) {
	txRow, err := s.q.GetDepositTransactionByHash(ctx, GetDepositTransactionByHashParams{
		Chain:  chain,
		TxHash: txHash,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: tx %s:%s not found", chain, txHash)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting tx by hash: %w", err)
	}

	row, err := s.q.GetDepositSession(ctx, GetDepositSessionParams{
		ID:       txRow.SessionID,
		TenantID: tenantID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: session %s not found for tx %s:%s", txRow.SessionID, chain, txHash)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting session for tx %s:%s: %w", chain, txHash, err)
	}
	return depositSessionFromRow(row), nil
}

// GetDepositTxByHash retrieves a deposit transaction by chain and tx hash.
func (s *DepositStoreAdapter) GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error) {
	row, err := s.q.GetDepositTransactionByHash(ctx, GetDepositTransactionByHashParams{
		Chain:  chain,
		TxHash: txHash,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: tx %s:%s not found", chain, txHash)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting tx: %w", err)
	}
	return depositTxFromRow(row), nil
}

// ListSessionTxs retrieves all transactions for a session.
func (s *DepositStoreAdapter) ListSessionTxs(ctx context.Context, sessionID uuid.UUID) ([]domain.DepositTransaction, error) {
	rows, err := s.q.ListDepositTransactionsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("settla-deposit-store: listing txs: %w", err)
	}
	txs := make([]domain.DepositTransaction, len(rows))
	for i, row := range rows {
		txs[i] = *depositTxFromRow(row)
	}
	return txs, nil
}

// AccumulateReceived adds an amount to the session's received_amount.
func (s *DepositStoreAdapter) AccumulateReceived(ctx context.Context, tenantID, sessionID uuid.UUID, amount decimal.Decimal) error {
	return s.q.AccumulateReceivedAmount(ctx, AccumulateReceivedAmountParams{
		ID:       sessionID,
		TenantID: tenantID,
		ReceivedAmount: numericFromDecimal(amount),
	})
}

// GetExpiredPendingSessions returns sessions in PENDING_PAYMENT with expires_at < now().
func (s *DepositStoreAdapter) GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.DepositSession, error) {
	rows, err := s.q.GetExpiredPendingSessions(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("settla-deposit-store: getting expired sessions: %w", err)
	}
	sessions := make([]domain.DepositSession, len(rows))
	for i, row := range rows {
		sessions[i] = *depositSessionFromRow(row)
	}
	return sessions, nil
}

// GetSessionByIDOnly retrieves a deposit session by ID without tenant filtering.
func (s *DepositStoreAdapter) GetSessionByIDOnly(ctx context.Context, sessionID uuid.UUID) (*domain.DepositSession, error) {
	row, err := s.q.GetDepositSessionByIDOnly(ctx, sessionID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-deposit-store: session %s not found: %w", sessionID, err)
		}
		return nil, fmt.Errorf("settla-deposit-store: getting session %s: %w", sessionID, err)
	}
	return depositSessionPublicFromRow(row), nil
}

// depositSessionPublicFromRow converts a GetDepositSessionByIDOnlyRow to a domain session
// with only the fields returned by the public query.
func depositSessionPublicFromRow(row GetDepositSessionByIDOnlyRow) *domain.DepositSession {
	return &domain.DepositSession{
		ID:             row.ID,
		TenantID:       row.TenantID,
		Status:         domain.DepositSessionStatus(row.Status),
		Chain:          row.Chain,
		Token:          row.Token,
		DepositAddress: row.DepositAddress,
		ExpectedAmount: decimalFromNumeric(row.ExpectedAmount),
		ReceivedAmount: decimalFromNumeric(row.ReceivedAmount),
		Currency:       domain.Currency(row.Currency),
		ExpiresAt:      row.ExpiresAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

// ── Row conversion helpers ───────────────────────────────────────────────────

func depositSessionFromRow(row CryptoDepositSession) *domain.DepositSession {
	s := &domain.DepositSession{
		ID:               row.ID,
		TenantID:         row.TenantID,
		IdempotencyKey:   row.IdempotencyKey.String,
		Status:           domain.DepositSessionStatus(row.Status),
		Version:          row.Version,
		Chain:            row.Chain,
		Token:            row.Token,
		DepositAddress:   row.DepositAddress,
		ExpectedAmount:   decimalFromNumeric(row.ExpectedAmount),
		ReceivedAmount:   decimalFromNumeric(row.ReceivedAmount),
		Currency:         domain.Currency(row.Currency),
		CollectionFeeBPS: int(row.CollectionFeeBps),
		FeeAmount:        decimalFromNumeric(row.FeeAmount),
		NetAmount:        decimalFromNumeric(row.NetAmount),
		SettlementPref:   domain.SettlementPreference(row.SettlementPref),
		DerivationIndex:  row.DerivationIndex,
		ExpiresAt:        row.ExpiresAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		FailureReason:    row.FailureReason.String,
		FailureCode:      row.FailureCode.String,
	}
	if row.SettlementTransferID.Valid {
		id := uuid.UUID(row.SettlementTransferID.Bytes)
		s.SettlementTransferID = &id
	}
	if row.DetectedAt.Valid {
		t := row.DetectedAt.Time
		s.DetectedAt = &t
	}
	if row.ConfirmedAt.Valid {
		t := row.ConfirmedAt.Time
		s.ConfirmedAt = &t
	}
	if row.CreditedAt.Valid {
		t := row.CreditedAt.Time
		s.CreditedAt = &t
	}
	if row.SettledAt.Valid {
		t := row.SettledAt.Time
		s.SettledAt = &t
	}
	if row.ExpiredAt.Valid {
		t := row.ExpiredAt.Time
		s.ExpiredAt = &t
	}
	if row.FailedAt.Valid {
		t := row.FailedAt.Time
		s.FailedAt = &t
	}
	if row.Metadata != nil {
		if err := json.Unmarshal(row.Metadata, &s.Metadata); err != nil {
			slog.Warn("settla-deposit-store: failed to unmarshal metadata", "session_id", row.ID, "error", err)
		}
	}
	return s
}

func depositTxFromRow(row CryptoDepositTransaction) *domain.DepositTransaction {
	dtx := &domain.DepositTransaction{
		ID:              row.ID,
		SessionID:       row.SessionID,
		TenantID:        row.TenantID,
		Chain:           row.Chain,
		TxHash:          row.TxHash,
		FromAddress:     row.FromAddress,
		ToAddress:       row.ToAddress,
		TokenContract:   row.TokenContract,
		Amount:          decimalFromNumeric(row.Amount),
		BlockNumber:     row.BlockNumber,
		BlockHash:       row.BlockHash,
		Confirmations:   row.Confirmations,
		RequiredConfirm: row.RequiredConfirm,
		Confirmed:       row.Confirmed,
		DetectedAt:      row.DetectedAt,
		CreatedAt:       row.CreatedAt,
	}
	if row.ConfirmedAt.Valid {
		t := row.ConfirmedAt.Time
		dtx.ConfirmedAt = &t
	}
	return dtx
}

func cryptoAddressPoolFromRow(row CryptoAddressPool) *domain.CryptoAddressPool {
	addr := &domain.CryptoAddressPool{
		ID:              row.ID,
		TenantID:        row.TenantID,
		Chain:           row.Chain,
		Address:         row.Address,
		DerivationIndex: row.DerivationIndex,
		Dispensed:       row.Dispensed,
		CreatedAt:       row.CreatedAt,
	}
	if row.DispensedAt.Valid {
		t := row.DispensedAt.Time
		addr.DispensedAt = &t
	}
	if row.SessionID.Valid {
		id := uuid.UUID(row.SessionID.Bytes)
		addr.SessionID = &id
	}
	return addr
}
