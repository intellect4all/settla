package bank

import (
	"fmt"
	"sync"

	"github.com/intellect4all/settla/domain"
)

// Registry manages the set of available banking partners. It is safe for concurrent use.
type Registry struct {
	partners map[string]domain.BankingPartner
	mu       sync.RWMutex
}

// NewRegistry creates an empty banking partner registry.
func NewRegistry() *Registry {
	return &Registry{
		partners: make(map[string]domain.BankingPartner),
	}
}

// Register adds a banking partner to the registry.
// If a partner with the same ID already exists, it is replaced.
func (r *Registry) Register(partner domain.BankingPartner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partners[partner.ID()] = partner
}

// Get retrieves a banking partner by ID.
// Returns an error if the partner is not registered.
func (r *Registry) Get(id string) (domain.BankingPartner, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.partners[id]
	if !ok {
		return nil, fmt.Errorf("settla-bank: banking partner %q not found", id)
	}
	return p, nil
}

// List returns all registered banking partners.
func (r *Registry) List() []domain.BankingPartner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]domain.BankingPartner, 0, len(r.partners))
	for _, p := range r.partners {
		result = append(result, p)
	}
	return result
}
