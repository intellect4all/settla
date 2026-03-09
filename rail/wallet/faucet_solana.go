package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSolanaDevnetRPCURL = "https://api.devnet.solana.com"
	defaultAirdropLamports    = uint64(1_000_000_000) // 1 SOL
)

// solanaDevnetFaucet requests SOL airdrops via the native Solana RPC method.
// Devnet docs: https://docs.solanalabs.com/clusters/rpc-endpoints
type solanaDevnetFaucet struct {
	rpcURL     string
	lamports   uint64
	httpClient *http.Client
	maxRetries int
	retryDelay time.Duration
}

func newSolanaFaucet(cfg FaucetConfig) *solanaDevnetFaucet {
	rpcURL := cfg.SolanaRPCURL
	if rpcURL == "" {
		rpcURL = defaultSolanaDevnetRPCURL
	}

	lamports := uint64(cfg.SolanaAirdropSOL * 1e9)
	if lamports == 0 {
		lamports = defaultAirdropLamports
	}

	return &solanaDevnetFaucet{
		rpcURL:     rpcURL,
		lamports:   lamports,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		maxRetries: cfg.MaxRetries,
		retryDelay: cfg.RetryDelay,
	}
}

func (f *solanaDevnetFaucet) Chain() Chain      { return ChainSolana }
func (f *solanaDevnetFaucet) IsAutomated() bool { return true }
func (f *solanaDevnetFaucet) FaucetURL() string { return "https://faucet.solana.com" }

func (f *solanaDevnetFaucet) Fund(ctx context.Context, address string) error {
	if !ValidateSolanaAddress(address) {
		return fmt.Errorf("settla-wallet: invalid Solana address: %s", address)
	}

	return withRetry(ctx, f.maxRetries, f.retryDelay, func() error {
		return f.requestAirdrop(ctx, address)
	})
}

type solanaRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type solanaRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *solanaDevnetFaucet) requestAirdrop(ctx context.Context, address string) error {
	rpcReq := solanaRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "requestAirdrop",
		Params:  []interface{}{address, f.lamports},
	}

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return fmt.Errorf("settla-wallet: failed to marshal airdrop request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.rpcURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("settla-wallet: failed to build airdrop request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("settla-wallet: Solana airdrop request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp solanaRPCResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&rpcResp); err != nil {
		return fmt.Errorf("settla-wallet: failed to decode airdrop response: %w", err)
	}

	if rpcResp.Error != nil {
		msg := rpcResp.Error.Message
		if isRateLimited(msg) {
			return fmt.Errorf("settla-wallet: Solana Devnet airdrop rate limited: %s", msg)
		}
		return fmt.Errorf("settla-wallet: Solana airdrop failed (code %d): %s",
			rpcResp.Error.Code, msg)
	}

	return nil
}

// isRateLimited checks if an error message indicates rate limiting.
func isRateLimited(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "airdrop limit")
}
