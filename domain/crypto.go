package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// PIIEncryptionErrors.
var (
	ErrInvalidDEKSize     = errors.New("settla-domain: DEK must be 32 bytes (AES-256)")
	ErrCiphertextTooShort = errors.New("settla-domain: ciphertext too short (must include nonce)")
	ErrTenantShredded     = errors.New("settla-domain: tenant PII has been crypto-shredded")
)

// RedactedPII is the placeholder returned when a tenant has been crypto-shredded.
const RedactedPII = "[REDACTED]"

// EncryptPII encrypts plaintext using AES-256-GCM with the given per-tenant
// data encryption key (DEK). The 12-byte random nonce is prepended to the
// ciphertext, so the returned slice is nonce + ciphertext + GCM tag.
//
// The DEK must be exactly 32 bytes for AES-256.
func EncryptPII(plaintext []byte, tenantDEK []byte) ([]byte, error) {
	if len(tenantDEK) != 32 {
		return nil, ErrInvalidDEKSize
	}

	block, err := aes.NewCipher(tenantDEK)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("settla-domain: generating nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce, so result = nonce || ciphertext || tag.
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// DecryptPII decrypts ciphertext produced by EncryptPII. It expects the
// 12-byte nonce prepended to the ciphertext+tag.
func DecryptPII(ciphertext []byte, tenantDEK []byte) ([]byte, error) {
	if len(tenantDEK) != 32 {
		return nil, ErrInvalidDEKSize
	}

	block, err := aes.NewCipher(tenantDEK)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrCiphertextTooShort
	}

	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: decrypting PII: %w", err)
	}

	return plaintext, nil
}

// KeyManager abstracts access to per-tenant data encryption keys (DEKs).
// In production this is backed by AWS KMS envelope encryption; the DEK is
// stored encrypted (wrapped) and unwrapped on demand.
type KeyManager interface {
	// GetDEK returns the plaintext DEK for the given tenant using the current
	// (latest) key version. Returns ErrTenantShredded if the tenant's key has
	// been destroyed.
	GetDEK(tenantID uuid.UUID) ([]byte, error)

	// GetDEKVersion returns the plaintext DEK for the given tenant at the
	// specified key version. This is used during decryption to select the
	// correct key that was active when the data was encrypted. Version 0
	// means plaintext (no decryption needed). Returns ErrTenantShredded if
	// the tenant's key has been destroyed.
	// Implementations that do not support versioning may ignore the version
	// parameter and return the current DEK (backward-compatible).
	GetDEKVersion(tenantID uuid.UUID, version int) ([]byte, error)

	// CurrentKeyVersion returns the latest key version for the given tenant.
	// New encryptions always use this version. Returns 1 for tenants that
	// have never had a key rotation.
	CurrentKeyVersion(tenantID uuid.UUID) (int, error)

	// DeleteDEK permanently destroys the tenant's DEK, making all PII
	// encrypted with it unrecoverable. This is the core of crypto-shred.
	DeleteDEK(tenantID uuid.UUID) error
}

// PIIEncryptor provides encrypt/decrypt operations for PII fields,
// scoped to a specific tenant. It uses the KeyManager to obtain DEKs
// and handles the shredded-tenant case by returning RedactedPII.
type PIIEncryptor struct {
	km KeyManager
}

// NewPIIEncryptor creates a PIIEncryptor backed by the given KeyManager.
func NewPIIEncryptor(km KeyManager) *PIIEncryptor {
	return &PIIEncryptor{km: km}
}

// Encrypt encrypts a PII field value for the given tenant using the current
// (latest) key version.
func (e *PIIEncryptor) Encrypt(tenantID uuid.UUID, plaintext string) ([]byte, error) {
	dek, err := e.km.GetDEK(tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: getting DEK for tenant %s: %w", tenantID, err)
	}
	return EncryptPII([]byte(plaintext), dek)
}

// CurrentKeyVersion returns the current (latest) encryption key version for the
// tenant. New transfers are always encrypted with this version.
func (e *PIIEncryptor) CurrentKeyVersion(tenantID uuid.UUID) (int, error) {
	return e.km.CurrentKeyVersion(tenantID)
}

// Decrypt decrypts a PII field value for the given tenant using the current key.
// If the tenant has been crypto-shredded, returns RedactedPII.
func (e *PIIEncryptor) Decrypt(tenantID uuid.UUID, ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	dek, err := e.km.GetDEK(tenantID)
	if err != nil {
		if errors.Is(err, ErrTenantShredded) {
			return RedactedPII, nil
		}
		return "", fmt.Errorf("settla-domain: getting DEK for tenant %s: %w", tenantID, err)
	}
	plaintext, err := DecryptPII(ciphertext, dek)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// DecryptWithVersion decrypts a PII field value using the DEK at the specified
// key version. Version 0 means plaintext (returns the ciphertext bytes as-is).
// If the tenant has been crypto-shredded, returns RedactedPII.
func (e *PIIEncryptor) DecryptWithVersion(tenantID uuid.UUID, ciphertext []byte, version int) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	// Version 0 means plaintext — no decryption needed.
	if version == 0 {
		return string(ciphertext), nil
	}
	dek, err := e.km.GetDEKVersion(tenantID, version)
	if err != nil {
		if errors.Is(err, ErrTenantShredded) {
			return RedactedPII, nil
		}
		return "", fmt.Errorf("settla-domain: getting DEK v%d for tenant %s: %w", version, tenantID, err)
	}
	plaintext, err := DecryptPII(ciphertext, dek)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// EncryptSender encrypts all PII fields in a Sender struct, returning
// the encrypted field values. Non-PII fields (ID, Country) are left as-is.
func (e *PIIEncryptor) EncryptSender(tenantID uuid.UUID, s Sender) (*EncryptedSender, error) {
	nameEnc, err := e.Encrypt(tenantID, s.Name)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting sender name: %w", err)
	}
	emailEnc, err := e.Encrypt(tenantID, s.Email)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting sender email: %w", err)
	}
	return &EncryptedSender{
		ID:             s.ID,
		EncryptedName:  nameEnc,
		EncryptedEmail: emailEnc,
		Country:        s.Country,
	}, nil
}

// DecryptSender decrypts an EncryptedSender back to a Sender.
// If the tenant is shredded, PII fields will contain "[REDACTED]".
func (e *PIIEncryptor) DecryptSender(tenantID uuid.UUID, es *EncryptedSender) (Sender, error) {
	name, err := e.Decrypt(tenantID, es.EncryptedName)
	if err != nil {
		return Sender{}, fmt.Errorf("settla-domain: decrypting sender name: %w", err)
	}
	email, err := e.Decrypt(tenantID, es.EncryptedEmail)
	if err != nil {
		return Sender{}, fmt.Errorf("settla-domain: decrypting sender email: %w", err)
	}
	return Sender{
		ID:      es.ID,
		Name:    name,
		Email:   email,
		Country: es.Country,
	}, nil
}

// DecryptSenderWithVersion decrypts an EncryptedSender using the DEK at the
// specified key version. Version 0 means plaintext.
func (e *PIIEncryptor) DecryptSenderWithVersion(tenantID uuid.UUID, es *EncryptedSender, version int) (Sender, error) {
	name, err := e.DecryptWithVersion(tenantID, es.EncryptedName, version)
	if err != nil {
		return Sender{}, fmt.Errorf("settla-domain: decrypting sender name (v%d): %w", version, err)
	}
	email, err := e.DecryptWithVersion(tenantID, es.EncryptedEmail, version)
	if err != nil {
		return Sender{}, fmt.Errorf("settla-domain: decrypting sender email (v%d): %w", version, err)
	}
	return Sender{
		ID:      es.ID,
		Name:    name,
		Email:   email,
		Country: es.Country,
	}, nil
}

// EncryptRecipient encrypts all PII fields in a Recipient struct.
// Non-PII fields (Country) are left as-is.
func (e *PIIEncryptor) EncryptRecipient(tenantID uuid.UUID, r Recipient) (*EncryptedRecipient, error) {
	nameEnc, err := e.Encrypt(tenantID, r.Name)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting recipient name: %w", err)
	}
	accountEnc, err := e.Encrypt(tenantID, r.AccountNumber)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting recipient account: %w", err)
	}
	sortCodeEnc, err := e.Encrypt(tenantID, r.SortCode)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting recipient sort code: %w", err)
	}
	bankNameEnc, err := e.Encrypt(tenantID, r.BankName)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting recipient bank name: %w", err)
	}
	ibanEnc, err := e.Encrypt(tenantID, r.IBAN)
	if err != nil {
		return nil, fmt.Errorf("settla-domain: encrypting recipient IBAN: %w", err)
	}
	return &EncryptedRecipient{
		EncryptedName:          nameEnc,
		EncryptedAccountNumber: accountEnc,
		EncryptedSortCode:      sortCodeEnc,
		EncryptedBankName:      bankNameEnc,
		Country:                r.Country,
		EncryptedIBAN:          ibanEnc,
	}, nil
}

// DecryptRecipient decrypts an EncryptedRecipient back to a Recipient.
// If the tenant is shredded, PII fields will contain "[REDACTED]".
func (e *PIIEncryptor) DecryptRecipient(tenantID uuid.UUID, er *EncryptedRecipient) (Recipient, error) {
	name, err := e.Decrypt(tenantID, er.EncryptedName)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient name: %w", err)
	}
	account, err := e.Decrypt(tenantID, er.EncryptedAccountNumber)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient account: %w", err)
	}
	sortCode, err := e.Decrypt(tenantID, er.EncryptedSortCode)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient sort code: %w", err)
	}
	bankName, err := e.Decrypt(tenantID, er.EncryptedBankName)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient bank name: %w", err)
	}
	iban, err := e.Decrypt(tenantID, er.EncryptedIBAN)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient IBAN: %w", err)
	}
	return Recipient{
		Name:          name,
		AccountNumber: account,
		SortCode:      sortCode,
		BankName:      bankName,
		Country:       er.Country,
		IBAN:          iban,
	}, nil
}

// DecryptRecipientWithVersion decrypts an EncryptedRecipient using the DEK at
// the specified key version. Version 0 means plaintext.
func (e *PIIEncryptor) DecryptRecipientWithVersion(tenantID uuid.UUID, er *EncryptedRecipient, version int) (Recipient, error) {
	name, err := e.DecryptWithVersion(tenantID, er.EncryptedName, version)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient name (v%d): %w", version, err)
	}
	account, err := e.DecryptWithVersion(tenantID, er.EncryptedAccountNumber, version)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient account (v%d): %w", version, err)
	}
	sortCode, err := e.DecryptWithVersion(tenantID, er.EncryptedSortCode, version)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient sort code (v%d): %w", version, err)
	}
	bankName, err := e.DecryptWithVersion(tenantID, er.EncryptedBankName, version)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient bank name (v%d): %w", version, err)
	}
	iban, err := e.DecryptWithVersion(tenantID, er.EncryptedIBAN, version)
	if err != nil {
		return Recipient{}, fmt.Errorf("settla-domain: decrypting recipient IBAN (v%d): %w", version, err)
	}
	return Recipient{
		Name:          name,
		AccountNumber: account,
		SortCode:      sortCode,
		BankName:      bankName,
		Country:       er.Country,
		IBAN:          iban,
	}, nil
}

// EncryptedSender holds the encrypted form of Sender PII fields.
// Non-PII fields (ID, Country) remain in plaintext.
type EncryptedSender struct {
	ID             uuid.UUID `json:"id"`
	EncryptedName  []byte    `json:"name"`
	EncryptedEmail []byte    `json:"email"`
	Country        string    `json:"country"`
}

// EncryptedRecipient holds the encrypted form of Recipient PII fields.
// Non-PII fields (Country) remain in plaintext.
type EncryptedRecipient struct {
	EncryptedName          []byte `json:"name"`
	EncryptedAccountNumber []byte `json:"account_number"`
	EncryptedSortCode      []byte `json:"sort_code"`
	EncryptedBankName      []byte `json:"bank_name"`
	Country                string `json:"country"`
	EncryptedIBAN          []byte `json:"iban"`
}
