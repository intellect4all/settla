package main

import (
	"sync"
)

// PendingDeposit represents a simulated on-chain deposit that will be returned
// by the blockchain transactions endpoint for deposit detection.
type PendingDeposit struct {
	TxHash      string `json:"tx_hash"`
	From        string `json:"from"`
	To          string `json:"to"` // the watched deposit address
	Token       string `json:"token"`
	Amount      string `json:"amount"`
	BlockNumber uint64 `json:"block_number"`
}

// Config holds dynamic provider behavior configuration.
// All fields are safe to read/write concurrently via Get/Set methods.
type Config struct {
	mu sync.RWMutex

	// LatencyMs is the simulated processing delay in milliseconds.
	LatencyMs int `json:"latency_ms"`

	// ErrorRate is the probability of returning an error (0.0 = never, 1.0 = always).
	ErrorRate float64 `json:"error_rate"`

	// ErrorCode is the HTTP status code to return on simulated errors.
	ErrorCode int `json:"error_code"`

	// ErrorMessage is the error message to return on simulated errors.
	ErrorMessage string `json:"error_message"`

	// FailProviders maps provider IDs to true if that specific provider should fail.
	// When a provider is in this map with value true, all its requests fail regardless of ErrorRate.
	FailProviders map[string]bool `json:"fail_providers"`

	// pendingDeposits holds simulated deposits keyed by watched address.
	// These are consumed (removed) when read by the blockchain transactions endpoint.
	pendingDeposits map[string][]PendingDeposit
}

// DefaultConfig returns the default configuration: 200ms latency, 0% errors.
func DefaultConfig() *Config {
	return &Config{
		LatencyMs:       200,
		ErrorRate:       0.0,
		ErrorCode:       503,
		ErrorMessage:    "provider temporarily unavailable",
		FailProviders:   make(map[string]bool),
		pendingDeposits: make(map[string][]PendingDeposit),
	}
}

// Get returns a snapshot of the current configuration.
func (c *Config) Get() ConfigSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	fp := make(map[string]bool, len(c.FailProviders))
	for k, v := range c.FailProviders {
		fp[k] = v
	}

	return ConfigSnapshot{
		LatencyMs:     c.LatencyMs,
		ErrorRate:     c.ErrorRate,
		ErrorCode:     c.ErrorCode,
		ErrorMessage:  c.ErrorMessage,
		FailProviders: fp,
	}
}

// ConfigSnapshot is a read-only snapshot of the config.
type ConfigSnapshot struct {
	LatencyMs     int             `json:"latency_ms"`
	ErrorRate     float64         `json:"error_rate"`
	ErrorCode     int             `json:"error_code"`
	ErrorMessage  string          `json:"error_message"`
	FailProviders map[string]bool `json:"fail_providers"`
}

// ShouldFail returns true if the given provider is forced to fail.
func (s ConfigSnapshot) ShouldFail(providerID string) bool {
	return s.FailProviders[providerID]
}

// Update applies partial config updates from a ConfigUpdate.
func (c *Config) Update(u ConfigUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if u.LatencyMs != nil {
		c.LatencyMs = *u.LatencyMs
	}
	if u.ErrorRate != nil {
		c.ErrorRate = *u.ErrorRate
	}
	if u.ErrorCode != nil {
		c.ErrorCode = *u.ErrorCode
	}
	if u.ErrorMessage != nil {
		c.ErrorMessage = *u.ErrorMessage
	}
	if u.FailProviders != nil {
		c.FailProviders = u.FailProviders
	}
}

// Reset restores the config to defaults.
func (c *Config) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LatencyMs = 200
	c.ErrorRate = 0.0
	c.ErrorCode = 503
	c.ErrorMessage = "provider temporarily unavailable"
	c.FailProviders = make(map[string]bool)
	c.pendingDeposits = make(map[string][]PendingDeposit)
}

// AddPendingDeposit adds a simulated deposit for the given address.
func (c *Config) AddPendingDeposit(deposit PendingDeposit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingDeposits[deposit.To] = append(c.pendingDeposits[deposit.To], deposit)
}

// GetPendingDeposits returns and removes all pending deposits for the given address.
func (c *Config) GetPendingDeposits(address string) []PendingDeposit {
	c.mu.Lock()
	defer c.mu.Unlock()
	deposits := c.pendingDeposits[address]
	delete(c.pendingDeposits, address)
	return deposits
}

// ConfigUpdate is used for partial config updates via the admin API.
type ConfigUpdate struct {
	LatencyMs     *int            `json:"latency_ms,omitempty"`
	ErrorRate     *float64        `json:"error_rate,omitempty"`
	ErrorCode     *int            `json:"error_code,omitempty"`
	ErrorMessage  *string         `json:"error_message,omitempty"`
	FailProviders map[string]bool `json:"fail_providers,omitempty"`
}
