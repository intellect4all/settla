package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// AmountScale is the fixed-point scale for converting decimal amounts to
// TigerBeetle uint64 amounts. Matches Postgres NUMERIC(28,8).
const AmountScale int64 = 100_000_000 // 10^8

var amountScaleDec = decimal.NewFromInt(AmountScale)

// ID128 is a 128-bit identifier used by TigerBeetle for accounts and transfers.
type ID128 [16]byte

// AccountIDFromCode generates a deterministic 128-bit TigerBeetle account ID
// from a ledger account code using SHA-256(code)[:16].
func AccountIDFromCode(code string) ID128 {
	h := sha256.Sum256([]byte(code))
	var id ID128
	copy(id[:], h[:16])
	return id
}

// IDFromUUID converts a UUID to a 128-bit TigerBeetle ID.
func IDFromUUID(u uuid.UUID) ID128 {
	var id ID128
	copy(id[:], u[:])
	return id
}

// generateTransferID creates a deterministic 128-bit transfer ID from an entry
// ID and a line index, ensuring idempotency for replayed entries.
func generateTransferID(entryID uuid.UUID, index int) ID128 {
	data := fmt.Sprintf("%s:%d", entryID, index)
	h := sha256.Sum256([]byte(data))
	var id ID128
	copy(id[:], h[:16])
	return id
}

// DecimalToTBAmount converts a shopspring/decimal to a TigerBeetle uint64
// by multiplying by AmountScale. Truncates any fractional digits beyond 8 dp
// (the ledger's precision limit). Truncation — not rounding — ensures we never
// overstate amounts, which is standard practice in settlement systems.
// Returns an error if the value is negative.
func DecimalToTBAmount(d decimal.Decimal) (uint64, error) {
	truncated := d.Truncate(8)
	scaled := truncated.Mul(amountScaleDec)
	if scaled.IsNegative() {
		return 0, fmt.Errorf("settla-ledger: negative amount %s", d)
	}
	return uint64(scaled.IntPart()), nil
}

// TBAmountToDecimal converts a TigerBeetle uint64 back to decimal.
func TBAmountToDecimal(amount uint64) decimal.Decimal {
	return decimal.NewFromInt(int64(amount)).Div(amountScaleDec)
}

// TBAccount represents a TigerBeetle account.
type TBAccount struct {
	ID             ID128
	UserData128    ID128
	UserData64     uint64
	UserData32     uint32
	Ledger         uint32
	Code           uint16
	Flags          uint16
	DebitsPending  uint64
	DebitsPosted   uint64
	CreditsPending uint64
	CreditsPosted  uint64
}

// TBTransfer represents a TigerBeetle transfer.
type TBTransfer struct {
	ID              ID128
	DebitAccountID  ID128
	CreditAccountID ID128
	UserData128     ID128
	UserData64      uint64
	UserData32      uint32
	Amount          uint64
	Ledger          uint32
	Code            uint16
	Flags           uint16
}

// TBCreateResult represents the outcome of a create-account or create-transfer call.
type TBCreateResult struct {
	Index  uint32
	Result uint32 // 0 = success
}

// TigerBeetle result codes.
const (
	TBResultOK     uint32 = 0
	TBResultExists uint32 = 1
)

// TBFlagLinked links transfers so they succeed or fail atomically.
const TBFlagLinked uint16 = 1 << 0

// TBClient abstracts the TigerBeetle client so the backend can be tested
// without a running TigerBeetle cluster. The production adapter wraps
// tigerbeetle-go/pkg/client.
type TBClient interface {
	CreateAccounts(accounts []TBAccount) ([]TBCreateResult, error)
	CreateTransfers(transfers []TBTransfer) ([]TBCreateResult, error)
	LookupAccounts(ids []ID128) ([]TBAccount, error)
	Close()
}

// tbBackend implements the TigerBeetle write path.
type tbBackend struct {
	client TBClient
	ledger uint32 // TB ledger ID (partitions data at TB level)
	logger *slog.Logger
}

func newTBBackend(client TBClient, ledgerID uint32, logger *slog.Logger) *tbBackend {
	return &tbBackend{
		client: client,
		ledger: ledgerID,
		logger: logger.With("backend", "tigerbeetle"),
	}
}

// EnsureAccounts creates TigerBeetle accounts for the given codes.
// Idempotent: existing accounts are ignored.
func (tb *tbBackend) EnsureAccounts(ctx context.Context, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	accounts := make([]TBAccount, len(codes))
	for i, code := range codes {
		accounts[i] = TBAccount{
			ID:     AccountIDFromCode(code),
			Ledger: tb.ledger,
			Code:   1,
		}
	}
	results, err := tb.client.CreateAccounts(accounts)
	if err != nil {
		return fmt.Errorf("settla-ledger: creating TB accounts: %w", err)
	}
	for _, r := range results {
		if r.Result != TBResultOK && r.Result != TBResultExists {
			return fmt.Errorf("settla-ledger: creating TB account at index %d: result code %d", r.Index, r.Result)
		}
	}
	return nil
}

// PostEntries creates TigerBeetle transfers for a journal entry. Each
// debit-credit pair maps to a single TB transfer; multi-line entries use
// linked transfers for atomicity. Returns the generated TB transfer IDs
// for downstream sync.
func (tb *tbBackend) PostEntries(ctx context.Context, entry domain.JournalEntry) ([]ID128, error) {
	var debits, credits []domain.EntryLine
	for _, line := range entry.Lines {
		switch line.EntryType {
		case domain.EntryTypeDebit:
			debits = append(debits, line)
		case domain.EntryTypeCredit:
			credits = append(credits, line)
		}
	}

	transfers, err := tb.buildTransfers(entry, debits, credits)
	if err != nil {
		return nil, err
	}
	if len(transfers) == 0 {
		return nil, fmt.Errorf("settla-ledger: no transfers to create from entry %s", entry.ID)
	}

	results, err := tb.client.CreateTransfers(transfers)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: posting to TigerBeetle: %w", err)
	}
	for _, r := range results {
		if r.Result != TBResultOK && r.Result != TBResultExists {
			return nil, fmt.Errorf("settla-ledger: TB transfer at index %d failed: result code %d", r.Index, r.Result)
		}
	}

	ids := make([]ID128, len(transfers))
	for i, t := range transfers {
		ids[i] = t.ID
	}
	return ids, nil
}

// buildTransfers decomposes a balanced journal entry into TigerBeetle transfers.
//
// Algorithm: greedily match debits with credits. Each match produces one TB
// transfer. When a debit is larger than the current credit (or vice-versa),
// the remainder carries to the next line. All transfers except the last are
// linked for atomic execution.
func (tb *tbBackend) buildTransfers(entry domain.JournalEntry, debits, credits []domain.EntryLine) ([]TBTransfer, error) {
	var transfers []TBTransfer

	di, ci := 0, 0
	var dRem, cRem decimal.Decimal
	if len(debits) > 0 {
		dRem = debits[0].Amount
	}
	if len(credits) > 0 {
		cRem = credits[0].Amount
	}

	idempKey := AccountIDFromCode(entry.IdempotencyKey)
	idx := 0

	for di < len(debits) && ci < len(credits) {
		amount := dRem
		if cRem.LessThan(dRem) {
			amount = cRem
		}

		tbAmount, err := DecimalToTBAmount(amount)
		if err != nil {
			return nil, fmt.Errorf("settla-ledger: converting amount for pair %d: %w", idx, err)
		}

		transfers = append(transfers, TBTransfer{
			ID:              generateTransferID(entry.ID, idx),
			DebitAccountID:  AccountIDFromCode(debits[di].AccountCode),
			CreditAccountID: AccountIDFromCode(credits[ci].AccountCode),
			UserData128:     idempKey,
			Amount:          tbAmount,
			Ledger:          tb.ledger,
			Code:            1,
		})

		dRem = dRem.Sub(amount)
		cRem = cRem.Sub(amount)

		if dRem.IsZero() {
			di++
			if di < len(debits) {
				dRem = debits[di].Amount
			}
		}
		if cRem.IsZero() {
			ci++
			if ci < len(credits) {
				cRem = credits[ci].Amount
			}
		}
		idx++
	}

	// Link all but last transfer for atomicity.
	for i := 0; i < len(transfers)-1; i++ {
		transfers[i].Flags |= TBFlagLinked
	}

	return transfers, nil
}

// GetBalance returns the authoritative balance for an account code by
// looking up the TigerBeetle account. Returns credits_posted - debits_posted.
func (tb *tbBackend) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
	id := AccountIDFromCode(accountCode)
	accounts, err := tb.client.LookupAccounts([]ID128{id})
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-ledger: looking up TB account %s: %w", accountCode, err)
	}
	if len(accounts) == 0 {
		return decimal.Zero, fmt.Errorf("settla-ledger: account %s: %w", accountCode, domain.ErrAccountNotFound(accountCode))
	}
	acc := accounts[0]
	balance := TBAmountToDecimal(acc.CreditsPosted).Sub(TBAmountToDecimal(acc.DebitsPosted))
	return balance, nil
}

// tbTransferIDUint64 extracts the lower 64 bits of an ID128 for logging/tracking.
func tbTransferIDUint64(id ID128) uint64 {
	return binary.LittleEndian.Uint64(id[:8])
}