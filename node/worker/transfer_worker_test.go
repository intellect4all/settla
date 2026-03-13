package worker

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// mockEngine records which methods were called and in what order.
type mockEngine struct {
	mu      sync.Mutex
	calls   []engineCall
	failOn  string // method name to return error for
}

type engineCall struct {
	method     string
	transferID uuid.UUID
}

func (m *mockEngine) record(method string, transferID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, engineCall{method: method, transferID: transferID})
	if m.failOn == method {
		return &testError{method: method}
	}
	return nil
}

func (m *mockEngine) getCalls() []engineCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]engineCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func (m *mockEngine) FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("FundTransfer", transferID)
}
func (m *mockEngine) InitiateOnRamp(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("InitiateOnRamp", transferID)
}
func (m *mockEngine) HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleOnRampResult", transferID)
}
func (m *mockEngine) HandleSettlementResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleSettlementResult", transferID)
}
func (m *mockEngine) HandleOffRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleOffRampResult", transferID)
}
func (m *mockEngine) CompleteTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("CompleteTransfer", transferID)
}
func (m *mockEngine) FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason, code string) error {
	return m.record("FailTransfer", transferID)
}

type testError struct {
	method string
}

func (e *testError) Error() string { return "test error in " + e.method }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestHandleEvent_RoutesToCorrectEngineMethod verifies that each event type
// is routed to the correct engine method.
func TestHandleEvent_RoutesToCorrectEngineMethod(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	tests := []struct {
		eventType      string
		expectedMethod string
	}{
		{domain.EventTransferCreated, "FundTransfer"},
		{domain.EventTransferFunded, "InitiateOnRamp"},
		{domain.EventOnRampCompleted, "HandleOnRampResult"},
		{domain.EventSettlementCompleted, "HandleSettlementResult"},
		{domain.EventOffRampCompleted, "HandleOffRampResult"},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			engine := &mockEngine{}
			w := &TransferWorker{
				partition: 0,
				engine:    engine,
				logger:    testLogger(),
			}

			event := domain.Event{
				ID:        uuid.New(),
				TenantID:  tenantID,
				Type:      tt.eventType,
				Timestamp: time.Now().UTC(),
				Data: &domain.Transfer{
					ID:       transferID,
					TenantID: tenantID,
				},
			}

			err := w.handleEvent(context.Background(), event)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			calls := engine.getCalls()
			if len(calls) != 1 {
				t.Fatalf("expected 1 engine call, got %d", len(calls))
			}
			if calls[0].method != tt.expectedMethod {
				t.Errorf("expected method %s, got %s", tt.expectedMethod, calls[0].method)
			}
			if calls[0].transferID != transferID {
				t.Errorf("expected transfer ID %s, got %s", transferID, calls[0].transferID)
			}
		})
	}
}

// TestHandleEvent_TransferFailedNoAction verifies that transfer.failed events
// are acknowledged without triggering engine actions.
func TestHandleEvent_TransferFailedNoAction(t *testing.T) {
	engine := &mockEngine{}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	transferID := uuid.New()
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.EventTransferFailed,
		Data: map[string]any{
			"transfer_id": transferID,
		},
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected no engine calls for transfer.failed, got %d", len(calls))
	}
}

// TestHandleEvent_UnhandledEventType verifies that unknown event types are
// silently skipped (acked).
func TestHandleEvent_UnhandledEventType(t *testing.T) {
	engine := &mockEngine{}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     "some.unknown.event",
		Data: &domain.Transfer{
			ID: uuid.New(),
		},
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for unknown event")
	}
}

// TestHandleEvent_EngineError verifies that engine errors are propagated
// so the subscriber can nack the message.
func TestHandleEvent_EngineError(t *testing.T) {
	engine := &mockEngine{failOn: "FundTransfer"}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.EventTransferCreated,
		Data: &domain.Transfer{
			ID: uuid.New(),
		},
	}

	err := w.handleEvent(context.Background(), event)
	if err == nil {
		t.Fatal("expected error from failed engine call")
	}
}

// TestHandleEvent_ExtractsTransferIDFromMap verifies ID extraction from
// map[string]any event data (used by some event publishers).
func TestHandleEvent_ExtractsTransferIDFromMap(t *testing.T) {
	engine := &mockEngine{}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	transferID := uuid.New()
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.EventTransferCreated,
		Data: map[string]any{
			"transfer_id": transferID,
		},
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].transferID != transferID {
		t.Errorf("expected transfer ID %s, got %s", transferID, calls[0].transferID)
	}
}

// TestHandleEvent_ExtractsTransferIDFromString verifies ID extraction when
// transfer_id is a string (e.g., after JSON deserialization).
func TestHandleEvent_ExtractsTransferIDFromString(t *testing.T) {
	engine := &mockEngine{}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	transferID := uuid.New()
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.EventTransferCreated,
		Data: map[string]any{
			"transfer_id": transferID.String(),
		},
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].transferID != transferID {
		t.Errorf("expected transfer ID %s, got %s", transferID, calls[0].transferID)
	}
}

// TestSagaOrdering verifies that a full saga (created->funded->onramp->settle->offramp->complete)
// routes events to the correct engine methods in order.
func TestSagaOrdering(t *testing.T) {
	engine := &mockEngine{}
	w := &TransferWorker{
		partition: 0,
		engine:    engine,
		logger:    testLogger(),
	}

	tenantID := uuid.New()
	transferID := uuid.New()
	transfer := &domain.Transfer{ID: transferID, TenantID: tenantID}

	sagaEvents := []string{
		domain.EventTransferCreated,
		domain.EventTransferFunded,
		domain.EventOnRampCompleted,
		domain.EventSettlementCompleted,
		domain.EventOffRampCompleted,
	}

	expectedMethods := []string{
		"FundTransfer",
		"InitiateOnRamp",
		"HandleOnRampResult",
		"HandleSettlementResult",
		"HandleOffRampResult",
	}

	for _, eventType := range sagaEvents {
		event := domain.Event{
			ID:        uuid.New(),
			TenantID:  tenantID,
			Type:      eventType,
			Timestamp: time.Now().UTC(),
			Data:      transfer,
		}
		if err := w.handleEvent(context.Background(), event); err != nil {
			t.Fatalf("event %s failed: %v", eventType, err)
		}
	}

	calls := engine.getCalls()
	if len(calls) != len(expectedMethods) {
		t.Fatalf("expected %d engine calls, got %d", len(expectedMethods), len(calls))
	}

	for i, expected := range expectedMethods {
		if calls[i].method != expected {
			t.Errorf("call %d: expected %s, got %s", i, expected, calls[i].method)
		}
		if calls[i].transferID != transferID {
			t.Errorf("call %d: expected transfer ID %s, got %s", i, transferID, calls[i].transferID)
		}
	}
}

// TestPartitionedRouting verifies that events for the same tenant always
// go to the same partition, and different tenants can go to different partitions.
func TestPartitionedRouting(t *testing.T) {
	numPartitions := 8
	tenantA := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	tenantB := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	partitionA := messaging.TenantPartition(tenantA, numPartitions)
	partitionB := messaging.TenantPartition(tenantB, numPartitions)

	// Same tenant always same partition
	for i := 0; i < 100; i++ {
		if messaging.TenantPartition(tenantA, numPartitions) != partitionA {
			t.Fatal("tenant A partition changed")
		}
		if messaging.TenantPartition(tenantB, numPartitions) != partitionB {
			t.Fatal("tenant B partition changed")
		}
	}

	// Verify subjects are correctly formed
	subjectA := messaging.PartitionSubject(partitionA, "transfer.created")
	subjectB := messaging.PartitionSubject(partitionB, "transfer.created")

	if partitionA == partitionB {
		if subjectA != subjectB {
			t.Error("same partition should produce same subject")
		}
	} else {
		if subjectA == subjectB {
			t.Error("different partitions should produce different subjects")
		}
	}

	t.Logf("tenant A (%s) -> partition %d", tenantA, partitionA)
	t.Logf("tenant B (%s) -> partition %d", tenantB, partitionB)
}
