// Package keymgmt provides key management for HD wallet derivation.
// It handles secure storage and retrieval of master seeds, as well as
// BIP-44 compliant key derivation for multiple blockchain networks.
package keymgmt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tyler-smith/go-bip32"
)

// KeyManager defines the interface for managing HD wallet seeds and derivation.
type KeyManager interface {
	// GetMasterSeed retrieves the decrypted master seed for a key ID.
	// The caller is responsible for zeroing the returned bytes after use.
	GetMasterSeed(keyID string) ([]byte, error)

	// StoreMasterSeed encrypts and stores a master seed.
	StoreMasterSeed(keyID string, seed []byte) error

	// DerivePath derives a BIP-32 key at a specific BIP-44 path.
	// Path: m/44'/coinType'/account'/change/index
	// The caller is responsible for zeroing the returned key after use.
	DerivePath(keyID string, coinType, account, change, index uint32) (*bip32.Key, error)

	// HasSeed checks if a seed exists for the given key ID.
	HasSeed(keyID string) bool

	// Close securely clears all sensitive data and closes any resources.
	Close() error
}

// Config holds configuration for the key manager.
type Config struct {
	// EncryptionKey is a 64-character hex string (32 bytes) for AES-256.
	EncryptionKey string

	// StorageType is "file" or "env".
	StorageType string

	// StoragePath is the directory for file storage.
	StoragePath string
}

// NewKeyManager creates a KeyManager based on the configuration.
func NewKeyManager(cfg Config) (KeyManager, error) {
	switch cfg.StorageType {
	case "file":
		return NewFileStore(cfg)
	case "env", "environment":
		return NewEnvStore(cfg)
	default:
		return nil, fmt.Errorf("settla-keymgmt: unknown storage type: %s (supported: file, env)", cfg.StorageType)
	}
}

// FileStore implements KeyManager with file-based encrypted storage.
type FileStore struct {
	encryptionKey []byte
	storagePath   string
	mu            sync.RWMutex
}

// StoredKey is the JSON structure for persisted encrypted seeds.
type StoredKey struct {
	KeyID         string `json:"key_id"`
	EncryptedSeed string `json:"encrypted_seed"`
	Checksum      string `json:"checksum"` // First 8 bytes of seed for verification
	CreatedAt     string `json:"created_at"`
}

// NewFileStore creates a new file-based key manager.
func NewFileStore(cfg Config) (*FileStore, error) {
	// Ensure storage directory exists with restricted permissions
	if err := os.MkdirAll(cfg.StoragePath, 0700); err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to create storage path: %w", err)
	}

	// Decode hex encryption key
	keyBytes, err := hex.DecodeString(cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: invalid encryption key hex: %w", err)
	}

	// Validate key size (must be 32 bytes for AES-256)
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("settla-keymgmt: encryption key must be 32 bytes (64 hex chars), got %d bytes", len(keyBytes))
	}

	return &FileStore{
		encryptionKey: keyBytes,
		storagePath:   cfg.StoragePath,
	}, nil
}

// GetMasterSeed retrieves and decrypts a master seed.
func (fs *FileStore) GetMasterSeed(keyID string) ([]byte, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	filePath := filepath.Join(fs.storagePath, fmt.Sprintf("%s.json", keyID))

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("settla-keymgmt: seed not found for key ID %s", keyID)
		}
		return nil, fmt.Errorf("settla-keymgmt: failed to read seed file: %w", err)
	}

	var stored StoredKey
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to parse seed file: %w", err)
	}

	// Decrypt seed
	seed, err := Decrypt(stored.EncryptedSeed, fs.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to decrypt seed: %w", err)
	}

	return seed, nil
}

// StoreMasterSeed encrypts and persists a master seed.
func (fs *FileStore) StoreMasterSeed(keyID string, seed []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Encrypt seed
	encryptedSeed, err := Encrypt(seed, fs.encryptionKey)
	if err != nil {
		return fmt.Errorf("settla-keymgmt: failed to encrypt seed: %w", err)
	}

	// Create checksum from first 8 bytes (for verification, not security)
	checksumLen := 8
	if len(seed) < checksumLen {
		checksumLen = len(seed)
	}

	stored := StoredKey{
		KeyID:         keyID,
		EncryptedSeed: encryptedSeed,
		Checksum:      hex.EncodeToString(seed[:checksumLen]),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("settla-keymgmt: failed to marshal seed: %w", err)
	}

	filePath := filepath.Join(fs.storagePath, fmt.Sprintf("%s.json", keyID))
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("settla-keymgmt: failed to write seed file: %w", err)
	}

	return nil
}

// DerivePath derives a BIP-32 key at the specified BIP-44 path.
// Path: m/44'/coinType'/account'/change/index
func (fs *FileStore) DerivePath(keyID string, coinType, account, change, index uint32) (*bip32.Key, error) {
	// Get master seed
	seed, err := fs.GetMasterSeed(keyID)
	if err != nil {
		return nil, err
	}
	defer SecureClearBytes(seed)

	// Generate master key from seed
	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to create master key: %w", err)
	}
	defer SecureZeroBIP32Key(masterKey)

	// Derive path: m/44'/coinType'/account'/change/index
	// Purpose (44' - hardened)
	purpose, err := masterKey.NewChildKey(bip32.FirstHardenedChild + 44)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to derive purpose: %w", err)
	}
	defer SecureZeroBIP32Key(purpose)

	// Coin type (hardened)
	coin, err := purpose.NewChildKey(bip32.FirstHardenedChild + coinType)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to derive coin type: %w", err)
	}
	defer SecureZeroBIP32Key(coin)

	// Account (hardened)
	acct, err := coin.NewChildKey(bip32.FirstHardenedChild + account)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to derive account: %w", err)
	}
	defer SecureZeroBIP32Key(acct)

	// Change (not hardened)
	chng, err := acct.NewChildKey(change)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to derive change: %w", err)
	}
	defer SecureZeroBIP32Key(chng)

	// Index (not hardened) - this is the final derived key
	derived, err := chng.NewChildKey(index)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to derive index: %w", err)
	}

	return derived, nil
}

// HasSeed checks if a seed exists for the given key ID.
func (fs *FileStore) HasSeed(keyID string) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	filePath := filepath.Join(fs.storagePath, fmt.Sprintf("%s.json", keyID))
	_, err := os.Stat(filePath)
	return err == nil
}

// Close securely zeros the encryption key in memory.
func (fs *FileStore) Close() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.encryptionKey != nil {
		SecureClearBytes(fs.encryptionKey)
		fs.encryptionKey = nil
	}
	return nil
}

// EnvStore implements KeyManager with environment variable storage.
// Seeds are loaded from environment variables on startup.
type EnvStore struct {
	encryptionKey []byte
	seeds         map[string][]byte
	mu            sync.RWMutex
}

// NewEnvStore creates a new environment-based key manager.
// It expects seeds in environment variables named {prefix}_{keyID}.
func NewEnvStore(cfg Config) (*EnvStore, error) {
	// Decode hex encryption key
	keyBytes, err := hex.DecodeString(cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: invalid encryption key hex: %w", err)
	}

	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("settla-keymgmt: encryption key must be 32 bytes, got %d", len(keyBytes))
	}

	return &EnvStore{
		encryptionKey: keyBytes,
		seeds:         make(map[string][]byte),
	}, nil
}

// LoadSeedFromEnv loads a seed from an environment variable.
// The seed should be hex-encoded.
func (es *EnvStore) LoadSeedFromEnv(keyID, envVar string) error {
	seedHex := os.Getenv(envVar)
	if seedHex == "" {
		return fmt.Errorf("settla-keymgmt: environment variable %s not set", envVar)
	}

	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return fmt.Errorf("settla-keymgmt: invalid seed hex in %s: %w", envVar, err)
	}

	es.mu.Lock()
	es.seeds[keyID] = seed
	es.mu.Unlock()

	return nil
}

// GetMasterSeed retrieves a seed loaded from environment.
func (es *EnvStore) GetMasterSeed(keyID string) ([]byte, error) {
	es.mu.RLock()
	defer es.mu.RUnlock()

	seed, ok := es.seeds[keyID]
	if !ok {
		return nil, fmt.Errorf("settla-keymgmt: seed not found for key ID %s", keyID)
	}

	// Return a copy to prevent modification
	result := make([]byte, len(seed))
	copy(result, seed)
	return result, nil
}

// StoreMasterSeed stores a seed in memory (not persisted to env).
func (es *EnvStore) StoreMasterSeed(keyID string, seed []byte) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	seedCopy := make([]byte, len(seed))
	copy(seedCopy, seed)
	es.seeds[keyID] = seedCopy
	return nil
}

// DerivePath derives a BIP-32 key at the specified BIP-44 path.
func (es *EnvStore) DerivePath(keyID string, coinType, account, change, index uint32) (*bip32.Key, error) {
	seed, err := es.GetMasterSeed(keyID)
	if err != nil {
		return nil, err
	}
	defer SecureClearBytes(seed)

	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, fmt.Errorf("settla-keymgmt: failed to create master key: %w", err)
	}
	defer SecureZeroBIP32Key(masterKey)

	purpose, err := masterKey.NewChildKey(bip32.FirstHardenedChild + 44)
	if err != nil {
		return nil, err
	}
	defer SecureZeroBIP32Key(purpose)

	coin, err := purpose.NewChildKey(bip32.FirstHardenedChild + coinType)
	if err != nil {
		return nil, err
	}
	defer SecureZeroBIP32Key(coin)

	acct, err := coin.NewChildKey(bip32.FirstHardenedChild + account)
	if err != nil {
		return nil, err
	}
	defer SecureZeroBIP32Key(acct)

	chng, err := acct.NewChildKey(change)
	if err != nil {
		return nil, err
	}
	defer SecureZeroBIP32Key(chng)

	return chng.NewChildKey(index)
}

// HasSeed checks if a seed exists for the given key ID.
func (es *EnvStore) HasSeed(keyID string) bool {
	es.mu.RLock()
	defer es.mu.RUnlock()
	_, ok := es.seeds[keyID]
	return ok
}

// Close securely zeros all sensitive data.
func (es *EnvStore) Close() error {
	es.mu.Lock()
	defer es.mu.Unlock()

	for _, seed := range es.seeds {
		SecureClearBytes(seed)
	}
	es.seeds = nil

	if es.encryptionKey != nil {
		SecureClearBytes(es.encryptionKey)
		es.encryptionKey = nil
	}
	return nil
}
