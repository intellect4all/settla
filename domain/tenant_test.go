package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFeeScheduleCalculateFeeOnRamp(t *testing.T) {
	fs := FeeSchedule{
		OnRampBPS: 40, // 40 bps = 0.40%
		MinFeeUSD: decimal.NewFromFloat(1.00),
		MaxFeeUSD: decimal.NewFromFloat(50.00),
	}
	fee, err := fs.CalculateFee(decimal.NewFromInt(1000), "onramp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("4") // 1000 * 40/10000 = 4.00
	if !fee.Equal(expected) {
		t.Errorf("expected fee %s, got %s", expected, fee)
	}
}

func TestFeeScheduleCalculateFeeOffRamp(t *testing.T) {
	fs := FeeSchedule{
		OffRampBPS: 30, // 30 bps = 0.30%
		MinFeeUSD:  decimal.NewFromFloat(1.00),
		MaxFeeUSD:  decimal.NewFromFloat(50.00),
	}
	fee, err := fs.CalculateFee(decimal.NewFromInt(2000), "offramp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("6") // 2000 * 30/10000 = 6.00
	if !fee.Equal(expected) {
		t.Errorf("expected fee %s, got %s", expected, fee)
	}
}

func TestFeeScheduleBelowMinReturnsMin(t *testing.T) {
	fs := FeeSchedule{
		OnRampBPS: 10,
		MinFeeUSD: decimal.NewFromFloat(5.00),
		MaxFeeUSD: decimal.NewFromFloat(100.00),
	}
	// 100 * 10/10000 = 0.10, which is below min of 5.00
	fee, err := fs.CalculateFee(decimal.NewFromInt(100), "onramp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("5")
	if !fee.Equal(expected) {
		t.Errorf("expected min fee %s, got %s", expected, fee)
	}
}

func TestFeeScheduleAboveMaxReturnsMax(t *testing.T) {
	fs := FeeSchedule{
		OnRampBPS: 100, // 1%
		MinFeeUSD: decimal.NewFromFloat(1.00),
		MaxFeeUSD: decimal.NewFromFloat(10.00),
	}
	// 5000 * 100/10000 = 50.00, which exceeds max of 10.00
	fee, err := fs.CalculateFee(decimal.NewFromInt(5000), "onramp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := decimal.NewFromString("10")
	if !fee.Equal(expected) {
		t.Errorf("expected max fee %s, got %s", expected, fee)
	}
}

func TestFeeScheduleInvalidFeeType(t *testing.T) {
	fs := FeeSchedule{OnRampBPS: 40}
	_, err := fs.CalculateFee(decimal.NewFromInt(1000), "invalid")
	if err == nil {
		t.Error("expected error for invalid fee type, got nil")
	}
}

func TestTenantIsActiveRequiresBothConditions(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		status   TenantStatus
		kyb      KYBStatus
		expected bool
	}{
		{"active+verified", TenantStatusActive, KYBStatusVerified, true},
		{"suspended", TenantStatusSuspended, KYBStatusVerified, false},
		{"pending KYB", TenantStatusActive, KYBStatusPending, false},
		{"onboarding+verified", TenantStatusOnboarding, KYBStatusVerified, false},
		{"active+in_review", TenantStatusActive, KYBStatusInReview, false},
		{"active+rejected", TenantStatusActive, KYBStatusRejected, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenant := &Tenant{
				Status:        tt.status,
				KYBStatus:     tt.kyb,
				KYBVerifiedAt: &now,
			}
			if tenant.IsActive() != tt.expected {
				t.Errorf("IsActive() = %v, want %v", tenant.IsActive(), tt.expected)
			}
		})
	}
}
