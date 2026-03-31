package appconfig

import (
	"strings"
	"testing"
)

func TestBaseConfigValidate_AllRequired(t *testing.T) {
	cfg := &BaseConfig{
		Env:                 EnvDevelopment,
		NATSInitialReplicas: 1,
		NodePartitions:      8,
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation errors for missing required fields")
	}
	// Should report all missing fields at once
	wantFields := []string{
		"SETTLA_TRANSFER_DB_URL",
		"SETTLA_LEDGER_DB_URL",
		"SETTLA_TREASURY_DB_URL",
		"SETTLA_TIGERBEETLE_ADDRESSES",
		"SETTLA_NATS_URL",
	}
	for _, field := range wantFields {
		found := false
		for _, err := range errs {
			if strings.Contains(err, field) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected error mentioning %q, got errors: %v", field, errs)
		}
	}
}

func TestBaseConfigValidate_SSLEnforcement(t *testing.T) {
	cfg := &BaseConfig{
		Env:                  EnvProduction,
		TransferDBURL:        "postgres://host:5432/db?sslmode=disable",
		LedgerDBURL:          "postgres://host:5432/db?sslmode=verify-ca",
		TreasuryDBURL:        "postgres://host:5432/db?sslmode=verify-full",
		TigerBeetleAddresses: "localhost:3001",
		NATSURL:              "nats://localhost:4222",
		NATSToken:            "token",
		RedisURL:             "redis://localhost:6379",
		NATSInitialReplicas:  3,
		NodePartitions:       8,
	}
	errs := cfg.Validate()
	found := false
	for _, err := range errs {
		if strings.Contains(err, "SETTLA_TRANSFER_DB_URL") && strings.Contains(err, "sslmode=disable") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SSL error for TransferDBURL, got: %v", errs)
	}
}

func TestBaseConfigValidate_NATSAuthRequired(t *testing.T) {
	cfg := &BaseConfig{
		Env:                  EnvProduction,
		TransferDBURL:        "postgres://host:5432/db?sslmode=verify-ca",
		LedgerDBURL:          "postgres://host:5432/db?sslmode=verify-ca",
		TreasuryDBURL:        "postgres://host:5432/db?sslmode=verify-ca",
		TigerBeetleAddresses: "localhost:3001",
		NATSURL:              "nats://localhost:4222",
		RedisURL:             "redis://localhost:6379",
		NATSInitialReplicas:  3,
		NodePartitions:       8,
		// No NATS auth
	}
	errs := cfg.Validate()
	found := false
	for _, err := range errs {
		if strings.Contains(err, "NATS authentication required") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NATS auth error, got: %v", errs)
	}
}

func TestBaseConfigValidate_Valid(t *testing.T) {
	cfg := &BaseConfig{
		Env:                  EnvDevelopment,
		TransferDBURL:        "postgres://host:5432/transfer",
		LedgerDBURL:          "postgres://host:5432/ledger",
		TreasuryDBURL:        "postgres://host:5432/treasury",
		TigerBeetleAddresses: "localhost:3001",
		NATSURL:              "nats://localhost:4222",
		NATSInitialReplicas:  1,
		NodePartitions:       8,
	}
	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestServerConfigValidate_MissingJWT(t *testing.T) {
	cfg := &ServerConfig{
		BaseConfig: BaseConfig{
			Env:                  EnvDevelopment,
			TransferDBURL:        "postgres://host:5432/transfer",
			LedgerDBURL:          "postgres://host:5432/ledger",
			TreasuryDBURL:        "postgres://host:5432/treasury",
			TigerBeetleAddresses: "localhost:3001",
			NATSURL:              "nats://localhost:4222",
			NATSInitialReplicas:  1,
			NodePartitions:       8,
		},
		// JWTSecret missing
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing JWT secret")
	}
	if !strings.Contains(err.Error(), "SETTLA_JWT_SECRET") {
		t.Errorf("expected JWT error, got: %v", err)
	}
}

func TestServerConfigValidate_RLSRequired(t *testing.T) {
	cfg := &ServerConfig{
		BaseConfig: BaseConfig{
			Env:                  EnvProduction,
			TransferDBURL:        "postgres://host:5432/transfer?sslmode=verify-ca",
			LedgerDBURL:          "postgres://host:5432/ledger?sslmode=verify-ca",
			TreasuryDBURL:        "postgres://host:5432/treasury?sslmode=verify-ca",
			TigerBeetleAddresses: "localhost:3001",
			NATSURL:              "nats://localhost:4222",
			NATSToken:            "token",
			RedisURL:             "redis://localhost:6379",
			NATSInitialReplicas:  3,
			NodePartitions:       8,
		},
		JWTSecret: "secret",
		// TransferAppDBURL missing — required in production for RLS
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing RLS config in production")
	}
	if !strings.Contains(err.Error(), "SETTLA_TRANSFER_APP_DB_URL") {
		t.Errorf("expected RLS error, got: %v", err)
	}
}

func TestNodeConfigValidate_InvalidPartition(t *testing.T) {
	cfg := &NodeConfig{
		BaseConfig: BaseConfig{
			Env:                  EnvDevelopment,
			TransferDBURL:        "postgres://host:5432/transfer",
			LedgerDBURL:          "postgres://host:5432/ledger",
			TreasuryDBURL:        "postgres://host:5432/treasury",
			TigerBeetleAddresses: "localhost:3001",
			NATSURL:              "nats://localhost:4222",
			NATSInitialReplicas:  1,
			NodePartitions:       8,
		},
		PartitionID: 10, // >= NodePartitions (8)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid partition ID")
	}
	if !strings.Contains(err.Error(), "SETTLA_NODE_PARTITION_ID") {
		t.Errorf("expected partition ID error, got: %v", err)
	}
}

func TestNATSReplicasRange(t *testing.T) {
	cfg := &BaseConfig{
		Env:                  EnvDevelopment,
		TransferDBURL:        "postgres://host:5432/transfer",
		LedgerDBURL:          "postgres://host:5432/ledger",
		TreasuryDBURL:        "postgres://host:5432/treasury",
		TigerBeetleAddresses: "localhost:3001",
		NATSURL:              "nats://localhost:4222",
		NATSInitialReplicas:  6, // > 5
		NodePartitions:       8,
	}
	errs := cfg.Validate()
	found := false
	for _, err := range errs {
		if strings.Contains(err, "SETTLA_NATS_REPLICAS") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected NATS replicas range error, got: %v", errs)
	}
}
