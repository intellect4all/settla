package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// Batcher implements write-ahead batching for TigerBeetle.
//
// At 25K writes/sec, batching in 10ms windows means ~250 entries per batch,
// significantly reducing TB round-trips. Each individual entry still gets
// its own error response via the result channel.
//
// Flow:
//  1. Caller calls Submit(ctx, entry) — blocks until result is ready
//  2. Entry is added to the pending batch
//  3. After batchWindow elapses (or batch hits maxSize), all pending entries
//     are flushed to TigerBeetle in a single CreateTransfers call
//  4. Each caller's result channel receives the outcome
type Batcher struct {
	tb         *tbBackend
	window     time.Duration
	maxSize    int
	logger     *slog.Logger
	metrics    *observability.Metrics

	mu      sync.Mutex
	pending []batchItem
	timer   *time.Timer

	done chan struct{}
	wg   sync.WaitGroup
}

type batchItem struct {
	entry  domain.JournalEntry
	result chan error
}

func newBatcher(tb *tbBackend, window time.Duration, maxSize int, logger *slog.Logger, metrics *observability.Metrics) *Batcher {
	return &Batcher{
		tb:      tb,
		window:  window,
		maxSize: maxSize,
		logger:  logger.With("component", "batcher"),
		metrics: metrics,
		done:    make(chan struct{}),
	}
}

// Start begins the batcher. Must be called before Submit.
func (b *Batcher) Start() {
	// No background goroutine needed — flush is triggered by timer or maxSize.
}

// Stop flushes any pending entries and stops the batcher.
func (b *Batcher) Stop() {
	close(b.done)

	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	pending := b.pending
	b.pending = nil
	b.mu.Unlock()

	if len(pending) > 0 {
		b.flushBatch(pending)
	}

	b.wg.Wait()
}

// Submit adds an entry to the current batch and blocks until the batch is flushed.
// Returns the error from the TigerBeetle write for this specific entry.
func (b *Batcher) Submit(ctx context.Context, entry domain.JournalEntry) error {
	resultCh := make(chan error, 1)

	b.mu.Lock()

	// Check if stopped.
	select {
	case <-b.done:
		b.mu.Unlock()
		return fmt.Errorf("settla-ledger: batcher stopped")
	default:
	}

	b.pending = append(b.pending, batchItem{
		entry:  entry,
		result: resultCh,
	})

	shouldFlush := len(b.pending) >= b.maxSize

	if shouldFlush {
		// Max size reached — flush immediately.
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		pending := b.pending
		b.pending = nil
		b.mu.Unlock()

		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.flushBatch(pending)
		}()
	} else if b.timer == nil {
		// First item in a new batch — start the timer.
		b.timer = time.AfterFunc(b.window, func() {
			b.mu.Lock()
			pending := b.pending
			b.pending = nil
			b.timer = nil
			b.mu.Unlock()

			if len(pending) > 0 {
				b.wg.Add(1)
				go func() {
					defer b.wg.Done()
					b.flushBatch(pending)
				}()
			}
		})
		b.mu.Unlock()
	} else {
		b.mu.Unlock()
	}

	// Wait for result.
	select {
	case err := <-resultCh:
		return err
	case <-ctx.Done():
		return fmt.Errorf("settla-ledger: batch submit cancelled: %w", ctx.Err())
	}
}

// flushBatch posts all entries in the batch to TigerBeetle and distributes results.
func (b *Batcher) flushBatch(items []batchItem) {
	// Collect all TB transfers from all entries.
	type entryTransfers struct {
		startIdx int
		count    int
	}

	var allTransfers []TBTransfer
	entryMap := make([]entryTransfers, len(items))

	for i, item := range items {
		var debits, credits []domain.EntryLine
		for _, line := range item.entry.Lines {
			switch line.EntryType {
			case domain.EntryTypeDebit:
				debits = append(debits, line)
			case domain.EntryTypeCredit:
				credits = append(credits, line)
			}
		}

		transfers, err := b.tb.buildTransfers(item.entry, debits, credits)
		if err != nil {
			items[i].result <- fmt.Errorf("settla-ledger: building transfers: %w", err)
			continue
		}

		entryMap[i] = entryTransfers{
			startIdx: len(allTransfers),
			count:    len(transfers),
		}
		allTransfers = append(allTransfers, transfers...)
	}

	if len(allTransfers) == 0 {
		// All entries failed at build stage — results already sent.
		return
	}

	// Unlink transfers across entries (each entry's last transfer should be unlinked,
	// but we already set that in buildTransfers). However, we need to make sure
	// transfers from different entries are NOT linked to each other.
	// buildTransfers already handles linking within an entry, so we're fine.

	// Flush all transfers to TB in one call.
	results, err := b.tb.client.CreateTransfers(allTransfers)

	if b.metrics != nil {
		b.metrics.LedgerTBBatchSize.Observe(float64(len(items)))
	}

	b.logger.Debug("batch flushed to TB",
		"entries", len(items),
		"transfers", len(allTransfers))

	if err != nil {
		// TB call failed entirely — all entries fail.
		for _, item := range items {
			select {
			case item.result <- fmt.Errorf("settla-ledger: batch TB write failed: %w", err):
			default:
			}
		}
		return
	}

	// Build a map of failed transfer indices.
	failedIdx := make(map[uint32]uint32)
	for _, r := range results {
		if r.Result != TBResultOK && r.Result != TBResultExists {
			failedIdx[r.Index] = r.Result
		}
	}

	// Distribute results to individual callers.
	for i, item := range items {
		et := entryMap[i]
		if et.count == 0 {
			continue // Already got an error from build stage.
		}

		var entryErr error
		for j := et.startIdx; j < et.startIdx+et.count; j++ {
			if code, failed := failedIdx[uint32(j)]; failed {
				entryErr = fmt.Errorf("settla-ledger: TB transfer at index %d failed: result code %d", j, code)
				break
			}
		}

		select {
		case item.result <- entryErr:
		default:
		}
	}
}
