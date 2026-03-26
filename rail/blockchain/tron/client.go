package tron

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/wallet"
)

const (
	// defaultFeeLimit is the maximum fee allowed for TRC20 transfers (100 TRX in SUN).
	defaultFeeLimit = int64(100 * 1_000_000)

	// trc20Decimals is the number of decimal places for USDT/USDC on Tron.
	trc20Decimals = int32(6)

	// energyPriceSUN is the approximate SUN cost per energy unit on Tron.
	energyPriceSUN = int64(280)

	// trc20TransferEnergy is the conservative energy estimate for a TRC20 transfer.
	trc20TransferEnergy = int64(30_000)

)

// Client is the Tron Nile testnet blockchain client.
// It implements domain.BlockchainClient.
type Client struct {
	rpc        *rpcClient
	walletMgr  *wallet.Manager
	config     TronConfig
	trxUSDRate decimal.Decimal
	logger     *slog.Logger
}

// Compile-time interface check.
var _ domain.BlockchainClient = (*Client)(nil)

// NewClient creates a new Tron blockchain client.
// walletMgr is required for SendTransaction; pass nil for a read-only client.
func NewClient(config TronConfig, walletMgr *wallet.Manager, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	rate := config.TRXUSDRate
	if rate == 0 {
		rate = 0.115
	}
	return &Client{
		rpc:        newRPCClient(config.RPCURL, config.APIKey, logger),
		walletMgr:  walletMgr,
		config:     config,
		trxUSDRate: decimal.NewFromFloat(rate),
		logger:     logger,
	}
}

// Chain returns the blockchain identifier.
func (c *Client) Chain() domain.CryptoChain {
	return domain.ChainTron
}

// GetBalance returns the TRX or TRC20 token balance for an address.
//
// If token is empty, the native TRX balance is returned (in TRX, not SUN).
// If token is a Base58Check TRC20 contract address, the token balance is returned
// assuming 6 decimal places (USDT/USDC standard on Tron).
// An unactivated address (no TRX ever received) returns zero without error.
func (c *Client) GetBalance(ctx context.Context, address, token string) (decimal.Decimal, error) {
	account, err := c.rpc.getAccount(ctx, address)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-tron: GetBalance(%s): %w", address, err)
	}

	// Unactivated account — balance is effectively zero
	if len(account.Data) == 0 {
		return decimal.Zero, nil
	}

	data := account.Data[0]

	if token == "" {
		// Native TRX: convert from SUN to TRX
		return decimal.NewFromInt(data.Balance).Div(decimal.NewFromInt(sunPerTRX)), nil
	}

	// TRC20 token: search the trc20 balance map returned by TronGrid.
	// TronGrid returns the contract address in Base58 format as the map key.
	for _, trc20Map := range data.TRC20 {
		for contractAddr, balanceStr := range trc20Map {
			if strings.EqualFold(contractAddr, token) {
				balance, ok := new(big.Int).SetString(balanceStr, 10)
				if !ok {
					return decimal.Zero, fmt.Errorf("settla-tron: invalid TRC20 balance string: %q", balanceStr)
				}
				return decimal.NewFromBigInt(balance, -trc20Decimals), nil
			}
		}
	}

	// Token not in the account's TRC20 map → balance is zero
	return decimal.Zero, nil
}

// EstimateGas returns an approximate gas cost in USD for a transaction.
//
// For TRC20 transfers, the estimate is based on the conservative energy ceiling
// (trc20TransferEnergy × energyPriceSUN). For native TRX transfers, bandwidth
// cost is returned. These are approximations; actual cost depends on network state.
func (c *Client) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	if req.Token == "" {
		// Native TRX transfer: bandwidth-only, ~0.001 TRX
		bandwidthTRX := decimal.NewFromFloat(0.001)
		return bandwidthTRX.Mul(c.trxUSDRate).Round(4), nil
	}

	// TRC20 transfer: energy cost dominates
	estimatedSUN := trc20TransferEnergy * energyPriceSUN // 8,400,000 SUN = 8.4 TRX
	estimatedTRX := decimal.NewFromInt(estimatedSUN).Div(decimal.NewFromInt(sunPerTRX))
	return estimatedTRX.Mul(c.trxUSDRate).Round(4), nil
}

// SendTransaction builds, signs, and broadcasts a Tron transaction.
//
// TxRequest.From is either:
//   - A wallet PATH (e.g., "system/tron/hot") — the wallet is loaded by path for signing.
//   - A Base58Check Tron address — the manager is scanned to find the matching wallet.
//
// TxRequest.To must be a valid Base58Check Tron recipient address.
// TxRequest.Token is the Base58Check TRC20 contract address; empty for native TRX.
// TxRequest.Amount is the human-readable amount (e.g., 100.5 for 100.5 USDT).
func (c *Client) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	if c.walletMgr == nil {
		return nil, fmt.Errorf("settla-tron: SendTransaction requires a wallet manager")
	}

	signer, err := c.resolveSignerWallet(req.From)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: resolving signer wallet: %w", err)
	}

	senderHex, err := AddressToHex(signer.Address)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: invalid sender address %q: %w", signer.Address, err)
	}

	var chainTx *domain.ChainTx
	if req.Token == "" {
		chainTx, err = c.sendTRX(ctx, senderHex, req.To, req.Amount, signer.Path)
	} else {
		chainTx, err = c.sendTRC20(ctx, senderHex, req.To, req.Token, req.Amount, signer.Path)
	}
	if err != nil {
		return nil, err
	}

	c.logger.Info("settla-tron: transaction broadcast",
		"tx_hash", chainTx.Hash,
		"from", signer.Address,
		"to", req.To,
		"amount", req.Amount,
		"token", req.Token,
		"explorer", ExplorerURL(c.config.ExplorerURL, chainTx.Hash),
	)

	return chainTx, nil
}

// sendTRC20 builds and broadcasts a TRC20 token transfer.
func (c *Client) sendTRC20(ctx context.Context, senderHex, toAddress, tokenContract string, amount decimal.Decimal, walletPath string) (*domain.ChainTx, error) {
	contractHex, err := AddressToHex(tokenContract)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: invalid token contract %q: %w", tokenContract, err)
	}

	// Convert human-readable amount to base units (6 decimals for USDT/USDC)
	amountBase := amount.Mul(decimal.New(1, trc20Decimals)).BigInt()

	// ABI-encode the transfer(address,uint256) call parameters
	params, err := EncodeTRC20Transfer(toAddress, amountBase)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: encoding TRC20 params: %w", err)
	}

	// Build unsigned transaction via TronGrid
	triggerResp, err := c.rpc.triggerSmartContract(ctx, TriggerSmartContractReq{
		OwnerAddress:     senderHex,
		ContractAddress:  contractHex,
		FunctionSelector: "transfer(address,uint256)",
		Parameter:        params,
		FeeLimit:         defaultFeeLimit,
		CallValue:        0,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-tron: building TRC20 transaction: %w", err)
	}

	// Sign and broadcast
	return c.signAndBroadcast(ctx, triggerResp.Transaction, walletPath)
}

// sendTRX builds and broadcasts a native TRX transfer.
func (c *Client) sendTRX(ctx context.Context, senderHex, toAddress string, amount decimal.Decimal, walletPath string) (*domain.ChainTx, error) {
	toHex, err := AddressToHex(toAddress)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: invalid recipient address %q: %w", toAddress, err)
	}

	amountSUN := amount.Mul(decimal.NewFromInt(sunPerTRX)).IntPart()
	tx, err := c.rpc.createTRXTransaction(ctx, senderHex, toHex, amountSUN)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: building TRX transaction: %w", err)
	}

	return c.signAndBroadcast(ctx, *tx, walletPath)
}

// signAndBroadcast signs an unsigned TronTx with the wallet at walletPath and broadcasts it.
func (c *Client) signAndBroadcast(ctx context.Context, tx TronTx, walletPath string) (*domain.ChainTx, error) {
	// Compute SHA-256 hash of the raw transaction bytes — this is what gets signed on Tron
	txHash, err := TransactionHash(tx.RawDataHex)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: computing transaction hash: %w", err)
	}

	// Sign via wallet manager (private key never leaves the wallet package)
	sig, err := c.walletMgr.SignTransaction(ctx, walletPath, txHash)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: signing transaction: %w", err)
	}

	// Attach signature and broadcast
	tx.Signature = []string{hex.EncodeToString(sig)}
	broadcastResp, err := c.rpc.broadcastTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: broadcasting transaction: %w", err)
	}

	txID := broadcastResp.TxID
	if txID == "" {
		txID = tx.TxID // fallback to the ID from the build step
	}

	return &domain.ChainTx{
		Hash:   txID,
		Status: "pending",
	}, nil
}

// GetTransaction retrieves a transaction by hash and returns its current status.
//
// Status values:
//   - "pending"    — not yet included in a block
//   - "confirming" — included but below the confirmation threshold
//   - "confirmed"  — reached the required number of confirmations
//   - "failed"     — transaction reverted on-chain
func (c *Client) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	info, err := c.rpc.getTransactionInfo(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("settla-tron: GetTransaction(%s): %w", hash, err)
	}

	if info.ID == "" {
		return nil, fmt.Errorf("settla-tron: transaction not found: %s", hash)
	}

	currentBlock, err := c.rpc.getNowBlock(ctx)
	if err != nil {
		currentBlock = 0 // best-effort; confirmations will be 0
	}

	confirmations := 0
	if currentBlock > 0 && info.BlockNumber > 0 {
		if diff := currentBlock - info.BlockNumber; diff > 0 {
			confirmations = int(diff)
		}
	}

	status := "pending"
	if info.BlockNumber > 0 {
		receiptOK := info.Receipt.Result == "SUCCESS" || info.Receipt.Result == ""
		if !receiptOK {
			status = "failed"
		} else if confirmations >= c.config.Confirmations {
			status = "confirmed"
		} else {
			status = "confirming"
		}
	}

	feeDecimal := decimal.NewFromInt(info.Fee).Div(decimal.NewFromInt(sunPerTRX))

	return &domain.ChainTx{
		Hash:          hash,
		Status:        status,
		Confirmations: confirmations,
		BlockNumber:   uint64(info.BlockNumber),
		Fee:           feeDecimal,
	}, nil
}

// SubscribeTransactions starts a goroutine that polls TronGrid for confirmed TRC20
// transfers to or from address and sends them on ch.
//
// The goroutine runs until ctx is cancelled. The first call returns nil immediately;
// errors during polling are logged but do not stop the subscription.
func (c *Client) SubscribeTransactions(ctx context.Context, address string, ch chan<- domain.ChainTx) error {
	go func() {
		ticker := time.NewTicker(c.config.BlockTime)
		defer ticker.Stop()

		seen := make(map[string]struct{})

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.pollAndDispatch(ctx, address, seen, ch)
			}
		}
	}()
	return nil
}

// pollAndDispatch fetches recent TRC20 transfers and sends newly-confirmed ones to ch.
func (c *Client) pollAndDispatch(ctx context.Context, address string, seen map[string]struct{}, ch chan<- domain.ChainTx) {
	transfers, err := c.rpc.getTRC20Transfers(ctx, address, "", 20)
	if err != nil {
		c.logger.Warn("settla-tron: polling TRC20 transfers",
			"address", address,
			"error", err,
		)
		return
	}

	for _, t := range transfers.Data {
		if _, ok := seen[t.TransactionID]; ok {
			continue
		}

		chainTx, err := c.GetTransaction(ctx, t.TransactionID)
		if err != nil {
			continue
		}

		if chainTx.Status == "confirmed" {
			seen[t.TransactionID] = struct{}{}
			select {
			case ch <- *chainTx:
			case <-ctx.Done():
				return
			}
		}
	}
}

// resolveSignerWallet finds the wallet to use for signing.
//
// If from contains "/" it is treated as a wallet path (e.g., "system/tron/hot").
// Otherwise it is treated as a Base58Check Tron address and the wallet manager
// is scanned for a matching Tron wallet.
func (c *Client) resolveSignerWallet(from string) (*wallet.Wallet, error) {
	if c.walletMgr == nil {
		return nil, fmt.Errorf("wallet manager not configured")
	}

	// Wallet paths contain "/" (e.g., "system/tron/hot", "tenant/lemfi/tron")
	if strings.Contains(from, "/") {
		return c.walletMgr.GetOrCreateWallet(context.Background(), from, wallet.ChainTron, nil)
	}

	// Otherwise, scan Tron wallets for an address match
	wallets, err := c.walletMgr.ListWallets(string(wallet.ChainTron))
	if err != nil {
		return nil, fmt.Errorf("listing tron wallets: %w", err)
	}
	for _, w := range wallets {
		if w.Address == from {
			return w, nil
		}
	}
	return nil, fmt.Errorf("no tron wallet found for address: %s", from)
}
