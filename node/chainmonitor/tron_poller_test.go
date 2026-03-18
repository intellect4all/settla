package chainmonitor

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// mockCheckpointStore implements CheckpointStore for testing.
type mockCheckpointStore struct {
	blockNumber int64
	blockHash   string
}

func (m *mockCheckpointStore) GetCheckpoint(_ context.Context, _ string) (*domain.BlockCheckpoint, error) {
	if m.blockNumber == 0 {
		return nil, nil
	}
	return &domain.BlockCheckpoint{
		BlockNumber: m.blockNumber,
		BlockHash:   m.blockHash,
	}, nil
}

func (m *mockCheckpointStore) UpsertCheckpoint(_ context.Context, _ string, blockNumber int64, blockHash string) error {
	m.blockNumber = blockNumber
	m.blockHash = blockHash
	return nil
}

// mockOutboxWriter implements OutboxWriter for testing.
type mockOutboxWriter struct {
	writtenTxs     []*domain.DepositTransaction
	writtenEntries [][]domain.OutboxEntry
	existingTxs    map[string]*domain.DepositTransaction
	sessions       map[string]*domain.DepositSession
}

func newMockOutboxWriter() *mockOutboxWriter {
	return &mockOutboxWriter{
		existingTxs: make(map[string]*domain.DepositTransaction),
		sessions:    make(map[string]*domain.DepositSession),
	}
}

func (m *mockOutboxWriter) WriteDetectedTx(_ context.Context, dtx *domain.DepositTransaction, entries []domain.OutboxEntry) error {
	dtx.ID = uuid.New()
	m.writtenTxs = append(m.writtenTxs, dtx)
	m.writtenEntries = append(m.writtenEntries, entries)
	m.existingTxs[dtx.Chain+":"+dtx.TxHash] = dtx
	return nil
}

func (m *mockOutboxWriter) GetDepositTxByHash(_ context.Context, chain, txHash string) (*domain.DepositTransaction, error) {
	if dtx, ok := m.existingTxs[chain+":"+txHash]; ok {
		return dtx, nil
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockOutboxWriter) GetSessionByAddress(_ context.Context, address string) (*domain.DepositSession, error) {
	if s, ok := m.sessions[address]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("no session for address %s", address)
}

func TestProcessIncomingTx_HappyPath(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["TAddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
		Status:   domain.DepositSessionStatusPendingPayment,
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "usdt-contract", Decimals: 6, IsActive: true},
	})

	poller := &TronPoller{
		chain:        "tron",
		cfg:          ChainConfig{Confirmations: 19},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       nil,
	}
	// Use slog default for test
	poller.logger = testLogger()

	incoming := domain.IncomingTransaction{
		Chain:         "tron",
		TxHash:        "abc123",
		FromAddress:   "TSender",
		ToAddress:     "TAddr1",
		TokenContract: "usdt-contract",
		Amount:        decimal.NewFromFloat(100.50),
		BlockNumber:   12345,
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
	if dtx.TxHash != "abc123" {
		t.Errorf("tx hash = %s, want abc123", dtx.TxHash)
	}
	if !dtx.Amount.Equal(decimal.NewFromFloat(100.50)) {
		t.Errorf("amount = %s, want 100.50", dtx.Amount)
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

func TestProcessIncomingTx_Idempotency(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["TAddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
	}

	// Pre-populate as already recorded
	writer.existingTxs["tron:abc123"] = &domain.DepositTransaction{
		ID:     uuid.New(),
		TxHash: "abc123",
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "usdt-contract", Decimals: 6, IsActive: true},
	})

	poller := &TronPoller{
		chain:        "tron",
		cfg:          ChainConfig{Confirmations: 19},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "tron",
		TxHash:        "abc123",
		ToAddress:     "TAddr1",
		TokenContract: "usdt-contract",
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

func TestProcessIncomingTx_UnwatchedToken(t *testing.T) {
	writer := newMockOutboxWriter()
	sessionID := uuid.New()
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	writer.sessions["TAddr1"] = &domain.DepositSession{
		ID:       sessionID,
		TenantID: tenantID,
	}

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "usdt-contract", Decimals: 6, IsActive: true},
	})

	poller := &TronPoller{
		chain:        "tron",
		cfg:          ChainConfig{Confirmations: 19},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "tron",
		TxHash:        "xyz456",
		ToAddress:     "TAddr1",
		TokenContract: "random-token", // not watched
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

func TestProcessIncomingTx_NoSession(t *testing.T) {
	writer := newMockOutboxWriter()
	// No sessions registered

	tokens := NewTokenRegistry()
	tokens.Reload([]domain.Token{
		{Chain: "tron", Symbol: "USDT", ContractAddress: "usdt-contract", Decimals: 6, IsActive: true},
	})

	poller := &TronPoller{
		chain:        "tron",
		cfg:          ChainConfig{Confirmations: 19},
		tokens:       tokens,
		outboxWriter: writer,
		logger:       testLogger(),
	}

	incoming := domain.IncomingTransaction{
		Chain:         "tron",
		TxHash:        "def789",
		ToAddress:     "TUnknown",
		TokenContract: "usdt-contract",
		Amount:        decimal.NewFromFloat(25),
	}

	err := poller.processIncomingTx(context.Background(), incoming)
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}
