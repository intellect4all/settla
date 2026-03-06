package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/ledgerdb"
)

// pgBackend implements the Postgres read path for the CQRS read model.
// It queries journal_entries, entry_lines, and balance_snapshots tables
// that are populated by the TigerBeetle → Postgres sync consumer.
type pgBackend struct {
	q      ledgerdb.Querier
	logger *slog.Logger
}

// NewPGBackend creates a Postgres read-path backend.
func NewPGBackend(q ledgerdb.Querier, logger *slog.Logger) *pgBackend {
	return &pgBackend{
		q:      q,
		logger: logger.With("backend", "postgres"),
	}
}

// GetEntries returns entry lines for an account within a time range.
// Resolves account code → account ID, then queries entry_lines with pagination.
func (pg *pgBackend) GetEntries(ctx context.Context, accountCode string, from, to time.Time, limit, offset int) ([]domain.EntryLine, error) {
	// Resolve account code to ID.
	account, err := pg.q.GetAccountByCode(ctx, accountCode)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: resolving account %s: %w", accountCode, err)
	}

	rows, err := pg.q.ListEntryLinesByAccountInDateRange(ctx, ledgerdb.ListEntryLinesByAccountInDateRangeParams{
		AccountID:   account.ID,
		CreatedAt:   from,
		CreatedAt_2: to,
		Limit:       int32(limit),
		Offset:      int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: querying entry lines for %s: %w", accountCode, err)
	}

	entries := make([]domain.EntryLine, len(rows))
	for i, row := range rows {
		entries[i] = domain.EntryLine{
			ID:          row.ID,
			AccountID:   row.AccountID,
			AccountCode: accountCode,
			EntryType:   domain.EntryType(row.EntryType),
			Amount:      numericToDecimal(row.Amount),
			Currency:    domain.Currency(row.Currency),
			Description: row.Description.String,
		}
	}
	return entries, nil
}

// GetJournalEntryWithLines loads a journal entry and its lines from Postgres.
// Used by ReverseEntry to build the reversal.
func (pg *pgBackend) GetJournalEntryWithLines(ctx context.Context, entryID uuid.UUID) (*domain.JournalEntry, error) {
	// journal_entries is partitioned by posted_at — we need to search.
	// Use idempotency key lookup or scan recent partitions.
	// For reversal, we use ListJournalEntriesByReference as a fallback pattern,
	// but the direct approach is to query entry_lines by journal_entry_id.
	lines, err := pg.q.ListEntryLinesByJournal(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: loading entry lines for %s: %w", entryID, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("settla-ledger: entry %s not found: %w", entryID, domain.ErrAccountNotFound(entryID.String()))
	}

	domainLines := make([]domain.EntryLine, len(lines))
	for i, line := range lines {
		// Resolve account ID → code for the reversal.
		account, err := pg.q.GetAccount(ctx, line.AccountID)
		if err != nil {
			return nil, fmt.Errorf("settla-ledger: resolving account %s: %w", line.AccountID, err)
		}
		domainLines[i] = domain.EntryLine{
			ID:          line.ID,
			AccountID:   line.AccountID,
			AccountCode: account.Code,
			EntryType:   domain.EntryType(line.EntryType),
			Amount:      numericToDecimal(line.Amount),
			Currency:    domain.Currency(line.Currency),
			Description: line.Description.String,
		}
	}

	return &domain.JournalEntry{
		ID:    entryID,
		Lines: domainLines,
	}, nil
}

// SyncJournalEntry writes a journal entry and its lines to Postgres.
// Called by the SyncConsumer to populate the read model from TigerBeetle events.
func (pg *pgBackend) SyncJournalEntry(ctx context.Context, entry domain.JournalEntry) error {
	// Write journal entry.
	var metadata []byte
	if entry.Metadata != nil {
		var err error
		metadata, err = json.Marshal(entry.Metadata)
		if err != nil {
			return fmt.Errorf("settla-ledger: marshalling metadata: %w", err)
		}
	}

	_, err := pg.q.CreateJournalEntry(ctx, ledgerdb.CreateJournalEntryParams{
		TenantID:       uuidToPgtype(entry.TenantID),
		IdempotencyKey: textToPgtype(entry.IdempotencyKey),
		EffectiveDate:  dateToPgtype(entry.EffectiveDate),
		Description:    entry.Description,
		ReferenceType:  textToPgtype(entry.ReferenceType),
		ReferenceID:    uuidToPgtype(entry.ReferenceID),
		ReversalOf:     uuidToPgtype(entry.ReversalOf),
		Metadata:       metadata,
	})
	if err != nil {
		return fmt.Errorf("settla-ledger: syncing journal entry %s: %w", entry.ID, err)
	}

	// Write entry lines.
	for _, line := range entry.Lines {
		// Resolve account code → ID for storage.
		accountID := line.AccountID
		if accountID == uuid.Nil && line.AccountCode != "" {
			account, err := pg.q.GetAccountByCode(ctx, line.AccountCode)
			if err != nil {
				pg.logger.Warn("account not found for sync, skipping line",
					"account_code", line.AccountCode,
					"entry_id", entry.ID,
					"error", err)
				continue
			}
			accountID = account.ID
		}

		_, err := pg.q.CreateEntryLine(ctx, ledgerdb.CreateEntryLineParams{
			JournalEntryID: entry.ID,
			AccountID:      accountID,
			EntryType:      string(line.EntryType),
			Amount:         decimalToNumeric(line.Amount),
			Currency:       string(line.Currency),
			Description:    textToPgtype(line.Description),
		})
		if err != nil {
			return fmt.Errorf("settla-ledger: syncing entry line for %s: %w", entry.ID, err)
		}
	}

	return nil
}

// SyncBalanceSnapshot upserts a balance snapshot from TigerBeetle account state.
func (pg *pgBackend) SyncBalanceSnapshot(ctx context.Context, accountCode string, balance decimal.Decimal, lastEntryID *uuid.UUID, version int64) error {
	account, err := pg.q.GetAccountByCode(ctx, accountCode)
	if err != nil {
		return fmt.Errorf("settla-ledger: resolving account %s for snapshot: %w", accountCode, err)
	}

	_, err = pg.q.UpsertBalanceSnapshot(ctx, ledgerdb.UpsertBalanceSnapshotParams{
		AccountID:   account.ID,
		Balance:     decimalToNumeric(balance),
		LastEntryID: uuidToPgtype(lastEntryID),
		Version:     version,
	})
	if err != nil {
		return fmt.Errorf("settla-ledger: upserting balance snapshot for %s: %w", accountCode, err)
	}

	return nil
}

// ── Type conversion helpers ──────────────────────────────────────────

// numericToDecimal converts a pgtype.Numeric to a shopspring/decimal.
func numericToDecimal(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil || n.NaN {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

// decimalToNumeric converts a shopspring/decimal to a pgtype.Numeric.
func decimalToNumeric(d decimal.Decimal) pgtype.Numeric {
	coeff := d.Coefficient()
	exp := d.Exponent()
	return pgtype.Numeric{
		Int:   new(big.Int).Set(coeff),
		Exp:   exp,
		Valid: true,
	}
}

func uuidToPgtype(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func textToPgtype(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: s, Valid: true}
}

func dateToPgtype(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{Valid: false}
	}
	return pgtype.Date{
		Time:  t,
		Valid: true,
	}
}
