package wallet

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"time"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
)

// Chain is an alias for domain.CryptoChain for backward compatibility within the wallet package.
type Chain = domain.CryptoChain

// Re-export chain constants from domain for backward compatibility.
const (
	ChainTron     = domain.ChainTron
	ChainSolana   = domain.ChainSolana
	ChainEthereum = domain.ChainEthereum
	ChainBase     = domain.ChainBase
)

// CoinType returns the BIP-44 coin type for the chain.
func CoinType(c Chain) uint32 {
	switch c {
	case ChainTron:
		return 195
	case ChainSolana:
		return 501
	case ChainEthereum, ChainBase:
		return 60 // Both EVM chains use Ethereum's coin type
	default:
		return 0
	}
}

// IsValidChain checks if a chain string is valid.
func IsValidChain(s string) bool {
	return domain.ValidateChain(domain.CryptoChain(s)) == nil
}

// WalletType distinguishes between system and tenant wallets.
type WalletType string

const (
	// WalletTypeSystem is a Settla-owned wallet (e.g., hot wallet for float).
	WalletTypeSystem WalletType = "system"
	// WalletTypeTenant is a tenant-owned wallet for deposits/withdrawals.
	WalletTypeTenant WalletType = "tenant"
)

// Wallet represents a blockchain wallet with its keypair.
// Private keys are kept in memory and never exported or logged.
type Wallet struct {
	// Path is the wallet identifier (e.g., "system/tron/hot" or "tenant/lemfi/tron").
	Path string

	// Chain is the blockchain this wallet operates on.
	Chain Chain

	// Address is the public blockchain address (format depends on chain).
	// - Tron: T-prefix Base58Check (e.g., "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8")
	// - Solana: Base58 (e.g., "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU")
	// - Ethereum/Base: 0x-prefixed hex (e.g., "0x71C7656EC7ab88b098defB751B7401B5f6d8976F")
	Address string

	// PublicKey is the raw public key bytes.
	PublicKey []byte

	// privateKey holds the private key in memory. Never exported.
	// Type depends on chain: *ecdsa.PrivateKey for EVM/Tron, ed25519.PrivateKey for Solana.
	privateKey interface{}

	// TenantID is set for tenant wallets, nil for system wallets.
	TenantID *uuid.UUID

	// TenantSlug is the tenant's slug (e.g., "lemfi") for path construction.
	TenantSlug string

	// Type indicates whether this is a system or tenant wallet.
	Type WalletType

	// DerivationIndex is the BIP-44 derivation index used for this wallet.
	DerivationIndex uint32

	// CreatedAt is when the wallet was created.
	CreatedAt time.Time
}

// ECDSAPrivateKey returns the ECDSA private key for EVM and Tron wallets.
// Returns nil if the wallet is not EVM/Tron or if the key is not set.
//
// IMPORTANT: The caller MUST call SecureZeroECDSA when done with the key.
func (w *Wallet) ECDSAPrivateKey() *ecdsa.PrivateKey {
	if key, ok := w.privateKey.(*ecdsa.PrivateKey); ok {
		return key
	}
	return nil
}

// Ed25519PrivateKey returns the Ed25519 private key for Solana wallets.
// Returns nil if the wallet is not Solana or if the key is not set.
//
// IMPORTANT: The caller MUST call SecureZeroEd25519 when done with the key.
func (w *Wallet) Ed25519PrivateKey() ed25519.PrivateKey {
	if key, ok := w.privateKey.(ed25519.PrivateKey); ok {
		return key
	}
	return nil
}

// HasPrivateKey returns true if the wallet has a private key loaded.
func (w *Wallet) HasPrivateKey() bool {
	return w.privateKey != nil
}

// ZeroPrivateKey securely zeros the private key in memory.
// Should be called when the wallet is no longer needed.
func (w *Wallet) ZeroPrivateKey() {
	if w.privateKey == nil {
		return
	}

	switch key := w.privateKey.(type) {
	case *ecdsa.PrivateKey:
		SecureZeroECDSA(key)
	case ed25519.PrivateKey:
		SecureZeroEd25519(key)
	}

	w.privateKey = nil
}

// setPrivateKey sets the private key (internal use only).
func (w *Wallet) setPrivateKey(key interface{}) {
	w.privateKey = key
}

// IsSystemWallet returns true if this is a system-owned wallet.
func (w *Wallet) IsSystemWallet() bool {
	return w.Type == WalletTypeSystem
}

// IsTenantWallet returns true if this is a tenant-owned wallet.
func (w *Wallet) IsTenantWallet() bool {
	return w.Type == WalletTypeTenant
}

// EncryptedWallet is the persisted form of a wallet with encrypted private key.
type EncryptedWallet struct {
	// Path is the wallet identifier.
	Path string `json:"path"`

	// Chain is the blockchain network.
	Chain string `json:"chain"`

	// Address is the public blockchain address.
	Address string `json:"address"`

	// PublicKey is hex-encoded public key.
	PublicKey string `json:"public_key"`

	// EncryptedKey is the base64-encoded encrypted private key.
	EncryptedKey string `json:"encrypted_key"`

	// TenantID is set for tenant wallets.
	TenantID *string `json:"tenant_id,omitempty"`

	// TenantSlug is the tenant's slug.
	TenantSlug string `json:"tenant_slug,omitempty"`

	// Type is "system" or "tenant".
	Type string `json:"type"`

	// DerivationIndex is the BIP-44 derivation index.
	DerivationIndex uint32 `json:"derivation_index"`

	// CreatedAt is the creation timestamp.
	CreatedAt string `json:"created_at"`
}

// WalletStore defines the interface for persisting encrypted wallets.
type WalletStore interface {
	// SaveWallet persists an encrypted wallet.
	SaveWallet(wallet *EncryptedWallet) error

	// GetWallet retrieves an encrypted wallet by path.
	GetWallet(path string) (*EncryptedWallet, error)

	// ListWallets returns all wallets, optionally filtered by chain.
	ListWallets(chain string) ([]*EncryptedWallet, error)

	// ListTenantWallets returns all wallets for a specific tenant.
	ListTenantWallets(tenantID string) ([]*EncryptedWallet, error)

	// DeleteWallet removes a wallet (use with caution).
	DeleteWallet(path string) error

	// Close closes the store and clears any sensitive data.
	Close() error
}

// WalletPath constructs a wallet path from components.
func WalletPath(walletType WalletType, identifier string, chain Chain) string {
	return string(walletType) + "/" + identifier + "/" + chain.String()
}

// SystemWalletPath constructs a system wallet path.
// Example: SystemWalletPath("hot", ChainTron) => "system/hot/tron"
func SystemWalletPath(name string, chain Chain) string {
	return WalletPath(WalletTypeSystem, name, chain)
}

// TenantWalletPath constructs a tenant wallet path.
// Example: TenantWalletPath("lemfi", ChainTron) => "tenant/lemfi/tron"
func TenantWalletPath(tenantSlug string, chain Chain) string {
	return WalletPath(WalletTypeTenant, tenantSlug, chain)
}
