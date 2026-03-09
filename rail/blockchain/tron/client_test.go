package tron_test

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/blockchain/tron"
)

// newTestServer creates an httptest.Server whose handler is configurable per-test.
func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// newTestClient creates a Tron client pointing at a test server.
func newTestClient(serverURL string) *tron.Client {
	cfg := tron.TronConfig{
		RPCURL:        serverURL,
		APIKey:        "",
		ExplorerURL:   "https://nile.tronscan.org",
		USDTContract:  "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj",
		BlockTime:     100 * time.Millisecond,
		Confirmations: 19,
	}
	return tron.NewClient(cfg, nil, nil)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ----------------------------------------------------------------------------
// Chain identifier
// ----------------------------------------------------------------------------

func TestChain(t *testing.T) {
	c := newTestClient("http://localhost")
	if got := c.Chain(); got != "tron" {
		t.Errorf("Chain() = %q, want %q", got, "tron")
	}
}

// ----------------------------------------------------------------------------
// GetBalance — TRX
// ----------------------------------------------------------------------------

func TestGetBalance_TRX(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []any{
				map[string]any{
					"address": "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
					"balance": 5_000_000, // 5 TRX in SUN
					"trc20":   []any{},
				},
			},
			"success": true,
		})
	}))

	c := newTestClient(srv.URL)
	balance, err := c.GetBalance(context.Background(), "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", "")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	expected := decimal.NewFromInt(5)
	if !balance.Equal(expected) {
		t.Errorf("GetBalance TRX = %s, want %s", balance, expected)
	}
}

// ----------------------------------------------------------------------------
// GetBalance — TRC20 token
// ----------------------------------------------------------------------------

func TestGetBalance_TRC20(t *testing.T) {
	const contract = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"data": []any{
				map[string]any{
					"address": "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
					"balance": 0,
					"trc20": []any{
						map[string]string{
							contract: "100000000", // 100 USDT (6 decimals)
						},
					},
				},
			},
			"success": true,
		})
	}))

	c := newTestClient(srv.URL)
	balance, err := c.GetBalance(context.Background(), "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", contract)
	if err != nil {
		t.Fatalf("GetBalance TRC20: %v", err)
	}
	expected := decimal.NewFromInt(100)
	if !balance.Equal(expected) {
		t.Errorf("GetBalance TRC20 = %s, want %s", balance, expected)
	}
}

// ----------------------------------------------------------------------------
// GetBalance — unactivated account returns zero, no error
// ----------------------------------------------------------------------------

func TestGetBalance_UnactivatedAccount(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "success": true})
	}))

	c := newTestClient(srv.URL)
	balance, err := c.GetBalance(context.Background(), "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !balance.IsZero() {
		t.Errorf("expected zero balance for unactivated account, got %s", balance)
	}
}

// ----------------------------------------------------------------------------
// EstimateGas
// ----------------------------------------------------------------------------

func TestEstimateGas_TRC20(t *testing.T) {
	c := newTestClient("http://localhost")
	req := domain.TxRequest{
		From:   "system/tron/hot",
		To:     "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
		Token:  "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj",
		Amount: decimal.NewFromInt(100),
	}
	gas, err := c.EstimateGas(context.Background(), req)
	if err != nil {
		t.Fatalf("EstimateGas: %v", err)
	}
	if gas.IsZero() || gas.IsNegative() {
		t.Errorf("EstimateGas returned non-positive value: %s", gas)
	}
}

func TestEstimateGas_NativeTRX(t *testing.T) {
	c := newTestClient("http://localhost")
	req := domain.TxRequest{
		From:   "system/tron/hot",
		To:     "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
		Amount: decimal.NewFromInt(5),
	}
	gas, err := c.EstimateGas(context.Background(), req)
	if err != nil {
		t.Fatalf("EstimateGas TRX: %v", err)
	}
	if gas.IsZero() || gas.IsNegative() {
		t.Errorf("EstimateGas TRX returned non-positive value: %s", gas)
	}
}

// ----------------------------------------------------------------------------
// GetTransaction — confirmed
// ----------------------------------------------------------------------------

func TestGetTransaction_Confirmed(t *testing.T) {
	const txHash = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"
	const blockNum = int64(50_000_000)

	callCount := 0
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			writeJSON(w, map[string]any{
				"id":             txHash,
				"fee":            1100000,
				"blockNumber":    blockNum,
				"blockTimeStamp": 1700000000000,
				"receipt": map[string]any{
					"result":       "SUCCESS",
					"energy_usage": 30000,
					"energy_fee":   8400000,
					"net_usage":    245,
					"net_fee":      0,
				},
			})
		case "/wallet/getnowblock":
			writeJSON(w, map[string]any{
				"block_header": map[string]any{
					"raw_data": map[string]any{
						"number": blockNum + 25, // 25 confirmations
					},
				},
			})
		}
	}))

	c := newTestClient(srv.URL)
	tx, err := c.GetTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}

	if tx.Hash != txHash {
		t.Errorf("Hash = %q, want %q", tx.Hash, txHash)
	}
	if tx.Status != "confirmed" {
		t.Errorf("Status = %q, want %q", tx.Status, "confirmed")
	}
	if tx.Confirmations < 19 {
		t.Errorf("Confirmations = %d, want >= 19", tx.Confirmations)
	}
	if tx.BlockNumber != uint64(blockNum) {
		t.Errorf("BlockNumber = %d, want %d", tx.BlockNumber, blockNum)
	}
	if tx.Fee.IsZero() {
		t.Error("Fee should be non-zero")
	}
}

// ----------------------------------------------------------------------------
// GetTransaction — still confirming
// ----------------------------------------------------------------------------

func TestGetTransaction_Confirming(t *testing.T) {
	const txHash = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"
	const blockNum = int64(50_000_000)

	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			writeJSON(w, map[string]any{
				"id":          txHash,
				"blockNumber": blockNum,
				"receipt":     map[string]any{"result": "SUCCESS"},
			})
		case "/wallet/getnowblock":
			writeJSON(w, map[string]any{
				"block_header": map[string]any{
					"raw_data": map[string]any{"number": blockNum + 5}, // only 5 confirmations
				},
			})
		}
	}))

	c := newTestClient(srv.URL)
	tx, err := c.GetTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if tx.Status != "confirming" {
		t.Errorf("Status = %q, want %q", tx.Status, "confirming")
	}
}

// ----------------------------------------------------------------------------
// GetTransaction — transaction not found
// ----------------------------------------------------------------------------

func TestGetTransaction_NotFound(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty ID means not found
		writeJSON(w, map[string]any{"id": "", "blockNumber": 0})
	}))

	c := newTestClient(srv.URL)
	_, err := c.GetTransaction(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent transaction, got nil")
	}
}

// ----------------------------------------------------------------------------
// GetTransaction — failed on-chain
// ----------------------------------------------------------------------------

func TestGetTransaction_Failed(t *testing.T) {
	const txHash = "failedtx00000000000000000000000000000000000000000000000000000001"
	const blockNum = int64(50_000_000)

	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			writeJSON(w, map[string]any{
				"id":          txHash,
				"blockNumber": blockNum,
				"receipt":     map[string]any{"result": "REVERT"},
			})
		case "/wallet/getnowblock":
			writeJSON(w, map[string]any{
				"block_header": map[string]any{
					"raw_data": map[string]any{"number": blockNum + 30},
				},
			})
		}
	}))

	c := newTestClient(srv.URL)
	tx, err := c.GetTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if tx.Status != "failed" {
		t.Errorf("Status = %q, want %q", tx.Status, "failed")
	}
}

// ----------------------------------------------------------------------------
// SubscribeTransactions — starts without error
// ----------------------------------------------------------------------------

func TestSubscribeTransactions_StartsOK(t *testing.T) {
	srv := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{}, "success": true})
	}))

	c := newTestClient(srv.URL)
	ch := make(chan domain.ChainTx, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.SubscribeTransactions(ctx, "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", ch); err != nil {
		t.Fatalf("SubscribeTransactions: %v", err)
	}
}

// ----------------------------------------------------------------------------
// SendTransaction — requires wallet manager
// ----------------------------------------------------------------------------

func TestSendTransaction_NoWalletManager(t *testing.T) {
	c := newTestClient("http://localhost")
	_, err := c.SendTransaction(context.Background(), domain.TxRequest{
		From:   "system/tron/hot",
		To:     "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
		Token:  "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj",
		Amount: decimal.NewFromInt(10),
	})
	if err == nil {
		t.Error("expected error when wallet manager is nil, got nil")
	}
}

// ----------------------------------------------------------------------------
// Transaction helpers (pure unit tests — no network)
// ----------------------------------------------------------------------------

func TestAddressToHex_Valid(t *testing.T) {
	// Known Tron Nile address and its hex equivalent
	const addr = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
	hexAddr, err := tron.AddressToHex(addr)
	if err != nil {
		t.Fatalf("AddressToHex: %v", err)
	}
	if !strings.HasPrefix(hexAddr, "41") {
		t.Errorf("hex address should start with '41', got %q", hexAddr)
	}
	if len(hexAddr) != 42 { // 21 bytes = 42 hex chars
		t.Errorf("hex address should be 42 chars, got %d", len(hexAddr))
	}
}

func TestAddressToHex_InvalidAddress(t *testing.T) {
	cases := []string{
		"",
		"invalid",
		"0x71C7656EC7ab88b098defB751B7401B5f6d8976F", // Ethereum address
		"TooShort",
	}
	for _, addr := range cases {
		if _, err := tron.AddressToHex(addr); err == nil {
			t.Errorf("AddressToHex(%q) expected error, got nil", addr)
		}
	}
}

func TestAddressRoundTrip(t *testing.T) {
	const addr = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"

	hexAddr, err := tron.AddressToHex(addr)
	if err != nil {
		t.Fatalf("AddressToHex: %v", err)
	}

	recovered, err := tron.AddressFromHex(hexAddr)
	if err != nil {
		t.Fatalf("AddressFromHex: %v", err)
	}

	if recovered != addr {
		t.Errorf("round-trip failed: got %q, want %q", recovered, addr)
	}
}

func TestIsValidAddress(t *testing.T) {
	validCases := []string{
		"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
		"TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj",
	}
	for _, addr := range validCases {
		if !tron.IsValidAddress(addr) {
			t.Errorf("IsValidAddress(%q) = false, want true", addr)
		}
	}

	invalidCases := []string{
		"",
		"not-a-tron-address",
		"0x71C7656EC7ab88b098defB751B7401B5f6d8976F",
	}
	for _, addr := range invalidCases {
		if tron.IsValidAddress(addr) {
			t.Errorf("IsValidAddress(%q) = true, want false", addr)
		}
	}
}

func TestEncodeTRC20Transfer(t *testing.T) {
	const toAddr = "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
	amount := new(big.Int).SetInt64(100_000_000) // 100 USDT

	encoded, err := tron.EncodeTRC20Transfer(toAddr, amount)
	if err != nil {
		t.Fatalf("EncodeTRC20Transfer: %v", err)
	}

	// Should be 128 hex chars (64 bytes = 32-byte address + 32-byte amount)
	if len(encoded) != 128 {
		t.Errorf("encoded params length = %d, want 128", len(encoded))
	}

	// Last 8 hex chars should be the amount in hex (100000000 = 0x5F5E100)
	amountHex := encoded[64+56:] // last 8 chars of the 64-char amount section
	expected := "05f5e100"
	if strings.ToLower(amountHex) != expected {
		t.Errorf("amount hex = %q, want %q", amountHex, expected)
	}
}

func TestEncodeTRC20Transfer_InvalidAddress(t *testing.T) {
	_, err := tron.EncodeTRC20Transfer("invalid", big.NewInt(1000))
	if err == nil {
		t.Error("expected error for invalid address, got nil")
	}
}

func TestTransactionHash(t *testing.T) {
	// SHA-256 of known input
	const rawHex = "0a02db5f2208d9f96b9b0e16f840b0f8bfad01527a6808c0843d5a024142" +
		"0973496e666f72776174696f6e"
	hash, err := tron.TransactionHash(rawHex)
	if err != nil {
		t.Fatalf("TransactionHash: %v", err)
	}
	if len(hash) != 32 {
		t.Errorf("hash length = %d, want 32", len(hash))
	}
}

func TestTransactionHash_InvalidHex(t *testing.T) {
	_, err := tron.TransactionHash("not-valid-hex!!")
	if err == nil {
		t.Error("expected error for invalid hex input, got nil")
	}
}

func TestExplorerURL(t *testing.T) {
	const base = "https://nile.tronscan.org"
	const hash = "abc123"
	url := tron.ExplorerURL(base, hash)
	expected := "https://nile.tronscan.org/#/transaction/abc123"
	if url != expected {
		t.Errorf("ExplorerURL = %q, want %q", url, expected)
	}
}
