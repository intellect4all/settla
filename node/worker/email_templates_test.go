package worker

import (
	"strings"
	"testing"

	"github.com/intellect4all/settla/domain"
)

func TestRenderEmailTemplate_SuccessCategory(t *testing.T) {
	data := EmailTemplateData{
		TenantName: "Lemfi",
		EventType:  domain.EventDepositSessionCredited,
		Subject:    "Deposit Credited",
		Timestamp:  "2026-03-14T12:00:00Z",
		Data: map[string]any{
			"amount":     "1000.00",
			"currency":   "USDT",
			"session_id": "abc-123",
		},
	}

	html, err := renderEmailTemplate(domain.EventDepositSessionCredited, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Success template uses green accent.
	assertContains(t, html, "#16a34a")
	assertContains(t, html, "#f0fdf4")
	assertContains(t, html, "Completed")
	assertContains(t, html, "Settla")
	assertContains(t, html, "Stablecoin Settlement Infrastructure")
	assertContains(t, html, "Lemfi")
	assertContains(t, html, "1000.00")
	assertContains(t, html, "USDT")
	assertContains(t, html, "abc-123")
	// Event type is rendered via formatEventType, not raw.
	assertContains(t, html, "Deposit Session Credited")
}

func TestRenderEmailTemplate_DetectionCategory(t *testing.T) {
	data := EmailTemplateData{
		TenantName: "Fincra",
		EventType:  domain.EventDepositTxDetected,
		Subject:    "Transaction Detected",
		Timestamp:  "2026-03-14T12:00:00Z",
		Data: map[string]any{
			"tx_hash": "0xdeadbeef",
			"chain":   "tron",
			"token":   "USDT",
		},
	}

	html, err := renderEmailTemplate(domain.EventDepositTxDetected, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Detection template uses blue accent.
	assertContains(t, html, "#2563eb")
	assertContains(t, html, "#eff6ff")
	assertContains(t, html, "Update")
	assertContains(t, html, "Fincra")
	assertContains(t, html, "0xdeadbeef")
	assertContains(t, html, "tron")
}

func TestRenderEmailTemplate_FailureCategory(t *testing.T) {
	data := EmailTemplateData{
		TenantName: "Paystack",
		EventType:  domain.EventDepositSessionFailed,
		Subject:    "Deposit Failed",
		Timestamp:  "2026-03-14T12:00:00Z",
		Data: map[string]any{
			"session_id": "fail-456",
			"status":     "FAILED",
		},
	}

	html, err := renderEmailTemplate(domain.EventDepositSessionFailed, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Failure template uses red accent.
	assertContains(t, html, "#dc2626")
	assertContains(t, html, "#fef2f2")
	assertContains(t, html, "Action Required")
	assertContains(t, html, "Paystack")
	assertContains(t, html, "fail-456")
	assertContains(t, html, "FAILED")
}

func TestRenderEmailTemplate_DefaultCategory(t *testing.T) {
	data := EmailTemplateData{
		TenantName: "AcmeCorp",
		EventType:  "unknown.event.type",
		Subject:    "Something happened",
		Timestamp:  "2026-03-14T12:00:00Z",
		Data: map[string]any{
			"info": "some detail",
		},
	}

	html, err := renderEmailTemplate("unknown.event.type", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Default uses neutral gray.
	assertContains(t, html, "#6b7280")
	assertContains(t, html, "#f9fafb")
	assertContains(t, html, "Notification")
	assertContains(t, html, "AcmeCorp")
	assertContains(t, html, "some detail")
	assertContains(t, html, "Stablecoin Settlement Infrastructure")
}

func TestRenderEmailTemplate_HTMLEscaping(t *testing.T) {
	data := EmailTemplateData{
		TenantName: `<script>alert("xss")</script>`,
		EventType:  domain.EventTransferCompleted,
		Subject:    `Test <b>bold</b>`,
		Timestamp:  "2026-03-14T12:00:00Z",
		Data: map[string]any{
			"amount":   `<img src=x onerror=alert(1)>`,
			"currency": `"USDT"&more`,
		},
	}

	html, err := renderEmailTemplate(domain.EventTransferCompleted, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that dangerous HTML tags are escaped (angle brackets become entities).
	assertNotContains(t, html, `<script>alert`)
	assertNotContains(t, html, `<img src=x`)
	// The escaped forms should be present.
	assertContains(t, html, "&lt;script&gt;")
	assertContains(t, html, "&lt;img src=x onerror=alert(1)&gt;")
}

func TestRenderEmailTemplate_EmptyData(t *testing.T) {
	data := EmailTemplateData{
		EventType: domain.EventTransferFailed,
		Subject:   "Transfer Failed",
	}

	html, err := renderEmailTemplate(domain.EventTransferFailed, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertContains(t, html, "Settla")
	assertContains(t, html, "Transfer Failed")
}

func TestRenderEmailTemplate_AllSuccessEvents(t *testing.T) {
	events := []string{
		domain.EventDepositSessionCredited,
		domain.EventDepositSessionSettled,
		domain.EventTransferCompleted,
		domain.EventBankDepositSessionCredited,
		domain.EventBankDepositSessionSettled,
	}
	for _, evt := range events {
		t.Run(evt, func(t *testing.T) {
			data := EmailTemplateData{EventType: evt, Subject: "test"}
			html, err := renderEmailTemplate(evt, data)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", evt, err)
			}
			assertContains(t, html, "#16a34a")
		})
	}
}

func TestRenderEmailTemplate_AllDetectionEvents(t *testing.T) {
	events := []string{
		domain.EventDepositTxDetected,
		domain.EventDepositTxConfirmed,
		domain.EventDepositSessionCreated,
		domain.EventDepositSessionHeld,
		domain.EventDepositLatePayment,
		domain.EventBankDepositSessionCreated,
		domain.EventBankDepositPaymentReceived,
		domain.EventBankDepositUnderpaid,
		domain.EventBankDepositOverpaid,
		domain.EventBankDepositLatePayment,
	}
	for _, evt := range events {
		t.Run(evt, func(t *testing.T) {
			data := EmailTemplateData{EventType: evt, Subject: "test"}
			html, err := renderEmailTemplate(evt, data)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", evt, err)
			}
			assertContains(t, html, "#2563eb")
		})
	}
}

func TestRenderEmailTemplate_AllFailureEvents(t *testing.T) {
	events := []string{
		domain.EventDepositSessionFailed,
		domain.EventDepositSessionExpired,
		domain.EventDepositSessionCancelled,
		domain.EventTransferFailed,
		domain.EventBankDepositSessionFailed,
		domain.EventBankDepositSessionExpired,
		domain.EventBankDepositSessionCancelled,
	}
	for _, evt := range events {
		t.Run(evt, func(t *testing.T) {
			data := EmailTemplateData{EventType: evt, Subject: "test"}
			html, err := renderEmailTemplate(evt, data)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", evt, err)
			}
			assertContains(t, html, "#dc2626")
		})
	}
}

func TestRenderEmailTemplate_ConvenienceFieldExtraction(t *testing.T) {
	data := EmailTemplateData{
		EventType: domain.EventDepositTxConfirmed,
		Subject:   "Confirmed",
		Data: map[string]any{
			"session_id":  "sess-001",
			"transfer_id": "txn-002",
			"amount":      "500.00",
			"currency":    "USDC",
			"chain":       "ethereum",
			"token":       "USDC",
			"status":      "CONFIRMED",
			"tx_hash":     "0xabc123",
		},
	}

	html, err := renderEmailTemplate(domain.EventDepositTxConfirmed, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertContains(t, html, "sess-001")
	assertContains(t, html, "txn-002")
	assertContains(t, html, "500.00")
	assertContains(t, html, "USDC")
	assertContains(t, html, "ethereum")
	assertContains(t, html, "CONFIRMED")
	assertContains(t, html, "0xabc123")
}

func TestFormatEventType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"deposit.session.credited", "Deposit Session Credited"},
		{"transfer.completed", "Transfer Completed"},
		{"bank_deposit.session.created", "Bank deposit Session Created"},
		{"deposit.tx.detected", "Deposit Tx Detected"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := formatEventType(tt.input)
			if got != tt.expected {
				t.Errorf("formatEventType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestRenderEmailTemplate_MobileResponsive(t *testing.T) {
	data := EmailTemplateData{
		EventType: domain.EventTransferCompleted,
		Subject:   "Transfer Done",
	}

	html, err := renderEmailTemplate(domain.EventTransferCompleted, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check for responsive meta tag and max-width constraint.
	assertContains(t, html, `name="viewport"`)
	assertContains(t, html, "max-width:600px")
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q, but it did not.\nOutput length: %d", substr, len(s))
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected output NOT to contain %q, but it did", substr)
	}
}
