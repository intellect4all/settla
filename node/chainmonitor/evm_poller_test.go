package chainmonitor

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

func TestEVMPoller_ProcessIncomingTx_HappyPath(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["0xaddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
		Status:   domain.DepositSessionStatusPendingPayment,
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "ethereum", Symbol: "USDT", ContractAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7", Decimals: 6, IsActive: true},
	})

	poller := &EVMPoller{
		chain:        "ethereum",
		cfg:          ChainConfig{Confirmations: 12},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "ethereum",
		TxHash:        "0xabc123",
		FromAddress:   "0xsender",
		ToAddress:     "0xaddr1",
		TokenContract: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		Amount:        decimal.NewFromFloat(250.75),
		BlockNumber:   18000000,
		BlockHash:     "0xblockhash",
		Timestamp:     time.Now().UTC(),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(writer.writtenTxs) != 1 {
		t.Fatalf("expected 1 written tx, got %d", len(writer.writtenTxs))
	}

	dtx := writer.writtenTxs[0]
	if dtx.SessionID != sessionID {
		t.Errorf("session ID = %s, want %s", dtx.SessionID, sessionID)
	}
	if dtx.TenantID != tenantID {
		t.Errorf("tenant ID = %s, want %s", dtx.TenantID, tenantID)
	}
	if dtx.TxHash != "0xabc123" {
		t.Errorf("tx hash = %s, want 0xabc123", dtx.TxHash)
	}
	if dtx.Chain != "ethereum" {
		t.Errorf("chain = %s, want ethereum", dtx.Chain)
	}
	if !dtx.Amount.Equal(decimal.NewFromFloat(250.75)) {
		t.Errorf("amount = %s, want 250.75", dtx.Amount)
	}
	if dtx.Confirmations != 12 {
		t.Errorf("confirmations = %d, want 12", dtx.Confirmations)
	}

	// Verify outbox entry
	if len(writer.writtenEntries) != 1 || len(writer.writtenEntries[0]) != 1 {
		t.Fatal("expected 1 outbox entry")
	}
	entry := writer.writtenEntries[0][0]
	if entry.EventType != domain.EventDepositTxDetected {
		t.Errorf("event type = %s, want %s", entry.EventType, domain.EventDepositTxDetected)
	}
	if entry.AggregateID != sessionID {
		t.Errorf("aggregate ID = %s, want %s", entry.AggregateID, sessionID)
	}
}

func TestEVMPoller_ProcessIncomingTx_Idempotency(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["0xaddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
	}

	// Pre-populate as already recorded
	writer.existingTxs["ethereum:0xabc123"] = &domain.DepositTransaction{
		ID:     uuid.New(),
		TxHash: "0xabc123",
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "ethereum", Symbol: "USDT", ContractAddress: "0xusdt", Decimals: 6, IsActive: true},
	})

	poller := &EVMPoller{
		chain:        "ethereum",
		cfg:          ChainConfig{Confirmations: 12},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "ethereum",
		TxHash:        "0xabc123",
		ToAddress:     "0xaddr1",
		TokenContract: "0xusdt",
		Amount:        decimal.NewFromFloat(100),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not have written again
	if len(writer.writtenTxs) != 0 {
		t.Errorf("expected 0 new writes (idempotent), got %d", len(writer.writtenTxs))
	}
}

func TestEVMPoller_ProcessIncomingTx_UnwatchedToken(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["0xaddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "ethereum", Symbol: "USDT", ContractAddress: "0xusdt", Decimals: 6, IsActive: true},
	})

	poller := &EVMPoller{
		chain:        "ethereum",
		cfg:          ChainConfig{Confirmations: 12},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "ethereum",
		TxHash:        "0xxyz456",
		ToAddress:     "0xaddr1",
		TokenContract: "0xrandom-token", // not watched
		Amount:        decimal.NewFromFloat(50),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not write for unwatched token
	if len(writer.writtenTxs) != 0 {
		t.Errorf("expected 0 writes for unwatched token, got %d", len(writer.writtenTxs))
	}
}

func TestEVMPoller_ProcessIncomingTx_NoSession(t *testing.T) {
	writer := newMockOutboxWriter()
	// No sessions registered

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "ethereum", Symbol: "USDT", ContractAddress: "0xusdt", Decimals: 6, IsActive: true},
	})

	poller := &EVMPoller{
		chain:        "ethereum",
		cfg:          ChainConfig{Confirmations: 12},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "ethereum",
		TxHash:        "0xdef789",
		ToAddress:     "0xunknown",
		TokenContract: "0xusdt",
		Amount:        decimal.NewFromFloat(25),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestEVMPoller_BaseChain(t *testing.T) {
	// Verify the EVM poller works correctly with Base chain identifier
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")

	writer.sessions["0xbaseaddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
		Status:   domain.DepositSessionStatusPendingPayment,
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "base", Symbol: "USDC", ContractAddress: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Decimals: 6, IsActive: true},
	})

	poller := &EVMPoller{
		chain:        "base",
		cfg:          ChainConfig{Chain: "base", Confirmations: 12},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "base",
		TxHash:        "0xbase_tx_hash",
		FromAddress:   "0xbasesender",
		ToAddress:     "0xbaseaddr1",
		TokenContract: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
		Amount:        decimal.NewFromFloat(500),
		BlockNumber:   10000000,
		BlockHash:     "0xbaseblockhash",
		Timestamp:     time.Now().UTC(),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(writer.writtenTxs) != 1 {
		t.Fatalf("expected 1 written tx, got %d", len(writer.writtenTxs))
	}

	dtx := writer.writtenTxs[0]
	if dtx.Chain != "base" {
		t.Errorf("chain = %s, want base", dtx.Chain)
	}
	if dtx.TenantID != tenantID {
		t.Errorf("tenant ID = %s, want %s", dtx.TenantID, tenantID)
	}
}

func TestEVMPoller_ChainAndPollInterval(t *testing.T) {
	poller := &EVMPoller{
		chain: "ethereum",
		cfg: ChainConfig{
			Chain:        "ethereum",
			PollInterval: 12 * time.Second,
		},
	}

	if poller.Chain() != "ethereum" {
		t.Errorf("Chain() = %s, want ethereum", poller.Chain())
	}
	if poller.PollInterval() != 12*time.Second {
		t.Errorf("PollInterval() = %v, want 12s", poller.PollInterval())
	}

	basePoller := &EVMPoller{
		chain: "base",
		cfg: ChainConfig{
			Chain:        "base",
			PollInterval: 2 * time.Second,
		},
	}

	if basePoller.Chain() != "base" {
		t.Errorf("Chain() = %s, want base", basePoller.Chain())
	}
	if basePoller.PollInterval() != 2*time.Second {
		t.Errorf("PollInterval() = %v, want 2s", basePoller.PollInterval())
	}
}

func TestEVMPoller_ImplementsChainPoller(t *testing.T) {
	// Compile-time interface check
	var _ ChainPoller = (*EVMPoller)(nil)
}
