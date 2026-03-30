# Chapter 10.2: PII Encryption -- Protecting Personal Data at Rest

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why application-level encryption is necessary beyond database-level encryption at rest
2. Trace the full encrypt-on-write, decrypt-on-read flow through the storage adapter
3. Describe the envelope encryption hierarchy (KMS master key, per-tenant DEK, encrypted PII)
4. Implement the crypto-shred pattern to satisfy both GDPR right-to-erasure and financial record retention
5. Design a key rotation scheme with zero-downtime backward compatibility

---

## Why Application-Level PII Encryption

Every transfer record in Settla contains personal data. A GBP-to-NGN transfer carries the sender's name and email, plus the recipient's name, bank account number, sort code, IBAN, and bank name. These are the Sender and Recipient structs from `domain/transfer.go`:

```go
// Sender identifies the party initiating the transfer.
type Sender struct {
    ID      uuid.UUID
    Name    string
    Email   string
    Country string
}

// Recipient identifies the party receiving funds.
type Recipient struct {
    Name          string
    AccountNumber string
    SortCode      string
    BankName      string
    Country       string
    IBAN          string
}
```

Three regulatory regimes demand protection for this data:

| Regulation | Requirement | Applies When |
|------------|-------------|--------------|
| GDPR (EU) | Personal data must be protected; individuals can request erasure | Any EU citizen is a sender or recipient |
| Travel Rule (FATF Rec 16) | Collect originator/beneficiary name, account, address for crypto transfers >$1,000 | Every stablecoin settlement Settla processes |
| PCI DSS / local banking regs | Protect account numbers and bank details | Every fiat leg of the transfer |

The naive response is "we encrypt the database." AWS offers EBS volume encryption (AES-256), and PostgreSQL supports TDE (transparent data encryption). Settla uses both. But they are not enough.

### The Problem with Disk Encryption

Disk encryption protects against one threat: physical theft of the storage medium. It does not protect against:

```
Threats that bypass disk encryption:
+-----------------------------------------------------------+
| 1. SQL injection        -- query returns decrypted rows   |
| 2. Compromised backup   -- pg_dump exports plaintext      |
| 3. Insider with DB creds-- psql shows all data            |
| 4. Memory dump          -- shared_buffers holds plaintext |
| 5. Log leakage          -- query logs capture PII values  |
+-----------------------------------------------------------+
```

When PostgreSQL reads a page from an encrypted EBS volume, the page is decrypted and placed into `shared_buffers` in plaintext. Every SELECT returns plaintext. Every `pg_dump` exports plaintext. Every query logged to CloudWatch contains plaintext. Disk encryption is a compliance checkbox, not a security boundary.

Application-level encryption solves this. The data is encrypted before it reaches PostgreSQL. The database stores opaque ciphertext. A `SELECT * FROM transfers` returns gibberish for PII fields. A backup contains gibberish. A log captures gibberish. The encryption key never touches the database server.

> **Key Insight:** Disk encryption protects data from the storage system. Application-level encryption protects data from the application's own database. In a breach, the attacker gets the database but not the encryption keys -- because the keys live in a separate system (AWS KMS).

---

## The Encryption Scheme: AES-256-GCM

Settla uses AES-256-GCM (Galois/Counter Mode) for all PII encryption. This is authenticated encryption, meaning it provides both confidentiality (the data is unreadable) and integrity (any tampering is detected). The choice is deliberate:

| Property | Why It Matters |
|----------|---------------|
| AES-256 | 256-bit key -- quantum-resistant for foreseeable future |
| GCM mode | Authenticated: detects bit-flip attacks on ciphertext |
| Random nonce | Each encryption produces different ciphertext for same plaintext |
| No padding | GCM is a stream cipher mode -- no padding oracle attacks |

The core encryption function lives in `domain/crypto.go`:

```go
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
```

The output format is a single byte slice:

```
+--------+-------------------+----------+
| Nonce  | Ciphertext        | GCM Tag  |
| 12 B   | len(plaintext) B  | 16 B     |
+--------+-------------------+----------+
         <-- gcm.Seal output ----------->
<-------- returned []byte --------------->
```

The `gcm.Seal(nonce, nonce, plaintext, nil)` call is doing something subtle. The first argument (`nonce`) is the destination prefix -- `Seal` appends the ciphertext to it. The second argument (`nonce`) is the nonce used for encryption. So the nonce is both the encryption parameter and the prefix of the output. This means the caller does not need to track the nonce separately -- it is always the first 12 bytes of the ciphertext.

Decryption reverses this by splitting the nonce from the sealed data:

```go
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
```

The `gcm.Open` call does two things: it verifies the GCM authentication tag (detecting any tampering) and then decrypts the ciphertext. If the tag verification fails, the function returns an error and zero plaintext. This is critical -- it means an attacker cannot flip bits in the ciphertext to change, say, a recipient account number without the corruption being detected.

---

## Envelope Encryption: The Key Hierarchy

A naive implementation would use a single encryption key for all PII across all tenants. This creates two problems:

1. **Blast radius.** If the key is compromised, all tenant data is exposed.
2. **Crypto-shred impossible.** You cannot delete one tenant's data by deleting a shared key.

Settla uses envelope encryption with per-tenant keys:

```
                    +------------------+
                    |   AWS KMS        |
                    |   Master Key     |  Never leaves KMS HSM
                    +--------+---------+
                             |
              KMS Encrypt/Decrypt API
                             |
         +-------------------+-------------------+
         |                   |                   |
  +------v------+    +------v------+    +------v------+
  | Tenant A    |    | Tenant B    |    | Tenant C    |
  | DEK (wrap)  |    | DEK (wrap)  |    | DEK (wrap)  |
  | 32 bytes    |    | 32 bytes    |    | 32 bytes    |
  +------+------+    +------+------+    +------+------+
         |                   |                   |
    AES-256-GCM         AES-256-GCM         AES-256-GCM
         |                   |                   |
  +------v------+    +------v------+    +------v------+
  | Sender name |    | Sender name |    | Sender name |
  | Sender email|    | Sender email|    | Sender email|
  | Recip name  |    | Recip name  |    | Recip name  |
  | Recip acct  |    | Recip acct  |    | Recip acct  |
  | Recip IBAN  |    | Recip IBAN  |    | Recip IBAN  |
  +-------------+    +-------------+    +-------------+
```

The hierarchy works as follows:

1. **KMS Master Key** -- lives inside the AWS KMS hardware security module (HSM). It never leaves KMS. Settla calls the KMS API to encrypt or decrypt DEKs, but the master key material is never exposed.

2. **Per-Tenant DEK (Data Encryption Key)** -- a 32-byte AES-256 key, unique per tenant. The DEK is stored in the database encrypted ("wrapped") by the KMS master key. When the application needs to encrypt or decrypt PII for a tenant, it calls KMS to unwrap the DEK, uses it in memory, then discards it.

3. **Encrypted PII** -- individual fields (sender name, recipient account number, etc.) encrypted with the tenant's DEK using AES-256-GCM.

The `KeyManager` interface abstracts this hierarchy:

```go
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
    GetDEKVersion(tenantID uuid.UUID, version int) ([]byte, error)

    // CurrentKeyVersion returns the latest key version for the given tenant.
    // New encryptions always use this version. Returns 1 for tenants that
    // have never had a key rotation.
    CurrentKeyVersion(tenantID uuid.UUID) (int, error)

    // DeleteDEK permanently destroys the tenant's DEK, making all PII
    // encrypted with it unrecoverable. This is the core of crypto-shred.
    DeleteDEK(tenantID uuid.UUID) error
}
```

Notice the version-awareness built into the interface. `GetDEK` returns the current (latest) key -- used for new encryptions. `GetDEKVersion` returns the key at a specific version -- used for decrypting data that was encrypted with an older key. This is essential for key rotation, which we cover later in this chapter.

---

## The PIIEncryptor: Field-Level Encryption

The raw `EncryptPII`/`DecryptPII` functions operate on byte slices. The `PIIEncryptor` struct provides higher-level operations that work with domain types and handle the KeyManager interaction:

```go
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
```

The `Encrypt` method gets the current DEK and encrypts a single field:

```go
// Encrypt encrypts a PII field value for the given tenant using the current
// (latest) key version.
func (e *PIIEncryptor) Encrypt(tenantID uuid.UUID, plaintext string) ([]byte, error) {
    dek, err := e.km.GetDEK(tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-domain: getting DEK for tenant %s: %w", tenantID, err)
    }
    return EncryptPII([]byte(plaintext), dek)
}
```

The `Decrypt` method handles the crypto-shred case. When a tenant's key has been destroyed, the method returns `"[REDACTED]"` instead of an error:

```go
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
```

The `DecryptWithVersion` method adds version-aware decryption for key rotation:

```go
// DecryptWithVersion decrypts a PII field value using the DEK at the specified
// key version. Version 0 means plaintext (returns the ciphertext bytes as-is).
// If the tenant has been crypto-shredded, returns RedactedPII.
func (e *PIIEncryptor) DecryptWithVersion(tenantID uuid.UUID, ciphertext []byte, version int) (string, error) {
    if len(ciphertext) == 0 {
        return "", nil
    }
    // Version 0 means plaintext -- no decryption needed.
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
```

Version 0 is the escape hatch. It means "this data is plaintext -- do not attempt decryption." This is how the system handles pre-encryption legacy data, as we will see in the backward compatibility section.

### Struct-Level Encryption

For convenience, the `PIIEncryptor` provides struct-level methods that encrypt or decrypt all PII fields in a Sender or Recipient at once. The encrypted versions use `[]byte` for PII fields while preserving non-PII fields (like Country) in plaintext:

```go
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
```

The `EncryptRecipient` method encrypts each PII field individually. Each field gets its own random nonce, so even if two recipients share the same bank name, the ciphertext differs:

```go
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
```

> **Key Insight:** Each PII field is encrypted independently with its own random nonce. This means you cannot tell whether two recipients have the same name by comparing ciphertexts. It also means a partial decryption failure (e.g., a corrupted `EncryptedIBAN`) does not prevent decrypting other fields.

---

## The Storage Adapter: Encrypt on Write, Decrypt on Read

The encryption logic is not scattered across the codebase. It lives in exactly one place: the `TransferStoreAdapter` in `store/transferdb/adapter.go`. The adapter sits between the domain layer and the database, transparently encrypting PII before INSERT and decrypting after SELECT.

The adapter holds an optional `PIIEncryptor`:

```go
// TransferStoreAdapter implements core.TransferStore using SQLC-generated queries
// against the Transfer DB.
type TransferStoreAdapter struct {
    q          *Queries
    pool       TxBeginner           // for transactional operations
    appPool    *pgxpool.Pool        // optional: RLS-enforced pool
    piiCrypto  *domain.PIIEncryptor // optional: encrypts PII before INSERT, decrypts after SELECT
    rlsEnabled bool
}
```

The encryptor is injected via a functional option:

```go
// WithPIIEncryptor configures the adapter to encrypt/decrypt PII fields
// (sender_name, sender_account, recipient_name, recipient_account, recipient_bank)
// using per-tenant DEKs via AES-256-GCM.
func WithPIIEncryptor(enc *domain.PIIEncryptor) TransferStoreOption {
    return func(a *TransferStoreAdapter) {
        a.piiCrypto = enc
    }
}
```

This is an optional dependency. When `piiCrypto` is nil (development/test), the adapter stores PII as plaintext JSON. When configured (production), PII is encrypted. The caller (in `cmd/settla-server/main.go`) decides:

```go
// Production setup:
km := kms.NewAWSKeyManager(kmsClient, masterKeyARN)
enc := domain.NewPIIEncryptor(km)
store := transferdb.NewTransferStoreAdapterWithOptions(queries,
    transferdb.WithTxPool(pool),
    transferdb.WithPIIEncryptor(enc),
)

// Development/test setup (no encryption):
store := transferdb.NewTransferStoreAdapterWithOptions(queries,
    transferdb.WithTxPool(pool),
)
```

### The Write Path: CreateTransfer

Here is the full write path from `adapter.go`. The adapter checks whether encryption is configured, encrypts the PII fields if so, and writes the result along with the key version:

```go
func (s *TransferStoreAdapter) CreateTransfer(ctx context.Context, transfer *domain.Transfer) error {
    feesJSON, err := json.Marshal(transfer.Fees)
    if err != nil {
        return fmt.Errorf("settla-core: marshalling fees: %w", err)
    }

    var senderJSON, recipientJSON []byte
    var encVersion int16

    if s.piiCrypto != nil {
        // Determine current key version for encryption.
        keyVer, err := s.piiCrypto.CurrentKeyVersion(transfer.TenantID)
        if err != nil {
            return fmt.Errorf("settla-core: getting current PII key version: %w", err)
        }
        encVersion = int16(keyVer)

        // Encrypt PII fields before storage.
        encSender, err := s.piiCrypto.EncryptSender(transfer.TenantID, transfer.Sender)
        if err != nil {
            return fmt.Errorf("settla-core: encrypting sender PII: %w", err)
        }
        senderJSON, err = json.Marshal(encSender)
        if err != nil {
            return fmt.Errorf("settla-core: marshalling encrypted sender: %w", err)
        }
        encRecipient, err := s.piiCrypto.EncryptRecipient(transfer.TenantID, transfer.Recipient)
        if err != nil {
            return fmt.Errorf("settla-core: encrypting recipient PII: %w", err)
        }
        recipientJSON, err = json.Marshal(encRecipient)
        if err != nil {
            return fmt.Errorf("settla-core: marshalling encrypted recipient: %w", err)
        }
    } else {
        // No encryption configured -- store plaintext (development/test only).
        // Version 0 indicates plaintext.
        encVersion = 0
        senderJSON, err = json.Marshal(transfer.Sender)
        if err != nil {
            return fmt.Errorf("settla-core: marshalling sender: %w", err)
        }
        recipientJSON, err = json.Marshal(transfer.Recipient)
        if err != nil {
            return fmt.Errorf("settla-core: marshalling recipient: %w", err)
        }
    }

    row, err := s.q.CreateTransfer(ctx, CreateTransferParams{
        // ... other fields ...
        Sender:               senderJSON,
        Recipient:            recipientJSON,
        PiiEncryptionVersion: encVersion,
    })
    // ...
}
```

The `pii_encryption_version` column is stored alongside the transfer row. This is critical for three reasons:

1. **Backward compatibility** -- version 0 means plaintext (pre-encryption data).
2. **Key rotation** -- the version tells the read path which DEK to use for decryption.
3. **Auditability** -- you can query how many rows are encrypted with each key version.

### The Read Path: transferFromRowWithDecrypt

Every query that returns transfers goes through `transferFromRowWithDecrypt`. This method first calls the basic `transferFromRow` (which parses non-PII fields), then conditionally decrypts PII:

```go
// transferFromRowWithDecrypt converts a database row to a domain.Transfer and
// decrypts PII fields if a PIIEncryptor is configured. When encrypted, the
// sender/recipient JSON contains EncryptedSender/EncryptedRecipient structures
// (with base64-encoded ciphertext). This method first tries to decrypt; if the
// JSON doesn't match the encrypted shape (e.g. pre-migration plaintext data),
// it falls back to the normal plaintext unmarshal.
func (s *TransferStoreAdapter) transferFromRowWithDecrypt(row Transfer) (*domain.Transfer, error) {
    t, err := transferFromRow(row)
    if err != nil {
        return nil, err
    }

    if s.piiCrypto == nil {
        return t, nil
    }

    keyVersion := int(row.PiiEncryptionVersion)

    // Version 0 means plaintext -- no decryption needed.
    // transferFromRow already parsed the JSON into t.Sender / t.Recipient.
    if keyVersion == 0 {
        return t, nil
    }

    // Try to decrypt sender PII from the raw JSON using the stored key version.
    if len(row.Sender) > 0 {
        var encSender domain.EncryptedSender
        if err := json.Unmarshal(row.Sender, &encSender); err == nil && len(encSender.EncryptedName) > 0 {
            sender, err := s.piiCrypto.DecryptSenderWithVersion(t.TenantID, &encSender, keyVersion)
            if err != nil {
                return nil, fmt.Errorf("settla-core: decrypting sender PII (v%d): %w", keyVersion, err)
            }
            t.Sender = sender
        }
        // If EncryptedName is empty, the data is plaintext (pre-encryption migration).
        // transferFromRow already handled it correctly.
    }

    // Try to decrypt recipient PII from the raw JSON using the stored key version.
    if len(row.Recipient) > 0 {
        var encRecipient domain.EncryptedRecipient
        if err := json.Unmarshal(row.Recipient, &encRecipient); err == nil && len(encRecipient.EncryptedName) > 0 {
            recipient, err := s.piiCrypto.DecryptRecipientWithVersion(t.TenantID, &encRecipient, keyVersion)
            if err != nil {
                return nil, fmt.Errorf("settla-core: decrypting recipient PII (v%d): %w", keyVersion, err)
            }
            t.Recipient = recipient
        }
    }

    return t, nil
}
```

The flow chart for the read path:

```
  SELECT from transfers
         |
         v
  transferFromRow(row)       -- parse all non-PII fields
         |
         v
  piiCrypto == nil?
    yes --> return t          -- dev mode, no decryption
    no  --> continue
         |
         v
  PiiEncryptionVersion == 0?
    yes --> return t          -- plaintext row (legacy)
    no  --> continue
         |
         v
  Unmarshal row.Sender into EncryptedSender
         |
  EncryptedName non-empty?
    yes --> DecryptSenderWithVersion(tenantID, encSender, version)
    no  --> keep plaintext (transferFromRow already parsed it)
         |
         v
  Unmarshal row.Recipient into EncryptedRecipient
         |
  EncryptedName non-empty?
    yes --> DecryptRecipientWithVersion(tenantID, encRecipient, version)
    no  --> keep plaintext
         |
         v
  return t (with decrypted PII)
```

This design means the domain layer (`core/`) never knows about encryption. The `Engine` works with plaintext `Sender` and `Recipient` structs. Encryption is purely a storage concern, handled at the adapter boundary.

---

## The Crypto-Shred Pattern (GDPR Right to Erasure)

GDPR Article 17 gives individuals the "right to be forgotten" -- they can request deletion of their personal data. But financial regulations (MLD5, PSD2, local banking laws) require keeping transaction records for 5-7 years. These two requirements appear to conflict:

```
  GDPR says:                Financial regulations say:
  +-----------------------+ +-----------------------------+
  | "Delete my personal   | | "Keep all transaction       |
  |  data when I ask."    | |  records for 7 years."      |
  +-----------------------+ +-----------------------------+
           |                           |
           +--------- CONFLICT --------+
```

Crypto-shredding resolves this conflict. The idea is simple: if you encrypt data with a key and then destroy the key, the data is permanently unreadable. It is effectively deleted, even though the ciphertext still exists on disk.

In Settla, this works at the tenant level:

```
  Before crypto-shred:                After crypto-shred:

  Transfer #12345                     Transfer #12345
  +----------------------------+      +----------------------------+
  | ID: abc-123                |      | ID: abc-123                |  <-- preserved
  | Amount: 1,000 GBP         |      | Amount: 1,000 GBP         |  <-- preserved
  | FX Rate: 1.27             |      | FX Rate: 1.27             |  <-- preserved
  | Status: COMPLETED         |      | Status: COMPLETED         |  <-- preserved
  | Created: 2025-01-15T14:30 |      | Created: 2025-01-15T14:30 |  <-- preserved
  | Sender: "Alice Smith"     |      | Sender: "[REDACTED]"      |  <-- unreadable
  | Email: "alice@example"    |      | Email: "[REDACTED]"       |  <-- unreadable
  | Recipient: "Bob Jones"    |      | Recipient: "[REDACTED]"   |  <-- unreadable
  | Account: "12345678"       |      | Account: "[REDACTED]"     |  <-- unreadable
  | IBAN: "GB82WEST..."       |      | IBAN: "[REDACTED]"        |  <-- unreadable
  +----------------------------+      +----------------------------+

  DEK for Tenant A: exists            DEK for Tenant A: DESTROYED
```

The transaction record survives -- amounts, timestamps, status, IDs are all in plaintext. The financial audit trail is intact. But the personal data (names, account numbers, IBANs) is permanently unreadable because the key that could decrypt it no longer exists.

### Implementation

The `KeyManager.DeleteDEK` method is the crypto-shred trigger:

```go
// DeleteDEK permanently destroys the tenant's DEK, making all PII
// encrypted with it unrecoverable. This is the core of crypto-shred.
DeleteDEK(tenantID uuid.UUID) error
```

After `DeleteDEK` is called, any subsequent call to `GetDEK` or `GetDEKVersion` for that tenant returns `ErrTenantShredded`:

```go
var ErrTenantShredded = errors.New("settla-domain: tenant PII has been crypto-shredded")
```

The `PIIEncryptor.Decrypt` method handles this gracefully:

```go
func (e *PIIEncryptor) Decrypt(tenantID uuid.UUID, ciphertext []byte) (string, error) {
    if len(ciphertext) == 0 {
        return "", nil
    }
    dek, err := e.km.GetDEK(tenantID)
    if err != nil {
        if errors.Is(err, ErrTenantShredded) {
            return RedactedPII, nil  // Returns "[REDACTED]"
        }
        return "", fmt.Errorf("settla-domain: getting DEK for tenant %s: %w", tenantID, err)
    }
    // ...
}
```

Notice that `ErrTenantShredded` is not treated as an error. The method returns `"[REDACTED]"` with a nil error. This means API responses for shredded tenants still work -- they just show `[REDACTED]` for PII fields. The caller does not need special handling.

The `RedactedPII` constant is defined as:

```go
const RedactedPII = "[REDACTED]"
```

### Why Not Just DELETE the Rows?

You might wonder: why not run `DELETE FROM transfers WHERE tenant_id = ?` and be done with it? Three reasons:

1. **Financial compliance.** Regulators can ask "how many GBP-to-NGN transfers did you process in Q3 2025?" The answer must be accurate. Deleting transfer rows breaks this.

2. **Ledger integrity.** The ledger entries reference transfer IDs. Deleting transfers creates dangling references and breaks the audit trail.

3. **Settlement reconciliation.** Net settlement records aggregate across transfers. Removing individual transfer records invalidates the aggregations.

Crypto-shredding preserves the financial record (the "what happened" and "how much") while destroying the personal data (the "who"). This is exactly what the regulatory frameworks intended.

> **Key Insight:** Crypto-shredding satisfies GDPR and financial retention simultaneously because it separates the identity of the parties from the financial facts of the transaction. The transaction happened, the amounts are real, the audit trail is intact -- but the names and account numbers behind it are gone forever.

---

## Backward Compatibility: The Version 0 Escape Hatch

When Settla first deploys PII encryption to an existing production system, the database already contains millions of transfer rows with plaintext sender/recipient JSON. A migration to encrypt all existing rows would be:

- **Slow.** 50 million rows at ~1ms per encryption = 14 hours.
- **Risky.** A failed migration leaves the database in an inconsistent state.
- **Unnecessary.** Old data can be encrypted lazily.

Instead, Settla uses a version-based approach with no migration required:

| `pii_encryption_version` | Meaning | Read Behavior |
|--------------------------|---------|---------------|
| 0 | Plaintext (pre-encryption or dev mode) | Parse JSON directly as Sender/Recipient |
| 1 | Encrypted with DEK version 1 | Decrypt with DEK v1 |
| 2 | Encrypted with DEK version 2 (after rotation) | Decrypt with DEK v2 |
| N | Encrypted with DEK version N | Decrypt with DEK vN |

The read path checks the version before attempting decryption:

```go
keyVersion := int(row.PiiEncryptionVersion)

// Version 0 means plaintext -- no decryption needed.
// transferFromRow already parsed the JSON into t.Sender / t.Recipient.
if keyVersion == 0 {
    return t, nil
}
```

And even for non-zero versions, the code has a fallback. If the JSON does not match the `EncryptedSender` shape (because it is plaintext), the `EncryptedName` field will be empty after unmarshal, and the code skips decryption:

```go
var encSender domain.EncryptedSender
if err := json.Unmarshal(row.Sender, &encSender); err == nil && len(encSender.EncryptedName) > 0 {
    sender, err := s.piiCrypto.DecryptSenderWithVersion(t.TenantID, &encSender, keyVersion)
    // ...
}
// If EncryptedName is empty, the data is plaintext (pre-encryption migration).
// transferFromRow already handled it correctly.
```

This double-check (version + shape) means the system handles three cases correctly:

1. **Version 0, plaintext JSON** -- the common case for legacy data. Skip decryption.
2. **Version N, encrypted JSON** -- the common case for new data. Decrypt with version N DEK.
3. **Version N, plaintext JSON** -- an edge case (should not happen, but defensively handled). The `EncryptedName` check prevents an attempt to decrypt plaintext.

The transition is gradual and invisible:

```
  Day 0: Enable encryption
  +-------------------------------------------------+
  | Existing rows: version=0, plaintext             |
  | New rows:      version=1, encrypted             |
  +-------------------------------------------------+

  Day 1-30: Mixed state
  +-------------------------------------------------+
  | Old rows:  version=0, plaintext  (read works)   |
  | New rows:  version=1, encrypted  (read works)   |
  +-------------------------------------------------+

  Day 30+: Background re-encryption (optional)
  +-------------------------------------------------+
  | All rows: version=1, encrypted                  |
  +-------------------------------------------------+
```

No downtime. No migration locks. No risk of half-encrypted data.

---

## Key Rotation

Encryption keys have a lifetime. Best practice is to rotate DEKs periodically (e.g., every 90 days) and immediately after any suspected compromise. Settla's version-aware design makes rotation straightforward.

### The Rotation Process

```
  Step 1: Generate new DEK (version N+1)
  +------------------------------------------+
  | KMS generates new 32-byte DEK            |
  | KMS wraps it with master key             |
  | Store wrapped DEK as version N+1         |
  | Update CurrentKeyVersion -> N+1          |
  +------------------------------------------+
           |
           v
  Step 2: Dual-key window (immediate)
  +------------------------------------------+
  | New writes: encrypt with DEK v(N+1)      |
  |   pii_encryption_version = N+1           |
  |                                          |
  | Old reads: decrypt with DEK v(N)         |
  |   pii_encryption_version = N             |
  |                                          |
  | Both keys are active simultaneously      |
  +------------------------------------------+
           |
           v
  Step 3: Background re-encryption (gradual)
  +------------------------------------------+
  | SELECT WHERE pii_encryption_version < N+1|
  | For each row:                            |
  |   1. Decrypt with old DEK (version N)    |
  |   2. Re-encrypt with new DEK (version N+1) |
  |   3. UPDATE row with new ciphertext      |
  |      and pii_encryption_version = N+1    |
  +------------------------------------------+
           |
           v
  Step 4: Retire old DEK (after all rows migrated)
  +------------------------------------------+
  | Verify: COUNT(*) WHERE                   |
  |   pii_encryption_version < N+1 = 0       |
  | Delete old DEK version N from KMS        |
  +------------------------------------------+
```

### Why Version-Aware Decryption Matters

During the dual-key window, the database contains rows encrypted with different key versions. The `DecryptWithVersion` method handles this by selecting the correct DEK:

```go
func (e *PIIEncryptor) DecryptWithVersion(tenantID uuid.UUID, ciphertext []byte, version int) (string, error) {
    if len(ciphertext) == 0 {
        return "", nil
    }
    // Version 0 means plaintext -- no decryption needed.
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
```

Without the version column, you would have to try every key version until one works (computationally expensive and error-prone). With the version column, you do exactly one KMS call and one decryption attempt per field.

### The Background Re-Encryption Job

The re-encryption job is a batch process that runs at low priority during off-peak hours. The pseudocode is:

```go
func ReEncryptBatch(ctx context.Context, store *TransferStoreAdapter, km KeyManager,
    tenantID uuid.UUID, targetVersion int, batchSize int) (int, error) {

    // Find rows still on old key versions.
    rows, err := store.q.ListTransfersByOldKeyVersion(ctx, ListTransfersByOldKeyVersionParams{
        TenantID:   tenantID,
        MaxVersion: int16(targetVersion - 1),
        Limit:      int32(batchSize),
    })
    if err != nil {
        return 0, err
    }

    migrated := 0
    for _, row := range rows {
        // Decrypt with old version.
        t, err := store.transferFromRowWithDecrypt(row)
        if err != nil {
            return migrated, err
        }

        // Re-encrypt with target version.
        encSender, _ := store.piiCrypto.EncryptSender(tenantID, t.Sender)
        encRecipient, _ := store.piiCrypto.EncryptRecipient(tenantID, t.Recipient)

        // Update row with new ciphertext and version.
        // ... UPDATE transfers SET sender=?, recipient=?, pii_encryption_version=? WHERE id=?
        migrated++
    }
    return migrated, nil
}
```

The job is idempotent -- re-encrypting an already-migrated row is a no-op because `ListTransfersByOldKeyVersion` filters by version. It is also safe to run concurrently with normal reads and writes because:

1. Reads always use the version stored in the row.
2. New writes always use the current (latest) version.
3. The UPDATE is atomic -- a reader sees either the old ciphertext+version or the new ciphertext+version, never a mix.

### Key Rotation Timeline

A typical rotation cycle for a production tenant:

| Time | Action | State |
|------|--------|-------|
| T+0 | Generate DEK v2, set as current | New writes: v2. Old reads: v1. |
| T+0 to T+7d | Background re-encryption runs | Mixed v1/v2, shrinking toward all v2. |
| T+7d | Verify zero rows at v1 | All rows at v2. |
| T+7d | Delete DEK v1 from KMS | v1 permanently unavailable. |

The 7-day window provides margin. If the re-encryption job has issues, you have time to fix it before the old key is destroyed.

---

## Where PII is NOT Stored

The encryption scheme only protects PII at the storage boundary. It is equally important to ensure PII does not leak into other systems:

| System | PII Present? | How It Is Prevented |
|--------|-------------|---------------------|
| TigerBeetle (ledger) | No | Ledger entries reference transfer IDs, never personal data |
| Redis (cache) | No | Cache keys use tenant_id and transfer_id, never PII fields |
| NATS messages | No | Event payloads contain IDs and amounts, never PII |
| Application logs | No (masked) | slog/pino formatters mask account numbers (`****4567`) and names (`J***`) |
| API responses | Yes (decrypted) | PII is decrypted in the adapter and included in API responses for authorized callers |
| Error messages | No | Error wrapping uses IDs, never PII values |

The adapter is the single point where PII enters and leaves the encrypted storage. This makes it auditable: you can grep the entire codebase for `piiCrypto` and find every place that touches encrypted data.

---

## Common Mistakes

1. **Using a single encryption key for all tenants.** This defeats the purpose of per-tenant isolation and makes crypto-shredding impossible. If Tenant A requests erasure, you cannot delete a shared key without destroying Tenant B's data.

2. **Encrypting the entire transfer row.** Only PII fields should be encrypted. If you encrypt amounts, timestamps, and status, you cannot query, aggregate, or reconcile transfers without decrypting every row. Settla encrypts only the `sender` and `recipient` JSONB columns.

3. **Forgetting to store the key version.** Without `pii_encryption_version`, you cannot implement key rotation or backward compatibility. You would have to try every key version until decryption succeeds -- a brute-force approach that fails at scale.

4. **Logging PII before encryption or after decryption.** A common leak vector is `slog.Info("creating transfer", "sender", transfer.Sender.Name)`. The encryption is useless if the plaintext ends up in CloudWatch. Settla's logging convention masks PII fields through custom formatters.

5. **Caching decrypted PII in Redis.** The adapter decrypts PII for API responses, but the decrypted values must never be written back to a cache. If Redis is compromised, the attacker would get plaintext PII despite the database being encrypted.

6. **Using ECB or CBC mode instead of GCM.** ECB reveals patterns in the plaintext (identical names produce identical ciphertext). CBC lacks authentication (an attacker can flip bits without detection). GCM provides both confidentiality and integrity.

7. **Reusing nonces with GCM.** GCM is catastrophically broken if the same nonce is reused with the same key. Settla uses `crypto/rand` for nonce generation, making collisions astronomically unlikely (12-byte nonce = 2^96 possible values).

8. **Treating decryption failure as a hard error during migration.** When enabling encryption on an existing system, old rows are plaintext. If the adapter treated decryption failure as fatal, every read of a legacy row would fail. The `EncryptedName` length check handles this gracefully.

---

## Exercises

1. **Calculate ciphertext overhead.** A recipient name "Alice Smith" is 11 bytes. After AES-256-GCM encryption, how many bytes is the ciphertext? Account for the nonce (12 bytes) and GCM tag (16 bytes). Then calculate the storage overhead for 50 million transfers per day, assuming each transfer has 6 encrypted PII fields averaging 20 bytes of plaintext each.

2. **Design a crypto-shred audit.** You receive a GDPR erasure request for Tenant X. Walk through the complete crypto-shred process: which API call triggers it, what happens in the KeyManager, how subsequent reads of Tenant X transfers behave, and how you would verify to the regulator that the erasure is complete.

3. **Spot the vulnerability.** Consider a system that encrypts PII with AES-256-GCM but stores the DEK in the same PostgreSQL database as the encrypted data (in a `tenant_keys` table). Why is this insufficient, and how does Settla's KMS-based approach fix the problem?

4. **Key rotation query.** Write a SQL query that reports the number of transfer rows per tenant per encryption key version. This query would be used by an operator to monitor the progress of a background re-encryption job. What index would you add to make this query efficient?

5. **Extend the schema.** The current `PIIEncryptor` encrypts Sender and Recipient fields. Suppose a new regulation requires encrypting the sender's IP address (currently logged but not stored in the transfer record). Describe the changes needed across the domain types, the `PIIEncryptor`, the `EncryptedSender` struct, and the adapter. Which invariant from the codebase ensures you do not break existing serialization?

6. **Failure mode analysis.** KMS has a brief outage (2 minutes). During this window, what happens to: (a) new transfer creation (the write path), (b) transfer reads (the read path), (c) the background re-encryption job? Which of these should retry and which should fail fast? How would you implement the retry strategy without blocking the hot path?

---

## What's Next

In the next chapter, we will examine Settla's approach to API key management and authentication security -- how keys are generated, stored as HMAC hashes, rotated, and revoked across a distributed gateway fleet. We will see how the three-level auth cache from Chapter 6.4 integrates with the broader security posture.
