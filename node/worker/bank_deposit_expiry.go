package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

const (
	// bankExpiryPollInterval is how often the expiry job checks for expired bank deposit sessions.
	bankExpiryPollInterval = 30 * time.Second

	// bankExpiryBatchSize is the maximum number of expired sessions to process per cycle.
	bankExpiryBatchSize = 100
)

// BankDepositExpiryStore provides access to expired bank deposit sessions.
type BankDepositExpiryStore interface {
	GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.BankDepositSession, error)
}

// BankDepositExpiryEngine abstracts the bank deposit engine for expiry operations.
type BankDepositExpiryEngine interface {
	ExpireSession(ctx context.Context, tenantID, sessionID uuid.UUID) error
}

// BankDepositExpiryJob runs a background goroutine that polls for expired
// PENDING_PAYMENT bank deposit sessions and calls engine.ExpireSession for each.
type BankDepositExpiryJob struct {
	store  BankDepositExpiryStore
	engine BankDepositExpiryEngine
	logger *slog.Logger
}

// NewBankDepositExpiryJob creates a new bank deposit expiry job.
func NewBankDepositExpiryJob(store BankDepositExpiryStore, engine BankDepositExpiryEngine, logger *slog.Logger) *BankDepositExpiryJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &BankDepositExpiryJob{
		store:  store,
		engine: engine,
		logger: logger.With("module", "bank-deposit-expiry"),
	}
}

// Run starts the expiry polling loop. Blocks until ctx is cancelled.
func (j *BankDepositExpiryJob) Run(ctx context.Context) {
	ticker := time.NewTicker(bankExpiryPollInterval)
	defer ticker.Stop()

	j.logger.Info("settla-bank-deposit-expiry: started",
		"poll_interval", bankExpiryPollInterval,
		"batch_size", bankExpiryBatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			j.logger.Info("settla-bank-deposit-expiry: stopped")
			return
		case <-ticker.C:
			j.processBatch(ctx)
		}
	}
}

// processBatch fetches and expires one batch of expired bank deposit sessions.
func (j *BankDepositExpiryJob) processBatch(ctx context.Context) {
	sessions, err := j.store.GetExpiredPendingSessions(ctx, bankExpiryBatchSize)
	if err != nil {
		j.logger.Warn("settla-bank-deposit-expiry: failed to get expired sessions",
			"error", err,
		)
		return
	}

	if len(sessions) == 0 {
		return
	}

	expired := 0
	for _, session := range sessions {
		if err := j.engine.ExpireSession(ctx, session.TenantID, session.ID); err != nil {
			j.logger.Warn("settla-bank-deposit-expiry: failed to expire session",
				"session_id", session.ID,
				"tenant_id", session.TenantID,
				"error", err,
			)
			continue
		}
		expired++
	}

	j.logger.Info("settla-bank-deposit-expiry: processed batch",
		"total", len(sessions),
		"expired", expired,
	)
}
