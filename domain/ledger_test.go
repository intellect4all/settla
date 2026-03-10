package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestValidateEntriesBalanced(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "revenue:fees", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
	}
	if err := ValidateEntries(lines); err != nil {
		t.Errorf("expected balanced entries to pass, got: %v", err)
	}
}

func TestValidateEntriesImbalanced(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "revenue:fees", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(99), Currency: CurrencyUSD}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected imbalanced entries to fail")
	}
	de, ok := err.(*DomainError)
	if !ok {
		t.Fatalf("expected *DomainError, got %T", err)
	}
	if de.Code() != CodeLedgerImbalance {
		t.Errorf("expected code %s, got %s", CodeLedgerImbalance, de.Code())
	}
}

func TestValidateEntriesEmpty(t *testing.T) {
	err := ValidateEntries(nil)
	if err == nil {
		t.Error("expected empty entries to fail")
	}
}

func TestValidateEntriesSingleLine(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected single line to fail")
	}
}

func TestValidateEntriesNegativeAmount(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(-100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "revenue:fees", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected negative amount to fail")
	}
}

func TestValidateEntriesZeroAmount(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.Zero, Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "revenue:fees", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected zero amount to fail")
	}
}

func TestValidateEntriesMultiCurrencyBalanced(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:usd", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "liabilities:usd", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:gbp", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(80), Currency: CurrencyGBP}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "liabilities:gbp", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(80), Currency: CurrencyGBP}},
	}
	if err := ValidateEntries(lines); err != nil {
		t.Errorf("expected multi-currency balanced entries to pass, got: %v", err)
	}
}

func TestValidateEntriesMultiCurrencyImbalanced(t *testing.T) {
	lines := []EntryLine{
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:usd", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "liabilities:usd", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "assets:gbp", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(80), Currency: CurrencyGBP}},
		{ID: uuid.New(), Posting: Posting{AccountCode: "liabilities:gbp", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(70), Currency: CurrencyGBP}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected multi-currency imbalanced entries to fail")
	}
}

func TestValidateEntriesDuplicateIDs(t *testing.T) {
	id := uuid.New()
	lines := []EntryLine{
		{ID: id, Posting: Posting{AccountCode: "assets:cash", EntryType: EntryTypeDebit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
		{ID: id, Posting: Posting{AccountCode: "revenue:fees", EntryType: EntryTypeCredit, Amount: decimal.NewFromInt(100), Currency: CurrencyUSD}},
	}
	err := ValidateEntries(lines)
	if err == nil {
		t.Error("expected duplicate IDs to fail")
	}
}
