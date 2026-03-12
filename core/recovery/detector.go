package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// DetectorMetrics holds Prometheus metrics for the recovery detector.
type DetectorMetrics struct {
	StuckTransfersFound   *prometheus.GaugeVec  // labels: status
	RecoveryAttempts      *prometheus.CounterVec // labels: status, result
	EscalationsCreated    prometheus.Counter
	RecoveryCycleDuration prometheus.Histogram
}

// NewDetectorMetrics creates and registers DetectorMetrics with the given registerer.
func NewDetectorMetrics(reg prometheus.Registerer) *DetectorMetrics {
	m := &DetectorMetrics{
		StuckTransfersFound: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_recovery_stuck_transfers",
			Help: "Number of stuck transfers found per status",
		}, []string{"status"}),
		RecoveryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_recovery_attempts_total",
			Help: "Total recovery attempts",
		}, []string{"status", "result"}),
		EscalationsCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "settla_recovery_escalations_total",
			Help: "Total transfers escalated to manual review",
		}),
		RecoveryCycleDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_recovery_cycle_duration_seconds",
			Help:    "Duration of recovery cycle runs",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.StuckTransfersFound, m.RecoveryAttempts, m.EscalationsCreated, m.RecoveryCycleDuration)
	return m
}

// TransferQueryStore queries transfers stuck in non-terminal states.
type TransferQueryStore interface {
	// ListStuckTransfers returns transfers in the given non-terminal status
	// whose updated_at is older than olderThan.
	ListStuckTransfers(ctx context.Context, status domain.TransferStatus, olderThan time.Time) ([]*domain.Transfer, error)
}

// ReviewStore manages manual review records for escalated stuck transfers.
type ReviewStore interface {
	// CreateManualReview creates a manual review record for a stuck transfer.
	CreateManualReview(ctx context.Context, transferID, tenantID uuid.UUID, transferStatus string, stuckSince time.Time) error
	// HasActiveReview checks whether a transfer already has an open (unresolved) review.
	HasActiveReview(ctx context.Context, transferID uuid.UUID) (bool, error)
}

// RecoveryEngine is the subset of Engine methods needed for automated recovery.
// All methods write outbox entries atomically; they never execute side effects directly.
type RecoveryEngine interface {
	HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	HandleSettlementResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	HandleOffRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	HandleRefundResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error
	FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason, code string) error
	FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
	InitiateOnRamp(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error
}

// ProviderStatusChecker queries external providers and blockchains for
// the current status of in-flight operations.
type ProviderStatusChecker interface {
	// CheckOnRampStatus queries the on-ramp provider for current transaction status.
	CheckOnRampStatus(ctx context.Context, providerID string, transferID uuid.UUID) (*ProviderStatus, error)
	// CheckOffRampStatus queries the off-ramp provider for current transaction status.
	CheckOffRampStatus(ctx context.Context, providerID string, transferID uuid.UUID) (*ProviderStatus, error)
	// CheckBlockchainStatus checks on-chain transaction confirmation status.
	CheckBlockchainStatus(ctx context.Context, chain string, txHash string) (*ChainStatus, error)
}

// ProviderStatus is the result of querying an on-ramp or off-ramp provider.
type ProviderStatus struct {
	Status    string // "completed", "failed", "pending", "unknown"
	Reference string
	Error     string
}

// ChainStatus is the result of querying a blockchain for transaction status.
type ChainStatus struct {
	Confirmed bool
	TxHash    string
	Error     string
}

// Thresholds defines the time thresholds for a given transfer state.
// Warn is logged, Recover triggers automated recovery, Escalate creates
// a manual review record.
type Thresholds struct {
	Warn     time.Duration
	Recover  time.Duration
	Escalate time.Duration
}

// DefaultThresholds defines the default stuck-detection thresholds per state.
var DefaultThresholds = map[domain.TransferStatus]Thresholds{
	domain.TransferStatusFunded:     {Warn: 5 * time.Minute, Recover: 10 * time.Minute, Escalate: 30 * time.Minute},
	domain.TransferStatusOnRamping:  {Warn: 10 * time.Minute, Recover: 15 * time.Minute, Escalate: 60 * time.Minute},
	domain.TransferStatusSettling:   {Warn: 5 * time.Minute, Recover: 10 * time.Minute, Escalate: 30 * time.Minute},
	domain.TransferStatusOffRamping: {Warn: 10 * time.Minute, Recover: 15 * time.Minute, Escalate: 60 * time.Minute},
	domain.TransferStatusRefunding:  {Warn: 10 * time.Minute, Recover: 15 * time.Minute, Escalate: 60 * time.Minute},
}

// Detector is a scheduled job that finds transfers stuck in non-terminal
// states past configurable time thresholds and attempts automated recovery.
// All recovery actions go through the outbox pattern via RecoveryEngine.
type Detector struct {
	store       TransferQueryStore
	reviewStore ReviewStore
	engine      RecoveryEngine
	providers   ProviderStatusChecker
	logger      *slog.Logger
	metrics     *DetectorMetrics
	interval    time.Duration
	thresholds  map[domain.TransferStatus]Thresholds
}

// NewDetector creates a Detector with default configuration.
func NewDetector(
	store TransferQueryStore,
	reviewStore ReviewStore,
	engine RecoveryEngine,
	providers ProviderStatusChecker,
	logger *slog.Logger,
) *Detector {
	return &Detector{
		store:       store,
		reviewStore: reviewStore,
		engine:      engine,
		providers:   providers,
		logger:      logger.With("module", "core.recovery"),
		interval:    60 * time.Second,
		thresholds:  DefaultThresholds,
	}
}

// WithMetrics attaches a DetectorMetrics instance to the Detector.
// If not called, metrics are silently skipped (nil-safe).
func (d *Detector) WithMetrics(m *DetectorMetrics) *Detector {
	d.metrics = m
	return d
}

// WithInterval overrides the default detection interval.
func (d *Detector) WithInterval(interval time.Duration) *Detector {
	d.interval = interval
	return d
}

// WithThresholds overrides the default thresholds.
func (d *Detector) WithThresholds(thresholds map[domain.TransferStatus]Thresholds) *Detector {
	d.thresholds = thresholds
	return d
}

// Run starts the detector loop. It blocks until the context is cancelled.
func (d *Detector) Run(ctx context.Context) error {
	d.logger.Info("settla-recovery: detector starting",
		"interval", d.interval.String(),
	)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("settla-recovery: detector stopping, draining current cycle")
			// Use a separate context with a deadline for the drain phase
			drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := d.runCycle(drainCtx); err != nil {
				d.logger.Warn("settla-recovery: drain cycle failed",
					"error", err,
				)
			}
			d.logger.Info("settla-recovery: detector stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := d.runCycle(ctx); err != nil {
				d.logger.Error("settla-recovery: cycle failed",
					"error", err,
				)
			}
		}
	}
}

// RunOnce executes a single detection and recovery cycle. Exported for testing.
func (d *Detector) RunOnce(ctx context.Context) error {
	return d.runCycle(ctx)
}

func (d *Detector) runCycle(ctx context.Context) error {
	start := time.Now()
	now := time.Now().UTC()
	var totalRecovered, totalEscalated, totalSkipped int

	for status, thresholds := range d.thresholds {
		recoverCutoff := now.Add(-thresholds.Recover)

		transfers, err := d.store.ListStuckTransfers(ctx, status, recoverCutoff)
		if err != nil {
			d.logger.Error("settla-recovery: listing stuck transfers",
				"status", status,
				"error", err,
			)
			continue
		}

		if d.metrics != nil {
			d.metrics.StuckTransfersFound.WithLabelValues(string(status)).Set(float64(len(transfers)))
		}

		for _, transfer := range transfers {
			stuckDuration := now.Sub(transfer.UpdatedAt)

			if stuckDuration >= thresholds.Escalate {
				if err := d.escalate(ctx, transfer, transfer.UpdatedAt); err != nil {
					d.logger.Error("settla-recovery: escalation failed",
						"transfer_id", transfer.ID,
						"tenant_id", transfer.TenantID,
						"status", transfer.Status,
						"error", err,
					)
				} else {
					totalEscalated++
				}
				// Still attempt recovery after escalation
			}

			recovered, err := d.recoverTransfer(ctx, transfer)
			if err != nil {
				d.logger.Error("settla-recovery: recovery failed",
					"transfer_id", transfer.ID,
					"tenant_id", transfer.TenantID,
					"status", transfer.Status,
					"stuck_duration", stuckDuration.String(),
					"error", err,
				)
				if d.metrics != nil {
					d.metrics.RecoveryAttempts.WithLabelValues(string(transfer.Status), "error").Inc()
				}
				continue
			}
			if recovered {
				totalRecovered++
				if d.metrics != nil {
					d.metrics.RecoveryAttempts.WithLabelValues(string(transfer.Status), "recovered").Inc()
				}
			} else {
				totalSkipped++
				if d.metrics != nil {
					d.metrics.RecoveryAttempts.WithLabelValues(string(transfer.Status), "skipped").Inc()
				}
			}
		}
	}

	if d.metrics != nil {
		d.metrics.RecoveryCycleDuration.Observe(time.Since(start).Seconds())
	}

	if totalRecovered > 0 || totalEscalated > 0 {
		d.logger.Info("settla-recovery: cycle complete",
			"recovered", totalRecovered,
			"escalated", totalEscalated,
			"skipped", totalSkipped,
		)
	}

	return nil
}

// recoverTransfer attempts automated recovery for a stuck transfer based on its state.
// Returns true if a recovery action was taken, false if skipped (e.g., pending status).
func (d *Detector) recoverTransfer(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	switch transfer.Status {
	case domain.TransferStatusFunded:
		return d.recoverFunded(ctx, transfer)
	case domain.TransferStatusOnRamping:
		return d.recoverOnRamping(ctx, transfer)
	case domain.TransferStatusSettling:
		return d.recoverSettling(ctx, transfer)
	case domain.TransferStatusOffRamping:
		return d.recoverOffRamping(ctx, transfer)
	case domain.TransferStatusRefunding:
		return d.recoverRefunding(ctx, transfer)
	default:
		return false, nil
	}
}

// recoverFunded re-injects the lost outbox intent for a FUNDED transfer by
// calling InitiateOnRamp (FUNDED → ON_RAMPING). FundTransfer expects CREATED
// status and would fail here; the correct next step is InitiateOnRamp, which
// is exactly what TransferWorker does when it receives EventTransferFunded.
//
// If the transfer has already moved past FUNDED concurrently, InitiateOnRamp
// returns ErrOptimisticLock or ErrInvalidTransition, both treated as "already
// recovered".
func (d *Detector) recoverFunded(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	d.logger.Info("settla-recovery: re-injecting on-ramp intent for stuck FUNDED transfer",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"stuck_since", transfer.UpdatedAt,
	)

	err := d.engine.InitiateOnRamp(ctx, transfer.TenantID, transfer.ID)
	if err == nil {
		return true, nil
	}

	// Optimistic lock means another goroutine already advanced the transfer.
	if errors.Is(err, core.ErrOptimisticLock) {
		d.logger.Info("settla-recovery: FUNDED transfer already advanced (optimistic lock)",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	// Invalid transition means the transfer is no longer in FUNDED state.
	var domErr *domain.DomainError
	if errors.As(err, &domErr) && domErr.Code() == domain.CodeInvalidTransition {
		d.logger.Info("settla-recovery: FUNDED transfer already past FUNDED state",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	// The error message from wrapTransitionError may also contain "concurrent modification".
	if strings.Contains(err.Error(), "concurrent modification") {
		d.logger.Info("settla-recovery: FUNDED transfer already advanced (concurrent modification)",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	return false, fmt.Errorf("settla-recovery: initiating on-ramp for stuck FUNDED transfer %s: %w", transfer.ID, err)
}

// recoverOnRamping queries the on-ramp provider and reconciles the result.
func (d *Detector) recoverOnRamping(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	status, err := d.providers.CheckOnRampStatus(ctx, transfer.OnRampProviderID, transfer.ID)
	if err != nil {
		return false, fmt.Errorf("settla-recovery: checking on-ramp status for transfer %s: %w", transfer.ID, err)
	}

	switch status.Status {
	case "completed":
		d.logger.Info("settla-recovery: on-ramp completed (missed callback), reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"provider_ref", status.Reference,
		)
		err := d.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:     true,
			ProviderRef: status.Reference,
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling on-ramp completion for transfer %s: %w", transfer.ID, err)
		}
		return true, nil

	case "failed":
		d.logger.Warn("settla-recovery: on-ramp failed (missed callback), reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", status.Error,
		)
		err := d.engine.HandleOnRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:   false,
			Error:     status.Error,
			ErrorCode: "provider_onramp_failed",
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling on-ramp failure for transfer %s: %w", transfer.ID, err)
		}
		return true, nil

	case "pending", "unknown":
		d.logger.Debug("settla-recovery: on-ramp still pending, skipping",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
		)
		return false, nil

	default:
		d.logger.Warn("settla-recovery: unknown on-ramp status",
			"transfer_id", transfer.ID,
			"status", status.Status,
		)
		return false, nil
	}
}

// recoverSettling queries the blockchain for the settlement transaction status.
func (d *Detector) recoverSettling(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	// Find the tx hash from the transfer's blockchain transactions
	var txHash string
	for _, tx := range transfer.BlockchainTxs {
		if tx.Type == "settlement" {
			txHash = tx.TxHash
			break
		}
	}

	if txHash == "" {
		d.logger.Warn("settla-recovery: no settlement tx hash found for stuck SETTLING transfer",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
		)
		return false, nil
	}

	status, err := d.providers.CheckBlockchainStatus(ctx, transfer.Chain, txHash)
	if err != nil {
		return false, fmt.Errorf("settla-recovery: checking blockchain status for transfer %s: %w", transfer.ID, err)
	}

	if status.Confirmed {
		d.logger.Info("settla-recovery: blockchain settlement confirmed (missed event), reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"tx_hash", status.TxHash,
		)
		err := d.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success: true,
			TxHash:  status.TxHash,
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling settlement confirmation for transfer %s: %w", transfer.ID, err)
		}
		return true, nil
	}

	if status.Error != "" {
		d.logger.Warn("settla-recovery: blockchain settlement failed, reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", status.Error,
		)
		err := d.engine.HandleSettlementResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:   false,
			Error:     status.Error,
			ErrorCode: "blockchain_settlement_failed",
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling settlement failure for transfer %s: %w", transfer.ID, err)
		}
		return true, nil
	}

	// Still pending
	d.logger.Debug("settla-recovery: blockchain settlement still pending, skipping",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
	)
	return false, nil
}

// recoverOffRamping queries the off-ramp provider and reconciles the result.
func (d *Detector) recoverOffRamping(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	status, err := d.providers.CheckOffRampStatus(ctx, transfer.OffRampProviderID, transfer.ID)
	if err != nil {
		return false, fmt.Errorf("settla-recovery: checking off-ramp status for transfer %s: %w", transfer.ID, err)
	}

	switch status.Status {
	case "completed":
		d.logger.Info("settla-recovery: off-ramp completed (missed callback), reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"provider_ref", status.Reference,
		)
		err := d.engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:     true,
			ProviderRef: status.Reference,
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling off-ramp completion for transfer %s: %w", transfer.ID, err)
		}
		return true, nil

	case "failed":
		d.logger.Warn("settla-recovery: off-ramp failed (missed callback), reconciling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", status.Error,
		)
		err := d.engine.HandleOffRampResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
			Success:   false,
			Error:     status.Error,
			ErrorCode: "provider_offramp_failed",
		})
		if err != nil {
			return false, fmt.Errorf("settla-recovery: handling off-ramp failure for transfer %s: %w", transfer.ID, err)
		}
		return true, nil

	case "pending", "unknown":
		d.logger.Debug("settla-recovery: off-ramp still pending, skipping",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
		)
		return false, nil

	default:
		d.logger.Warn("settla-recovery: unknown off-ramp status",
			"transfer_id", transfer.ID,
			"status", status.Status,
		)
		return false, nil
	}
}

// recoverRefunding attempts to complete a transfer stuck in REFUNDING state by
// calling HandleRefundResult(success=true). If the treasury release and ledger
// reversal intents were already processed (common case), this advances the
// transfer to FAILED (terminal). If the transfer has already moved past REFUNDING
// concurrently, optimistic lock / invalid transition errors are treated as
// "already recovered".
func (d *Detector) recoverRefunding(ctx context.Context, transfer *domain.Transfer) (bool, error) {
	d.logger.Info("settla-recovery: re-injecting refund completion for stuck REFUNDING transfer",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"stuck_since", transfer.UpdatedAt,
	)

	err := d.engine.HandleRefundResult(ctx, transfer.TenantID, transfer.ID, domain.IntentResult{
		Success: true,
	})
	if err == nil {
		return true, nil
	}

	// Optimistic lock or invalid transition means the transfer already advanced.
	if errors.Is(err, core.ErrOptimisticLock) {
		d.logger.Info("settla-recovery: REFUNDING transfer already advanced (optimistic lock)",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	var domErr *domain.DomainError
	if errors.As(err, &domErr) && domErr.Code() == domain.CodeInvalidTransition {
		d.logger.Info("settla-recovery: REFUNDING transfer already past REFUNDING state",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	if strings.Contains(err.Error(), "concurrent modification") {
		d.logger.Info("settla-recovery: REFUNDING transfer already advanced (concurrent modification)",
			"transfer_id", transfer.ID,
		)
		return true, nil
	}

	return false, fmt.Errorf("settla-recovery: handling refund result for stuck REFUNDING transfer %s: %w", transfer.ID, err)
}
