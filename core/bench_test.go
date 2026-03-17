package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// benchTransferStore is a thread-safe in-memory transfer store for benchmarks.
type benchTransferStore struct {
	mu         sync.RWMutex
	transfers  map[uuid.UUID]*domain.Transfer
	idempotent map[string]*domain.Transfer
}

func newBenchTransferStore() *benchTransferStore {
	return &benchTransferStore{
		transfers:  make(map[uuid.UUID]*domain.Transfer),
		idempotent: make(map[string]*domain.Transfer),
	}
}

func (s *benchTransferStore) CreateTransfer(_ context.Context, t *domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	t.Version = 1
	s.transfers[t.ID] = t
	if t.IdempotencyKey != "" {
		s.idempotent[fmt.Sprintf("%s:%s", t.TenantID, t.IdempotencyKey)] = t
	}
	return nil
}

func (s *benchTransferStore) GetTransfer(_ context.Context, tenantID, id uuid.UUID) (*domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.transfers[id]
	if !ok {
		return nil, domain.ErrTransferNotFound(id.String())
	}
	if tenantID != uuid.Nil && t.TenantID != tenantID {
		return nil, domain.ErrTransferNotFound(id.String())
	}
	return t, nil
}

func (s *benchTransferStore) GetTransferByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.idempotent[fmt.Sprintf("%s:%s", tenantID, key)]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return t, nil
}

func (s *benchTransferStore) UpdateTransfer(_ context.Context, t *domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transfers[t.ID] = t
	return nil
}

func (s *benchTransferStore) CreateTransferEvent(_ context.Context, _ *domain.TransferEvent) error {
	return nil
}

func (s *benchTransferStore) GetTransferEvents(_ context.Context, _, _ uuid.UUID) ([]domain.TransferEvent, error) {
	return nil, nil
}

func (s *benchTransferStore) GetDailyVolume(_ context.Context, _ uuid.UUID, _ time.Time) (decimal.Decimal, error) {
	return decimal.Zero, nil
}

func (s *benchTransferStore) CreateQuote(_ context.Context, _ *domain.Quote) error {
	return nil
}

func (s *benchTransferStore) GetQuote(_ context.Context, _, _ uuid.UUID) (*domain.Quote, error) {
	return nil, fmt.Errorf("not found")
}

func (s *benchTransferStore) ListTransfers(_ context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []domain.Transfer
	for _, t := range s.transfers {
		if t.TenantID == tenantID {
			result = append(result, *t)
		}
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (s *benchTransferStore) ListTransfersFiltered(_ context.Context, _ uuid.UUID, _, _ string, _ int) ([]domain.Transfer, error) {
	return nil, nil
}

func (s *benchTransferStore) TransitionWithOutbox(_ context.Context, transferID uuid.UUID, newStatus domain.TransferStatus, expectedVersion int64, _ []domain.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.transfers[transferID]
	if !ok {
		return domain.ErrTransferNotFound(transferID.String())
	}
	if t.Version != expectedVersion {
		return domain.ErrOptimisticLock("transfer", transferID.String())
	}
	t.Status = newStatus
	t.Version++
	return nil
}

func (s *benchTransferStore) CreateTransferWithOutbox(_ context.Context, t *domain.Transfer, _ []domain.OutboxEntry) error {
	return s.CreateTransfer(context.Background(), t)
}

func (s *benchTransferStore) GetTransferByExternalRef(_ context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.transfers {
		if t.TenantID == tenantID && t.ExternalRef == externalRef {
			return t, nil
		}
	}
	return nil, fmt.Errorf("transfer not found for external ref %s", externalRef)
}

// setupBenchmarkEngine creates an engine with mock dependencies for benchmarking.
// The engine only needs transferStore, tenantStore, router, logger, and metrics.
func setupBenchmarkEngine(b *testing.B) *Engine {
	b.Helper()

	tenant := activeTenant()
	transfers := newBenchTransferStore()
	tenants := &mockTenantStore{
		getFn: func(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
			if tenantID == tenant.ID {
				return tenant, nil
			}
			return nil, domain.ErrTenantNotFound(tenantID.String())
		},
	}
	router := &mockRouter{}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewEngine(transfers, tenants, router, nil, logger, nil)

	return engine
}

// setupBenchmarkEngineWithTransfer creates an engine and a transfer in CREATED state.
func setupBenchmarkEngineWithTransfer(b *testing.B) (*Engine, uuid.UUID) {
	b.Helper()

	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
	if err != nil {
		b.Fatalf("CreateTransfer: %v", err)
	}

	return engine, transfer.ID
}

// BenchmarkCreateTransfer measures transfer creation performance.
// Includes: tenant lookup, validation, quote fetch, persistence.
//
// Target: <100us per creation
func BenchmarkCreateTransfer(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()
	req := validRequest()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req.IdempotencyKey = fmt.Sprintf("idem-bench-%d", i)
		_, _ = engine.CreateTransfer(ctx, tenant.ID, req)
	}
}

// BenchmarkCreateTransferConcurrent measures creation throughput under load.
//
// Target: >10,000 transfers/sec total
func BenchmarkCreateTransferConcurrent(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()
	req := validRequest()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			r := req
			r.IdempotencyKey = fmt.Sprintf("idem-bench-%d", i)
			_, _ = engine.CreateTransfer(ctx, tenant.ID, r)
			i++
		}
	})
}

// BenchmarkFundTransfer measures funding step performance.
// Now just validation + store call (no network).
//
// Target: <50us per fund operation
func BenchmarkFundTransfer(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	transferIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-fund-%d", i)
		transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			b.Fatalf("CreateTransfer: %v", err)
		}
		transferIDs[i] = transfer.ID
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		transferID := transferIDs[i%100]
		_ = engine.FundTransfer(ctx, tenant.ID, transferID)
	}
}

// BenchmarkInitiateOnRamp measures on-ramp initiation performance.
// Now just validation + outbox write (no provider call).
//
// Target: <100us per on-ramp
func BenchmarkInitiateOnRamp(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	transferIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-onramp-%d", i)
		transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			b.Fatalf("CreateTransfer: %v", err)
		}
		if err := engine.FundTransfer(ctx, tenant.ID, transfer.ID); err != nil {
			b.Fatalf("FundTransfer: %v", err)
		}
		transferIDs[i] = transfer.ID
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		transferID := transferIDs[i%100]
		_ = engine.InitiateOnRamp(ctx, tenant.ID, transferID)
	}
}

// BenchmarkProcessTransfer_FullPipeline measures complete synchronous pipeline.
// Create -> Fund -> OnRamp -> HandleOnRampResult -> HandleSettlementResult -> HandleOffRampResult
//
// Target: <500us per full pipeline (excludes real provider delays)
func BenchmarkProcessTransfer_FullPipeline(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	transferIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-pipeline-%d", i)
		transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			b.Fatalf("CreateTransfer: %v", err)
		}
		transferIDs[i] = transfer.ID
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		transferID := transferIDs[i%100]
		_ = engine.ProcessTransfer(ctx, tenant.ID, transferID)
	}
}

// BenchmarkProcessTransferConcurrent measures pipeline throughput under load.
//
// Target: >2,000 complete pipelines/sec total
func BenchmarkProcessTransferConcurrent(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	transferIDs := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-conc-%d", i)
		transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			b.Fatalf("CreateTransfer: %v", err)
		}
		transferIDs[i] = transfer.ID
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			transferID := transferIDs[i%1000]
			_ = engine.ProcessTransfer(ctx, tenant.ID, transferID)
			i++
		}
	})
}

// BenchmarkGetTransfer measures transfer retrieval performance.
//
// Target: <10us per lookup
func BenchmarkGetTransfer(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	req := validRequest()
	transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
	if err != nil {
		b.Fatalf("CreateTransfer: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = engine.GetTransfer(ctx, tenant.ID, transfer.ID)
	}
}

// BenchmarkGetQuote measures quote generation performance.
//
// Target: <100us per quote
func BenchmarkGetQuote(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	req := domain.QuoteRequest{
		SourceCurrency: domain.CurrencyGBP,
		SourceAmount:   decimal.NewFromInt(1000),
		DestCurrency:   domain.CurrencyNGN,
		DestCountry:    "NG",
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = engine.GetQuote(ctx, tenant.ID, req)
	}
}

// BenchmarkCompleteTransfer measures transfer completion performance.
//
// Target: <100us per completion
func BenchmarkCompleteTransfer(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	// Pre-process transfers through the pipeline
	transferIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-complete-%d", i)
		transfer, err := engine.CreateTransfer(ctx, tenant.ID, req)
		if err != nil {
			b.Fatalf("CreateTransfer: %v", err)
		}
		if err := engine.ProcessTransfer(ctx, tenant.ID, transfer.ID); err != nil {
			b.Fatalf("ProcessTransfer: %v", err)
		}
		transferIDs[i] = transfer.ID
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-bench-complete-%d-%d", i, time.Now().UnixNano())
		transfer, _ := engine.CreateTransfer(ctx, tenant.ID, req)
		_ = engine.ProcessTransfer(ctx, tenant.ID, transfer.ID)
	}
}

// BenchmarkTransferStateTransition measures state transition performance.
//
// Target: <1us per transition
func BenchmarkTransferStateTransition(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		transfer := &domain.Transfer{
			ID:             uuid.New(),
			TenantID:       uuid.New(),
			Status:         domain.TransferStatusCreated,
			Version:        1,
			SourceCurrency: domain.CurrencyGBP,
			SourceAmount:   decimal.NewFromInt(1000),
			DestCurrency:   domain.CurrencyNGN,
			DestAmount:     decimal.NewFromInt(2000000),
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}

		_, _ = transfer.TransitionTo(domain.TransferStatusFunded)
		_, _ = transfer.TransitionTo(domain.TransferStatusOnRamping)
		_, _ = transfer.TransitionTo(domain.TransferStatusSettling)
		_, _ = transfer.TransitionTo(domain.TransferStatusOffRamping)
		_, _ = transfer.TransitionTo(domain.TransferStatusCompleted)
	}
}

// BenchmarkEngineWithIdempotency measures engine with idempotency checking.
//
// Target: <150us (includes cache lookup)
func BenchmarkEngineWithIdempotency(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()
	req := validRequest()

	_, _ = engine.CreateTransfer(ctx, tenant.ID, req)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = engine.CreateTransfer(ctx, tenant.ID, req)
	}
}

// BenchmarkListTransfers measures transfer listing performance.
//
// Target: <10ms for 100 transfers
func BenchmarkListTransfers(b *testing.B) {
	engine := setupBenchmarkEngine(b)
	ctx := context.Background()
	tenant := activeTenant()

	for i := 0; i < 100; i++ {
		req := validRequest()
		req.IdempotencyKey = fmt.Sprintf("idem-list-%d", i)
		_, _ = engine.CreateTransfer(ctx, tenant.ID, req)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = engine.ListTransfers(ctx, tenant.ID, 100, 0)
	}
}
