package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

const (
	// erc20TransferTopic is the keccak256 hash of Transfer(address,address,uint256).
	erc20TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	// erc20Decimals is the default decimal precision for ERC-20 stablecoins (USDT, USDC).
	erc20Decimals = 6
	// maxBlockRange limits the block range per eth_getLogs call to avoid RPC timeouts.
	maxBlockRange = 2000
)

// EVMClient wraps Ethereum JSON-RPC API calls with dual-provider failover.
// It works for any EVM-compatible chain (Ethereum, Base, Polygon, etc.)
// since they all share the same JSON-RPC specification.
type EVMClient struct {
	failover   *FailoverManager
	httpClient *http.Client
	logger     *slog.Logger

	// Block number cache — avoids redundant eth_blockNumber RPC calls.
	blockCacheMu  sync.RWMutex
	cachedBlockNum int64
	cachedBlockAt  time.Time
	blockCacheTTL  time.Duration
}

// blockCacheDefaultTTL is the default cache duration for eth_blockNumber results.
// Most chains have block times of 2-12 seconds, so 3 seconds is safe.
const blockCacheDefaultTTL = 3 * time.Second

// NewEVMClient creates an EVM RPC client with failover and connection pooling.
func NewEVMClient(providers []*Provider, logger *slog.Logger) *EVMClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &EVMClient{
		failover:      NewFailoverManager(providers, logger),
		httpClient:    &http.Client{Timeout: httpTimeout, Transport: NewPooledTransport()},
		logger:        logger,
		blockCacheTTL: blockCacheDefaultTTL,
	}
}

// GetLatestBlockNumber returns the latest block number via eth_blockNumber.
// Results are cached for blockCacheTTL to reduce redundant RPC calls.
func (c *EVMClient) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	// Check cache first.
	c.blockCacheMu.RLock()
	if c.cachedBlockNum > 0 && time.Since(c.cachedBlockAt) < c.blockCacheTTL {
		n := c.cachedBlockNum
		c.blockCacheMu.RUnlock()
		return n, nil
	}
	c.blockCacheMu.RUnlock()

	var blockNum int64
	// Use FanExecute for block number — latency-sensitive, benefits from fastest provider.
	err := c.failover.FanExecute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		var result string
		if err := c.jsonRPC(ctx, rpcURL, apiKey, "eth_blockNumber", []any{}, &result); err != nil {
			return err
		}
		n, err := ParseHexInt64(result)
		if err != nil {
			return fmt.Errorf("parsing block number %q: %w", result, err)
		}
		blockNum = n
		return nil
	})
	if err == nil {
		c.blockCacheMu.Lock()
		c.cachedBlockNum = blockNum
		c.cachedBlockAt = time.Now()
		c.blockCacheMu.Unlock()
	}
	return blockNum, err
}

// EVMBlock represents a block from eth_getBlockByNumber with fields needed
// for checkpoint management and reorg detection.
type EVMBlock struct {
	Number     int64
	Hash       string
	ParentHash string
	Timestamp  int64
}

// GetBlockByNumber returns the block at the given height via eth_getBlockByNumber.
// The block includes the hash and parent hash for reorg detection.
func (c *EVMClient) GetBlockByNumber(ctx context.Context, num int64) (*EVMBlock, error) {
	var block *EVMBlock
	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		var result ethBlockResult
		// false = do not include full transactions (we only need headers)
		if err := c.jsonRPC(ctx, rpcURL, apiKey, "eth_getBlockByNumber", []any{EncodeHexInt64(num), false}, &result); err != nil {
			return err
		}
		if result.Hash == "" {
			return fmt.Errorf("block %d not found", num)
		}
		blockNum, err := ParseHexInt64(result.Number)
		if err != nil {
			return fmt.Errorf("parsing block number: %w", err)
		}
		ts, err := ParseHexInt64(result.Timestamp)
		if err != nil {
			return fmt.Errorf("parsing block timestamp: %w", err)
		}
		block = &EVMBlock{
			Number:     blockNum,
			Hash:       result.Hash,
			ParentHash: result.ParentHash,
			Timestamp:  ts,
		}
		return nil
	})
	return block, err
}

// ERC20Transfer represents a single ERC-20 Transfer event decoded from a log.
type ERC20Transfer struct {
	TxHash        string
	BlockNumber   int64
	BlockHash     string
	LogIndex      int64
	From          string
	To            string
	Amount        decimal.Decimal
	TokenContract string
	Timestamp     time.Time // populated by the caller from block data
}

// GetERC20Transfers queries eth_getLogs for ERC-20 Transfer events to the given
// addresses within a block range. This batches all addresses into a single RPC
// call using the topics filter, which is much more efficient than per-address queries.
func (c *EVMClient) GetERC20Transfers(
	ctx context.Context,
	contractAddress string,
	toAddresses []string,
	fromBlock, toBlock int64,
	tokenDecimals int32,
) ([]ERC20Transfer, error) {
	if len(toAddresses) == 0 {
		return nil, nil
	}

	var allTransfers []ERC20Transfer
	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		allTransfers = nil // reset on retry

		// Process in chunks to respect maxBlockRange
		for start := fromBlock; start <= toBlock; start += maxBlockRange {
			end := start + maxBlockRange - 1
			if end > toBlock {
				end = toBlock
			}

			transfers, err := c.getLogsChunk(ctx, rpcURL, apiKey, contractAddress, toAddresses, start, end, tokenDecimals)
			if err != nil {
				return err
			}
			allTransfers = append(allTransfers, transfers...)
		}
		return nil
	})
	return allTransfers, err
}

// getLogsChunk executes a single eth_getLogs call for a block range.
func (c *EVMClient) getLogsChunk(
	ctx context.Context,
	rpcURL, apiKey string,
	contractAddress string,
	toAddresses []string,
	fromBlock, toBlock int64,
	tokenDecimals int32,
) ([]ERC20Transfer, error) {
	// Build padded topic[2] values (to addresses, left-padded to 32 bytes)
	paddedAddrs := make([]string, len(toAddresses))
	for i, addr := range toAddresses {
		paddedAddrs[i] = PadAddress(addr)
	}

	// eth_getLogs filter:
	// topic[0] = Transfer(address,address,uint256) event signature
	// topic[1] = null (any sender)
	// topic[2] = [addr1, addr2, ...] (OR filter on recipient addresses)
	filter := map[string]any{
		"address":   contractAddress,
		"fromBlock": EncodeHexInt64(fromBlock),
		"toBlock":   EncodeHexInt64(toBlock),
		"topics": []any{
			erc20TransferTopic,
			nil, // any from address
			paddedAddrs,
		},
	}

	var logs []ethLog
	if err := c.jsonRPC(ctx, rpcURL, apiKey, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, fmt.Errorf("eth_getLogs [%d, %d]: %w", fromBlock, toBlock, err)
	}

	transfers := make([]ERC20Transfer, 0, len(logs))
	for _, log := range logs {
		t, err := ParseERC20TransferLog(log, tokenDecimals)
		if err != nil {
			c.logger.Debug("settla-evm-monitor: skipping malformed log",
				"tx_hash", log.TransactionHash,
				"error", err,
			)
			continue
		}
		transfers = append(transfers, t)
	}
	return transfers, nil
}

// ParseERC20TransferLog decodes an ERC-20 Transfer event from an eth_getLogs result.
func ParseERC20TransferLog(log ethLog, tokenDecimals int32) (ERC20Transfer, error) {
	if len(log.Topics) < 3 {
		return ERC20Transfer{}, fmt.Errorf("expected at least 3 topics, got %d", len(log.Topics))
	}
	if log.Topics[0] != erc20TransferTopic {
		return ERC20Transfer{}, fmt.Errorf("unexpected topic[0]: %s", log.Topics[0])
	}

	blockNum, err := ParseHexInt64(log.BlockNumber)
	if err != nil {
		return ERC20Transfer{}, fmt.Errorf("parsing block number: %w", err)
	}
	logIdx, err := ParseHexInt64(log.LogIndex)
	if err != nil {
		return ERC20Transfer{}, fmt.Errorf("parsing log index: %w", err)
	}

	from := ExtractAddress(log.Topics[1])
	to := ExtractAddress(log.Topics[2])

	amount := decimal.Zero
	if log.Data != "" && log.Data != "0x" {
		amount = ParseHexAmount(log.Data, tokenDecimals)
	}

	return ERC20Transfer{
		TxHash:        log.TransactionHash,
		BlockNumber:   blockNum,
		BlockHash:     log.BlockHash,
		LogIndex:      logIdx,
		From:          from,
		To:            to,
		Amount:        amount,
		TokenContract: strings.ToLower(log.Address),
	}, nil
}

// ParseEVMTransfer converts an ERC20Transfer into a domain IncomingTransaction.
func ParseEVMTransfer(t ERC20Transfer, chain string) domain.IncomingTransaction {
	return domain.IncomingTransaction{
		Chain:         domain.CryptoChain(chain),
		TxHash:        t.TxHash,
		FromAddress:   t.From,
		ToAddress:     t.To,
		TokenContract: t.TokenContract,
		Amount:        t.Amount,
		BlockNumber:   t.BlockNumber,
		BlockHash:     t.BlockHash,
		Timestamp:     t.Timestamp,
	}
}


// ParseHexInt64 parses a 0x-prefixed hex string into an int64.
func ParseHexInt64(s string) (int64, error) {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return 0, nil
	}
	val, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return 0, fmt.Errorf("invalid hex number: %s", s)
	}
	return val.Int64(), nil
}

// EncodeHexInt64 encodes an int64 as a 0x-prefixed hex string.
func EncodeHexInt64(n int64) string {
	return fmt.Sprintf("0x%x", n)
}

// PadAddress left-pads a 20-byte Ethereum address to a 32-byte hex topic value.
// Input: "0xAbC123..." or "AbC123..." → Output: "0x000000000000000000000000abc123..."
func PadAddress(addr string) string {
	addr = strings.TrimPrefix(addr, "0x")
	addr = strings.TrimPrefix(addr, "0X")
	addr = strings.ToLower(addr)
	// Pad to 64 hex chars (32 bytes)
	padded := strings.Repeat("0", 64-len(addr)) + addr
	return "0x" + padded
}

// ExtractAddress extracts a 20-byte address from a 32-byte hex topic.
// Input: "0x000000000000000000000000abc123..." → Output: "0xabc123..."
func ExtractAddress(topic string) string {
	topic = strings.TrimPrefix(topic, "0x")
	topic = strings.TrimPrefix(topic, "0X")
	topic = strings.ToLower(topic)
	// Last 40 chars = 20-byte address
	if len(topic) > 40 {
		topic = topic[len(topic)-40:]
	}
	return "0x" + topic
}

// ParseHexAmount parses a 0x-prefixed hex data field into a decimal amount
// using the given token decimals.
func ParseHexAmount(data string, decimals int32) decimal.Decimal {
	data = strings.TrimPrefix(data, "0x")
	data = strings.TrimPrefix(data, "0X")
	// Remove leading zeros for big.Int parsing
	data = strings.TrimLeft(data, "0")
	if data == "" {
		return decimal.Zero
	}
	val, ok := new(big.Int).SetString(data, 16)
	if !ok {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(val, -decimals)
}


type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error,omitempty"`
	ID      int             `json:"id"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

func (c *EVMClient) jsonRPC(ctx context.Context, rpcURL, apiKey string, method string, params any, result any) error {
	start := time.Now()

	reqBody := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling JSON-RPC request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to %s: %w", rpcURL, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("settla-evm-monitor: RPC call",
		"method", method,
		"url", rpcURL,
		"status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, rpcURL, string(body))
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return fmt.Errorf("decoding JSON-RPC response from %s: %w", rpcURL, err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}

	if result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("decoding JSON-RPC result from %s: %w", rpcURL, err)
		}
	}
	return nil
}


// jsonRPCBatch sends multiple JSON-RPC requests in a single HTTP call and
// returns the responses indexed by their request ID. This reduces the number
// of round-trips for poll cycles that need multiple RPC methods.
func (c *EVMClient) jsonRPCBatch(ctx context.Context, rpcURL, apiKey string, requests []jsonRPCRequest) ([]jsonRPCResponse, error) {
	start := time.Now()

	b, err := json.Marshal(requests)
	if err != nil {
		return nil, fmt.Errorf("marshaling batch request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("creating batch HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch HTTP request to %s: %w", rpcURL, err)
	}
	defer resp.Body.Close()

	c.logger.Debug("settla-evm-monitor: RPC batch call",
		"count", len(requests),
		"url", rpcURL,
		"status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("reading batch response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, rpcURL, string(body))
	}

	var responses []jsonRPCResponse
	if err := json.Unmarshal(body, &responses); err != nil {
		return nil, fmt.Errorf("decoding batch response from %s: %w", rpcURL, err)
	}
	return responses, nil
}

// GetBlockNumberAndLogs combines eth_blockNumber and eth_getLogs into a single
// batch RPC call, reducing the per-poll round-trip count from 2 to 1.
func (c *EVMClient) GetBlockNumberAndLogs(
	ctx context.Context,
	contractAddress string,
	toAddresses []string,
	fromBlock, toBlock int64,
	tokenDecimals int32,
) (int64, []ERC20Transfer, error) {
	paddedAddrs := make([]string, len(toAddresses))
	for i, addr := range toAddresses {
		paddedAddrs[i] = PadAddress(addr)
	}

	filter := map[string]any{
		"address":   contractAddress,
		"fromBlock": EncodeHexInt64(fromBlock),
		"toBlock":   EncodeHexInt64(toBlock),
		"topics": []any{
			erc20TransferTopic,
			nil,
			paddedAddrs,
		},
	}

	var blockNum int64
	var transfers []ERC20Transfer

	err := c.failover.Execute(ctx, func(ctx context.Context, rpcURL, apiKey string) error {
		requests := []jsonRPCRequest{
			{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []any{}, ID: 1},
			{JSONRPC: "2.0", Method: "eth_getLogs", Params: []any{filter}, ID: 2},
		}

		responses, err := c.jsonRPCBatch(ctx, rpcURL, apiKey, requests)
		if err != nil {
			return err
		}

		// Parse responses by ID.
		for _, r := range responses {
			if r.Error != nil {
				return r.Error
			}
			switch r.ID {
			case 1: // eth_blockNumber
				var hexNum string
				if err := json.Unmarshal(r.Result, &hexNum); err != nil {
					return fmt.Errorf("decoding block number: %w", err)
				}
				blockNum, err = ParseHexInt64(hexNum)
				if err != nil {
					return fmt.Errorf("parsing block number: %w", err)
				}
			case 2: // eth_getLogs
				var logs []ethLog
				if err := json.Unmarshal(r.Result, &logs); err != nil {
					return fmt.Errorf("decoding logs: %w", err)
				}
				transfers = make([]ERC20Transfer, 0, len(logs))
				for _, log := range logs {
					t, err := ParseERC20TransferLog(log, tokenDecimals)
					if err != nil {
						continue
					}
					transfers = append(transfers, t)
				}
			}
		}
		return nil
	})

	if err == nil && blockNum > 0 {
		c.blockCacheMu.Lock()
		c.cachedBlockNum = blockNum
		c.cachedBlockAt = time.Now()
		c.blockCacheMu.Unlock()
	}

	return blockNum, transfers, err
}


// ethBlockResult is the raw result from eth_getBlockByNumber.
type ethBlockResult struct {
	Number     string `json:"number"`
	Hash       string `json:"hash"`
	ParentHash string `json:"parentHash"`
	Timestamp  string `json:"timestamp"`
}

// ethLog is a single log entry from eth_getLogs.
type ethLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	BlockHash        string   `json:"blockHash"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}


// hexToBytes decodes a 0x-prefixed hex string to bytes.
func hexToBytes(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	return hex.DecodeString(s)
}
