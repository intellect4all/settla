package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestNewMoneyValid(t *testing.T) {
	m, err := NewMoney("100.50", CurrencyUSD)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("100.50")
	if !m.Amount.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, m.Amount)
	}
	if m.Currency != CurrencyUSD {
		t.Errorf("expected USD, got %s", m.Currency)
	}
}

func TestNewMoneyInvalidCurrency(t *testing.T) {
	_, err := NewMoney("100", "INVALID")
	if err == nil {
		t.Error("expected error for unsupported currency")
	}
}

func TestNewMoneyInvalidAmount(t *testing.T) {
	_, err := NewMoney("not-a-number", CurrencyUSD)
	if err == nil {
		t.Error("expected error for invalid amount")
	}
}

func TestMoneyAddSameCurrency(t *testing.T) {
	a, _ := NewMoney("100", CurrencyUSD)
	b, _ := NewMoney("50.25", CurrencyUSD)
	result, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("150.25")
	if !result.Amount.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, result.Amount)
	}
}

func TestMoneyAddDifferentCurrencyError(t *testing.T) {
	a, _ := NewMoney("100", CurrencyUSD)
	b, _ := NewMoney("50", CurrencyGBP)
	_, err := a.Add(b)
	if err == nil {
		t.Error("expected error when adding different currencies")
	}
}

func TestMoneySubSameCurrency(t *testing.T) {
	a, _ := NewMoney("100", CurrencyUSD)
	b, _ := NewMoney("30", CurrencyUSD)
	result, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("70")
	if !result.Amount.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, result.Amount)
	}
}

func TestMoneySubDifferentCurrencyError(t *testing.T) {
	a, _ := NewMoney("100", CurrencyUSD)
	b, _ := NewMoney("30", CurrencyGBP)
	_, err := a.Sub(b)
	if err == nil {
		t.Error("expected error when subtracting different currencies")
	}
}

func TestMoneyMul(t *testing.T) {
	m, _ := NewMoney("100", CurrencyUSD)
	result := m.Mul(decimal.NewFromFloat(1.5))
	expected, _ := decimal.NewFromString("150")
	if !result.Amount.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, result.Amount)
	}
	if result.Currency != CurrencyUSD {
		t.Errorf("expected USD, got %s", result.Currency)
	}
}

func TestMoneyIsPositive(t *testing.T) {
	m, _ := NewMoney("1", CurrencyUSD)
	if !m.IsPositive() {
		t.Error("expected IsPositive to be true")
	}
}

func TestMoneyIsZero(t *testing.T) {
	m, _ := NewMoney("0", CurrencyUSD)
	if !m.IsZero() {
		t.Error("expected IsZero to be true")
	}
}

func TestMoneyIsNegative(t *testing.T) {
	m, _ := NewMoney("-1", CurrencyUSD)
	if !m.IsNegative() {
		t.Error("expected IsNegative to be true")
	}
}

func TestMoneyString(t *testing.T) {
	m, _ := NewMoney("1234.5", CurrencyUSD)
	expected := "1234.50 USD"
	if m.String() != expected {
		t.Errorf("expected %q, got %q", expected, m.String())
	}
}

func TestMoney8DecimalPrecision(t *testing.T) {
	m, err := NewMoney("0.00000001", CurrencyUSDT)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("0.00000001")
	if !m.Amount.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, m.Amount)
	}
}

func TestMoneyImmutability(t *testing.T) {
	original, _ := NewMoney("100", CurrencyUSD)
	other, _ := NewMoney("50", CurrencyUSD)

	originalAmount := original.Amount

	_, _ = original.Add(other)
	if !original.Amount.Equal(originalAmount) {
		t.Error("Add mutated the original Money value")
	}

	_, _ = original.Sub(other)
	if !original.Amount.Equal(originalAmount) {
		t.Error("Sub mutated the original Money value")
	}

	_ = original.Mul(decimal.NewFromInt(2))
	if !original.Amount.Equal(originalAmount) {
		t.Error("Mul mutated the original Money value")
	}
}

func TestValidateCurrencyValid(t *testing.T) {
	for c := range SupportedCurrencies {
		if err := ValidateCurrency(c); err != nil {
			t.Errorf("expected %s to be valid, got error: %v", c, err)
		}
	}
}

func TestValidateCurrencyInvalid(t *testing.T) {
	if err := ValidateCurrency("XYZ"); err == nil {
		t.Error("expected error for unsupported currency XYZ")
	}
}
