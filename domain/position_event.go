package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PositionEventType identifies the kind of position mutation recorded in the
// event-sourced position ledger.
type PositionEventType string

const (
	PosEventCredit  PositionEventType = "CREDIT"  // Balance increased (deposit, top-up, compensation)
	PosEventDebit   PositionEventType = "DEBIT"   // Balance decreased (withdrawal, rebalance)
	PosEventReserve PositionEventType = "RESERVE"  // Funds reserved for in-flight transfer
	PosEventRelease PositionEventType = "RELEASE"  // Reserved funds released (transfer failed)
	PosEventCommit  PositionEventType = "COMMIT"   // Reserved moved to locked
	PosEventConsume PositionEventType = "CONSUME"  // Reserved+balance decreased (transfer completed)
)

// PositionEvent is an immutable record of a single treasury position mutation.
// These events form an append-only log that serves as the audit authority and
// crash-recovery source for the in-memory treasury manager.
//
// The position_events table is partitioned by recorded_at (monthly) with 90-day
// retention. At peak load (~20,000 events/sec), events are batch-inserted every
// 10ms via a dedicated writer goroutine.
type PositionEvent struct {
	ID             uuid.UUID
	PositionID     uuid.UUID
	TenantID       uuid.UUID
	EventType      PositionEventType
	Amount         decimal.Decimal
	BalanceAfter   decimal.Decimal
	LockedAfter    decimal.Decimal
	ReferenceID    uuid.UUID
	ReferenceType  string // "deposit_session", "bank_deposit", "position_transaction", "transfer", "compensation"
	IdempotencyKey string
	RecordedAt     time.Time
}
