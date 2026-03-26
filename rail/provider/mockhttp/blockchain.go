package mockhttp

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// BlockchainClient delegates blockchain operations to the external mock provider HTTP service.
type BlockchainClient struct {
	chain  domain.CryptoChain
	client *Client
}

// NewBlockchainClient creates a BlockchainClient that delegates to the mock HTTP service.
func NewBlockchainClient(chain domain.CryptoChain, client *Client) *BlockchainClient {
	return &BlockchainClient{chain: chain, client: client}
}

func (c *BlockchainClient) Chain() domain.CryptoChain { return c.chain }

func (c *BlockchainClient) GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error) {
	var resp struct {
		Balance string `json:"balance"`
	}
	path := fmt.Sprintf("/api/blockchain/balance?chain=%s&address=%s&token=%s", c.chain, address, token)
	if err := c.client.doGet(ctx, path, &resp); err != nil {
		return decimal.Zero, err
	}
	bal, err := decimal.NewFromString(resp.Balance)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-mockhttp: parse balance: %w", err)
	}
	return bal, nil
}

func (c *BlockchainClient) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	// Fixed gas estimate — matches the in-process mock.
	return decimal.NewFromFloat(0.10), nil
}

func (c *BlockchainClient) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	body := map[string]string{
		"chain":  string(c.chain),
		"from":   req.From,
		"to":     req.To,
		"token":  req.Token,
		"amount": req.Amount.String(),
	}

	var resp struct {
		Hash          string `json:"hash"`
		Status        string `json:"status"`
		Confirmations int    `json:"confirmations"`
		BlockNumber   uint64 `json:"block_number"`
		Fee           string `json:"fee"`
	}
	if err := c.client.doPost(ctx, "/api/blockchain/send", body, &resp); err != nil {
		return nil, err
	}

	fee, _ := decimal.NewFromString(resp.Fee)
	return &domain.ChainTx{
		Hash:          resp.Hash,
		Status:        resp.Status,
		Confirmations: resp.Confirmations,
		BlockNumber:   resp.BlockNumber,
		Fee:           fee,
	}, nil
}

func (c *BlockchainClient) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	// Mock: return a completed transaction.
	return &domain.ChainTx{
		Hash:          hash,
		Status:        "confirmed",
		Confirmations: 20,
		BlockNumber:   1000000,
		Fee:           decimal.NewFromFloat(0.10),
	}, nil
}

func (c *BlockchainClient) SubscribeTransactions(_ context.Context, _ string, _ chan<- domain.ChainTx) error {
	// No-op for mock — chain monitor is not used in demo mode.
	return nil
}
