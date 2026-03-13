package messaging

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/intellect4all/settla/domain"
)

// --- fake jetstream.Msg implementation ---

type fakeMsg struct {
	data     []byte
	acked    atomic.Bool
	naked    atomic.Bool
	nakDelay time.Duration
}

func (m *fakeMsg) Data() []byte                          { return m.data }
func (m *fakeMsg) Subject() string                       { return "test.subject" }
func (m *fakeMsg) Reply() string                         { return "" }
func (m *fakeMsg) Headers() nats.Header                  { return nil }
func (m *fakeMsg) Ack() error                            { m.acked.Store(true); return nil }
func (m *fakeMsg) DoubleAck(_ context.Context) error     { return nil }
func (m *fakeMsg) Nak() error                            { m.naked.Store(true); return nil }
func (m *fakeMsg) NakWithDelay(d time.Duration) error    { m.naked.Store(true); m.nakDelay = d; return nil }
func (m *fakeMsg) InProgress() error                     { return nil }
func (m *fakeMsg) Term() error                           { return nil }
func (m *fakeMsg) TermWithReason(_ string) error         { return nil }
func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: 1}, nil
}

func makeWireEvent(t *testing.T, tenantID uuid.UUID) []byte {
	t.Helper()
	w := wireEvent{
		ID:        uuid.Must(uuid.NewV7()),
		TenantID:  tenantID,
		EventType: "test.event",
		Payload:   json.RawMessage(`{"transfer_id":"` + uuid.Must(uuid.NewV7()).String() + `"}`),
		CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testLogger() *slog.Logger {
	return slog.Default()
}

// TestStreamSubscriber_PooledConcurrency verifies that N handlers run concurrently
// with pool size N.
func TestStreamSubscriber_PooledConcurrency(t *testing.T) {
	const poolSize = 4
	const msgCount = 8

	ss := &StreamSubscriber{
		poolSize:   poolSize,
		workerPool: make(chan struct{}, poolSize),
		streamName: "TEST",
		logger:     testLogger(),
	}

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	var processed atomic.Int32
	var wg sync.WaitGroup

	handler := func(_ context.Context, _ domain.Event) error {
		c := current.Add(1)
		for {
			old := maxConcurrent.Load()
			if c <= old || maxConcurrent.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		current.Add(-1)
		processed.Add(1)
		wg.Done()
		return nil
	}

	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())

	wg.Add(msgCount)
	for i := range msgCount {
		msg := &fakeMsg{data: makeWireEvent(t, tenantID)}
		_ = i
		ss.dispatchPooled(ctx, msg, handler)
	}

	wg.Wait()
	ss.inflight.Wait()

	if got := processed.Load(); got != msgCount {
		t.Fatalf("expected %d processed, got %d", msgCount, got)
	}

	if got := maxConcurrent.Load(); got < 2 {
		t.Fatalf("expected concurrent execution (max concurrent >= 2), got %d", got)
	}
	if got := maxConcurrent.Load(); got > int32(poolSize) {
		t.Fatalf("expected max concurrent <= pool size %d, got %d", poolSize, got)
	}

	t.Logf("max concurrent: %d (pool size: %d)", maxConcurrent.Load(), poolSize)
}

// TestStreamSubscriber_SerialDefault verifies that pool size 1 processes serially.
func TestStreamSubscriber_SerialDefault(t *testing.T) {
	const msgCount = 4

	ss := &StreamSubscriber{
		poolSize:   1,
		streamName: "TEST",
		logger:     testLogger(),
	}

	var maxConcurrent atomic.Int32
	var current atomic.Int32
	var processed atomic.Int32

	handler := func(_ context.Context, _ domain.Event) error {
		c := current.Add(1)
		for {
			old := maxConcurrent.Load()
			if c <= old || maxConcurrent.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
		processed.Add(1)
		return nil
	}

	ctx := context.Background()
	tenantID := uuid.Must(uuid.NewV7())

	for range msgCount {
		msg := &fakeMsg{data: makeWireEvent(t, tenantID)}
		// Serial path: handleMessage runs inline
		ss.handleMessage(ctx, msg, handler)
	}

	if got := processed.Load(); got != msgCount {
		t.Fatalf("expected %d processed, got %d", msgCount, got)
	}

	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("expected max concurrent = 1 (serial), got %d", got)
	}
}

// TestSubscriber_PerTenantOrdering verifies that events for the same tenant are
// serialized (per-tenant mutex), while different tenants can parallelize.
func TestSubscriber_PerTenantOrdering(t *testing.T) {
	const poolSize = 4
	const eventsPerTenant = 4

	s := &Subscriber{
		poolSize:   poolSize,
		workerPool: make(chan struct{}, poolSize),
		logger:     testLogger(),
	}

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())

	// Track per-tenant max concurrency
	type tenantTracker struct {
		current      atomic.Int32
		maxConcurent atomic.Int32
	}
	trackers := sync.Map{}
	getTracker := func(id string) *tenantTracker {
		v, _ := trackers.LoadOrStore(id, &tenantTracker{})
		return v.(*tenantTracker)
	}

	var crossTenantMax atomic.Int32
	var crossTenantCurrent atomic.Int32
	var processed atomic.Int32
	var wg sync.WaitGroup

	handler := func(_ context.Context, event domain.Event) error {
		tid := event.TenantID.String()
		tr := getTracker(tid)

		c := tr.current.Add(1)
		for {
			old := tr.maxConcurent.Load()
			if c <= old || tr.maxConcurent.CompareAndSwap(old, c) {
				break
			}
		}

		cc := crossTenantCurrent.Add(1)
		for {
			old := crossTenantMax.Load()
			if cc <= old || crossTenantMax.CompareAndSwap(old, cc) {
				break
			}
		}

		time.Sleep(30 * time.Millisecond)

		crossTenantCurrent.Add(-1)
		tr.current.Add(-1)
		processed.Add(1)
		wg.Done()
		return nil
	}

	ctx := context.Background()
	totalMsgs := eventsPerTenant * 2
	wg.Add(totalMsgs)

	// Interleave tenants to maximize chance of concurrent dispatch
	for i := range eventsPerTenant {
		msgA := &fakeMsg{data: makeWireEvent(t, tenantA)}
		msgB := &fakeMsg{data: makeWireEvent(t, tenantB)}
		_ = i
		s.dispatchPooled(ctx, msgA, handler, "SETTLA_TRANSFERS")
		s.dispatchPooled(ctx, msgB, handler, "SETTLA_TRANSFERS")
	}

	wg.Wait()
	s.inflight.Wait()

	if got := processed.Load(); got != int32(totalMsgs) {
		t.Fatalf("expected %d processed, got %d", totalMsgs, got)
	}

	// Each tenant must have max concurrency of 1 (serialized)
	for _, tid := range []string{tenantA.String(), tenantB.String()} {
		tr := getTracker(tid)
		if got := tr.maxConcurent.Load(); got != 1 {
			t.Errorf("tenant %s: expected max concurrent = 1, got %d", tid, got)
		}
	}

	// Cross-tenant concurrency should be > 1 (parallelized)
	if got := crossTenantMax.Load(); got < 2 {
		t.Errorf("expected cross-tenant concurrency >= 2, got %d", got)
	}

	t.Logf("cross-tenant max concurrent: %d", crossTenantMax.Load())
	for _, tid := range []string{tenantA.String(), tenantB.String()} {
		tr := getTracker(tid)
		t.Logf("tenant %s max concurrent: %d", tid[:8], tr.maxConcurent.Load())
	}
}

// Ensure fakeMsg satisfies jetstream.Msg at compile time.
var _ jetstream.Msg = (*fakeMsg)(nil)

