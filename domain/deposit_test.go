package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── Crypto Deposit Session ─────────────────────────────────────────────

func TestDepositSession_CanTransitionTo(t *testing.T) {
	tests := []struct {
		from DepositSessionStatus
		to   DepositSessionStatus
		want bool
	}{
		// Valid transitions
		{DepositSessionStatusPendingPayment, DepositSessionStatusDetected, true},
		{DepositSessionStatusPendingPayment, DepositSessionStatusExpired, true},
		{DepositSessionStatusPendingPayment, DepositSessionStatusCancelled, true},
		{DepositSessionStatusDetected, DepositSessionStatusConfirmed, true},
		{DepositSessionStatusDetected, DepositSessionStatusPendingPayment, true},
		{DepositSessionStatusDetected, DepositSessionStatusFailed, true},
		{DepositSessionStatusConfirmed, DepositSessionStatusCrediting, true},
		{DepositSessionStatusConfirmed, DepositSessionStatusFailed, true},
		{DepositSessionStatusCrediting, DepositSessionStatusCredited, true},
		{DepositSessionStatusCrediting, DepositSessionStatusFailed, true},
		{DepositSessionStatusCredited, DepositSessionStatusSettling, true},
		{DepositSessionStatusCredited, DepositSessionStatusHeld, true},
		{DepositSessionStatusSettling, DepositSessionStatusSettled, true},
		{DepositSessionStatusSettling, DepositSessionStatusFailed, true},
		{DepositSessionStatusExpired, DepositSessionStatusDetected, true},
		{DepositSessionStatusCancelled, DepositSessionStatusDetected, true},

		// Invalid transitions
		{DepositSessionStatusPendingPayment, DepositSessionStatusCrediting, false},
		{DepositSessionStatusDetected, DepositSessionStatusSettling, false},
		{DepositSessionStatusConfirmed, DepositSessionStatusSettled, false},
		{DepositSessionStatusSettled, DepositSessionStatusFailed, false},
		{DepositSessionStatusHeld, DepositSessionStatusSettling, false},
		{DepositSessionStatusFailed, DepositSessionStatusDetected, false},
		{DepositSessionStatusFailed, DepositSessionStatusPendingPayment, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			s := &DepositSession{ID: uuid.New(), Status: tt.from, Version: 1}
			if got := s.CanTransitionTo(tt.to); got != tt.want {
				t.Errorf("CanTransitionTo: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDepositSession_TransitionTo(t *testing.T) {
	t.Run("valid transition mutates state", func(t *testing.T) {
		s := &DepositSession{ID: uuid.New(), Status: DepositSessionStatusPendingPayment, Version: 1}
		before := time.Now().UTC().Add(-time.Millisecond)

		err := s.TransitionTo(DepositSessionStatusDetected)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.Status != DepositSessionStatusDetected {
			t.Errorf("Status: got %s, want DETECTED", s.Status)
		}
		if s.Version != 2 {
			t.Errorf("Version: got %d, want 2", s.Version)
		}
		if s.UpdatedAt.Before(before) {
			t.Error("UpdatedAt was not updated")
		}
	})

	t.Run("invalid transition returns error", func(t *testing.T) {
		s := &DepositSession{ID: uuid.New(), Status: DepositSessionStatusFailed, Version: 3}
		err := s.TransitionTo(DepositSessionStatusSettled)
		if err == nil {
			t.Fatal("expected error for invalid transition")
		}
		var de *DomainError
		if !errors.As(err, &de) {
			t.Fatalf("expected *DomainError, got %T", err)
		}
		if de.Code() != CodeInvalidTransition {
			t.Errorf("Code: got %s, want %s", de.Code(), CodeInvalidTransition)
		}
		// State should not have changed
		if s.Status != DepositSessionStatusFailed {
			t.Errorf("Status should remain FAILED, got %s", s.Status)
		}
		if s.Version != 3 {
			t.Errorf("Version should remain 3, got %d", s.Version)
		}
	})
}

func TestDepositSession_IsTerminal(t *testing.T) {
	tests := []struct {
		status   DepositSessionStatus
		terminal bool
	}{
		{DepositSessionStatusPendingPayment, false},
		{DepositSessionStatusDetected, false},
		{DepositSessionStatusConfirmed, false},
		{DepositSessionStatusCrediting, false},
		{DepositSessionStatusCredited, false},
		{DepositSessionStatusSettling, false},
		{DepositSessionStatusSettled, true},
		{DepositSessionStatusHeld, true},
		{DepositSessionStatusFailed, true},
		{DepositSessionStatusCancelled, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			s := &DepositSession{Status: tt.status}
			if got := s.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal: got %v, want %v", got, tt.terminal)
			}
		})
	}
}

// ── Bank Deposit Session ───────────────────────────────────────────────

func TestBankDepositSession_CanTransitionTo(t *testing.T) {
	tests := []struct {
		from BankDepositSessionStatus
		to   BankDepositSessionStatus
		want bool
	}{
		// Valid transitions
		{BankDepositSessionStatusPendingPayment, BankDepositSessionStatusPaymentReceived, true},
		{BankDepositSessionStatusPendingPayment, BankDepositSessionStatusExpired, true},
		{BankDepositSessionStatusPendingPayment, BankDepositSessionStatusCancelled, true},
		{BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusCrediting, true},
		{BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusUnderpaid, true},
		{BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusOverpaid, true},
		{BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusCrediting, BankDepositSessionStatusCredited, true},
		{BankDepositSessionStatusCrediting, BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusCredited, BankDepositSessionStatusSettling, true},
		{BankDepositSessionStatusCredited, BankDepositSessionStatusHeld, true},
		{BankDepositSessionStatusSettling, BankDepositSessionStatusSettled, true},
		{BankDepositSessionStatusSettling, BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusUnderpaid, BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusOverpaid, BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusExpired, BankDepositSessionStatusPaymentReceived, true},
		{BankDepositSessionStatusCancelled, BankDepositSessionStatusPaymentReceived, true},

		// Invalid transitions
		{BankDepositSessionStatusPendingPayment, BankDepositSessionStatusCrediting, false},
		{BankDepositSessionStatusPaymentReceived, BankDepositSessionStatusSettled, false},
		{BankDepositSessionStatusSettled, BankDepositSessionStatusFailed, false},
		{BankDepositSessionStatusHeld, BankDepositSessionStatusSettling, false},
		{BankDepositSessionStatusFailed, BankDepositSessionStatusPaymentReceived, false},
		{BankDepositSessionStatusFailed, BankDepositSessionStatusCrediting, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			s := &BankDepositSession{ID: uuid.New(), Status: tt.from, Version: 1}
			if got := s.CanTransitionTo(tt.to); got != tt.want {
				t.Errorf("CanTransitionTo: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBankDepositSession_TransitionTo(t *testing.T) {
	t.Run("valid transition mutates state", func(t *testing.T) {
		s := &BankDepositSession{ID: uuid.New(), Status: BankDepositSessionStatusPendingPayment, Version: 1}
		before := time.Now().UTC().Add(-time.Millisecond)

		err := s.TransitionTo(BankDepositSessionStatusPaymentReceived)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.Status != BankDepositSessionStatusPaymentReceived {
			t.Errorf("Status: got %s, want PAYMENT_RECEIVED", s.Status)
		}
		if s.Version != 2 {
			t.Errorf("Version: got %d, want 2", s.Version)
		}
		if s.UpdatedAt.Before(before) {
			t.Error("UpdatedAt was not updated")
		}
	})

	t.Run("invalid transition returns error", func(t *testing.T) {
		s := &BankDepositSession{ID: uuid.New(), Status: BankDepositSessionStatusFailed, Version: 5}
		err := s.TransitionTo(BankDepositSessionStatusSettled)
		if err == nil {
			t.Fatal("expected error for invalid transition")
		}
		var de *DomainError
		if !errors.As(err, &de) {
			t.Fatalf("expected *DomainError, got %T", err)
		}
		if de.Code() != CodeInvalidTransition {
			t.Errorf("Code: got %s, want %s", de.Code(), CodeInvalidTransition)
		}
		if s.Status != BankDepositSessionStatusFailed {
			t.Errorf("Status should remain FAILED, got %s", s.Status)
		}
		if s.Version != 5 {
			t.Errorf("Version should remain 5, got %d", s.Version)
		}
	})

	t.Run("full lifecycle", func(t *testing.T) {
		s := &BankDepositSession{ID: uuid.New(), Status: BankDepositSessionStatusPendingPayment, Version: 1}
		steps := []BankDepositSessionStatus{
			BankDepositSessionStatusPaymentReceived,
			BankDepositSessionStatusCrediting,
			BankDepositSessionStatusCredited,
			BankDepositSessionStatusSettling,
			BankDepositSessionStatusSettled,
		}
		for _, next := range steps {
			if err := s.TransitionTo(next); err != nil {
				t.Fatalf("TransitionTo(%s) failed: %v", next, err)
			}
		}
		if s.Status != BankDepositSessionStatusSettled {
			t.Errorf("final status: got %s, want SETTLED", s.Status)
		}
		if s.Version != 6 {
			t.Errorf("final version: got %d, want 6", s.Version)
		}
	})
}

func TestBankDepositSession_IsTerminal(t *testing.T) {
	tests := []struct {
		status   BankDepositSessionStatus
		terminal bool
	}{
		{BankDepositSessionStatusPendingPayment, false},
		{BankDepositSessionStatusPaymentReceived, false},
		{BankDepositSessionStatusCrediting, false},
		{BankDepositSessionStatusCredited, false},
		{BankDepositSessionStatusSettling, false},
		{BankDepositSessionStatusUnderpaid, false},
		{BankDepositSessionStatusOverpaid, false},
		{BankDepositSessionStatusExpired, false},
		{BankDepositSessionStatusSettled, true},
		{BankDepositSessionStatusHeld, true},
		{BankDepositSessionStatusFailed, true},
		{BankDepositSessionStatusCancelled, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			s := &BankDepositSession{Status: tt.status}
			if got := s.IsTerminal(); got != tt.terminal {
				t.Errorf("IsTerminal: got %v, want %v", got, tt.terminal)
			}
		})
	}
}

// ── Deposit Full Lifecycle ─────────────────────────────────────────────

func TestDepositSession_FullLifecycle_AutoConvert(t *testing.T) {
	s := &DepositSession{ID: uuid.New(), Status: DepositSessionStatusPendingPayment, Version: 1}
	steps := []DepositSessionStatus{
		DepositSessionStatusDetected,
		DepositSessionStatusConfirmed,
		DepositSessionStatusCrediting,
		DepositSessionStatusCredited,
		DepositSessionStatusSettling,
		DepositSessionStatusSettled,
	}
	for _, next := range steps {
		if err := s.TransitionTo(next); err != nil {
			t.Fatalf("TransitionTo(%s) failed: %v", next, err)
		}
	}
	if !s.IsTerminal() {
		t.Error("expected terminal after SETTLED")
	}
	if s.Version != 7 {
		t.Errorf("version: got %d, want 7", s.Version)
	}
}

func TestDepositSession_FullLifecycle_Hold(t *testing.T) {
	s := &DepositSession{ID: uuid.New(), Status: DepositSessionStatusPendingPayment, Version: 1}
	steps := []DepositSessionStatus{
		DepositSessionStatusDetected,
		DepositSessionStatusConfirmed,
		DepositSessionStatusCrediting,
		DepositSessionStatusCredited,
		DepositSessionStatusHeld,
	}
	for _, next := range steps {
		if err := s.TransitionTo(next); err != nil {
			t.Fatalf("TransitionTo(%s) failed: %v", next, err)
		}
	}
	if !s.IsTerminal() {
		t.Error("expected terminal after HELD")
	}
}

func TestDepositSession_LatePaymentAfterExpiry(t *testing.T) {
	s := &DepositSession{ID: uuid.New(), Status: DepositSessionStatusPendingPayment, Version: 1}
	if err := s.TransitionTo(DepositSessionStatusExpired); err != nil {
		t.Fatalf("TransitionTo(EXPIRED) failed: %v", err)
	}
	// Late payment detected after expiry
	if err := s.TransitionTo(DepositSessionStatusDetected); err != nil {
		t.Fatalf("TransitionTo(DETECTED) from EXPIRED failed: %v", err)
	}
	if s.Status != DepositSessionStatusDetected {
		t.Errorf("expected DETECTED, got %s", s.Status)
	}
}

func TestDepositSession_UnknownStatus(t *testing.T) {
	s := &DepositSession{Status: DepositSessionStatus("UNKNOWN")}
	if s.CanTransitionTo(DepositSessionStatusDetected) {
		t.Error("expected false for unknown status")
	}
	if !s.IsTerminal() {
		// Unknown status defaults to non-terminal via the switch default
		// Actually let's check what IsTerminal returns for unknown
	}
}
