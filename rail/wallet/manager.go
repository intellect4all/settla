package wallet

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"

	"github.com/intellect4all/settla/rail/wallet/keymgmt"
)

// Manager orchestrates HD wallet creation, retrieval, and signing.
// It provides deterministic wallet derivation from a master seed.
type Manager struct {
	keyManager keymgmt.KeyManager
	store      *FileWalletStore
	keyID      string // Default key ID for derivation

	// indexCounter tracks next derivation index per chain
	indexCounter map[Chain]uint32
	indexMu      sync.Mutex

	// walletCache caches loaded wallets by path
	walletCache map[string]*Wallet
	cacheMu     sync.RWMutex

	// faucetCfg holds testnet faucet configuration.
	faucetCfg FaucetConfig

	logger *slog.Logger
}

// ManagerConfig holds configuration for the wallet manager.
type ManagerConfig struct {
	// MasterSeed is the BIP-39 master seed (64 bytes from mnemonic).
	// If nil, seed will be loaded from KeyManager.
	MasterSeed []byte

	// KeyID is the identifier for the master seed in the key manager.
	// Default: "settla-master"
	KeyID string

	// EncryptionKey is a 32-byte key for AES-256 encryption (hex-encoded).
	EncryptionKey string

	// StoragePath is the directory for wallet and seed storage.
	StoragePath string

	// Logger for wallet operations.
	Logger *slog.Logger

	// FaucetConfig holds configuration for testnet faucets.
	// Optional — defaults are used for most fields if not set.
	FaucetConfig FaucetConfig
}

// NewManager creates a new wallet manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.KeyID == "" {
		cfg.KeyID = "settla-master"
	}

	// Decode encryption key
	encryptionKeyBytes, err := hex.DecodeString(cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: invalid encryption key hex: %w", err)
	}

	if len(encryptionKeyBytes) != 32 {
		return nil, fmt.Errorf("settla-wallet: encryption key must be 32 bytes (64 hex chars)")
	}

	// Create key manager (file-based)
	km, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: cfg.EncryptionKey,
		StorageType:   "file",
		StoragePath:   cfg.StoragePath + "/keys",
	})
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to create key manager: %w", err)
	}

	// Create wallet store
	store, err := NewFileWalletStore(cfg.StoragePath+"/wallets", encryptionKeyBytes)
	if err != nil {
		km.Close()
		return nil, fmt.Errorf("settla-wallet: failed to create wallet store: %w", err)
	}

	m := &Manager{
		keyManager:   km,
		store:        store,
		keyID:        cfg.KeyID,
		indexCounter: make(map[Chain]uint32),
		walletCache:  make(map[string]*Wallet),
		faucetCfg:    cfg.FaucetConfig,
		logger:       cfg.Logger,
	}

	// Store master seed if provided
	if len(cfg.MasterSeed) > 0 {
		if !km.HasSeed(cfg.KeyID) {
			if err := km.StoreMasterSeed(cfg.KeyID, cfg.MasterSeed); err != nil {
				m.Close()
				return nil, fmt.Errorf("settla-wallet: failed to store master seed: %w", err)
			}
			cfg.Logger.Info("settla-wallet: stored master seed", "key_id", cfg.KeyID)
		}
		// Clear the seed from config
		SecureClearBytes(cfg.MasterSeed)
	}

	// Verify we have a seed
	if !km.HasSeed(cfg.KeyID) {
		m.Close()
		return nil, fmt.Errorf("settla-wallet: no master seed found for key ID %s", cfg.KeyID)
	}

	// Load existing wallet index counters
	if err := m.loadIndexCounters(); err != nil {
		cfg.Logger.Warn("settla-wallet: failed to load index counters, starting from 0", "error", err)
	}

	return m, nil
}

// GetOrCreateWallet returns an existing wallet or creates a new one.
// Derivation is deterministic: same path + same seed = same wallet.
func (m *Manager) GetOrCreateWallet(ctx context.Context, path string, chain Chain, tenantID *uuid.UUID) (*Wallet, error) {
	// Check cache first
	m.cacheMu.RLock()
	if wallet, ok := m.walletCache[path]; ok {
		m.cacheMu.RUnlock()
		return wallet, nil
	}
	m.cacheMu.RUnlock()

	// Check if wallet exists in store
	if m.store.WalletExists(path) {
		return m.loadWallet(path)
	}

	// Create new wallet
	return m.createWallet(ctx, path, chain, tenantID)
}

// GetSystemWallet returns (or creates) the system hot wallet for a chain.
func (m *Manager) GetSystemWallet(chain Chain) (*Wallet, error) {
	path := SystemWalletPath("hot", chain)
	return m.GetOrCreateWallet(context.Background(), path, chain, nil)
}

// GetTenantWallet returns (or creates) a tenant's wallet for a chain.
func (m *Manager) GetTenantWallet(tenantID uuid.UUID, tenantSlug string, chain Chain) (*Wallet, error) {
	path := TenantWalletPath(tenantSlug, chain)
	return m.GetOrCreateWallet(context.Background(), path, chain, &tenantID)
}

// SignTransaction signs transaction data with the wallet's private key.
// Returns the signature; the private key is never exposed.
func (m *Manager) SignTransaction(ctx context.Context, walletPath string, txHash []byte) ([]byte, error) {
	wallet, err := m.GetOrCreateWallet(ctx, walletPath, "", nil)
	if err != nil {
		// Try to load by path directly
		wallet, err = m.loadWallet(walletPath)
		if err != nil {
			return nil, fmt.Errorf("settla-wallet: wallet not found: %s", walletPath)
		}
	}

	if !wallet.HasPrivateKey() {
		return nil, fmt.Errorf("settla-wallet: wallet has no private key loaded")
	}

	switch wallet.Chain {
	case ChainEthereum, ChainBase, ChainTron:
		privateKey := wallet.ECDSAPrivateKey()
		if privateKey == nil {
			return nil, fmt.Errorf("settla-wallet: invalid private key type for %s", wallet.Chain)
		}
		sig, err := crypto.Sign(txHash, privateKey)
		if err != nil {
			return nil, fmt.Errorf("settla-wallet: signing failed: %w", err)
		}
		return sig, nil

	case ChainSolana:
		privateKey := wallet.Ed25519PrivateKey()
		if privateKey == nil {
			return nil, fmt.Errorf("settla-wallet: invalid private key type for Solana")
		}
		sig := ed25519.Sign(privateKey, txHash)
		return sig, nil

	default:
		return nil, fmt.Errorf("settla-wallet: unsupported chain for signing: %s", wallet.Chain)
	}
}

// ListWallets returns all wallets, optionally filtered by chain.
func (m *Manager) ListWallets(chain string) ([]*Wallet, error) {
	encrypted, err := m.store.ListWallets(chain)
	if err != nil {
		return nil, err
	}

	wallets := make([]*Wallet, 0, len(encrypted))
	for _, ew := range encrypted {
		wallet, err := m.loadWallet(ew.Path)
		if err != nil {
			m.logger.Warn("settla-wallet: failed to load wallet", "path", ew.Path, "error", err)
			continue
		}
		wallets = append(wallets, wallet)
	}

	return wallets, nil
}

// Close securely clears all sensitive data and closes resources.
func (m *Manager) Close() error {
	m.cacheMu.Lock()
	for _, wallet := range m.walletCache {
		wallet.ZeroPrivateKey()
	}
	m.walletCache = nil
	m.cacheMu.Unlock()

	if m.store != nil {
		m.store.Close()
	}

	if m.keyManager != nil {
		m.keyManager.Close()
	}

	return nil
}

// createWallet creates a new wallet at the given path.
func (m *Manager) createWallet(ctx context.Context, path string, chain Chain, tenantID *uuid.UUID) (*Wallet, error) {
	// Get next derivation index for this chain
	index := m.nextIndex(chain)

	// Derive wallet
	wallet, err := DeriveWallet(m.keyManager, m.keyID, chain, index)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: derivation failed: %w", err)
	}

	// Set metadata
	wallet.Path = path
	wallet.TenantID = tenantID
	wallet.DerivationIndex = index
	wallet.CreatedAt = time.Now().UTC()

	if tenantID != nil {
		wallet.Type = WalletTypeTenant
		// Extract tenant slug from path (e.g., "tenant/lemfi/tron" → "lemfi")
		wallet.TenantSlug = extractTenantSlug(path)
	} else {
		wallet.Type = WalletTypeSystem
	}

	// Encrypt and persist
	if err := m.persistWallet(wallet); err != nil {
		wallet.ZeroPrivateKey()
		return nil, err
	}

	// Cache the wallet
	m.cacheMu.Lock()
	m.walletCache[path] = wallet
	m.cacheMu.Unlock()

	m.logger.Info("settla-wallet: created wallet",
		"path", path,
		"chain", chain,
		"address", wallet.Address,
		"index", index,
	)

	return wallet, nil
}

// loadWallet loads and decrypts a wallet from storage.
func (m *Manager) loadWallet(path string) (*Wallet, error) {
	// Check cache
	m.cacheMu.RLock()
	if wallet, ok := m.walletCache[path]; ok {
		m.cacheMu.RUnlock()
		return wallet, nil
	}
	m.cacheMu.RUnlock()

	// Load from store
	encrypted, err := m.store.GetWallet(path)
	if err != nil {
		return nil, err
	}

	// Decrypt private key
	privateKeyBytes, err := Decrypt(encrypted.EncryptedKey, m.store.EncryptionKey())
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to decrypt private key: %w", err)
	}
	defer SecureClearBytes(privateKeyBytes)

	// Decode public key
	publicKey, err := hex.DecodeString(encrypted.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to decode public key: %w", err)
	}

	// Parse tenant ID if present
	var tenantID *uuid.UUID
	if encrypted.TenantID != nil {
		id, err := uuid.Parse(*encrypted.TenantID)
		if err == nil {
			tenantID = &id
		}
	}

	// Parse creation time
	createdAt, _ := time.Parse(time.RFC3339, encrypted.CreatedAt)

	wallet := &Wallet{
		Path:            encrypted.Path,
		Chain:           Chain(encrypted.Chain),
		Address:         encrypted.Address,
		PublicKey:       publicKey,
		TenantID:        tenantID,
		TenantSlug:      encrypted.TenantSlug,
		Type:            WalletType(encrypted.Type),
		DerivationIndex: encrypted.DerivationIndex,
		CreatedAt:       createdAt,
	}

	// Reconstruct private key based on chain
	chain := Chain(encrypted.Chain)
	switch chain {
	case ChainEthereum, ChainBase, ChainTron:
		privateKey, err := crypto.ToECDSA(privateKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("settla-wallet: failed to reconstruct ECDSA key: %w", err)
		}
		wallet.setPrivateKey(privateKey)

	case ChainSolana:
		if len(privateKeyBytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("settla-wallet: invalid Ed25519 key size")
		}
		privateKey := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
		copy(privateKey, privateKeyBytes)
		wallet.setPrivateKey(privateKey)

	default:
		return nil, fmt.Errorf("settla-wallet: unsupported chain: %s", chain)
	}

	// Cache the wallet
	m.cacheMu.Lock()
	m.walletCache[path] = wallet
	m.cacheMu.Unlock()

	return wallet, nil
}

// persistWallet encrypts and saves a wallet to storage.
func (m *Manager) persistWallet(wallet *Wallet) error {
	// Serialize private key based on chain
	var privateKeyBytes []byte
	switch wallet.Chain {
	case ChainEthereum, ChainBase, ChainTron:
		privateKey := wallet.ECDSAPrivateKey()
		if privateKey == nil {
			return fmt.Errorf("settla-wallet: no ECDSA private key")
		}
		privateKeyBytes = crypto.FromECDSA(privateKey)

	case ChainSolana:
		privateKey := wallet.Ed25519PrivateKey()
		if privateKey == nil {
			return fmt.Errorf("settla-wallet: no Ed25519 private key")
		}
		// Copy before zeroing — Ed25519PrivateKey() returns the live slice,
		// so SecureClearBytes below would zero the wallet's in-memory key.
		privateKeyBytes = make([]byte, len(privateKey))
		copy(privateKeyBytes, privateKey)

	default:
		return fmt.Errorf("settla-wallet: unsupported chain: %s", wallet.Chain)
	}

	// Encrypt private key
	encryptedKey, err := Encrypt(privateKeyBytes, m.store.EncryptionKey())
	SecureClearBytes(privateKeyBytes)
	if err != nil {
		return fmt.Errorf("settla-wallet: failed to encrypt private key: %w", err)
	}

	// Convert tenant ID to string
	var tenantIDStr *string
	if wallet.TenantID != nil {
		s := wallet.TenantID.String()
		tenantIDStr = &s
	}

	encrypted := &EncryptedWallet{
		Path:            wallet.Path,
		Chain:           string(wallet.Chain),
		Address:         wallet.Address,
		PublicKey:       hex.EncodeToString(wallet.PublicKey),
		EncryptedKey:    encryptedKey,
		TenantID:        tenantIDStr,
		TenantSlug:      wallet.TenantSlug,
		Type:            string(wallet.Type),
		DerivationIndex: wallet.DerivationIndex,
		CreatedAt:       wallet.CreatedAt.Format(time.RFC3339),
	}

	return m.store.SaveWallet(encrypted)
}

// nextIndex returns and increments the next derivation index for a chain.
func (m *Manager) nextIndex(chain Chain) uint32 {
	m.indexMu.Lock()
	defer m.indexMu.Unlock()

	index := m.indexCounter[chain]
	m.indexCounter[chain] = index + 1
	return index
}

// loadIndexCounters loads the highest derivation index from existing wallets.
func (m *Manager) loadIndexCounters() error {
	for _, chain := range ValidChains() {
		wallets, err := m.store.ListWallets(chain.String())
		if err != nil {
			continue
		}

		maxIndex := uint32(0)
		for _, w := range wallets {
			if w.DerivationIndex >= maxIndex {
				maxIndex = w.DerivationIndex + 1
			}
		}
		m.indexCounter[chain] = maxIndex
	}
	return nil
}

// extractTenantSlug extracts the tenant slug from a wallet path.
// e.g., "tenant/lemfi/tron" → "lemfi"
func extractTenantSlug(path string) string {
	// Path format: "tenant/{slug}/{chain}"
	if len(path) < 8 || path[:7] != "tenant/" {
		return ""
	}
	remaining := path[7:]
	for i, c := range remaining {
		if c == '/' {
			return remaining[:i]
		}
	}
	return remaining
}

// GetPrivateKeyForSigning returns the private key for signing (ECDSA for EVM/Tron).
// IMPORTANT: The caller is responsible for zeroing the key after use.
func (m *Manager) GetPrivateKeyForSigning(walletPath string) (*ecdsa.PrivateKey, error) {
	wallet, err := m.loadWallet(walletPath)
	if err != nil {
		return nil, err
	}

	if !wallet.HasPrivateKey() {
		return nil, fmt.Errorf("settla-wallet: wallet has no private key loaded")
	}

	switch wallet.Chain {
	case ChainEthereum, ChainBase, ChainTron:
		return wallet.ECDSAPrivateKey(), nil
	default:
		return nil, fmt.Errorf("settla-wallet: chain %s does not use ECDSA", wallet.Chain)
	}
}

// GetEd25519KeyForSigning returns the Ed25519 private key for Solana signing.
// IMPORTANT: The caller is responsible for zeroing the key after use.
func (m *Manager) GetEd25519KeyForSigning(walletPath string) (ed25519.PrivateKey, error) {
	wallet, err := m.loadWallet(walletPath)
	if err != nil {
		return nil, err
	}

	if !wallet.HasPrivateKey() {
		return nil, fmt.Errorf("settla-wallet: wallet has no private key loaded")
	}

	if wallet.Chain != ChainSolana {
		return nil, fmt.Errorf("settla-wallet: chain %s does not use Ed25519", wallet.Chain)
	}

	return wallet.Ed25519PrivateKey(), nil
}
