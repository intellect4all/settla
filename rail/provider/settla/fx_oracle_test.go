package settla

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"
)

func TestFXOracle_KnownRates(t *testing.T) {
	o := NewFXOracle()

	tests := []struct {
		from    string
		to      string
		baseMin float64
		baseMax float64
	}{
		// USD identity
		{"USD", "USD", 1.0 * (1 - jitterPct), 1.0 * (1 + jitterPct)},
		// GBP/USD ≈ 1.2645
		{"GBP", "USD", 1.2645 * (1 - jitterPct), 1.2645 * (1 + jitterPct)},
		// USD/GBP ≈ 1/1.2645
		{"USD", "GBP", (1.0 / 1.2645) * (1 - jitterPct), (1.0 / 1.2645) * (1 + jitterPct)},
		// EUR/USD ≈ 1.0835
		{"EUR", "USD", 1.0835 * (1 - jitterPct), 1.0835 * (1 + jitterPct)},
		// USD/NGN ≈ 1755.20
		{"USD", "NGN", 1755.20 * (1 - jitterPct), 1755.20 * (1 + jitterPct)},
		// NGN/USD ≈ 1/1755.20
		{"NGN", "USD", (1.0 / 1755.20) * (1 - jitterPct), (1.0 / 1755.20) * (1 + jitterPct)},
		// USD/GHS ≈ 15.80
		{"USD", "GHS", 15.80 * (1 - jitterPct), 15.80 * (1 + jitterPct)},
	}

	for _, tc := range tests {
		rate, err := o.GetRate(tc.from, tc.to)
		if err != nil {
			t.Errorf("GetRate(%s, %s) unexpected error: %v", tc.from, tc.to, err)
			continue
		}
		f, _ := rate.Float64()
		if f < tc.baseMin || f > tc.baseMax {
			t.Errorf("GetRate(%s, %s) = %v, want [%v, %v]", tc.from, tc.to, f, tc.baseMin, tc.baseMax)
		}
	}
}

func TestFXOracle_CrossRate(t *testing.T) {
	o := NewFXOracle()

	// GBP/NGN should equal approximately GBP/USD × USD/NGN
	// GBP/USD ≈ 1.2645, USD/NGN ≈ 1755.20 → GBP/NGN ≈ 2219.24
	expected := 1.2645 * 1755.20

	rate, err := o.GetRate("GBP", "NGN")
	if err != nil {
		t.Fatalf("GetRate(GBP, NGN): %v", err)
	}
	f, _ := rate.Float64()

	lo := expected * (1 - jitterPct)
	hi := expected * (1 + jitterPct)
	if f < lo || f > hi {
		t.Errorf("GBP/NGN cross rate = %.4f, want [%.4f, %.4f]", f, lo, hi)
	}
}

func TestFXOracle_InverseRate(t *testing.T) {
	o := NewFXOracle()

	// We can't test exact inverse because jitter is applied per call,
	// but we can verify that GetRate(A,B) * GetRate(B,A) ≈ 1 (within 2×jitter²).
	// More practically: verify both directions are in expected ranges.
	gbpUSD, err := o.GetRate("GBP", "USD")
	if err != nil {
		t.Fatal(err)
	}
	usdGBP, err := o.GetRate("USD", "GBP")
	if err != nil {
		t.Fatal(err)
	}

	product := gbpUSD.Mul(usdGBP)
	// product should be very close to 1.0 (within a small tolerance due to independent jitter calls)
	one := decimal.NewFromFloat(1.0)
	tolerance := decimal.NewFromFloat(0.01) // 1% tolerance for two independent jitter calls
	diff := product.Sub(one).Abs()
	if diff.GreaterThan(tolerance) {
		t.Errorf("GBP/USD × USD/GBP = %v, want ≈1.0 (diff=%v)", product, diff)
	}
}

func TestFXOracle_UnknownCurrency(t *testing.T) {
	o := NewFXOracle()

	_, err := o.GetRate("XXX", "USD")
	if err == nil {
		t.Error("expected error for unknown currency XXX, got nil")
	}

	_, err = o.GetRate("USD", "ZZZ")
	if err == nil {
		t.Error("expected error for unknown currency ZZZ, got nil")
	}
}

func TestFXOracle_JitterRange(t *testing.T) {
	o := NewFXOracle()

	// Run many samples to verify jitter stays within ±0.15%
	base := 1.2645 // GBP/USD
	lo := base * (1 - jitterPct)
	hi := base * (1 + jitterPct)

	for i := 0; i < 1000; i++ {
		rate, err := o.GetRate("GBP", "USD")
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		f, _ := rate.Float64()
		if f < lo || f > hi {
			t.Errorf("iteration %d: GBP/USD = %.6f out of jitter range [%.6f, %.6f]", i, f, lo, hi)
		}
	}
}

func TestFXOracle_ConcurrentAccess(t *testing.T) {
	o := NewFXOracle()

	var wg sync.WaitGroup
	errs := make(chan error, 200)

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := o.GetRate("GBP", "NGN")
			if err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			_, err := o.GetRate("EUR", "USD")
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent GetRate error: %v", err)
	}
}
