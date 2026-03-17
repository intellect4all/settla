package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/intellect4all/settla/domain"
)

// settlementLastSuccess tracks when the last successful settlement tick completed.
// Used by the SettlaSettlementOverdue Prometheus alert.
var settlementLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "settla_settlement_last_success_timestamp",
	Help: "Unix timestamp of the last successful settlement scheduler tick.",
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

// calculateForAllTenants runs CalculateNetSettlement for each NET_SETTLEMENT tenant.
func (s *Scheduler) calculateForAllTenants(ctx context.Context, periodStart, periodEnd time.Time) error {
	var tenants []domain.Tenant
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		tenants, err = s.tenantStore.ListTenantsBySettlementModel(ctx, domain.SettlementModelNetSettlement)
		if err == nil {
			break
		}
		s.logger.Warn("settla-settlement: failed to list tenants, retrying",
			"attempt", attempt+1, "error", err)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	if err != nil {
		s.logger.Error("settla-settlement: CRITICAL - settlement cycle skipped, all retries failed", "error", err)
		return fmt.Errorf("settla-settlement: listing tenants after 3 retries: %w", err)
	}

	s.logger.Info("settla-settlement: processing tenants",
		"tenant_count", len(tenants),
		"period_start", periodStart,
		"period_end", periodEnd,
	)

	var errs []error
	for _, tenant := range tenants {
		if tenant.Status != "ACTIVE" {
			s.logger.Info("settla-settlement: skipping inactive tenant",
				"tenant_id", tenant.ID,
				"tenant_name", tenant.Name,
				"status", string(tenant.Status),
			)
			continue
		}

		_, err := s.calculator.CalculateNetSettlement(ctx, tenant.ID, periodStart, periodEnd)
		if err != nil {
			s.logger.Error("settla-settlement: failed to calculate settlement",
				"tenant_id", tenant.ID,
				"tenant_name", tenant.Name,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("tenant %s: %w", tenant.ID, err))
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("settla-settlement: %d tenants failed: %v", len(errs), errs[0])
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
