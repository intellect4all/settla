package blockchain_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/blockchain/rpc"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

// fakeClient is a minimal domain.BlockchainClient for registry tests.
type fakeClient struct{ chain domain.CryptoChain }

func (f *fakeClient) Chain() domain.CryptoChain                                          { return f.chain }
func (f *fakeClient) GetBalance(_ context.Context, _, _ string) (decimal.Decimal, error) { return decimal.Zero, nil }
func (f *fakeClient) EstimateGas(_ context.Context, _ domain.TxRequest) (decimal.Decimal, error) {
	return decimal.Zero, nil
}
func (f *fakeClient) SendTransaction(_ context.Context, _ domain.TxRequest) (*domain.ChainTx, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeClient) GetTransaction(_ context.Context, _ string) (*domain.ChainTx, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeClient) SubscribeTransactions(_ context.Context, _ string, _ chan<- domain.ChainTx) error {
	return nil
}

// ── Registry tests ───────────────────────────────────────────────────────────

func TestRegistry_RegisterAndGetClient(t *testing.T) {
	r := blockchain.NewRegistry(slog.Default())

	tron := &fakeClient{chain: domain.ChainTron}
	eth := &fakeClient{chain: domain.ChainEthereum}
	r.Register(tron)
	r.Register(eth)

	got, err := r.GetClient(domain.ChainTron)
	if err != nil {
		t.Fatalf("GetClient(tron): unexpected error: %v", err)
	}
	if got.Chain() != domain.ChainTron {
		t.Errorf("GetClient(tron): chain = %q, want %q", got.Chain(), domain.ChainTron)
	}

	got, err = r.GetClient(domain.ChainEthereum)
	if err != nil {
		t.Fatalf("GetClient(ethereum): unexpected error: %v", err)
	}
	if got.Chain() != domain.ChainEthereum {
		t.Errorf("GetClient(ethereum): chain = %q, want %q", got.Chain(), domain.ChainEthereum)
	}
}

func TestRegistry_GetClient_UnknownChain(t *testing.T) {
	r := blockchain.NewRegistry(nil)

	_, err := r.GetClient(domain.CryptoChain("unknown"))
	if err == nil {
		t.Fatal("expected error for unknown chain, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention chain name, got: %v", err)
	}
}

func TestRegistry_Register_Replaces(t *testing.T) {
	r := blockchain.NewRegistry(nil)
	r.Register(&fakeClient{chain: domain.ChainTron})

	// Replace with a different instance for the same chain.
	newTron := &fakeClient{chain: domain.ChainTron}
	r.Register(newTron)

	got, _ := r.GetClient(domain.ChainTron)
	if got != newTron {
		t.Error("Register should replace the existing client for the same chain")
	}
}

func TestRegistry_MustGetClient_Panics(t *testing.T) {
	r := blockchain.NewRegistry(nil)

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("MustGetClient should panic for unknown chain")
		}
	}()
	r.MustGetClient(domain.CryptoChain("nonexistent"))
}

func TestRegistry_Chains(t *testing.T) {
	r := blockchain.NewRegistry(nil)
	r.Register(&fakeClient{chain: domain.ChainTron})
	r.Register(&fakeClient{chain: domain.ChainEthereum})
	r.Register(&fakeClient{chain: domain.ChainSolana})

	chains := r.Chains()
	slices.Sort(chains)

	want := []domain.CryptoChain{"ethereum", "solana", "tron"}
	if len(chains) != len(want) {
		t.Fatalf("Chains() = %v, want %v", chains, want)
	}
	for i := range chains {
		if chains[i] != want[i] {
			t.Errorf("Chains()[%d] = %q, want %q", i, chains[i], want[i])
		}
	}
}

// ── ExplorerURL tests ─────────────────────────────────────────────────────────

func TestExplorerURL(t *testing.T) {
	const hash = "abc123"

	tests := []struct {
		chain    domain.CryptoChain
		wantSub  string // required substring in the URL
		wantHash bool   // hash must appear in the URL
	}{
		{domain.ChainTron, "tronscan.org", true},
		{domain.ChainEthereum, "sepolia.etherscan.io", true},
		{domain.ChainBase, "basescan.org", true},
		{domain.ChainSolana, "explorer.solana.com", true},
		{domain.ChainSolana, "devnet", false}, // cluster param present
		{"unknown", "", false},                // empty for unknown chains
	}

	for _, tc := range tests {
		url := blockchain.ExplorerURL(tc.chain, hash)

		if tc.chain == "unknown" {
			if url != "" {
				t.Errorf("ExplorerURL(%q, _): want empty, got %q", tc.chain, url)
			}
			continue
		}

		if tc.wantSub != "" && !strings.Contains(url, tc.wantSub) {
			t.Errorf("ExplorerURL(%q, _) = %q: want substring %q", tc.chain, url, tc.wantSub)
		}
		if tc.wantHash && !strings.Contains(url, hash) {
			t.Errorf("ExplorerURL(%q, %q): tx hash missing from URL %q", tc.chain, hash, url)
		}
	}
}

// ── CircuitBreaker tests ──────────────────────────────────────────────────────

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := rpc.NewCircuitBreaker(3, 30*time.Second)

	if !cb.CanAttempt() {
		t.Fatal("new circuit breaker should be closed")
	}

	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.CanAttempt() {
		t.Error("circuit should still be closed after 2 failures (threshold is 3)")
	}

	cb.RecordFailure() // hits threshold
	if cb.CanAttempt() {
		t.Error("circuit should be open after 3 failures")
	}
	if cb.GetState() != rpc.StateOpen {
		t.Errorf("state = %v, want Open", cb.GetState())
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := rpc.NewCircuitBreaker(2, 30*time.Second)
	cb.RecordFailure()
	cb.RecordFailure() // open
	cb.Reset()

	if !cb.CanAttempt() {
		t.Error("circuit should be closed after Reset()")
	}
	if cb.GetFailures() != 0 {
		t.Errorf("failures = %d, want 0 after Reset()", cb.GetFailures())
	}
}

func TestCircuitBreaker_HalfOpenOnSuccess(t *testing.T) {
	cb := rpc.NewCircuitBreaker(1, time.Millisecond) // very short reset timeout
	cb.RecordFailure()                               // open

	time.Sleep(5 * time.Millisecond) // wait for reset timeout

	if !cb.CanAttempt() {
		t.Error("circuit should transition to HalfOpen after reset timeout")
	}
	if cb.GetState() != rpc.StateHalfOpen {
		t.Errorf("state = %v, want HalfOpen", cb.GetState())
	}

	cb.RecordSuccess()
	if cb.GetState() != rpc.StateClosed {
		t.Errorf("state = %v, want Closed after success in HalfOpen", cb.GetState())
	}
}

// ── RateLimiter tests ─────────────────────────────────────────────────────────

func TestRateLimiter_Allow(t *testing.T) {
	rl := rpc.NewRateLimiter(10, 2) // 10 req/s, burst 2

	// Should allow first two immediately.
	if !rl.Allow() {
		t.Error("first Allow() should succeed")
	}
	if !rl.Allow() {
		t.Error("second Allow() should succeed (within burst)")
	}
	// Bucket exhausted.
	if rl.Allow() {
		t.Error("third Allow() should fail (bucket exhausted)")
	}
}

func TestRateLimiter_Refills(t *testing.T) {
	rl := rpc.NewRateLimiter(1000, 1) // 1000/s, capacity 1
	rl.Allow()                        // drain

	time.Sleep(5 * time.Millisecond) // wait for refill

	if !rl.Allow() {
		t.Error("Allow() should succeed after token refill")
	}
}

// ── FailoverManager tests ─────────────────────────────────────────────────────

func TestFailoverManager_SuccessOnFirstEndpoint(t *testing.T) {
	eps := []string{"http://ep1", "http://ep2"}
	fm := rpc.NewFailoverManagerForTest(eps, nil, 0) // 0 backoff for fast tests

	called := ""
	err := fm.Execute(context.Background(), func(_ context.Context, ep string) error {
		called = ep
		return nil
	})

	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if called != "http://ep1" {
		t.Errorf("called endpoint = %q, want %q", called, "http://ep1")
	}
}

func TestFailoverManager_FailoverToSecondEndpoint(t *testing.T) {
	eps := []string{"http://ep1", "http://ep2"}
	fm := rpc.NewFailoverManagerForTest(eps, nil, 0)

	callCount := 0
	err := fm.Execute(context.Background(), func(_ context.Context, ep string) error {
		callCount++
		if ep == "http://ep1" {
			return errors.New("ep1 down")
		}
		return nil // ep2 succeeds
	})

	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (fail ep1, succeed ep2)", callCount)
	}
}

func TestFailoverManager_AllEndpointsFail(t *testing.T) {
	eps := []string{"http://ep1"}
	fm := rpc.NewFailoverManagerForTest(eps, nil, 0)

	callCount := 0
	err := fm.Execute(context.Background(), func(_ context.Context, _ string) error {
		callCount++
		return errors.New("always fails")
	})

	if err == nil {
		t.Fatal("Execute should return error when all endpoints fail")
	}
	// With 1 endpoint and maxAttempts=3, fn is called 3 times.
	if callCount != 3 {
		t.Errorf("callCount = %d, want 3", callCount)
	}
}

func TestFailoverManager_CircuitBreakerOpensAfterFailures(t *testing.T) {
	eps := []string{"http://ep1", "http://ep2"}
	fm := rpc.NewFailoverManagerForTest(eps, nil, 0)

	// Exhaust ep1's circuit (5 failures default).
	for range 5 {
		_ = fm.Execute(context.Background(), func(_ context.Context, ep string) error {
			if ep == "http://ep1" {
				return errors.New("ep1 down")
			}
			return nil
		})
	}

	// After ep1's circuit opens, subsequent calls should route to ep2.
	calledEPs := make(map[string]int)
	for range 3 {
		_ = fm.Execute(context.Background(), func(_ context.Context, ep string) error {
			calledEPs[ep]++
			return nil
		})
	}

	if calledEPs["http://ep1"] > 0 {
		t.Errorf("ep1 should have an open circuit breaker, but was called %d times", calledEPs["http://ep1"])
	}
}

func TestFailoverManager_ContextCancellation(t *testing.T) {
	eps := []string{"http://ep1"}
	fm := rpc.NewFailoverManagerForTest(eps, nil, 10*time.Millisecond) // non-zero backoff

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := fm.Execute(ctx, func(_ context.Context, _ string) error {
		return errors.New("should not matter")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}
