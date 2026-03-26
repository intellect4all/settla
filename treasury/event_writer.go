package treasury

import (
	"context"
	"time"

	"github.com/intellect4all/settla/domain"
)

// eventWriteInterval is how often the event writer drains the pendingEvents
// channel and batch-inserts events to the position_events table. 10ms at peak
// load (~20,000 events/sec) yields ~200 events per batch — one DB round-trip.
const eventWriteInterval = 10 * time.Millisecond

// eventWriteBatchSize is the maximum number of events to batch-insert per cycle.
// If more events are queued, they are carried over to the next cycle.
const eventWriteBatchSize = 1000

// startEventWriter launches the background goroutine that batch-writes position
// events to the database. Called by Manager.Start(). The writer exits when
// stopCh is closed and signals completion via eventWriterDone.
//
// If the store does not implement EventStore, the writer drains and discards
// events (they serve as audit records only when persisted).
func (m *Manager) startEventWriter() {
	go m.eventWriteLoop()
}

func (m *Manager) eventWriteLoop() {
	defer close(m.eventWriterDone)

	eventStore, hasEventStore := m.store.(EventStore)

	ticker := time.NewTicker(eventWriteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.drainAndWriteEvents(eventStore, hasEventStore)
		case <-m.stopCh:
			// Final drain before exit.
			m.drainAndWriteEvents(eventStore, hasEventStore)
			return
		}
	}
}

// drainAndWriteEvents collects all queued events and batch-inserts them.
func (m *Manager) drainAndWriteEvents(eventStore EventStore, hasEventStore bool) {
	batch := m.drainEventChannel()
	if len(batch) == 0 {
		return
	}

	if !hasEventStore {
		return // events drained and discarded — no persistent event store configured
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := eventStore.BatchWriteEvents(ctx, batch); err != nil {
		m.logger.Error("settla-treasury: event batch write failed",
			"batch_size", len(batch),
			"error", err,
		)
		// Re-queue events for retry on next cycle. If the channel is full,
		// newer events are silently dropped (best-effort audit log).
		for _, evt := range batch {
			select {
			case m.pendingEvents <- evt:
			default:
				// Channel full — drop oldest events silently.
				return
			}
		}
	}
}

// drainEventChannel collects up to eventWriteBatchSize events from the channel.
func (m *Manager) drainEventChannel() []domain.PositionEvent {
	batch := make([]domain.PositionEvent, 0, eventWriteBatchSize)
	for range eventWriteBatchSize {
		select {
		case evt := <-m.pendingEvents:
			batch = append(batch, evt)
		default:
			return batch
		}
	}
	return batch
}
