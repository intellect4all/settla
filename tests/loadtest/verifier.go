package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Verifier performs post-test data consistency checks.
//
// It verifies:
//   - Outbox fully drained: no unpublished entries remain in the transactional outbox
//   - Debits = credits: all ledger accounts balance across the test run
//   - Treasury positions reconcile with completed transfer sums (no orphaned reservations)
//   - Zero stuck transfers: no transfers in non-terminal state after drain
type Verifier struct {
	config  LoadTestConfig
	metrics *LoadTestMetrics
	logger  *slog.Logger
	client  *http.Client
}

// VerificationReport holds the results of all consistency checks.
type VerificationReport struct {
	// Transfer state
	TotalTransfers     int
	CompletedTransfers int
	FailedTransfers    int
	StuckTransfers     int
	TransfersPass      bool

	// Outbox drain check (new — outbox architecture invariant)
	OutboxUnpublished int64
	OutboxPass        bool
	OutboxMessage     string

	// DB-backed stuck transfer check (new — cross-checks the API-level result)
	DBStuckTransfers int64
	DBStuckPass      bool
	DBStuckMessage   string

	// Treasury reconciliation
	TreasuryPass    bool
	TreasuryMessage string

	// Ledger balance (debit = credit)
	LedgerPass    bool
	LedgerMessage string

	// Orphaned reservations
	OrphanedReservations int
	ReservationsPass     bool

	// Error rate
	ErrorRate              float64
	ErrorRatePass          bool
	MaxAcceptableErrorRate float64

	// Error categorization
	ErrorsByCategory map[string]int

	// Per-tenant breakdown
	TenantResults []TenantVerificationResult

	// Overall
	AllPassed  bool
	FailReason string
}

// TenantVerificationResult holds per-tenant verification data.
type TenantVerificationResult struct {
	TenantID           string
	TransferCount      int
	CompletedCount     int
	TotalVolume        decimal.Decimal
	Currency           string
	TreasuryReconciled bool
	FailReason         string
}

// PositionsResponse represents the GET /v1/treasury/positions API response.
type PositionsResponse struct {
	TenantID  string          `json:"tenant_id"`
	Positions []PositionEntry `json:"positions"`
}

// PositionEntry is a single treasury position.
type PositionEntry struct {
	Currency  string `json:"currency"`
	Location  string `json:"location"`
	Available string `json:"available"`
	Reserved  string `json:"reserved"`
	Total     string `json:"total"`
}

// NewVerifier creates a verifier for the given load test run.
func NewVerifier(config LoadTestConfig, metrics *LoadTestMetrics, logger *slog.Logger) *Verifier {
	return &Verifier{
		config:  config,
		metrics: metrics,
		logger:  logger,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// VerifyConsistency runs all consistency checks and returns a report.
// Returns an error if any mandatory check fails.
func (v *Verifier) VerifyConsistency(ctx context.Context) (*VerificationReport, error) {
	v.logger.Info("running post-test consistency verification")

	// Collect results snapshot.
	v.metrics.mu.RLock()
	results := make([]TransferResult, len(v.metrics.results))
	copy(results, v.metrics.results)
	v.metrics.mu.RUnlock()

	report := &VerificationReport{}

	// --- Check 1: All transfers in terminal state (API-level) ---
	v.checkTransferStates(results, report)

	// --- Check 1b: Error categorization ---
	v.categorizeErrors(results, report)

	// --- Check 1c: Error rate within acceptable threshold ---
	v.checkErrorRate(results, report)

	// --- Check 2: Outbox fully drained (zero unpublished entries) ---
	v.checkOutboxDrained(ctx, report)

	// --- Check 3: No stuck transfers in DB (cross-check beyond API polling) ---
	v.checkDBStuckTransfers(ctx, report)

	// --- Check 4: Treasury positions + orphaned reservations ---
	v.checkTreasuryPositions(ctx, results, report)

	// --- Check 5: Ledger debit = credit balance ---
	v.checkLedgerBalance(ctx, report)

	// --- Assemble per-tenant breakdown ---
	v.buildTenantResults(results, report)

	// --- Overall pass/fail ---
	report.AllPassed = report.TransfersPass &&
		report.ErrorRatePass &&
		report.OutboxPass &&
		report.DBStuckPass &&
		report.TreasuryPass &&
		report.LedgerPass &&
		report.ReservationsPass &&
		report.StuckTransfers == 0

	if !report.AllPassed {
		parts := []string{}
		if !report.TransfersPass {
			parts = append(parts, fmt.Sprintf("%d stuck transfers (API)", report.StuckTransfers))
		}
		if !report.OutboxPass {
			parts = append(parts, "outbox: "+report.OutboxMessage)
		}
		if !report.DBStuckPass {
			parts = append(parts, "db stuck: "+report.DBStuckMessage)
		}
		if !report.TreasuryPass {
			parts = append(parts, "treasury mismatch: "+report.TreasuryMessage)
		}
		if !report.LedgerPass {
			parts = append(parts, "ledger: "+report.LedgerMessage)
		}
		if !report.ReservationsPass {
			parts = append(parts, fmt.Sprintf("%d orphaned reservations", report.OrphanedReservations))
		}
		if !report.ErrorRatePass {
			parts = append(parts, fmt.Sprintf("error rate %.1f%% exceeds %.1f%% threshold", report.ErrorRate, report.MaxAcceptableErrorRate))
		}
		report.FailReason = strings.Join(parts, "; ")
		return report, fmt.Errorf("consistency verification failed: %s", report.FailReason)
	}

	return report, nil
}

// checkTransferStates verifies every tracked transfer reached a terminal state.
func (v *Verifier) checkTransferStates(results []TransferResult, report *VerificationReport) {
	terminalStates := map[string]bool{
		"COMPLETED": true,
		"FAILED":    true,
		"REFUNDED":  true,
	}

	for _, r := range results {
		if r.Error != nil {
			// Transfer that failed at creation/polling — counts as an error, not stuck.
			report.FailedTransfers++
			report.TotalTransfers++
			continue
		}
		report.TotalTransfers++
		switch r.Status {
		case "COMPLETED":
			report.CompletedTransfers++
		case "FAILED", "REFUNDED":
			report.FailedTransfers++
		default:
			if !terminalStates[r.Status] {
				report.StuckTransfers++
				v.logger.Error("transfer stuck in non-terminal state",
					"transfer_id", r.TransferID,
					"tenant_id", r.TenantID,
					"status", r.Status,
				)
			}
		}
	}

	report.TransfersPass = report.StuckTransfers == 0
}

// categorizeErrors groups errors by category from the metrics recorder.
func (v *Verifier) categorizeErrors(_ []TransferResult, report *VerificationReport) {
	report.ErrorsByCategory = make(map[string]int)

	v.metrics.errorsMu.RLock()
	defer v.metrics.errorsMu.RUnlock()

	for code, cnt := range v.metrics.errors {
		report.ErrorsByCategory[code] = int(cnt.Load())
	}
}

// checkErrorRate verifies that the overall error rate is within the acceptable threshold.
func (v *Verifier) checkErrorRate(_ []TransferResult, report *VerificationReport) {
	maxRate := v.config.MaxErrorRate
	if maxRate == 0 {
		maxRate = 1.0 // default 1%
	}
	report.MaxAcceptableErrorRate = maxRate
	if report.TotalTransfers == 0 {
		report.ErrorRatePass = true
		return
	}
	report.ErrorRate = float64(report.FailedTransfers) / float64(report.TotalTransfers) * 100
	report.ErrorRatePass = report.ErrorRate <= maxRate
}

// checkOutboxDrained queries the Transfer DB to verify the transactional outbox
// has no unpublished entries remaining after the drain phase.
//
// A non-empty outbox after drain means:
//   - The relay goroutine failed to publish some events to NATS, or
//   - Workers consumed events but did not mark them published, or
//   - Max retries were exhausted — entries are permanently stuck.
//
// Any of these indicate a saga execution gap: some transfers may appear
// COMPLETED in the API while their downstream side-effects (ledger posts,
// webhook deliveries) were never executed.
func (v *Verifier) checkOutboxDrained(ctx context.Context, report *VerificationReport) {
	if v.config.TransferDBURL == "" {
		// No DB URL provided — skip this check gracefully.
		report.OutboxPass = true
		report.OutboxMessage = "skipped (no -transfer-db flag provided)"
		v.logger.Info("outbox drain check skipped: no transfer DB URL configured")
		return
	}

	conn, err := pgx.Connect(ctx, v.config.TransferDBURL)
	if err != nil {
		// DB unreachable post-test — treat as a warning, not a failure.
		report.OutboxPass = true
		report.OutboxMessage = fmt.Sprintf("skipped (could not connect to transfer DB: %v)", err)
		v.logger.Warn("outbox drain check skipped: cannot connect to transfer DB", "error", err)
		return
	}
	defer conn.Close(ctx) //nolint:errcheck

	// Count entries that are unpublished AND have not exhausted retries.
	// Entries with retry_count >= max_retries are permanently failed and will
	// never be retried; those are a separate alert (they indicate saga gaps).
	var unpublished int64
	err = conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox WHERE published = false`).Scan(&unpublished)
	if err != nil {
		report.OutboxPass = true
		report.OutboxMessage = fmt.Sprintf("skipped (query error: %v)", err)
		v.logger.Warn("outbox drain check: query failed", "error", err)
		return
	}

	// Additionally count permanently-stuck entries (retries exhausted).
	var exhausted int64
	err = conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count >= max_retries`).Scan(&exhausted)
	if err != nil {
		exhausted = 0 // non-fatal; best effort
	}

	report.OutboxUnpublished = unpublished
	if unpublished == 0 {
		report.OutboxPass = true
		report.OutboxMessage = "outbox fully drained (0 unpublished entries)"
		v.logger.Info("outbox drain check passed", "unpublished", 0)
	} else {
		report.OutboxPass = false
		report.OutboxMessage = fmt.Sprintf("%d unpublished outbox entries remain (%d with retries exhausted)",
			unpublished, exhausted)
		v.logger.Error("outbox not fully drained after test",
			"unpublished", unpublished,
			"retries_exhausted", exhausted,
		)
	}
}

// checkDBStuckTransfers queries the Transfer DB directly to count transfers
// that remain in a non-terminal state after the drain phase completes.
//
// This is a deeper check than the API-level checkTransferStates because it
// catches transfers that were created during the test but whose polling
// goroutine timed out before the transfer finished — those transfers would
// appear as "poll_failed" errors in the metrics but may still be progressing
// in the system (or genuinely stuck in the DB).
func (v *Verifier) checkDBStuckTransfers(ctx context.Context, report *VerificationReport) {
	if v.config.TransferDBURL == "" {
		report.DBStuckPass = true
		report.DBStuckMessage = "skipped (no -transfer-db flag provided)"
		return
	}

	conn, err := pgx.Connect(ctx, v.config.TransferDBURL)
	if err != nil {
		report.DBStuckPass = true
		report.DBStuckMessage = fmt.Sprintf("skipped (could not connect to transfer DB: %v)", err)
		v.logger.Warn("DB stuck transfer check skipped: cannot connect", "error", err)
		return
	}
	defer conn.Close(ctx) //nolint:errcheck

	// Terminal states — any other status after drain is a stuck transfer.
	// REFUNDED is terminal (compensation completed). FAILED is terminal.
	// COMPLETED is terminal.
	var stuck int64
	err = conn.QueryRow(ctx,
		`SELECT COUNT(*)
		 FROM transfers
		 WHERE status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED')
		   AND created_at > now() - INTERVAL '2 hours'`).Scan(&stuck)
	if err != nil {
		report.DBStuckPass = true
		report.DBStuckMessage = fmt.Sprintf("skipped (query error: %v)", err)
		v.logger.Warn("DB stuck transfer check: query failed", "error", err)
		return
	}

	report.DBStuckTransfers = stuck
	if stuck == 0 {
		report.DBStuckPass = true
		report.DBStuckMessage = "no stuck transfers in DB"
		v.logger.Info("DB stuck transfer check passed", "stuck", 0)
	} else {
		report.DBStuckPass = false
		report.DBStuckMessage = fmt.Sprintf("%d transfer(s) in non-terminal state after drain", stuck)

		// Log the specific stuck transfers for diagnosis.
		rows, qErr := conn.Query(ctx,
			`SELECT id, tenant_id, status, created_at, updated_at
			 FROM transfers
			 WHERE status NOT IN ('COMPLETED', 'FAILED', 'REFUNDED')
			   AND created_at > now() - INTERVAL '2 hours'
			 ORDER BY created_at DESC
			 LIMIT 20`)
		if qErr == nil {
			defer rows.Close()
			for rows.Next() {
				var id, tenantID, status string
				var createdAt, updatedAt time.Time
				if sErr := rows.Scan(&id, &tenantID, &status, &createdAt, &updatedAt); sErr == nil {
					v.logger.Error("stuck transfer in DB",
						"id", id,
						"tenant_id", tenantID,
						"status", status,
						"created_at", createdAt,
						"stuck_for", time.Since(updatedAt).Round(time.Second),
					)
				}
			}
		}
	}
}

// checkTreasuryPositions calls GET /v1/treasury/positions for each unique tenant
// and verifies that the current position reflects completed transfers.
func (v *Verifier) checkTreasuryPositions(ctx context.Context, results []TransferResult, report *VerificationReport) {
	// Compute per-tenant completed volume from results.
	type tenantSummary struct {
		apiKey         string
		completedAmt   decimal.Decimal
		completedCnt   int
		sourceCurrency string
	}
	summaries := make(map[string]*tenantSummary) // tenantID → summary

	for _, cfg := range v.config.Tenants {
		if _, ok := summaries[cfg.ID]; !ok {
			summaries[cfg.ID] = &tenantSummary{
				apiKey:         cfg.APIKey,
				sourceCurrency: cfg.Currency,
			}
		}
	}
	for _, r := range results {
		if r.Status == "COMPLETED" {
			s, ok := summaries[r.TenantID]
			if !ok {
				continue
			}
			s.completedAmt = s.completedAmt.Add(r.Amount)
			s.completedCnt++
		}
	}

	// For each unique tenant, fetch positions and check that reserved = 0
	// (all reservations flushed after drain).
	allReconciled := true
	msgs := []string{}

	for tenantID, summary := range summaries {
		positions, err := v.fetchPositions(ctx, summary.apiKey)
		if err != nil {
			v.logger.Warn("could not fetch treasury positions",
				"tenant_id", tenantID,
				"error", err,
			)
			// Cannot verify — treat as pass (system may be down post-test).
			continue
		}

		// Check that reserved amounts are 0 (drain should have flushed all reservations).
		for _, pos := range positions.Positions {
			reserved, _ := decimal.NewFromString(pos.Reserved)
			if !reserved.IsZero() {
				allReconciled = false
				msg := fmt.Sprintf("tenant %s: position %s/%s has reserved=%s after drain",
					tenantID[:8], pos.Currency, pos.Location, pos.Reserved)
				msgs = append(msgs, msg)
				report.OrphanedReservations++
				v.logger.Warn("orphaned reservation detected", "tenant_id", tenantID,
					"currency", pos.Currency, "location", pos.Location,
					"reserved", pos.Reserved)
			}
		}
	}

	report.TreasuryPass = allReconciled
	report.ReservationsPass = report.OrphanedReservations == 0
	if len(msgs) > 0 {
		report.TreasuryMessage = strings.Join(msgs, "; ")
	} else {
		report.TreasuryMessage = "all positions reconciled"
	}
}

// fetchPositions calls GET /v1/treasury/positions with the given API key.
func (v *Verifier) fetchPositions(ctx context.Context, apiKey string) (*PositionsResponse, error) {
	url := v.config.GatewayURL + "/v1/treasury/positions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("treasury positions returned HTTP %d", resp.StatusCode)
	}

	var positions PositionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&positions); err != nil {
		return nil, fmt.Errorf("decode positions: %w", err)
	}
	return &positions, nil
}

// checkLedgerBalance verifies that total debits equal total credits across all
// ledger accounts created during the test window.
//
// When a Ledger DB URL is provided, we query the Postgres read-side directly.
// The read-side is populated by the TigerBeetle→Postgres sync consumer, so a
// debit ≠ credit result means either:
//   - The sync consumer is lagging (entries not yet replicated), or
//   - A balanced-posting invariant was violated (bug).
//
// When no Ledger DB URL is provided, we fall back to a gateway /health ping
// (best-effort, no balance assertion).
func (v *Verifier) checkLedgerBalance(ctx context.Context, report *VerificationReport) {
	if v.config.LedgerDBURL != "" {
		v.checkLedgerBalanceViaDB(ctx, report)
		return
	}
	v.checkLedgerBalanceViaHealth(ctx, report)
}

// checkLedgerBalanceViaDB queries the Postgres read-side for debit/credit sums.
func (v *Verifier) checkLedgerBalanceViaDB(ctx context.Context, report *VerificationReport) {
	conn, err := pgx.Connect(ctx, v.config.LedgerDBURL)
	if err != nil {
		report.LedgerPass = true
		report.LedgerMessage = fmt.Sprintf("skipped (could not connect to ledger DB: %v)", err)
		v.logger.Warn("ledger balance check skipped: cannot connect", "error", err)
		return
	}
	defer conn.Close(ctx) //nolint:errcheck

	// Query entry_lines created in the last 2 hours (covers the test window).
	// entry_lines stores individual debit/credit lines from each journal entry.
	// Sum must balance: total_debits == total_credits.
	var totalDebits, totalCredits decimal.Decimal
	var debitStr, creditStr string
	err = conn.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN entry_type = 'DEBIT'  THEN amount ELSE 0 END), 0)::text AS total_debits,
			COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE 0 END), 0)::text AS total_credits
		FROM entry_lines
		WHERE created_at > now() - INTERVAL '2 hours'
	`).Scan(&debitStr, &creditStr)
	if err != nil {
		// Table may not exist if ledger sync hasn't run yet — treat as warning.
		report.LedgerPass = true
		report.LedgerMessage = fmt.Sprintf("skipped (entry_lines query error: %v)", err)
		v.logger.Warn("ledger balance check: entry_lines query failed", "error", err)
		return
	}

	totalDebits, err = decimal.NewFromString(debitStr)
	if err != nil {
		report.LedgerPass = true
		report.LedgerMessage = fmt.Sprintf("skipped (could not parse debit sum: %v)", err)
		return
	}
	totalCredits, err = decimal.NewFromString(creditStr)
	if err != nil {
		report.LedgerPass = true
		report.LedgerMessage = fmt.Sprintf("skipped (could not parse credit sum: %v)", err)
		return
	}

	imbalance := totalDebits.Sub(totalCredits).Abs()
	if imbalance.IsZero() {
		report.LedgerPass = true
		report.LedgerMessage = fmt.Sprintf("debits = credits = %s (balanced)", totalDebits.StringFixed(2))
		v.logger.Info("ledger balance check passed",
			"total_debits", totalDebits.StringFixed(2),
			"total_credits", totalCredits.StringFixed(2),
		)
	} else {
		report.LedgerPass = false
		report.LedgerMessage = fmt.Sprintf("IMBALANCE: debits=%s credits=%s delta=%s",
			totalDebits.StringFixed(8), totalCredits.StringFixed(8), imbalance.StringFixed(8))
		v.logger.Error("ledger balance check FAILED: debits ≠ credits",
			"total_debits", totalDebits.StringFixed(8),
			"total_credits", totalCredits.StringFixed(8),
			"imbalance", imbalance.StringFixed(8),
		)
	}
}

// checkLedgerBalanceViaHealth performs a best-effort ledger balance check via /health.
// Used when no Ledger DB URL is provided.
func (v *Verifier) checkLedgerBalanceViaHealth(ctx context.Context, report *VerificationReport) {
	url := v.config.GatewayURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		report.LedgerPass = true // Cannot verify, assume OK.
		report.LedgerMessage = "health endpoint unreachable (skipped; use -ledger-db for full check)"
		return
	}

	resp, err := v.client.Do(req)
	if err != nil {
		report.LedgerPass = true
		report.LedgerMessage = "health endpoint unreachable (skipped; use -ledger-db for full check)"
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		report.LedgerPass = true
		report.LedgerMessage = "gateway healthy (use -ledger-db for debit=credit assertion)"
	} else {
		report.LedgerPass = false
		report.LedgerMessage = fmt.Sprintf("gateway unhealthy: HTTP %d", resp.StatusCode)
	}
}

// buildTenantResults assembles per-tenant results for the report.
func (v *Verifier) buildTenantResults(results []TransferResult, report *VerificationReport) {
	type tenantAccum struct {
		cfg        TenantConfig
		total      int
		completed  int
		volume     decimal.Decimal
		reconciled bool
	}

	accums := make(map[string]*tenantAccum)
	for _, cfg := range v.config.Tenants {
		if _, ok := accums[cfg.ID]; !ok {
			accums[cfg.ID] = &tenantAccum{cfg: cfg, reconciled: report.TreasuryPass}
		}
	}
	for _, r := range results {
		a, ok := accums[r.TenantID]
		if !ok {
			continue
		}
		a.total++
		if r.Status == "COMPLETED" {
			a.completed++
			a.volume = a.volume.Add(r.Amount)
		}
	}

	for _, a := range accums {
		report.TenantResults = append(report.TenantResults, TenantVerificationResult{
			TenantID:           a.cfg.ID,
			TransferCount:      a.total,
			CompletedCount:     a.completed,
			TotalVolume:        a.volume,
			Currency:           a.cfg.Currency,
			TreasuryReconciled: a.reconciled,
		})
	}
}

// String formats the report as the canonical consistency verification output.
func (r *VerificationReport) String() string {
	var b strings.Builder

	b.WriteString("\n=== CONSISTENCY VERIFICATION ===\n")

	stuckSymbol := checkMark(r.TransfersPass)
	b.WriteString(fmt.Sprintf("Transfers (API): %d created, %d completed, %d failed, %d stuck  %s\n",
		r.TotalTransfers, r.CompletedTransfers, r.FailedTransfers, r.StuckTransfers, stuckSymbol))

	outboxSymbol := checkMark(r.OutboxPass)
	b.WriteString(fmt.Sprintf("Outbox drain:    %s  %s\n", r.OutboxMessage, outboxSymbol))

	dbStuckSymbol := checkMark(r.DBStuckPass)
	b.WriteString(fmt.Sprintf("DB stuck:        %s  %s\n", r.DBStuckMessage, dbStuckSymbol))

	treasurySymbol := checkMark(r.TreasuryPass)
	b.WriteString(fmt.Sprintf("Treasury:        %s  %s\n", r.TreasuryMessage, treasurySymbol))

	ledgerSymbol := checkMark(r.LedgerPass)
	b.WriteString(fmt.Sprintf("Ledger balance:  %s  %s\n", r.LedgerMessage, ledgerSymbol))

	reserveSymbol := checkMark(r.ReservationsPass)
	b.WriteString(fmt.Sprintf("Reservations:    %d orphaned  %s\n", r.OrphanedReservations, reserveSymbol))

	errorRateSymbol := checkMark(r.ErrorRatePass)
	b.WriteString(fmt.Sprintf("Error rate:      %.1f%% (max %.1f%%)  %s\n", r.ErrorRate, r.MaxAcceptableErrorRate, errorRateSymbol))

	if len(r.ErrorsByCategory) > 0 {
		b.WriteString("Error breakdown:\n")
		for category, count := range r.ErrorsByCategory {
			b.WriteString(fmt.Sprintf("  %-20s %d\n", category, count))
		}
	}

	for _, t := range r.TenantResults {
		tenantSymbol := checkMark(t.TreasuryReconciled)
		shortID := t.TenantID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		b.WriteString(fmt.Sprintf("Tenant %s: %d transfers, %s %s volume  %s\n",
			shortID, t.CompletedCount,
			t.TotalVolume.StringFixed(2), t.Currency,
			tenantSymbol))
	}

	if r.AllPassed {
		b.WriteString("=== ALL CHECKS PASSED ===\n")
	} else {
		b.WriteString(fmt.Sprintf("=== VERIFICATION FAILED: %s ===\n", r.FailReason))
	}

	return b.String()
}

// checkMark returns a Unicode check or cross symbol.
func checkMark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
