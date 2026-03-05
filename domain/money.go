package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Currency represents an ISO 4217 currency code or stablecoin symbol.
type Currency string

// Supported fiat and stablecoin currencies.
const (
	CurrencyNGN  Currency = "NGN"
	CurrencyUSD  Currency = "USD"
	CurrencyGBP  Currency = "GBP"
	CurrencyEUR  Currency = "EUR"
	CurrencyGHS  Currency = "GHS"
	CurrencyKES  Currency = "KES"
	CurrencyUSDT Currency = "USDT"
	CurrencyUSDC Currency = "USDC"
)

// SupportedCurrencies is the set of all currencies Settla can process.
var SupportedCurrencies = map[Currency]bool{
	CurrencyNGN:  true,
	CurrencyUSD:  true,
	CurrencyGBP:  true,
	CurrencyEUR:  true,
	CurrencyGHS:  true,
	CurrencyKES:  true,
	CurrencyUSDT: true,
	CurrencyUSDC: true,
}

// ValidateCurrency returns an error if the currency is not supported.
func ValidateCurrency(c Currency) error {
	if !SupportedCurrencies[c] {
		return fmt.Errorf("settla-domain: unsupported currency: %s", c)
	}
	return nil
}

// Money is an immutable, currency-qualified decimal amount.
// All monetary math MUST use shopspring/decimal. Never use float64 for money.
type Money struct {
	Amount   decimal.Decimal
	Currency Currency
}

// NewMoney creates a Money value from a string amount and currency.
// Returns an error if the amount string is invalid or the currency is unsupported.
func NewMoney(amount string, currency Currency) (Money, error) {
	if err := ValidateCurrency(currency); err != nil {
		return Money{}, err
	}
	d, err := decimal.NewFromString(amount)
	if err != nil {
		return Money{}, fmt.Errorf("settla-domain: invalid amount %q: %w", amount, err)
	}
	return Money{Amount: d, Currency: currency}, nil
}

// Add returns a new Money with the sum. Returns an error if currencies differ.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("settla-domain: cannot add %s to %s: %w",
			other.Currency, m.Currency, ErrCurrencyMismatch("add", string(m.Currency), string(other.Currency)))
	}
	return Money{Amount: m.Amount.Add(other.Amount), Currency: m.Currency}, nil
}

// Sub returns a new Money with the difference. Returns an error if currencies differ.
func (m Money) Sub(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("settla-domain: cannot subtract %s from %s: %w",
			other.Currency, m.Currency, ErrCurrencyMismatch("subtract", string(m.Currency), string(other.Currency)))
	}
	return Money{Amount: m.Amount.Sub(other.Amount), Currency: m.Currency}, nil
}

// Mul returns a new Money with the amount multiplied by the given factor.
func (m Money) Mul(factor decimal.Decimal) Money {
	return Money{Amount: m.Amount.Mul(factor), Currency: m.Currency}
}

// IsPositive returns true if the amount is greater than zero.
func (m Money) IsPositive() bool {
	return m.Amount.IsPositive()
}

// IsZero returns true if the amount is zero.
func (m Money) IsZero() bool {
	return m.Amount.IsZero()
}

// IsNegative returns true if the amount is less than zero.
func (m Money) IsNegative() bool {
	return m.Amount.IsNegative()
}

// String returns the amount and currency formatted as "100.00 USD".
func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.Amount.StringFixed(2), m.Currency)
}
