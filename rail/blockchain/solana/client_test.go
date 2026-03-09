package solana_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	solanalib "github.com/gagliardetto/solana-go"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	solanachain "github.com/intellect4all/settla/rail/blockchain/solana"
)

// ---- mock RPC server --------------------------------------------------------

// mockRPCServer creates an httptest server responding to Solana JSON-RPC calls.
// handler receives the RPC method name and params; its return value becomes "result".
func mockRPCServer(t *testing.T, handler func(method string, params []interface{}) interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string        `json:"jsonrpc"`
			ID      interface{}   `json:"id"`
			Method  string        `json:"method"`
			Params  []interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
	}))
}

// newTestClient creates a Client pointed at the given mock server URL.
func newTestClient(t *testing.T, serverURL string, signer solanachain.WalletSigner) *solanachain.Client {
	t.Helper()
	cfg := solanachain.Config{
		RPCURL:        serverURL,
		ExplorerURL:   "https://explorer.solana.com",
		USDCMint:      "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		BlockTime:     400 * time.Millisecond,
		Confirmations: 32,
		Commitment:    solanachain.CommitmentConfirmed,
		Logger:        slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	return solanachain.New(cfg, signer)
}

// ---- mock signer ------------------------------------------------------------

type mockSigner struct {
	key ed25519.PrivateKey
}

func (m *mockSigner) GetEd25519KeyForSigning(_ string) (ed25519.PrivateKey, error) {
	if m.key == nil {
		return nil, fmt.Errorf("mock: no key configured")
	}
	return m.key, nil
}

// ---- Chain() ----------------------------------------------------------------

func TestChain(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	if c.Chain() != "solana" {
		t.Fatalf("want chain=solana, got %q", c.Chain())
	}
}

// ---- GetBalance: native SOL -------------------------------------------------

func TestGetBalance_SOL(t *testing.T) {
	const testAddress = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	const expectedLamports = 2_500_000_000 // 2.5 SOL

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		if method != "getBalance" {
			t.Errorf("unexpected method %q", method)
		}
		return map[string]interface{}{
			"context": map[string]interface{}{"slot": 1},
			"value":   float64(expectedLamports),
		}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	balance, err := c.GetBalance(context.Background(), testAddress, "SOL")
	if err != nil {
		t.Fatalf("GetBalance SOL: %v", err)
	}

	want := decimal.NewFromFloat(2.5)
	if !balance.Equal(want) {
		t.Errorf("GetBalance SOL: want %s, got %s", want, balance)
	}
}

// ---- GetBalance: empty token string → native SOL ----------------------------

func TestGetBalance_EmptyToken_ReturnSOL(t *testing.T) {
	const testAddress = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		if method != "getBalance" {
			t.Errorf("unexpected method for empty token: %q (want getBalance)", method)
		}
		return map[string]interface{}{
			"context": map[string]interface{}{"slot": 1},
			"value":   float64(1_000_000_000), // 1 SOL
		}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	balance, err := c.GetBalance(context.Background(), testAddress, "")
	if err != nil {
		t.Fatalf("GetBalance empty token: %v", err)
	}
	if !balance.Equal(decimal.NewFromFloat(1.0)) {
		t.Errorf("want 1.0 SOL, got %s", balance)
	}
}

// ---- GetBalance: SPL token --------------------------------------------------

func TestGetBalance_SPL(t *testing.T) {
	const ownerAddr = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	const mintAddr = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		switch method {
		case "getTokenAccountBalance":
			return map[string]interface{}{
				"context": map[string]interface{}{"slot": 100},
				"value": map[string]interface{}{
					"amount":         "500000000",
					"decimals":       6,
					"uiAmount":       500.0,
					"uiAmountString": "500.000000",
				},
			}
		default:
			// ATA derivation is local; only getTokenAccountBalance hits network.
			return nil
		}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	balance, err := c.GetBalance(context.Background(), ownerAddr, mintAddr)
	if err != nil {
		t.Fatalf("GetBalance SPL: %v", err)
	}
	want := decimal.NewFromFloat(500.0)
	if !balance.Equal(want) {
		t.Errorf("want %s, got %s", want, balance)
	}
}

// ---- GetBalance: SPL token — no ATA exists (zero balance) ------------------

func TestGetBalance_SPL_NoATA(t *testing.T) {
	const ownerAddr = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	const mintAddr = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		// No ATA → getTokenAccountBalance returns null result.
		return nil
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	balance, err := c.GetBalance(context.Background(), ownerAddr, mintAddr)
	if err != nil {
		t.Fatalf("GetBalance SPL no ATA: %v", err)
	}
	if !balance.Equal(decimal.Zero) {
		t.Errorf("expected zero balance for non-existent ATA, got %s", balance)
	}
}

// ---- GetBalance: invalid address -------------------------------------------

func TestGetBalance_InvalidAddress(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	_, err := c.GetBalance(context.Background(), "not-a-valid-address!!!", "SOL")
	if err == nil {
		t.Fatal("expected error for invalid address, got nil")
	}
}

// ---- EstimateGas ------------------------------------------------------------

func TestEstimateGas(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)

	req := domain.TxRequest{
		From:   "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		To:     "4Nd1mBQtrMJVYVfKf2PX98ej5qR8A8n6vK9wSxRs3Jj",
		Token:  "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		Amount: decimal.NewFromFloat(1.0),
	}

	fee, err := c.EstimateGas(context.Background(), req)
	if err != nil {
		t.Fatalf("EstimateGas: %v", err)
	}

	if fee.LessThanOrEqual(decimal.Zero) {
		t.Errorf("fee must be positive, got %s", fee)
	}
	// Fee should be < 0.01 SOL (~0.00204 expected).
	if fee.GreaterThan(decimal.NewFromFloat(0.01)) {
		t.Errorf("fee seems too large: %s SOL", fee)
	}
}

// ---- GetTransaction: confirmed ----------------------------------------------

func TestGetTransaction_Confirmed(t *testing.T) {
	const txHash = "5WcNsZFNpMjKmEEJbxmhSb4QCJnBpBMGbaMFGmhZ3G3kHJeF7xDMFJmR5rAKyNGpVjTdQXzqFNEBm5gJVFxzrKM"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		if method != "getTransaction" {
			t.Errorf("unexpected method %q", method)
		}
		return map[string]interface{}{
			"slot": float64(12345678),
			"meta": map[string]interface{}{
				"err":          nil,
				"fee":          float64(5000),
				"preBalances":  []interface{}{float64(1_000_000_000)},
				"postBalances": []interface{}{float64(999_995_000)},
			},
			"blockTime": float64(1_700_000_000),
		}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	tx, err := c.GetTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransaction confirmed: %v", err)
	}

	if tx.Hash != txHash {
		t.Errorf("want hash=%s, got %s", txHash, tx.Hash)
	}
	if tx.Status != "confirmed" {
		t.Errorf("want status=confirmed, got %s", tx.Status)
	}
	if tx.BlockNumber != 12345678 {
		t.Errorf("want BlockNumber=12345678, got %d", tx.BlockNumber)
	}
	expectedFee := decimal.NewFromInt(5000).Div(decimal.NewFromInt(1_000_000_000))
	if !tx.Fee.Equal(expectedFee) {
		t.Errorf("want fee=%s, got %s", expectedFee, tx.Fee)
	}
}

// ---- GetTransaction: failed tx ----------------------------------------------

func TestGetTransaction_Failed(t *testing.T) {
	const txHash = "5WcNsZFNpMjKmEEJbxmhSb4QCJnBpBMGbaMFGmhZ3G3kHJeF7xDMFJmR5rAKyNGpVjTdQXzqFNEBm5gJVFxzrKM"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		return map[string]interface{}{
			"slot": float64(12345679),
			"meta": map[string]interface{}{
				"err": map[string]interface{}{
					"InstructionError": []interface{}{0, "InvalidArgument"},
				},
				"fee":          float64(5000),
				"preBalances":  []interface{}{float64(1_000_000_000)},
				"postBalances": []interface{}{float64(999_995_000)},
			},
		}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	tx, err := c.GetTransaction(context.Background(), txHash)
	if err != nil {
		t.Fatalf("GetTransaction failed tx: %v", err)
	}
	if tx.Status != "failed" {
		t.Errorf("want status=failed, got %s", tx.Status)
	}
}

// ---- GetTransaction: invalid signature -------------------------------------

func TestGetTransaction_InvalidSignature(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	_, err := c.GetTransaction(context.Background(), "not-a-valid-sig")
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
}

// ---- RegisterWallet --------------------------------------------------------

func TestRegisterWallet(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	// Must not panic.
	c.RegisterWallet("7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU", "system/hot/solana")
}

// ---- SendTransaction: input validation -------------------------------------

func TestSendTransaction_NoWalletRegistered(t *testing.T) {
	c := newTestClient(t, "http://localhost", &mockSigner{})

	_, err := c.SendTransaction(context.Background(), domain.TxRequest{
		From:   "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		To:     "4Nd1mBQtrMJVYVfKf2PX98ej5qR8A8n6vK9wSxRs3Jj",
		Token:  "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		Amount: decimal.NewFromFloat(1.0),
	})
	if err == nil {
		t.Fatal("expected error when no wallet registered, got nil")
	}
}

func TestSendTransaction_ZeroAmount(t *testing.T) {
	c := newTestClient(t, "http://localhost", &mockSigner{})
	c.RegisterWallet("7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU", "system/hot/solana")

	_, err := c.SendTransaction(context.Background(), domain.TxRequest{
		From:   "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		To:     "4Nd1mBQtrMJVYVfKf2PX98ej5qR8A8n6vK9wSxRs3Jj",
		Token:  "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		Amount: decimal.Zero,
	})
	if err == nil {
		t.Fatal("expected error for zero amount, got nil")
	}
}

func TestSendTransaction_MissingToken(t *testing.T) {
	c := newTestClient(t, "http://localhost", &mockSigner{})
	c.RegisterWallet("7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU", "system/hot/solana")

	_, err := c.SendTransaction(context.Background(), domain.TxRequest{
		From:   "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		To:     "4Nd1mBQtrMJVYVfKf2PX98ej5qR8A8n6vK9wSxRs3Jj",
		Token:  "", // missing token mint
		Amount: decimal.NewFromFloat(1.0),
	})
	if err == nil {
		t.Fatal("expected error for missing token mint, got nil")
	}
}

// ---- SendTransaction: end-to-end with mocked RPC ---------------------------

func TestSendTransaction_Success(t *testing.T) {
	// Generate a real Ed25519 keypair for the sender.
	_, senderPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}

	senderPub := solanalib.PublicKeyFromBytes(senderPriv.Public().(ed25519.PublicKey))
	senderAddr := senderPub.String()
	// Generate a valid recipient keypair.
	_, recipientPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipientPub := solanalib.PublicKeyFromBytes(recipientPriv.Public().(ed25519.PublicKey))
	recipientAddr := recipientPub.String()
	mintAddr := "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"

	const fakeSig = "5WcNsZFNpMjKmEEJbxmhSb4QCJnBpBMGbaMFGmhZ3G3kHJeF7xDMFJmR5rAKyNGpVjTdQXzqFNEBm5gJVFxzrKM"

	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		switch method {
		case "getAccountInfo":
			// Solana RPC returns {"context": {...}, "value": null} when an account
			// doesn't exist. The gagliardetto library translates value=null → ErrNotFound.
			return map[string]interface{}{
				"context": map[string]interface{}{"slot": 100},
				"value":   nil,
			}
		case "getTokenSupply":
			return map[string]interface{}{
				"context": map[string]interface{}{"slot": 100},
				"value": map[string]interface{}{
					"amount":         "1000000000000",
					"decimals":       6,
					"uiAmount":       1000000.0,
					"uiAmountString": "1000000.000000",
				},
			}
		case "getLatestBlockhash":
			return map[string]interface{}{
				"context": map[string]interface{}{"slot": 100},
				"value": map[string]interface{}{
					"blockhash":            "BvCaQGt1MaCo6NXqKwHLbFk9VjBMVHN9P5JjLcbJF8gu",
					"lastValidBlockHeight": 150,
				},
			}
		case "sendTransaction":
			return fakeSig
		default:
			// Other methods (simulateTransaction, etc.) return nil.
			return nil
		}
	})
	defer srv.Close()

	ms := &mockSigner{key: senderPriv}
	c := newTestClient(t, srv.URL, ms)
	c.RegisterWallet(senderAddr, "system/hot/solana")

	tx, err := c.SendTransaction(context.Background(), domain.TxRequest{
		From:   senderAddr,
		To:     recipientAddr,
		Token:  mintAddr,
		Amount: decimal.NewFromFloat(10.5),
	})
	if err != nil {
		t.Fatalf("SendTransaction: %v", err)
	}

	if tx.Hash == "" {
		t.Fatal("expected non-empty tx hash")
	}
	if tx.Status != "submitted" {
		t.Errorf("want status=submitted, got %s", tx.Status)
	}
}

// ---- SubscribeTransactions -------------------------------------------------

func TestSubscribeTransactions_InvalidAddress(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	ch := make(chan domain.ChainTx, 1)
	err := c.SubscribeTransactions(context.Background(), "not-valid!", ch)
	if err == nil {
		t.Fatal("expected error for invalid address, got nil")
	}
}

func TestSubscribeTransactions_DeliversTx(t *testing.T) {
	const testAddr = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	const fakeSig = "5WcNsZFNpMjKmEEJbxmhSb4QCJnBpBMGbaMFGmhZ3G3kHJeF7xDMFJmR5rAKyNGpVjTdQXzqFNEBm5gJVFxzrKM"

	callCount := 0
	srv := mockRPCServer(t, func(method string, _ []interface{}) interface{} {
		if method != "getSignaturesForAddress" {
			return nil
		}
		callCount++
		if callCount == 1 {
			// First poll: return one confirmed signature.
			return []interface{}{
				map[string]interface{}{
					"signature":          fakeSig,
					"slot":               float64(100),
					"confirmationStatus": "confirmed",
					"err":                nil,
				},
			}
		}
		// Subsequent polls: no new signatures.
		return []interface{}{}
	})
	defer srv.Close()

	c := newTestClient(t, srv.URL, nil)
	ch := make(chan domain.ChainTx, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if err := c.SubscribeTransactions(ctx, testAddr, ch); err != nil {
		t.Fatalf("SubscribeTransactions: %v", err)
	}

	select {
	case tx := <-ch:
		if tx.Hash != fakeSig {
			t.Errorf("want hash=%s, got %s", fakeSig, tx.Hash)
		}
		if tx.Status != "confirmed" {
			t.Errorf("want status=confirmed, got %s", tx.Status)
		}
		if tx.BlockNumber != 100 {
			t.Errorf("want BlockNumber=100, got %d", tx.BlockNumber)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for transaction from SubscribeTransactions")
	}
}

// ---- ExplorerURL ------------------------------------------------------------

func TestExplorerURL(t *testing.T) {
	c := newTestClient(t, "http://localhost", nil)
	const sig = "5WcNsZFNpMjKmEEJbxmhSb4QCJnBpBMGbaMFGmhZ3G3kHJeF7xDMFJmR5rAKyNGpVjTdQXzqFNEBm5gJVFxzrKM"
	url := c.ExplorerURL(sig)

	want := fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=devnet", sig)
	if url != want {
		t.Errorf("ExplorerURL: want %q, got %q", want, url)
	}
}

// ---- DevnetConfig defaults -------------------------------------------------

func TestDevnetConfig(t *testing.T) {
	cfg := solanachain.DevnetConfig()
	if cfg.RPCURL == "" {
		t.Error("RPCURL should not be empty")
	}
	if cfg.USDCMint == "" {
		t.Error("USDCMint should not be empty")
	}
	if cfg.Confirmations <= 0 {
		t.Error("Confirmations should be positive")
	}
}
