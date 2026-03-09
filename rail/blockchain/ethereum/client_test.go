package ethereum

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ---- Encoding / decoding unit tests (no network) ----

func TestEncodeERC20Transfer(t *testing.T) {
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := big.NewInt(1_000_000) // 1 USDC (6 decimals)

	data := encodeERC20Transfer(to, amount)
	if len(data) != 68 {
		t.Fatalf("expected 68 bytes, got %d", len(data))
	}
	// Verify selector
	if data[0] != 0xa9 || data[1] != 0x05 || data[2] != 0x9c || data[3] != 0xbb {
		t.Fatalf("wrong selector: %x", data[:4])
	}
	// Verify address in bytes [16:36] (12 zero padding + 20 byte address)
	if data[4+12] != 0x12 || data[4+31] != 0x90 {
		t.Fatalf("address not correctly encoded")
	}
	// Verify amount in bytes [36:68] (right-aligned big-endian)
	decoded := decodeUint256(data[36:68])
	if decoded.Cmp(amount) != 0 {
		t.Fatalf("amount mismatch: got %s, want %s", decoded, amount)
	}
}

func TestEncodeERC20BalanceOf(t *testing.T) {
	owner := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	data := encodeERC20BalanceOf(owner)
	if len(data) != 36 {
		t.Fatalf("expected 36 bytes, got %d", len(data))
	}
	if data[0] != 0x70 || data[1] != 0xa0 || data[2] != 0x82 || data[3] != 0x31 {
		t.Fatalf("wrong selector: %x", data[:4])
	}
}

func TestDecodeUint256(t *testing.T) {
	cases := []struct {
		input []byte
		want  int64
	}{
		{make([]byte, 32), 0},
		{func() []byte { b := make([]byte, 32); b[31] = 1; return b }(), 1},
		{func() []byte { b := make([]byte, 32); b[30] = 1; b[31] = 0; return b }(), 256},
	}
	for _, tc := range cases {
		got := decodeUint256(tc.input)
		if got.Int64() != tc.want {
			t.Errorf("decodeUint256: got %d, want %d", got.Int64(), tc.want)
		}
	}
}

func TestToOnChainAmount(t *testing.T) {
	cases := []struct {
		amount   string
		decimals int
		want     string
	}{
		{"1.0", 6, "1000000"},
		{"1.5", 6, "1500000"},
		{"0.000001", 6, "1"},
		{"1.0", 18, "1000000000000000000"},
		{"100.0", 6, "100000000"},
	}
	for _, tc := range cases {
		amount := decimal.RequireFromString(tc.amount)
		got := toOnChainAmount(amount, tc.decimals)
		if got.String() != tc.want {
			t.Errorf("toOnChainAmount(%s, %d): got %s, want %s", tc.amount, tc.decimals, got.String(), tc.want)
		}
	}
}

func TestFromOnChainAmount(t *testing.T) {
	cases := []struct {
		amount   int64
		decimals int
		want     string
	}{
		{1_000_000, 6, "1"},
		{500_000, 6, "0.5"},
		{1_000_000_000_000_000_000, 18, "1"},
	}
	for _, tc := range cases {
		got := fromOnChainAmount(big.NewInt(tc.amount), tc.decimals)
		if got.String() != tc.want {
			t.Errorf("fromOnChainAmount(%d, %d): got %s, want %s", tc.amount, tc.decimals, got.String(), tc.want)
		}
	}
}

func TestTokenDecimals(t *testing.T) {
	if tokenDecimals("USDC") != 6 {
		t.Error("USDC should be 6 decimals")
	}
	if tokenDecimals("USDT") != 6 {
		t.Error("USDT should be 6 decimals")
	}
	if tokenDecimals("ETH") != 18 {
		t.Error("ETH should be 18 decimals")
	}
	if tokenDecimals("UNKNOWN") != 18 {
		t.Error("unknown token should default to 18 decimals")
	}
}

func TestNormalizeAddress(t *testing.T) {
	valid := "0x1234567890123456789012345678901234567890"
	addr, err := normalizeAddress(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.ToLower(addr.Hex()) != valid {
		t.Errorf("address mismatch: got %s", addr.Hex())
	}

	invalidCases := []string{
		"1234567890123456789012345678901234567890",   // no 0x prefix
		"0x123",                                       // too short
		"0xZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ", // invalid hex
	}
	for _, tc := range invalidCases {
		if _, err := normalizeAddress(tc); err == nil {
			t.Errorf("expected error for %q, got nil", tc)
		}
	}
}

func TestNonceManager(t *testing.T) {
	nm := newNonceManager()
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	calls := 0
	fetchFn := func(_ context.Context, _ common.Address) (uint64, error) {
		calls++
		return 42, nil
	}

	nonce0, _ := nm.Next(context.Background(), addr, fetchFn)
	nonce1, _ := nm.Next(context.Background(), addr, fetchFn)
	nonce2, _ := nm.Next(context.Background(), addr, fetchFn)

	if calls != 1 {
		t.Errorf("fetchFn called %d times, expected 1", calls)
	}
	if nonce0 != 42 || nonce1 != 43 || nonce2 != 44 {
		t.Errorf("nonce sequence wrong: %d %d %d", nonce0, nonce1, nonce2)
	}

	nm.Reset(addr)
	nonce3, _ := nm.Next(context.Background(), addr, fetchFn)
	if calls != 2 {
		t.Errorf("fetchFn not called after Reset, calls=%d", calls)
	}
	if nonce3 != 42 {
		t.Errorf("expected 42 after reset, got %d", nonce3)
	}
}

func TestConfig(t *testing.T) {
	cfg := SepoliaConfig
	if cfg.ChainID != 11155111 {
		t.Errorf("Sepolia chain ID wrong: %d", cfg.ChainID)
	}

	addr, err := cfg.ContractAddress("USDC")
	if err != nil {
		t.Fatalf("USDC contract not found: %v", err)
	}
	if !strings.HasPrefix(addr, "0x") {
		t.Errorf("USDC contract address should start with 0x: %s", addr)
	}

	_, err = cfg.ContractAddress("DOGE")
	if err == nil {
		t.Error("expected error for unknown token")
	}

	url := cfg.ExplorerTxURL("0xabc123")
	if !strings.Contains(url, "0xabc123") {
		t.Errorf("explorer URL missing hash: %s", url)
	}
}

func TestBaseSepoliaConfig(t *testing.T) {
	cfg := BaseSepoliaConfig
	if cfg.ChainID != 84532 {
		t.Errorf("Base Sepolia chain ID wrong: %d", cfg.ChainID)
	}
	if cfg.ChainName != "base" {
		t.Errorf("Base chain name wrong: %s", cfg.ChainName)
	}
}

// ---- Client tests with mock JSON-RPC server ----

// jsonRPCRequest is a single JSON-RPC request or batch item.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result"`           // always present, may be "null"
	Error   interface{}     `json:"error,omitempty"`
}

// mockRPCServer starts an httptest server handling the given JSON-RPC methods.
// handlers maps method name → response result value.
func mockRPCServer(t *testing.T, handlers map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var rawReq json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&rawReq); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		// Detect batch vs single
		if len(rawReq) > 0 && rawReq[0] == '[' {
			var reqs []jsonRPCRequest
			json.Unmarshal(rawReq, &reqs)
			resps := make([]jsonRPCResponse, 0, len(reqs))
			for _, req := range reqs {
				resps = append(resps, handleJSONRPC(req, handlers))
			}
			json.NewEncoder(w).Encode(resps)
		} else {
			var req jsonRPCRequest
			json.Unmarshal(rawReq, &req)
			json.NewEncoder(w).Encode(handleJSONRPC(req, handlers))
		}
	}))
}

func handleJSONRPC(req jsonRPCRequest, handlers map[string]interface{}) jsonRPCResponse {
	result, ok := handlers[req.Method]
	if !ok {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage("null"),
			Error:   map[string]interface{}{"code": -32601, "message": "method not found: " + req.Method},
		}
	}
	encoded, _ := json.Marshal(result)
	return jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(encoded)}
}

// newTestClient creates a Client backed by the given httptest server.
func newTestClient(t *testing.T, srv *httptest.Server, config Config) *Client {
	t.Helper()
	inner, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	rpc := &rpcClient{inner: inner}
	return newClientWithRPC(config, rpc, noopSigner{}, nil)
}

// noopSigner is a Signer that always returns an error (used for non-signing tests).
type noopSigner struct{}

func (noopSigner) SignTx(_ context.Context, _ common.Address, _ *types.Transaction, _ *big.Int) (*types.Transaction, error) {
	return nil, nil
}

func TestClientChain(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	if c.Chain() != "ethereum" {
		t.Errorf("Chain() = %q, want %q", c.Chain(), "ethereum")
	}

	c2 := newTestClient(t, srv, BaseSepoliaConfig)
	if c2.Chain() != "base" {
		t.Errorf("Chain() = %q, want %q", c2.Chain(), "base")
	}
}

func TestGetBalanceETH(t *testing.T) {
	// eth_getBalance returns hex-encoded wei
	// 1.5 ETH = 1500000000000000000 wei = 0x14D1120D7B160000
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_getBalance": "0x14D1120D7B160000",
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	bal, err := c.GetBalance(context.Background(), "0x1234567890123456789012345678901234567890", "ETH")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}

	expected := decimal.RequireFromString("1.5")
	if !bal.Equal(expected) {
		t.Errorf("GetBalance ETH: got %s, want %s", bal, expected)
	}
}

func TestGetBalanceERC20(t *testing.T) {
	// eth_call for balanceOf returns a 32-byte hex result
	// 100 USDC = 100_000_000 (6 decimals) = 0x5F5E100
	// Padded to 32 bytes: 0x0000...05F5E100
	result := "0x" + strings.Repeat("0", 56) + "05F5E100"
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_call": result,
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	bal, err := c.GetBalance(context.Background(), "0x1234567890123456789012345678901234567890", "USDC")
	if err != nil {
		t.Fatalf("GetBalance USDC: %v", err)
	}

	expected := decimal.RequireFromString("100")
	if !bal.Equal(expected) {
		t.Errorf("GetBalance USDC: got %s, want %s", bal, expected)
	}
}

func TestGetBalanceInvalidAddress(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	_, err := c.GetBalance(context.Background(), "not-an-address", "ETH")
	if err == nil {
		t.Error("expected error for invalid address")
	}
}

func TestGetBalanceUnknownToken(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	_, err := c.GetBalance(context.Background(), "0x1234567890123456789012345678901234567890", "DOGE")
	if err == nil {
		t.Error("expected error for unknown token without 0x address")
	}
}

// minimalReceipt returns a map with the required fields for a Receipt JSON parse.
func minimalReceipt(status, blockNumber string) map[string]interface{} {
	return map[string]interface{}{
		"status":            status,
		"transactionHash":   "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"transactionIndex":  "0x0",
		"blockHash":         "0x1111111111111111111111111111111111111111111111111111111111111111",
		"blockNumber":       blockNumber,
		"from":              "0x1111111111111111111111111111111111111111",
		"to":                "0x2222222222222222222222222222222222222222",
		"gasUsed":           "0x5208",
		"cumulativeGasUsed": "0x5208",
		"logs":              []interface{}{},
		"logsBloom":         "0x" + strings.Repeat("0", 512),
		"effectiveGasPrice": "0x3b9aca00", // 1 Gwei
	}
}


func TestGetTransactionNotFound(t *testing.T) {
	// Both receipt and tx not found → UNKNOWN (not an error)
	hash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_getTransactionReceipt": nil,
		"eth_getTransactionByHash":  nil,
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	tx, err := c.GetTransaction(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if tx.Status != TxStatusUnknown {
		t.Errorf("expected UNKNOWN, got %s", tx.Status)
	}
}

func TestGetTransactionConfirmed(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_getTransactionReceipt": minimalReceipt("0x1", "0x64"), // block 100
		"eth_blockNumber":           "0x6e",                        // block 110 → 10 confirmations
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	hash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	tx, err := c.GetTransaction(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}

	if tx.Status != TxStatusConfirmed {
		t.Errorf("expected CONFIRMED, got %s", tx.Status)
	}
	if tx.BlockNumber != 100 {
		t.Errorf("expected block 100, got %d", tx.BlockNumber)
	}
	if tx.Confirmations != 10 {
		t.Errorf("expected 10 confirmations, got %d", tx.Confirmations)
	}
	if tx.Fee.IsZero() {
		t.Error("expected non-zero fee")
	}
}

func TestGetTransactionFailed(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_getTransactionReceipt": minimalReceipt("0x0", "0x64"), // status 0 = reverted
		"eth_blockNumber":           "0x65",
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	hash := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	tx, err := c.GetTransaction(context.Background(), hash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if tx.Status != TxStatusFailed {
		t.Errorf("expected FAILED, got %s", tx.Status)
	}
}

func TestSubscribeTransactionsCancel(t *testing.T) {
	srv := mockRPCServer(t, map[string]interface{}{
		"eth_blockNumber": "0x64",
		"eth_getLogs":     []interface{}{},
	})
	defer srv.Close()

	c := newTestClient(t, srv, SepoliaConfig)
	ch := make(chan domain.ChainTx, 10)
	ctx, cancel := context.WithCancel(context.Background())

	err := c.SubscribeTransactions(ctx, "0x1234567890123456789012345678901234567890", ch)
	if err != nil {
		t.Fatalf("SubscribeTransactions: %v", err)
	}

	// Cancel immediately — goroutine should exit cleanly
	cancel()
}

func TestWalletSignerRegister(t *testing.T) {
	signer := NewWalletSigner(nil, big.NewInt(11155111))
	signer.RegisterWallet("0x1234567890123456789012345678901234567890", "system/hot/ethereum")

	signer.mu.RLock()
	path, ok := signer.addressToPath["0x1234567890123456789012345678901234567890"]
	signer.mu.RUnlock()

	if !ok || path != "system/hot/ethereum" {
		t.Errorf("wallet not registered: ok=%v path=%q", ok, path)
	}
}

func TestWalletSignerUnregistered(t *testing.T) {
	signer := NewWalletSigner(nil, big.NewInt(11155111))
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	_, err := signer.SignTx(context.Background(), addr, nil, big.NewInt(11155111))
	if err == nil {
		t.Error("expected error for unregistered address")
	}
}

func TestBuildTransactionERC20(t *testing.T) {
	c := &Client{
		config:  SepoliaConfig,
		chainID: big.NewInt(11155111),
		nonces:  newNonceManager(),
		logger:  nil,
	}

	to := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	gasPrice := big.NewInt(1_000_000_000) // 1 Gwei
	amount := decimal.RequireFromString("50")

	tx, err := c.buildTransaction(0, common.Address{}, to, "USDC", amount, gasPrice)
	if err != nil {
		t.Fatalf("buildTransaction: %v", err)
	}

	// To should be the USDC contract address, not the recipient
	if tx.To().Hex() == to.Hex() {
		t.Error("ERC20 tx.To() should be contract, not recipient")
	}
	if tx.Value().Sign() != 0 {
		t.Error("ERC20 tx value should be 0")
	}
	if len(tx.Data()) != 68 {
		t.Errorf("ERC20 data should be 68 bytes, got %d", len(tx.Data()))
	}
}

func TestBuildTransactionETH(t *testing.T) {
	c := &Client{
		config:  SepoliaConfig,
		chainID: big.NewInt(11155111),
		nonces:  newNonceManager(),
	}

	to := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")
	gasPrice := big.NewInt(1_000_000_000)
	amount := decimal.RequireFromString("0.1")

	tx, err := c.buildTransaction(5, common.Address{}, to, "", amount, gasPrice)
	if err != nil {
		t.Fatalf("buildTransaction ETH: %v", err)
	}

	if tx.To().Hex() != to.Hex() {
		t.Errorf("ETH tx.To() should be recipient, got %s", tx.To().Hex())
	}
	if tx.Nonce() != 5 {
		t.Errorf("nonce should be 5, got %d", tx.Nonce())
	}
	if len(tx.Data()) != 0 {
		t.Error("native ETH tx should have no data")
	}
}

