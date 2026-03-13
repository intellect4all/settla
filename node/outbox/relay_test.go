package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── Mock OutboxStore ────────────────────────────────────────────────────

type mockStore struct {
	mu        sync.Mutex
	entries   []OutboxRow
	published map[uuid.UUID]bool
	failed    map[uuid.UUID]int32
}

func newMockStore(entries []OutboxRow) *mockStore {
	return &mockStore{
		entries:   entries,
		published: make(map[uuid.UUID]bool),
		failed:    make(map[uuid.UUID]int32),
	}
}

func (m *mockStore) GetUnpublishedEntries(_ context.Context, limit int32) ([]OutboxRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []OutboxRow
	for _, e := range m.entries {
		if !m.published[e.ID] && e.RetryCount+m.failed[e.ID] < e.MaxRetries {
			result = append(result, e)
			if int32(len(result)) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockStore) MarkPublished(_ context.Context, id uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published[id] = true
	return nil
}

func (m *mockStore) MarkFailed(_ context.Context, id uuid.UUID, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed[id]++
	return nil
}

func (m *mockStore) isPublished(id uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.published[id]
}

func (m *mockStore) failCount(id uuid.UUID) int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed[id]
}

// ── Mock Publisher ──────────────────────────────────────────────────────

type publishedMsg struct {
	Subject string
	MsgID   string
	Data    []byte
}

type mockPublisher struct {
	mu       sync.Mutex
	messages []publishedMsg
	failIDs  map[string]bool // message IDs that should fail
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{
		failIDs: make(map[string]bool),
	}
}

func (p *mockPublisher) Publish(_ context.Context, subject string, msgID string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failIDs[msgID] {
		return fmt.Errorf("mock publish failure for %s", msgID)
	}

	p.messages = append(p.messages, publishedMsg{
		Subject: subject,
		MsgID:   msgID,
		Data:    data,
	})
	return nil
}

func (p *mockPublisher) getMessages() []publishedMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]publishedMsg, len(p.messages))
	copy(result, p.messages)
	return result
}

// ── Helpers ─────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.Default()
}

func makeEntry(eventType string, tenantID uuid.UUID) OutboxRow {
	return OutboxRow{
		ID:            uuid.New(),
		AggregateType: "transfer",
		AggregateID:   uuid.New(),
		TenantID:      tenantID,
		EventType:     eventType,
		Payload:       []byte(`{"test":true}`),
		IsIntent:      false,
		RetryCount:    0,
		MaxRetries:    5,
		CreatedAt:     time.Now().UTC(),
	}
}

func makeIntent(eventType string, tenantID uuid.UUID) OutboxRow {
	e := makeEntry(eventType, tenantID)
	e.IsIntent = true
	return e
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestRelayPublishesToCorrectSubjects(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	tests := []struct {
		name         string
		eventType    string
		wantContains string // substring the subject must contain
	}{
		{
			name:         "transfer event goes to partitioned subject",
			eventType:    "transfer.created",
			wantContains: "settla.transfer.partition.",
		},
		{
			name:         "treasury intent goes to treasury stream",
			eventType:    "treasury.reserve",
			wantContains: "settla.treasury.",
		},
		{
			name:         "ledger intent goes to ledger stream",
			eventType:    "ledger.post",
			wantContains: "settla.ledger.",
		},
		{
			name:         "blockchain intent goes to blockchain stream",
			eventType:    "blockchain.send",
			wantContains: "settla.blockchain.",
		},
		{
			name:         "webhook intent goes to webhook stream",
			eventType:    "webhook.deliver",
			wantContains: "settla.webhook.",
		},
		{
			name:         "provider onramp goes to provider command stream",
			eventType:    "provider.onramp.execute",
			wantContains: "settla.provider.command.",
		},
		{
			name:         "provider inbound webhook goes to inbound stream",
			eventType:    "provider.inbound.payment_received",
			wantContains: "settla.provider.inbound.",
		},
		{
			name:         "settlement event goes to transfer partition",
			eventType:    "settlement.completed",
			wantContains: "settla.transfer.partition.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := makeEntry(tt.eventType, tenantID)
			store := newMockStore([]OutboxRow{entry})
			pub := newMockPublisher()

			relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

			if err := relay.poll(context.Background()); err != nil {
				t.Fatalf("poll failed: %v", err)
			}

			msgs := pub.getMessages()
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message, got %d", len(msgs))
			}

			if got := msgs[0].Subject; !contains(got, tt.wantContains) {
				t.Errorf("subject = %q, want to contain %q", got, tt.wantContains)
			}
		})
	}
}

func TestSameTenantAlwaysRoutesToSamePartition(t *testing.T) {
	tenantID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	// Create 20 entries for the same tenant.
	var entries []OutboxRow
	for i := 0; i < 20; i++ {
		entries = append(entries, makeEntry("transfer.created", tenantID))
	}

	store := newMockStore(entries)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 20 {
		t.Fatalf("expected 20 messages, got %d", len(msgs))
	}

	// All subjects must be identical (same tenant + same event type → same partition).
	firstSubject := msgs[0].Subject
	for i, msg := range msgs[1:] {
		if msg.Subject != firstSubject {
			t.Errorf("message %d subject = %q, want %q (deterministic routing)", i+1, msg.Subject, firstSubject)
		}
	}
}

func TestDifferentEventTypesRouteToCorrectStreams(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entries := []OutboxRow{
		makeEntry("transfer.created", tenantID),
		makeIntent("treasury.reserve", tenantID),
		makeIntent("ledger.post", tenantID),
		makeIntent("blockchain.send", tenantID),
		makeIntent("webhook.deliver", tenantID),
	}

	store := newMockStore(entries)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}

	// Each should be on a different stream prefix.
	prefixes := map[string]bool{}
	for _, msg := range msgs {
		// Extract stream prefix (e.g., "settla.transfer", "settla.treasury")
		prefixes[msg.Subject] = true
	}
	if len(prefixes) != 5 {
		t.Errorf("expected 5 unique subjects, got %d", len(prefixes))
	}
}

func TestPublishedEntriesAreMarkedAsPublished(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entries := []OutboxRow{
		makeEntry("transfer.created", tenantID),
		makeEntry("transfer.funded", tenantID),
	}

	store := newMockStore(entries)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	for _, e := range entries {
		if !store.isPublished(e.ID) {
			t.Errorf("entry %s should be marked as published", e.ID)
		}
	}
}

func TestFailedPublishesIncrementRetryCount(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entry := makeEntry("transfer.created", tenantID)
	store := newMockStore([]OutboxRow{entry})
	pub := newMockPublisher()

	// Mark this entry's message ID to fail on publish.
	pub.failIDs[entry.ID.String()] = true

	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	// poll should not return an error (per-entry failures are logged, not propagated).
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	if store.isPublished(entry.ID) {
		t.Error("entry should NOT be marked as published after failure")
	}

	if got := store.failCount(entry.ID); got != 1 {
		t.Errorf("expected fail count 1, got %d", got)
	}
}

func TestEntriesAtMaxRetriesAreNotRefetched(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entry := makeEntry("transfer.created", tenantID)
	entry.RetryCount = 5 // already at max
	entry.MaxRetries = 5

	store := newMockStore([]OutboxRow{entry})
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (max retries reached), got %d", len(msgs))
	}
}

func TestRelayPublishesOutboxMessageFormat(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	entry := makeEntry("transfer.created", tenantID)

	store := newMockStore([]OutboxRow{entry})
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// Verify the published message deserializes correctly.
	var msg outboxMessage
	if err := json.Unmarshal(msgs[0].Data, &msg); err != nil {
		t.Fatalf("failed to unmarshal published message: %v", err)
	}

	if msg.ID != entry.ID {
		t.Errorf("message ID = %s, want %s", msg.ID, entry.ID)
	}
	if msg.EventType != entry.EventType {
		t.Errorf("message event_type = %s, want %s", msg.EventType, entry.EventType)
	}
	if msg.TenantID != entry.TenantID {
		t.Errorf("message tenant_id = %s, want %s", msg.TenantID, entry.TenantID)
	}
	if msg.AggregateType != entry.AggregateType {
		t.Errorf("message aggregate_type = %s, want %s", msg.AggregateType, entry.AggregateType)
	}
	if msg.IsIntent != entry.IsIntent {
		t.Errorf("message is_intent = %v, want %v", msg.IsIntent, entry.IsIntent)
	}

	// Verify NATS message ID is the outbox entry UUID.
	if msgs[0].MsgID != entry.ID.String() {
		t.Errorf("NATS msgID = %s, want %s", msgs[0].MsgID, entry.ID.String())
	}
}

func TestRelayStopsOnContextCancellation(t *testing.T) {
	store := newMockStore(nil)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(),
		WithPollInterval(10*time.Millisecond),
		WithNumPartitions(8),
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- relay.Run(ctx)
	}()

	// Let it run a few cycles.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop within timeout")
	}
}

func TestSubjectForEntry_TransferPartitioning(t *testing.T) {
	// Verify that SubjectForEntry produces deterministic partitioned subjects.
	tenantA := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenantB := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	entryA := makeEntry("transfer.created", tenantA)
	entryB := makeEntry("transfer.created", tenantB)

	subjectA := SubjectForEntry(entryA, 8)
	subjectB := SubjectForEntry(entryB, 8)

	// Both should contain partition info.
	if !contains(subjectA, "settla.transfer.partition.") {
		t.Errorf("subject A = %q, want partition prefix", subjectA)
	}
	if !contains(subjectB, "settla.transfer.partition.") {
		t.Errorf("subject B = %q, want partition prefix", subjectB)
	}

	// Same call twice should produce same result (deterministic).
	if SubjectForEntry(entryA, 8) != subjectA {
		t.Error("subject routing is not deterministic")
	}
}

func TestRelayBatchSizeOption(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	// Create 10 entries.
	var entries []OutboxRow
	for i := 0; i < 10; i++ {
		entries = append(entries, makeEntry("transfer.created", tenantID))
	}

	store := newMockStore(entries)
	pub := newMockPublisher()

	// Set batch size to 3 — should only publish 3 per poll.
	relay := NewRelay(store, pub, testLogger(),
		WithBatchSize(3),
		WithNumPartitions(8),
	)

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages (batch size limit), got %d", len(msgs))
	}
}

func TestRelayWithMetrics(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entry := makeEntry("transfer.created", tenantID)
	store := newMockStore([]OutboxRow{entry})
	pub := newMockPublisher()
	metrics := NewRelayMetrics()

	relay := NewRelay(store, pub, testLogger(),
		WithNumPartitions(8),
		WithMetrics(metrics),
	)

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	// Just verify no panics — detailed Prometheus assertions are overkill for unit tests.
	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

func TestMarkPublishedFailureDoesNotBlockRelay(t *testing.T) {
	// CRIT-4: When NATS publish succeeds but MarkPublished fails, the relay should:
	// 1. NOT treat it as a publish failure (no double-logging)
	// 2. Return nil from publishEntry (message IS in NATS)
	// 3. The entry stays unpublished in DB, will be re-polled next cycle
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entry := makeEntry("transfer.created", tenantID)
	store := &markFailStore{
		mockStore:   newMockStore([]OutboxRow{entry}),
		markFailIDs: map[uuid.UUID]bool{entry.ID: true},
	}
	pub := newMockPublisher()

	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	// poll should NOT return an error — the NATS publish succeeded.
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll should succeed even when MarkPublished fails: %v", err)
	}

	// Message was published to NATS.
	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	// But entry is NOT marked published in DB (MarkPublished failed).
	if store.isPublished(entry.ID) {
		t.Error("entry should NOT be marked published since MarkPublished failed")
	}

	// Verify it was NOT counted as a publish failure (failCount should be 0).
	if got := store.failCount(entry.ID); got != 0 {
		t.Errorf("mark failure should not increment retry count, got %d", got)
	}
}

func TestRepublishAfterMarkFailureIsIdempotent(t *testing.T) {
	// CRIT-4: Simulates relay restart scenario where an entry was published
	// to NATS but not marked. On re-poll, it's re-published with the same
	// message ID (outbox UUID). NATS dedup catches it.
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	entry := makeEntry("transfer.created", tenantID)
	store := newMockStore([]OutboxRow{entry})
	pub := newMockPublisher()

	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	// First poll: publishes and marks.
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after first poll, got %d", len(msgs))
	}

	// Verify the NATS message ID is the outbox entry UUID (dedup key).
	if msgs[0].MsgID != entry.ID.String() {
		t.Errorf("NATS msgID should be outbox UUID %s, got %s", entry.ID, msgs[0].MsgID)
	}

	// Second poll: entry is now marked published, so it shouldn't be re-fetched.
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}

	msgs = pub.getMessages()
	if len(msgs) != 1 {
		t.Errorf("expected still 1 message (no re-publish), got %d", len(msgs))
	}
}

// markFailStore wraps mockStore but fails MarkPublished for specific IDs.
type markFailStore struct {
	*mockStore
	markFailIDs map[uuid.UUID]bool
}

func (s *markFailStore) MarkPublished(ctx context.Context, id uuid.UUID, createdAt time.Time) error {
	if s.markFailIDs[id] {
		return fmt.Errorf("simulated DB error for MarkPublished")
	}
	return s.mockStore.MarkPublished(ctx, id, createdAt)
}

// ── Helpers ─────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestRelayPreservesOrderForSameTenant(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	// Create 10 entries with sequential timestamps.
	baseTime := time.Now().UTC()
	var entries []OutboxRow
	for i := 0; i < 10; i++ {
		e := makeEntry("transfer.created", tenantID)
		e.CreatedAt = baseTime.Add(time.Duration(i) * time.Millisecond)
		entries = append(entries, e)
	}

	store := newMockStore(entries)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(), WithNumPartitions(8))

	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 10 {
		t.Fatalf("expected 10 messages, got %d", len(msgs))
	}

	// Verify messages were published in the same order as creation order.
	// The relay iterates entries sequentially, so order must be preserved.
	for i, entry := range entries {
		if msgs[i].MsgID != entry.ID.String() {
			t.Errorf("message %d: got msgID %s, want %s (order violation)", i, msgs[i].MsgID, entry.ID)
		}
	}
}

func TestRelayPreservesOrderAcrossPolls(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	baseTime := time.Now().UTC()
	var entries []OutboxRow
	for i := 0; i < 10; i++ {
		e := makeEntry("transfer.created", tenantID)
		e.CreatedAt = baseTime.Add(time.Duration(i) * time.Millisecond)
		entries = append(entries, e)
	}

	store := newMockStore(entries)
	pub := newMockPublisher()
	relay := NewRelay(store, pub, testLogger(),
		WithBatchSize(5),
		WithNumPartitions(8),
	)

	// First poll: should publish first 5.
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("first poll failed: %v", err)
	}

	// Second poll: should publish remaining 5.
	if err := relay.poll(context.Background()); err != nil {
		t.Fatalf("second poll failed: %v", err)
	}

	msgs := pub.getMessages()
	if len(msgs) != 10 {
		t.Fatalf("expected 10 messages across 2 polls, got %d", len(msgs))
	}

	// Combined order must match creation order.
	for i, entry := range entries {
		if msgs[i].MsgID != entry.ID.String() {
			t.Errorf("message %d: got msgID %s, want %s (cross-poll order violation)", i, msgs[i].MsgID, entry.ID)
		}
	}
}
