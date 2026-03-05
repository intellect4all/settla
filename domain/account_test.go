package domain

import (
	"testing"
)

func TestNormalBalanceFor(t *testing.T) {
	tests := []struct {
		accountType AccountType
		expected    NormalBalance
	}{
		{AccountTypeAsset, NormalBalanceDebit},
		{AccountTypeExpense, NormalBalanceDebit},
		{AccountTypeLiability, NormalBalanceCredit},
		{AccountTypeRevenue, NormalBalanceCredit},
	}
	for _, tt := range tests {
		result := NormalBalanceFor(tt.accountType)
		if result != tt.expected {
			t.Errorf("NormalBalanceFor(%s) = %s, want %s", tt.accountType, result, tt.expected)
		}
	}
}

func TestTenantAccountCode(t *testing.T) {
	code := TenantAccountCode("lemfi", "assets:bank:gbp:clearing")
	expected := "tenant:lemfi:assets:bank:gbp:clearing"
	if code != expected {
		t.Errorf("expected %q, got %q", expected, code)
	}
}

func TestIsSystemAccount(t *testing.T) {
	tests := []struct {
		code     string
		expected bool
	}{
		{"assets:crypto:usdt:tron", true},
		{"liabilities:system:clearing", true},
		{"tenant:lemfi:assets:bank:gbp:clearing", false},
		{"tenant:fincra:revenue:fees", false},
	}
	for _, tt := range tests {
		result := IsSystemAccount(tt.code)
		if result != tt.expected {
			t.Errorf("IsSystemAccount(%q) = %v, want %v", tt.code, result, tt.expected)
		}
	}
}

func TestParseAccountCode(t *testing.T) {
	segments, err := ParseAccountCode("tenant:lemfi:assets:bank:gbp:clearing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"tenant", "lemfi", "assets", "bank", "gbp", "clearing"}
	if len(segments) != len(expected) {
		t.Fatalf("expected %d segments, got %d", len(expected), len(segments))
	}
	for i, s := range segments {
		if s != expected[i] {
			t.Errorf("segment[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestParseAccountCodeEmpty(t *testing.T) {
	_, err := ParseAccountCode("")
	if err == nil {
		t.Error("expected error for empty account code")
	}
}

func TestParseAccountCodeSystem(t *testing.T) {
	segments, err := ParseAccountCode("assets:crypto:usdt:tron")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(segments) != 4 {
		t.Fatalf("expected 4 segments, got %d", len(segments))
	}
	if segments[0] != "assets" {
		t.Errorf("first segment = %q, want %q", segments[0], "assets")
	}
}
