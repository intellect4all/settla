package ledger

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/intellect4all/settla/domain"
)

// SyncConsumer runs a background goroutine that syncs journal entries
// from TigerBeetle to Postgres. Entries are queued via Enqueue() after
// being posted to TB, and flushed in batches to PG at a configurable interval.
//
// This is the TB → PG sync path in the CQRS architecture. Postgres is the
// read model; TigerBeetle is the write authority.
type SyncConsumer struct {
	pg        *pgBackend
	batchSize int
	interval  time.Duration
	logger    *slog.Logger

	queue chan domain.JournalEntry
	done  chan struct{}
	wg    sync.WaitGroup
}

func newSyncConsumer(pg *pgBackend, batchSize int, interval time.Duration, logger *slog.Logger) *SyncConsumer {
	return &SyncConsumer{
		pg:        pg,
		batchSize: batchSize,
		interval:  interval,
		logger:    logger.With("component", "sync-consumer"),
		queue:     make(chan domain.JournalEntry, 10000),
		done:      make(chan struct{}),
	}
}

// Start begins the background sync goroutine.
func (sc *SyncConsumer) Start() {
	sc.wg.Add(1)
	go sc.run()
}

// Stop flushes remaining entries and stops the sync goroutine.
func (sc *SyncConsumer) Stop() {
	close(sc.done)
	sc.wg.Wait()
}

// Enqueue adds a journal entry to the sync queue. Non-blocking; if the queue
// is full, the entry is dropped with a warning (it can be recovered via
// TB → PG reconciliation).
func (sc *SyncConsumer) Enqueue(entry domain.JournalEntry) {
	select {
	case sc.queue <- entry:
	default:
		sc.logger.Warn("sync queue full, dropping entry",
			"entry_id", entry.ID,
			"queue_size", len(sc.queue))
	}
}

// Pending returns the number of entries waiting to be synced.
func (sc *SyncConsumer) Pending() int {
	return len(sc.queue)
}

func (sc *SyncConsumer) run() {
	defer sc.wg.Done()

	ticker := time.NewTicker(sc.interval)
	defer ticker.Stop()

	batch := make([]domain.JournalEntry, 0, sc.batchSize)

	for {
		select {
		case <-sc.done:
			// Final flush: drain remaining queue entries.
			sc.drainQueue(&batch)
			if len(batch) > 0 {
				sc.flush(batch)
			}
			return

		case entry := <-sc.queue:
			batch = append(batch, entry)
			if len(batch) >= sc.batchSize {
				sc.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				sc.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// drainQueue reads all remaining entries from the queue into the batch.
func (sc *SyncConsumer) drainQueue(batch *[]domain.JournalEntry) {
	for {
		select {
		case entry := <-sc.queue:
			*batch = append(*batch, entry)
		default:
			return
		}
	}
}

// flush writes a batch of journal entries to Postgres. On failure, entries
// are logged and can be recovered via TB → PG reconciliation.
func (sc *SyncConsumer) flush(batch []domain.JournalEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	synced := 0
	for _, entry := range batch {
		if err := sc.pg.SyncJournalEntry(ctx, entry); err != nil {
			sc.logger.Error("failed to sync entry to PG",
				"entry_id", entry.ID,
				"error", err)
			// Continue with remaining entries — don't fail the entire batch.
			continue
		}
		synced++
	}

	if synced > 0 {
		sc.logger.Debug("synced entries to PG",
			"synced", synced,
			"total", len(batch))
	}
}
