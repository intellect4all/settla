package mock

import (
	"context"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// BlockchainClient is a mock blockchain client for testing and demos.
// Simulates a Tron-like chain with configurable gas fees and balances.
type BlockchainClient struct {
	chain    string
	gasFee   decimal.Decimal
	mu       sync.Mutex
	balances map[string]decimal.Decimal // address:token → balance
	txCount  int
}

// NewBlockchainClient creates a mock blockchain client.
func NewBlockchainClient(chain string, gasFee decimal.Decimal) *BlockchainClient {
	return &BlockchainClient{
		chain:    chain,
		gasFee:   gasFee,
		balances: make(map[string]decimal.Decimal),
	}
}

// SetBalance sets the mock balance for an address and token.
func (c *BlockchainClient) SetBalance(address, token string, balance decimal.Decimal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balances[address+":"+token] = balance
}

func (c *BlockchainClient) Chain() string { return c.chain }

func (c *BlockchainClient) GetBalance(_ context.Context, address string, token string) (decimal.Decimal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bal, ok := c.balances[address+":"+token]
	if !ok {
		return decimal.Zero, nil
	}
	return bal, nil
}

func (c *BlockchainClient) EstimateGas(_ context.Context, _ domain.TxRequest) (decimal.Decimal, error) {
	return c.gasFee, nil
}

func (c *BlockchainClient) SendTransaction(_ context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	c.mu.Lock()
	c.txCount++
	txNum := c.txCount
	c.mu.Unlock()

	return &domain.ChainTx{
		Hash:          fmt.Sprintf("mock-tx-%s-%d", c.chain, txNum),
		Status:        "CONFIRMED",
		Confirmations: 20,
		BlockNumber:   uint64(1000 + txNum),
		Fee:           c.gasFee,
	}, nil
}

func (c *BlockchainClient) GetTransaction(_ context.Context, hash string) (*domain.ChainTx, error) {
	return &domain.ChainTx{
		Hash:          hash,
		Status:        "CONFIRMED",
		Confirmations: 20,
	}, nil
}

func (c *BlockchainClient) SubscribeTransactions(_ context.Context, _ string, ch chan<- domain.ChainTx) error {
	// Mock: no-op subscription. Real implementation would stream events.
	return nil
}
