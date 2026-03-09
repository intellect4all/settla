package wallet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileWalletStore implements WalletStore with file-based storage.
// Wallets are stored as JSON files with encrypted private keys.
type FileWalletStore struct {
	storagePath   string
	encryptionKey []byte
	mu            sync.RWMutex
}

// NewFileWalletStore creates a new file-based wallet store.
func NewFileWalletStore(storagePath string, encryptionKey []byte) (*FileWalletStore, error) {
	if len(encryptionKey) != 32 {
		return nil, fmt.Errorf("settla-wallet: encryption key must be 32 bytes, got %d", len(encryptionKey))
	}

	// Create storage directory with restricted permissions
	if err := os.MkdirAll(storagePath, 0700); err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to create storage directory: %w", err)
	}

	keyCopy := make([]byte, 32)
	copy(keyCopy, encryptionKey)

	return &FileWalletStore{
		storagePath:   storagePath,
		encryptionKey: keyCopy,
	}, nil
}

// SaveWallet persists an encrypted wallet to disk.
func (s *FileWalletStore) SaveWallet(wallet *EncryptedWallet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create subdirectories if needed (e.g., system/tron, tenant/lemfi)
	dir := filepath.Join(s.storagePath, filepath.Dir(wallet.Path))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("settla-wallet: failed to create wallet directory: %w", err)
	}

	data, err := json.MarshalIndent(wallet, "", "  ")
	if err != nil {
		return fmt.Errorf("settla-wallet: failed to marshal wallet: %w", err)
	}

	filePath := s.walletFilePath(wallet.Path)
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("settla-wallet: failed to write wallet file: %w", err)
	}

	return nil
}

// GetWallet retrieves an encrypted wallet by path.
func (s *FileWalletStore) GetWallet(path string) (*EncryptedWallet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := s.walletFilePath(path)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("settla-wallet: wallet not found: %s", path)
		}
		return nil, fmt.Errorf("settla-wallet: failed to read wallet file: %w", err)
	}

	var wallet EncryptedWallet
	if err := json.Unmarshal(data, &wallet); err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to parse wallet file: %w", err)
	}

	return &wallet, nil
}

// ListWallets returns all wallets, optionally filtered by chain.
func (s *FileWalletStore) ListWallets(chain string) ([]*EncryptedWallet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var wallets []*EncryptedWallet

	err := filepath.Walk(s.storagePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		var wallet EncryptedWallet
		if err := json.Unmarshal(data, &wallet); err != nil {
			return nil // Skip malformed files
		}

		// Filter by chain if specified
		if chain != "" && wallet.Chain != chain {
			return nil
		}

		wallets = append(wallets, &wallet)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to list wallets: %w", err)
	}

	return wallets, nil
}

// ListTenantWallets returns all wallets for a specific tenant.
func (s *FileWalletStore) ListTenantWallets(tenantID string) ([]*EncryptedWallet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var wallets []*EncryptedWallet

	err := filepath.Walk(s.storagePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var wallet EncryptedWallet
		if err := json.Unmarshal(data, &wallet); err != nil {
			return nil
		}

		if wallet.TenantID != nil && *wallet.TenantID == tenantID {
			wallets = append(wallets, &wallet)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("settla-wallet: failed to list tenant wallets: %w", err)
	}

	return wallets, nil
}

// DeleteWallet removes a wallet (use with caution).
func (s *FileWalletStore) DeleteWallet(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.walletFilePath(path)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("settla-wallet: wallet not found: %s", path)
		}
		return fmt.Errorf("settla-wallet: failed to delete wallet: %w", err)
	}

	return nil
}

// WalletExists checks if a wallet exists at the given path.
func (s *FileWalletStore) WalletExists(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := s.walletFilePath(path)
	_, err := os.Stat(filePath)
	return err == nil
}

// Close securely zeros the encryption key.
func (s *FileWalletStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.encryptionKey != nil {
		SecureClearBytes(s.encryptionKey)
		s.encryptionKey = nil
	}
	return nil
}

// Compile-time check that FileWalletStore satisfies the WalletStore interface.
var _ WalletStore = (*FileWalletStore)(nil)

// walletFilePath converts a wallet path to a file path.
// e.g., "system/tron/hot" → "{storagePath}/system/tron/hot.json"
func (s *FileWalletStore) walletFilePath(path string) string {
	// Sanitize path to prevent directory traversal
	cleanPath := filepath.Clean(path)
	cleanPath = strings.ReplaceAll(cleanPath, "..", "")
	return filepath.Join(s.storagePath, cleanPath+".json")
}

// EncryptionKey returns the encryption key (for use with EncryptPrivateKey).
// This should only be used internally by the wallet manager.
func (s *FileWalletStore) EncryptionKey() []byte {
	return s.encryptionKey
}
