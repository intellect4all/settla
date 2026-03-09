// Package tron implements the Tron Nile testnet blockchain client for Settla Rail.
// It provides real on-chain TRC20 token transfers for testnet settlement operations.
package tron

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// base58Alphabet is the Base58 character set used by Tron (same as Bitcoin).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// sunPerTRX is the number of SUN per TRX (1 TRX = 1,000,000 SUN).
const sunPerTRX = int64(1_000_000)

// ValidateAddress checks that a Tron address is well-formed Base58Check.
// Valid Tron addresses are 34 characters, start with 'T', and pass checksum.
func ValidateAddress(address string) error {
	if len(address) == 0 {
		return errors.New("empty address")
	}
	if len(address) != 34 {
		return fmt.Errorf("tron address must be 34 characters, got %d", len(address))
	}
	if address[0] != 'T' {
		return errors.New("tron address must start with 'T'")
	}

	decoded, err := base58Decode(address)
	if err != nil {
		return fmt.Errorf("base58 decode: %w", err)
	}
	if len(decoded) != 25 {
		return fmt.Errorf("decoded address must be 25 bytes, got %d", len(decoded))
	}
	if decoded[0] != 0x41 {
		return fmt.Errorf("invalid version byte: expected 0x41, got 0x%02x", decoded[0])
	}

	// Constant-time checksum verification
	h1 := sha256.Sum256(decoded[:21])
	h2 := sha256.Sum256(h1[:])
	if subtle.ConstantTimeCompare(decoded[21:25], h2[:4]) != 1 {
		return errors.New("checksum mismatch")
	}
	return nil
}

// IsValidAddress reports whether addr is a valid Tron Base58Check address.
func IsValidAddress(addr string) bool {
	return ValidateAddress(addr) == nil
}

// AddressToHex converts a Base58Check Tron address (T-prefix) to 21-byte hex (41-prefix).
// The hex format is used in TronGrid API calls.
func AddressToHex(addr string) (string, error) {
	if err := ValidateAddress(addr); err != nil {
		return "", fmt.Errorf("invalid tron address %q: %w", addr, err)
	}
	decoded, err := base58Decode(addr)
	if err != nil {
		return "", err
	}
	// decoded is 25 bytes: 21-byte payload + 4-byte checksum
	return hex.EncodeToString(decoded[:21]), nil
}

// AddressFromHex converts a 21-byte hex Tron address (41-prefix) to Base58Check format.
func AddressFromHex(hexAddr string) (string, error) {
	hexAddr = strings.TrimPrefix(hexAddr, "0x")
	payload, err := hex.DecodeString(hexAddr)
	if err != nil {
		return "", fmt.Errorf("invalid hex: %w", err)
	}
	if len(payload) != 21 {
		return "", fmt.Errorf("tron hex address must be 21 bytes, got %d", len(payload))
	}
	if payload[0] != 0x41 {
		return "", fmt.Errorf("invalid prefix byte: expected 0x41, got 0x%02x", payload[0])
	}
	return base58CheckEncode(payload), nil
}

// EncodeTRC20Transfer ABI-encodes parameters for the transfer(address,uint256) function.
// Returns the hex parameter string for use in triggersmartcontract.
// toAddress must be a valid Base58Check Tron address.
// amount is the token amount in base units (e.g., 1000000 for 1 USDT with 6 decimals).
func EncodeTRC20Transfer(toAddress string, amount *big.Int) (string, error) {
	hexAddr, err := AddressToHex(toAddress)
	if err != nil {
		return "", fmt.Errorf("encoding recipient: %w", err)
	}

	addrBytes, err := hex.DecodeString(hexAddr)
	if err != nil {
		return "", err
	}

	// ABI encoding for address: 12 zero-padding bytes + 20-byte EVM address (skip 0x41 prefix)
	paddedAddr := make([]byte, 32)
	copy(paddedAddr[12:], addrBytes[1:]) // addrBytes[1:] is the 20-byte Ethereum-style part

	// ABI encoding for uint256: big-endian, left-padded to 32 bytes
	paddedAmount := make([]byte, 32)
	amountBytes := amount.Bytes()
	if len(amountBytes) > 32 {
		return "", errors.New("amount exceeds uint256 max")
	}
	copy(paddedAmount[32-len(amountBytes):], amountBytes)

	return hex.EncodeToString(append(paddedAddr, paddedAmount...)), nil
}

// TransactionHash computes the SHA-256 hash of the raw transaction hex.
// This is the digest that must be signed for Tron transactions.
func TransactionHash(rawDataHex string) ([]byte, error) {
	raw, err := hex.DecodeString(rawDataHex)
	if err != nil {
		return nil, fmt.Errorf("decoding raw transaction hex: %w", err)
	}
	hash := sha256.Sum256(raw)
	return hash[:], nil
}

// ExplorerURL returns the block explorer URL for a given transaction hash.
func ExplorerURL(explorerBase, txHash string) string {
	return explorerBase + "/#/transaction/" + txHash
}

// base58CheckEncode encodes a payload with SHA256 double-hash checksum.
func base58CheckEncode(payload []byte) string {
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	full := make([]byte, len(payload)+4)
	copy(full, payload)
	copy(full[len(payload):], h2[:4])
	return base58Encode(full)
}

// base58Encode encodes bytes to a Base58 string.
func base58Encode(input []byte) string {
	if len(input) == 0 {
		return ""
	}

	leadingZeros := 0
	for _, b := range input {
		if b == 0 {
			leadingZeros++
		} else {
			break
		}
	}

	result := make([]byte, len(input)*2)
	resultLen := 0

	for _, b := range input {
		carry := int(b)
		for i := 0; i < resultLen; i++ {
			carry += int(result[i]) << 8
			result[i] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			result[resultLen] = byte(carry % 58)
			resultLen++
			carry /= 58
		}
	}

	for i := 0; i < leadingZeros; i++ {
		result[resultLen] = 0
		resultLen++
	}

	encoded := make([]byte, resultLen)
	for i := 0; i < resultLen; i++ {
		encoded[i] = base58Alphabet[result[resultLen-1-i]]
	}
	return string(encoded)
}

// base58Decode decodes a Base58 string to bytes.
func base58Decode(input string) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("empty input")
	}

	leadingZeros := 0
	for _, c := range input {
		if c == '1' {
			leadingZeros++
		} else {
			break
		}
	}

	result := make([]byte, len(input))
	resultLen := 0

	for _, c := range input {
		carry := strings.IndexRune(base58Alphabet, c)
		if carry < 0 {
			return nil, fmt.Errorf("invalid Base58 character: %c", c)
		}
		for i := 0; i < resultLen; i++ {
			carry += int(result[i]) * 58
			result[i] = byte(carry & 0xff)
			carry >>= 8
		}
		for carry > 0 {
			result[resultLen] = byte(carry & 0xff)
			resultLen++
			carry >>= 8
		}
	}

	for i := 0; i < leadingZeros; i++ {
		result[resultLen] = 0
		resultLen++
	}

	decoded := make([]byte, resultLen)
	for i := 0; i < resultLen; i++ {
		decoded[i] = result[resultLen-1-i]
	}
	return decoded, nil
}
