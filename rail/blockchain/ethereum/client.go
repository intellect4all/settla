package ethereum

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	geth "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// WalletManager provides ECDSA private keys for transaction signing.
// Implemented by rail/wallet.Manager.
type WalletManager interface {
	GetPrivateKeyForSigning(walletPath string) (*ecdsa.PrivateKey, error)
}

// Signer signs EVM transactions on behalf of a given sender address.
type Signer interface {
	SignTx(ctx context.Context, from common.Address, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

// WalletSigner implements Signer using the settla wallet manager.
// Register each wallet address with RegisterWallet before use.
type WalletSigner struct {
	manager WalletManager
	chainID *big.Int

	mu            sync.RWMutex
	addressToPath map[string]string // lowercase 0x address → wallet path
}

// NewWalletSigner creates a signer backed by the settla wallet manager.
func NewWalletSigner(manager WalletManager, chainID *big.Int) *WalletSigner {
	return &WalletSigner{
		manager:       manager,
		chainID:       chainID,
		addressToPath: make(map[string]string),
	}
}

// RegisterWallet maps a blockchain address to its wallet manager path.
// Call this for every address you intend to sign transactions from.
// Example: RegisterWallet("0xABC...", "system/hot/ethereum")
func (ws *WalletSigner) RegisterWallet(address, walletPath string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.addressToPath[strings.ToLower(address)] = walletPath
}

// SignTx signs the transaction using the registered wallet for from.
func (ws *WalletSigner) SignTx(_ context.Context, from common.Address, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	ws.mu.RLock()
	path, ok := ws.addressToPath[strings.ToLower(from.Hex())]
	ws.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("settla-ethereum: no wallet registered for address %s", from.Hex())
	}

	privKey, err := ws.manager.GetPrivateKeyForSigning(path)
	if err != nil {
		return nil, fmt.Errorf("settla-ethereum: get signing key for %s: %w", from.Hex(), err)
	}

	signer := types.LatestSignerForChainID(chainID)
	signed, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		return nil, fmt.Errorf("settla-ethereum: sign tx: %w", err)
	}
	return signed, nil
}

// Client implements domain.BlockchainClient for EVM chains.
// A single Client instance works for one chain (e.g., Sepolia or Base Sepolia).
type Client struct {
	config  Config
	chainID *big.Int
	rpc     *rpcClient
	signer  Signer
	nonces  *nonceManager
	logger  *slog.Logger
}

// Compile-time check that Client satisfies domain.BlockchainClient.
var _ domain.BlockchainClient = (*Client)(nil)

// NewClient creates a new EVM blockchain client and connects to the RPC endpoint.
func NewClient(config Config, signer Signer, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.Default()
	}

	rpc, err := newRPCClient(config.RPCURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		config:  config,
		chainID: big.NewInt(config.ChainID),
		rpc:     rpc,
		signer:  signer,
		nonces:  newNonceManager(),
		logger:  logger,
	}, nil
}

// newClientWithRPC constructs a Client with a pre-built rpcClient. Used in tests.
func newClientWithRPC(config Config, rpc *rpcClient, signer Signer, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		config:  config,
		chainID: big.NewInt(config.ChainID),
		rpc:     rpc,
		signer:  signer,
		nonces:  newNonceManager(),
		logger:  logger,
	}
}

// Close releases the underlying RPC connection.
func (c *Client) Close() {
	c.rpc.close()
}

// Chain returns the blockchain identifier (e.g., "ethereum", "base").
func (c *Client) Chain() string {
	return c.config.ChainName
}

// GetBalance returns the token balance for address.
// Pass token="" or "ETH" for native ETH. Pass a token symbol (e.g., "USDC")
// or a 0x contract address to query an ERC20 balance.
func (c *Client) GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error) {
	addr, err := normalizeAddress(address)
	if err != nil {
		return decimal.Zero, err
	}

	// Native ETH balance
	if token == "" || strings.EqualFold(token, "ETH") {
		wei, err := c.rpc.ethBalance(ctx, addr)
		if err != nil {
			return decimal.Zero, fmt.Errorf("settla-ethereum: ETH balance of %s on %s: %w", address, c.config.ChainName, err)
		}
		return fromOnChainAmount(wei, 18), nil
	}

	// ERC20 balance — resolve contract address
	contractStr, err := c.config.ContractAddress(token)
	if err != nil {
		// Allow passing a raw 0x contract address as token
		if strings.HasPrefix(token, "0x") || strings.HasPrefix(token, "0X") {
			contractStr = token
		} else {
			return decimal.Zero, err
		}
	}

	contract, err := normalizeAddress(contractStr)
	if err != nil {
		return decimal.Zero, err
	}

	callData := encodeERC20BalanceOf(addr)
	result, err := c.rpc.callContract(ctx, geth.CallMsg{
		To:   &contract,
		Data: callData,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-ethereum: balanceOf(%s, %s) on %s: %w", address, token, c.config.ChainName, err)
	}

	balance := decodeUint256(result)
	return fromOnChainAmount(balance, tokenDecimals(token)), nil
}

// EstimateGas estimates the gas fee in ETH for the given transaction.
func (c *Client) EstimateGas(ctx context.Context, req domain.TxRequest) (decimal.Decimal, error) {
	fromAddr, err := normalizeAddress(req.From)
	if err != nil {
		return decimal.Zero, err
	}
	toAddr, err := normalizeAddress(req.To)
	if err != nil {
		return decimal.Zero, err
	}

	msg, err := c.buildCallMsg(fromAddr, toAddr, req.Token, req.Amount)
	if err != nil {
		return decimal.Zero, err
	}

	gasLimit, err := c.rpc.estimateGas(ctx, msg)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-ethereum: estimate gas on %s: %w", c.config.ChainName, err)
	}

	gasPrice, err := c.rpc.suggestGasPrice(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-ethereum: suggest gas price on %s: %w", c.config.ChainName, err)
	}

	// Add 20% buffer to the estimated gas limit
	gasLimitBuffered := gasLimit * 12 / 10
	feeWei := new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(gasLimitBuffered))
	return fromOnChainAmount(feeWei, 18), nil
}

// SendTransaction builds, signs, and broadcasts a transaction.
// For ERC20 tokens (USDC, USDT), it calls the contract's transfer function.
// For native ETH (token="" or "ETH"), it sends a plain value transfer.
func (c *Client) SendTransaction(ctx context.Context, req domain.TxRequest) (*domain.ChainTx, error) {
	fromAddr, err := normalizeAddress(req.From)
	if err != nil {
		return nil, err
	}
	toAddr, err := normalizeAddress(req.To)
	if err != nil {
		return nil, err
	}

	gasPrice, err := c.rpc.suggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-ethereum: suggest gas price on %s: %w", c.config.ChainName, err)
	}

	nonce, err := c.nonces.Next(ctx, fromAddr, func(ctx context.Context, addr common.Address) (uint64, error) {
		return c.rpc.pendingNonceAt(ctx, addr)
	})
	if err != nil {
		return nil, err
	}

	tx, err := c.buildTransaction(nonce, fromAddr, toAddr, req.Token, req.Amount, gasPrice)
	if err != nil {
		c.nonces.Reset(fromAddr)
		return nil, err
	}

	signedTx, err := c.signer.SignTx(ctx, fromAddr, tx, c.chainID)
	if err != nil {
		c.nonces.Reset(fromAddr)
		return nil, fmt.Errorf("settla-ethereum: sign tx on %s: %w", c.config.ChainName, err)
	}

	if err := c.rpc.sendTransaction(ctx, signedTx); err != nil {
		c.nonces.Reset(fromAddr)
		return nil, fmt.Errorf("settla-ethereum: broadcast tx on %s: %w", c.config.ChainName, err)
	}

	txHash := signedTx.Hash().Hex()

	c.logger.Info("settla-ethereum: transaction sent",
		"chain", c.config.ChainName,
		"hash", txHash,
		"from", req.From,
		"to", req.To,
		"token", req.Token,
		"amount", req.Amount.String(),
	)

	feeWei := new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(signedTx.Gas()))
	return &domain.ChainTx{
		Hash:   txHash,
		Status: TxStatusPending,
		Fee:    fromOnChainAmount(feeWei, 18),
	}, nil
}

// GetTransaction retrieves a transaction's current status by hash.
func (c *Client) GetTransaction(ctx context.Context, hash string) (*domain.ChainTx, error) {
	txHash := common.HexToHash(hash)

	receipt, err := c.rpc.transactionReceipt(ctx, txHash)
	if err != nil {
		if !errors.Is(err, geth.NotFound) {
			return nil, fmt.Errorf("settla-ethereum: get tx %s on %s: %w", hash, c.config.ChainName, err)
		}
		// Receipt not found — check if the tx is pending (in mempool)
		_, isPending, err2 := c.rpc.transactionByHash(ctx, txHash)
		if errors.Is(err2, geth.NotFound) {
			// No receipt and no tx — completely unknown
			return &domain.ChainTx{Hash: hash, Status: TxStatusUnknown}, nil
		}
		if err2 != nil {
			return nil, fmt.Errorf("settla-ethereum: get tx %s on %s: %w", hash, c.config.ChainName, err2)
		}
		if isPending {
			return &domain.ChainTx{Hash: hash, Status: TxStatusPending}, nil
		}
		return &domain.ChainTx{Hash: hash, Status: TxStatusUnknown}, nil
	}

	currentBlock, err := c.rpc.blockNumber(ctx)
	if err != nil {
		currentBlock = receipt.BlockNumber.Uint64()
	}

	confirmations := uint64(0)
	if currentBlock >= receipt.BlockNumber.Uint64() {
		confirmations = currentBlock - receipt.BlockNumber.Uint64()
	}

	status := TxStatusConfirmed
	if receipt.Status == 0 {
		status = TxStatusFailed
	}

	var fee decimal.Decimal
	if receipt.EffectiveGasPrice != nil {
		feeWei := new(big.Int).Mul(receipt.EffectiveGasPrice, new(big.Int).SetUint64(receipt.GasUsed))
		fee = fromOnChainAmount(feeWei, 18)
	}

	return &domain.ChainTx{
		Hash:          hash,
		Status:        status,
		Confirmations: int(confirmations),
		BlockNumber:   receipt.BlockNumber.Uint64(),
		Fee:           fee,
	}, nil
}

// SubscribeTransactions polls for incoming ERC20 Transfer events to address.
// It starts a goroutine that runs until ctx is cancelled.
// New confirmed transactions are sent to ch.
func (c *Client) SubscribeTransactions(ctx context.Context, address string, ch chan<- domain.ChainTx) error {
	addr, err := normalizeAddress(address)
	if err != nil {
		return err
	}

	pollInterval := c.config.PollInterval
	if pollInterval == 0 {
		pollInterval = 15 * time.Second
	}

	startBlock, err := c.rpc.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("settla-ethereum: get block number on %s: %w", c.config.ChainName, err)
	}

	go func() {
		lastBlock := startBlock
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				currentBlock, err := c.rpc.blockNumber(ctx)
				if err != nil {
					c.logger.Warn("settla-ethereum: block number poll failed",
						"chain", c.config.ChainName, "error", err)
					continue
				}
				if currentBlock <= lastBlock {
					continue
				}

				txs, err := c.getIncomingTransfers(ctx, addr, lastBlock+1, currentBlock)
				if err != nil {
					c.logger.Warn("settla-ethereum: get transfers failed",
						"chain", c.config.ChainName,
						"address", address,
						"from_block", lastBlock+1,
						"to_block", currentBlock,
						"error", err)
					continue
				}

				for _, tx := range txs {
					select {
					case ch <- tx:
					case <-ctx.Done():
						return
					}
				}

				lastBlock = currentBlock
			}
		}
	}()

	return nil
}

// buildCallMsg constructs an geth.CallMsg for eth_estimateGas / eth_call.
func (c *Client) buildCallMsg(from, to common.Address, token string, amount decimal.Decimal) (geth.CallMsg, error) {
	if token == "" || strings.EqualFold(token, "ETH") {
		amountWei := toOnChainAmount(amount, 18)
		return geth.CallMsg{From: from, To: &to, Value: amountWei}, nil
	}

	contractStr, err := c.resolveContract(token)
	if err != nil {
		return geth.CallMsg{}, err
	}
	contract, err := normalizeAddress(contractStr)
	if err != nil {
		return geth.CallMsg{}, err
	}

	amountOnChain := toOnChainAmount(amount, tokenDecimals(token))
	callData := encodeERC20Transfer(to, amountOnChain)
	return geth.CallMsg{From: from, To: &contract, Data: callData}, nil
}

// buildTransaction constructs an unsigned legacy transaction.
func (c *Client) buildTransaction(nonce uint64, from, to common.Address, token string, amount decimal.Decimal, gasPrice *big.Int) (*types.Transaction, error) {
	if token == "" || strings.EqualFold(token, "ETH") {
		amountWei := toOnChainAmount(amount, 18)
		//nolint:staticcheck // legacy transaction intentional for broad testnet compat
		return types.NewTransaction(nonce, to, amountWei, 21_000, gasPrice, nil), nil
	}

	contractStr, err := c.resolveContract(token)
	if err != nil {
		return nil, err
	}
	contract, err := normalizeAddress(contractStr)
	if err != nil {
		return nil, err
	}

	amountOnChain := toOnChainAmount(amount, tokenDecimals(token))
	callData := encodeERC20Transfer(to, amountOnChain)

	gasLimit := c.config.GasLimit
	if gasLimit == 0 {
		gasLimit = 100_000
	}

	//nolint:staticcheck // legacy transaction intentional for broad testnet compat
	return types.NewTransaction(nonce, contract, big.NewInt(0), gasLimit, gasPrice, callData), nil
}

// resolveContract returns the contract address for the given token.
func (c *Client) resolveContract(token string) (string, error) {
	addr, err := c.config.ContractAddress(token)
	if err != nil {
		// Fall through to raw 0x address
		if strings.HasPrefix(token, "0x") || strings.HasPrefix(token, "0X") {
			return token, nil
		}
		return "", err
	}
	return addr, nil
}

// getIncomingTransfers returns confirmed ERC20 Transfer events to addr in [fromBlock, toBlock].
func (c *Client) getIncomingTransfers(ctx context.Context, addr common.Address, fromBlock, toBlock uint64) ([]domain.ChainTx, error) {
	// ERC20 Transfer event topic: keccak256("Transfer(address,address,uint256)")
	transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	// Pad address to topic (32 bytes)
	addrTopic := common.BytesToHash(addr.Bytes())

	query := geth.FilterQuery{
		FromBlock: new(big.Int).SetUint64(fromBlock),
		ToBlock:   new(big.Int).SetUint64(toBlock),
		Topics: [][]common.Hash{
			{transferTopic}, // Transfer event signature
			nil,             // from: any
			{addrTopic},     // to: our address
		},
	}

	logs, err := c.rpc.filterLogs(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("settla-ethereum: filter logs on %s: %w", c.config.ChainName, err)
	}

	result := make([]domain.ChainTx, 0, len(logs))
	for _, log := range logs {
		if log.Removed {
			continue
		}
		result = append(result, domain.ChainTx{
			Hash:        log.TxHash.Hex(),
			Status:      TxStatusConfirmed,
			BlockNumber: log.BlockNumber,
		})
	}
	return result, nil
}
