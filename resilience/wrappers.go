package resilience

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// Compile-time check.
var _ domain.Ledger = (*CircuitBreakerLedger)(nil)

// CircuitBreakerLedger wraps a domain.Ledger with circuit breaker protection.
// When the downstream ledger (typically TigerBeetle/Postgres) is failing, the
// circuit opens and rejects requests immediately instead of piling up.
type CircuitBreakerLedger struct {
	inner domain.Ledger
	cb    *CircuitBreaker
}

// NewCircuitBreakerLedger wraps a Ledger with a circuit breaker.
func NewCircuitBreakerLedger(inner domain.Ledger, cb *CircuitBreaker) *CircuitBreakerLedger {
	return &CircuitBreakerLedger{inner: inner, cb: cb}
}

func (l *CircuitBreakerLedger) PostEntries(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error) {
	var result *domain.JournalEntry
	err := l.cb.Execute(ctx, func(ctx context.Context) error {
		var e error
		result, e = l.inner.PostEntries(ctx, entry)
		return e
	})
	return result, err
}

func (l *CircuitBreakerLedger) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
	var result decimal.Decimal
	err := l.cb.Execute(ctx, func(ctx context.Context) error {
		var e error
		result, e = l.inner.GetBalance(ctx, accountCode)
		return e
	})
	return result, err
}

func (l *CircuitBreakerLedger) GetEntries(ctx context.Context, accountCode string, from, to time.Time, limit, offset int) ([]domain.EntryLine, error) {
	var result []domain.EntryLine
	err := l.cb.Execute(ctx, func(ctx context.Context) error {
		var e error
		result, e = l.inner.GetEntries(ctx, accountCode, from, to, limit, offset)
		return e
	})
	return result, err
}

func (l *CircuitBreakerLedger) ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*domain.JournalEntry, error) {
	var result *domain.JournalEntry
	err := l.cb.Execute(ctx, func(ctx context.Context) error {
		var e error
		result, e = l.inner.ReverseEntry(ctx, entryID, reason)
		return e
	})
	return result, err
}
