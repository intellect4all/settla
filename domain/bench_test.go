package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BenchmarkValidateEntries measures the performance of entry validation.
// This is a pure function with no I/O - should be extremely fast.
//
// Target: <1μs per validation (50,000+ validations/sec)
func BenchmarkValidateEntries(b *testing.B) {
	// Create a 4-line balanced entry (typical transfer posting)
	lines := []EntryLine{
		{
			ID:          uuid.New(),
			AccountCode: "tenant:lemfi:assets:bank:gbp:clearing",
			EntryType:   EntryTypeDebit,
			Amount:      decimal.NewFromInt(100000),
			Currency:    CurrencyGBP,
			Description: "Debit clearing",
		},
		{
			ID:          uuid.New(),
			AccountCode: "assets:crypto:usdt:tron",
			EntryType:   EntryTypeCredit,
			Amount:      decimal.NewFromInt(99900),
			Currency:    CurrencyGBP,
			Description: "Credit crypto asset",
		},
		{
			ID:          uuid.New(),
			AccountCode: "expenses:provider:onramp",
			EntryType:   EntryTypeDebit,
			Amount:      decimal.NewFromInt(100),
			Currency:    CurrencyGBP,
			Description: "On-ramp fee",
		},
		{
			ID:          uuid.New(),
			AccountCode: "revenue:fees:settlement",
			EntryType:   EntryTypeCredit,
			Amount:      decimal.NewFromInt(100),
			Currency:    CurrencyGBP,
			Description: "Settlement fee revenue",
		},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = ValidateEntries(lines)
	}
}

// BenchmarkValidateEntries_TwoLine measures validation for simple 2-line entries.
//
// Target: <500ns per validation
func BenchmarkValidateEntries_TwoLine(b *testing.B) {
	lines := []EntryLine{
		{
			ID:          uuid.New(),
			AccountCode: "tenant:lemfi:assets:bank:gbp:clearing",
			EntryType:   EntryTypeDebit,
			Amount:      decimal.NewFromInt(1000),
			Currency:    CurrencyGBP,
			Description: "Debit",
		},
		{
			ID:          uuid.New(),
			AccountCode: "tenant:lemfi:liabilities:customer:pending",
			EntryType:   EntryTypeCredit,
			Amount:      decimal.NewFromInt(1000),
			Currency:    CurrencyGBP,
			Description: "Credit",
		},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = ValidateEntries(lines)
	}
}

// BenchmarkTransferTransitionTo measures state machine transition performance.
// This is a pure memory operation - should be extremely fast.
//
// Target: <100ns per transition
func BenchmarkTransferTransitionTo(b *testing.B) {
	transfer := &Transfer{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		Status:         TransferStatusCreated,
		Version:        1,
		SourceCurrency: CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   CurrencyNGN,
		DestAmount:     decimal.NewFromInt(2000000),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Reset transfer status for each iteration
		transfer.Status = TransferStatusCreated
		transfer.Version = 1

		_, _ = transfer.TransitionTo(TransferStatusFunded)
	}
}

// BenchmarkTransferCanTransitionTo measures transition validation performance.
//
// Target: <50ns per check
func BenchmarkTransferCanTransitionTo(b *testing.B) {
	transfer := &Transfer{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		Status:         TransferStatusCreated,
		SourceCurrency: CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   CurrencyNGN,
		DestAmount:     decimal.NewFromInt(2000000),
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = transfer.CanTransitionTo(TransferStatusFunded)
	}
}

// BenchmarkTransferTransition_FullLifecycle measures a complete state transition chain.
//
// Target: <500ns for full lifecycle (6 transitions)
func BenchmarkTransferTransition_FullLifecycle(b *testing.B) {
	transitions := []TransferStatus{
		TransferStatusFunded,
		TransferStatusOnRamping,
		TransferStatusSettling,
		TransferStatusOffRamping,
		TransferStatusCompleting,
		TransferStatusCompleted,
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		transfer := &Transfer{
			ID:             uuid.New(),
			TenantID:       uuid.New(),
			Status:         TransferStatusCreated,
			Version:        1,
			SourceCurrency: CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(1000),
			DestCurrency:   CurrencyNGN,
			DestAmount:     decimal.NewFromInt(2000000),
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}

		for _, target := range transitions {
			_, _ = transfer.TransitionTo(target)
		}
	}
}

// BenchmarkPositionAvailable measures position available balance calculation.
//
// Target: <50ns per calculation
func BenchmarkPositionAvailable(b *testing.B) {
	position := &Position{
		ID:            uuid.New(),
		TenantID:      uuid.New(),
		Currency:      CurrencyUSD,
		Location:      "bank:chase",
		Balance:       decimal.NewFromInt(1000000),
		Locked:        decimal.NewFromInt(100000),
		MinBalance:    decimal.NewFromInt(50000),
		TargetBalance: decimal.NewFromInt(2000000),
		UpdatedAt:     time.Now().UTC(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = position.Available()
	}
}

// BenchmarkPositionCanLock measures position lock validation.
//
// Target: <50ns per check
func BenchmarkPositionCanLock(b *testing.B) {
	position := &Position{
		ID:            uuid.New(),
		TenantID:      uuid.New(),
		Currency:      CurrencyUSD,
		Location:      "bank:chase",
		Balance:       decimal.NewFromInt(1000000),
		Locked:        decimal.NewFromInt(100000),
		MinBalance:    decimal.NewFromInt(50000),
		TargetBalance: decimal.NewFromInt(2000000),
		UpdatedAt:     time.Now().UTC(),
	}
	amount := decimal.NewFromInt(5000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = position.CanLock(amount)
	}
}

// BenchmarkQuoteIsExpired measures quote expiration check.
//
// Target: <20ns per check
func BenchmarkQuoteIsExpired(b *testing.B) {
	quote := &Quote{
		ID:             uuid.New(),
		TenantID:       uuid.New(),
		SourceCurrency: CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   CurrencyNGN,
		DestAmount:     decimal.NewFromInt(2000000),
		FXRate:         decimal.NewFromFloat(2000),
		Fees: FeeBreakdown{
			OnRampFee:   decimal.NewFromInt(10),
			NetworkFee:  decimal.NewFromInt(5),
			OffRampFee:  decimal.NewFromInt(15),
			TotalFeeUSD: decimal.NewFromInt(30),
		},
		Route: RouteInfo{
			Chain:           "tron",
			StableCoin:      CurrencyUSDT,
			OnRampProvider:  "provider1",
			OffRampProvider: "provider2",
		},
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		CreatedAt: time.Now().UTC(),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = quote.IsExpired()
	}
}

// BenchmarkValidateCurrency measures currency validation performance.
//
// Target: <10ns per validation
func BenchmarkValidateCurrency(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = ValidateCurrency(CurrencyUSD)
	}
}

// BenchmarkMoneyAdd measures decimal addition performance (common operation).
//
// Target: <100ns per addition
func BenchmarkMoneyAdd(b *testing.B) {
	a := decimal.NewFromInt(1000000)
	c := decimal.NewFromInt(500000)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = a.Add(c)
	}
}

// BenchmarkMoneyMul measures decimal multiplication performance (FX calculations).
//
// Target: <150ns per multiplication
func BenchmarkMoneyMul(b *testing.B) {
	amount := decimal.NewFromInt(1000)
	rate := decimal.NewFromFloat(2000.5)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = amount.Mul(rate)
	}
}
