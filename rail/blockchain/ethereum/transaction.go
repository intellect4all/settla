package ethereum

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"
)

// ERC20 method selectors — first 4 bytes of keccak256(function signature).
var (
	// selectorTransfer = keccak256("transfer(address,uint256)")[:4]
	selectorTransfer = []byte{0xa9, 0x05, 0x9c, 0xbb}

	// selectorBalanceOf = keccak256("balanceOf(address)")[:4]
	selectorBalanceOf = []byte{0x70, 0xa0, 0x82, 0x31}
)

// encodeERC20Transfer encodes calldata for ERC20 transfer(address,uint256).
func encodeERC20Transfer(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[0:4], selectorTransfer)
	// Address: 12 zero bytes + 20 byte address
	copy(data[4+12:4+32], to.Bytes())
	// Amount: big-endian, right-aligned in 32 bytes
	amountBytes := amount.Bytes()
	copy(data[4+32+(32-len(amountBytes)):4+64], amountBytes)
	return data
}

// encodeERC20BalanceOf encodes calldata for ERC20 balanceOf(address).
func encodeERC20BalanceOf(owner common.Address) []byte {
	data := make([]byte, 4+32)
	copy(data[0:4], selectorBalanceOf)
	copy(data[4+12:4+32], owner.Bytes())
	return data
}

// decodeUint256 decodes the first 32-byte big-endian uint256 from a contract call result.
func decodeUint256(result []byte) *big.Int {
	if len(result) < 32 {
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(result[:32])
}

// toOnChainAmount converts a decimal amount to its on-chain integer representation.
// decimals is the token's decimal count (e.g., 6 for USDC, 18 for ETH).
func toOnChainAmount(amount decimal.Decimal, decimals int) *big.Int {
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	shifted := amount.Mul(decimal.NewFromBigInt(multiplier, 0))
	return shifted.BigInt()
}

// fromOnChainAmount converts an on-chain integer amount to a human-readable decimal.
func fromOnChainAmount(amount *big.Int, decimals int) decimal.Decimal {
	if amount == nil {
		return decimal.Zero
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return decimal.NewFromBigInt(amount, 0).Div(decimal.NewFromBigInt(divisor, 0))
}

// tokenDecimals returns the ERC20 decimal places for common tokens.
func tokenDecimals(token string) int {
	switch strings.ToUpper(token) {
	case "USDC", "USDT":
		return 6
	default:
		return 18
	}
}

// normalizeAddress parses and validates an Ethereum address string into common.Address.
func normalizeAddress(addr string) (common.Address, error) {
	if !strings.HasPrefix(addr, "0x") && !strings.HasPrefix(addr, "0X") {
		return common.Address{}, fmt.Errorf("settla-ethereum: invalid address %q: must start with 0x", addr)
	}
	addrHex := addr[2:]
	if len(addrHex) != 40 {
		return common.Address{}, fmt.Errorf("settla-ethereum: invalid address %q: must be 42 chars total", addr)
	}
	b, err := hex.DecodeString(addrHex)
	if err != nil {
		return common.Address{}, fmt.Errorf("settla-ethereum: invalid address %q: %w", addr, err)
	}
	return common.BytesToAddress(b), nil
}

// nonceEntry holds per-address nonce state with its own mutex.
type nonceEntry struct {
	mu     sync.Mutex
	nonce  uint64
	loaded bool
}

// nonceManager tracks per-address transaction nonces for sequential submission.
// Each address gets its own mutex so different addresses can submit concurrently.
type nonceManager struct {
	entries sync.Map // map[common.Address]*nonceEntry
}

func newNonceManager() *nonceManager {
	return &nonceManager{}
}

// Next returns the next nonce for addr, fetching from the network on first call.
// fetchFn is called at most once per address unless Reset is called.
// Per-address locking allows concurrent submissions from different addresses.
func (nm *nonceManager) Next(ctx context.Context, addr common.Address, fetchFn func(ctx context.Context, addr common.Address) (uint64, error)) (uint64, error) {
	v, _ := nm.entries.LoadOrStore(addr, &nonceEntry{})
	entry := v.(*nonceEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if !entry.loaded {
		nonce, err := fetchFn(ctx, addr)
		if err != nil {
			return 0, fmt.Errorf("settla-ethereum: fetch nonce for %s: %w", addr.Hex(), err)
		}
		entry.nonce = nonce
		entry.loaded = true
	}

	nonce := entry.nonce
	entry.nonce++
	return nonce, nil
}

// Reset invalidates the cached nonce for addr so the next call re-fetches from chain.
// Call this when a transaction fails to avoid nonce gaps.
func (nm *nonceManager) Reset(addr common.Address) {
	v, ok := nm.entries.Load(addr)
	if !ok {
		return
	}
	entry := v.(*nonceEntry)
	entry.mu.Lock()
	entry.loaded = false
	entry.mu.Unlock()
}
