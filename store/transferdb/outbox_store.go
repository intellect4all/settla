package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// beginRepeatableRead starts a transaction with REPEATABLE READ isolation when
// the pool supports TxBeginnerWithOptions (i.e. *pgxpool.Pool), falling back to
// the default isolation otherwise. REPEATABLE READ prevents phantom reads during
// the concurrent UPDATE + INSERT pattern used by the outbox operations.
func beginRepeatableRead(ctx context.Context, pool TxBeginner) (pgx.Tx, error) {
	if p, ok := pool.(TxBeginnerWithOptions); ok {
		return p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	}
	return pool.Begin(ctx)
}

// TransitionWithOutbox atomically updates a transfer's status and inserts outbox entries
// in a single database transaction. Uses optimistic locking via version check.
// Returns domain.ErrOptimisticLock if the expected version does not match.
func (s *TransferStoreAdapter) TransitionWithOutbox(ctx context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-store: TransitionWithOutbox requires a TxBeginner (pool); adapter was created without one")
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Set RLS tenant context when appPool is configured and entries carry a tenant ID.
	if s.appPool != nil && len(entries) > 0 && entries[0].TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, entries[0].TenantID); err != nil {
			return fmt.Errorf("settla-store: set tenant context for transfer %s: %w", transferID, err)
		}
	}

	// 1. UPDATE transfer with optimistic lock + status-specific timestamps.
	// Cast $1 to TEXT explicitly so PostgreSQL doesn't get confused between
	// the enum assignment (SET status = $1::transfer_status_enum) and the
	// string comparisons in the CASE expressions.
	tag, err := tx.Exec(ctx,
		`UPDATE transfers
		 SET status = $1::transfer_status_enum,
		     version = version + 1,
		     updated_at = now(),
		     funded_at    = CASE WHEN $1::text = 'FUNDED'    THEN now() ELSE funded_at    END,
		     completed_at = CASE WHEN $1::text = 'COMPLETED' THEN now() ELSE completed_at END,
		     failed_at    = CASE WHEN $1::text = 'FAILED'    THEN now() ELSE failed_at    END
		 WHERE id = $2 AND version = $3`,
		string(newStatus), transferID, expectedVersion)
	if err != nil {
		return fmt.Errorf("settla-store: update transfer %s: %w", transferID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("settla-store: transfer %s: %w", transferID, core.ErrOptimisticLock)
	}

	// 2. Batch INSERT outbox entries using COPY for throughput.
	if len(entries) > 0 {
		params := outboxEntriesToParams(entries)
		qtx := s.q.WithTx(tx)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-store: insert outbox entries for transfer %s: %w", transferID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-store: commit tx for transfer %s: %w", transferID, err)
	}
	return nil
}

// CreateTransferWithOutbox atomically creates a transfer and inserts outbox entries
// in a single database transaction.
func (s *TransferStoreAdapter) CreateTransferWithOutbox(ctx context.Context, transfer *domain.Transfer, entries []domain.OutboxEntry) error {
	if s.pool == nil {
		return fmt.Errorf("settla-store: CreateTransferWithOutbox requires a TxBeginner (pool); adapter was created without one")
	}

	feesJSON, err := json.Marshal(transfer.Fees)
	if err != nil {
		return fmt.Errorf("settla-store: marshalling fees: %w", err)
	}

	var senderJSON, recipientJSON []byte

	if s.piiCrypto != nil {
		// Encrypt PII fields before storage.
		encSender, err := s.piiCrypto.EncryptSender(transfer.TenantID, transfer.Sender)
		if err != nil {
			return fmt.Errorf("settla-store: encrypting sender PII: %w", err)
		}
		senderJSON, err = json.Marshal(encSender)
		if err != nil {
			return fmt.Errorf("settla-store: marshalling encrypted sender: %w", err)
		}
		encRecipient, err := s.piiCrypto.EncryptRecipient(transfer.TenantID, transfer.Recipient)
		if err != nil {
			return fmt.Errorf("settla-store: encrypting recipient PII: %w", err)
		}
		recipientJSON, err = json.Marshal(encRecipient)
		if err != nil {
			return fmt.Errorf("settla-store: marshalling encrypted recipient: %w", err)
		}
	} else {
		senderJSON, err = json.Marshal(transfer.Sender)
		if err != nil {
			return fmt.Errorf("settla-store: marshalling sender: %w", err)
		}
		recipientJSON, err = json.Marshal(transfer.Recipient)
		if err != nil {
			return fmt.Errorf("settla-store: marshalling recipient: %w", err)
		}
	}

	tx, err := beginRepeatableRead(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("settla-store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Set RLS tenant context when appPool is configured.
	if s.appPool != nil && transfer.TenantID != uuid.Nil {
		if err := rls.SetTenantLocal(ctx, tx, transfer.TenantID); err != nil {
			return fmt.Errorf("settla-store: set tenant context for new transfer: %w", err)
		}
	}

	// 1. INSERT transfer within transaction.
	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateTransfer(ctx, CreateTransferParams{
		TenantID:          transfer.TenantID,
		ExternalRef:       textFromString(transfer.ExternalRef),
		IdempotencyKey:    textFromString(transfer.IdempotencyKey),
		Status:            TransferStatusEnum(transfer.Status),
		SourceCurrency:    string(transfer.SourceCurrency),
		SourceAmount:      numericFromDecimal(transfer.SourceAmount),
		DestCurrency:      string(transfer.DestCurrency),
		DestAmount:        numericFromDecimal(transfer.DestAmount),
		StableCoin:        textFromString(string(transfer.StableCoin)),
		StableAmount:      numericFromDecimal(transfer.StableAmount),
		Chain:             textFromString(transfer.Chain),
		FxRate:            numericFromDecimal(transfer.FXRate),
		Fees:              feesJSON,
		Sender:            senderJSON,
		Recipient:         recipientJSON,
		QuoteID:           uuidFromPtr(transfer.QuoteID),
		OnRampProviderID:  textFromString(transfer.OnRampProviderID),
		OffRampProviderID: textFromString(transfer.OffRampProviderID),
	})
	if err != nil {
		return fmt.Errorf("settla-store: creating transfer: %w", err)
	}

	transfer.ID = row.ID
	transfer.Version = row.Version
	transfer.CreatedAt = row.CreatedAt
	transfer.UpdatedAt = row.UpdatedAt

	// 2. Batch INSERT outbox entries.
	if len(entries) > 0 {
		// Re-marshal the transfer payload now that the DB has assigned the real ID.
		// The caller marshalled the payload before the INSERT, so it contains a stale
		// client-generated UUID. We must update both the aggregate ID and the payload.
		updatedPayload, err := json.Marshal(transfer)
		if err != nil {
			return fmt.Errorf("settla-store: re-marshalling transfer payload after ID assignment: %w", err)
		}

		for i := range entries {
			if entries[i].AggregateID == uuid.Nil {
				entries[i].AggregateID = transfer.ID
			}
			// Replace stale payload with one containing the real transfer ID.
			if entries[i].AggregateType == "transfer" && !entries[i].IsIntent {
				entries[i].Payload = updatedPayload
			}
		}
		params := outboxEntriesToParams(entries)
		if _, err := qtx.InsertOutboxEntries(ctx, params); err != nil {
			return fmt.Errorf("settla-store: insert outbox entries for new transfer: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-store: commit tx for new transfer: %w", err)
	}
	return nil
}

// outboxEntriesToParams converts domain.OutboxEntry slices to SQLC InsertOutboxEntriesParams.
func outboxEntriesToParams(entries []domain.OutboxEntry) []InsertOutboxEntriesParams {
	params := make([]InsertOutboxEntriesParams, len(entries))
	for i, e := range entries {
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		payload := e.Payload
		if payload == nil {
			payload = []byte("{}")
		}
		params[i] = InsertOutboxEntriesParams{
			ID:            e.ID,
			AggregateType: e.AggregateType,
			AggregateID:   e.AggregateID,
			TenantID:      e.TenantID,
			EventType:     e.EventType,
			Payload:       payload,
			IsIntent:      e.IsIntent,
			MaxRetries:    int32(e.MaxRetries),
			CreatedAt:     createdAt,
		}
	}
	return params
}
