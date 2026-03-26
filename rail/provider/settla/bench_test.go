package settla

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/wallet"
)

// ============================================================================
// Throughput benchmarks for 50M transactions/day target validation.
//
// Target: 580 TPS sustained, 3,000–5,000 TPS peak.
//
// Each benchmark measures a hot-path component. The overall system capacity
// is limited by the slowest synchronous component in the request path.
//
// Run: go test -bench=Benchmark -benchmem -benchtime=5s ./rail/provider/settla/...
// ============================================================================

// --- FX Oracle benchmarks ---

func BenchmarkFXOracle_GetRate(b *testing.B) {
	oracle := NewFXOracle()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = oracle.GetRate("GBP", "USD")
		}
	})
}

func BenchmarkFXOracle_CrossRate(b *testing.B) {
	oracle := NewFXOracle()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = oracle.GetRate("GBP", "NGN")
		}
	})
}

// --- Fiat Simulator benchmarks ---

func BenchmarkFiatSimulator_InitiateCollection(b *testing.B) {
	sim := NewFiatSimulator(SimulatorConfig{
		FailureRate:    0,
		CurrencyDelays: benchFastDelays(),
	})
	ctx := context.Background()
	amount := decimal.NewFromInt(100)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = sim.SimulateCollection(ctx, amount, "GBP", fmt.Sprintf("ref-%d", i))
			i++
		}
	})
}

func BenchmarkFiatSimulator_GetStatus(b *testing.B) {
	sim := NewFiatSimulator(SimulatorConfig{
		FailureRate:    0,
		CurrencyDelays: benchFastDelays(),
	})
	ctx := context.Background()
	// Pre-populate 10K transactions
	ids := make([]string, 10000)
	for i := range ids {
		tx, _ := sim.SimulateCollection(ctx, decimal.NewFromInt(100), "GBP", fmt.Sprintf("pre-%d", i))
		ids[i] = tx.ID
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = sim.GetStatus(ids[i%len(ids)])
			i++
		}
	})
}

// --- On-Ramp Provider benchmarks ---

func BenchmarkOnRamp_GetQuote(b *testing.B) {
	p, _ := newBenchOnRampProvider()
	ctx := context.Background()
	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyUSDT,
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = p.GetQuote(ctx, req)
		}
	})
}

func BenchmarkOnRamp_Execute(b *testing.B) {
	p, _ := newBenchOnRampProvider()
	ctx := context.Background()
	currencies := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = p.Execute(ctx, domain.OnRampRequest{
				Amount:       decimal.NewFromInt(100),
				FromCurrency: currencies[i%len(currencies)],
				ToCurrency:   stables[i%len(stables)],
				Reference:    fmt.Sprintf("bench-%d", i),
			})
			i++
		}
	})
}

func BenchmarkOnRamp_GetStatus(b *testing.B) {
	p, _ := newBenchOnRampProvider()
	ctx := context.Background()
	// Pre-populate transactions
	ids := make([]string, 1000)
	for i := range ids {
		tx, _ := p.Execute(ctx, domain.OnRampRequest{
			Amount:       decimal.NewFromInt(100),
			FromCurrency: domain.CurrencyGBP,
			ToCurrency:   domain.CurrencyUSDT,
			Reference:    fmt.Sprintf("pre-%d", i),
		})
		ids[i] = tx.ID
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = p.GetStatus(ctx, ids[i%len(ids)])
			i++
		}
	})
}

// --- Off-Ramp Provider benchmarks ---

func BenchmarkOffRamp_GetQuote(b *testing.B) {
	p := newBenchOffRampProvider()
	ctx := context.Background()
	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyUSDT,
		SourceAmount:   decimal.NewFromInt(100),
		DestCurrency:   domain.CurrencyNGN,
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = p.GetQuote(ctx, req)
		}
	})
}

func BenchmarkOffRamp_Execute(b *testing.B) {
	p := newBenchOffRampProvider()
	ctx := context.Background()
	fiats := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = p.Execute(ctx, domain.OffRampRequest{
				Amount:       decimal.NewFromInt(50),
				FromCurrency: stables[i%len(stables)],
				ToCurrency:   fiats[i%len(fiats)],
				Reference:    fmt.Sprintf("bench-off-%d", i),
			})
			i++
		}
	})
}

// --- Blockchain Registry lookup benchmark ---

func BenchmarkRegistryGetClient(b *testing.B) {
	reg := newFakeChainRegistry(domain.ChainTron, domain.ChainEthereum, domain.ChainBase, domain.ChainSolana)
	chains := []domain.CryptoChain{domain.ChainTron, domain.ChainEthereum, domain.ChainBase, domain.ChainSolana}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = reg.GetClient(chains[i%len(chains)])
			i++
		}
	})
}

// --- Throughput simulation tests ---
// These are not Go benchmarks but time-bounded stress tests that measure
// actual TPS to validate against the 50M tx/day target.

func TestThroughput_FXOracle_PeakTPS(t *testing.T) {
	oracle := NewFXOracle()
	ctx := context.Background()
	_ = ctx

	const targetTPS = 5000
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				_, _ = oracle.GetRate("GBP", "NGN")
				ops.Add(1)
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("FX Oracle: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("FX Oracle throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

func TestThroughput_FiatSimulator_SustainedTPS(t *testing.T) {
	sim := NewFiatSimulator(SimulatorConfig{
		FailureRate:    0,
		CurrencyDelays: benchFastDelays(),
	})
	ctx := context.Background()

	const targetTPS = 5000
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				_, _ = sim.SimulateCollection(ctx, decimal.NewFromInt(100), "GBP", fmt.Sprintf("tps-%d", i))
				ops.Add(1)
				i++
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("Fiat Simulator initiate: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("Fiat Simulator throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

func TestThroughput_OnRampQuote_PeakTPS(t *testing.T) {
	p, _ := newBenchOnRampProvider()
	ctx := context.Background()

	const targetTPS = 5000
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	currencies := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				_, _ = p.GetQuote(ctx, domain.QuoteRequest{
					SourceCurrency: currencies[i%len(currencies)],
					SourceAmount:   decimal.NewFromInt(100),
					DestCurrency:   stables[i%len(stables)],
				})
				ops.Add(1)
				i++
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("On-Ramp GetQuote: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("On-Ramp GetQuote throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

func TestThroughput_OnRampExecute_SustainedTPS(t *testing.T) {
	p, _ := newBenchOnRampProvider()
	ctx := context.Background()

	// Execute initiates async work (goroutine per tx); sustained target
	// is the initiation rate, not completion rate.
	const targetTPS = 580
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	currencies := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				_, _ = p.Execute(ctx, domain.OnRampRequest{
					Amount:       decimal.NewFromInt(100),
					FromCurrency: currencies[i%len(currencies)],
					ToCurrency:   stables[i%len(stables)],
					Reference:    fmt.Sprintf("sust-%d", i),
				})
				ops.Add(1)
				i++
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("On-Ramp Execute: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("On-Ramp Execute throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

func TestThroughput_OffRampQuote_PeakTPS(t *testing.T) {
	p := newBenchOffRampProvider()
	ctx := context.Background()

	const targetTPS = 5000
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	fiats := []domain.Currency{domain.CurrencyGBP, domain.CurrencyNGN, domain.CurrencyUSD}
	stables := []domain.Currency{domain.CurrencyUSDT, domain.CurrencyUSDC}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				_, _ = p.GetQuote(ctx, domain.QuoteRequest{
					SourceCurrency: stables[i%len(stables)],
					SourceAmount:   decimal.NewFromInt(100),
					DestCurrency:   fiats[i%len(fiats)],
				})
				ops.Add(1)
				i++
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("Off-Ramp GetQuote: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("Off-Ramp GetQuote throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

func TestThroughput_RegistryLookup_PeakTPS(t *testing.T) {
	reg := newFakeChainRegistry(domain.ChainTron, domain.ChainEthereum, domain.ChainBase, domain.ChainSolana)
	chains := []domain.CryptoChain{domain.ChainTron, domain.ChainEthereum, domain.ChainBase, domain.ChainSolana}

	const targetTPS = 50000 // registry must be 10x faster than request path
	const duration = 3 * time.Second

	var ops atomic.Int64
	deadline := time.Now().Add(duration)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for time.Now().Before(deadline) {
				_, _ = reg.GetClient(chains[i%len(chains)])
				ops.Add(1)
				i++
			}
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	tps := float64(ops.Load()) / elapsed
	t.Logf("Registry GetClient: %.0f ops/sec (target: %d TPS)", tps, targetTPS)
	if tps < float64(targetTPS) {
		t.Errorf("Registry GetClient throughput %.0f TPS is below target %d TPS", tps, targetTPS)
	}
}

// --- helpers ---

func benchFastDelays() map[string][2]time.Duration {
	d := [2]time.Duration{1 * time.Millisecond, 2 * time.Millisecond}
	return map[string][2]time.Duration{
		"GBP": d, "NGN": d, "USD": d, "EUR": d, "GHS": d,
	}
}

func newBenchOnRampProvider() (*OnRampProvider, *fakeChainRegistry) {
	fxOracle := NewFXOracle()
	fiatSim := NewFiatSimulator(SimulatorConfig{
		FailureRate:    0,
		CurrencyDelays: benchFastDelays(),
	})
	chainReg := newFakeChainRegistry(domain.ChainTron, domain.ChainEthereum, domain.ChainBase, domain.ChainSolana)
	walletMgr := newFakeWalletManager()
	p := NewOnRampProvider(fxOracle, fiatSim, chainReg, walletMgr, DefaultOnRampConfig())
	return p, chainReg
}

func newBenchOffRampProvider() *OffRampProvider {
	fxOracle := NewFXOracle()
	fiatSim := NewFiatSimulator(SimulatorConfig{
		FailureRate:    0,
		CurrencyDelays: benchFastDelays(),
	})
	return NewOffRampProvider(fxOracle, fiatSim, nil, nil, nil)
}

// --- Blockchain client benchmarks (using fake) ---

func BenchmarkBlockchainClient_SendTx(b *testing.B) {
	client := &fakeBlockchainClient{chain: "tron"}
	ctx := context.Background()
	req := domain.TxRequest{
		From:   "TFakeAddress001",
		To:     "TFakeAddress002",
		Token:  "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj",
		Amount: decimal.NewFromInt(100),
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = client.SendTransaction(ctx, req)
		}
	})
}

func BenchmarkBlockchainClient_GetBalance(b *testing.B) {
	client := &fakeBlockchainClient{chain: "tron"}
	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = client.GetBalance(ctx, "TFakeAddress001", "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj")
		}
	})
}

// --- Wallet manager fake benchmarks ---

func BenchmarkWalletManager_GetSystemWallet(b *testing.B) {
	mgr := newFakeWalletManager()
	chains := []wallet.Chain{wallet.ChainTron, wallet.ChainEthereum, wallet.ChainBase, wallet.ChainSolana}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = mgr.GetSystemWallet(chains[i%len(chains)])
			i++
		}
	})
}
