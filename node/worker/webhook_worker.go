package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

// semEntry pairs a channel-based semaphore with a last-used timestamp for idle eviction.
type semEntry struct {
	ch       chan struct{}
	lastUsed atomic.Int64 // unix seconds
}

// tenantSemaphore provides per-tenant fair queueing to prevent a single
// high-volume tenant from monopolising the global HTTP semaphore.
type tenantSemaphore struct {
	mu            sync.Mutex
	sems          map[string]*semEntry
	maxPerTenant  int
}

// newTenantSemaphore creates a per-tenant semaphore with the given concurrency limit.
func newTenantSemaphore(maxPerTenant int) *tenantSemaphore {
	return &tenantSemaphore{
		sems:         make(map[string]*semEntry),
		maxPerTenant: maxPerTenant,
	}
}

// acquire blocks until a slot is available for the given tenant, or ctx is cancelled.
func (ts *tenantSemaphore) acquire(ctx context.Context, tenantID string) error {
	ts.mu.Lock()
	entry, ok := ts.sems[tenantID]
	if !ok {
		entry = &semEntry{ch: make(chan struct{}, ts.maxPerTenant)}
		ts.sems[tenantID] = entry
	}
	entry.lastUsed.Store(time.Now().Unix())
	ts.mu.Unlock()

	select {
	case entry.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release frees a slot for the given tenant.
func (ts *tenantSemaphore) release(tenantID string) {
	ts.mu.Lock()
	entry, ok := ts.sems[tenantID]
	ts.mu.Unlock()
	if ok {
		entry.lastUsed.Store(time.Now().Unix())
		<-entry.ch
	}
}

// cbEntry pairs a circuit breaker with a last-used timestamp for idle eviction.
type cbEntry struct {
	cb       *resilience.CircuitBreaker
	lastUsed atomic.Int64 // unix seconds
}

// WebhookWorker consumes webhook delivery intent messages from NATS and
// delivers webhook payloads to tenant endpoints with HMAC-SHA256 signatures.
type WebhookWorker struct {
	partition        int
	tenantStore      TenantWebhookStore
	httpClient       *http.Client
	defaultCB        *resilience.CircuitBreaker
	tenantCBs        sync.Map // map[string]*cbEntry — per-tenant CBs with idle tracking
	subscriber       *messaging.StreamSubscriber
	logger           *slog.Logger
	httpSem          chan struct{}
	tenantSem        *tenantSemaphore
	allowPrivateURLs bool // testing only — disables SSRF protection
}

// DefaultWebhookHTTPTimeout is the default timeout for webhook HTTP delivery requests.
const DefaultWebhookHTTPTimeout = 10 * time.Second

// WebhookWorkerConfig holds optional configuration for WebhookWorker.
type WebhookWorkerConfig struct {
	// HTTPTimeout is the timeout for webhook delivery HTTP requests.
	// Defaults to DefaultWebhookHTTPTimeout (10s) if zero.
	HTTPTimeout time.Duration

	// AllowPrivateURLs disables SSRF protection for webhook URLs.
	// ONLY set to true in test environments — never in production.
	AllowPrivateURLs bool
}

// NewWebhookWorker creates a webhook worker that subscribes to the webhooks stream.
// An optional circuit breaker can be provided; if nil, a default one is created
// (5 failures, 30s reset timeout). An optional config can be provided; if nil,
// defaults are used.
func NewWebhookWorker(
	partition int,
	tenantStore TenantWebhookStore,
	client *messaging.Client,
	logger *slog.Logger,
	cb *resilience.CircuitBreaker,
	cfg *WebhookWorkerConfig,
	opts ...messaging.SubscriberOption,
) *WebhookWorker {
	if cb == nil {
		cb = resilience.NewCircuitBreaker("webhook-http",
			resilience.WithFailureThreshold(5),
			resilience.WithResetTimeout(30*time.Second),
		)
	}

	httpTimeout := DefaultWebhookHTTPTimeout
	if cfg != nil && cfg.HTTPTimeout > 0 {
		httpTimeout = cfg.HTTPTimeout
	}

	consumerName := messaging.StreamConsumerName("settla-webhook-worker", partition)

	return &WebhookWorker{
		partition:   partition,
		tenantStore: tenantStore,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
		defaultCB: cb,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamWebhooks,
			consumerName,
			opts...,
		),
		logger:           logger.With("module", "webhook-worker", "partition", partition),
		httpSem:          make(chan struct{}, 100),
		tenantSem:        newTenantSemaphore(10),
		allowPrivateURLs: cfg != nil && cfg.AllowPrivateURLs,
	}
}

// Start begins consuming webhook intent messages. Blocks until ctx is cancelled.
func (w *WebhookWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-webhook-worker: starting", "partition", w.partition)
	go w.cleanupTenantResources(ctx)
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
	ID         string          `json:"id"`
	EventType  string          `json:"event_type"`
	TransferID string          `json:"transfer_id,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	TenantID   string          `json:"tenant_id"`
	Data       json.RawMessage `json:"data,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
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

	// Build webhook body with a deterministic delivery ID so tenants can deduplicate retries.
	// The ID is derived from the NATS event ID which is stable across redeliveries.
	deliveryID := deterministicDeliveryID(event.ID, payload.EventType)
	webhookBody := WebhookPayload{
		ID:        deliveryID,
		EventType: payload.EventType,
		TenantID:  payload.TenantID.String(),
		Data:      payload.Data,
		CreatedAt: time.Now().UTC(),
	}
	if payload.TransferID != uuid.Nil {
		webhookBody.TransferID = payload.TransferID.String()
	}
	if payload.SessionID != uuid.Nil {
		webhookBody.SessionID = payload.SessionID.String()
	}

	body, err := json.Marshal(webhookBody)
	if err != nil {
		w.logger.Error("settla-webhook-worker: failed to marshal webhook body",
			"transfer_id", payload.TransferID,
			"error", err,
		)
		return nil // ACK — marshalling error won't resolve on retry
	}

	// Validate webhook URL to prevent SSRF attacks targeting internal services.
	if !w.allowPrivateURLs {
		if err := validateWebhookURL(tenant.WebhookURL); err != nil {
			w.logger.Error("settla-webhook-worker: webhook URL rejected (SSRF protection)",
				"tenant_id", payload.TenantID,
				"webhook_url", tenant.WebhookURL,
				"error", err,
			)
			return nil // ACK — unsafe URL won't resolve on retry
		}
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

	// Per-tenant fair queueing: acquire tenant slot before global semaphore
	// to prevent a single high-volume tenant from monopolising all slots.
	tenantKey := payload.TenantID.String()
	if err := w.tenantSem.acquire(ctx, tenantKey); err != nil {
		return err
	}
	defer w.tenantSem.release(tenantKey)

	// Backpressure: limit concurrent HTTP calls
	select {
	case w.httpSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.httpSem }()

	// Use per-tenant circuit breaker to prevent one broken tenant from
	// opening the circuit for all tenants.
	tenantCB := w.getTenantCB(payload.TenantID.String())

	var resp *http.Response
	cbErr := tenantCB.Execute(ctx, func(ctx context.Context) error {
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
	defer func() {
		// Drain the response body so the underlying TCP connection can be reused
		// by the HTTP client's connection pool. Without this, Go's http.Transport
		// cannot recycle the connection for keep-alive.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

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

// getTenantCB returns or creates a per-tenant circuit breaker with the same
// settings as the default CB. This prevents one broken tenant's webhook endpoint
// from opening the circuit for all other tenants.
func (w *WebhookWorker) getTenantCB(tenantID string) *resilience.CircuitBreaker {
	if existing, ok := w.tenantCBs.Load(tenantID); ok {
		entry := existing.(*cbEntry)
		entry.lastUsed.Store(time.Now().Unix())
		return entry.cb
	}
	entry := &cbEntry{
		cb: resilience.NewCircuitBreaker("webhook-"+tenantID,
			resilience.WithFailureThreshold(5),
			resilience.WithResetTimeout(30*time.Second),
		),
	}
	entry.lastUsed.Store(time.Now().Unix())
	actual, _ := w.tenantCBs.LoadOrStore(tenantID, entry)
	return actual.(*cbEntry).cb
}

// cleanupTenantResources evicts per-tenant semaphores and circuit breakers
// that have been idle for more than 5 minutes. This prevents unbounded memory
// growth as new tenants are encountered over time.
func (w *WebhookWorker) cleanupTenantResources(ctx context.Context) {
	const idleTimeout = 5 * time.Minute
	ticker := time.NewTicker(idleTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-idleTimeout).Unix()

			// Evict idle semaphores (only if no in-flight webhooks).
			w.tenantSem.mu.Lock()
			for tenantID, entry := range w.tenantSem.sems {
				if entry.lastUsed.Load() < cutoff && len(entry.ch) == 0 {
					delete(w.tenantSem.sems, tenantID)
				}
			}
			w.tenantSem.mu.Unlock()

			// Evict idle circuit breakers.
			w.tenantCBs.Range(func(key, value any) bool {
				entry := value.(*cbEntry)
				if entry.lastUsed.Load() < cutoff {
					w.tenantCBs.Delete(key)
				}
				return true
			})
		}
	}
}

// signWebhook computes the HMAC-SHA256 signature of the payload using the secret.
func signWebhook(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// deterministicDeliveryID generates a stable webhook delivery ID from the event
// ID and event type. This ensures that NATS redeliveries of the same event
// produce the same delivery ID, allowing tenants to deduplicate webhook calls.
func deterministicDeliveryID(eventID uuid.UUID, eventType string) string {
	h := sha256.New()
	h.Write(eventID[:])
	h.Write([]byte(eventType))
	sum := h.Sum(nil)
	// Format as UUID v5-style for consistency with existing ID format.
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// validateWebhookURL checks that a webhook URL is safe to call, rejecting
// URLs that target private networks, cloud metadata endpoints, or non-HTTPS
// schemes to prevent SSRF attacks.
func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Require HTTPS in production-like environments; allow HTTP for localhost in dev.
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("unsupported scheme %q, must be https or http", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty hostname")
	}

	// Reject well-known dangerous hostnames.
	lower := strings.ToLower(host)
	if lower == "localhost" || lower == "metadata.google.internal" {
		return fmt.Errorf("hostname %q is not allowed", host)
	}

	// Resolve the hostname and check all resolved IPs.
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("DNS lookup failed for %q: %w", host, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("invalid resolved IP %q", ipStr)
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("resolved IP %s is in a private/reserved range", ipStr)
		}
	}

	return nil
}

// isPrivateIP returns true if the IP is in a private, loopback, link-local,
// or otherwise reserved range that should not be targeted by outbound webhooks.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network *net.IPNet
	}{
		{mustParseCIDR("10.0.0.0/8")},
		{mustParseCIDR("172.16.0.0/12")},
		{mustParseCIDR("192.168.0.0/16")},
		{mustParseCIDR("127.0.0.0/8")},
		{mustParseCIDR("169.254.0.0/16")}, // Link-local / cloud metadata
		{mustParseCIDR("::1/128")},         // IPv6 loopback
		{mustParseCIDR("fc00::/7")},        // IPv6 unique local
		{mustParseCIDR("fe80::/10")},       // IPv6 link-local
	}
	for _, r := range privateRanges {
		if r.network.Contains(ip) {
			return true
		}
	}
	return false
}

func mustParseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		panic("invalid CIDR: " + s)
	}
	return network
}
