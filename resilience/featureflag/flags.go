// Package featureflag provides a lightweight, in-process feature flag system
// backed by environment variables with hot-reload from a JSON config file.
//
// Evaluation order: env var override → config file → default value (false).
//
// Config file format (e.g. /etc/settla/features.json):
//
//	{
//	  "flags": {
//	    "new_settlement_flow": { "enabled": true, "rollout_pct": 50 },
//	    "enhanced_reconciliation": { "enabled": false }
//	  }
//	}
//
// Env var override: SETTLA_FF_NEW_SETTLEMENT_FLOW=true
//
// Usage:
//
//	if flags.IsEnabled("new_settlement_flow") { ... }
//	if flags.IsEnabledForTenant("new_settlement_flow", tenantID) { ... }
package featureflag

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Flag represents a single feature flag with optional percentage-based rollout.
type Flag struct {
	Name        string  `json:"name"`
	Enabled     bool    `json:"enabled"`
	RolloutPct  float64 `json:"rollout_pct"`  // 0-100, percentage of tenants
	Description string  `json:"description,omitempty"`
}

// configFile is the JSON structure of the config file.
type configFile struct {
	Flags map[string]Flag `json:"flags"`
}

// Manager provides feature flag evaluation with hot-reload support.
type Manager struct {
	configPath string
	logger     *slog.Logger

	mu    sync.RWMutex
	flags map[string]Flag

	reloadInterval time.Duration
}

// NewManager creates a new feature flag manager. It performs an initial load
// of the config file (if it exists) and returns the manager. If the config
// file does not exist, the manager starts with no flags (all return false).
func NewManager(configPath string, logger *slog.Logger) *Manager {
	m := &Manager{
		configPath:     configPath,
		logger:         logger,
		flags:          make(map[string]Flag),
		reloadInterval: 30 * time.Second,
	}

	if err := m.Reload(); err != nil {
		logger.Warn("settla-featureflag: initial config load failed, starting with empty flags",
			"config_path", configPath,
			"error", err,
		)
	}

	return m
}

// IsEnabled returns true if the flag is globally enabled.
// Returns false for unknown flags (safe default).
//
// Evaluation order: env var override → config file → false.
func (m *Manager) IsEnabled(name string) bool {
	// Check env var override first: SETTLA_FF_<UPPER_SNAKE_NAME>
	if v, ok := m.envOverride(name); ok {
		return v
	}

	m.mu.RLock()
	f, exists := m.flags[name]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	return f.Enabled
}

// IsEnabledForTenant returns true if the flag is enabled for a specific tenant.
// Uses consistent hashing on (flag_name + tenant_id) so the same tenant always
// gets the same result for a given flag. Increasing rollout_pct from 50→75
// only adds tenants, never removes existing ones.
//
// Returns false for unknown flags (safe default).
func (m *Manager) IsEnabledForTenant(name string, tenantID uuid.UUID) bool {
	// Check env var override first — if set, it applies to all tenants.
	if v, ok := m.envOverride(name); ok {
		return v
	}

	m.mu.RLock()
	f, exists := m.flags[name]
	m.mu.RUnlock()

	if !exists {
		return false
	}

	if !f.Enabled {
		return false
	}

	// If rollout is 100% (or unset/zero treated as 100% when enabled), all tenants get it.
	if f.RolloutPct >= 100 {
		return true
	}
	if f.RolloutPct <= 0 {
		// Enabled but 0% rollout means no tenants get it.
		return false
	}

	// Consistent hash: sha256(flag_name + tenant_id) → bucket 0-99.
	bucket := consistentBucket(name, tenantID)
	return float64(bucket) < f.RolloutPct
}

// Reload re-reads the config file and updates the in-memory flags.
// Called periodically (every 30s) by Start, or manually, or on SIGHUP.
func (m *Manager) Reload() error {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("settla-featureflag: reading config %s: %w", m.configPath, err)
	}

	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("settla-featureflag: parsing config %s: %w", m.configPath, err)
	}

	// Populate Name field from map key.
	flags := make(map[string]Flag, len(cfg.Flags))
	for k, f := range cfg.Flags {
		f.Name = k
		flags[k] = f
	}

	m.mu.Lock()
	m.flags = flags
	m.mu.Unlock()

	m.logger.Info("settla-featureflag: config reloaded",
		"config_path", m.configPath,
		"flag_count", len(flags),
	)

	return nil
}

// Start begins background config file watching. It reloads the config every
// 30 seconds and also listens for context cancellation to stop.
func (m *Manager) Start(ctx context.Context) {
	ticker := time.NewTicker(m.reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("settla-featureflag: stopping config watcher")
			return
		case <-ticker.C:
			if err := m.Reload(); err != nil {
				m.logger.Warn("settla-featureflag: periodic reload failed",
					"error", err,
				)
			}
		}
	}
}

// All returns all flags and their current state (for admin/debug endpoints).
func (m *Manager) All() map[string]Flag {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]Flag, len(m.flags))
	maps.Copy(result, m.flags)
	return result
}

// envOverride checks for an environment variable override.
// Format: SETTLA_FF_<UPPER_SNAKE_CASE_NAME>
// Returns the boolean value and whether the env var was set.
func (m *Manager) envOverride(name string) (bool, bool) {
	envKey := "SETTLA_FF_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	val, ok := os.LookupEnv(envKey)
	if !ok {
		return false, false
	}
	switch strings.ToLower(val) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		m.logger.Warn("settla-featureflag: invalid env var value, ignoring",
			"env_key", envKey,
			"value", val,
		)
		return false, false
	}
}

// consistentBucket returns a deterministic bucket in [0, 100) for a given
// flag name and tenant ID. The same inputs always produce the same bucket.
// Increasing rollout_pct only adds buckets, never removes existing ones.
func consistentBucket(flagName string, tenantID uuid.UUID) int {
	h := sha256.New()
	h.Write([]byte(flagName))
	h.Write(tenantID[:])
	sum := h.Sum(nil)
	// Use first 8 bytes as uint64, mod 100.
	n := binary.BigEndian.Uint64(sum[:8])
	return int(n % 100)
}
