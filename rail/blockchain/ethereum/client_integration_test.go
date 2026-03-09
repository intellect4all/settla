//go:build integration

package ethereum

import (
	"context"
	"testing"
)

// TestIntegrationSepoliaBalance fetches a real ETH balance from Sepolia testnet.
// Run with: go test -tags integration ./rail/blockchain/ethereum/...
func TestIntegrationSepoliaBalance(t *testing.T) {
	c, err := NewClient(SepoliaConfig, noopSigner{}, nil)
	if err != nil {
		t.Skipf("cannot connect to Sepolia: %v", err)
	}
	defer c.Close()

	// Well-known Ethereum Foundation address
	addr := "0xde0B295669a9FD93d5F28D9Ec85E40f4cb697BAe"
	bal, err := c.GetBalance(context.Background(), addr, "ETH")
	if err != nil {
		t.Fatalf("GetBalance ETH: %v", err)
	}
	t.Logf("Sepolia ETH balance of %s: %s ETH", addr, bal.String())
}

// TestIntegrationSepoliaUSDCBalance fetches a real USDC balance from Sepolia testnet.
func TestIntegrationSepoliaUSDCBalance(t *testing.T) {
	c, err := NewClient(SepoliaConfig, noopSigner{}, nil)
	if err != nil {
		t.Skipf("cannot connect to Sepolia: %v", err)
	}
	defer c.Close()

	addr := "0xde0B295669a9FD93d5F28D9Ec85E40f4cb697BAe"
	bal, err := c.GetBalance(context.Background(), addr, "USDC")
	if err != nil {
		t.Fatalf("GetBalance USDC: %v", err)
	}
	t.Logf("Sepolia USDC balance of %s: %s USDC", addr, bal.String())
}

// TestIntegrationBaseSepoliaBalance fetches a real ETH balance from Base Sepolia testnet.
func TestIntegrationBaseSepoliaBalance(t *testing.T) {
	c, err := NewClient(BaseSepoliaConfig, noopSigner{}, nil)
	if err != nil {
		t.Skipf("cannot connect to Base Sepolia: %v", err)
	}
	defer c.Close()

	addr := "0xde0B295669a9FD93d5F28D9Ec85E40f4cb697BAe"
	bal, err := c.GetBalance(context.Background(), addr, "ETH")
	if err != nil {
		t.Fatalf("GetBalance ETH Base Sepolia: %v", err)
	}
	t.Logf("Base Sepolia ETH balance of %s: %s ETH", addr, bal.String())
}

// TestIntegrationGetTransaction looks up a known transaction on Sepolia.
func TestIntegrationGetTransaction(t *testing.T) {
	c, err := NewClient(SepoliaConfig, noopSigner{}, nil)
	if err != nil {
		t.Skipf("cannot connect to Sepolia: %v", err)
	}
	defer c.Close()

	// Genesis-era transaction (won't be found on Sepolia, expect UNKNOWN)
	hash := "0x0000000000000000000000000000000000000000000000000000000000000001"
	tx, err := c.GetTransaction(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	t.Logf("Transaction %s status: %s", hash, tx.Status)
}
