package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPositionAvailable(t *testing.T) {
	p := &Position{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Balance:  decimal.NewFromInt(1000),
		Locked:   decimal.NewFromInt(300),
	}
	expected := decimal.NewFromInt(700)
	if !p.Available().Equal(expected) {
		t.Errorf("expected available %s, got %s", expected, p.Available())
	}
}

func TestPositionCanLockSufficient(t *testing.T) {
	p := &Position{
		Balance: decimal.NewFromInt(1000),
		Locked:  decimal.NewFromInt(300),
	}
	if !p.CanLock(decimal.NewFromInt(700)) {
		t.Error("expected CanLock(700) to be true with 700 available")
	}
	if !p.CanLock(decimal.NewFromInt(500)) {
		t.Error("expected CanLock(500) to be true with 700 available")
	}
}

func TestPositionCanLockInsufficient(t *testing.T) {
	p := &Position{
		Balance: decimal.NewFromInt(1000),
		Locked:  decimal.NewFromInt(300),
	}
	if p.CanLock(decimal.NewFromInt(701)) {
		t.Error("expected CanLock(701) to be false with 700 available")
	}
}

func TestPositionIsAboveMinimum(t *testing.T) {
	p := &Position{
		Balance:    decimal.NewFromInt(1000),
		MinBalance: decimal.NewFromInt(500),
	}
	if !p.IsAboveMinimum() {
		t.Error("expected IsAboveMinimum to be true when balance >= min")
	}

	p.Balance = decimal.NewFromInt(499)
	if p.IsAboveMinimum() {
		t.Error("expected IsAboveMinimum to be false when balance < min")
	}

	p.Balance = decimal.NewFromInt(500)
	if !p.IsAboveMinimum() {
		t.Error("expected IsAboveMinimum to be true when balance == min")
	}
}
