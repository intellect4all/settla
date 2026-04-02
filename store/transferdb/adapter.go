package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// TxBeginner abstracts the ability to begin a pgx transaction.
// *pgxpool.Pool satisfies this interface.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// TxBeginnerWithOptions extends TxBeginner with the ability to begin a
// transaction with explicit isolation options. *pgxpool.Pool satisfies this
// interface. Used by outbox operations to request REPEATABLE READ isolation,
// which prevents phantom reads during the concurrent UPDATE + INSERT pattern
// used by TransitionWithOutbox and CreateTransferWithOutbox.
type TxBeginnerWithOptions interface {
	TxBeginner
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Compile-time interface checks.
var (
	_ core.TransferStore = (*TransferStoreAdapter)(nil)
	_ core.TenantStore   = (*TenantStoreAdapter)(nil)
)

// TransferStoreAdapter implements core.TransferStore using SQLC-generated queries
// against the Transfer DB.
type TransferStoreAdapter struct {
	q          *Queries
	pool       TxBeginner           // for transactional operations (TransitionWithOutbox, CreateTransferWithOutbox)
	appPool    *pgxpool.Pool        // optional: RLS-enforced pool (settla_app role) for tenant-scoped reads
	piiCrypto  *domain.PIIEncryptor // optional: encrypts PII before INSERT, decrypts after SELECT
	rlsEnabled bool                 // true when appPool is configured; false means RLS is bypassed
}

// TransferStoreOption configures optional features of the TransferStoreAdapter.
type TransferStoreOption func(*TransferStoreAdapter)

// WithPIIEncryptor configures the adapter to encrypt/decrypt PII fields
// (sender_name, sender_account, recipient_name, recipient_account, recipient_bank)
// using per-tenant DEKs via AES-256-GCM.
func WithPIIEncryptor(enc *domain.PIIEncryptor) TransferStoreOption {
	return func(a *TransferStoreAdapter) {
		a.piiCrypto = enc
	}
}

// WithTxPool configures the adapter with a transaction pool for outbox operations.
func WithTxPool(pool TxBeginner) TransferStoreOption {
	return func(a *TransferStoreAdapter) {
		a.pool = pool
	}
}

// WithAppPool configures the adapter with an RLS-enforced pool (settla_app role)
// for tenant-scoped read operations. When set, tenant-scoped reads use this pool
// with SET LOCAL app.current_tenant_id for RLS enforcement.
// When nil, falls back to the owner pool (no RLS).
func WithAppPool(pool *pgxpool.Pool) TransferStoreOption {
	return func(a *TransferStoreAdapter) {
		a.appPool = pool
	}
}


// NewTransferStoreAdapter creates a new TransferStoreAdapter.
// The pool parameter is optional — pass nil if transactional outbox methods are not needed.
func NewTransferStoreAdapter(q *Queries, pool ...TxBeginner) *TransferStoreAdapter {
	a := &TransferStoreAdapter{q: q}
	if len(pool) > 0 && pool[0] != nil {
		a.pool = pool[0]
	}
	a.rlsEnabled = a.appPool != nil
	if !a.rlsEnabled {
		slog.Warn("settla-store: RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// NewTransferStoreAdapterWithOptions creates a TransferStoreAdapter with functional options.
func NewTransferStoreAdapterWithOptions(q *Queries, opts ...TransferStoreOption) *TransferStoreAdapter {
	a := &TransferStoreAdapter{q: q}
	for _, opt := range opts {
		opt(a)
	}
	a.rlsEnabled = a.appPool != nil
	if !a.rlsEnabled {
		slog.Warn("settla-store: RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

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
		// No encryption configured — store plaintext (development/test only).
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
		TenantID:             transfer.TenantID,
		ExternalRef:          textFromString(transfer.ExternalRef),
		IdempotencyKey:       textFromString(transfer.IdempotencyKey),
		Status:               TransferStatusEnum(transfer.Status),
		SourceCurrency:       string(transfer.SourceCurrency),
		SourceAmount:         numericFromDecimal(transfer.SourceAmount),
		DestCurrency:         string(transfer.DestCurrency),
		DestAmount:           numericFromDecimal(transfer.DestAmount),
		StableCoin:           textFromString(string(transfer.StableCoin)),
		StableAmount:         numericFromDecimal(transfer.StableAmount),
		Chain:                textFromString(string(transfer.Chain)),
		FxRate:               numericFromDecimal(transfer.FXRate),
		Fees:                 feesJSON,
		Sender:               senderJSON,
		Recipient:            recipientJSON,
		QuoteID:              uuidFromPtr(transfer.QuoteID),
		OnRampProviderID:     textFromString(transfer.OnRampProviderID),
		OffRampProviderID:    textFromString(transfer.OffRampProviderID),
		PiiEncryptionVersion: encVersion,
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating transfer: %w", err)
	}

	transfer.ID = row.ID
	transfer.Version = row.Version
	transfer.CreatedAt = row.CreatedAt
	transfer.UpdatedAt = row.UpdatedAt
	return nil
}

func (s *TransferStoreAdapter) GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error) {
	// When tenantID is uuid.Nil, look up by ID only (used by worker pipeline
	// where tenant_id comes from the trusted NATS event payload).
	// SECURITY: This path bypasses tenant isolation — never call with uuid.Nil
	// from API-facing code. The RLS pool is not used here because workers run
	// as the database owner, not the tenant-scoped settla_app role.
	if tenantID == uuid.Nil {
		row, err := s.q.GetTransferByID(ctx, transferID)
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer %s: %w", transferID, err)
		}
		return s.transferFromRowWithDecrypt(row)
	}

	// Use RLS-enforced pool when available.
	if s.appPool != nil {
		var result *domain.Transfer
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetTransfer(ctx, GetTransferParams{
				ID:       transferID,
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}
			result, err = s.transferFromRowWithDecrypt(row)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer %s: %w", transferID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransfer", "tenant_id", tenantID)
	row, err := s.q.GetTransfer(ctx, GetTransferParams{
		ID:       transferID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer %s: %w", transferID, err)
	}
	return s.transferFromRowWithDecrypt(row)
}

func (s *TransferStoreAdapter) GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error) {
	if s.appPool != nil {
		var result *domain.Transfer
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetTransferByIdempotencyKey(ctx, GetTransferByIdempotencyKeyParams{
				TenantID:       tenantID,
				IdempotencyKey: textFromString(key),
			})
			if err != nil {
				return err
			}
			result, err = s.transferFromRowWithDecrypt(row)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer by idempotency key: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferByIdempotencyKey", "tenant_id", tenantID)
	row, err := s.q.GetTransferByIdempotencyKey(ctx, GetTransferByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: textFromString(key),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer by idempotency key: %w", err)
	}
	return s.transferFromRowWithDecrypt(row)
}

func (s *TransferStoreAdapter) GetTransferByExternalRef(ctx context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error) {
	if s.appPool != nil {
		var result *domain.Transfer
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetTransferByExternalRef(ctx, GetTransferByExternalRefParams{
				TenantID:    tenantID,
				ExternalRef: textFromString(externalRef),
			})
			if err != nil {
				return err
			}
			result, err = s.transferFromRowWithDecrypt(row)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer by external ref: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferByExternalRef", "tenant_id", tenantID)
	row, err := s.q.GetTransferByExternalRef(ctx, GetTransferByExternalRefParams{
		TenantID:    tenantID,
		ExternalRef: textFromString(externalRef),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer by external ref: %w", err)
	}
	return s.transferFromRowWithDecrypt(row)
}

func (s *TransferStoreAdapter) UpdateTransfer(ctx context.Context, transfer *domain.Transfer) error {
	// Use optimistic lock: only update if version matches
	err := s.q.UpdateTransferStatusWithVersion(ctx, UpdateTransferStatusWithVersionParams{
		ID:       transfer.ID,
		TenantID: transfer.TenantID,
		Status:   TransferStatusEnum(transfer.Status),
		Version:  transfer.Version - 1, // TransitionTo already incremented Version
	})
	if err != nil {
		return fmt.Errorf("settla-core: updating transfer %s: %w", transfer.ID, err)
	}
	return nil
}

func (s *TransferStoreAdapter) CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error {
	metadataJSON, err := json.Marshal(event.Metadata)
	if err != nil {
		slog.Warn("settla-store: failed to marshal event metadata", "transfer_id", event.TransferID, "error", err)
	}
	_, err = s.q.CreateTransferEvent(ctx, CreateTransferEventParams{
		TransferID:  event.TransferID,
		TenantID:    event.TenantID,
		FromStatus:  textFromString(string(event.FromStatus)),
		ToStatus:    string(event.ToStatus),
		Metadata:    metadataJSON,
		ProviderRef: textFromString(event.ProviderRef),
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating transfer event: %w", err)
	}
	return nil
}

func (s *TransferStoreAdapter) GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error) {
	parseEvents := func(rows []TransferEvent) []domain.TransferEvent {
		events := make([]domain.TransferEvent, len(rows))
		for i, row := range rows {
			events[i] = domain.TransferEvent{
				ID:          row.ID,
				TransferID:  row.TransferID,
				TenantID:    row.TenantID,
				FromStatus:  domain.TransferStatus(row.FromStatus.String),
				ToStatus:    domain.TransferStatus(row.ToStatus),
				OccurredAt:  row.OccurredAt,
				ProviderRef: row.ProviderRef.String,
			}
			if row.Metadata != nil {
				if err := json.Unmarshal(row.Metadata, &events[i].Metadata); err != nil {
					slog.Warn("settla-store: failed to unmarshal event metadata", "event_id", row.ID, "error", err)
				}
			}
		}
		return events
	}

	if s.appPool != nil {
		var events []domain.TransferEvent
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListTransferEvents(ctx, ListTransferEventsParams{
				TenantID:   tenantID,
				TransferID: transferID,
			})
			if err != nil {
				return err
			}
			events = parseEvents(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer events: %w", err)
		}
		return events, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferEvents", "tenant_id", tenantID)
	rows, err := s.q.ListTransferEvents(ctx, ListTransferEventsParams{
		TenantID:   tenantID,
		TransferID: transferID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer events: %w", err)
	}
	return parseEvents(rows), nil
}

func (s *TransferStoreAdapter) GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error) {
	startOfDay := date.Truncate(24 * time.Hour)
	endOfDay := startOfDay.Add(24 * time.Hour)

	if s.appPool != nil {
		var result decimal.Decimal
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			r, err := s.q.WithTx(tx).SumDailyVolumeByTenant(ctx, SumDailyVolumeByTenantParams{
				TenantID:    tenantID,
				CreatedAt:   startOfDay,
				CreatedAt_2: endOfDay,
			})
			if err != nil {
				return err
			}
			result = decimalFromNumeric(r)
			return nil
		})
		if err != nil {
			return decimal.Zero, fmt.Errorf("settla-core: getting daily volume: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetDailyVolume", "tenant_id", tenantID)
	result, err := s.q.SumDailyVolumeByTenant(ctx, SumDailyVolumeByTenantParams{
		TenantID:    tenantID,
		CreatedAt:   startOfDay,
		CreatedAt_2: endOfDay,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-core: getting daily volume: %w", err)
	}
	return decimalFromNumeric(result), nil
}

// CountPendingTransfers returns the number of non-terminal transfers for a tenant.
func (s *TransferStoreAdapter) CountPendingTransfers(ctx context.Context, tenantID uuid.UUID) (int, error) {
	count, err := s.q.CountPendingTransfers(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("settla-store: counting pending transfers: %w", err)
	}
	return int(count), nil
}

func (s *TransferStoreAdapter) CreateQuote(ctx context.Context, quote *domain.Quote) error {
	feesJSON, err := json.Marshal(quote.Fees)
	if err != nil {
		slog.Warn("settla-store: failed to marshal quote fees", "error", err)
	}
	routeJSON, err := json.Marshal(quote.Route)
	if err != nil {
		slog.Warn("settla-store: failed to marshal quote route", "error", err)
	}

	row, err := s.q.CreateQuote(ctx, CreateQuoteParams{
		TenantID:       quote.TenantID,
		SourceCurrency: string(quote.SourceCurrency),
		SourceAmount:   numericFromDecimal(quote.SourceAmount),
		DestCurrency:   string(quote.DestCurrency),
		DestAmount:     numericFromDecimal(quote.DestAmount),
		StableAmount:   numericFromDecimal(quote.StableAmount),
		FxRate:         numericFromDecimal(quote.FXRate),
		Fees:           feesJSON,
		Route:          routeJSON,
		ExpiresAt:      quote.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating quote: %w", err)
	}
	quote.ID = row.ID
	quote.CreatedAt = row.CreatedAt
	return nil
}

func (s *TransferStoreAdapter) GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error) {
	if s.appPool != nil {
		var result *domain.Quote
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetQuote(ctx, GetQuoteParams{
				ID:       quoteID,
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}
			result, err = quoteFromRow(row)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting quote %s: %w", quoteID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetQuote", "tenant_id", tenantID)
	row, err := s.q.GetQuote(ctx, GetQuoteParams{
		ID:       quoteID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting quote %s: %w", quoteID, err)
	}
	return quoteFromRow(row)
}

func (s *TransferStoreAdapter) ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	if s.appPool != nil {
		var transfers []domain.Transfer
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListTransfersByTenant(ctx, ListTransfersByTenantParams{
				TenantID: tenantID,
				Limit:    int32(limit),
				Offset:   int32(offset),
			})
			if err != nil {
				return err
			}
			transfers = make([]domain.Transfer, len(rows))
			for i, row := range rows {
				t, err := s.transferFromRowWithDecrypt(row)
				if err != nil {
					return err
				}
				transfers[i] = *t
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: listing transfers for tenant %s: %w", tenantID, err)
		}
		return transfers, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListTransfers", "tenant_id", tenantID)
	rows, err := s.q.ListTransfersByTenant(ctx, ListTransfersByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: listing transfers for tenant %s: %w", tenantID, err)
	}
	transfers := make([]domain.Transfer, len(rows))
	for i, row := range rows {
		t, err := s.transferFromRowWithDecrypt(row)
		if err != nil {
			return nil, err
		}
		transfers[i] = *t
	}
	return transfers, nil
}

func (s *TransferStoreAdapter) ListTransfersFiltered(ctx context.Context, tenantID uuid.UUID, statusFilter, searchQuery string, limit int) ([]domain.Transfer, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.q.ListTransfersByTenantFiltered(ctx, ListTransfersByTenantFilteredParams{
		TenantID:     tenantID,
		StatusFilter: statusFilter,
		SearchQuery:  searchQuery,
		PageSize:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: listing transfers filtered for tenant %s: %w", tenantID, err)
	}
	transfers := make([]domain.Transfer, len(rows))
	for i, row := range rows {
		t, err := s.transferFromRowWithDecrypt(row)
		if err != nil {
			return nil, err
		}
		transfers[i] = *t
	}
	return transfers, nil
}

// TenantStoreAdapter implements core.TenantStore using SQLC-generated queries.
type TenantStoreAdapter struct {
	q *Queries
}

// NewTenantStoreAdapter creates a new TenantStoreAdapter.
func NewTenantStoreAdapter(q *Queries) *TenantStoreAdapter {
	return &TenantStoreAdapter{q: q}
}

func (s *TenantStoreAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	row, err := s.q.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting tenant %s: %w", tenantID, err)
	}
	return tenantFromRow(row)
}

func (s *TenantStoreAdapter) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row, err := s.q.GetTenantBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting tenant by slug %s: %w", slug, err)
	}
	return tenantFromRow(row)
}

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

	// Version 0 means plaintext — no decryption needed.
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

// --- Conversion helpers ---

func transferFromRow(row Transfer) (*domain.Transfer, error) {
	t := &domain.Transfer{
		ID:                row.ID,
		TenantID:          row.TenantID,
		ExternalRef:       row.ExternalRef.String,
		IdempotencyKey:    row.IdempotencyKey.String,
		Status:            domain.TransferStatus(row.Status),
		Version:           row.Version,
		SourceCurrency:    domain.Currency(row.SourceCurrency),
		SourceAmount:      decimalFromNumeric(row.SourceAmount),
		DestCurrency:      domain.Currency(row.DestCurrency),
		DestAmount:        decimalFromNumeric(row.DestAmount),
		StableCoin:        domain.Currency(row.StableCoin.String),
		StableAmount:      decimalFromNumeric(row.StableAmount),
		Chain:             domain.CryptoChain(row.Chain.String),
		FXRate:            decimalFromNumeric(row.FxRate),
		OnRampProviderID:  row.OnRampProviderID.String,
		OffRampProviderID: row.OffRampProviderID.String,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		FailureReason:     row.FailureReason.String,
		FailureCode:       row.FailureCode.String,
	}
	if row.QuoteID.Valid {
		id := uuid.UUID(row.QuoteID.Bytes)
		t.QuoteID = &id
	}
	if row.FundedAt.Valid {
		t.FundedAt = &row.FundedAt.Time
	}
	if row.CompletedAt.Valid {
		t.CompletedAt = &row.CompletedAt.Time
	}
	if row.FailedAt.Valid {
		t.FailedAt = &row.FailedAt.Time
	}
	if row.Fees != nil {
		if err := json.Unmarshal(row.Fees, &t.Fees); err != nil {
			slog.Warn("settla-store: failed to unmarshal fees", "transfer_id", row.ID, "error", err)
		}
	}
	if row.Sender != nil {
		if err := json.Unmarshal(row.Sender, &t.Sender); err != nil {
			slog.Warn("settla-store: failed to unmarshal sender", "transfer_id", row.ID, "error", err)
		}
	}
	if row.Recipient != nil {
		if err := json.Unmarshal(row.Recipient, &t.Recipient); err != nil {
			slog.Warn("settla-store: failed to unmarshal recipient", "transfer_id", row.ID, "error", err)
		}
	}
	return t, nil
}

func tenantFromRow(row Tenant) (*domain.Tenant, error) {
	t := &domain.Tenant{
		ID:               row.ID,
		Name:             row.Name,
		Slug:             row.Slug,
		Status:           domain.TenantStatus(row.Status),
		SettlementModel:  domain.SettlementModel(row.SettlementModel),
		WebhookURL:       row.WebhookUrl.String,
		WebhookSecret:    row.WebhookSecret.String,
		DailyLimitUSD:    decimalFromNumeric(row.DailyLimitUsd),
		PerTransferLimit: decimalFromNumeric(row.PerTransferLimit),
		KYBStatus:        domain.KYBStatus(row.KybStatus),
		CryptoConfig: domain.TenantCryptoConfig{
			CryptoEnabled:         row.CryptoEnabled,
			DefaultSettlementPref: domain.SettlementPreference(row.DefaultSettlementPref),
			SupportedChains:       cryptoChainsFromStrings(row.SupportedChains),
			MinConfirmationsTron:  row.MinConfirmationsTron,
			MinConfirmationsEth:   row.MinConfirmationsEth,
			MinConfirmationsBase:  row.MinConfirmationsBase,
			PaymentToleranceBPS:   row.PaymentToleranceBps,
			DefaultSessionTTLSecs: row.DefaultSessionTtlSecs,
		},
		BankConfig: domain.TenantBankConfig{
			BankDepositsEnabled:     row.BankDepositsEnabled,
			DefaultBankingPartner:   row.DefaultBankingPartner.String,
			BankSupportedCurrencies: currenciesFromStrings(row.BankSupportedCurrencies),
			DefaultMismatchPolicy:   domain.PaymentMismatchPolicy(row.DefaultMismatchPolicy),
			DefaultSessionTTLSecs:   row.BankDefaultSessionTtlSecs,
		},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.KybVerifiedAt.Valid {
		t.KYBVerifiedAt = &row.KybVerifiedAt.Time
	}
	if row.FeeSchedule != nil {
		if err := json.Unmarshal(row.FeeSchedule, &t.FeeSchedule); err != nil {
			slog.Warn("settla-store: failed to unmarshal fee_schedule", "tenant_id", row.ID, "error", err)
		}
	}
	if row.Metadata != nil {
		if err := json.Unmarshal(row.Metadata, &t.Metadata); err != nil {
			slog.Warn("settla-store: failed to unmarshal metadata", "tenant_id", row.ID, "error", err)
		}
	}
	return t, nil
}

func quoteFromRow(row Quote) (*domain.Quote, error) {
	q := &domain.Quote{
		ID:             row.ID,
		TenantID:       row.TenantID,
		SourceCurrency: domain.Currency(row.SourceCurrency),
		SourceAmount:   decimalFromNumeric(row.SourceAmount),
		DestCurrency:   domain.Currency(row.DestCurrency),
		DestAmount:     decimalFromNumeric(row.DestAmount),
		StableAmount:   decimalFromNumeric(row.StableAmount),
		FXRate:         decimalFromNumeric(row.FxRate),
		ExpiresAt:      row.ExpiresAt,
		CreatedAt:      row.CreatedAt,
	}
	if row.Fees != nil {
		if err := json.Unmarshal(row.Fees, &q.Fees); err != nil {
			slog.Warn("settla-store: failed to unmarshal fees", "quote_id", row.ID, "error", err)
		}
	}
	if row.Route != nil {
		if err := json.Unmarshal(row.Route, &q.Route); err != nil {
			slog.Warn("settla-store: failed to unmarshal route", "quote_id", row.ID, "error", err)
		}
	}
	return q, nil
}

func textFromString(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(d.String())
	return n
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

func uuidFromPtr(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func pgtypeDateFromPtr(t *time.Time) pgtype.Date {
	if t == nil || t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

func timePtrFromPgtypeDate(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := d.Time
	return &t
}

func timePtrFromPgtypeTz(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	ts := t.Time
	return &ts
}

func cryptoChainsFromStrings(ss []string) []domain.CryptoChain {
	out := make([]domain.CryptoChain, len(ss))
	for i, s := range ss {
		out[i] = domain.CryptoChain(s)
	}
	return out
}

func currenciesFromStrings(ss []string) []domain.Currency {
	out := make([]domain.Currency, len(ss))
	for i, s := range ss {
		out[i] = domain.Currency(s)
	}
	return out
}
