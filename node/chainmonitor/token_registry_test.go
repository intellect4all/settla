package chainmonitor

import (
	"testing"

	"github.com/intellect4all/settla/domain"
)

func TestTokenRegistry_LookupByContract(t *testing.T) {
	r := NewTokenRegistry()
	r.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Decimals: 6, IsActive: true},
		{Chain: "tron", Symbol: "USDC", ContractAddress: "TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8", Decimals: 6, IsActive: true},
		{Chain: "tron", Symbol: "OLD", ContractAddress: "TInactive", Decimals: 6, IsActive: false},
	})

	tok, ok := r.LookupByContract("tron", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	if !ok {
		t.Fatal("expected to find USDT token")
	}
	if tok.Symbol != "USDT" {
		t.Errorf("got symbol %q, want USDT", tok.Symbol)
	}

	_, ok = r.LookupByContract("tron", "TInactive")
	if ok {
		t.Error("inactive token should not be found")
	}

	_, ok = r.LookupByContract("ethereum", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	if ok {
		t.Error("wrong chain should not match")
	}
}

func TestTokenRegistry_TokensForChain(t *testing.T) {
	r := NewTokenRegistry()
	r.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "T1", Decimals: 6, IsActive: true},
		{Chain: "tron", Symbol: "USDC", ContractAddress: "T2", Decimals: 6, IsActive: true},
		{Chain: "ethereum", Symbol: "USDT", ContractAddress: "0x1", Decimals: 6, IsActive: true},
	})

	tronTokens := r.TokensForChain("tron")
	if len(tronTokens) != 2 {
		t.Errorf("expected 2 tron tokens, got %d", len(tronTokens))
	}

	ethTokens := r.TokensForChain("ethereum")
	if len(ethTokens) != 1 {
		t.Errorf("expected 1 ethereum token, got %d", len(ethTokens))
	}

	baseTokens := r.TokensForChain("base")
	if len(baseTokens) != 0 {
		t.Errorf("expected 0 base tokens, got %d", len(baseTokens))
	}
}

func TestTokenRegistry_ContractAddresses(t *testing.T) {
	r := NewTokenRegistry()
	r.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "T1", Decimals: 6, IsActive: true},
		{Chain: "tron", Symbol: "USDC", ContractAddress: "T2", Decimals: 6, IsActive: true},
	})

	addrs := r.ContractAddresses("tron")
	if len(addrs) != 2 {
		t.Errorf("expected 2 contract addresses, got %d", len(addrs))
	}
}

func TestTokenRegistry_ReloadAtomic(t *testing.T) {
	r := NewTokenRegistry()

	// Initial load
	r.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "T1", Decimals: 6, IsActive: true},
	})
	if len(r.TokensForChain("tron")) != 1 {
		t.Fatal("expected 1 token after first reload")
	}

	// Replace with different set
	r.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDC", ContractAddress: "T2", Decimals: 6, IsActive: true},
		{Chain: "tron", Symbol: "DAI", ContractAddress: "T3", Decimals: 18, IsActive: true},
	})
	if len(r.TokensForChain("tron")) != 2 {
		t.Errorf("expected 2 tokens after second reload, got %d", len(r.TokensForChain("tron")))
	}

	// Original token should be gone
	_, ok := r.LookupByContract("tron", "T1")
	if ok {
		t.Error("old token T1 should not be present after reload")
	}
}
