package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BankingPartner defines the interface for a banking partner that provisions virtual accounts
// and handles bank deposit operations (refunds, account recycling).
type BankingPartner interface {
	// ID returns the unique identifier of the banking partner.
	ID() string
	// SupportedCurrencies returns the list of currencies this partner supports.
	SupportedCurrencies() []Currency
	// ProvisionAccount creates a new virtual account for a tenant.
	ProvisionAccount(ctx context.Context, req ProvisionAccountRequest) (*ProvisionAccountResult, error)
	// RecycleAccount marks a virtual account as available for reuse.
	RecycleAccount(ctx context.Context, accountNumber string) error
	// RefundPayment initiates a refund for a bank deposit.
	RefundPayment(ctx context.Context, req RefundPaymentRequest) (*RefundPaymentResult, error)
}

// ProvisionAccountRequest contains the parameters for provisioning a virtual account.
type ProvisionAccountRequest struct {
	TenantID    uuid.UUID          `json:"tenant_id"`
	Currency    Currency           `json:"currency"`
	AccountType VirtualAccountType `json:"account_type"`
	Reference   string             `json:"reference"`
}

// ProvisionAccountResult contains the details of a newly provisioned virtual account.
type ProvisionAccountResult struct {
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	SortCode      string `json:"sort_code"`
	IBAN          string `json:"iban"`
}

// RefundPaymentRequest contains the parameters for refunding a bank deposit.
type RefundPaymentRequest struct {
	SessionID     uuid.UUID       `json:"session_id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	AccountNumber string          `json:"account_number"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      Currency        `json:"currency"`
	Reason        string          `json:"reason"`
	BankReference string          `json:"bank_reference"`
}

// RefundPaymentResult contains the outcome of a refund request.
type RefundPaymentResult struct {
	RefundReference string `json:"refund_reference"`
	Success         bool   `json:"success"`
}

// BankingPartnerConfig holds the database-stored configuration for a banking partner.
type BankingPartnerConfig struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	WebhookSecret       string            `json:"webhook_secret"`
	SupportedCurrencies []string          `json:"supported_currencies"`
	IsActive            bool              `json:"is_active"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}
