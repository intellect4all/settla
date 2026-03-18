package worker

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestParseDLQSubject(t *testing.T) {
	tests := []struct {
		subject    string
		wantStream string
		wantEvent  string
	}{
		{
			subject:    "settla.dlq.SETTLA_TRANSFERS.transfer.created",
			wantStream: "SETTLA_TRANSFERS",
			wantEvent:  "transfer.created",
		},
		{
			subject:    "settla.dlq.SETTLA_LEDGER.ledger.entry.created",
			wantStream: "SETTLA_LEDGER",
			wantEvent:  "ledger.entry.created",
		},
		{
			subject:    "settla.dlq.SETTLA_PROVIDERS.unmarshal_error",
			wantStream: "SETTLA_PROVIDERS",
			wantEvent:  "unmarshal_error",
		},
		{
			subject:    "settla.dlq.SETTLA_BLOCKCHAIN.blockchain.tx.confirmed",
			wantStream: "SETTLA_BLOCKCHAIN",
			wantEvent:  "blockchain.tx.confirmed",
		},
		{
			subject:    "unknown.subject",
			wantStream: "unknown",
			wantEvent:  "unknown",
		},
		{
			subject:    "settla.dlq.SETTLA_TREASURY",
			wantStream: "SETTLA_TREASURY",
			wantEvent:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			stream, event := parseDLQSubject(tt.subject)
			if stream != tt.wantStream {
				t.Errorf("parseDLQSubject(%q) stream = %q, want %q", tt.subject, stream, tt.wantStream)
			}
			if event != tt.wantEvent {
				t.Errorf("parseDLQSubject(%q) event = %q, want %q", tt.subject, event, tt.wantEvent)
			}
		})
	}
}

func TestExtractIDs(t *testing.T) {
	t.Run("valid JSON with both IDs", func(t *testing.T) {
		data := []byte(`{"id":"a0000000-0000-0000-0000-000000000001","tenant_id":"b0000000-0000-0000-0000-000000000002","event_type":"transfer.created"}`)
		tenantID, eventID := extractIDs(data)
		if tenantID != "b0000000-0000-0000-0000-000000000002" {
			t.Errorf("tenant_id = %q, want b0000000-...", tenantID)
		}
		if eventID != "a0000000-0000-0000-0000-000000000001" {
			t.Errorf("event_id = %q, want a0000000-...", eventID)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		data := []byte(`not json`)
		tenantID, eventID := extractIDs(data)
		if tenantID != "" || eventID != "" {
			t.Errorf("expected empty IDs for invalid JSON, got tenant=%q event=%q", tenantID, eventID)
		}
	})

	t.Run("empty JSON", func(t *testing.T) {
		data := []byte(`{}`)
		tenantID, eventID := extractIDs(data)
		if tenantID != "" || eventID != "" {
			t.Errorf("expected empty IDs for empty JSON, got tenant=%q event=%q", tenantID, eventID)
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestDLQMonitorRingBuffer(t *testing.T) {
	m := &DLQMonitor{
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}

	// Add entries and verify ordering.
	now := time.Now().UTC()
	for i := range 5 {
		m.mu.Lock()
		m.entries[m.head] = DLQEntry{
			ID:           fmt.Sprintf("entry-%d", i),
			SourceStream: "SETTLA_TRANSFERS",
			EventType:    "transfer.created",
			ReceivedAt:   now.Add(time.Duration(i) * time.Second),
		}
		m.head = (m.head + 1) % dlqMaxBufferSize
		m.count++
		m.totalReceived++
		m.bySourceStream["SETTLA_TRANSFERS"]++
		m.byEventType["transfer.created"]++
		m.mu.Unlock()
	}

	// List should return newest first.
	entries := m.ListEntries(0)
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
	if entries[0].ID != "entry-4" {
		t.Errorf("newest entry = %q, want entry-4", entries[0].ID)
	}
	if entries[4].ID != "entry-0" {
		t.Errorf("oldest entry = %q, want entry-0", entries[4].ID)
	}

	// List with limit.
	limited := m.ListEntries(2)
	if len(limited) != 2 {
		t.Fatalf("expected 2 entries with limit, got %d", len(limited))
	}
	if limited[0].ID != "entry-4" {
		t.Errorf("newest with limit = %q, want entry-4", limited[0].ID)
	}

	// Get by ID.
	found := m.GetEntry("entry-2")
	if found == nil {
		t.Fatal("expected to find entry-2")
	}
	if found.ID != "entry-2" {
		t.Errorf("GetEntry ID = %q, want entry-2", found.ID)
	}

	// Get non-existent.
	notFound := m.GetEntry("entry-99")
	if notFound != nil {
		t.Error("expected nil for non-existent entry")
	}

	// Stats.
	stats := m.Stats()
	if stats.TotalReceived != 5 {
		t.Errorf("TotalReceived = %d, want 5", stats.TotalReceived)
	}
	if stats.BufferedCount != 5 {
		t.Errorf("BufferedCount = %d, want 5", stats.BufferedCount)
	}
	if stats.BySourceStream["SETTLA_TRANSFERS"] != 5 {
		t.Errorf("BySourceStream[SETTLA_TRANSFERS] = %d, want 5", stats.BySourceStream["SETTLA_TRANSFERS"])
	}
}

func TestDLQMonitor_RingBufferOverflow(t *testing.T) {
	m := &DLQMonitor{
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}

	// Insert dlqMaxBufferSize+5 entries — buffer should keep only the newest dlqMaxBufferSize.
	totalInserted := dlqMaxBufferSize + 5
	for i := range totalInserted {
		m.mu.Lock()
		m.entries[m.head] = DLQEntry{
			ID:           fmt.Sprintf("entry-%d", i),
			SourceStream: "SETTLA_TRANSFERS",
			EventType:    "transfer.created",
			ReceivedAt:   time.Now().UTC(),
		}
		m.head = (m.head + 1) % dlqMaxBufferSize
		if m.count < dlqMaxBufferSize {
			m.count++
		}
		m.totalReceived++
		m.bySourceStream["SETTLA_TRANSFERS"]++
		m.byEventType["transfer.created"]++
		m.mu.Unlock()
	}

	if m.count != dlqMaxBufferSize {
		t.Errorf("count = %d, want %d", m.count, dlqMaxBufferSize)
	}

	entries := m.ListEntries(0)
	if len(entries) != dlqMaxBufferSize {
		t.Fatalf("ListEntries returned %d, want %d", len(entries), dlqMaxBufferSize)
	}

	// Newest should be the last entry inserted.
	newestID := fmt.Sprintf("entry-%d", totalInserted-1)
	if entries[0].ID != newestID {
		t.Errorf("newest = %q, want %s", entries[0].ID, newestID)
	}
	// Oldest should be entry-5 (first 5 were evicted by the overflow).
	if entries[dlqMaxBufferSize-1].ID != "entry-5" {
		t.Errorf("oldest = %q, want entry-5", entries[dlqMaxBufferSize-1].ID)
	}

	stats := m.Stats()
	if stats.TotalReceived != int64(totalInserted) {
		t.Errorf("TotalReceived = %d, want 1005", stats.TotalReceived)
	}
}

func TestDLQMonitor_Stats_Empty(t *testing.T) {
	m := &DLQMonitor{
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}

	stats := m.Stats()
	if stats.TotalReceived != 0 {
		t.Errorf("TotalReceived = %d, want 0", stats.TotalReceived)
	}
	if stats.BufferedCount != 0 {
		t.Errorf("BufferedCount = %d, want 0", stats.BufferedCount)
	}
	if stats.OldestEntry != nil {
		t.Errorf("OldestEntry should be nil, got %v", stats.OldestEntry)
	}
	if stats.NewestEntry != nil {
		t.Errorf("NewestEntry should be nil, got %v", stats.NewestEntry)
	}
	if len(stats.BySourceStream) != 0 {
		t.Errorf("BySourceStream should be empty, got %v", stats.BySourceStream)
	}
	if len(stats.ByEventType) != 0 {
		t.Errorf("ByEventType should be empty, got %v", stats.ByEventType)
	}
}

func TestDLQMonitor_ConcurrentAccess(t *testing.T) {
	m := &DLQMonitor{
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // writers + readers

	// 100 concurrent writers.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			m.mu.Lock()
			m.entries[m.head] = DLQEntry{
				ID:           fmt.Sprintf("concurrent-%d", idx),
				SourceStream: "SETTLA_TRANSFERS",
				EventType:    "transfer.created",
				ReceivedAt:   time.Now().UTC(),
			}
			m.head = (m.head + 1) % dlqMaxBufferSize
			if m.count < dlqMaxBufferSize {
				m.count++
			}
			m.totalReceived++
			m.bySourceStream["SETTLA_TRANSFERS"]++
			m.byEventType["transfer.created"]++
			m.mu.Unlock()
		}(i)
	}

	// 100 concurrent readers.
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = m.ListEntries(10)
			_ = m.Stats()
			_ = m.GetEntry("concurrent-0")
		}()
	}

	wg.Wait()

	// After all writes, count should equal goroutines.
	if m.count != goroutines {
		t.Errorf("count = %d, want %d", m.count, goroutines)
	}
}

func TestDLQMonitor_GetEntry_Evicted(t *testing.T) {
	m := &DLQMonitor{
		entries:        make([]DLQEntry, dlqMaxBufferSize),
		bySourceStream: make(map[string]int64),
		byEventType:    make(map[string]int64),
	}

	// Insert entry-0, then fill buffer with 1000 more entries to evict it.
	m.mu.Lock()
	m.entries[m.head] = DLQEntry{
		ID:           "entry-0",
		SourceStream: "SETTLA_TRANSFERS",
		EventType:    "transfer.created",
		ReceivedAt:   time.Now().UTC(),
	}
	m.head = (m.head + 1) % dlqMaxBufferSize
	m.count++
	m.totalReceived++
	m.mu.Unlock()

	for i := 1; i <= dlqMaxBufferSize; i++ {
		m.mu.Lock()
		m.entries[m.head] = DLQEntry{
			ID:           fmt.Sprintf("entry-%d", i),
			SourceStream: "SETTLA_TRANSFERS",
			EventType:    "transfer.created",
			ReceivedAt:   time.Now().UTC(),
		}
		m.head = (m.head + 1) % dlqMaxBufferSize
		if m.count < dlqMaxBufferSize {
			m.count++
		}
		m.totalReceived++
		m.mu.Unlock()
	}

	// entry-0 should have been evicted.
	found := m.GetEntry("entry-0")
	if found != nil {
		t.Errorf("expected nil for evicted entry, got %+v", found)
	}

	// entry-1000 should still be present (the newest).
	found = m.GetEntry(fmt.Sprintf("entry-%d", dlqMaxBufferSize))
	if found == nil {
		t.Error("expected to find newest entry")
	}
}

// Use fmt.Sprintf in test to keep import used.
var _ = fmt.Sprintf
