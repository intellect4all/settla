package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// EntryType indicates whether an entry line is a debit or credit.
type EntryType string

const (
	// EntryTypeDebit represents a debit posting.
	EntryTypeDebit EntryType = "DEBIT"
	// EntryTypeCredit represents a credit posting.
	EntryTypeCredit EntryType = "CREDIT"
)

// Posting is a value object representing a single debit or credit against an account.
// It captures the minimal information needed to express one side of a balanced entry.
type Posting struct {
	AccountCode string
	EntryType   EntryType
	Amount      decimal.Decimal // Always positive; direction is determined by EntryType.
	Currency    Currency
	Description string
}

// EntryLine is a persisted posting within a journal entry, enriched with IDs.
type EntryLine struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Posting
}

// JournalEntry represents a balanced set of postings in the ledger.
// Every entry must balance: the sum of all debit amounts must equal
// the sum of all credit amounts per currency.
type JournalEntry struct {
	ID             uuid.UUID
	TenantID       *uuid.UUID // nil for system-level entries
	IdempotencyKey string
	PostedAt       time.Time
	EffectiveDate  time.Time
	Description    string
	ReferenceType  string     // e.g., "transfer", "fee", "reversal"
	ReferenceID    *uuid.UUID // ID of the referenced entity
	ReversedBy     *uuid.UUID // ID of entry that reversed this one
	ReversalOf     *uuid.UUID // ID of entry this one reverses
	Lines          []EntryLine
	Metadata       map[string]string
}

// Ledger records and queries double-entry journal postings.
//
// This interface is designed for a dual-backend CQRS pattern:
//
//   - Write path (PostEntries, ReverseEntry): implemented by TigerBeetleLedger,
//     which writes to TigerBeetle for the hot path at 1M+ TPS. TigerBeetle is
//     the write authority and source of truth for balances.
//
//   - Read/query path (GetBalance, GetEntries): implemented by PostgresLedger,
//     which reads from Postgres. The Postgres read model is populated by a
//     TB→PG sync consumer that tails TigerBeetle and writes to Postgres.
//
//   - A composite Service delegates writes to TigerBeetle and reads to Postgres,
//     presenting a unified interface to callers.
//
// Callers should depend only on this interface and remain unaware of the
// underlying backend split.
type Ledger interface {
	// PostEntries records a balanced journal entry (sum of debits must equal sum of credits).
	// TigerBeetle enforces the balance invariant at the engine level.
	PostEntries(ctx context.Context, entry JournalEntry) (*JournalEntry, error)

	// GetBalance returns the current balance for an account code.
	// TigerBeetle provides authoritative O(1) balance lookups.
	GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error)

	// GetEntries returns entry lines for an account within a time range, with pagination.
	// Postgres handles rich queries for dashboards and audit trails.
	GetEntries(ctx context.Context, accountCode string, from, to time.Time, limit, offset int) ([]EntryLine, error)

	// ReverseEntry creates a new journal entry that reverses the original entry.
	// The reversal entry has mirrored debits/credits and references the original.
	ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*JournalEntry, error)
}

// ValidateEntries is a pure function that validates a set of entry lines.
// It checks:
//   - At least 2 lines exist
//   - All amounts are positive
//   - Debits equal credits per currency
//   - No duplicate line IDs
func ValidateEntries(lines []EntryLine) error {
	if len(lines) < 2 {
		return ErrLedgerImbalance(fmt.Sprintf("need at least 2 lines, got %d", len(lines)))
	}

	// Check all amounts are positive, collect IDs for duplicate check,
	// and detect duplicate account+entry-type combinations within the same entry.
	seenIDs := make(map[uuid.UUID]bool, len(lines))
	seenAccountEntry := make(map[string]bool, len(lines))
	for _, line := range lines {
		if !line.Amount.IsPositive() {
			return ErrLedgerImbalance(fmt.Sprintf("line amount must be positive, got %s", line.Amount))
		}
		if line.ID != uuid.Nil {
			if seenIDs[line.ID] {
				return ErrLedgerImbalance(fmt.Sprintf("duplicate line ID %s", line.ID))
			}
			seenIDs[line.ID] = true
		}
		aeKey := line.AccountCode + ":" + string(line.EntryType)
		if seenAccountEntry[aeKey] {
			return ErrLedgerImbalance(fmt.Sprintf("duplicate account %s with entry type %s", line.AccountCode, line.EntryType))
		}
		seenAccountEntry[aeKey] = true
	}

	// Check debits == credits per currency
	type balance struct {
		debits  decimal.Decimal
		credits decimal.Decimal
	}
	byCurrency := make(map[Currency]*balance)
	for _, line := range lines {
		b, ok := byCurrency[line.Currency]
		if !ok {
			b = &balance{debits: decimal.Zero, credits: decimal.Zero}
			byCurrency[line.Currency] = b
		}
		switch line.EntryType {
		case EntryTypeDebit:
			b.debits = b.debits.Add(line.Amount)
		case EntryTypeCredit:
			b.credits = b.credits.Add(line.Amount)
		}
	}

	for currency, b := range byCurrency {
		if !b.debits.Equal(b.credits) {
			return ErrLedgerImbalance(fmt.Sprintf("%s debits %s != credits %s",
				currency, b.debits.StringFixed(2), b.credits.StringFixed(2)))
		}
	}

	return nil
}
