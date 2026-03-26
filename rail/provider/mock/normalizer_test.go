package mock

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizer_CompletedWebhook(t *testing.T) {
	n := &Normalizer{}
	transferID := uuid.New()
	tenantID := uuid.New()

	body, _ := json.Marshal(map[string]string{
		"transfer_id": transferID.String(),
		"tenant_id":   tenantID.String(),
		"status":      "success",
		"reference":   "ext-ref-123",
		"tx_hash":     "0xabc",
		"tx_type":     "onramp",
	})

	result, err := n.NormalizeWebhook("mock-onramp-gbp", body)
	if err != nil {
		t.Fatalf("NormalizeWebhook: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result for completed webhook")
	}
	if result.TransferID != transferID {
		t.Errorf("transfer_id: got %s, want %s", result.TransferID, transferID)
	}
	if result.TenantID != tenantID {
		t.Errorf("tenant_id: got %s, want %s", result.TenantID, tenantID)
	}
	if result.Status != "completed" {
		t.Errorf("status: got %q, want %q", result.Status, "completed")
	}
	if result.ProviderRef != "ext-ref-123" {
		t.Errorf("provider_ref: got %q, want %q", result.ProviderRef, "ext-ref-123")
	}
	if result.TxHash != "0xabc" {
		t.Errorf("tx_hash: got %q, want %q", result.TxHash, "0xabc")
	}
	if result.ProviderID != "mock-onramp-gbp" {
		t.Errorf("provider_id: got %q, want %q", result.ProviderID, "mock-onramp-gbp")
	}
}

func TestNormalizer_FailedWebhook(t *testing.T) {
	n := &Normalizer{}

	body, _ := json.Marshal(map[string]string{
		"transfer_id": uuid.New().String(),
		"tenant_id":   uuid.New().String(),
		"status":      "declined",
		"error":       "insufficient funds",
		"error_code":  "INSUF_FUNDS",
	})

	result, err := n.NormalizeWebhook("mock-offramp-ngn", body)
	if err != nil {
		t.Fatalf("NormalizeWebhook: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("status: got %q, want %q", result.Status, "failed")
	}
	if result.Error != "insufficient funds" {
		t.Errorf("error: got %q, want %q", result.Error, "insufficient funds")
	}
}

func TestNormalizer_PendingStatusSkipped(t *testing.T) {
	n := &Normalizer{}

	body, _ := json.Marshal(map[string]string{
		"transfer_id": uuid.New().String(),
		"tenant_id":   uuid.New().String(),
		"status":      "pending",
	})

	result, err := n.NormalizeWebhook("mock-onramp-gbp", body)
	if err != nil {
		t.Fatalf("NormalizeWebhook: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil result for pending status (non-terminal)")
	}
}

func TestNormalizer_MissingRequiredFields(t *testing.T) {
	n := &Normalizer{}

	body, _ := json.Marshal(map[string]string{
		"status": "completed",
	})

	_, err := n.NormalizeWebhook("mock-onramp-gbp", body)
	if err == nil {
		t.Fatal("expected error for missing required fields")
	}
}

func TestNormalizer_InvalidJSON(t *testing.T) {
	n := &Normalizer{}

	_, err := n.NormalizeWebhook("mock-onramp-gbp", []byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizer_DefaultTxType(t *testing.T) {
	n := &Normalizer{}

	body, _ := json.Marshal(map[string]string{
		"transfer_id": uuid.New().String(),
		"tenant_id":   uuid.New().String(),
		"status":      "completed",
		// tx_type omitted
	})

	result, err := n.NormalizeWebhook("mock-onramp-gbp", body)
	if err != nil {
		t.Fatalf("NormalizeWebhook: %v", err)
	}
	if result.TxType != "onramp" {
		t.Errorf("tx_type: got %q, want %q (default)", result.TxType, "onramp")
	}
}
