package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/resilience"
)

// TenantWebhookStore provides tenant webhook configuration for delivery.
type TenantWebhookStore interface {
	// GetTenant retrieves the tenant for webhook URL and secret.
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// WebhookWorker consumes webhook delivery intent messages from NATS and
// delivers webhook payloads to tenant endpoints with HMAC-SHA256 signatures.
type WebhookWorker struct {
	partition   int
	tenantStore TenantWebhookStore
	httpClient  *http.Client
	cb          *resilience.CircuitBreaker
	subscriber  *messaging.StreamSubscriber
	logger      *slog.Logger
}

// NewWebhookWorker creates a webhook worker that subscribes to the webhooks stream.
// An optional circuit breaker can be provided; if nil, a default one is created
// (5 failures, 30s reset timeout).
func NewWebhookWorker(
	partition int,
	tenantStore TenantWebhookStore,
	client *messaging.Client,
	logger *slog.Logger,
	cb *resilience.CircuitBreaker,
	opts ...messaging.SubscriberOption,
) *WebhookWorker {
	if cb == nil {
		cb = resilience.NewCircuitBreaker("webhook-http",
			resilience.WithFailureThreshold(5),
			resilience.WithResetTimeout(30*time.Second),
		)
	}

	consumerName := messaging.StreamConsumerName("settla-webhook-worker", partition)

	return &WebhookWorker{
		partition:   partition,
		tenantStore: tenantStore,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		cb: cb,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamWebhooks,
			consumerName,
			opts...,
		),
		logger: logger.With("module", "webhook-worker", "partition", partition),
	}
}

// Start begins consuming webhook intent messages. Blocks until ctx is cancelled.
func (w *WebhookWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-webhook-worker: starting", "partition", w.partition)
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixWebhook, w.partition)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the subscription.
func (w *WebhookWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes webhook intent events.
func (w *WebhookWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentWebhookDeliver:
		return w.handleDeliver(ctx, event)
	default:
		w.logger.Debug("settla-webhook-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// WebhookPayload is the JSON body sent to tenant webhook endpoints.
type WebhookPayload struct {
	ID         string    `json:"id"`
	EventType  string    `json:"event_type"`
	TransferID string    `json:"transfer_id"`
	TenantID   string    `json:"tenant_id"`
	Data       json.RawMessage `json:"data,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// handleDeliver constructs and sends a webhook to the tenant's configured endpoint.
func (w *WebhookWorker) handleDeliver(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.WebhookDeliverPayload](event)
	if err != nil {
		w.logger.Error("settla-webhook-worker: failed to unmarshal deliver payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	w.logger.Info("settla-webhook-worker: delivering webhook",
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"event_type", payload.EventType,
	)

	// Load tenant to get webhook URL and secret
	tenant, err := w.tenantStore.GetTenant(ctx, payload.TenantID)
	if err != nil {
		w.logger.Error("settla-webhook-worker: failed to load tenant",
			"tenant_id", payload.TenantID,
			"error", err,
		)
		return fmt.Errorf("settla-webhook-worker: loading tenant %s: %w", payload.TenantID, err)
	}

	if tenant.WebhookURL == "" {
		w.logger.Info("settla-webhook-worker: tenant has no webhook URL, skipping",
			"tenant_id", payload.TenantID,
		)
		return nil // ACK — no webhook configured
	}

	// Build webhook body
	webhookBody := WebhookPayload{
		ID:         uuid.Must(uuid.NewV7()).String(),
		EventType:  payload.EventType,
		TransferID: payload.TransferID.String(),
		TenantID:   payload.TenantID.String(),
		Data:       payload.Data,
		CreatedAt:  time.Now().UTC(),
	}

	body, err := json.Marshal(webhookBody)
	if err != nil {
		w.logger.Error("settla-webhook-worker: failed to marshal webhook body",
			"transfer_id", payload.TransferID,
			"error", err,
		)
		return nil // ACK — marshalling error won't resolve on retry
	}

	// Sign with HMAC-SHA256
	signature := signWebhook(body, tenant.WebhookSecret)

	// Send HTTP POST
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tenant.WebhookURL, bytes.NewReader(body))
	if err != nil {
		w.logger.Error("settla-webhook-worker: failed to create request",
			"webhook_url", tenant.WebhookURL,
			"error", err,
		)
		return nil // ACK — malformed URL won't resolve on retry
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Settla-Signature", signature)
	req.Header.Set("X-Settla-Event", payload.EventType)
	req.Header.Set("X-Settla-Delivery", webhookBody.ID)

	var resp *http.Response
	cbErr := w.cb.Execute(ctx, func(ctx context.Context) error {
		var doErr error
		resp, doErr = w.httpClient.Do(req)
		return doErr
	})
	if cbErr != nil {
		w.logger.Warn("settla-webhook-worker: delivery failed",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"webhook_url", tenant.WebhookURL,
			"error", cbErr,
		)
		// Return error to trigger NAK with retry (includes ErrCircuitOpen)
		return fmt.Errorf("settla-webhook-worker: delivering to %s: %w", tenant.WebhookURL, cbErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		w.logger.Info("settla-webhook-worker: delivery succeeded",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"status_code", resp.StatusCode,
		)
		return nil // ACK
	}

	// Retryable: server errors, timeout, rate limited
	if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429 {
		w.logger.Warn("settla-webhook-worker: retryable delivery failure",
			"transfer_id", payload.TransferID,
			"tenant_id", payload.TenantID,
			"status_code", resp.StatusCode,
			"webhook_url", tenant.WebhookURL,
		)
		return fmt.Errorf("settla-webhook-worker: retryable HTTP %d", resp.StatusCode)
	}

	// Non-retryable: 4xx client errors (bad request, unauthorized, not found, etc.)
	w.logger.Error("settla-webhook-worker: permanent delivery failure",
		"status", resp.StatusCode,
		"transfer_id", payload.TransferID,
		"tenant_id", payload.TenantID,
		"webhook_url", tenant.WebhookURL,
	)
	return nil // ACK — don't retry permanent failures
}

// signWebhook computes the HMAC-SHA256 signature of the payload using the secret.
func signWebhook(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
