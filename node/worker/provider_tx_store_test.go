package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

func TestInMemoryProviderTransferStore_GetNotFound(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	tx, err := store.GetProviderTransaction(context.Background(), uuid.New(), uuid.New(), "onramp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx != nil {
		t.Errorf("expected nil for non-existent key, got %+v", tx)
	}
}

func TestInMemoryProviderTransferStore_CreateAndGet(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	transferID := uuid.New()
	txType := "onramp"
	ptx := &domain.ProviderTx{
		ID:     uuid.New().String(),
		Status: "pending",
	}

	if err := store.CreateProviderTransaction(context.Background(), transferID, txType, ptx); err != nil {
		t.Fatalf("CreateProviderTransaction failed: %v", err)
	}

	got, err := store.GetProviderTransaction(context.Background(), uuid.New(), transferID, txType)
	if err != nil {
		t.Fatalf("GetProviderTransaction failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.ID != ptx.ID {
		t.Errorf("ID = %q, want %q", got.ID, ptx.ID)
	}
	if got.Status != "pending" {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestInMemoryProviderTransferStore_Update(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	transferID := uuid.New()
	txType := "offramp"
	ptx := &domain.ProviderTx{ID: uuid.New().String(), Status: "pending"}

	if err := store.CreateProviderTransaction(context.Background(), transferID, txType, ptx); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updated := &domain.ProviderTx{ID: ptx.ID, Status: "completed"}
	if err := store.UpdateProviderTransaction(context.Background(), transferID, txType, updated); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, transferID, txType)
	if got == nil {
		t.Fatal("expected non-nil after update")
	}
	if got.Status != "completed" {
		t.Errorf("Status after update = %q, want completed", got.Status)
	}
}

func TestInMemoryProviderTransferStore_Delete(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	transferID := uuid.New()
	txType := "onramp"
	ptx := &domain.ProviderTx{ID: uuid.New().String(), Status: "pending"}

	_ = store.CreateProviderTransaction(context.Background(), transferID, txType, ptx)
	if err := store.DeleteProviderTransaction(context.Background(), transferID, txType); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, transferID, txType)
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestInMemoryProviderTransferStore_Claim(t *testing.T) {
	tests := []struct {
		name       string
		existing   *domain.ProviderTx
		wantClaim  bool
	}{
		{
			name:      "fresh claim succeeds",
			existing:  nil,
			wantClaim: true,
		},
		{
			name:      "completed returns nil",
			existing:  &domain.ProviderTx{ID: "x", Status: "completed"},
			wantClaim: false,
		},
		{
			name:      "confirmed returns nil",
			existing:  &domain.ProviderTx{ID: "x", Status: "confirmed"},
			wantClaim: false,
		},
		{
			name:      "pending returns nil",
			existing:  &domain.ProviderTx{ID: "x", Status: "pending"},
			wantClaim: false,
		},
		{
			name:      "failed allows re-claim",
			existing:  &domain.ProviderTx{ID: "x", Status: "failed"},
			wantClaim: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewInMemoryProviderTransferStore()
			transferID := uuid.New()
			txType := "onramp"

			if tt.existing != nil {
				_ = store.CreateProviderTransaction(context.Background(), transferID, txType, tt.existing)
			}

			id, err := store.ClaimProviderTransaction(context.Background(), ClaimProviderTransactionParams{
				TenantID:   uuid.New(),
				TransferID: transferID,
				TxType:     txType,
				Provider:   "test",
			})
			if err != nil {
				t.Fatalf("ClaimProviderTransaction error: %v", err)
			}

			if tt.wantClaim && id == nil {
				t.Error("expected successful claim (non-nil ID)")
			}
			if !tt.wantClaim && id != nil {
				t.Errorf("expected nil claim, got %v", id)
			}
		})
	}
}

func TestInMemoryProviderTransferStore_ConcurrentClaims(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	transferID := uuid.New()
	txType := "onramp"

	const goroutines = 100
	var wg sync.WaitGroup
	var errCount atomic.Int32

	// First claim, then mark as completed so subsequent claims are blocked.
	id, err := store.ClaimProviderTransaction(context.Background(), ClaimProviderTransactionParams{
		TenantID:   uuid.New(),
		TransferID: transferID,
		TxType:     txType,
		Provider:   "test",
	})
	if err != nil || id == nil {
		t.Fatalf("initial claim failed: err=%v, id=%v", err, id)
	}
	_ = store.UpdateProviderTransaction(context.Background(), transferID, txType,
		&domain.ProviderTx{ID: id.String(), Status: "completed"})

	// Now 100 goroutines try to claim the same key — all should get nil (already completed).
	var successCount atomic.Int32
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			claimID, claimErr := store.ClaimProviderTransaction(context.Background(), ClaimProviderTransactionParams{
				TenantID:   uuid.New(),
				TransferID: transferID,
				TxType:     txType,
				Provider:   "test",
			})
			if claimErr != nil {
				errCount.Add(1)
				return
			}
			if claimID != nil {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("expected 0 errors, got %d", errCount.Load())
	}
	if successCount.Load() != 0 {
		t.Errorf("expected 0 successful claims (already completed), got %d", successCount.Load())
	}
}

func TestInMemoryProviderTransferStore_KeyIsolation(t *testing.T) {
	store := NewInMemoryProviderTransferStore()
	id1 := uuid.New()
	id2 := uuid.New()

	ptx1 := &domain.ProviderTx{ID: "tx1", Status: "pending"}
	ptx2 := &domain.ProviderTx{ID: "tx2", Status: "completed"}

	_ = store.CreateProviderTransaction(context.Background(), id1, "onramp", ptx1)
	_ = store.CreateProviderTransaction(context.Background(), id2, "onramp", ptx2)
	_ = store.CreateProviderTransaction(context.Background(), id1, "offramp", &domain.ProviderTx{ID: "tx3", Status: "failed"})

	// Each key should be independent.
	got1, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, id1, "onramp")
	got2, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, id2, "onramp")
	got3, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, id1, "offramp")

	if got1 == nil || got1.ID != "tx1" {
		t.Errorf("id1:onramp = %v, want tx1", got1)
	}
	if got2 == nil || got2.ID != "tx2" {
		t.Errorf("id2:onramp = %v, want tx2", got2)
	}
	if got3 == nil || got3.ID != "tx3" {
		t.Errorf("id1:offramp = %v, want tx3", got3)
	}

	// Non-existent combo should be nil.
	got4, _ := store.GetProviderTransaction(context.Background(), uuid.Nil, id2, "offramp")
	if got4 != nil {
		t.Errorf("id2:offramp should be nil, got %+v", got4)
	}
}
