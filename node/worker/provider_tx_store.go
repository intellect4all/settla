package worker

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// ClaimProviderTransactionParams holds the parameters for an atomic provider transaction claim.
type ClaimProviderTransactionParams struct {
	TenantID   uuid.UUID
	TransferID uuid.UUID
	TxType     string
	Provider   string
}

// InMemoryProviderTransferStore is a simple in-memory implementation of
// ProviderTransferStore for development and testing. In production, this
// would be backed by the transfer database via SQLC queries.
type InMemoryProviderTransferStore struct {
	mu  sync.RWMutex
	txs map[string]*domain.ProviderTx
}

// NewInMemoryProviderTransferStore creates an in-memory provider transaction store.
func NewInMemoryProviderTransferStore() *InMemoryProviderTransferStore {
	return &InMemoryProviderTransferStore{
		txs: make(map[string]*domain.ProviderTx),
	}
}

func (s *InMemoryProviderTransferStore) key(transferID uuid.UUID, txType string) string {
	return transferID.String() + ":" + txType
}

// GetProviderTransaction returns the provider transaction for a transfer+type, or nil if not found.
// tenantID is accepted for interface compliance but unused in the in-memory impl.
func (s *InMemoryProviderTransferStore) GetProviderTransaction(_ context.Context, _ uuid.UUID, transferID uuid.UUID, txType string) (*domain.ProviderTx, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, ok := s.txs[s.key(transferID, txType)]
	if !ok {
		return nil, nil
	}
	return tx, nil
}

// CreateProviderTransaction stores a new provider transaction.
func (s *InMemoryProviderTransferStore) CreateProviderTransaction(_ context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs[s.key(transferID, txType)] = tx
	return nil
}

// UpdateProviderTransaction updates an existing provider transaction.
func (s *InMemoryProviderTransferStore) UpdateProviderTransaction(_ context.Context, transferID uuid.UUID, txType string, tx *domain.ProviderTx) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs[s.key(transferID, txType)] = tx
	return nil
}

// ClaimProviderTransaction atomically claims the provider transaction slot.
// Returns non-nil UUID on success, nil UUID if already claimed (terminal status).
func (s *InMemoryProviderTransferStore) ClaimProviderTransaction(_ context.Context, params ClaimProviderTransactionParams) (*uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := s.key(params.TransferID, params.TxType)
	if existing, ok := s.txs[k]; ok {
		switch existing.Status {
		case "completed", "confirmed", "pending":
			return nil, nil // Already done or in progress — caller should skip
		}
		// "failed" or "claiming" (stuck) — allow re-claim
		delete(s.txs, k)
	}

	id := uuid.New()
	s.txs[k] = &domain.ProviderTx{ID: id.String(), Status: "claiming"}
	return &id, nil
}

// UpdateTransferRoute is a no-op in the in-memory store (transfer records are
// managed externally in tests).
func (s *InMemoryProviderTransferStore) UpdateTransferRoute(_ context.Context, _ uuid.UUID, _, _, _ string, _ domain.Currency) error {
	return nil
}

// DeleteProviderTransaction removes a provider transaction record from the in-memory store.
func (s *InMemoryProviderTransferStore) DeleteProviderTransaction(_ context.Context, transferID uuid.UUID, txType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.txs, s.key(transferID, txType))
	return nil
}

// SwitchRoute atomically deletes the provider transaction and updates the route.
// In the in-memory store this is inherently atomic under the mutex.
func (s *InMemoryProviderTransferStore) SwitchRoute(_ context.Context, transferID uuid.UUID, txType string, _, _, _ string, _ domain.Currency) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.txs, s.key(transferID, txType))
	return nil
}

// Compile-time check.
var _ ProviderTransferStore = (*InMemoryProviderTransferStore)(nil)

// BlockchainReviewStore is the subset of the review store needed by BlockchainWorker
// to escalate stuck pending transactions to manual review. It mirrors
// core/recovery.ReviewStore but is defined locally to avoid an import cycle.
type BlockchainReviewStore interface {
	CreateManualReview(ctx context.Context, transferID, tenantID uuid.UUID, transferStatus string, stuckSince time.Time) error
	HasActiveReview(ctx context.Context, transferID uuid.UUID) (bool, error)
}
