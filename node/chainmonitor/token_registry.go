package chainmonitor

import (
	"strings"
	"sync/atomic"

	"github.com/intellect4all/settla/domain"
)

// TokenRegistry provides lock-free token lookups using atomic copy-on-write.
// The monitor reloads the registry from the database periodically.
type TokenRegistry struct {
	data atomic.Pointer[tokenMap]
}

// tokenMap is the immutable snapshot: chain → contractAddress → Token.
type tokenMap struct {
	byContract map[string]domain.Token // key: "chain:contractAddress" (lowercased)
	byChain    map[string][]domain.Token
}

// NewTokenRegistry creates an empty token registry.
func NewTokenRegistry() *TokenRegistry {
	r := &TokenRegistry{}
	empty := &tokenMap{
		byContract: make(map[string]domain.Token),
		byChain:    make(map[string][]domain.Token),
	}
	r.data.Store(empty)
	return r
}

// Reload atomically replaces the token map with the provided tokens.
func (r *TokenRegistry) Reload(tokens []domain.Token) {
	m := &tokenMap{
		byContract: make(map[string]domain.Token, len(tokens)),
		byChain:    make(map[string][]domain.Token),
	}
	for _, t := range tokens {
		if !t.IsActive {
			continue
		}
		key := tokenKey(string(t.Chain), t.ContractAddress)
		m.byContract[key] = t
		m.byChain[string(t.Chain)] = append(m.byChain[string(t.Chain)], t)
	}
	r.data.Store(m)
}

// LookupByContract returns the token matching (chain, contractAddress), if active.
func (r *TokenRegistry) LookupByContract(chain, contractAddress string) (domain.Token, bool) {
	m := r.data.Load()
	t, ok := m.byContract[tokenKey(chain, contractAddress)]
	return t, ok
}

// TokensForChain returns all active tokens for a chain.
func (r *TokenRegistry) TokensForChain(chain string) []domain.Token {
	m := r.data.Load()
	return m.byChain[chain]
}

// ContractAddresses returns all watched contract addresses for a chain (lowercased).
func (r *TokenRegistry) ContractAddresses(chain string) []string {
	tokens := r.TokensForChain(chain)
	addrs := make([]string, len(tokens))
	for i, t := range tokens {
		addrs[i] = t.ContractAddress
	}
	return addrs
}

func tokenKey(chain, contractAddress string) string {
	return chain + ":" + strings.ToLower(contractAddress)
}
