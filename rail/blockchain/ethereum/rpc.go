package ethereum

import (
	"context"
	"fmt"
	"math/big"
	"time"

	geth "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

const defaultRPCTimeout = 30 * time.Second

// rpcClient wraps go-ethereum's ethclient with a fixed timeout on every call.
type rpcClient struct {
	inner *ethclient.Client
}

func newRPCClient(rpcURL string) (*rpcClient, error) {
	c, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("settla-ethereum: dial %s: %w", rpcURL, err)
	}
	return &rpcClient{inner: c}, nil
}

func (r *rpcClient) close() {
	r.inner.Close()
}

func (r *rpcClient) blockNumber(ctx context.Context) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.BlockNumber(ctx)
}

func (r *rpcClient) ethBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.BalanceAt(ctx, addr, nil)
}

func (r *rpcClient) callContract(ctx context.Context, msg geth.CallMsg) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.CallContract(ctx, msg, nil)
}

func (r *rpcClient) estimateGas(ctx context.Context, msg geth.CallMsg) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.EstimateGas(ctx, msg)
}

func (r *rpcClient) suggestGasPrice(ctx context.Context) (*big.Int, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.SuggestGasPrice(ctx)
}

func (r *rpcClient) pendingNonceAt(ctx context.Context, addr common.Address) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.PendingNonceAt(ctx, addr)
}

func (r *rpcClient) sendTransaction(ctx context.Context, tx *types.Transaction) error {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.SendTransaction(ctx, tx)
}

func (r *rpcClient) transactionReceipt(ctx context.Context, hash common.Hash) (*types.Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.TransactionReceipt(ctx, hash)
}

func (r *rpcClient) transactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.TransactionByHash(ctx, hash)
}

func (r *rpcClient) filterLogs(ctx context.Context, query geth.FilterQuery) ([]types.Log, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	return r.inner.FilterLogs(ctx, query)
}
