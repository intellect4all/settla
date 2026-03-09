package solana

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// rpcClient wraps the gagliardetto rpc.Client with helpers for common operations.
type rpcClient struct {
	inner      *rpc.Client
	commitment rpc.CommitmentType
	logger     *slog.Logger
}

func newRPCClient(rpcURL string, commitment rpc.CommitmentType, logger *slog.Logger) *rpcClient {
	if commitment == "" {
		commitment = CommitmentConfirmed
	}
	return &rpcClient{
		inner:      rpc.New(rpcURL),
		commitment: commitment,
		logger:     logger,
	}
}

// getSOLBalance returns the native SOL balance for an address (in lamports).
func (r *rpcClient) getSOLBalance(ctx context.Context, owner solana.PublicKey) (uint64, error) {
	result, err := r.inner.GetBalance(ctx, owner, r.commitment)
	if err != nil {
		return 0, fmt.Errorf("settla-solana: getBalance RPC failed: %w", err)
	}
	return result.Value, nil
}

// getTokenAccountBalance returns the token balance for a specific token account.
// Returns (uiAmountString, decimals, error). Returns ("0", 0, nil) if account not found.
func (r *rpcClient) getTokenAccountBalance(ctx context.Context, tokenAccount solana.PublicKey) (string, uint8, error) {
	result, err := r.inner.GetTokenAccountBalance(ctx, tokenAccount, r.commitment)
	if err != nil {
		// Token account may not exist yet — treat as zero balance.
		r.logger.Debug("settla-solana: token account not found (zero balance)",
			"account", tokenAccount,
		)
		return "0", 0, nil
	}
	if result == nil || result.Value == nil {
		return "0", 0, nil
	}
	amount := result.Value.UiAmountString
	if amount == "" {
		amount = "0"
	}
	return amount, result.Value.Decimals, nil
}

// getTokenDecimals queries the mint to obtain its decimal places.
func (r *rpcClient) getTokenDecimals(ctx context.Context, mint solana.PublicKey) (uint8, error) {
	result, err := r.inner.GetTokenSupply(ctx, mint, r.commitment)
	if err != nil {
		return 0, fmt.Errorf("settla-solana: getTokenSupply failed: %w", err)
	}
	if result.Value == nil {
		return 0, fmt.Errorf("settla-solana: mint supply result is nil")
	}
	return result.Value.Decimals, nil
}

// accountExists returns true if the account at pubkey has been initialised on-chain.
// Returns (false, nil) when the account does not exist (account not found).
func (r *rpcClient) accountExists(ctx context.Context, pubkey solana.PublicKey) (bool, error) {
	_, err := r.inner.GetAccountInfoWithOpts(ctx, pubkey, &rpc.GetAccountInfoOpts{
		Commitment: r.commitment,
	})
	if err != nil {
		// ErrNotFound means the account (e.g. an ATA) simply doesn't exist yet.
		if errors.Is(err, rpc.ErrNotFound) {
			return false, nil
		}
		// Treat "expected a value, got null result" the same way.
		return false, fmt.Errorf("settla-solana: getAccountInfo failed: %w", err)
	}
	return true, nil
}

// getLatestBlockhash returns the latest blockhash.
func (r *rpcClient) getLatestBlockhash(ctx context.Context) (solana.Hash, error) {
	result, err := r.inner.GetLatestBlockhash(ctx, r.commitment)
	if err != nil {
		return solana.Hash{}, fmt.Errorf("settla-solana: getLatestBlockhash failed: %w", err)
	}
	return result.Value.Blockhash, nil
}

// sendTransaction submits a signed transaction to the network.
func (r *rpcClient) sendTransaction(ctx context.Context, tx *solana.Transaction) (solana.Signature, error) {
	sig, err := r.inner.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: r.commitment,
	})
	if err != nil {
		return solana.Signature{}, fmt.Errorf("settla-solana: sendTransaction failed: %w", err)
	}
	return sig, nil
}

// getTransaction retrieves a transaction by signature.
// Returns (nil, nil) if the transaction is not yet available.
func (r *rpcClient) getTransaction(ctx context.Context, sig solana.Signature) (*rpc.GetTransactionResult, error) {
	maxVersion := uint64(0)
	result, err := r.inner.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
		Commitment:                     r.commitment,
		MaxSupportedTransactionVersion: &maxVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-solana: getTransaction failed: %w", err)
	}
	return result, nil
}

// getSignaturesForAddress returns recent transaction signatures for an address.
func (r *rpcClient) getSignaturesForAddress(ctx context.Context, address solana.PublicKey, limit int) ([]*rpc.TransactionSignature, error) {
	opts := &rpc.GetSignaturesForAddressOpts{
		Limit:      &limit,
		Commitment: r.commitment,
	}
	sigs, err := r.inner.GetSignaturesForAddressWithOpts(ctx, address, opts)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: getSignaturesForAddress failed: %w", err)
	}
	return sigs, nil
}
