package solana

import (
	"context"
	"fmt"
	"math/big"

	solana "github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/shopspring/decimal"
)

// splTransferResult holds the outcome of an SPL transfer build.
type splTransferResult struct {
	tx            *solana.Transaction
	createdATA    bool   // true if a CreateAssociatedTokenAccount ix was prepended
	recipientATA  solana.PublicKey
}

// buildSPLTransfer constructs a signed SPL token transfer transaction.
//
// Steps:
//  1. Derive sender and recipient Associated Token Accounts (ATAs).
//  2. If the recipient's ATA does not exist, prepend a CreateAssociatedTokenAccount instruction.
//  3. Convert the decimal amount to raw token units using the mint's decimals.
//  4. Append a TransferChecked instruction.
//  5. Fetch the latest blockhash and build the transaction.
//  6. Sign with the provided Ed25519 private key (converted to solana.PrivateKey).
func buildSPLTransfer(
	ctx context.Context,
	rpc *rpcClient,
	sender, recipient, mint solana.PublicKey,
	amount decimal.Decimal,
	memo string,
	privateKey []byte, // ed25519.PrivateKey (64 bytes)
) (*splTransferResult, error) {

	// 1. Derive ATAs.
	senderATA, _, err := solana.FindAssociatedTokenAddress(sender, mint)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: derive sender ATA: %w", err)
	}

	recipientATA, _, err := solana.FindAssociatedTokenAddress(recipient, mint)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: derive recipient ATA: %w", err)
	}

	// 2. Check recipient ATA existence.
	recipientATAExists, err := rpc.accountExists(ctx, recipientATA)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: check recipient ATA: %w", err)
	}

	// 3. Get mint decimals.
	decimals, err := rpc.getTokenDecimals(ctx, mint)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: get token decimals: %w", err)
	}

	// Convert decimal amount to raw token units.
	rawAmount, err := decimalToRawAmount(amount, decimals)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: convert amount: %w", err)
	}
	if rawAmount == 0 {
		return nil, fmt.Errorf("settla-solana: amount rounds to zero raw units")
	}

	// 4. Build instructions.
	var instructions []solana.Instruction
	createdATA := false

	if !recipientATAExists {
		createATAIx, err := buildCreateATAInstruction(sender, recipient, mint)
		if err != nil {
			return nil, fmt.Errorf("settla-solana: build CreateATA instruction: %w", err)
		}
		instructions = append(instructions, createATAIx)
		createdATA = true
	}

	transferIx := token.NewTransferCheckedInstruction(
		rawAmount,
		decimals,
		senderATA,
		mint,
		recipientATA,
		sender,
		[]solana.PublicKey{},
	).Build()
	instructions = append(instructions, transferIx)

	// 5. Fetch latest blockhash.
	blockhash, err := rpc.getLatestBlockhash(ctx)
	if err != nil {
		return nil, err
	}

	// 6. Build transaction.
	tx, err := solana.NewTransaction(
		instructions,
		blockhash,
		solana.TransactionPayer(sender),
	)
	if err != nil {
		return nil, fmt.Errorf("settla-solana: build transaction: %w", err)
	}

	// 7. Sign transaction.
	solanaKey := solana.PrivateKey(privateKey)
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(sender) {
			return &solanaKey
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("settla-solana: sign transaction: %w", err)
	}

	return &splTransferResult{
		tx:           tx,
		createdATA:   createdATA,
		recipientATA: recipientATA,
	}, nil
}

// buildCreateATAInstruction creates the CreateAssociatedTokenAccount instruction.
// payer funds the ATA creation; wallet is the new ATA owner; mint is the token mint.
func buildCreateATAInstruction(payer, wallet, mint solana.PublicKey) (solana.Instruction, error) {
	instruction, err := ata.NewCreateInstruction(
		payer,
		wallet,
		mint,
	).ValidateAndBuild()
	if err != nil {
		return nil, fmt.Errorf("settla-solana: validate CreateATA instruction: %w", err)
	}
	return instruction, nil
}

// decimalToRawAmount converts a human-readable decimal amount to raw token units.
// For example, 1.5 USDC with 6 decimals → 1_500_000.
func decimalToRawAmount(amount decimal.Decimal, decimals uint8) (uint64, error) {
	if amount.IsNegative() || amount.IsZero() {
		return 0, fmt.Errorf("amount must be positive")
	}

	// Multiply by 10^decimals
	multiplier := decimal.New(1, int32(decimals))
	raw := amount.Mul(multiplier)

	// Convert to *big.Int
	rawInt := raw.BigInt()
	if !rawInt.IsUint64() {
		return 0, fmt.Errorf("amount overflows uint64: %s", raw.String())
	}

	return rawInt.Uint64(), nil
}

// rawAmountToDecimal converts raw token units to a human-readable decimal.
// For example, 1_500_000 with 6 decimals → 1.5.
func rawAmountToDecimal(rawAmount *big.Int, decimals uint8) decimal.Decimal {
	d := decimal.NewFromBigInt(rawAmount, -int32(decimals))
	return d
}
