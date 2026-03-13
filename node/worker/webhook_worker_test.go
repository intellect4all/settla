package worker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/resilience"
)

// mockTenantWebhookStore implements TenantWebhookStore for testing.
type mockTenantWebhookStore struct {
	tenants map[uuid.UUID]*domain.Tenant
}

func newMockTenantWebhookStore() *mockTenantWebhookStore {
	return &mockTenantWebhookStore{
		tenants: make(map[uuid.UUID]*domain.Tenant),
	}
}

func (m *mockTenantWebhookStore) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	tenant, ok := m.tenants[tenantID]
	if !ok {
		return nil, &tenantNotFoundError{tenantID: tenantID}
	}
	return tenant, nil
}

type tenantNotFoundError struct {
	tenantID uuid.UUID
}

func (e *tenantNotFoundError) Error() string {
	return "tenant not found: " + e.tenantID.String()
}

func webhookTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func webhookTestCB() *resilience.CircuitBreaker {
	return resilience.NewCircuitBreaker("webhook-test")
}

func TestWebhookWorker_DeliverSuccess(t *testing.T) {
	tenantID := uuid.New()
	transferID := uuid.New()
	secret := "test-webhook-secret"

	var receivedBody []byte
	var receivedSignature string
	var receivedEvent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedSignature = r.Header.Get("X-Settla-Signature")
		receivedEvent = r.Header.Get("X-Settla-Event")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := newMockTenantWebhookStore()
	store.tenants[tenantID] = &domain.Tenant{
		ID:            tenantID,
		WebhookURL:    server.URL,
		WebhookSecret: secret,
	}

	w := &WebhookWorker{
		tenantStore: store,
		httpClient:  server.Client(),
		cb:          webhookTestCB(),
		logger:      webhookTestLogger(),
	}

	payload := domain.WebhookDeliverPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		EventType:  domain.EventTransferCompleted,
		Data:       []byte(`{"status":"COMPLETED"}`),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentWebhookDeliver,
		Data:     &payload,
	}

	err := w.handleDeliver(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify signature
	if receivedSignature == "" {
		t.Fatal("expected X-Settla-Signature header")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if receivedSignature != expectedSig {
		t.Errorf("signature mismatch: got %s, want %s", receivedSignature, expectedSig)
	}

	// Verify event type header
	if receivedEvent != domain.EventTransferCompleted {
		t.Errorf("expected event header %s, got %s", domain.EventTransferCompleted, receivedEvent)
	}

	// Verify body is valid JSON
	var webhookBody WebhookPayload
	if err := json.Unmarshal(receivedBody, &webhookBody); err != nil {
		t.Fatalf("failed to unmarshal webhook body: %v", err)
	}
	if webhookBody.TransferID != transferID.String() {
		t.Errorf("expected transfer_id %s, got %s", transferID, webhookBody.TransferID)
	}
	if webhookBody.EventType != domain.EventTransferCompleted {
		t.Errorf("expected event_type %s, got %s", domain.EventTransferCompleted, webhookBody.EventType)
	}
}

func TestWebhookWorker_DeliverNon2xx_ReturnsError(t *testing.T) {
	tenantID := uuid.New()
	transferID := uuid.New()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := newMockTenantWebhookStore()
	store.tenants[tenantID] = &domain.Tenant{
		ID:            tenantID,
		WebhookURL:    server.URL,
		WebhookSecret: "secret",
	}

	w := &WebhookWorker{
		tenantStore: store,
		httpClient:  server.Client(),
		cb:          webhookTestCB(),
		logger:      webhookTestLogger(),
	}

	payload := domain.WebhookDeliverPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		EventType:  domain.EventTransferFailed,
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentWebhookDeliver,
		Data:     &payload,
	}

	err := w.handleDeliver(context.Background(), event)
	if err == nil {
		t.Fatal("expected error for non-2xx response (triggers NAK/retry)")
	}
}

func TestWebhookWorker_NoWebhookURL_Skipped(t *testing.T) {
	tenantID := uuid.New()
	transferID := uuid.New()

	store := newMockTenantWebhookStore()
	store.tenants[tenantID] = &domain.Tenant{
		ID:         tenantID,
		WebhookURL: "", // no webhook configured
	}

	w := &WebhookWorker{
		tenantStore: store,
		httpClient:  http.DefaultClient,
		cb:          webhookTestCB(),
		logger:      webhookTestLogger(),
	}

	payload := domain.WebhookDeliverPayload{
		TransferID: transferID,
		TenantID:   tenantID,
		EventType:  domain.EventTransferCompleted,
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: tenantID,
		Type:     domain.IntentWebhookDeliver,
		Data:     &payload,
	}

	err := w.handleDeliver(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error when webhook URL is empty, got %v", err)
	}
}

func TestWebhookWorker_TenantNotFound_ReturnsError(t *testing.T) {
	store := newMockTenantWebhookStore() // empty store

	w := &WebhookWorker{
		tenantStore: store,
		httpClient:  http.DefaultClient,
		cb:          webhookTestCB(),
		logger:      webhookTestLogger(),
	}

	payload := domain.WebhookDeliverPayload{
		TransferID: uuid.New(),
		TenantID:   uuid.New(), // not in store
		EventType:  domain.EventTransferCompleted,
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     domain.IntentWebhookDeliver,
		Data:     &payload,
	}

	err := w.handleDeliver(context.Background(), event)
	if err == nil {
		t.Fatal("expected error when tenant not found")
	}
}

func TestWebhookWorker_EventRouting(t *testing.T) {
	w := &WebhookWorker{
		tenantStore: newMockTenantWebhookStore(),
		httpClient:  http.DefaultClient,
		cb:          webhookTestCB(),
		logger:      webhookTestLogger(),
	}

	event := domain.Event{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Type:     "some.unknown.event",
	}

	err := w.handleEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("expected no error for unknown event, got %v", err)
	}
}

func TestSignWebhook(t *testing.T) {
	payload := []byte(`{"test":"data"}`)
	secret := "my-secret-key"

	sig := signWebhook(payload, secret)

	// Verify manually
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if sig != expected {
		t.Errorf("signature mismatch: got %s, want %s", sig, expected)
	}
}
