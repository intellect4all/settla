// Package solana implements the domain.BlockchainClient interface for the Solana blockchain.
// It targets Solana Devnet for testnet operations with SPL token (USDC) transfers.
package solana

import (
	"log/slog"
	"time"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/intellect4all/settla/domain"
)

// Well-known program addresses.
const (
	// TokenProgramID is the SPL Token program.
	TokenProgramID = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"

	// AssociatedTokenProgramID is the Associated Token Account program.
	AssociatedTokenProgramID = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"

	// SystemProgramID is the native system program.
	SystemProgramID = "11111111111111111111111111111111"

	// SysvarRentPubkey is the rent sysvar.
	SysvarRentPubkey = "SysvarRent111111111111111111111111111111111"
)

// chainIdentifier is the chain() return value.
const chainIdentifier = domain.ChainSolana

// Solana-specific constants.
const (
	// lamportsPerSOL is the number of lamports in one SOL.
	lamportsPerSOL = 1_000_000_000

	// baseFeePerSignature is the Solana base fee per signature in lamports.
	baseFeePerSignature = 5_000

	// ataRentExemptLamports is the rent-exempt minimum for a token account (~0.002 SOL).
	ataRentExemptLamports = 2_039_280

	// pollInterval is how often SubscribeTransactions polls for new transactions.
	pollInterval = 2 * time.Second
)

// Commitment levels for Solana RPC calls.
const (
	CommitmentProcessed = rpc.CommitmentProcessed
	CommitmentConfirmed = rpc.CommitmentConfirmed
	CommitmentFinalized = rpc.CommitmentFinalized
)

// Config holds Solana client configuration.
type Config struct {
	// RPCURL is the Solana JSON-RPC endpoint.
	// Default for Devnet: https://api.devnet.solana.com
	RPCURL string

	// ExplorerURL is the base Solana Explorer URL (without trailing slash).
	// Default for Devnet: https://explorer.solana.com
	ExplorerURL string

	// USDCMint is the USDC SPL token mint address for this network.
	// Devnet USDC: 4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU
	USDCMint string

	// BlockTime is the average slot time.
	// Devnet: ~400ms
	BlockTime time.Duration

	// Confirmations is the number of slots required for finality.
	// Devnet: 32
	Confirmations int

	// Commitment is the default commitment level for RPC calls.
	// Default: CommitmentConfirmed
	Commitment rpc.CommitmentType

	// Logger for structured output. Defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// DevnetConfig returns the default Solana Devnet configuration.
func DevnetConfig() Config {
	return Config{
		RPCURL:        "https://api.devnet.solana.com",
		ExplorerURL:   "https://explorer.solana.com",
		USDCMint:      "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
		BlockTime:     400 * time.Millisecond,
		Confirmations: 32,
		Commitment:    CommitmentConfirmed,
	}
}
