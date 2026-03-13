package worker

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// webhookMockEngine extends mockEngine to also capture IntentResult values
// so we can verify success/failure details from webhook handlers.
type webhookMockEngine struct {
	mu      sync.Mutex
	calls   []webhookEngineCall
}

type webhookEngineCall struct {
	method     string
	transferID uuid.UUID
	result     *domain.IntentResult
}

func (m *webhookMockEngine) record(method string, transferID uuid.UUID, result *domain.IntentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, webhookEngineCall{method: method, transferID: transferID, result: result})
	return nil
}

func (m *webhookMockEngine) getCalls() []webhookEngineCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]webhookEngineCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func (m *webhookMockEngine) FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("FundTransfer", transferID, nil)
}
func (m *webhookMockEngine) InitiateOnRamp(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("InitiateOnRamp", transferID, nil)
}
func (m *webhookMockEngine) HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleOnRampResult", transferID, &result)
}
func (m *webhookMockEngine) HandleSettlementResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleSettlementResult", transferID, &result)
}
func (m *webhookMockEngine) HandleOffRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	return m.record("HandleOffRampResult", transferID, &result)
}
func (m *webhookMockEngine) CompleteTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	return m.record("CompleteTransfer", transferID, nil)
}
func (m *webhookMockEngine) FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason, code string) error {
	return m.record("FailTransfer", transferID, nil)
}

func inboundWebhookTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestInboundWebhook_OnRampCompleted(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-pending-1",
		Status: "pending",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "test-provider",
		ProviderRef: "ext-ref-123",
		Status:      "completed",
		TxHash:      "0xdeadbeef",
		TxType:      "onramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOnRampWebhook,
		Data:     &payload,
	}

	err := w.handleOnRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should have been called with success result
	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", calls[0].method)
	}
	if calls[0].transferID != transferID {
		t.Errorf("expected transfer ID %s, got %s", transferID, calls[0].transferID)
	}
	if !calls[0].result.Success {
		t.Error("expected success=true")
	}
	if calls[0].result.ProviderRef != "ext-ref-123" {
		t.Errorf("expected provider ref ext-ref-123, got %s", calls[0].result.ProviderRef)
	}
	if calls[0].result.TxHash != "0xdeadbeef" {
		t.Errorf("expected tx hash 0xdeadbeef, got %s", calls[0].result.TxHash)
	}

	// Store should have been updated
	storeCalls := store.getCalls()
	hasUpdate := false
	for _, c := range storeCalls {
		if c.method == "UpdateProviderTransaction" && c.txType == "onramp" {
			hasUpdate = true
		}
	}
	if !hasUpdate {
		t.Error("expected UpdateProviderTransaction call for onramp")
	}
}

func TestInboundWebhook_OnRampFailed(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-pending-2",
		Status: "pending",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "test-provider",
		ProviderRef: "ext-ref-456",
		Status:      "failed",
		Error:       "insufficient funds at provider",
		ErrorCode:   "PROVIDER_INSUFFICIENT_FUNDS",
		TxType:      "onramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOnRampWebhook,
		Data:     &payload,
	}

	err := w.handleOnRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", calls[0].method)
	}
	if calls[0].result.Success {
		t.Error("expected success=false")
	}
	if calls[0].result.Error != "insufficient funds at provider" {
		t.Errorf("expected error message, got %s", calls[0].result.Error)
	}
	if calls[0].result.ErrorCode != "PROVIDER_INSUFFICIENT_FUNDS" {
		t.Errorf("expected error code PROVIDER_INSUFFICIENT_FUNDS, got %s", calls[0].result.ErrorCode)
	}
}

func TestInboundWebhook_OffRampCompleted(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "offramp", &domain.ProviderTx{
		ID:     "tx-offramp-1",
		Status: "pending",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "offramp-provider",
		ProviderRef: "off-ext-789",
		Status:      "completed",
		TxType:      "offramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOffRampWebhook,
		Data:     &payload,
	}

	err := w.handleOffRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].method != "HandleOffRampResult" {
		t.Errorf("expected HandleOffRampResult, got %s", calls[0].method)
	}
	if !calls[0].result.Success {
		t.Error("expected success=true")
	}
	if calls[0].result.ProviderRef != "off-ext-789" {
		t.Errorf("expected provider ref off-ext-789, got %s", calls[0].result.ProviderRef)
	}
}

func TestInboundWebhook_DuplicateWebhook_AlreadyCompleted(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-done-1",
		Status: "completed",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "test-provider",
		ProviderRef: "ext-ref-dup",
		Status:      "completed",
		TxType:      "onramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOnRampWebhook,
		Data:     &payload,
	}

	err := w.handleOnRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Engine should NOT have been called — duplicate webhook
	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for duplicate webhook on already-completed tx")
	}
}

func TestInboundWebhook_DuplicateWebhook_AlreadyConfirmed(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "offramp", &domain.ProviderTx{
		ID:     "tx-confirmed-1",
		Status: "confirmed",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "offramp-provider",
		ProviderRef: "ext-ref-dup2",
		Status:      "completed",
		TxType:      "offramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOffRampWebhook,
		Data:     &payload,
	}

	err := w.handleOffRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for duplicate webhook on confirmed tx")
	}
}

func TestInboundWebhook_UnknownTransfer(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore() // empty — no transactions
	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "test-provider",
		ProviderRef: "ext-unknown",
		Status:      "completed",
		TxType:      "onramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOnRampWebhook,
		Data:     &payload,
	}

	err := w.handleOnRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for unknown transfer, got %v", err)
	}

	// Engine should NOT have been called
	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for unknown transfer")
	}
}

func TestInboundWebhook_MalformedPayload(t *testing.T) {
	engine := &webhookMockEngine{}
	store := newMockProviderTransferStore()

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	// Event with invalid data that can't be unmarshalled to ProviderWebhookPayload
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.EventProviderOnRampWebhook,
		Data:     "not a valid payload",
	}

	err := w.handleOnRampWebhook(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for malformed payload (ACK), got %v", err)
	}

	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for malformed payload")
	}
}

func TestInboundWebhook_EventRouting(t *testing.T) {
	store := newMockProviderTransferStore()
	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	// Unknown event type should be silently skipped
	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     "some.unknown.webhook.event",
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for unknown event type, got %v", err)
	}

	if len(engine.getCalls()) != 0 {
		t.Error("expected no engine calls for unknown event type")
	}
}

func TestInboundWebhook_EventRoutingOnRamp(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "onramp", &domain.ProviderTx{
		ID:     "tx-route-1",
		Status: "pending",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "test-provider",
		ProviderRef: "ext-route-1",
		Status:      "completed",
		TxType:      "onramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOnRampWebhook,
		Data:     &payload,
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].method != "HandleOnRampResult" {
		t.Errorf("expected HandleOnRampResult, got %s", calls[0].method)
	}
}

func TestInboundWebhook_EventRoutingOffRamp(t *testing.T) {
	transferID := uuid.New()
	tenantID := uuid.New()

	store := newMockProviderTransferStore()
	store.setTx(transferID, "offramp", &domain.ProviderTx{
		ID:     "tx-route-2",
		Status: "pending",
	})

	engine := &webhookMockEngine{}

	w := &InboundWebhookWorker{
		transferStore: store,
		engine:        engine,
		logger:        inboundWebhookTestLogger(),
	}

	payload := domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  "offramp-provider",
		ProviderRef: "ext-route-2",
		Status:      "completed",
		TxType:      "offramp",
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.EventProviderOffRampWebhook,
		Data:     &payload,
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := engine.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	if calls[0].method != "HandleOffRampResult" {
		t.Errorf("expected HandleOffRampResult, got %s", calls[0].method)
	}
}
