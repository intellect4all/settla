package featureflag

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func writeConfig(t *testing.T, dir string, cfg configFile) string {
	t.Helper()
	path := filepath.Join(dir, "features.json")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFlagEnabledDisabled(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"feature_a": {Enabled: true, RolloutPct: 100},
			"feature_b": {Enabled: false, RolloutPct: 0},
		},
	})

	m := NewManager(path, testLogger())

	if !m.IsEnabled("feature_a") {
		t.Error("expected feature_a to be enabled")
	}
	if m.IsEnabled("feature_b") {
		t.Error("expected feature_b to be disabled")
	}
}

func TestMissingFlagReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{},
	})

	m := NewManager(path, testLogger())

	if m.IsEnabled("nonexistent") {
		t.Error("expected missing flag to return false")
	}
	if m.IsEnabledForTenant("nonexistent", uuid.New()) {
		t.Error("expected missing flag for tenant to return false")
	}
}

func TestMissingConfigFileStartsEmpty(t *testing.T) {
	m := NewManager("/tmp/does-not-exist-featureflags.json", testLogger())

	if m.IsEnabled("anything") {
		t.Error("expected all flags false when config file missing")
	}
}

func TestEnvVarOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"my_feature": {Enabled: false, RolloutPct: 0},
		},
	})

	m := NewManager(path, testLogger())

	// Config says disabled.
	if m.IsEnabled("my_feature") {
		t.Error("expected my_feature disabled from config")
	}

	// Env var overrides to enabled.
	t.Setenv("SETTLA_FF_MY_FEATURE", "true")
	if !m.IsEnabled("my_feature") {
		t.Error("expected env var to override config to enabled")
	}

	// Env var can also force-disable an enabled flag.
	t.Setenv("SETTLA_FF_MY_FEATURE", "false")
	if m.IsEnabled("my_feature") {
		t.Error("expected env var false to override")
	}
}

func TestEnvVarOverridesForTenant(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"tenant_feature": {Enabled: true, RolloutPct: 10},
		},
	})

	m := NewManager(path, testLogger())

	// Force enable via env var — should apply to all tenants.
	t.Setenv("SETTLA_FF_TENANT_FEATURE", "true")
	for range 20 {
		if !m.IsEnabledForTenant("tenant_feature", uuid.New()) {
			t.Error("expected env var override to apply to all tenants")
		}
	}
}

func TestTenantRolloutConsistency(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"gradual_rollout": {Enabled: true, RolloutPct: 50},
		},
	})

	m := NewManager(path, testLogger())

	// Same tenant should always get the same result.
	tenant := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	first := m.IsEnabledForTenant("gradual_rollout", tenant)
	for range 100 {
		if m.IsEnabledForTenant("gradual_rollout", tenant) != first {
			t.Fatal("expected consistent result for same tenant across calls")
		}
	}
}

func TestTenantRolloutExpansion(t *testing.T) {
	dir := t.TempDir()

	// Start at 50% rollout.
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"expanding": {Enabled: true, RolloutPct: 50},
		},
	})

	m := NewManager(path, testLogger())

	// Generate a fixed set of tenants and record who is enabled at 50%.
	tenants := make([]uuid.UUID, 200)
	for i := range tenants {
		tenants[i] = uuid.New()
	}

	enabledAt50 := make(map[uuid.UUID]bool)
	for _, tid := range tenants {
		if m.IsEnabledForTenant("expanding", tid) {
			enabledAt50[tid] = true
		}
	}

	// Expand to 75%.
	writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"expanding": {Enabled: true, RolloutPct: 75},
		},
	})
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	// Every tenant that was enabled at 50% must still be enabled at 75%.
	for tid := range enabledAt50 {
		if !m.IsEnabledForTenant("expanding", tid) {
			t.Errorf("tenant %s was enabled at 50%% but disabled at 75%%", tid)
		}
	}

	// Count enabled at 75% — should be >= count at 50%.
	enabledAt75Count := 0
	for _, tid := range tenants {
		if m.IsEnabledForTenant("expanding", tid) {
			enabledAt75Count++
		}
	}
	if enabledAt75Count < len(enabledAt50) {
		t.Errorf("expected at least %d tenants at 75%%, got %d", len(enabledAt50), enabledAt75Count)
	}
}

func TestConfigHotReload(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"hot_reload": {Enabled: false},
		},
	})

	m := NewManager(path, testLogger())

	if m.IsEnabled("hot_reload") {
		t.Error("expected hot_reload disabled initially")
	}

	// Update config file.
	writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"hot_reload": {Enabled: true, RolloutPct: 100},
		},
	})

	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	if !m.IsEnabled("hot_reload") {
		t.Error("expected hot_reload enabled after reload")
	}
}

func TestAllReturnsFlags(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"flag_x": {Enabled: true, RolloutPct: 100},
			"flag_y": {Enabled: false, RolloutPct: 0},
		},
	})

	m := NewManager(path, testLogger())
	all := m.All()

	if len(all) != 2 {
		t.Fatalf("expected 2 flags, got %d", len(all))
	}
	if !all["flag_x"].Enabled {
		t.Error("expected flag_x enabled")
	}
	if all["flag_y"].Enabled {
		t.Error("expected flag_y disabled")
	}
}

func TestStartPeriodicReload(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"periodic": {Enabled: false},
		},
	})

	m := NewManager(path, testLogger())
	m.reloadInterval = 50 * time.Millisecond // Speed up for test.

	go m.Start(t.Context())

	// Update the config file.
	writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"periodic": {Enabled: true, RolloutPct: 100},
		},
	})

	// Wait for periodic reload to pick it up.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if m.IsEnabled("periodic") {
			return // Success.
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("expected periodic reload to enable the flag")
}

func TestZeroRolloutPctDisabledForAllTenants(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, configFile{
		Flags: map[string]Flag{
			"zero_pct": {Enabled: true, RolloutPct: 0},
		},
	})

	m := NewManager(path, testLogger())

	// Globally enabled (no rollout check).
	if !m.IsEnabled("zero_pct") {
		t.Error("expected IsEnabled to return true (no rollout check)")
	}

	// But no tenant should get it.
	for range 100 {
		if m.IsEnabledForTenant("zero_pct", uuid.New()) {
			t.Fatal("expected 0% rollout to disable for all tenants")
		}
	}
}
