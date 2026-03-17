package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/chainmonitor"
)

// ── CheckpointStore adapter ─────────────────────────────────────────────────

// Compile-time interface check.
var _ chainmonitor.CheckpointStore = (*CheckpointStoreAdapter)(nil)

// CheckpointStoreAdapter implements chainmonitor.CheckpointStore using SQLC queries.
type CheckpointStoreAdapter struct {
	q *Queries
}

// NewCheckpointStoreAdapter creates a checkpoint store adapter.
func NewCheckpointStoreAdapter(q *Queries) *CheckpointStoreAdapter {
	return &CheckpointStoreAdapter{q: q}
}

// GetCheckpoint retrieves the block checkpoint for a chain. Returns nil if not found.
func (s *CheckpointStoreAdapter) GetCheckpoint(ctx context.Context, chain string) (*domain.BlockCheckpoint, error) {
	row, err := s.q.GetBlockCheckpoint(ctx, chain)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-checkpoint-store: getting checkpoint for %s: %w", chain, err)
	}
	return &domain.BlockCheckpoint{
		ID:          row.ID,
		Chain:       row.Chain,
		BlockNumber: row.BlockNumber,
		BlockHash:   row.BlockHash,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// UpsertCheckpoint creates or updates the block checkpoint for a chain.
func (s *CheckpointStoreAdapter) UpsertCheckpoint(ctx context.Context, chain string, blockNumber int64, blockHash string) error {
	_, err := s.q.UpsertBlockCheckpoint(ctx, UpsertBlockCheckpointParams{
		Chain:       chain,
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
	})
	if err != nil {
		return fmt.Errorf("settla-checkpoint-store: upserting checkpoint for %s: %w", chain, err)
	}
	return nil
}

// ── AddressStore adapter ────────────────────────────────────────────────────

// Compile-time interface check.
var _ chainmonitor.AddressStore = (*AddressStoreAdapter)(nil)

// AddressStoreAdapter implements chainmonitor.AddressStore using SQLC queries.
type AddressStoreAdapter struct {
	q *Queries
}

// NewAddressStoreAdapter creates an address store adapter.
func NewAddressStoreAdapter(q *Queries) *AddressStoreAdapter {
	return &AddressStoreAdapter{q: q}
}

// ListActiveAddresses returns addresses with active (non-terminal) deposit sessions.
func (s *AddressStoreAdapter) ListActiveAddresses(ctx context.Context, chain string) ([]chainmonitor.AddressInfo, error) {
	rows, err := s.q.ListPoolAddressesByTenant(ctx, ListPoolAddressesByTenantParams{
		TenantID: uuid.Nil, // We need all tenants; will use a different query
		Chain:    chain,
		Limit:    10000,
		Offset:   0,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-address-store: listing active addresses for %s: %w", chain, err)
	}

	// Note: In production we should have a dedicated query that joins dispensed
	// pool addresses with active sessions. For now, return all dispensed addresses.
	var infos []chainmonitor.AddressInfo
	for _, row := range rows {
		if !row.Dispensed {
			continue
		}
		infos = append(infos, chainmonitor.AddressInfo{
			Chain:    row.Chain,
			Address:  row.Address,
			TenantID: row.TenantID.String(),
		})
	}
	return infos, nil
}

// ── TokenStore adapter ──────────────────────────────────────────────────────

// Compile-time interface check.
var _ chainmonitor.TokenStore = (*TokenStoreAdapter)(nil)

// TokenStoreAdapter implements chainmonitor.TokenStore using SQLC queries.
type TokenStoreAdapter struct {
	q *Queries
}

// NewTokenStoreAdapter creates a token store adapter.
func NewTokenStoreAdapter(q *Queries) *TokenStoreAdapter {
	return &TokenStoreAdapter{q: q}
}

// ListTokensByChain returns all active tokens for a chain.
func (s *TokenStoreAdapter) ListTokensByChain(ctx context.Context, chain string) ([]domain.Token, error) {
	rows, err := s.q.ListTokensByChain(ctx, chain)
	if err != nil {
		return nil, fmt.Errorf("settla-token-store: listing tokens for %s: %w", chain, err)
	}

	tokens := make([]domain.Token, len(rows))
	for i, row := range rows {
		tokens[i] = domain.Token{
			ID:              row.ID,
			Chain:           row.Chain,
			Symbol:          row.Symbol,
			ContractAddress: row.ContractAddress,
			Decimals:        row.Decimals,
			IsActive:        row.IsActive,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		}
	}
	return tokens, nil
}

// ── OutboxWriter adapter ────────────────────────────────────────────────────

// Compile-time interface check.
var _ chainmonitor.OutboxWriter = (*OutboxWriterAdapter)(nil)

// OutboxWriterAdapter implements chainmonitor.OutboxWriter for the tron poller.
type OutboxWriterAdapter struct {
	q    *Queries
	pool TxBeginner
}

// NewOutboxWriterAdapter creates an outbox writer adapter.
func NewOutboxWriterAdapter(q *Queries, pool TxBeginner) *OutboxWriterAdapter {
	return &OutboxWriterAdapter{q: q, pool: pool}
}

// WriteDetectedTx atomically inserts a deposit transaction and outbox entries.
func (s *OutboxWriterAdapter) WriteDetectedTx(ctx context.Context, dtx *domain.DepositTransaction, entries []domain.OutboxEntry) error {
	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-outbox-writer: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := s.q.WithTx(tx)

	// INSERT deposit transaction
	row, err := qtx.CreateDepositTransaction(ctx, CreateDepositTransactionParams{
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
		return fmt.Errorf("settla-outbox-writer: creating deposit tx: %w", err)
	}
	dtx.ID = row.ID
	dtx.CreatedAt = row.CreatedAt

	// Batch INSERT outbox entries
	if len(entries) > 0 {
		for i := range entries {
			if entries[i].AggregateID == uuid.Nil {
				entries[i].AggregateID = dtx.SessionID
			}
			if entries[i].CreatedAt.IsZero() {
				entries[i].CreatedAt = time.Now().UTC()
			}
		}
		params := outboxEntriesToParams(entries)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-outbox-writer: inserting outbox entries: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-outbox-writer: commit tx: %w", err)
	}
	return nil
}

// GetDepositTxByHash retrieves a deposit transaction by chain and hash.
func (s *OutboxWriterAdapter) GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error) {
	row, err := s.q.GetDepositTransactionByHash(ctx, GetDepositTransactionByHashParams{
		Chain:  chain,
		TxHash: txHash,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-outbox-writer: getting deposit tx: %w", err)
	}
	return depositTxFromRow(row), nil
}

// GetSessionByAddress returns the active session for a deposit address.
// Uses the non-tenant-scoped query because the chain monitor discovers deposits
// on-chain and doesn't know the tenant_id until the session is found.
func (s *OutboxWriterAdapter) GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error) {
	row, err := s.q.GetDepositSessionByAddressOnly(ctx, address)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("settla-outbox-writer: no session for address %s", address)
		}
		return nil, fmt.Errorf("settla-outbox-writer: getting session by address: %w", err)
	}
	return depositSessionFromRow(row), nil
}
