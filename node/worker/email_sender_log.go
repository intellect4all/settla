package worker

import (
	"context"
	"log/slog"
)

// LogEmailSender is a development EmailSender that logs emails instead of sending them.
// Replace with a real implementation (SendGrid, Resend, AWS SES) for production.
type LogEmailSender struct {
	logger *slog.Logger
}

// NewLogEmailSender creates a LogEmailSender that logs all email sends.
func NewLogEmailSender(logger *slog.Logger) *LogEmailSender {
	return &LogEmailSender{logger: logger.With("module", "email-sender")}
}

// SendEmail logs the email details instead of sending.
func (s *LogEmailSender) SendEmail(_ context.Context, req SendEmailRequest) error {
	s.logger.Info("settla-email: would send email",
		"to", req.To,
		"subject", req.Subject,
		"event_type", req.EventType,
		"tenant_id", req.TenantID,
		"data_keys", mapKeys(req.Data),
	)
	return nil
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
