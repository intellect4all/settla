package settla

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
)

// Normalizer handles webhook payloads from the Settla testnet provider.
// In testnet mode the provider simulates async callbacks with the same
// JSON structure as the mock provider — this normalizer parses that format.
//
// When real providers are integrated, each will have its own normalizer that
// understands the provider's specific webhook format.
type Normalizer struct{}

// settlaWebhookBody is the expected shape of a Settla testnet webhook.
type settlaWebhookBody struct {
	TransferID string `json:"transfer_id"`
	TenantID   string `json:"tenant_id"`
	Reference  string `json:"reference"`
	Status     string `json:"status"`
	TxHash     string `json:"tx_hash,omitempty"`
	TxType     string `json:"tx_type,omitempty"`
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

func (n *Normalizer) NormalizeWebhook(providerSlug string, rawBody []byte) (*domain.ProviderWebhookPayload, error) {
	var body settlaWebhookBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return nil, fmt.Errorf("settla normalizer: invalid JSON: %w", err)
	}

	if body.TransferID == "" || body.TenantID == "" || body.Status == "" {
		return nil, fmt.Errorf("settla normalizer: missing required fields (transfer_id, tenant_id, status)")
	}

	transferID, err := uuid.Parse(body.TransferID)
	if err != nil {
		return nil, fmt.Errorf("settla normalizer: invalid transfer_id: %w", err)
	}
	tenantID, err := uuid.Parse(body.TenantID)
	if err != nil {
		return nil, fmt.Errorf("settla normalizer: invalid tenant_id: %w", err)
	}

	status := normalizeSettlaStatus(body.Status)
	if status == "" {
		return nil, nil // non-terminal, skip
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

func normalizeSettlaStatus(s string) string {
	switch strings.ToLower(s) {
	case "completed", "success", "confirmed":
		return "completed"
	case "failed", "failure", "error":
		return "failed"
	default:
		return ""
	}
}
