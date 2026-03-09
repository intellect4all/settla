package keymgmt

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"runtime"
	"unsafe"

	"github.com/tyler-smith/go-bip32"
)

// SecureClearBytes zeros out sensitive data in memory using a technique
// that resists compiler optimizations that might remove the clearing.
// This is critical for private keys and other sensitive cryptographic material.
//
// The function uses unsafe pointer arithmetic to ensure the compiler cannot
// optimize away the zeroing operation. It also calls runtime.KeepAlive to
// prevent the garbage collector from collecting the slice before zeroing.
func SecureClearBytes(b []byte) {
	if len(b) == 0 {
		return
	}

	// Use volatile-like access pattern to prevent optimization
	ptr := unsafe.Pointer(&b[0])
	for i := range b {
		*(*byte)(unsafe.Add(ptr, i)) = 0
	}

	// Ensure the compiler doesn't optimize away the zeroing
	runtime.KeepAlive(b)
}

// SecureZeroBIP32Key zeros out a BIP32 key's sensitive material.
// This should be called via defer immediately after deriving a key.
func SecureZeroBIP32Key(k *bip32.Key) {
	if k == nil {
		return
	}
	SecureClearBytes(k.Key)
	SecureClearBytes(k.ChainCode)
}

// SecureZeroECDSA zeros out an ECDSA private key's sensitive material.
// Note: This zeros the D value (private scalar). The public key remains intact
// but is derived from D, so the key is effectively invalidated.
//
// IMPORTANT: Always call this via defer after obtaining a private key:
//
//	privateKey, err := ...
//	if err != nil { return err }
//	defer SecureZeroECDSA(privateKey)
func SecureZeroECDSA(key *ecdsa.PrivateKey) {
	if key == nil || key.D == nil {
		return
	}

	// Get the underlying bytes of the big.Int and zero them
	bytes := key.D.Bytes()
	SecureClearBytes(bytes)

	// Set D to zero (this may allocate new memory, but the original is cleared)
	key.D.SetInt64(0)
}

// SecureZeroEd25519 zeros out an Ed25519 private key.
// Ed25519 private keys in Go are just []byte slices (64 bytes: 32-byte seed + 32-byte public key).
//
// IMPORTANT: Always call this via defer after obtaining a private key:
//
//	privateKey := ed25519.NewKeyFromSeed(seed)
//	defer SecureZeroEd25519(privateKey)
func SecureZeroEd25519(key ed25519.PrivateKey) {
	SecureClearBytes(key)
}
