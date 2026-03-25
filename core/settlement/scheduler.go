package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/intellect4all/settla/domain"
)

const (
	// defaultBatchSize is the number of tenant IDs fetched per paginated query.
	defaultBatchSize int32 = 500
	// defaultWorkers is the number of concurrent goroutines processing settlements.
	defaultWorkers = 32
	// perTenantTimeout is the maximum time allowed for a single tenant's settlement.
	perTenantTimeout = 60 * time.Second
)

// settlementLastSuccess tracks when the last successful settlement tick completed.
// Used by the SettlaSettlementOverdue Prometheus alert.
var settlementLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "settla_settlement_last_success_timestamp",
	Help: "Unix timestamp of the last successful settlement scheduler tick.",
})

var settlementTenantsProcessed = promauto.NewCounter(prometheus.CounterOpts{
	Name: "settla_settlement_tenants_processed_total",
	Help: "Total number of tenants processed (success or failure) by the settlement scheduler.",
})

var settlementTenantsFailed = promauto.NewCounter(prometheus.CounterOpts{
	Name: "settla_settlement_tenants_failed_total",
	Help: "Total number of tenant settlement calculations that failed.",
})

var settlementTickDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "settla_settlement_tick_duration_seconds",
	Help:    "Duration of a complete settlement scheduler tick.",
	Buckets: prometheus.ExponentialBuckets(1, 2, 15), // 1s to ~4.5h
})

// OverdueThresholds defines the escalation policy for overdue net settlements.
var OverdueThresholds = struct {
	Reminder time.Duration // 3 days: send reminder
	Warning  time.Duration // 5 days: send warning
	Suspend  time.Duration // 7 days: suspend tenant
}{
	Reminder: 3 * 24 * time.Hour,
	Warning:  5 * 24 * time.Hour,
	Suspend:  7 * 24 * time.Hour,
}

// OverdueAction represents an action to take on an overdue settlement.
type OverdueAction struct {
	SettlementID string
	TenantID     string
	TenantName   string
	Action       string // "reminder", "warning", "suspend"
	DaysPastDue  int
}

// Scheduler runs daily settlement calculations for all NET_SETTLEMENT tenants
// and tracks overdue payments with escalating actions.
type Scheduler struct {
	calculator  *Calculator
	tenantStore TenantStore
	logger      *slog.Logger
	interval    time.Duration // default 24h
}

// NewScheduler creates a settlement scheduler that runs at the given interval.
func NewScheduler(
	calculator *Calculator,
	tenantStore TenantStore,
	logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		calculator:  calculator,
		tenantStore: tenantStore,
		logger:      logger.With("module", "core.settlement.scheduler"),
		interval:    24 * time.Hour,
	}
}

// SetInterval overrides the default 24h interval (primarily for testing).
func (s *Scheduler) SetInterval(d time.Duration) {
	s.interval = d
}

// Start begins the scheduler loop. It runs the first tick immediately, then
// at each interval. The context controls the lifecycle.
func (s *Scheduler) Start(ctx context.Context) error {
	s.logger.Info("settla-settlement: scheduler starting",
		"interval", s.interval.String(),
	)

	// Run immediately on start
	if err := s.tick(ctx); err != nil {
		s.logger.Error("settla-settlement: initial tick failed", "error", err)
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("settla-settlement: scheduler stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				s.logger.Error("settla-settlement: tick failed", "error", err)
				// Continue running; don't crash on transient errors
			}
		}
	}
}

// RunOnce executes a single settlement calculation cycle. Useful for testing
// and manual invocation.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	return s.tick(ctx)
}

// tick performs a single cycle: calculate yesterday's settlement for all
// NET_SETTLEMENT tenants and check for overdue payments.
func (s *Scheduler) tick(ctx context.Context) error {
	tickStart := time.Now()
	defer func() { settlementTickDuration.Observe(time.Since(tickStart).Seconds()) }()

	s.logger.Info("settla-settlement: scheduler tick starting")

	// Calculate yesterday's settlement window: 00:00 UTC to 00:00 UTC
	now := time.Now().UTC()
	periodEnd := now.Truncate(24 * time.Hour)        // today 00:00 UTC
	periodStart := periodEnd.Add(-24 * time.Hour)     // yesterday 00:00 UTC

	// Process settlements for all NET_SETTLEMENT tenants
	if err := s.calculateForAllTenants(ctx, periodStart, periodEnd); err != nil {
		return fmt.Errorf("settla-settlement: calculating settlements: %w", err)
	}

	// Check for overdue settlements
	actions, err := s.checkOverdue(ctx, now)
	if err != nil {
		return fmt.Errorf("settla-settlement: checking overdue: %w", err)
	}

	if len(actions) > 0 {
		s.logger.Warn("settla-settlement: overdue settlements found",
			"overdue_actions", len(actions),
		)
		for _, action := range actions {
			s.logger.Warn("settla-settlement: overdue action required",
				"settlement_id", action.SettlementID,
				"tenant_id", action.TenantID,
				"tenant_name", action.TenantName,
				"action", action.Action,
				"days_past_due", action.DaysPastDue,
			)
		}
	}

	settlementLastSuccess.SetToCurrentTime()
	s.logger.Info("settla-settlement: scheduler tick completed")
	return nil
}

// calculateForAllTenants processes settlement for all active NET_SETTLEMENT tenants
// using paginated fetching and a bounded worker pool for concurrency.
func (s *Scheduler) calculateForAllTenants(ctx context.Context, periodStart, periodEnd time.Time) error {
	// Get total count for logging
	totalTenants, err := s.tenantStore.CountActiveTenantsBySettlementModel(ctx, domain.SettlementModelNetSettlement)
	if err != nil {
		return fmt.Errorf("settla-settlement: counting tenants: %w", err)
	}

	s.logger.Info("settla-settlement: processing tenants",
		"tenant_count", totalTenants,
		"workers", defaultWorkers,
		"batch_size", defaultBatchSize,
		"period_start", periodStart,
		"period_end", periodEnd,
	)

	if totalTenants == 0 {
		return nil
	}

	// Worker pool: feed tenant IDs through a channel, workers consume concurrently.
	tenantCh := make(chan uuid.UUID, defaultBatchSize)
	var failCount atomic.Int64

	var wg sync.WaitGroup
	for range defaultWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tenantID := range tenantCh {
				if err := s.processOneTenant(ctx, tenantID, periodStart, periodEnd); err != nil {
					failCount.Add(1)
				}
				settlementTenantsProcessed.Inc()
			}
		}()
	}

	// Producer: cursor-paginate through tenant IDs and feed them to the worker channel.
	var afterID uuid.UUID // uuid.Nil for first page
	for {
		ids, err := s.tenantStore.ListActiveTenantIDsBySettlementModel(
			ctx, domain.SettlementModelNetSettlement, defaultBatchSize, afterID,
		)
		if err != nil {
			// Close channel so workers drain and exit
			close(tenantCh)
			wg.Wait()
			return fmt.Errorf("settla-settlement: listing tenant batch after %s: %w", afterID, err)
		}

		for _, id := range ids {
			select {
			case tenantCh <- id:
			case <-ctx.Done():
				close(tenantCh)
				wg.Wait()
				return ctx.Err()
			}
		}

		if int32(len(ids)) < defaultBatchSize {
			break // last page
		}
		afterID = ids[len(ids)-1] // cursor = last ID from this batch
	}

	close(tenantCh)
	wg.Wait()

	failed := failCount.Load()
	succeeded := totalTenants - failed

	s.logger.Info("settla-settlement: batch complete",
		"succeeded", succeeded,
		"failed", failed,
		"total", totalTenants,
	)

	if failed > 0 {
		s.logger.Warn("settla-settlement: some tenants failed during batch calculation",
			"failed", failed,
			"succeeded", succeeded,
			"total", totalTenants,
		)
		// Partial success: don't fail the entire run because of individual tenant errors.
		// Individual failures are already logged with tenant context in processOneTenant.
	}
	return nil
}

// processOneTenant calculates the net settlement for a single tenant with a per-tenant timeout.
func (s *Scheduler) processOneTenant(ctx context.Context, tenantID uuid.UUID, periodStart, periodEnd time.Time) error {
	tenantCtx, cancel := context.WithTimeout(ctx, perTenantTimeout)
	defer cancel()

	_, err := s.calculator.CalculateNetSettlement(tenantCtx, tenantID, periodStart, periodEnd)
	if err != nil {
		s.logger.Error("settla-settlement: failed to calculate settlement",
			"tenant_id", tenantID,
			"error", err,
		)
		settlementTenantsFailed.Inc()
		return err
	}
	return nil
}

// checkOverdue examines pending settlements and returns escalation actions
// for those past their due date.
func (s *Scheduler) checkOverdue(ctx context.Context, now time.Time) ([]OverdueAction, error) {
	pending, err := s.calculator.store.ListPendingSettlements(ctx, domain.AdminCaller{
		Service: "settlement_scheduler",
		Reason:  "check_overdue",
	})
	if err != nil {
		return nil, fmt.Errorf("listing pending settlements: %w", err)
	}

	var actions []OverdueAction
	for _, settlement := range pending {
		if settlement.DueDate == nil {
			continue
		}

		overdue := now.Sub(*settlement.DueDate)
		if overdue <= 0 {
			continue // not yet overdue
		}

		daysPastDue := int(overdue.Hours() / 24)

		var action string
		switch {
		case overdue >= OverdueThresholds.Suspend:
			action = "suspend"
			// Update settlement status to overdue
			if err := s.calculator.store.UpdateSettlementStatus(ctx, settlement.ID, "overdue"); err != nil {
				s.logger.Error("settla-settlement: failed to update settlement status",
					"settlement_id", settlement.ID,
					"error", err,
				)
			}
		case overdue >= OverdueThresholds.Warning:
			action = "warning"
		case overdue >= OverdueThresholds.Reminder:
			action = "reminder"
		default:
			continue // less than 3 days overdue, no action
		}

		actions = append(actions, OverdueAction{
			SettlementID: settlement.ID.String(),
			TenantID:     settlement.TenantID.String(),
			TenantName:   settlement.TenantName,
			Action:       action,
			DaysPastDue:  daysPastDue,
		})
	}

	return actions, nil
}
