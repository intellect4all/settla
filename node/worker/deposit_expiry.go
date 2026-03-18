package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

const (
	// expiryPollInterval is how often the expiry job checks for expired sessions.
	expiryPollInterval = 30 * time.Second

	// expiryBatchSize is the maximum number of expired sessions to process per cycle.
	expiryBatchSize = 100
)

// DepositExpiryStore provides access to expired deposit sessions.
type DepositExpiryStore interface {
	GetExpiredPendingSessions(ctx context.Context, limit int) ([]domain.DepositSession, error)
}

// DepositExpiryEngine abstracts the deposit engine for expiry operations.
type DepositExpiryEngine interface {
	ExpireSession(ctx context.Context, tenantID, sessionID uuid.UUID) error
}

// DepositExpiryJob runs a background goroutine that polls for expired
// PENDING_PAYMENT sessions and calls engine.ExpireSession for each.
type DepositExpiryJob struct {
	store  DepositExpiryStore
	engine DepositExpiryEngine
	logger *slog.Logger
}

// NewDepositExpiryJob creates a new expiry job.
func NewDepositExpiryJob(store DepositExpiryStore, engine DepositExpiryEngine, logger *slog.Logger) *DepositExpiryJob {
	if logger == nil {
		logger = slog.Default()
	}
	return &DepositExpiryJob{
		store:  store,
		engine: engine,
		logger: logger.With("module", "deposit-expiry"),
	}
}

// Run starts the expiry polling loop. Blocks until ctx is cancelled.
func (j *DepositExpiryJob) Run(ctx context.Context) {
	ticker := time.NewTicker(expiryPollInterval)
	defer ticker.Stop()

	j.logger.Info("settla-deposit-expiry: started",
		"poll_interval", expiryPollInterval,
		"batch_size", expiryBatchSize,
	)

	for {
		select {
		case <-ctx.Done():
			j.logger.Info("settla-deposit-expiry: stopped")
			return
		case <-ticker.C:
			j.processBatch(ctx)
		}
	}
}

// processBatch fetches and expires one batch of expired sessions.
func (j *DepositExpiryJob) processBatch(ctx context.Context) {
	sessions, err := j.store.GetExpiredPendingSessions(ctx, expiryBatchSize)
	if err != nil {
		j.logger.Warn("settla-deposit-expiry: failed to get expired sessions",
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
			j.logger.Warn("settla-deposit-expiry: failed to expire session",
				"session_id", session.ID,
				"tenant_id", session.TenantID,
				"error", err,
			)
			continue
		}
		expired++
	}

	j.logger.Info("settla-deposit-expiry: processed batch",
		"total", len(sessions),
		"expired", expired,
	)
}
