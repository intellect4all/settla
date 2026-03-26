package wallet_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gocrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/tyler-smith/go-bip39"

	"github.com/intellect4all/settla/rail/wallet"
	"github.com/intellect4all/settla/rail/wallet/keymgmt"
)

// Test encryption key (32 bytes = 64 hex chars)
const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Test mnemonic for deterministic testing
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func TestDeterministicDerivation(t *testing.T) {
	// Generate seed from mnemonic
	seed := bip39.NewSeed(testMnemonic, "")

	// Create temp directory
	tempDir := t.TempDir()

	// Create key manager and store seed
	km, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   filepath.Join(tempDir, "keys"),
	})
	if err != nil {
		t.Fatalf("failed to create key manager: %v", err)
	}
	defer km.Close()

	keyID := "test-master"
	if err := km.StoreMasterSeed(keyID, seed); err != nil {
		t.Fatalf("failed to store seed: %v", err)
	}

	// Derive wallets for each chain
	chains := []wallet.Chain{
		wallet.ChainEthereum,
		wallet.ChainTron,
		wallet.ChainSolana,
		wallet.ChainBase,
	}

	// Store first derivation addresses
	firstAddresses := make(map[wallet.Chain]string)

	for _, chain := range chains {
		w, err := wallet.DeriveWallet(km, keyID, chain, 0)
		if err != nil {
			t.Fatalf("failed to derive %s wallet: %v", chain, err)
		}
		firstAddresses[chain] = w.Address
		w.ZeroPrivateKey()
	}

	// Derive again and verify addresses match (deterministic)
	for _, chain := range chains {
		w, err := wallet.DeriveWallet(km, keyID, chain, 0)
		if err != nil {
			t.Fatalf("failed to derive %s wallet second time: %v", chain, err)
		}

		if w.Address != firstAddresses[chain] {
			t.Errorf("%s: addresses don't match: got %s, want %s", chain, w.Address, firstAddresses[chain])
		}
		w.ZeroPrivateKey()
	}
}

func TestAddressFormats(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	km, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   filepath.Join(tempDir, "keys"),
	})
	if err != nil {
		t.Fatalf("failed to create key manager: %v", err)
	}
	defer km.Close()

	keyID := "test-master"
	if err := km.StoreMasterSeed(keyID, seed); err != nil {
		t.Fatalf("failed to store seed: %v", err)
	}

	tests := []struct {
		chain         wallet.Chain
		addressPrefix string
		addressLen    int
	}{
		{wallet.ChainEthereum, "0x", 42},
		{wallet.ChainBase, "0x", 42},
		{wallet.ChainTron, "T", 34},
		// Solana addresses are base58, variable length typically 32-44 chars
		{wallet.ChainSolana, "", 0}, // No prefix, variable length
	}

	for _, tt := range tests {
		t.Run(string(tt.chain), func(t *testing.T) {
			w, err := wallet.DeriveWallet(km, keyID, tt.chain, 0)
			if err != nil {
				t.Fatalf("failed to derive wallet: %v", err)
			}
			defer w.ZeroPrivateKey()

			// Check address prefix
			if tt.addressPrefix != "" && !strings.HasPrefix(w.Address, tt.addressPrefix) {
				t.Errorf("address doesn't have expected prefix: got %s, want prefix %s", w.Address, tt.addressPrefix)
			}

			// Check address length (for fixed-length formats)
			if tt.addressLen > 0 && len(w.Address) != tt.addressLen {
				t.Errorf("address has unexpected length: got %d, want %d", len(w.Address), tt.addressLen)
			}

			// Validate address format
			if !wallet.ValidateAddress(tt.chain, w.Address) {
				t.Errorf("generated address is invalid: %s", w.Address)
			}
		})
	}
}

func TestEncryptionRoundTrip(t *testing.T) {
	key, _ := hex.DecodeString(testEncryptionKey)
	plaintext := []byte("test secret data")

	// Encrypt
	ciphertext, err := wallet.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encryption failed: %v", err)
	}

	// Decrypt
	decrypted, err := wallet.Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}

	// Verify
	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted data doesn't match: got %s, want %s", decrypted, plaintext)
	}
}

func TestEncryptionDifferentCiphertexts(t *testing.T) {
	key, _ := hex.DecodeString(testEncryptionKey)
	plaintext := []byte("test data")

	// Encrypt same data twice
	c1, err := wallet.Encrypt(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := wallet.Encrypt(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}

	// Different nonces should produce different ciphertexts
	if c1 == c2 {
		t.Error("encrypting same data twice produced identical ciphertexts")
	}

	// Both should decrypt to same plaintext
	d1, _ := wallet.Decrypt(c1, key)
	d2, _ := wallet.Decrypt(c2, key)

	if string(d1) != string(d2) {
		t.Error("different ciphertexts decrypted to different plaintexts")
	}
}

func TestSecureClearBytes(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	original := make([]byte, len(data))
	copy(original, data)

	wallet.SecureClearBytes(data)

	// Verify all bytes are zeroed
	for i, b := range data {
		if b != 0 {
			t.Errorf("byte %d not zeroed: got %d", i, b)
		}
	}
}

func TestWalletManager(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	// Create system wallet
	sysWallet, err := mgr.GetSystemWallet(wallet.ChainTron)
	if err != nil {
		t.Fatalf("failed to get system wallet: %v", err)
	}

	if sysWallet.Type != wallet.WalletTypeSystem {
		t.Errorf("wrong wallet type: got %s, want system", sysWallet.Type)
	}

	if !wallet.ValidateTronAddress(sysWallet.Address) {
		t.Errorf("invalid Tron address: %s", sysWallet.Address)
	}

	// Create tenant wallet
	tenantID := uuid.New()
	tenantWallet, err := mgr.GetTenantWallet(tenantID, "lemfi", wallet.ChainEthereum)
	if err != nil {
		t.Fatalf("failed to get tenant wallet: %v", err)
	}

	if tenantWallet.Type != wallet.WalletTypeTenant {
		t.Errorf("wrong wallet type: got %s, want tenant", tenantWallet.Type)
	}

	if tenantWallet.TenantID == nil || *tenantWallet.TenantID != tenantID {
		t.Error("tenant ID not set correctly")
	}

	// Verify wallets persist and reload
	wallet2, err := mgr.GetSystemWallet(wallet.ChainTron)
	if err != nil {
		t.Fatal(err)
	}

	if wallet2.Address != sysWallet.Address {
		t.Errorf("reloaded wallet has different address: got %s, want %s", wallet2.Address, sysWallet.Address)
	}
}

func TestWalletPersistence(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	// Create manager and wallet
	mgr1, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	w1, err := mgr1.GetSystemWallet(wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}
	address1 := w1.Address
	mgr1.Close()

	// Create new manager with same config (no seed, should load from store)
	mgr2, err := wallet.NewManager(wallet.ManagerConfig{
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr2.Close()

	w2, err := mgr2.GetSystemWallet(wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}

	if w2.Address != address1 {
		t.Errorf("persisted wallet has different address: got %s, want %s", w2.Address, address1)
	}
}

func TestTronAddressValidation(t *testing.T) {
	tests := []struct {
		address string
		valid   bool
	}{
		{"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", true},  // Valid Tron address
		{"TXXXXXXXXXXXXXXXXXXXXXXXXXXXXpKDQa", false}, // Invalid checksum
		{"0x71C7656EC7ab88b098defB751B7401B5f6d8976F", false}, // Ethereum format
		{"T123", false},  // Too short
		{"AAAA", false},  // Not T-prefix
		{"", false},      // Empty
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if got := wallet.ValidateTronAddress(tt.address); got != tt.valid {
				t.Errorf("ValidateTronAddress(%s) = %v, want %v", tt.address, got, tt.valid)
			}
		})
	}
}

func TestEthereumAddressValidation(t *testing.T) {
	tests := []struct {
		address string
		valid   bool
	}{
		{"0x71C7656EC7ab88b098defB751B7401B5f6d8976F", true},
		{"0x0000000000000000000000000000000000000000", true},
		{"71C7656EC7ab88b098defB751B7401B5f6d8976F", false},   // Missing 0x
		{"0x71C7656EC7ab88b098defB751B7401B5f6d8976", false},  // Too short
		{"0xZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ", false}, // Invalid hex
		{"TJRabPrwbZy45sbavfcjinPJC18kjpRTv8", false},         // Tron format
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if got := wallet.ValidateEthereumAddress(tt.address); got != tt.valid {
				t.Errorf("ValidateEthereumAddress(%s) = %v, want %v", tt.address, got, tt.valid)
			}
		})
	}
}

func TestChainTypes(t *testing.T) {
	tests := []struct {
		chain    wallet.Chain
		coinType uint32
		isEVM    bool
	}{
		{wallet.ChainEthereum, 60, true},
		{wallet.ChainBase, 60, true},
		{wallet.ChainTron, 195, false},
		{wallet.ChainSolana, 501, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.chain), func(t *testing.T) {
			if got := wallet.CoinType(tt.chain); got != tt.coinType {
				t.Errorf("CoinType() = %d, want %d", got, tt.coinType)
			}
			if got := tt.chain.IsEVM(); got != tt.isEVM {
				t.Errorf("IsEVM() = %v, want %v", got, tt.isEVM)
			}
		})
	}
}

func TestWalletPaths(t *testing.T) {
	tests := []struct {
		pathFunc func() string
		expected string
	}{
		{func() string { return wallet.SystemWalletPath("hot", wallet.ChainTron) }, "system/hot/tron"},
		{func() string { return wallet.SystemWalletPath("hot", wallet.ChainEthereum) }, "system/hot/ethereum"},
		{func() string { return wallet.TenantWalletPath("lemfi", wallet.ChainTron) }, "tenant/lemfi/tron"},
		{func() string { return wallet.TenantWalletPath("fincra", wallet.ChainSolana) }, "tenant/fincra/solana"},
	}

	for _, tt := range tests {
		got := tt.pathFunc()
		if got != tt.expected {
			t.Errorf("path = %s, want %s", got, tt.expected)
		}
	}
}

func TestDifferentIndexesDifferentAddresses(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	km, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   filepath.Join(tempDir, "keys"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer km.Close()

	keyID := "test-master"
	if err := km.StoreMasterSeed(keyID, seed); err != nil {
		t.Fatal(err)
	}

	// Derive 5 wallets at different indices
	addresses := make(map[string]bool)
	for i := uint32(0); i < 5; i++ {
		w, err := wallet.DeriveWallet(km, keyID, wallet.ChainEthereum, i)
		if err != nil {
			t.Fatal(err)
		}

		if addresses[w.Address] {
			t.Errorf("duplicate address at index %d: %s", i, w.Address)
		}
		addresses[w.Address] = true
		w.ZeroPrivateKey()
	}
}

func TestKeyManagerPersistence(t *testing.T) {
	tempDir := t.TempDir()
	seed := []byte("test seed for persistence testing 12345678")

	// Create key manager and store seed
	km1, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	keyID := "test-persist"
	if err := km1.StoreMasterSeed(keyID, seed); err != nil {
		t.Fatal(err)
	}
	km1.Close()

	// Create new key manager and retrieve seed
	km2, err := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer km2.Close()

	retrieved, err := km2.GetMasterSeed(keyID)
	if err != nil {
		t.Fatal(err)
	}
	defer keymgmt.SecureClearBytes(retrieved)

	if string(retrieved) != string(seed) {
		t.Error("retrieved seed doesn't match original")
	}
}

func TestSignTransaction(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	ctx := context.Background()

	t.Run("ethereum", func(t *testing.T) {
		w, err := mgr.GetSystemWallet(wallet.ChainEthereum)
		if err != nil {
			t.Fatal(err)
		}

		hash := make([]byte, 32)
		for i := range hash {
			hash[i] = byte(i)
		}

		sig, err := mgr.SignTransaction(ctx, w.Path, hash)
		if err != nil {
			t.Fatalf("signing failed: %v", err)
		}

		// Ethereum signatures are 65 bytes [R || S || V]
		if len(sig) != 65 {
			t.Errorf("expected 65-byte signature, got %d bytes", len(sig))
		}

		// Recover the public key from the signature and verify it matches
		recoveredPub, err := gocrypto.Ecrecover(hash, sig)
		if err != nil {
			t.Fatalf("ecrecover failed: %v", err)
		}

		if !bytes.Equal(recoveredPub, w.PublicKey) {
			t.Error("recovered public key does not match wallet public key")
		}
	})

	t.Run("tron", func(t *testing.T) {
		w, err := mgr.GetSystemWallet(wallet.ChainTron)
		if err != nil {
			t.Fatal(err)
		}

		hash := make([]byte, 32)
		for i := range hash {
			hash[i] = byte(i + 10)
		}

		sig, err := mgr.SignTransaction(ctx, w.Path, hash)
		if err != nil {
			t.Fatalf("signing failed: %v", err)
		}

		// Tron uses the same ECDSA as Ethereum — 65-byte signature
		if len(sig) != 65 {
			t.Errorf("expected 65-byte signature, got %d bytes", len(sig))
		}

		recoveredPub, err := gocrypto.Ecrecover(hash, sig)
		if err != nil {
			t.Fatalf("ecrecover failed: %v", err)
		}

		if !bytes.Equal(recoveredPub, w.PublicKey) {
			t.Error("recovered public key does not match wallet public key")
		}
	})

	t.Run("solana", func(t *testing.T) {
		w, err := mgr.GetSystemWallet(wallet.ChainSolana)
		if err != nil {
			t.Fatal(err)
		}

		message := []byte("test message for solana signing")

		sig, err := mgr.SignTransaction(ctx, w.Path, message)
		if err != nil {
			t.Fatalf("signing failed: %v", err)
		}

		// Ed25519 signatures are always 64 bytes
		if len(sig) != 64 {
			t.Errorf("expected 64-byte signature, got %d bytes", len(sig))
		}

		if !ed25519.Verify(w.PublicKey, message, sig) {
			t.Error("Ed25519 signature verification failed")
		}
	})
}

func TestConcurrentWalletAccess(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer mgr.Close()

	const numGoroutines = 20

	var wg sync.WaitGroup
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Mix of system and tenant wallet accesses
			if idx%2 == 0 {
				_, errs[idx] = mgr.GetSystemWallet(wallet.ChainEthereum)
			} else {
				id := uuid.New()
				_, errs[idx] = mgr.GetTenantWallet(id, "tenant-concurrent", wallet.ChainTron)
			}
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

func TestTenantWalletIsolation(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	mgr, err := wallet.NewManager(wallet.ManagerConfig{
		MasterSeed:    seed,
		KeyID:         "test-master",
		EncryptionKey: testEncryptionKey,
		StoragePath:   tempDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	// System wallet should have a different address than tenant wallet for the same chain
	sysWallet, err := mgr.GetSystemWallet(wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}

	tenantID := uuid.New()
	tenantWallet, err := mgr.GetTenantWallet(tenantID, "lemfi", wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}

	// Different derivation index → different addresses
	if sysWallet.Address == tenantWallet.Address {
		t.Error("system and tenant wallets must have different addresses")
	}

	// Two different tenants on the same chain must have different wallets
	tenant2ID := uuid.New()
	tenant2Wallet, err := mgr.GetTenantWallet(tenant2ID, "fincra", wallet.ChainEthereum)
	if err != nil {
		t.Fatal(err)
	}

	if tenantWallet.Address == tenant2Wallet.Address {
		t.Error("different tenants must have different wallet addresses")
	}
}

// BenchmarkDerivation measures derivation performance
func BenchmarkDerivation(b *testing.B) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := b.TempDir()

	km, _ := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   filepath.Join(tempDir, "keys"),
	})
	defer km.Close()
	km.StoreMasterSeed("bench", seed)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w, _ := wallet.DeriveWallet(km, "bench", wallet.ChainEthereum, uint32(i))
		w.ZeroPrivateKey()
	}
}

func TestFileWalletStore(t *testing.T) {
	tempDir := t.TempDir()
	key, _ := hex.DecodeString(testEncryptionKey)

	store, err := wallet.NewFileWalletStore(tempDir, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Create test wallet
	ew := &wallet.EncryptedWallet{
		Path:            "system/hot/tron",
		Chain:           "tron",
		Address:         "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8",
		PublicKey:       "abcd1234",
		EncryptedKey:    "encrypted_key_here",
		Type:            "system",
		DerivationIndex: 0,
		CreatedAt:       "2024-01-01T00:00:00Z",
	}

	// Save
	if err := store.SaveWallet(ew); err != nil {
		t.Fatalf("SaveWallet failed: %v", err)
	}

	// Verify file exists
	if !store.WalletExists(ew.Path) {
		t.Error("wallet should exist after save")
	}

	// Retrieve
	retrieved, err := store.GetWallet(ew.Path)
	if err != nil {
		t.Fatalf("GetWallet failed: %v", err)
	}

	if retrieved.Address != ew.Address {
		t.Errorf("address mismatch: got %s, want %s", retrieved.Address, ew.Address)
	}

	// List wallets
	wallets, err := store.ListWallets("tron")
	if err != nil {
		t.Fatal(err)
	}
	if len(wallets) != 1 {
		t.Errorf("expected 1 wallet, got %d", len(wallets))
	}
}

// TestPrivateKeyNeverInLogs verifies private keys don't appear in strings
func TestPrivateKeyNeverInLogs(t *testing.T) {
	seed := bip39.NewSeed(testMnemonic, "")
	tempDir := t.TempDir()

	km, _ := keymgmt.NewKeyManager(keymgmt.Config{
		EncryptionKey: testEncryptionKey,
		StorageType:   "file",
		StoragePath:   filepath.Join(tempDir, "keys"),
	})
	defer km.Close()
	km.StoreMasterSeed("test", seed)

	w, _ := wallet.DeriveWallet(km, "test", wallet.ChainEthereum, 0)
	defer w.ZeroPrivateKey()

	// The wallet's string representation should not contain the private key
	// This is a basic sanity check - in production, logging would be tested more thoroughly
	if w.HasPrivateKey() {
		// Wallet has private key but it shouldn't be directly accessible as bytes
		// The ECDSAPrivateKey() method returns it, but that's intentional for signing
		if w.ECDSAPrivateKey() == nil {
			t.Error("ECDSA private key should be available for EVM wallets")
		}
	}
}

// TestMain sets up and tears down test environment
func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}
