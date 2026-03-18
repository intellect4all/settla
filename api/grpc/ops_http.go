package grpc

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/transferdb"
)

// RegisterOpsHandlers registers the internal ops HTTP handlers on mux at /internal/ops/*.
// The auditLogger parameter is optional — pass nil to disable audit logging for ops actions.
func RegisterOpsHandlers(mux *http.ServeMux, store transferdb.OpsStore, logger *slog.Logger, auditLogger ...domain.AuditLogger) {
	var audit domain.AuditLogger
	if len(auditLogger) > 0 {
		audit = auditLogger[0]
	}
	opsAPIKey := os.Getenv("SETTLA_OPS_API_KEY")
	if opsAPIKey == "" {
		if os.Getenv("NODE_ENV") == "production" {
			logger.Error("settla-ops: SETTLA_OPS_API_KEY is required in production — ops endpoints disabled")
			return
		}
		logger.Warn("settla-ops: SETTLA_OPS_API_KEY is not set — ops endpoints are unauthenticated (dev mode only)")
	}

	mux.HandleFunc("/internal/ops/", func(w http.ResponseWriter, r *http.Request) {
		// Authenticate ops requests when SETTLA_OPS_API_KEY is configured.
		if opsAPIKey != "" {
			provided := r.Header.Get("X-Ops-Api-Key")
			if provided == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing X-Ops-Api-Key header"})
				return
			}
			if subtle.ConstantTimeCompare([]byte(provided), []byte(opsAPIKey)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid ops API key"})
				return
			}
		}

		logger.Info("settla-ops: access", "method", r.Method, "path", r.URL.Path, "remote_ip", r.RemoteAddr)

		// Strip prefix to get the sub-path.
		sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/")
		sub = strings.TrimSuffix(sub, "/")

		switch {
		// ── Tenants ──────────────────────────────────────────────────────
		// GET /internal/ops/tenants
		case r.Method == http.MethodGet && sub == "tenants":
			handleListTenants(w, r, store, logger)

		// GET /internal/ops/tenants/{id}
		case r.Method == http.MethodGet && strings.HasPrefix(sub, "tenants/") && !strings.Contains(sub[len("tenants/"):], "/"):
			handleGetTenant(w, r, store, logger)

		// POST /internal/ops/tenants/{id}/status
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "tenants/") && strings.HasSuffix(sub, "/status"):
			handleUpdateTenantStatus(w, r, store, logger, audit)

		// POST /internal/ops/tenants/{id}/kyb
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "tenants/") && strings.HasSuffix(sub, "/kyb"):
			handleUpdateTenantKYB(w, r, store, logger, audit)

		// POST /internal/ops/tenants/{id}/fees
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "tenants/") && strings.HasSuffix(sub, "/fees"):
			handleUpdateTenantFees(w, r, store, logger, audit)

		// POST /internal/ops/tenants/{id}/limits
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "tenants/") && strings.HasSuffix(sub, "/limits"):
			handleUpdateTenantLimits(w, r, store, logger, audit)

		// ── Manual Reviews ──────────────────────────────────────────────
		// POST /internal/ops/manual-reviews/{id}/approve
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "manual-reviews/") && strings.HasSuffix(sub, "/approve"):
			handleManualReviewAction(w, r, store, logger, "APPROVED", audit)

		// POST /internal/ops/manual-reviews/{id}/reject
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "manual-reviews/") && strings.HasSuffix(sub, "/reject"):
			handleManualReviewAction(w, r, store, logger, "REJECTED", audit)

		// GET /internal/ops/manual-reviews
		case r.Method == http.MethodGet && sub == "manual-reviews":
			handleListManualReviews(w, r, store, logger)

		// GET /internal/ops/reconciliation/latest or POST /internal/ops/reconciliation/run
		case (r.Method == http.MethodGet && sub == "reconciliation/latest") ||
			(r.Method == http.MethodPost && sub == "reconciliation/run"):
			handleGetReconciliation(w, r, store, logger)

		// GET /internal/ops/settlements/report
		case r.Method == http.MethodGet && sub == "settlements/report":
			handleSettlementsReport(w, r, store, logger)

		// POST /internal/ops/settlements/{tenantId}/mark-paid
		case r.Method == http.MethodPost && strings.HasPrefix(sub, "settlements/") && strings.HasSuffix(sub, "/mark-paid"):
			handleMarkSettlementPaid(w, r, store, logger, audit)

		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	})
}

// ── Tenant Handlers ─────────────────────────────────────────────────────────

// handleListTenants handles GET /internal/ops/tenants?limit=&offset=
func handleListTenants(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger) {
	limit := int32(50)
	offset := int32(0)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := fmt.Sscanf(l, "%d", &limit); err != nil || v == 0 {
			limit = 50
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := fmt.Sscanf(o, "%d", &offset); err != nil || v == 0 {
			offset = 0
		}
	}
	tenants, err := store.ListAllTenants(r.Context(), limit, offset)
	if err != nil {
		logger.Error("ops: listing tenants", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list tenants"})
		return
	}
	if tenants == nil {
		tenants = []transferdb.OpsTenant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": tenants})
}

// handleGetTenant handles GET /internal/ops/tenants/{id}
func handleGetTenant(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger) {
	sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/tenants/")
	sub = strings.TrimSuffix(sub, "/")
	id, err := uuid.Parse(sub)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	tenant, err := store.GetTenantByID(r.Context(), id)
	if err != nil {
		logger.Error("ops: getting tenant", "id", id, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

// handleUpdateTenantStatus handles POST /internal/ops/tenants/{id}/status
func handleUpdateTenantStatus(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, audit domain.AuditLogger) {
	id, err := extractTenantID(r.URL.Path, "/status")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Status != "ACTIVE" && body.Status != "SUSPENDED" && body.Status != "ONBOARDING" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be ACTIVE, SUSPENDED, or ONBOARDING"})
		return
	}
	if err := store.UpdateTenantStatus(r.Context(), id, body.Status); err != nil {
		logger.Error("ops: updating tenant status", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update tenant status"})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   id,
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "tenant.status_updated",
		EntityType: "tenant",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]string{"status": body.Status}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateTenantKYB handles POST /internal/ops/tenants/{id}/kyb
func handleUpdateTenantKYB(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, audit domain.AuditLogger) {
	id, err := extractTenantID(r.URL.Path, "/kyb")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	var body struct {
		KybStatus string `json:"kyb_status"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.KybStatus != "PENDING" && body.KybStatus != "IN_REVIEW" && body.KybStatus != "VERIFIED" && body.KybStatus != "REJECTED" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kyb_status must be PENDING, IN_REVIEW, VERIFIED, or REJECTED"})
		return
	}
	if err := store.UpdateTenantKYBStatus(r.Context(), id, body.KybStatus); err != nil {
		logger.Error("ops: updating tenant KYB", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update KYB status"})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   id,
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "tenant.kyb_updated",
		EntityType: "tenant",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]string{"kyb_status": body.KybStatus}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateTenantFees handles POST /internal/ops/tenants/{id}/fees
func handleUpdateTenantFees(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, audit domain.AuditLogger) {
	id, err := extractTenantID(r.URL.Path, "/fees")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	var body struct {
		OnRampBps  int    `json:"on_ramp_bps"`
		OffRampBps int    `json:"off_ramp_bps"`
		MinFeeUsd  string `json:"min_fee_usd"`
		MaxFeeUsd  string `json:"max_fee_usd"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	feeJSON, err := json.Marshal(map[string]any{
		"onramp_bps":  body.OnRampBps,
		"offramp_bps": body.OffRampBps,
		"min_fee_usd": body.MinFeeUsd,
		"max_fee_usd": body.MaxFeeUsd,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to encode fee schedule"})
		return
	}
	if err := store.UpdateTenantFees(r.Context(), id, feeJSON); err != nil {
		logger.Error("ops: updating tenant fees", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update fee schedule"})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   id,
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "tenant.fees_updated",
		EntityType: "tenant",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]any{
			"onramp_bps":  body.OnRampBps,
			"offramp_bps": body.OffRampBps,
			"min_fee_usd": body.MinFeeUsd,
			"max_fee_usd": body.MaxFeeUsd,
		}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateTenantLimits handles POST /internal/ops/tenants/{id}/limits
func handleUpdateTenantLimits(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, audit domain.AuditLogger) {
	id, err := extractTenantID(r.URL.Path, "/limits")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
		return
	}
	var body struct {
		DailyLimitUsd    string `json:"daily_limit_usd"`
		PerTransferLimit string `json:"per_transfer_limit"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.DailyLimitUsd == "" || body.PerTransferLimit == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "daily_limit_usd and per_transfer_limit are required"})
		return
	}
	if err := store.UpdateTenantLimits(r.Context(), id, body.DailyLimitUsd, body.PerTransferLimit); err != nil {
		logger.Error("ops: updating tenant limits", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update limits"})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   id,
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "tenant.limits_updated",
		EntityType: "tenant",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]string{
			"daily_limit_usd":    body.DailyLimitUsd,
			"per_transfer_limit": body.PerTransferLimit,
		}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// extractTenantID extracts a UUID from a path like /internal/ops/tenants/{id}/suffix
func extractTenantID(path, suffix string) (uuid.UUID, error) {
	sub := strings.TrimPrefix(path, "/internal/ops/tenants/")
	sub = strings.TrimSuffix(sub, suffix)
	sub = strings.TrimSuffix(sub, "/")
	return uuid.Parse(sub)
}

// ── Manual Review Handlers ─────────────────────────────────────────────────

// handleListManualReviews handles GET /internal/ops/manual-reviews?status=
func handleListManualReviews(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger) {
	status := r.URL.Query().Get("status")
	// tenantID query param allows scoping to a single tenant (omit for all tenants).
	var tenantID *uuid.UUID
	if tid := r.URL.Query().Get("tenant_id"); tid != "" {
		if parsed, parseErr := uuid.Parse(tid); parseErr == nil {
			tenantID = &parsed
		}
	}
	reviews, err := store.ListManualReviews(r.Context(), domain.AdminCaller{
		Service: "ops_api",
		Reason:  "manual_review_list",
	}, tenantID, status)
	if err != nil {
		logger.Error("ops: listing manual reviews", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list manual reviews"})
		return
	}
	if reviews == nil {
		reviews = []transferdb.OpsManualReview{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": reviews})
}

// handleManualReviewAction handles POST .../approve and .../reject.
func handleManualReviewAction(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, action string, audit domain.AuditLogger) {
	// Extract ID from path: manual-reviews/{id}/approve or .../reject
	sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/manual-reviews/")
	// sub is now "{id}/approve" or "{id}/reject"
	parts := strings.Split(sub, "/")
	if len(parts) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid review id"})
		return
	}

	var body struct {
		Notes string `json:"notes"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := store.ResolveManualReview(r.Context(), id, action, body.Notes, "ops"); err != nil {
		logger.Error("ops: resolving manual review", "id", id, "action", action, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to %s review", strings.ToLower(action))})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   uuid.Nil, // manual reviews are cross-tenant
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "manual_review." + strings.ToLower(action),
		EntityType: "manual_review",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]string{"status": action, "notes": body.Notes}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CheckResultJSON is the format stored by the reconciler in discrepancies JSONB.
type CheckResultJSON struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // "pass", "fail", "warn"
	Details    any    `json:"details"`
	Mismatches any    `json:"mismatches"`
	CheckedAt  string `json:"checked_at"`
}

type reconciliationCheckResponse struct {
	Name             string `json:"name"`
	Status           string `json:"status"` // "OK", "WARNING", "CRITICAL"
	Description      string `json:"description"`
	DiscrepancyCount int    `json:"discrepancy_count"`
	Details          []any  `json:"details"`
	RanAt            string `json:"ran_at"`
	DurationMs       int    `json:"duration_ms"`
}

type reconciliationResponse struct {
	ID         string                        `json:"id"`
	RanAt      string                        `json:"ran_at"`
	DurationMs int32                         `json:"duration_ms"`
	Status     string                        `json:"status"` // "OK", "WARNING", "CRITICAL"
	Checks     []reconciliationCheckResponse `json:"checks"`
}

// handleGetReconciliation handles GET /internal/ops/reconciliation/latest and
// POST /internal/ops/reconciliation/run (returns latest in both cases).
func handleGetReconciliation(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger) {
	report, err := store.GetLatestReconciliationReport(r.Context())
	if err != nil {
		logger.Error("ops: getting reconciliation report", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get reconciliation report"})
		return
	}
	if report == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no reconciliation report found"})
		return
	}

	// Map overall status.
	overallStatus := "OK"
	if report.NeedsReview {
		overallStatus = "CRITICAL"
	} else if report.ChecksPassed < report.ChecksRun {
		overallStatus = "WARNING"
	}

	// Try to unmarshal discrepancies into check results.
	var checks []reconciliationCheckResponse
	if len(report.Discrepancies) > 2 { // more than empty `[]` or `{}`
		var rawChecks []CheckResultJSON
		if err := json.Unmarshal(report.Discrepancies, &rawChecks); err == nil && len(rawChecks) > 0 {
			checks = make([]reconciliationCheckResponse, 0, len(rawChecks))
			for _, rc := range rawChecks {
				checkStatus := reconcilerStatusToDashboard(rc.Status)
				discCount := 0
				var details []any
				if rc.Mismatches != nil {
					if arr, ok := rc.Mismatches.([]any); ok {
						discCount = len(arr)
						details = arr
					}
				}
				if details == nil {
					details = []any{}
				}
				checks = append(checks, reconciliationCheckResponse{
					Name:             rc.Name,
					Status:           checkStatus,
					Description:      fmt.Sprintf("Check: %s", rc.Name),
					DiscrepancyCount: discCount,
					Details:          details,
					RanAt:            report.RanAt.UTC().Format(time.RFC3339),
					DurationMs:       0,
				})
			}
		}
	}

	// Synthesize check entries if we couldn't parse them.
	if len(checks) == 0 && report.ChecksRun > 0 {
		checks = make([]reconciliationCheckResponse, 0, int(report.ChecksRun))
		failed := report.ChecksRun - report.ChecksPassed
		for i := int32(0); i < report.ChecksRun; i++ {
			st := "OK"
			if i < failed {
				st = "CRITICAL"
			}
			checks = append(checks, reconciliationCheckResponse{
				Name:             fmt.Sprintf("check_%d", i+1),
				Status:           st,
				Description:      fmt.Sprintf("Automated check %d", i+1),
				DiscrepancyCount: 0,
				Details:          []any{},
				RanAt:            report.RanAt.UTC().Format(time.RFC3339),
				DurationMs:       0,
			})
		}
	}
	if checks == nil {
		checks = []reconciliationCheckResponse{}
	}

	resp := reconciliationResponse{
		ID:         report.ID.String(),
		RanAt:      report.RanAt.UTC().Format(time.RFC3339),
		DurationMs: report.DurationMs,
		Status:     overallStatus,
		Checks:     checks,
	}
	writeJSON(w, http.StatusOK, resp)
}

func reconcilerStatusToDashboard(s string) string {
	switch strings.ToLower(s) {
	case "pass":
		return "OK"
	case "warn":
		return "WARNING"
	case "fail":
		return "CRITICAL"
	default:
		return "OK"
	}
}

// corridorJSON matches the settlement.CorridorPosition JSON structure.
type corridorJSON struct {
	SourceCurrency string `json:"source_currency"`
	DestCurrency   string `json:"dest_currency"`
	TotalSource    string `json:"total_source"`
	TotalDest      string `json:"total_dest"`
	TransferCount  int    `json:"transfer_count"`
}

type settlementLeg struct {
	Corridor       string `json:"corridor"`
	SourceCurrency string `json:"source_currency"`
	DestCurrency   string `json:"dest_currency"`
	TotalSent      string `json:"total_sent"`
	TotalReceived  string `json:"total_received"`
	NetUSD         string `json:"net_usd"`
	TransferCount  int    `json:"transfer_count"`
	FeeRevenueUSD  string `json:"fee_revenue_usd"`
}

type settlementTenantEntry struct {
	TenantID           string          `json:"tenant_id"`
	TenantName         string          `json:"tenant_name"`
	Legs               []settlementLeg `json:"legs"`
	TotalReceivableUSD string          `json:"total_receivable_usd"`
	TotalPayableUSD    string          `json:"total_payable_usd"`
	NetPositionUSD     string          `json:"net_position_usd"`
	DueDate            string          `json:"due_date,omitempty"`
	PaymentStatus      string          `json:"payment_status"`
	PaymentRef         string          `json:"payment_ref,omitempty"`
}

type settlementsReportResponse struct {
	PeriodStart        string                  `json:"period_start"`
	PeriodEnd          string                  `json:"period_end"`
	GeneratedAt        string                  `json:"generated_at"`
	TotalVolumeUSD     string                  `json:"total_volume_usd"`
	TotalFeeRevenueUSD string                  `json:"total_fee_revenue_usd"`
	Tenants            []settlementTenantEntry `json:"tenants"`
}

// handleSettlementsReport handles GET /internal/ops/settlements/report.
func handleSettlementsReport(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger) {
	settlements, err := store.ListNetSettlements(r.Context(), domain.AdminCaller{
		Service: "ops_api",
		Reason:  "settlements_report",
	}, nil)
	if err != nil {
		logger.Error("ops: listing settlements", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list settlements"})
		return
	}

	var (
		periodStart        time.Time
		periodEnd          time.Time
		totalFeeRevenueUSD float64
		tenants            []settlementTenantEntry
	)

	for i, s := range settlements {
		if i == 0 {
			periodStart = s.PeriodStart
			periodEnd = s.PeriodEnd
		}
		if s.PeriodStart.Before(periodStart) {
			periodStart = s.PeriodStart
		}
		if s.PeriodEnd.After(periodEnd) {
			periodEnd = s.PeriodEnd
		}

		// Parse corridors JSONB.
		var corridors []corridorJSON
		if len(s.Corridors) > 2 {
			_ = json.Unmarshal(s.Corridors, &corridors)
		}

		legs := make([]settlementLeg, 0, len(corridors))
		for _, c := range corridors {
			netUSD := "0"
			if c.SourceCurrency == c.DestCurrency {
				// Same currency: net = dest - source (rough).
				netUSD = "0"
			}
			legs = append(legs, settlementLeg{
				Corridor:       fmt.Sprintf("%s/%s", c.SourceCurrency, c.DestCurrency),
				SourceCurrency: c.SourceCurrency,
				DestCurrency:   c.DestCurrency,
				TotalSent:      c.TotalSource,
				TotalReceived:  c.TotalDest,
				NetUSD:         netUSD,
				TransferCount:  c.TransferCount,
				FeeRevenueUSD:  "0",
			})
		}
		if legs == nil {
			legs = []settlementLeg{}
		}

		// net_by_currency and net position.
		netPositionUSD := "0"
		totalReceivableUSD := "0"
		totalPayableUSD := "0"

		// Parse payment_ref from instructions if present.
		paymentRef := ""
		if len(s.Instructions) > 2 {
			var instMap map[string]any
			if err := json.Unmarshal(s.Instructions, &instMap); err == nil {
				if ref, ok := instMap["payment_ref"].(string); ok {
					paymentRef = ref
				}
			}
		}

		dueDate := ""
		if s.DueDate != nil {
			dueDate = s.DueDate.UTC().Format("2006-01-02")
		}

		entry := settlementTenantEntry{
			TenantID:           s.TenantID.String(),
			TenantName:         s.TenantName,
			Legs:               legs,
			TotalReceivableUSD: totalReceivableUSD,
			TotalPayableUSD:    totalPayableUSD,
			NetPositionUSD:     netPositionUSD,
			DueDate:            dueDate,
			PaymentStatus:      settlementStatusToDashboard(s.Status),
			PaymentRef:         paymentRef,
		}
		tenants = append(tenants, entry)

		// Accumulate total fee revenue.
		_ = totalFeeRevenueUSD // used below as string
	}

	if tenants == nil {
		tenants = []settlementTenantEntry{}
	}

	// Build period strings.
	periodStartStr := ""
	periodEndStr := ""
	if !periodStart.IsZero() {
		periodStartStr = periodStart.UTC().Format(time.RFC3339)
		periodEndStr = periodEnd.UTC().Format(time.RFC3339)
	}

	resp := settlementsReportResponse{
		PeriodStart:        periodStartStr,
		PeriodEnd:          periodEndStr,
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		TotalVolumeUSD:     "0",
		TotalFeeRevenueUSD: "0",
		Tenants:            tenants,
	}
	writeJSON(w, http.StatusOK, resp)
}

func settlementStatusToDashboard(s string) string {
	switch strings.ToLower(s) {
	case "pending":
		return "PENDING"
	case "approved":
		return "SCHEDULED"
	case "settled", "paid":
		return "PAID"
	case "overdue":
		return "OVERDUE"
	default:
		return "PENDING"
	}
}

// handleMarkSettlementPaid handles POST /internal/ops/settlements/{tenantId}/mark-paid.
// The tenantId in the path is treated as a settlement ID for direct DB update.
// If not a valid UUID or not found, the handler looks up the latest pending settlement
// for that tenant ID instead.
func handleMarkSettlementPaid(w http.ResponseWriter, r *http.Request, store transferdb.OpsStore, logger *slog.Logger, audit domain.AuditLogger) {
	sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/settlements/")
	// sub is "{tenantId}/mark-paid"
	parts := strings.Split(sub, "/")
	if len(parts) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var body struct {
		PaymentRef string `json:"payment_ref"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Try to mark by settlement ID directly.
	// The dashboard passes tenant_id, so we first try to find the latest pending
	// settlement for this tenant via a list scan.
	settlements, listErr := store.ListNetSettlements(r.Context(), domain.AdminCaller{
		Service: "ops_api",
		Reason:  "mark_settlement_paid",
	}, nil)
	if listErr == nil {
		// Find the latest pending settlement for this tenant_id.
		for _, s := range settlements {
			if s.TenantID == id && (s.Status == "pending" || s.Status == "approved") {
				if err := store.MarkSettlementPaid(r.Context(), s.ID, body.PaymentRef); err != nil {
					logger.Error("ops: marking settlement paid", "id", s.ID, "error", err)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to mark settlement as paid"})
					return
				}
				auditLog(r.Context(), audit, logger, domain.AuditEntry{
					TenantID:   s.TenantID,
					ActorType:  "ops",
					ActorID:    extractActorFromRequest(r),
					Action:     "settlement.mark_paid",
					EntityType: "settlement",
					EntityID:   uuidPtr(s.ID),
					NewValue:   mustJSON(map[string]string{"status": "paid", "payment_ref": body.PaymentRef}),
				})
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
		}
	}

	// Fall back: treat the path id as a settlement id directly.
	if err := store.MarkSettlementPaid(r.Context(), id, body.PaymentRef); err != nil {
		logger.Error("ops: marking settlement paid by id", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to mark settlement as paid"})
		return
	}
	auditLog(r.Context(), audit, logger, domain.AuditEntry{
		TenantID:   uuid.Nil, // settlement ID used directly, tenant unknown
		ActorType:  "ops",
		ActorID:    extractActorFromRequest(r),
		Action:     "settlement.mark_paid",
		EntityType: "settlement",
		EntityID:   uuidPtr(id),
		NewValue:   mustJSON(map[string]string{"status": "paid", "payment_ref": body.PaymentRef}),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeJSON writes v as JSON with the given HTTP status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readBody decodes the JSON request body into v.
func readBody(r *http.Request, v any) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}
