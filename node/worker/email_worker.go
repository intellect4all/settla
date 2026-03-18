package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/messaging"
)

// EmailSender is the interface for sending email notifications.
// Implementations can use SendGrid, Resend, AWS SES, SMTP, or any provider.
type EmailSender interface {
	// SendEmail sends an email to the given recipients.
	SendEmail(ctx context.Context, req SendEmailRequest) error
}

// SendEmailRequest is the input for sending an email notification.
type SendEmailRequest struct {
	To        []string          // recipient email addresses
	Subject   string            // email subject line
	EventType string            // e.g., "deposit.session.credited"
	Data      map[string]any    // template data for rendering
	TenantID  uuid.UUID         // tenant for branding/context
}

// TenantEmailStore provides tenant notification configuration.
type TenantEmailStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// EmailWorker consumes email notification intents from NATS and dispatches
// emails via the configured EmailSender. It follows the same pattern as
// WebhookWorker: consume intent → load tenant config → send → ACK/NAK.
type EmailWorker struct {
	partition   int
	tenantStore TenantEmailStore
	sender      EmailSender
	subscriber  *messaging.StreamSubscriber
	logger      *slog.Logger
}

// NewEmailWorker creates an email worker that subscribes to the SETTLA_EMAILS stream.
func NewEmailWorker(
	partition int,
	tenantStore TenantEmailStore,
	sender EmailSender,
	client *messaging.Client,
	logger *slog.Logger,
	opts ...messaging.SubscriberOption,
) *EmailWorker {
	consumerName := messaging.StreamConsumerName("settla-email-worker", partition)

	return &EmailWorker{
		partition:   partition,
		tenantStore: tenantStore,
		sender:      sender,
		subscriber: messaging.NewStreamSubscriber(
			client,
			messaging.StreamEmails,
			consumerName,
			opts...,
		),
		logger: logger.With("module", "email-worker", "partition", partition),
	}
}

// Start begins consuming email intent messages. Blocks until ctx is cancelled.
func (w *EmailWorker) Start(ctx context.Context) error {
	w.logger.Info("settla-email-worker: starting", "partition", w.partition)
	filter := messaging.StreamPartitionFilter(messaging.SubjectPrefixEmail, w.partition)
	return w.subscriber.SubscribeStream(ctx, filter, w.handleEvent)
}

// Stop cancels the subscription.
func (w *EmailWorker) Stop() {
	w.subscriber.Stop()
}

// handleEvent routes email intent events.
func (w *EmailWorker) handleEvent(ctx context.Context, event domain.Event) error {
	switch event.Type {
	case domain.IntentEmailNotify:
		return w.handleNotify(ctx, event)
	default:
		w.logger.Debug("settla-email-worker: unhandled event type, skipping",
			"event_type", event.Type,
		)
		return nil
	}
}

// handleNotify processes an email notification intent.
func (w *EmailWorker) handleNotify(ctx context.Context, event domain.Event) error {
	payload, err := unmarshalEventData[domain.EmailNotifyPayload](event)
	if err != nil {
		w.logger.Error("settla-email-worker: failed to unmarshal email payload",
			"event_id", event.ID,
			"error", err,
		)
		return nil // ACK — malformed payload
	}

	// Load tenant for notification config
	tenant, err := w.tenantStore.GetTenant(ctx, payload.TenantID)
	if err != nil {
		w.logger.Error("settla-email-worker: failed to load tenant",
			"tenant_id", payload.TenantID,
			"error", err,
		)
		return fmt.Errorf("settla-email-worker: loading tenant %s: %w", payload.TenantID, err)
	}

	// Check if email notifications are enabled
	if !tenant.NotificationConfig.EmailEnabled {
		w.logger.Debug("settla-email-worker: email notifications disabled for tenant",
			"tenant_id", payload.TenantID,
		)
		return nil // ACK
	}

	// Check if this event type should trigger an email
	if !w.shouldNotify(tenant, payload.EventType) {
		w.logger.Debug("settla-email-worker: event type not configured for notification",
			"tenant_id", payload.TenantID,
			"event_type", payload.EventType,
		)
		return nil // ACK
	}

	recipients := tenant.NotificationConfig.NotificationEmails
	if len(recipients) == 0 {
		w.logger.Debug("settla-email-worker: no notification emails configured",
			"tenant_id", payload.TenantID,
		)
		return nil // ACK
	}

	// Parse template data from payload
	var templateData map[string]any
	if len(payload.Data) > 0 {
		_ = json.Unmarshal(payload.Data, &templateData)
	}
	if templateData == nil {
		templateData = make(map[string]any)
	}
	templateData["tenant_name"] = tenant.Name
	templateData["event_type"] = payload.EventType
	templateData["timestamp"] = time.Now().UTC().Format(time.RFC3339)

	if payload.SessionID != uuid.Nil {
		templateData["session_id"] = payload.SessionID.String()
	}
	if payload.TransferID != uuid.Nil {
		templateData["transfer_id"] = payload.TransferID.String()
	}

	w.logger.Info("settla-email-worker: sending notification",
		"tenant_id", payload.TenantID,
		"event_type", payload.EventType,
		"recipients", len(recipients),
	)

	if err := w.sender.SendEmail(ctx, SendEmailRequest{
		To:        recipients,
		Subject:   payload.Subject,
		EventType: payload.EventType,
		Data:      templateData,
		TenantID:  payload.TenantID,
	}); err != nil {
		w.logger.Error("settla-email-worker: delivery failed",
			"tenant_id", payload.TenantID,
			"event_type", payload.EventType,
			"error", err,
		)
		// Return error for retryable failures
		return fmt.Errorf("settla-email-worker: sending email: %w", err)
	}

	w.logger.Info("settla-email-worker: email sent successfully",
		"tenant_id", payload.TenantID,
		"event_type", payload.EventType,
	)

	return nil // ACK
}

// shouldNotify checks tenant preferences to determine if this event type warrants an email.
func (w *EmailWorker) shouldNotify(tenant *domain.Tenant, eventType string) bool {
	cfg := tenant.NotificationConfig

	switch eventType {
	// Success events
	case domain.EventDepositSessionCredited, domain.EventDepositSessionSettled,
		domain.EventBankDepositSessionCredited, domain.EventBankDepositSessionSettled,
		domain.EventTransferCompleted:
		return cfg.NotifyOnSuccess

	// Failure events
	case domain.EventDepositSessionFailed, domain.EventDepositSessionExpired,
		domain.EventDepositSessionCancelled, domain.EventTransferFailed,
		domain.EventBankDepositSessionFailed, domain.EventBankDepositSessionExpired:
		return cfg.NotifyOnFailure

	// Detection events
	case domain.EventDepositTxDetected, domain.EventDepositTxConfirmed,
		domain.EventDepositSessionCreated, domain.EventDepositSessionHeld,
		domain.EventDepositLatePayment,
		domain.EventBankDepositPaymentReceived, domain.EventBankDepositSessionCreated,
		domain.EventBankDepositUnderpaid, domain.EventBankDepositOverpaid,
		domain.EventBankDepositLatePayment:
		return cfg.NotifyOnDetection

	default:
		return false
	}
}
