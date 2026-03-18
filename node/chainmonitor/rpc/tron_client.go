package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

const (
	httpTimeout  = 30 * time.Second
	maxBody      = 10 << 20 // 10 MB
	trc20Decimals = 6
)

// TronClient wraps TronGrid HTTP API calls with dual-provider failover.
type TronClient struct {
	failover   *FailoverManager
	httpClient *http.Client
	logger     *slog.Logger
}

// NewTronClient creates a Tron RPC client with failover.
func NewTronClient(providers []*Provider, logger *slog.Logger) *TronClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &TronClient{
		failover:   NewFailoverManager(providers, logger),
		httpClient: &http.Client{Timeout: httpTimeout},
		logger:     logger,
	}
}

// GetNowBlock returns the latest block number.
func (c *TronClient) GetNowBlock(ctx context.Context) (int64, error) {
	var blockNum int64
	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		var resp NowBlockResp
		if err := c.post(ctx, rpcURL, apiKey, "/wallet/getnowblock", struct{}{}, &resp); err != nil {
			return err
		}
		blockNum = resp.BlockHeader.RawData.Number
		return nil
	})
	return blockNum, err
}

// GetBlockByNum returns the block at the given height, including all transactions.
func (c *TronClient) GetBlockByNum(ctx context.Context, num int64) (*BlockResp, error) {
	var block *BlockResp
	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		var resp BlockResp
		req := map[string]any{"num": num, "detail": true}
		if err := c.post(ctx, rpcURL, apiKey, "/wallet/getblockbynum", req, &resp); err != nil {
			return err
		}
		block = &resp
		return nil
	})
	return block, err
}

// GetTRC20Transfers fetches recent confirmed TRC20 transfers for an address.
func (c *TronClient) GetTRC20Transfers(ctx context.Context, address, contractAddress string, minTimestamp int64, limit int) ([]TRC20Transfer, error) {
	var transfers []TRC20Transfer
	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		path := fmt.Sprintf("/v1/accounts/%s/transactions/trc20?limit=%d&only_confirmed=true&order_by=block_timestamp,asc", address, limit)
		if contractAddress != "" {
			path += "&contract_address=" + contractAddress
		}
		if minTimestamp > 0 {
			path += fmt.Sprintf("&min_timestamp=%d", minTimestamp)
		}
		var resp TRC20TransfersResp
		if err := c.get(ctx, rpcURL, apiKey, path, &resp); err != nil {
			return err
		}
		transfers = resp.Data
		return nil
	})
	return transfers, err
}

// ParseTRC20Transfer converts a raw TRC20 transfer into a domain IncomingTransaction.
func ParseTRC20Transfer(t TRC20Transfer, chain string) domain.IncomingTransaction {
	amount := decimal.Zero
	if val, ok := new(big.Int).SetString(t.Value, 10); ok {
		amount = decimal.NewFromBigInt(val, -int32(trc20Decimals))
	}

	return domain.IncomingTransaction{
		Chain:         chain,
		TxHash:        t.TransactionID,
		FromAddress:   t.From,
		ToAddress:     t.To,
		TokenContract: strings.ToLower(t.TokenInfo.Address),
		Amount:        amount,
		BlockNumber:   t.BlockTimestamp / 3000, // approximate block from timestamp
		BlockHash:     "",                      // filled by the poller from block data
		Timestamp:     time.UnixMilli(t.BlockTimestamp),
	}
}

// ── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *TronClient) get(ctx context.Context, baseURL, apiKey, path string, resp any) error {
	return c.do(ctx, http.MethodGet, baseURL+path, apiKey, nil, resp)
}

func (c *TronClient) post(ctx context.Context, baseURL, apiKey, path string, body, resp any) error {
	return c.do(ctx, http.MethodPost, baseURL+path, apiKey, body, resp)
}

func (c *TronClient) do(ctx context.Context, method, url, apiKey string, reqBody, respBody any) error {
	start := time.Now()

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("settla-tron-monitor: RPC call",
		"method", method,
		"url", url,
		"status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, string(body))
	}

	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("decoding response from %s: %w", url, err)
		}
	}
	return nil
}

// ── Response types ──────────────────────────────────────────────────────────

// NowBlockResp is the response from /wallet/getnowblock.
type NowBlockResp struct {
	BlockHeader struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	BlockID string `json:"blockID"`
}

// BlockResp is the response from /wallet/getblockbynum.
type BlockResp struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number         int64  `json:"number"`
			Timestamp      int64  `json:"timestamp"`
			ParentHash     string `json:"parentHash"`
			TxTrieRoot     string `json:"txTrieRoot"`
			WitnessAddress string `json:"witness_address"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []json.RawMessage `json:"transactions"`
}

// TRC20Transfer is a single TRC20 transfer from the TronGrid API.
type TRC20Transfer struct {
	TransactionID  string `json:"transaction_id"`
	BlockTimestamp int64  `json:"block_timestamp"`
	From           string `json:"from"`
	To             string `json:"to"`
	Value          string `json:"value"`
	Type           string `json:"type"`
	TokenInfo      struct {
		Symbol   string `json:"symbol"`
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
		Name     string `json:"name"`
	} `json:"token_info"`
}

// TRC20TransfersResp is the response from /v1/accounts/{address}/transactions/trc20.
type TRC20TransfersResp struct {
	Data    []TRC20Transfer `json:"data"`
	Success bool            `json:"success"`
	Meta    struct {
		At       int64  `json:"at"`
		Fingerprint string `json:"fingerprint"`
		PageSize int    `json:"page_size"`
	} `json:"meta"`
}
