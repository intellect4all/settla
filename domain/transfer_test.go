package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestCanTransitionToAllValid(t *testing.T) {
	for from, targets := range ValidTransitions {
		for _, to := range targets {
			tr := &Transfer{Status: from}
			if !tr.CanTransitionTo(to) {
				t.Errorf("CanTransitionTo(%s → %s) should be true", from, to)
			}
		}
	}
}

func TestCanTransitionToInvalid(t *testing.T) {
	tests := []struct {
		from TransferStatus
		to   TransferStatus
	}{
		{TransferStatusCreated, TransferStatusCompleted},
		{TransferStatusCreated, TransferStatusOnRamping},
		{TransferStatusFunded, TransferStatusCompleted},
		{TransferStatusCompleted, TransferStatusCreated},
		{TransferStatusRefunded, TransferStatusCreated},
		{TransferStatusSettling, TransferStatusRefunding},
	}
	for _, tt := range tests {
		tr := &Transfer{Status: tt.from}
		if tr.CanTransitionTo(tt.to) {
			t.Errorf("CanTransitionTo(%s → %s) should be false", tt.from, tt.to)
		}
	}
}

func TestTransitionToValidIncrementsVersion(t *testing.T) {
	tr := &Transfer{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Status:   TransferStatusCreated,
		Version:  1,
	}
	event, err := tr.TransitionTo(TransferStatusFunded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.Status != TransferStatusFunded {
		t.Errorf("expected status FUNDED, got %s", tr.Status)
	}
	if tr.Version != 2 {
		t.Errorf("expected version 2, got %d", tr.Version)
	}
	if event == nil {
		t.Fatal("expected non-nil event")
	}
	if event.FromStatus != TransferStatusCreated {
		t.Errorf("expected from CREATED, got %s", event.FromStatus)
	}
	if event.ToStatus != TransferStatusFunded {
		t.Errorf("expected to FUNDED, got %s", event.ToStatus)
	}
	if event.TransferID != tr.ID {
		t.Error("event TransferID should match transfer ID")
	}
	if event.TenantID != tr.TenantID {
		t.Error("event TenantID should match transfer TenantID")
	}
}

func TestTransitionToInvalidReturnsError(t *testing.T) {
	tr := &Transfer{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Status:   TransferStatusCreated,
		Version:  1,
	}
	event, err := tr.TransitionTo(TransferStatusCompleted)
	if err == nil {
		t.Error("expected error for invalid transition")
	}
	if event != nil {
		t.Error("expected nil event on error")
	}
	if tr.Status != TransferStatusCreated {
		t.Error("status should not change on invalid transition")
	}
	if tr.Version != 1 {
		t.Error("version should not change on invalid transition")
	}
	de, ok := err.(*DomainError)
	if !ok {
		t.Fatalf("expected *DomainError, got %T", err)
	}
	if de.Code() != CodeInvalidTransition {
		t.Errorf("expected code %s, got %s", CodeInvalidTransition, de.Code())
	}
}

func TestTransitionToFullLifecycle(t *testing.T) {
	tr := &Transfer{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Status:   TransferStatusCreated,
		Version:  0,
	}

	lifecycle := []TransferStatus{
		TransferStatusFunded,
		TransferStatusOnRamping,
		TransferStatusSettling,
		TransferStatusOffRamping,
		TransferStatusCompleted,
	}

	for i, target := range lifecycle {
		_, err := tr.TransitionTo(target)
		if err != nil {
			t.Fatalf("step %d: unexpected error transitioning to %s: %v", i, target, err)
		}
		if tr.Status != target {
			t.Fatalf("step %d: expected status %s, got %s", i, target, tr.Status)
		}
	}

	if tr.Version != int64(len(lifecycle)) {
		t.Errorf("expected version %d, got %d", len(lifecycle), tr.Version)
	}
}

func TestTransitionToFailurePath(t *testing.T) {
	tr := &Transfer{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Status:   TransferStatusCreated,
		Version:  0,
	}

	// CREATED → FAILED → REFUNDING → REFUNDED
	steps := []TransferStatus{
		TransferStatusFailed,
		TransferStatusRefunding,
		TransferStatusRefunded,
	}

	for _, target := range steps {
		_, err := tr.TransitionTo(target)
		if err != nil {
			t.Fatalf("unexpected error transitioning to %s: %v", target, err)
		}
	}

	if tr.Status != TransferStatusRefunded {
		t.Errorf("expected REFUNDED, got %s", tr.Status)
	}
}
