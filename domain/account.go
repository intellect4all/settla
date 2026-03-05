package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AccountType classifies a ledger account.
type AccountType string

const (
	// AccountTypeAsset represents accounts that track owned resources.
	AccountTypeAsset AccountType = "ASSET"
	// AccountTypeLiability represents accounts that track obligations.
	AccountTypeLiability AccountType = "LIABILITY"
	// AccountTypeRevenue represents accounts that track income.
	AccountTypeRevenue AccountType = "REVENUE"
	// AccountTypeExpense represents accounts that track costs.
	AccountTypeExpense AccountType = "EXPENSE"
)

// NormalBalance indicates whether an account normally carries a debit or credit balance.
type NormalBalance string

const (
	// NormalBalanceDebit means the account increases with debits (assets, expenses).
	NormalBalanceDebit NormalBalance = "DEBIT"
	// NormalBalanceCredit means the account increases with credits (liabilities, revenue).
	NormalBalanceCredit NormalBalance = "CREDIT"
)

// NormalBalanceFor returns the normal balance direction for a given account type.
// Assets and expenses have debit normal balances; liabilities and revenue have credit.
func NormalBalanceFor(at AccountType) NormalBalance {
	switch at {
	case AccountTypeAsset, AccountTypeExpense:
		return NormalBalanceDebit
	case AccountTypeLiability, AccountTypeRevenue:
		return NormalBalanceCredit
	default:
		return NormalBalanceDebit
	}
}

// Account represents a ledger account identified by a hierarchical code.
//
// Tenant-scoped accounts use format: tenant:{slug}:assets:bank:gbp:clearing
// System accounts omit the tenant prefix: assets:crypto:usdt:tron
type Account struct {
	ID            uuid.UUID
	TenantID      *uuid.UUID // nil for system accounts
	Code          string
	Name          string
	Type          AccountType
	Currency      Currency
	NormalBalance NormalBalance
	ParentID      *uuid.UUID
	IsActive      bool
	Metadata      map[string]string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TenantAccountCode builds a tenant-scoped account code.
// Format: tenant:{slug}:{path}
// Example: TenantAccountCode("lemfi", "assets:bank:gbp:clearing") → "tenant:lemfi:assets:bank:gbp:clearing"
func TenantAccountCode(tenantSlug, path string) string {
	return fmt.Sprintf("tenant:%s:%s", tenantSlug, path)
}

// IsSystemAccount returns true if the account code does NOT have a "tenant:" prefix.
// System accounts are shared across all tenants (e.g., "assets:crypto:usdt:tron").
func IsSystemAccount(code string) bool {
	return !strings.HasPrefix(code, "tenant:")
}

// ParseAccountCode splits an account code on ":" and returns the segments.
// Returns an error if the code is empty.
func ParseAccountCode(code string) ([]string, error) {
	if code == "" {
		return nil, fmt.Errorf("settla-domain: empty account code")
	}
	return strings.Split(code, ":"), nil
}
