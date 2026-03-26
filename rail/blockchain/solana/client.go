package solana

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"sync"
	"time"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// WalletSigner provides Ed25519 key material for signing Solana transactions.
// *wallet.Manager satisfies this interface via GetEd25519KeyForSigning.
type WalletSigner interface {
	GetEd25519KeyForSigning(walletPath string) (ed25519.PrivateKey, error)
}

// Client implements domain.BlockchainClient for Solana.
// It connects to Solana Devnet (or any configured RPC endpoint) and supports:
//   - Native SOL and SPL token balance queries.
//   - SPL token transfers with automatic ATA creation for new recipients.
//   - Transaction status polling.
var _ domain.BlockchainClient = (*Client)(nil)

// Client is the Solana blockchain client.
type Client struct {
	rpc    *rpcClient
	cfg    Config
	signer WalletSigner
	logger *slog.Logger

	// walletIndex maps a blockchain address string to a wallet path for signing.
	// Populated via RegisterWallet.
	walletIndex   map[string]string
	walletIndexMu sync.RWMutex
}

// New creates a new Solana client.
// signer may be nil if SendTransaction will never be called (read-only use).
func New(cfg Config, signer WalletSigner) *Client {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	commitment := cfg.Commitment
	if commitment == "" {
		commitment = CommitmentConfirmed
	}

	return &Client{
		rpc:         newRPCClient(cfg.RPCURL, commitment, logger),
		cfg:         cfg,
		signer:      signer,
		logger:      logger,
		walletIndex: make(map[string]string),
	}
}

// RegisterWallet maps a blockchain address to a wallet path that the WalletSigner
// can use for signing. Must be called before SendTransaction for each wallet.
func (c *Client) RegisterWallet(address, walletPath string) {
	c.walletIndexMu.Lock()
	c.walletIndex[address] = walletPath
	c.walletIndexMu.Unlock()
}

// Chain implements domain.BlockchainClient.
func (c *Client) Chain() domain.CryptoChain {
	return chainIdentifier
}

// GetBalance implements domain.BlockchainClient.
// If token is "" or "SOL", returns the native SOL balance (in SOL, not lamports).
// Otherwise, token is treated as a mint address and the SPL token balance is returned.
func (c *Client) GetBalance(ctx context.Context, address, token string) (decimal.Decimal, error) {
	owner, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-solana: invalid address %q: %w", address, err)
	}

	if token == "" || token == "SOL" {
		return c.getNativeBalance(ctx, owner)
	}

	return c.getSPLBalance(ctx, owner, token)
}

// getNativeBalance returns the SOL balance for the owner (in SOL, not lamports).
func (c *Client) getNativeBalance(ctx context.Context, owner solana.PublicKey) (decimal.Decimal, error) {
	lamports, err := c.rpc.getSOLBalance(ctx, owner)
	if err != nil {
		return decimal.Zero, err
	}

	sol := decimal.NewFromUint64(lamports).Div(decimal.NewFromInt(lamportsPerSOL))
	return sol, nil
}

// getSPLBalance returns the SPL token balance for the owner's ATA for the given mint.
// Returns zero if no ATA exists yet (the owner simply has no tokens yet).
func (c *Client) getSPLBalance(ctx context.Context, owner solana.PublicKey, mintAddr string) (decimal.Decimal, error) {
	mint, err := solana.PublicKeyFromBase58(mintAddr)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-solana: invalid mint address %q: %w", mintAddr, err)
	}

	// Derive the owner's ATA for this mint.
	ataAddr, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-solana: derive ATA: %w", err)
	}

	uiAmountStr, _, err := c.rpc.getTokenAccountBalance(ctx, ataAddr)
	if err != nil {
		return decimal.Zero, err
	}

	balance, err := decimal.NewFromString(uiAmountStr)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-solana: parse token balance %q: %w", uiAmountStr, err)
	}

	return balance, nil
}

// EstimateGas implements domain.BlockchainClient.
// Returns a conservative estimate in SOL that covers the transaction signature fee
// plus ATA creation rent if the recipient has no existing token account.
func (c *Client) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	// Base signature fee + ATA rent-exempt minimum (~0.002039 SOL total).
	totalLamports := int64(baseFeePerSignature + ataRentExemptLamports)
	fee := decimal.NewFromInt(totalLamports).Div(decimal.NewFromInt(lamportsPerSOL))
	return fee, nil
}

// SendTransaction implements domain.BlockchainClient.
// Builds, signs, and submits an SPL token transfer.
//
// req.From must have been registered via RegisterWallet.
// req.Token must be the mint address of the SPL token to transfer.
// req.Amount is the human-readable amount (e.g., "1.50" for 1.50 USDC).
//
// If the recipient has no ATA for this mint, one is created automatically
// (funded by the sender).
func (c *Client) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	if req.Token == "" {
		return nil, fmt.Errorf("settla-solana: req.Token (mint address) is required")
	}
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, fmt.Errorf("settla-solana: req.Amount must be positive")
	}

	// Look up wallet path for signing.
	c.walletIndexMu.RLock()
	walletPath, ok := c.walletIndex[req.From]
	c.walletIndexMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("settla-solana: no wallet registered for address %s; call RegisterWallet first", req.From)
	}

	// Get Ed25519 private key.
	if c.signer == nil {
		return nil, fmt.Errorf("settla-solana: no WalletSigner configured")
	}

	privateKey, err := c.signer.GetEd25519KeyForSigning(walletPath)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: failed to retrieve signing key: %w", err)
	}

	// Zero private key after use.
	defer func() {
		for i := range privateKey {
			privateKey[i] = 0
		}
	}()

	// Parse addresses.
	sender, err := solana.PublicKeyFromBase58(req.From)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: invalid sender address %q: %w", req.From, err)
	}

	recipient, err := solana.PublicKeyFromBase58(req.To)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: invalid recipient address %q: %w", req.To, err)
	}

	mint, err := solana.PublicKeyFromBase58(req.Token)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: invalid mint address %q: %w", req.Token, err)
	}

	// Build, sign, and submit.
	result, err := buildSPLTransfer(ctx, c.rpc, sender, recipient, mint, req.Amount, req.Memo, []byte(privateKey))
	if err != nil {
		return nil, err
	}

	sig, err := c.rpc.sendTransaction(ctx, result.tx)
	if err != nil {
		return nil, err
	}

	c.logger.Info("settla-solana: transaction submitted",
		"signature", sig.String(),
		"from", req.From,
		"to", req.To,
		"amount", req.Amount,
		"mint", req.Token,
		"created_ata", result.createdATA,
	)

	return &domain.ChainTx{
		Hash:   sig.String(),
		Status: "submitted",
	}, nil
}

// GetTransaction implements domain.BlockchainClient.
// Retrieves a transaction by its base-58 signature string.
func (c *Client) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	sig, err := solana.SignatureFromBase58(hash)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: invalid signature %q: %w", hash, err)
	}

	result, err := c.rpc.getTransaction(ctx, sig)
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, fmt.Errorf("settla-solana: transaction not found: %s", hash)
	}

	chainTx := &domain.ChainTx{
		Hash:        hash,
		Status:      "pending",
		BlockNumber: result.Slot,
	}

	if result.Meta != nil {
		if result.Meta.Err != nil {
			chainTx.Status = "failed"
		} else {
			chainTx.Status = "confirmed"
			feeLamports := decimal.NewFromUint64(result.Meta.Fee)
			chainTx.Fee = feeLamports.Div(decimal.NewFromInt(lamportsPerSOL))
		}
	}

	return chainTx, nil
}

// SubscribeTransactions implements domain.BlockchainClient.
// Polls for new transactions involving address every 2 seconds and sends them on ch.
// Returns immediately; polling runs in a background goroutine until ctx is cancelled.
func (c *Client) SubscribeTransactions(ctx context.Context, address string, ch chan<- domain.ChainTx) error {
	pubkey, err := solana.PublicKeyFromBase58(address)
	if err != nil {
		return fmt.Errorf("settla-solana: invalid address %q: %w", address, err)
	}

	go c.pollTransactions(ctx, pubkey, address, ch)
	return nil
}

// pollTransactions is the background goroutine for SubscribeTransactions.
func (c *Client) pollTransactions(ctx context.Context, pubkey solana.PublicKey, address string, ch chan<- domain.ChainTx) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Track the newest signature we've already sent so we don't re-deliver.
	var lastSig solana.Signature

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.deliverNewTransactions(ctx, pubkey, address, ch, &lastSig)
		}
	}
}

// deliverNewTransactions fetches signatures for address, sends any that are newer than lastSig.
func (c *Client) deliverNewTransactions(
	ctx context.Context,
	pubkey solana.PublicKey,
	address string,
	ch chan<- domain.ChainTx,
	lastSig *solana.Signature,
) {
	sigs, err := c.rpc.getSignaturesForAddress(ctx, pubkey, 20)
	if err != nil {
		c.logger.Warn("settla-solana: polling signatures failed",
			"address", address,
			"error", err,
		)
		return
	}

	// Signatures are newest-first. Find where our lastSig falls and send everything newer.
	var toDeliver []*rpc.TransactionSignature
	for _, info := range sigs {
		if info.Signature == *lastSig {
			break
		}
		toDeliver = append(toDeliver, info)
	}

	// Deliver in chronological order (oldest first).
	for i := len(toDeliver) - 1; i >= 0; i-- {
		info := toDeliver[i]
		status := "confirmed"
		if info.Err != nil {
			status = "failed"
		}

		select {
		case ch <- domain.ChainTx{
			Hash:        info.Signature.String(),
			Status:      status,
			BlockNumber: info.Slot,
		}:
		case <-ctx.Done():
			return
		}
	}

	// Update lastSig to the newest AFTER successful delivery of all transactions.
	// If ctx.Done() fires mid-delivery, lastSig stays unchanged and next poll retries.
	if len(sigs) > 0 {
		*lastSig = sigs[0].Signature
	}
}

// ExplorerURL returns the Solana Explorer URL for a transaction on the configured cluster.
func (c *Client) ExplorerURL(txHash string) string {
	return fmt.Sprintf("%s/tx/%s?cluster=devnet", c.cfg.ExplorerURL, txHash)
}

// Logger returns this client's structured logger.
func (c *Client) Logger() *slog.Logger {
	return c.logger
}
