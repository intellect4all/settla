package ledger

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
)

// mockPGSyncer records synced entries for test assertion.
type mockPGSyncer struct {
	mu      sync.Mutex
	entries []domain.JournalEntry
	failIDs map[uuid.UUID]bool // entry IDs that should fail
}

func newMockPGSyncer() *mockPGSyncer {
	return &mockPGSyncer{
		failIDs: make(map[uuid.UUID]bool),
	}
}

func (m *mockPGSyncer) SyncJournalEntry(_ context.Context, entry domain.JournalEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failIDs[entry.ID] {
		return errors.New("mock sync error")
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockPGSyncer) synced() []domain.JournalEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]domain.JournalEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

func makeJournalEntry() domain.JournalEntry {
	return domain.JournalEntry{
		ID:          uuid.New(),
		Description: "test entry",
		PostedAt:    time.Now().UTC(),
	}
}

func TestSyncConsumer_EnqueueAndFlush(t *testing.T) {
	mock := newMockPGSyncer()
	sc := newSyncConsumer(mock, 100, 10*time.Millisecond, slog.Default(), 100)

	sc.Start()

	for i := 0; i < 5; i++ {
		sc.Enqueue(makeJournalEntry())
	}

	time.Sleep(50 * time.Millisecond)
	sc.Stop()

	got := mock.synced()
	if len(got) != 5 {
		t.Fatalf("expected 5 synced entries, got %d", len(got))
	}
}

func TestSyncConsumer_BatchSizeFlush(t *testing.T) {
	mock := newMockPGSyncer()
	// Long interval so the timer won't fire; batch size 3 should trigger flush.
	sc := newSyncConsumer(mock, 3, 1*time.Second, slog.Default(), 100)

	sc.Start()

	for i := 0; i < 3; i++ {
		sc.Enqueue(makeJournalEntry())
	}

	time.Sleep(50 * time.Millisecond)
	sc.Stop()

	got := mock.synced()
	if len(got) != 3 {
		t.Fatalf("expected 3 synced entries (batch size trigger), got %d", len(got))
	}
}

func TestSyncConsumer_StopDrainsQueue(t *testing.T) {
	mock := newMockPGSyncer()
	sc := newSyncConsumer(mock, 100, 1*time.Second, slog.Default(), 100)

	// Enqueue entries directly into the channel before starting.
	for i := 0; i < 5; i++ {
		sc.queue <- makeJournalEntry()
	}

	sc.Start()
	sc.Stop()

	got := mock.synced()
	if len(got) != 5 {
		t.Fatalf("expected 5 synced entries after stop drain, got %d", len(got))
	}
}

func TestSyncConsumer_FlushErrorContinues(t *testing.T) {
	mock := newMockPGSyncer()
	sc := newSyncConsumer(mock, 100, 10*time.Millisecond, slog.Default(), 100)

	entries := make([]domain.JournalEntry, 5)
	for i := range entries {
		entries[i] = makeJournalEntry()
	}

	// Mark the third entry to fail.
	mock.failIDs[entries[2].ID] = true

	sc.Start()

	for _, e := range entries {
		sc.Enqueue(e)
	}

	time.Sleep(50 * time.Millisecond)
	sc.Stop()

	got := mock.synced()
	if len(got) != 4 {
		t.Fatalf("expected 4 synced entries (1 failure skipped), got %d", len(got))
	}
}
