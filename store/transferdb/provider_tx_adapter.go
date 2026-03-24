package transferdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/worker"
)

// ProviderTxAdapter implements worker.ProviderTransferStore backed by the
// provider_transactions table in Transfer DB. It uses the atomic UPSERT pattern
// (INSERT ON CONFLICT DO NOTHING) for ClaimProviderTransaction to guarantee
// exactly-once execution across multiple settla-node instances.
type ProviderTxAdapter struct {
	db   DBTX
	pool TxBeginner // used by SwitchRoute for transactional operations
}

// NewProviderTxAdapter creates a DB-backed provider transaction store.
func NewProviderTxAdapter(pool *pgxpool.Pool) *ProviderTxAdapter {
	return &ProviderTxAdapter{db: pool, pool: pool}
}

// txTypeToDB maps worker-side lowercase tx types to DB enum values.
func txTypeToDB(workerType string) string {
	switch workerType {
	case "onramp":
		return "ON_RAMP"
	case "offramp":
		return "OFF_RAMP"
	case "blockchain":
		return "BLOCKCHAIN"
	default:
		return workerType
	}
}

// ClaimProviderTransaction atomically claims a provider transaction slot.
// Returns non-nil UUID on success, nil if already claimed (conflict).
func (a *ProviderTxAdapter) ClaimProviderTransaction(ctx context.Context, params worker.ClaimProviderTransactionParams) (*uuid.UUID, error) {
	dbType := txTypeToDB(params.TxType)
	row := a.db.QueryRow(ctx, `
		INSERT INTO provider_transactions (
			tenant_id, provider, tx_type,
			transfer_id, status, amount, currency, metadata
		) VALUES ($1, $2, $3, $4, 'claiming', 0, '', '{}')
		ON CONFLICT (tenant_id, transfer_id, tx_type) DO NOTHING
		RETURNING id`,
		params.TenantID, params.Provider, dbType, params.TransferID,
	)

	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // Already claimed
		}
		return nil, fmt.Errorf("settla-provider-tx: claim for transfer %s type %s: %w",
			params.TransferID, params.TxType, err)
	}
	return &id, nil
}

// GetProviderTransaction returns the provider transaction for a transfer+type, or nil if not found.
// tenantID is included in the WHERE clause for tenant isolation.
func (a *ProviderTxAdapter) GetProviderTransaction(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, txType string) (*domain.ProviderTx, error) {
	dbType := txTypeToDB(txType)
	row := a.db.QueryRow(ctx, `
		SELECT id, external_id, status, amount, currency, tx_hash, metadata
		FROM provider_transactions
		WHERE transfer_id = $1 AND tx_type = $2 AND tenant_id = $3
		LIMIT 1`,
		transferID, dbType, tenantID,
	)

	var (
		id         uuid.UUID
		externalID pgtype.Text
		status     string
		amount     pgtype.Numeric
		currency   string
		txHash     pgtype.Text
		metadata   []byte
	)
	if err := row.Scan(&id, &externalID, &status, &amount, &currency, &txHash, &metadata); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-provider-tx: get for transfer %s type %s: %w",
			transferID, txType, err)
	}

	tx := &domain.ProviderTx{
		ID:       id.String(),
		Status:   status,
		Currency: domain.Currency(currency),
	}
	if externalID.Valid {
		tx.ExternalID = externalID.String
	}
	if txHash.Valid {
		tx.TxHash = txHash.String
	}
	if amount.Valid && amount.Int != nil {
		tx.Amount = decimal.NewFromBigInt(amount.Int, amount.Exp)
	}
	if len(metadata) > 0 && string(metadata) != "{}" {
		_ = json.Unmarshal(metadata, &tx.Metadata)
	}

	return tx, nil
}

// CreateProviderTransaction records a new provider transaction.
func (a *ProviderTxAdapter) CreateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	dbType := txTypeToDB(txType)
	metaJSON, _ := json.Marshal(tx.Metadata)
	if metaJSON == nil {
		metaJSON = []byte("{}")
	}

	_, err := a.db.Exec(ctx, `
		INSERT INTO provider_transactions (
			tenant_id, provider, tx_type, external_id,
			transfer_id, status, amount, currency,
			chain, tx_hash, metadata
		) VALUES (
			'00000000-0000-0000-0000-000000000000', '', $1, $2,
			$3, $4, $5, $6, NULL, $7, $8
		)`,
		dbType, nullText(tx.ExternalID),
		transferID, tx.Status, tx.Amount.String(), string(tx.Currency),
		nullText(tx.TxHash), metaJSON,
	)
	if err != nil {
		return fmt.Errorf("settla-provider-tx: create for transfer %s type %s: %w",
			transferID, txType, err)
	}
	return nil
}

// UpdateProviderTransaction updates an existing provider transaction.
// The WHERE clause excludes terminal statuses (completed, confirmed) to prevent
// race conditions when multiple webhook deliveries arrive for the same transfer.
// Returns domain.ErrOptimisticLock if no rows were affected (already advanced).
func (a *ProviderTxAdapter) UpdateProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	dbType := txTypeToDB(txType)
	metaJSON, _ := json.Marshal(tx.Metadata)
	if metaJSON == nil {
		metaJSON = []byte("{}")
	}

	tag, err := a.db.Exec(ctx, `
		UPDATE provider_transactions
		SET status = $3,
		    external_id = $4,
		    tx_hash = $5,
		    metadata = $6,
		    updated_at = now()
		WHERE transfer_id = $1 AND tx_type = $2
		  AND status NOT IN ('completed', 'confirmed')`,
		transferID, dbType, tx.Status,
		nullText(tx.ExternalID), nullText(tx.TxHash), metaJSON,
	)
	if err != nil {
		return fmt.Errorf("settla-provider-tx: update for transfer %s type %s: %w",
			transferID, txType, err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrOptimisticLock("provider_transaction", transferID.String())
	}
	return nil
}

// UpdateTransferRoute updates the provider IDs, chain, and stablecoin on a transfer
// during fallback routing. Empty string values are left unchanged (COALESCE with
// existing column value).
func (a *ProviderTxAdapter) UpdateTransferRoute(ctx context.Context, transferID uuid.UUID, onRampProvider, offRampProvider, chain string, stableCoin domain.Currency) error {
	_, err := a.db.Exec(ctx, `
		UPDATE transfers
		SET on_ramp_provider_id = COALESCE(NULLIF($2, ''), on_ramp_provider_id),
		    off_ramp_provider_id = COALESCE(NULLIF($3, ''), off_ramp_provider_id),
		    chain = COALESCE(NULLIF($4, ''), chain),
		    stablecoin = COALESCE(NULLIF($5, ''), stablecoin),
		    updated_at = now()
		WHERE id = $1`,
		transferID, onRampProvider, offRampProvider, chain, string(stableCoin),
	)
	if err != nil {
		return fmt.Errorf("settla-provider-tx: update transfer route for %s: %w", transferID, err)
	}
	return nil
}

// DeleteProviderTransaction removes a provider transaction record, allowing
// a fallback provider to re-claim the slot.
func (a *ProviderTxAdapter) DeleteProviderTransaction(ctx context.Context, transferID uuid.UUID, txType string) error {
	dbType := txTypeToDB(txType)
	_, err := a.db.Exec(ctx, `
		DELETE FROM provider_transactions
		WHERE transfer_id = $1 AND tx_type = $2`,
		transferID, dbType,
	)
	if err != nil {
		return fmt.Errorf("settla-provider-tx: delete for transfer %s type %s: %w", transferID, txType, err)
	}
	return nil
}

// SwitchRoute atomically deletes the current provider transaction and updates
// the transfer's route to use a fallback provider within a single DB transaction.
func (a *ProviderTxAdapter) SwitchRoute(ctx context.Context, transferID uuid.UUID, txType string,
	onRampProvider, offRampProvider, chain string, stableCoin domain.Currency) error {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settla-provider-tx: begin switch-route tx: %w", err)
	}
	defer tx.Rollback(ctx)

	scoped := &ProviderTxAdapter{db: tx}

	if err := scoped.DeleteProviderTransaction(ctx, transferID, txType); err != nil {
		return fmt.Errorf("settla-provider-tx: switch-route delete: %w", err)
	}
	if err := scoped.UpdateTransferRoute(ctx, transferID, onRampProvider, offRampProvider, chain, stableCoin); err != nil {
		return fmt.Errorf("settla-provider-tx: switch-route update: %w", err)
	}

	return tx.Commit(ctx)
}

func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// Compile-time interface check.
var _ worker.ProviderTransferStore = (*ProviderTxAdapter)(nil)
