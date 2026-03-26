package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/resilience"
)


// Compile-time check: Service implements domain.Ledger.
var _ domain.Ledger = (*Service)(nil)

// Service is the composite dual-backend ledger.
//
// Write path (PostEntries, ReverseEntry): delegates to TigerBeetle via tbBackend.
// TigerBeetle is the write authority and source of truth for balances.
//
// Read path (GetBalance): delegates to TigerBeetle for authoritative O(1) lookups.
//
// Query path (GetEntries): delegates to Postgres via pgBackend for rich queries,
// dashboards, and audit trails. Postgres is eventually consistent (~100ms lag).
//
// A background SyncConsumer tails entries posted to TB and populates the
// Postgres read model.
type Service struct {
	tb        *tbBackend
	pg        *pgBackend
	sync      *SyncConsumer
	batcher   *Batcher
	bulkhead  *resilience.Bulkhead
	publisher domain.EventPublisher
	logger    *slog.Logger
	metrics   *observability.Metrics
}

// NewService creates a dual-backend ledger service.
//
// If tbClient is nil, the service operates in stub mode (returns zero values)
// for use during development before TigerBeetle is wired.
func NewService(
	tbClient TBClient,
	pg *pgBackend,
	publisher domain.EventPublisher,
	logger *slog.Logger,
	metrics *observability.Metrics,
	opts ...Option,
) *Service {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	svcLogger := logger.With("module", "ledger.service")

	s := &Service{
		pg:        pg,
		publisher: publisher,
		logger:    svcLogger,
		metrics:   metrics,
	}

	if tbClient != nil {
		s.tb = newTBBackend(tbClient, cfg.tbLedgerID, svcLogger)

		if cfg.batchWindow > 0 {
			s.batcher = newBatcher(s.tb, cfg.batchWindow, cfg.batchMaxSize, svcLogger, metrics)
		}

		if cfg.bulkheadMax > 0 {
			s.bulkhead = resilience.NewBulkhead("tigerbeetle-writes", cfg.bulkheadMax)
		}
	}

	if pg != nil {
		s.sync = newSyncConsumer(pg, cfg.syncBatchSize, cfg.syncInterval, svcLogger, cfg.syncQueueSize)
	}

	return s
}

// Start begins background goroutines (sync consumer, batcher).
// Must be called after construction and before PostEntries.
func (s *Service) Start() {
	if s.sync != nil {
		s.sync.Start()
	}
	if s.batcher != nil {
		s.batcher.Start()
	}
}

// Stop gracefully shuts down background goroutines.
// Flushes pending sync entries and batched writes before returning.
func (s *Service) Stop() {
	if s.batcher != nil {
		s.batcher.Stop()
	}
	if s.sync != nil {
		s.sync.Stop()
	}
}

// PostEntries records a balanced set of postings as a journal entry.
//
// Flow:
//  1. Validate entries (domain.ValidateEntries — pure function)
//  2. Ensure TB accounts exist for all referenced account codes
//  3. Post to TigerBeetle (atomic, balanced by TB engine)
//  4. Queue entry for async PG sync (don't block the hot path)
//  5. Publish domain event
func (s *Service) PostEntries(ctx context.Context, entry domain.JournalEntry) (*domain.JournalEntry, error) {
	if err := domain.ValidateEntries(entry.Lines); err != nil {
		return nil, fmt.Errorf("settla-ledger: validating entries: %w", err)
	}

	// Assign ID and timestamp if not set.
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.PostedAt.IsZero() {
		entry.PostedAt = time.Now().UTC()
	}
	if entry.EffectiveDate.IsZero() {
		entry.EffectiveDate = entry.PostedAt
	}

	// Assign IDs to entry lines.
	for i := range entry.Lines {
		if entry.Lines[i].ID == uuid.Nil {
			entry.Lines[i].ID = uuid.New()
		}
	}

	if s.tb == nil {
		// Stub mode: no TigerBeetle, just return the entry.
		if s.metrics != nil {
			s.metrics.LedgerPostingsTotal.WithLabelValues(entry.ReferenceType).Inc()
		}
		return &entry, nil
	}

	// Ensure all referenced accounts exist in TigerBeetle.
	codes := make([]string, len(entry.Lines))
	for i, line := range entry.Lines {
		codes[i] = string(line.AccountCode)
	}
	if err := s.tb.EnsureAccounts(ctx, codes); err != nil {
		return nil, fmt.Errorf("settla-ledger: ensuring accounts: %w", err)
	}

	// Post to TigerBeetle (hot write path) with retry on transient failures.
	tbStart := time.Now()
	tbWrite := func(ctx context.Context) error {
		if s.batcher != nil {
			return s.batcher.Submit(ctx, entry)
		}
		_, err := s.tb.PostEntries(ctx, entry)
		return err
	}
	tbRetryConfig := resilience.RetryConfig{
		Operation:    "tigerbeetle-write",
		MaxAttempts:  3,
		InitialDelay: 50 * time.Millisecond,
		MaxDelay:     500 * time.Millisecond,
	}
	shouldRetryTB := func(err error) bool {
		return ctx.Err() == nil // retry on any non-context error
	}
	if s.bulkhead != nil {
		if err := resilience.Retry(ctx, tbRetryConfig, shouldRetryTB, func(ctx context.Context) error {
			return s.bulkhead.Execute(ctx, tbWrite)
		}); err != nil {
			return nil, fmt.Errorf("settla-ledger: TB write (bulkhead): %w", err)
		}
	} else {
		if err := resilience.Retry(ctx, tbRetryConfig, shouldRetryTB, tbWrite); err != nil {
			return nil, fmt.Errorf("settla-ledger: TB write: %w", err)
		}
	}
	if s.metrics != nil {
		s.metrics.LedgerTBWritesTotal.Inc()
		s.metrics.LedgerTBWriteLatency.Observe(time.Since(tbStart).Seconds())
		s.metrics.LedgerPostingsTotal.WithLabelValues(entry.ReferenceType).Inc()
	}

	// Queue for async Postgres sync (non-blocking).
	if s.sync != nil {
		beforeDropped := s.sync.Dropped()
		s.sync.Enqueue(entry)
		if s.sync.Dropped() > beforeDropped {
			s.metrics.LedgerSyncQueueDropped.Inc()
		}
		s.metrics.LedgerSyncQueueDepth.Set(float64(s.sync.Pending()))
	}

	s.logger.Info("settla-ledger: entry posted",
		"journal_entry_id", entry.ID,
		"reference_type", entry.ReferenceType,
		"lines", len(entry.Lines),
	)

	// Publish domain event.
	if s.publisher != nil {
		if err := s.publisher.Publish(ctx, domain.Event{
			ID:        uuid.New(),
			TenantID:  tenantIDOrNil(entry.TenantID),
			Type:      "ledger.entry.posted",
			Timestamp: time.Now().UTC(),
			Data:      entry.ID,
		}); err != nil {
			s.logger.Warn("settla-ledger: failed to publish entry posted event",
				"journal_entry_id", entry.ID,
				"error", err,
			)
		}
	}

	return &entry, nil
}

// GetBalance returns the authoritative balance for an account code.
// Reads directly from TigerBeetle (O(1) lookup, microsecond latency).
func (s *Service) GetBalance(ctx context.Context, accountCode string) (decimal.Decimal, error) {
	if s.tb == nil {
		return decimal.Zero, nil
	}
	balance, err := s.tb.GetBalance(ctx, accountCode)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-ledger: getting balance: %w", err)
	}
	return balance, nil
}

// GetEntries returns entry lines for an account within a time range.
// Reads from Postgres (rich query capability for dashboards and audit).
// Note: there may be slight lag (~100ms) vs TigerBeetle.
func (s *Service) GetEntries(ctx context.Context, accountCode string, from, to time.Time, limit, offset int) ([]domain.EntryLine, error) {
	if s.pg == nil {
		return nil, nil
	}
	entries, err := s.pg.GetEntries(ctx, accountCode, from, to, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: querying entries: %w", err)
	}
	return entries, nil
}

// ReverseEntry creates a reversal journal entry for the given entry ID.
// The reversal mirrors all lines with swapped debit/credit types and
// references the original entry.
func (s *Service) ReverseEntry(ctx context.Context, entryID uuid.UUID, reason string) (*domain.JournalEntry, error) {
	if s.pg == nil {
		return nil, fmt.Errorf("settla-ledger: postgres backend not available for reversal lookup")
	}

	// Load original entry from Postgres.
	original, err := s.pg.GetJournalEntryWithLines(ctx, entryID)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: loading entry for reversal: %w", err)
	}

	// Build reversal: swap debit ↔ credit on each line.
	reversalID := uuid.New()
	var reversalLines []domain.EntryLine
	for _, line := range original.Lines {
		reversedType := domain.EntryTypeDebit
		if line.EntryType == domain.EntryTypeDebit {
			reversedType = domain.EntryTypeCredit
		}
		reversalLines = append(reversalLines, domain.EntryLine{
			ID:        uuid.New(),
			AccountID: line.AccountID,
			Posting: domain.Posting{
				AccountCode: line.AccountCode,
				EntryType:   reversedType,
				Amount:      line.Amount,
				Currency:    line.Currency,
				Description: fmt.Sprintf("Reversal: %s", reason),
			},
		})
	}

	reversal := domain.JournalEntry{
		ID:             reversalID,
		TenantID:       original.TenantID,
		IdempotencyKey: domain.IdempotencyKey(fmt.Sprintf("reversal:%s", entryID)),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("Reversal of %s: %s", entryID, reason),
		ReferenceType:  "reversal",
		ReferenceID:    &entryID,
		ReversalOf:     &entryID,
		Lines:          reversalLines,
		Metadata:       map[string]string{"reason": reason},
	}

	result, err := s.PostEntries(ctx, reversal)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: posting reversal: %w", err)
	}

	return result, nil
}

func tenantIDOrNil(id *uuid.UUID) uuid.UUID {
	if id != nil {
		return *id
	}
	return uuid.Nil
}

// Option configures ledger Service behaviour.
type Option func(*config)

type config struct {
	tbLedgerID    uint32
	batchWindow   time.Duration
	batchMaxSize  int
	syncBatchSize int
	syncInterval  time.Duration
	syncQueueSize int
	bulkheadMax   int
}

func defaultConfig() config {
	return config{
		tbLedgerID:    1,
		batchWindow:   10 * time.Millisecond,
		batchMaxSize:  500,
		syncBatchSize: 1000,
		syncInterval:  100 * time.Millisecond,
		syncQueueSize: 50000,
		bulkheadMax:   100,
	}
}

// WithTBLedgerID sets the TigerBeetle ledger partition ID.
func WithTBLedgerID(id uint32) Option {
	return func(c *config) { c.tbLedgerID = id }
}

// WithBatchWindow sets the write-ahead batching window. 0 disables batching.
func WithBatchWindow(d time.Duration) Option {
	return func(c *config) { c.batchWindow = d }
}

// WithBatchMaxSize sets the maximum entries per batch.
func WithBatchMaxSize(n int) Option {
	return func(c *config) { c.batchMaxSize = n }
}

// WithSyncInterval sets the PG sync flush interval.
func WithSyncInterval(d time.Duration) Option {
	return func(c *config) { c.syncInterval = d }
}

// WithSyncQueueSize sets the sync consumer channel buffer size.
func WithSyncQueueSize(n int) Option {
	return func(c *config) { c.syncQueueSize = n }
}

// WithBulkhead sets the maximum concurrent TigerBeetle writes. 0 disables the bulkhead.
func WithBulkhead(maxConcurrent int) Option {
	return func(c *config) { c.bulkheadMax = maxConcurrent }
}

// WithNoBatching disables write-ahead batching (each PostEntries goes directly to TB).
func WithNoBatching() Option {
	return func(c *config) { c.batchWindow = 0 }
}
