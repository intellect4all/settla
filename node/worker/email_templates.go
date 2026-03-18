package worker

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/intellect4all/settla/domain"
)

// EmailTemplateData holds all fields available to email templates.
type EmailTemplateData struct {
	TenantName string
	EventType  string
	Subject    string
	Timestamp  string
	Data       map[string]any
	// Convenience fields extracted from Data if present.
	SessionID  string
	TransferID string
	Amount     string
	Currency   string
	Chain      string
	Token      string
	Status     string
	TxHash     string
	// Computed by the renderer based on event type category.
	AccentColor   string
	AccentBgColor string
	StatusIcon    template.HTML
	CategoryLabel string
}

// extractConvenienceFields populates convenience fields from the Data map.
func (d *EmailTemplateData) extractConvenienceFields() {
	if d.Data == nil {
		return
	}
	extract := func(key string) string {
		if v, ok := d.Data[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}
	if d.SessionID == "" {
		d.SessionID = extract("session_id")
	}
	if d.TransferID == "" {
		d.TransferID = extract("transfer_id")
	}
	if d.Amount == "" {
		d.Amount = extract("amount")
	}
	if d.Currency == "" {
		d.Currency = extract("currency")
	}
	if d.Chain == "" {
		d.Chain = extract("chain")
	}
	if d.Token == "" {
		d.Token = extract("token")
	}
	if d.Status == "" {
		d.Status = extract("status")
	}
	if d.TxHash == "" {
		d.TxHash = extract("tx_hash")
	}
}

// templateCategory classifies event types for template selection.
type templateCategory int

const (
	categoryDefault   templateCategory = iota
	categorySuccess
	categoryDetection
	categoryFailure
)

// successEvents are event types that use the success (green) template.
var successEvents = map[string]struct{}{
	domain.EventDepositSessionCredited:     {},
	domain.EventDepositSessionSettled:       {},
	domain.EventTransferCompleted:           {},
	domain.EventBankDepositSessionCredited:  {},
	domain.EventBankDepositSessionSettled:   {},
}

// detectionEvents are event types that use the detection (blue) template.
var detectionEvents = map[string]struct{}{
	domain.EventDepositTxDetected:           {},
	domain.EventDepositTxConfirmed:          {},
	domain.EventDepositSessionCreated:       {},
	domain.EventDepositSessionHeld:          {},
	domain.EventDepositLatePayment:          {},
	domain.EventBankDepositSessionCreated:   {},
	domain.EventBankDepositPaymentReceived:  {},
	domain.EventBankDepositUnderpaid:        {},
	domain.EventBankDepositOverpaid:         {},
	domain.EventBankDepositLatePayment:      {},
}

// failureEvents are event types that use the failure (red) template.
var failureEvents = map[string]struct{}{
	domain.EventDepositSessionFailed:        {},
	domain.EventDepositSessionExpired:       {},
	domain.EventDepositSessionCancelled:     {},
	domain.EventTransferFailed:              {},
	domain.EventBankDepositSessionFailed:    {},
	domain.EventBankDepositSessionExpired:   {},
	domain.EventBankDepositSessionCancelled: {},
}

func classifyEvent(eventType string) templateCategory {
	if _, ok := successEvents[eventType]; ok {
		return categorySuccess
	}
	if _, ok := detectionEvents[eventType]; ok {
		return categoryDetection
	}
	if _, ok := failureEvents[eventType]; ok {
		return categoryFailure
	}
	return categoryDefault
}

// categoryStyle returns accent color, background tint, icon, and label for a category.
func categoryStyle(cat templateCategory) (accentColor, accentBg string, icon template.HTML, label string) {
	switch cat {
	case categorySuccess:
		return "#16a34a", "#f0fdf4", "&#10003;", "Completed"
	case categoryDetection:
		return "#2563eb", "#eff6ff", "&#9432;", "Update"
	case categoryFailure:
		return "#dc2626", "#fef2f2", "&#10007;", "Action Required"
	default:
		return "#6b7280", "#f9fafb", "&#9679;", "Notification"
	}
}

// formatEventType converts "deposit.session.credited" to "Deposit Session Credited".
func formatEventType(eventType string) string {
	parts := strings.Split(eventType, ".")
	for i, p := range parts {
		p = strings.ReplaceAll(p, "_", " ")
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// emailTemplate is the shared HTML email template. All categories use the same structure
// with different accent colors injected via template data.
const emailTemplateSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Subject}}</title>
</head>
<body style="margin:0;padding:0;background-color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;-webkit-font-smoothing:antialiased;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background-color:#f4f4f5;">
<tr><td align="center" style="padding:40px 16px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="max-width:600px;width:100%;background-color:#ffffff;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.08);">

{{/* — Header bar — */}}
<tr><td style="background-color:{{.AccentColor}};height:4px;font-size:0;line-height:0;">&nbsp;</td></tr>

{{/* — Logo / Brand — */}}
<tr><td style="padding:32px 40px 0 40px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%">
<tr>
<td style="font-size:20px;font-weight:700;color:#111827;letter-spacing:-0.02em;">Settla</td>
<td align="right" style="font-size:12px;font-weight:600;color:{{.AccentColor}};text-transform:uppercase;letter-spacing:0.05em;">{{.CategoryLabel}}</td>
</tr>
</table>
</td></tr>

{{/* — Status badge — */}}
<tr><td style="padding:24px 40px 0 40px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%">
<tr><td style="background-color:{{.AccentBgColor}};border-radius:6px;padding:16px 20px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%">
<tr>
<td width="32" style="font-size:20px;color:{{.AccentColor}};vertical-align:middle;">{{.StatusIcon}}</td>
<td style="vertical-align:middle;">
<div style="font-size:15px;font-weight:600;color:#111827;">{{formatEventType .EventType}}</div>
{{if .TenantName}}<div style="font-size:13px;color:#6b7280;margin-top:2px;">Tenant: {{.TenantName}}</div>{{end}}
</td>
</tr>
</table>
</td></tr>
</table>
</td></tr>

{{/* — Key details card — */}}
{{if or .Amount .SessionID .TransferID .TxHash}}
<tr><td style="padding:24px 40px 0 40px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border:1px solid #e5e7eb;border-radius:6px;overflow:hidden;">
{{if .Amount}}
<tr>
<td style="padding:14px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Amount</td>
<td style="padding:14px 20px;border-bottom:1px solid #f3f4f6;font-size:15px;font-weight:600;color:#111827;">{{.Amount}}{{if .Currency}} {{.Currency}}{{end}}</td>
</tr>
{{end}}
{{if .SessionID}}
<tr>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Session</td>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:13px;color:#374151;font-family:'SF Mono',Menlo,Consolas,monospace;">{{.SessionID}}</td>
</tr>
{{end}}
{{if .TransferID}}
<tr>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Transfer</td>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:13px;color:#374151;font-family:'SF Mono',Menlo,Consolas,monospace;">{{.TransferID}}</td>
</tr>
{{end}}
{{if .Chain}}
<tr>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Chain</td>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:13px;color:#374151;">{{.Chain}}{{if .Token}} / {{.Token}}{{end}}</td>
</tr>
{{end}}
{{if .TxHash}}
<tr>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Tx Hash</td>
<td style="padding:12px 20px;border-bottom:1px solid #f3f4f6;font-size:13px;color:#374151;font-family:'SF Mono',Menlo,Consolas,monospace;word-break:break-all;">{{.TxHash}}</td>
</tr>
{{end}}
{{if .Status}}
<tr>
<td style="padding:12px 20px;font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;width:120px;">Status</td>
<td style="padding:12px 20px;font-size:13px;font-weight:600;color:{{$.AccentColor}};">{{.Status}}</td>
</tr>
{{end}}
</table>
</td></tr>
{{end}}

{{/* — Additional data table — */}}
{{if .Data}}
<tr><td style="padding:24px 40px 0 40px;">
<div style="font-size:12px;font-weight:600;color:#6b7280;text-transform:uppercase;letter-spacing:0.04em;margin-bottom:8px;">Details</div>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border:1px solid #e5e7eb;border-radius:6px;overflow:hidden;">
{{range $key, $val := .Data}}
<tr>
<td style="padding:10px 20px;border-bottom:1px solid #f3f4f6;font-size:12px;font-weight:600;color:#6b7280;width:140px;vertical-align:top;">{{$key}}</td>
<td style="padding:10px 20px;border-bottom:1px solid #f3f4f6;font-size:13px;color:#374151;word-break:break-all;">{{$val}}</td>
</tr>
{{end}}
</table>
</td></tr>
{{end}}

{{/* — Timestamp — */}}
{{if .Timestamp}}
<tr><td style="padding:24px 40px 0 40px;">
<div style="font-size:12px;color:#9ca3af;">{{.Timestamp}} UTC</div>
</td></tr>
{{end}}

{{/* — Footer — */}}
<tr><td style="padding:32px 40px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%">
<tr><td style="border-top:1px solid #e5e7eb;padding-top:20px;">
<div style="font-size:12px;color:#9ca3af;line-height:1.5;">Settla &mdash; Stablecoin Settlement Infrastructure</div>
<div style="font-size:11px;color:#d1d5db;margin-top:4px;">This is an automated notification. Do not reply to this email.</div>
</td></tr>
</table>
</td></tr>

</table>
</td></tr>
</table>
</body>
</html>`

// parsedEmailTemplate is the pre-parsed email template. Panics at init if the template is invalid.
var parsedEmailTemplate = template.Must(
	template.New("email").Funcs(template.FuncMap{
		"formatEventType": formatEventType,
	}).Parse(emailTemplateSource),
)

// renderEmailTemplate renders an HTML email for the given event type and data.
// The event type determines the color scheme (success=green, detection=blue, failure=red, default=neutral).
func renderEmailTemplate(eventType string, data EmailTemplateData) (string, error) {
	cat := classifyEvent(eventType)
	accentColor, accentBg, icon, label := categoryStyle(cat)

	data.AccentColor = accentColor
	data.AccentBgColor = accentBg
	data.StatusIcon = icon
	data.CategoryLabel = label
	data.extractConvenienceFields()

	var buf bytes.Buffer
	if err := parsedEmailTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("settla-email: rendering template for %s: %w", eventType, err)
	}
	return buf.String(), nil
}
