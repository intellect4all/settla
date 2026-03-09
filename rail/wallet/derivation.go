package wallet

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/ethereum/go-ethereum/crypto"
	solana "github.com/gagliardetto/solana-go"
	"github.com/tyler-smith/go-bip32"

	"github.com/intellect4all/settla/rail/wallet/keymgmt"
)

// DeriveWallet derives a wallet for the specified chain at the given index.
// Uses BIP-44 paths:
// - Tron: m/44'/195'/0'/0/{index}
// - Solana: m/44'/501'/0'/0/{index}
// - Ethereum/Base: m/44'/60'/0'/0/{index}
//
// The returned wallet contains the private key in memory.
// Call wallet.ZeroPrivateKey() when done.
func DeriveWallet(km keymgmt.KeyManager, keyID string, chain Chain, index uint32) (*Wallet, error) {
	coinType := chain.CoinType()
	if coinType == 0 && chain != ChainEthereum && chain != ChainBase {
		return nil, fmt.Errorf("settla-wallet: unsupported chain: %s", chain)
	}

	// Derive BIP-32 key at path m/44'/coinType'/0'/0/index
	derivedKey, err := km.DerivePath(keyID, coinType, 0, 0, index)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to derive key: %w", err)
	}
	defer SecureZeroBIP32Key(derivedKey)

	switch chain {
	case ChainEthereum, ChainBase:
		return deriveEthereumWallet(derivedKey, chain, index)
	case ChainTron:
		return deriveTronWallet(derivedKey, index)
	case ChainSolana:
		return deriveSolanaWallet(derivedKey, index)
	default:
		return nil, fmt.Errorf("settla-wallet: unsupported chain: %s", chain)
	}
}

// deriveEthereumWallet creates an Ethereum/Base wallet from a derived BIP-32 key.
func deriveEthereumWallet(key *bip32.Key, chain Chain, index uint32) (*Wallet, error) {
	// Convert to ECDSA private key (secp256k1)
	privateKey, err := crypto.ToECDSA(key.Key)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to convert to ECDSA: %w", err)
	}

	// Get public key
	publicKeyECDSA, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		SecureZeroECDSA(privateKey)
		return nil, fmt.Errorf("settla-wallet: failed to get public key")
	}

	// Generate address
	address := crypto.PubkeyToAddress(*publicKeyECDSA).Hex()
	publicKeyBytes := crypto.FromECDSAPub(publicKeyECDSA)

	wallet := &Wallet{
		Chain:           chain,
		Address:         address,
		PublicKey:       publicKeyBytes,
		DerivationIndex: index,
	}
	wallet.setPrivateKey(privateKey)

	return wallet, nil
}

// deriveTronWallet creates a Tron wallet from a derived BIP-32 key.
// Tron uses the same secp256k1 curve as Ethereum but with Base58Check encoding.
func deriveTronWallet(key *bip32.Key, index uint32) (*Wallet, error) {
	// Convert to ECDSA private key (secp256k1, same as Ethereum)
	privateKey, err := crypto.ToECDSA(key.Key)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to convert to ECDSA: %w", err)
	}

	publicKeyECDSA, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		SecureZeroECDSA(privateKey)
		return nil, fmt.Errorf("settla-wallet: failed to get public key")
	}

	// Generate Tron address (keccak256 → last 20 bytes → 0x41 prefix → Base58Check)
	address := publicKeyToTronAddress(publicKeyECDSA)
	publicKeyBytes := crypto.FromECDSAPub(publicKeyECDSA)

	wallet := &Wallet{
		Chain:           ChainTron,
		Address:         address,
		PublicKey:       publicKeyBytes,
		DerivationIndex: index,
	}
	wallet.setPrivateKey(privateKey)

	return wallet, nil
}

// deriveSolanaWallet creates a Solana wallet from a derived BIP-32 key.
// Solana uses Ed25519 keys.
func deriveSolanaWallet(key *bip32.Key, index uint32) (*Wallet, error) {
	// Solana uses Ed25519 - use first 32 bytes of derived key as seed
	if len(key.Key) < 32 {
		return nil, fmt.Errorf("settla-wallet: derived key too short for Ed25519")
	}

	seed := make([]byte, 32)
	copy(seed, key.Key[:32])

	privateKey := ed25519.NewKeyFromSeed(seed)
	SecureClearBytes(seed) // Clear the seed copy

	publicKeyBytes := privateKey.Public().(ed25519.PublicKey)

	// Convert to Solana public key for address
	solanaPubKey := solana.PublicKeyFromBytes(publicKeyBytes)
	address := solanaPubKey.String()

	wallet := &Wallet{
		Chain:           ChainSolana,
		Address:         address,
		PublicKey:       publicKeyBytes,
		DerivationIndex: index,
	}
	wallet.setPrivateKey(privateKey)

	return wallet, nil
}

// publicKeyToTronAddress converts an ECDSA public key to a Tron address.
// Process: keccak256(pubKey[1:]) → take last 20 bytes → prepend 0x41 → Base58Check encode
func publicKeyToTronAddress(pub *ecdsa.PublicKey) string {
	// Get the Ethereum-style address bytes (keccak256 of uncompressed pubkey, last 20 bytes)
	addrBytes := crypto.PubkeyToAddress(*pub).Bytes()

	// Prepend Tron's version byte (0x41) and Base58Check encode
	payload := make([]byte, 21)
	payload[0] = 0x41
	copy(payload[1:], addrBytes)

	return base58CheckEncode(payload)
}

// base58CheckEncode encodes data with Base58Check (data includes version byte).
func base58CheckEncode(payload []byte) string {
	// Double SHA-256 for checksum
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	checksum := second[:4]

	// Append checksum to payload
	full := make([]byte, len(payload)+4)
	copy(full, payload)
	copy(full[len(payload):], checksum)

	return base58.Encode(full)
}

// ValidateTronAddress checks if a Tron address is valid.
// Valid Tron addresses start with 'T' and are 34 characters in Base58Check.
func ValidateTronAddress(address string) bool {
	if len(address) != 34 || address[0] != 'T' {
		return false
	}

	decoded, version, err := base58CheckDecode(address)
	if err != nil {
		return false
	}

	// Tron addresses use version byte 0x41
	return version == 0x41 && len(decoded) == 20
}

// base58CheckDecode decodes a Base58Check encoded string.
// Returns (payload without version, version, error).
func base58CheckDecode(address string) ([]byte, byte, error) {
	decoded := base58.Decode(address)
	if len(decoded) < 5 {
		return nil, 0, fmt.Errorf("decoded data too short")
	}

	// Split into payload and checksum
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	// Verify checksum
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	expectedChecksum := second[:4]

	for i := 0; i < 4; i++ {
		if checksum[i] != expectedChecksum[i] {
			return nil, 0, fmt.Errorf("checksum mismatch")
		}
	}

	version := payload[0]
	return payload[1:], version, nil
}

// TronAddressFromHex converts a hex address (with 41 prefix) to Base58Check.
func TronAddressFromHex(hexAddr string) (string, error) {
	payload, err := hex.DecodeString(hexAddr)
	if err != nil {
		return "", fmt.Errorf("invalid hex: %w", err)
	}
	if len(payload) != 21 || payload[0] != 0x41 {
		return "", fmt.Errorf("invalid Tron hex address: must be 21 bytes with 0x41 prefix")
	}
	return base58CheckEncode(payload), nil
}

// TronAddressToHex converts a Base58Check Tron address to hex (with 41 prefix).
func TronAddressToHex(address string) (string, error) {
	decoded, version, err := base58CheckDecode(address)
	if err != nil {
		return "", err
	}
	if version != 0x41 {
		return "", fmt.Errorf("invalid version byte: expected 0x41, got 0x%02x", version)
	}
	payload := make([]byte, 21)
	payload[0] = 0x41
	copy(payload[1:], decoded)
	return hex.EncodeToString(payload), nil
}

// ValidateSolanaAddress checks if a Solana address is valid.
func ValidateSolanaAddress(address string) bool {
	_, err := solana.PublicKeyFromBase58(address)
	return err == nil
}

// ValidateEthereumAddress checks if an Ethereum address is valid.
func ValidateEthereumAddress(address string) bool {
	if len(address) != 42 || address[:2] != "0x" {
		return false
	}
	_, err := hex.DecodeString(address[2:])
	return err == nil
}

// ValidateAddress checks if an address is valid for the given chain.
func ValidateAddress(chain Chain, address string) bool {
	switch chain {
	case ChainTron:
		return ValidateTronAddress(address)
	case ChainSolana:
		return ValidateSolanaAddress(address)
	case ChainEthereum, ChainBase:
		return ValidateEthereumAddress(address)
	default:
		return false
	}
}
