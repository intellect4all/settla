package compensation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// ExecutorMetrics holds Prometheus metrics for the compensation executor.
type ExecutorMetrics struct {
	CompensationsStarted   *prometheus.CounterVec // labels: strategy
	CompensationsCompleted *prometheus.CounterVec // labels: strategy, result
	CompensationDuration   prometheus.Histogram
}

// NewExecutorMetrics creates and registers ExecutorMetrics with the given registerer.
func NewExecutorMetrics(reg prometheus.Registerer) *ExecutorMetrics {
	m := &ExecutorMetrics{
		CompensationsStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_compensation_started_total",
			Help: "Total compensation executions started",
		}, []string{"strategy"}),
		CompensationsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_compensation_completed_total",
			Help: "Total compensation executions completed",
		}, []string{"strategy", "result"}),
		CompensationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_compensation_duration_seconds",
			Help:    "Duration of compensation executions",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.CompensationsStarted, m.CompensationsCompleted, m.CompensationDuration)
	return m
}

// CompensationStore persists compensation records for audit and tracking.
type CompensationStore interface {
	CreateCompensationRecord(ctx context.Context, params CreateCompensationParams) (uuid.UUID, error)
	UpdateCompensationRecord(ctx context.Context, id uuid.UUID, stepsCompleted, stepsFailed []byte, fxLoss decimal.Decimal, status string) error
}

// CreateCompensationParams are the parameters for creating a compensation record.
type CreateCompensationParams struct {
	TransferID     uuid.UUID
	TenantID       uuid.UUID
	Strategy       string
	RefundAmount   decimal.Decimal
	RefundCurrency string
}

// CompensationEngine is the interface into the core engine for executing
// refund and failure transitions. The compensation executor uses this to
// trigger state changes that produce outbox entries for workers.
type CompensationEngine interface {
	InitiateRefund(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
	FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason, code string) error
	HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
}

// Executor takes a CompensationPlan and orchestrates its execution. It creates
// a compensation record, delegates to the engine for state transitions (which
// produce outbox intents for workers), and tracks step completion.
type Executor struct {
	store   CompensationStore
	engine  CompensationEngine
	logger  *slog.Logger
	metrics *ExecutorMetrics
}

// NewExecutor creates a compensation executor.
func NewExecutor(store CompensationStore, engine CompensationEngine, logger *slog.Logger) *Executor {
	return &Executor{
		store:  store,
		engine: engine,
		logger: logger.With("module", "core.compensation"),
	}
}

// WithMetrics attaches an ExecutorMetrics instance to the Executor.
// If not called, metrics are silently skipped (nil-safe).
func (e *Executor) WithMetrics(m *ExecutorMetrics) *Executor {
	e.metrics = m
	return e
}

// Execute runs the compensation plan:
//  1. Creates a compensation_records entry with status "in_progress".
//  2. Delegates to the engine based on the strategy.
//  3. Tracks completed/failed steps in the compensation record.
//  4. Updates the record status to "completed" or "failed".
func (e *Executor) Execute(ctx context.Context, plan CompensationPlan) error {
	start := time.Now()
	strategy := string(plan.Strategy)

	if e.metrics != nil {
		e.metrics.CompensationsStarted.WithLabelValues(strategy).Inc()
	}

	// Create compensation record.
	recordID, err := e.store.CreateCompensationRecord(ctx, CreateCompensationParams{
		TransferID:     plan.TransferID,
		TenantID:       plan.TenantID,
		Strategy:       strategy,
		RefundAmount:   plan.RefundAmount,
		RefundCurrency: string(plan.RefundCurrency),
	})
	if err != nil {
		return fmt.Errorf("settla-compensation: creating compensation record for transfer %s: %w", plan.TransferID, err)
	}

	e.logger.Info("settla-compensation: executing plan",
		"compensation_id", recordID,
		"transfer_id", plan.TransferID,
		"tenant_id", plan.TenantID,
		"strategy", plan.Strategy,
	)

	var stepsCompleted []string
	var stepsFailed []string

	switch plan.Strategy {
	case StrategySimpleRefund:
		err = e.executeSimpleRefund(ctx, plan)
		if err != nil {
			stepsFailed = append(stepsFailed, "initiate_refund")
		} else {
			stepsCompleted = append(stepsCompleted, "treasury.release", "ledger.reverse")
		}

	case StrategyReverseOnRamp:
		err = e.executeReverseOnRamp(ctx, plan)
		if err != nil {
			stepsFailed = append(stepsFailed, "reverse_onramp")
		} else {
			stepsCompleted = append(stepsCompleted, "provider.reverse_onramp", "treasury.release", "ledger.reverse")
		}

	case StrategyCreditStablecoin:
		err = e.executeCreditStablecoin(ctx, plan)
		if err != nil {
			stepsFailed = append(stepsFailed, "credit_stablecoin")
		} else {
			stepsCompleted = append(stepsCompleted, "position.credit", "treasury.release")
		}

	case StrategyManualReview:
		// No automated steps — just record it.
		e.logger.Warn("settla-compensation: manual review required",
			"transfer_id", plan.TransferID,
			"tenant_id", plan.TenantID,
		)
	}

	// Determine final status.
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if plan.Strategy == StrategyManualReview {
		status = "pending_review"
	}

	// Persist step tracking.
	completedJSON, _ := json.Marshal(stepsCompleted)
	failedJSON, _ := json.Marshal(stepsFailed)

	updateErr := e.store.UpdateCompensationRecord(ctx, recordID, completedJSON, failedJSON, plan.FXLoss, status)
	if updateErr != nil {
		e.logger.Error("settla-compensation: failed to update compensation record",
			"compensation_id", recordID,
			"error", updateErr,
		)
		// Don't mask the original error if there was one.
		if err == nil {
			return fmt.Errorf("settla-compensation: updating compensation record %s: %w", recordID, updateErr)
		}
	}

	// Record metrics after determining final status.
	if e.metrics != nil {
		result := status
		e.metrics.CompensationsCompleted.WithLabelValues(strategy, result).Inc()
		e.metrics.CompensationDuration.Observe(time.Since(start).Seconds())
	}

	if err != nil {
		return fmt.Errorf("settla-compensation: executing %s for transfer %s: %w", plan.Strategy, plan.TransferID, err)
	}

	e.logger.Info("settla-compensation: plan executed",
		"compensation_id", recordID,
		"transfer_id", plan.TransferID,
		"status", status,
		"strategy", plan.Strategy,
	)

	return nil
}

// executeSimpleRefund delegates to engine.InitiateRefund, which writes outbox
// intents for treasury.release and ledger.reverse. If the transfer is not yet
// in FAILED state (e.g. ON_RAMPING with provider "failed"), it calls
// FailTransfer first to transition to FAILED before initiating the refund.
func (e *Executor) executeSimpleRefund(ctx context.Context, plan CompensationPlan) error {
	if plan.TransferStatus != domain.TransferStatusFailed {
		if err := e.engine.FailTransfer(ctx, plan.TenantID, plan.TransferID,
			"provider confirmed failure during compensation",
			"COMPENSATION_SIMPLE_REFUND",
		); err != nil {
			return fmt.Errorf("settla-compensation: failing transfer before refund: %w", err)
		}
	}
	return e.engine.InitiateRefund(ctx, plan.TenantID, plan.TransferID)
}

// executeReverseOnRamp fails the transfer with a specific reason, which causes
// the engine to write outbox intents for treasury release and ledger reversal.
// The reverse on-ramp provider call is embedded as a step in the plan and will
// be picked up by workers from the compensation record's steps.
func (e *Executor) executeReverseOnRamp(ctx context.Context, plan CompensationPlan) error {
	return e.engine.FailTransfer(ctx, plan.TenantID, plan.TransferID,
		"off-ramp failed, reversing on-ramp",
		"COMPENSATION_REVERSE_ONRAMP",
	)
}

// executeCreditStablecoin fails the transfer with a reason indicating stablecoin
// credit compensation. The position credit step is tracked in the compensation
// record and executed by workers.
func (e *Executor) executeCreditStablecoin(ctx context.Context, plan CompensationPlan) error {
	return e.engine.FailTransfer(ctx, plan.TenantID, plan.TransferID,
		"off-ramp failed, crediting stablecoin position",
		"COMPENSATION_CREDIT_STABLECOIN",
	)
}
