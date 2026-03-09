// Package wallet provides HD wallet management for Settla's blockchain operations.
// This file re-exports security-critical functions from keymgmt for convenience.
package wallet

import (
	"crypto/ecdsa"
	"crypto/ed25519"

	"github.com/tyler-smith/go-bip32"

	"github.com/intellect4all/settla/rail/wallet/keymgmt"
)

// SecureClearBytes zeros out sensitive data in memory using a technique
// that resists compiler optimizations that might remove the clearing.
// Re-exported from keymgmt for convenience.
func SecureClearBytes(b []byte) {
	keymgmt.SecureClearBytes(b)
}

// SecureZeroBIP32Key zeros out a BIP32 key's sensitive material.
// Re-exported from keymgmt for convenience.
func SecureZeroBIP32Key(k *bip32.Key) {
	keymgmt.SecureZeroBIP32Key(k)
}

// SecureZeroECDSA zeros out an ECDSA private key's sensitive material.
// Re-exported from keymgmt for convenience.
func SecureZeroECDSA(key *ecdsa.PrivateKey) {
	keymgmt.SecureZeroECDSA(key)
}

// SecureZeroEd25519 zeros out an Ed25519 private key.
// Re-exported from keymgmt for convenience.
func SecureZeroEd25519(key ed25519.PrivateKey) {
	keymgmt.SecureZeroEd25519(key)
}
