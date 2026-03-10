package domain

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// --- Test helpers: in-memory KeyManager and ShredStore ---

type inMemoryKeyManager struct {
	mu   sync.RWMutex
	keys map[uuid.UUID][]byte
}

func newInMemoryKeyManager() *inMemoryKeyManager {
	return &inMemoryKeyManager{keys: make(map[uuid.UUID][]byte)}
}

func (m *inMemoryKeyManager) SetDEK(tenantID uuid.UUID, dek []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[tenantID] = dek
}

func (m *inMemoryKeyManager) GetDEK(tenantID uuid.UUID) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dek, ok := m.keys[tenantID]
	if !ok {
		return nil, ErrTenantShredded
	}
	return dek, nil
}

func (m *inMemoryKeyManager) DeleteDEK(tenantID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, tenantID)
	return nil
}

type inMemoryShredStore struct {
	mu      sync.RWMutex
	records map[uuid.UUID]*TenantShredRecord
}

func newInMemoryShredStore() *inMemoryShredStore {
	return &inMemoryShredStore{records: make(map[uuid.UUID]*TenantShredRecord)}
}

func (s *inMemoryShredStore) MarkTenantShredded(_ context.Context, record *TenantShredRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.TenantID] = record
	return nil
}

func (s *inMemoryShredStore) IsTenantShredded(_ context.Context, tenantID uuid.UUID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[tenantID]
	return ok && r.Status == TenantShredStatusShredded, nil
}

func generateDEK(t *testing.T) []byte {
	t.Helper()
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatalf("generating DEK: %v", err)
	}
	return dek
}

// --- EncryptPII / DecryptPII ---

func TestEncryptDecryptPII(t *testing.T) {
	dek := generateDEK(t)
	plaintext := []byte("John Doe")

	ciphertext, err := EncryptPII(plaintext, dek)
	if err != nil {
		t.Fatalf("EncryptPII: %v", err)
	}

	// Ciphertext must differ from plaintext.
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}

	// Ciphertext must include the 12-byte nonce + at least 16-byte GCM tag.
	if len(ciphertext) < 12+16+len(plaintext) {
		t.Fatalf("ciphertext too short: got %d bytes", len(ciphertext))
	}

	decrypted, err := DecryptPII(ciphertext, dek)
	if err != nil {
		t.Fatalf("DecryptPII: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted text mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptPII_DifferentNonces(t *testing.T) {
	dek := generateDEK(t)
	plaintext := []byte("same input")

	c1, err := EncryptPII(plaintext, dek)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	c2, err := EncryptPII(plaintext, dek)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}

	// Two encryptions of the same plaintext must produce different ciphertexts
	// (because of random nonces).
	if bytes.Equal(c1, c2) {
		t.Fatal("two encryptions of the same plaintext should not be identical")
	}

	// But both must decrypt to the same plaintext.
	d1, _ := DecryptPII(c1, dek)
	d2, _ := DecryptPII(c2, dek)
	if !bytes.Equal(d1, d2) || !bytes.Equal(d1, plaintext) {
		t.Fatal("both ciphertexts should decrypt to the same plaintext")
	}
}

func TestEncryptPII_InvalidDEKSize(t *testing.T) {
	shortKey := make([]byte, 16) // AES-128, not AES-256
	_, err := EncryptPII([]byte("test"), shortKey)
	if err != ErrInvalidDEKSize {
		t.Fatalf("expected ErrInvalidDEKSize, got %v", err)
	}
}

func TestDecryptPII_InvalidDEKSize(t *testing.T) {
	_, err := DecryptPII(make([]byte, 50), make([]byte, 16))
	if err != ErrInvalidDEKSize {
		t.Fatalf("expected ErrInvalidDEKSize, got %v", err)
	}
}

func TestDecryptPII_CiphertextTooShort(t *testing.T) {
	dek := generateDEK(t)
	_, err := DecryptPII(make([]byte, 5), dek)
	if err != ErrCiphertextTooShort {
		t.Fatalf("expected ErrCiphertextTooShort, got %v", err)
	}
}

func TestDecryptPII_WrongKey(t *testing.T) {
	dek1 := generateDEK(t)
	dek2 := generateDEK(t)

	ciphertext, err := EncryptPII([]byte("secret"), dek1)
	if err != nil {
		t.Fatalf("EncryptPII: %v", err)
	}

	_, err = DecryptPII(ciphertext, dek2)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestEncryptPII_EmptyPlaintext(t *testing.T) {
	dek := generateDEK(t)
	ciphertext, err := EncryptPII([]byte(""), dek)
	if err != nil {
		t.Fatalf("EncryptPII empty: %v", err)
	}
	decrypted, err := DecryptPII(ciphertext, dek)
	if err != nil {
		t.Fatalf("DecryptPII empty: %v", err)
	}
	if string(decrypted) != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}

// --- PIIEncryptor ---

func TestPIIEncryptor_EncryptDecrypt(t *testing.T) {
	km := newInMemoryKeyManager()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)

	enc := NewPIIEncryptor(km)

	original := "Jane Smith"
	ciphertext, err := enc.Encrypt(tenantID, original)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := enc.Decrypt(tenantID, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if decrypted != original {
		t.Fatalf("got %q, want %q", decrypted, original)
	}
}

func TestPIIEncryptor_DecryptEmpty(t *testing.T) {
	km := newInMemoryKeyManager()
	enc := NewPIIEncryptor(km)

	result, err := enc.Decrypt(uuid.New(), nil)
	if err != nil {
		t.Fatalf("Decrypt nil: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string for nil ciphertext, got %q", result)
	}

	result, err = enc.Decrypt(uuid.New(), []byte{})
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty string for empty ciphertext, got %q", result)
	}
}

func TestPIIEncryptor_ShreddedTenantReturnsRedacted(t *testing.T) {
	km := newInMemoryKeyManager()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)

	enc := NewPIIEncryptor(km)

	// Encrypt some PII.
	ciphertext, err := enc.Encrypt(tenantID, "Sensitive Data")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Now delete the DEK (simulate crypto-shred).
	_ = km.DeleteDEK(tenantID)

	// Decrypt should return [REDACTED], not an error.
	result, err := enc.Decrypt(tenantID, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt after shred: %v", err)
	}
	if result != RedactedPII {
		t.Fatalf("expected %q after shred, got %q", RedactedPII, result)
	}
}

// --- Sender / Recipient encryption ---

func TestPIIEncryptor_SenderRoundTrip(t *testing.T) {
	km := newInMemoryKeyManager()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)
	enc := NewPIIEncryptor(km)

	sender := Sender{
		ID:      uuid.New(),
		Name:    "Alice Johnson",
		Email:   "alice@example.com",
		Country: "GB",
	}

	encrypted, err := enc.EncryptSender(tenantID, sender)
	if err != nil {
		t.Fatalf("EncryptSender: %v", err)
	}

	// Non-PII fields preserved.
	if encrypted.ID != sender.ID {
		t.Fatalf("ID mismatch: got %s, want %s", encrypted.ID, sender.ID)
	}
	if encrypted.Country != sender.Country {
		t.Fatalf("Country mismatch: got %s, want %s", encrypted.Country, sender.Country)
	}

	// PII fields should be encrypted (not plaintext).
	if string(encrypted.EncryptedName) == sender.Name {
		t.Fatal("EncryptedName should not equal plaintext Name")
	}

	decrypted, err := enc.DecryptSender(tenantID, encrypted)
	if err != nil {
		t.Fatalf("DecryptSender: %v", err)
	}

	if decrypted.Name != sender.Name {
		t.Fatalf("Name: got %q, want %q", decrypted.Name, sender.Name)
	}
	if decrypted.Email != sender.Email {
		t.Fatalf("Email: got %q, want %q", decrypted.Email, sender.Email)
	}
	if decrypted.Country != sender.Country {
		t.Fatalf("Country: got %q, want %q", decrypted.Country, sender.Country)
	}
}

func TestPIIEncryptor_RecipientRoundTrip(t *testing.T) {
	km := newInMemoryKeyManager()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)
	enc := NewPIIEncryptor(km)

	recipient := Recipient{
		Name:          "Bob Williams",
		AccountNumber: "12345678",
		SortCode:      "01-02-03",
		BankName:      "Barclays",
		Country:       "GB",
		IBAN:          "GB82WEST12345698765432",
	}

	encrypted, err := enc.EncryptRecipient(tenantID, recipient)
	if err != nil {
		t.Fatalf("EncryptRecipient: %v", err)
	}

	// Non-PII field preserved.
	if encrypted.Country != recipient.Country {
		t.Fatalf("Country mismatch")
	}

	decrypted, err := enc.DecryptRecipient(tenantID, encrypted)
	if err != nil {
		t.Fatalf("DecryptRecipient: %v", err)
	}

	if decrypted.Name != recipient.Name {
		t.Fatalf("Name: got %q, want %q", decrypted.Name, recipient.Name)
	}
	if decrypted.AccountNumber != recipient.AccountNumber {
		t.Fatalf("AccountNumber: got %q, want %q", decrypted.AccountNumber, recipient.AccountNumber)
	}
	if decrypted.SortCode != recipient.SortCode {
		t.Fatalf("SortCode: got %q, want %q", decrypted.SortCode, recipient.SortCode)
	}
	if decrypted.BankName != recipient.BankName {
		t.Fatalf("BankName: got %q, want %q", decrypted.BankName, recipient.BankName)
	}
	if decrypted.IBAN != recipient.IBAN {
		t.Fatalf("IBAN: got %q, want %q", decrypted.IBAN, recipient.IBAN)
	}
}

func TestPIIEncryptor_RecipientShreddedReturnsRedacted(t *testing.T) {
	km := newInMemoryKeyManager()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)
	enc := NewPIIEncryptor(km)

	recipient := Recipient{
		Name:          "Charlie Brown",
		AccountNumber: "99887766",
		SortCode:      "04-05-06",
		BankName:      "HSBC",
		Country:       "GB",
		IBAN:          "GB11CITI12345612345678",
	}

	encrypted, err := enc.EncryptRecipient(tenantID, recipient)
	if err != nil {
		t.Fatalf("EncryptRecipient: %v", err)
	}

	// Shred the tenant.
	_ = km.DeleteDEK(tenantID)

	decrypted, err := enc.DecryptRecipient(tenantID, encrypted)
	if err != nil {
		t.Fatalf("DecryptRecipient after shred: %v", err)
	}

	if decrypted.Name != RedactedPII {
		t.Fatalf("Name after shred: got %q, want %q", decrypted.Name, RedactedPII)
	}
	if decrypted.AccountNumber != RedactedPII {
		t.Fatalf("AccountNumber after shred: got %q, want %q", decrypted.AccountNumber, RedactedPII)
	}
	if decrypted.BankName != RedactedPII {
		t.Fatalf("BankName after shred: got %q, want %q", decrypted.BankName, RedactedPII)
	}
	// Country is not PII, should be preserved.
	if decrypted.Country != "GB" {
		t.Fatalf("Country after shred: got %q, want %q", decrypted.Country, "GB")
	}
}

// --- CryptoShredder ---

func TestCryptoShredder_ShredTenant(t *testing.T) {
	km := newInMemoryKeyManager()
	store := newInMemoryShredStore()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)

	shredder := NewCryptoShredder(km, store)

	// Encrypt some PII first.
	enc := NewPIIEncryptor(km)
	ciphertext, err := enc.Encrypt(tenantID, "Super Secret PII")
	if err != nil {
		t.Fatalf("Encrypt before shred: %v", err)
	}

	// Shred the tenant.
	err = shredder.ShredTenant(context.Background(), tenantID, "admin@settla.io", "GDPR erasure request #42")
	if err != nil {
		t.Fatalf("ShredTenant: %v", err)
	}

	// Verify DEK is gone.
	_, err = km.GetDEK(tenantID)
	if err != ErrTenantShredded {
		t.Fatalf("expected ErrTenantShredded after shred, got %v", err)
	}

	// Verify store records the shred.
	shredded, err := store.IsTenantShredded(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("IsTenantShredded: %v", err)
	}
	if !shredded {
		t.Fatal("expected tenant to be marked as shredded")
	}

	// Verify decryption returns [REDACTED].
	result, err := enc.Decrypt(tenantID, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt after shred: %v", err)
	}
	if result != RedactedPII {
		t.Fatalf("expected %q, got %q", RedactedPII, result)
	}
}

func TestCryptoShredder_IdempotentShred(t *testing.T) {
	km := newInMemoryKeyManager()
	store := newInMemoryShredStore()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)

	shredder := NewCryptoShredder(km, store)

	// First shred.
	err := shredder.ShredTenant(context.Background(), tenantID, "admin", "first request")
	if err != nil {
		t.Fatalf("first ShredTenant: %v", err)
	}

	// Second shred (idempotent, should not error).
	err = shredder.ShredTenant(context.Background(), tenantID, "admin", "duplicate request")
	if err != nil {
		t.Fatalf("second ShredTenant should be idempotent: %v", err)
	}
}

func TestCryptoShredder_ShredRecord(t *testing.T) {
	km := newInMemoryKeyManager()
	store := newInMemoryShredStore()
	tenantID := uuid.New()
	dek := generateDEK(t)
	km.SetDEK(tenantID, dek)

	shredder := NewCryptoShredder(km, store)

	err := shredder.ShredTenant(context.Background(), tenantID, "compliance-bot", "GDPR Art.17 request from tenant")
	if err != nil {
		t.Fatalf("ShredTenant: %v", err)
	}

	// Verify the record details.
	store.mu.RLock()
	record := store.records[tenantID]
	store.mu.RUnlock()

	if record == nil {
		t.Fatal("expected shred record to exist")
	}
	if record.Status != TenantShredStatusShredded {
		t.Fatalf("status: got %q, want %q", record.Status, TenantShredStatusShredded)
	}
	if record.ShreddedBy != "compliance-bot" {
		t.Fatalf("shreddedBy: got %q, want %q", record.ShreddedBy, "compliance-bot")
	}
	if record.Reason != "GDPR Art.17 request from tenant" {
		t.Fatalf("reason: got %q, want %q", record.Reason, "GDPR Art.17 request from tenant")
	}
	if record.ShreddedAt == nil {
		t.Fatal("ShreddedAt should not be nil")
	}
}

// --- Benchmarks ---

func BenchmarkEncryptPII(b *testing.B) {
	dek := make([]byte, 32)
	_, _ = rand.Read(dek)
	plaintext := []byte("John Doe, 12345678, Barclays")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = EncryptPII(plaintext, dek)
	}
}

func BenchmarkDecryptPII(b *testing.B) {
	dek := make([]byte, 32)
	_, _ = rand.Read(dek)
	plaintext := []byte("John Doe, 12345678, Barclays")
	ciphertext, _ := EncryptPII(plaintext, dek)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = DecryptPII(ciphertext, dek)
	}
}
