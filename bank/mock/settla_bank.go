package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// MockSettlaBank is a mock banking partner for testing and local development.
// It generates deterministic account numbers and simulates all banking operations.
type MockSettlaBank struct {
	id         string
	currencies []domain.Currency
	counter    int
}

// NewMockSettlaBank creates a new mock banking partner with sensible defaults.
func NewMockSettlaBank() *MockSettlaBank {
	return &MockSettlaBank{
		id: "mock-settla-bank",
		currencies: []domain.Currency{
			domain.Currency("GBP"),
			domain.Currency("EUR"),
			domain.Currency("USD"),
		},
	}
}

// ID returns the unique identifier of the mock banking partner.
func (b *MockSettlaBank) ID() string {
	return b.id
}

// SupportedCurrencies returns the list of currencies this mock partner supports.
func (b *MockSettlaBank) SupportedCurrencies() []domain.Currency {
	return b.currencies
}

// ProvisionAccount creates a new virtual account with a deterministic account number.
// The account number is derived from tenant ID, currency, and reference for reproducibility.
func (b *MockSettlaBank) ProvisionAccount(_ context.Context, req domain.ProvisionAccountRequest) (*domain.ProvisionAccountResult, error) {
	b.counter++
	acctNum := fmt.Sprintf("MOCK-%s-%s-%04d", req.Currency, req.TenantID.String()[:8], b.counter)
	return &domain.ProvisionAccountResult{
		AccountNumber: acctNum,
		AccountName:   fmt.Sprintf("Settla %s %s", req.Currency, req.Reference),
		SortCode:      "000000",
		IBAN:          fmt.Sprintf("GB00MOCK%s%04d", req.Currency, b.counter),
	}, nil
}

// RecycleAccount marks a virtual account as available for reuse (no-op in mock).
func (b *MockSettlaBank) RecycleAccount(_ context.Context, _ string) error {
	return nil
}

// RefundPayment initiates a mock refund for a bank deposit.
func (b *MockSettlaBank) RefundPayment(_ context.Context, req domain.RefundPaymentRequest) (*domain.RefundPaymentResult, error) {
	return &domain.RefundPaymentResult{
		RefundReference: fmt.Sprintf("REFUND-%s", req.SessionID.String()[:8]),
		Success:         true,
	}, nil
}

// SimulateIncomingCredit creates an IncomingBankCredit for testing.
// This simulates a bank notification that funds have arrived at the given account.
func (b *MockSettlaBank) SimulateIncomingCredit(accountNumber string, amount decimal.Decimal, payerName, ref string) domain.IncomingBankCredit {
	return domain.IncomingBankCredit{
		AccountNumber:      accountNumber,
		Amount:             amount,
		Currency:           domain.Currency("GBP"),
		PayerName:          payerName,
		PayerAccountNumber: "99998888",
		PayerReference:     ref,
		BankReference:      fmt.Sprintf("BANKREF-%s-%d", ref, time.Now().UnixNano()),
		ReceivedAt:         time.Now().UTC(),
	}
}
