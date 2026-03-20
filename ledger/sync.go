package ledger

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// pgSyncer is the interface the sync consumer needs from the Postgres backend.
type pgSyncer interface {
	SyncJournalEntry(ctx context.Context, entry domain.JournalEntry) error
}

// SyncConsumer runs a background goroutine that syncs journal entries
// from TigerBeetle to Postgres. Entries are queued via Enqueue() after
// being posted to TB, and flushed in batches to PG at a configurable interval.
//
// This is the TB → PG sync path in the CQRS architecture. Postgres is the
// read model; TigerBeetle is the write authority.
type SyncConsumer struct {
	pg        pgSyncer
	batchSize int
	interval  time.Duration
	logger    *slog.Logger

	queue        chan domain.JournalEntry
	done         chan struct{}
	wg           sync.WaitGroup
	droppedTotal    atomic.Int64
	syncFailedTotal atomic.Int64
}

func newSyncConsumer(pg pgSyncer, batchSize int, interval time.Duration, logger *slog.Logger, queueSize int) *SyncConsumer {
	if queueSize <= 0 {
		queueSize = 50000
	}
	return &SyncConsumer{
		pg:        pg,
		batchSize: batchSize,
		interval:  interval,
		logger:    logger.With("component", "sync-consumer"),
		queue:     make(chan domain.JournalEntry, queueSize),
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
	queueLen := len(sc.queue)
	queueCap := cap(sc.queue)

	if queueCap > 0 {
		fillRatio := float64(queueLen) / float64(queueCap)
		observability.LedgerSyncQueueFillRatio.Set(fillRatio)
		if fillRatio > 0.8 {
			sc.logger.Warn("settla-ledger: sync queue >80% full",
				"queue_len", queueLen, "queue_cap", queueCap)
		}
	}

	select {
	case sc.queue <- entry:
	default:
		sc.droppedTotal.Add(1)
		sc.logger.Warn("settla-ledger: sync queue full, dropping entry",
			"entry_id", entry.ID,
			"queue_size", queueLen,
			"dropped_total", sc.droppedTotal.Load())
	}
}

// Pending returns the number of entries waiting to be synced.
func (sc *SyncConsumer) Pending() int {
	return len(sc.queue)
}

// Dropped returns the total number of entries dropped due to a full queue.
func (sc *SyncConsumer) Dropped() int64 {
	return sc.droppedTotal.Load()
}

// SyncFailedTotal returns the total number of entries that failed to sync after retries.
func (sc *SyncConsumer) SyncFailedTotal() int64 {
	return sc.syncFailedTotal.Load()
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
	var failed []domain.JournalEntry
	for _, entry := range batch {
		if err := sc.pg.SyncJournalEntry(ctx, entry); err != nil {
			sc.logger.Error("failed to sync entry to PG",
				"entry_id", entry.ID,
				"error", err)
			failed = append(failed, entry)
			continue
		}
		synced++
	}

	// Retry failed entries up to 2 more times with 100ms backoff
	for retry := 0; retry < 2 && len(failed) > 0; retry++ {
		time.Sleep(100 * time.Millisecond)
		var stillFailed []domain.JournalEntry
		for _, entry := range failed {
			if err := sc.pg.SyncJournalEntry(ctx, entry); err != nil {
				stillFailed = append(stillFailed, entry)
				continue
			}
			synced++
		}
		failed = stillFailed
	}

	if len(failed) > 0 {
		sc.syncFailedTotal.Add(int64(len(failed)))
		sc.logger.Error("entries failed after retries",
			"failed_count", len(failed),
			"failed_total", sc.syncFailedTotal.Load())

		// Re-queue failed entries back to the pending buffer so they are
		// retried on the next flush cycle instead of being silently lost.
		requeued := 0
		for _, entry := range failed {
			select {
			case sc.queue <- entry:
				requeued++
			default:
				// Queue is full — entry will need to be recovered via
				// TB → PG reconciliation.
				sc.droppedTotal.Add(1)
			}
		}
		if requeued > 0 {
			sc.logger.Warn("re-queued failed entries for next flush cycle",
				"requeued_count", requeued,
				"dropped_count", len(failed)-requeued)
		}
	}

	if synced > 0 {
		sc.logger.Debug("synced entries to PG",
			"synced", synced,
			"total", len(batch))
	}
}
