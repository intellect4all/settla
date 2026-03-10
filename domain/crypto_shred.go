package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TenantShredStatus represents whether a tenant's PII has been crypto-shredded.
type TenantShredStatus string

const (
	// TenantShredStatusNone indicates the tenant's PII is intact.
	TenantShredStatusNone TenantShredStatus = "NONE"
	// TenantShredStatusShredded indicates the tenant's DEK has been destroyed.
	TenantShredStatusShredded TenantShredStatus = "SHREDDED"
)

// TenantShredRecord records that a tenant's PII has been crypto-shredded.
type TenantShredRecord struct {
	TenantID    uuid.UUID
	Status      TenantShredStatus
	ShreddedAt  *time.Time
	ShreddedBy  string // operator or system identifier
	Reason      string // e.g. "GDPR erasure request #12345"
}

// ShredStore persists crypto-shred records. Implementations live in
// store/transferdb (same bounded context as tenant data).
type ShredStore interface {
	// MarkTenantShredded records that a tenant's PII has been crypto-shredded.
	MarkTenantShredded(ctx context.Context, record *TenantShredRecord) error

	// IsTenantShredded returns true if the tenant has been crypto-shredded.
	IsTenantShredded(ctx context.Context, tenantID uuid.UUID) (bool, error)
}

// CryptoShredder is a domain service that orchestrates the crypto-shred procedure
// for GDPR erasure. It uses domain-defined interfaces (KeyManager, ShredStore)
// whose implementations live in the infrastructure layer.
// It deletes the tenant's DEK from the KeyManager and records the shred
// in the ShredStore, making all PII for that tenant permanently unrecoverable
// while preserving the financial transaction records for the 7-year
// regulatory retention period.
type CryptoShredder struct {
	km    KeyManager
	store ShredStore
}

// NewCryptoShredder creates a CryptoShredder.
func NewCryptoShredder(km KeyManager, store ShredStore) *CryptoShredder {
	return &CryptoShredder{km: km, store: store}
}

// ShredTenant permanently destroys the tenant's PII encryption key and
// records the shred event. After this call:
//   - All reads of encrypted PII for this tenant will return "[REDACTED]"
//   - Financial records (amounts, timestamps, status) remain intact
//   - The operation is irreversible
//
// Parameters:
//   - ctx: context for cancellation/deadline
//   - tenantID: the tenant whose PII should be shredded
//   - operator: identifier of the person/system initiating the shred
//   - reason: audit reason (e.g. "GDPR erasure request #12345")
func (cs *CryptoShredder) ShredTenant(ctx context.Context, tenantID uuid.UUID, operator, reason string) error {
	// Check if already shredded (idempotent).
	shredded, err := cs.store.IsTenantShredded(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settla-domain: checking shred status for tenant %s: %w", tenantID, err)
	}
	if shredded {
		// Already shredded — idempotent success.
		return nil
	}

	// Step 1: Delete the DEK from the KeyManager (KMS).
	// This is the irreversible step that makes PII unrecoverable.
	if err := cs.km.DeleteDEK(tenantID); err != nil {
		return fmt.Errorf("settla-domain: deleting DEK for tenant %s: %w", tenantID, err)
	}

	// Step 2: Record the shred in the database for audit trail.
	now := time.Now().UTC()
	record := &TenantShredRecord{
		TenantID:   tenantID,
		Status:     TenantShredStatusShredded,
		ShreddedAt: &now,
		ShreddedBy: operator,
		Reason:     reason,
	}
	if err := cs.store.MarkTenantShredded(ctx, record); err != nil {
		// DEK is already deleted — log this as a critical inconsistency.
		// The PII is already unrecoverable, but we failed to record it.
		return fmt.Errorf("settla-domain: recording shred for tenant %s (DEK already deleted): %w", tenantID, err)
	}

	return nil
}
