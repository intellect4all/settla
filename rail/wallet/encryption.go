package wallet

import (
	"github.com/intellect4all/settla/rail/wallet/keymgmt"
)

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// The nonce is prepended to the ciphertext and the result is base64-encoded.
// Re-exported from keymgmt for convenience.
func Encrypt(plaintext []byte, key []byte) (string, error) {
	return keymgmt.Encrypt(plaintext, key)
}

// Decrypt decrypts a base64-encoded ciphertext that was encrypted with Encrypt.
// Re-exported from keymgmt for convenience.
func Decrypt(ciphertext string, key []byte) ([]byte, error) {
	return keymgmt.Decrypt(ciphertext, key)
}
