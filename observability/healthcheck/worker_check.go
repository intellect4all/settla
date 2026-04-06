package healthcheck

import (
	"context"
	"fmt"
	"time"
)

// WorkerLivenessSource provides liveness signals from a message consumer.
type WorkerLivenessSource interface {
	// LastProcessedAt returns the time the last message was processed, or zero.
	LastProcessedAt() time.Time
}

// WorkerLivenessCheck detects stuck message consumers. A consumer is considered
// stuck if it has not processed any messages within maxIdleTime while the system
// has been running long enough for messages to flow (i.e., past startup).
//
// This check is designed to catch scenarios where a NATS consumer subscription
// silently disconnects or a handler deadlocks.
type WorkerLivenessCheck struct {
	name        string
	sources     []WorkerLivenessSource
	maxIdleTime time.Duration
	startedAt   time.Time
}

// NewWorkerLivenessCheck creates a worker liveness health check.
// maxIdleTime is the maximum duration a worker can go without processing a
// message before being considered stuck (default recommendation: 5 minutes).
func NewWorkerLivenessCheck(name string, sources []WorkerLivenessSource, maxIdleTime time.Duration) *WorkerLivenessCheck {
	return &WorkerLivenessCheck{
		name:        name,
		sources:     sources,
		maxIdleTime: maxIdleTime,
		startedAt:   time.Now(),
	}
}

func (c *WorkerLivenessCheck) Name() string  { return c.name }
func (c *WorkerLivenessCheck) Optional() bool { return false }

func (c *WorkerLivenessCheck) Check(_ context.Context) error {
	// Don't flag workers during startup grace period.
	if time.Since(c.startedAt) < c.maxIdleTime {
		return nil
	}

	for i, src := range c.sources {
		last := src.LastProcessedAt()
		if last.IsZero() {
			// Worker has never processed a message — could be idle partition.
			// Only fail if enough time has passed since startup.
			if time.Since(c.startedAt) > 2*c.maxIdleTime {
				return fmt.Errorf("worker %d has never processed a message (started %s ago)", i, time.Since(c.startedAt).Truncate(time.Second))
			}
			continue
		}
		idle := time.Since(last)
		if idle > c.maxIdleTime {
			return fmt.Errorf("worker %d stuck: last processed %s ago (threshold %s)", i, idle.Truncate(time.Second), c.maxIdleTime)
		}
	}
	return nil
}
