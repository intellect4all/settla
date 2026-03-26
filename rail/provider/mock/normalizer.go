package mock

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
)

// Normalizer handles webhook payloads from mock providers.
// Mock providers use a simple JSON format with top-level fields.
type Normalizer struct{}

// mockWebhookBody is the expected shape of a mock provider webhook.
type mockWebhookBody struct {
	TransferID string `json:"transfer_id"`
	TenantID   string `json:"tenant_id"`
	Reference  string `json:"reference"`
	Status     string `json:"status"`
	TxHash     string `json:"tx_hash,omitempty"`
	TxType     string `json:"tx_type,omitempty"` // "onramp" or "offramp"
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

func (n *Normalizer) NormalizeWebhook(providerSlug string, rawBody []byte) (*domain.ProviderWebhookPayload, error) {
	var body mockWebhookBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return nil, fmt.Errorf("mock normalizer: invalid JSON: %w", err)
	}

	if body.TransferID == "" || body.TenantID == "" || body.Status == "" {
		return nil, fmt.Errorf("mock normalizer: missing required fields (transfer_id, tenant_id, status)")
	}

	transferID, err := uuid.Parse(body.TransferID)
	if err != nil {
		return nil, fmt.Errorf("mock normalizer: invalid transfer_id: %w", err)
	}
	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		return nil, fmt.Errorf("mock normalizer: invalid tenant_id: %w", err)
	}

	status := normalizeStatus(body.Status)
	if status == "" {
		return nil, nil // non-terminal status, skip
	}

	txType := body.TxType
	if txType == "" {
		txType = "onramp"
	}

	return &domain.ProviderWebhookPayload{
		TransferID:  transferID,
		TenantID:    tenantID,
		ProviderID:  providerSlug,
		ProviderRef: body.Reference,
		Status:      status,
		TxHash:      body.TxHash,
		Error:       body.Error,
		ErrorCode:   body.ErrorCode,
		TxType:      txType,
	}, nil
}

func normalizeStatus(s string) string {
	switch strings.ToLower(s) {
	case "completed", "success", "successful", "confirmed":
		return "completed"
	case "failed", "failure", "error", "rejected", "declined":
		return "failed"
	default:
		return ""
	}
}
