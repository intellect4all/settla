package chainmonitor

import (
	"sync"
	"sync/atomic"
)

// AddressSet provides a two-layer address lookup:
//   - A mutable layer protected by RWMutex for additions
//   - An immutable snapshot captured via atomic.Pointer for lock-free reads by pollers
//
// Pollers call Snapshot() once at the start of each poll cycle and use the
// returned map for all lookups during that cycle. This ensures zero contention
// between the sync goroutine (which calls Add/Replace) and pollers.
type AddressSet struct {
	mu       sync.RWMutex
	mutable  map[string]AddressInfo // chain:address → info
	snapshot atomic.Pointer[map[string]AddressInfo]
}

// AddressInfo holds the metadata for a watched address.
type AddressInfo struct {
	Chain    string
	Address  string
	TenantID string
}

// NewAddressSet creates an empty address set.
func NewAddressSet() *AddressSet {
	s := &AddressSet{
		mutable: make(map[string]AddressInfo),
	}
	empty := make(map[string]AddressInfo)
	s.snapshot.Store(&empty)
	return s
}

// addressKey builds the map key for an address.
func addressKey(chain, address string) string {
	return chain + ":" + address
}

// Add inserts or updates an address in the mutable set.
// Call Publish() to make additions visible to pollers.
func (s *AddressSet) Add(info AddressInfo) {
	key := addressKey(info.Chain, info.Address)
	s.mu.Lock()
	s.mutable[key] = info
	s.mu.Unlock()
}

// Replace atomically swaps the entire mutable set (used for full reconciliation).
// Automatically publishes the new snapshot.
func (s *AddressSet) Replace(addresses []AddressInfo) {
	newMap := make(map[string]AddressInfo, len(addresses))
	for _, info := range addresses {
		newMap[addressKey(info.Chain, info.Address)] = info
	}
	s.mu.Lock()
	s.mutable = newMap
	s.mu.Unlock()
	s.Publish()
}

// Publish creates an immutable snapshot from the current mutable set
// and makes it available to pollers via atomic load.
func (s *AddressSet) Publish() {
	s.mu.RLock()
	snap := make(map[string]AddressInfo, len(s.mutable))
	for k, v := range s.mutable {
		snap[k] = v
	}
	s.mu.RUnlock()
	s.snapshot.Store(&snap)
}

// Snapshot returns the current immutable snapshot for lock-free reads.
// Pollers should capture this once at the start of a poll cycle.
func (s *AddressSet) Snapshot() map[string]AddressInfo {
	return *s.snapshot.Load()
}

// Len returns the number of addresses in the mutable set.
func (s *AddressSet) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mutable)
}

// Contains checks if an address exists in the snapshot (lock-free).
func (s *AddressSet) Contains(chain, address string) bool {
	snap := s.Snapshot()
	_, ok := snap[addressKey(chain, address)]
	return ok
}

// Lookup returns the AddressInfo for a given address from the snapshot (lock-free).
func (s *AddressSet) Lookup(chain, address string) (AddressInfo, bool) {
	snap := s.Snapshot()
	info, ok := snap[addressKey(chain, address)]
	return info, ok
}
