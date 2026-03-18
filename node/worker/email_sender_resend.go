package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const resendAPIURL = "https://api.resend.com/emails"

// ResendEmailSender sends emails via the Resend HTTP API (https://resend.com/docs/api-reference/emails/send-email).
// It uses net/http directly to avoid adding an external SDK dependency.
type ResendEmailSender struct {
	apiKey string
	from   string
	client *http.Client
	logger *slog.Logger
}

// NewResendEmailSender creates a ResendEmailSender with the given API key and from address.
func NewResendEmailSender(apiKey, from string, logger *slog.Logger) *ResendEmailSender {
	return &ResendEmailSender{
		apiKey: apiKey,
		from:   from,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger.With("module", "email-sender-resend"),
	}
}

// resendRequest is the JSON body for POST /emails.
type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

// SendEmail sends an email via the Resend API.
func (s *ResendEmailSender) SendEmail(ctx context.Context, req SendEmailRequest) error {
	tmplData := EmailTemplateData{
		EventType: req.EventType,
		Subject:   req.Subject,
		Data:      req.Data,
	}
	// Extract well-known fields from Data for convenience.
	if v, ok := req.Data["tenant_name"]; ok {
		tmplData.TenantName = fmt.Sprintf("%v", v)
	}
	if v, ok := req.Data["timestamp"]; ok {
		tmplData.Timestamp = fmt.Sprintf("%v", v)
	}
	tmplData.extractConvenienceFields()

	htmlBody, err := renderEmailTemplate(req.EventType, tmplData)
	if err != nil {
		s.logger.Error("settla-email: template render failed, using event type as body",
			"event_type", req.EventType,
			"error", err,
		)
		htmlBody = fmt.Sprintf("<p>Event: %s</p>", req.EventType)
	}

	payload := resendRequest{
		From:    s.from,
		To:      req.To,
		Subject: req.Subject,
		HTML:    htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("settla-email: marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("settla-email: creating request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		// Network/timeout errors are retryable.
		return fmt.Errorf("settla-email: sending request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for logging (cap at 1KB to avoid OOM on unexpected responses).
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.logger.Info("settla-email: sent successfully",
			"to", req.To,
			"event_type", req.EventType,
			"tenant_id", req.TenantID,
			"status", resp.StatusCode,
		)
		return nil
	}

	// 429 (rate limit) and 5xx are retryable — return wrapped error so the worker NAKs.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		s.logger.Warn("settla-email: retryable failure from Resend API",
			"status", resp.StatusCode,
			"body", string(respBody),
			"tenant_id", req.TenantID,
		)
		return fmt.Errorf("settla-email: Resend API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// 4xx (except 429) are permanent failures — log and return nil so the worker ACKs.
	s.logger.Error("settla-email: permanent failure from Resend API",
		"status", resp.StatusCode,
		"body", string(respBody),
		"to", req.To,
		"event_type", req.EventType,
		"tenant_id", req.TenantID,
	)
	return nil
}

