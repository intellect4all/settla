package domain

import (
	"time"

	"github.com/google/uuid"
)

// ReconciliationCheckResult captures the outcome of a single reconciliation check.
type ReconciliationCheckResult struct {
	Name       string
	Status     string // "pass", "fail", "warn"
	Details    string
	Mismatches int
	CheckedAt  time.Time
}

// ReconciliationReport is the aggregate result of running all reconciliation checks.
type ReconciliationReport struct {
	ID          uuid.UUID
	RunAt       time.Time
	OverallPass bool
	Results     []ReconciliationCheckResult
}
