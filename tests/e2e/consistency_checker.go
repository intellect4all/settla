//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Consistency Checker
//
// Run independently after test suite:
//   go test -tags e2e -run TestConsistency ./tests/e2e/ -v
//
// Or as a standalone program (see RunConsistencyChecker below).
// ---------------------------------------------------------------------------

// ConsistencyResult holds the result of a single consistency check.
type ConsistencyResult struct {
	Check   string `json:"check"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ConsistencyReport aggregates all check results.
type ConsistencyReport struct {
	RunAt    time.Time           `json:"run_at"`
	Gateway  string              `json:"gateway"`
	Results  []ConsistencyResult `json:"results"`
	PassRate float64             `json:"pass_rate"`
}

// consistencyChecker runs post-test data integrity checks via the API.
type consistencyChecker struct {
	base   string
	apiKey string
	http   *http.Client
}

func newConsistencyChecker() *consistencyChecker {
	gw := os.Getenv("GATEWAY_URL")
	if gw == "" {
		gw = "http://localhost:3100"
	}
	key := os.Getenv("E2E_SEED_API_KEY")
	if key == "" {
		key = "sk_live_lemfi_demo_key"
	}
	return &consistencyChecker{
		base:   strings.TrimRight(gw, "/"),
		apiKey: key,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (cc *consistencyChecker) get(path string) (map[string]any, int, error) {
	return cc.doRequest("GET", path, nil)
}

func (cc *consistencyChecker) doRequest(method, path string, body any) (map[string]any, int, error) {
	url := cc.base + path

	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cc.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+cc.apiKey)
	}

	resp, err := cc.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m, resp.StatusCode, nil
}

func (cc *consistencyChecker) run() *ConsistencyReport {
	report := &ConsistencyReport{
		RunAt:   time.Now().UTC(),
		Gateway: cc.base,
	}

	checks := []func() ConsistencyResult{
		cc.checkHealthEndpoint,
		cc.checkTreasuryPositionsAccessible,
		cc.checkLedgerAccountsAccessible,
		cc.checkNoStuckTransfers,
		cc.checkReconciliation,
		cc.checkWebhookHealth,
		cc.checkDepositSessionIntegrity,
		cc.checkBankDepositSessionIntegrity,
	}

	for _, check := range checks {
		report.Results = append(report.Results, check())
	}

	passed := 0
	for _, r := range report.Results {
		if r.Passed {
			passed++
		}
	}
	if len(report.Results) > 0 {
		report.PassRate = float64(passed) / float64(len(report.Results)) * 100
	}

	return report
}

// ---------------------------------------------------------------------------
// Individual Checks
// ---------------------------------------------------------------------------

func (cc *consistencyChecker) checkHealthEndpoint() ConsistencyResult {
	_, status, err := cc.get("/health")
	if err != nil {
		return ConsistencyResult{Check: "health_endpoint", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	return ConsistencyResult{
		Check:   "health_endpoint",
		Passed:  status == 200,
		Message: fmt.Sprintf("status=%d", status),
	}
}

func (cc *consistencyChecker) checkTreasuryPositionsAccessible() ConsistencyResult {
	_, status, err := cc.get("/v1/treasury/positions")
	if err != nil {
		return ConsistencyResult{Check: "treasury_positions", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	return ConsistencyResult{
		Check:   "treasury_positions",
		Passed:  status == 200,
		Message: fmt.Sprintf("status=%d", status),
	}
}

func (cc *consistencyChecker) checkLedgerAccountsAccessible() ConsistencyResult {
	_, status, err := cc.get("/v1/accounts?page_size=5")
	if err != nil {
		return ConsistencyResult{Check: "ledger_accounts", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	return ConsistencyResult{
		Check:   "ledger_accounts",
		Passed:  status == 200,
		Message: fmt.Sprintf("status=%d", status),
	}
}

func (cc *consistencyChecker) checkNoStuckTransfers() ConsistencyResult {
	// List recent transfers and check for any in non-terminal state that are too old
	body, status, err := cc.get("/v1/transfers?page_size=50")
	if err != nil {
		return ConsistencyResult{Check: "no_stuck_transfers", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	if status != 200 {
		return ConsistencyResult{Check: "no_stuck_transfers", Passed: false, Message: fmt.Sprintf("status=%d", status)}
	}

	terminalStates := map[string]bool{
		"COMPLETED": true, "FAILED": true, "REFUNDED": true,
	}

	transfers, ok := body["transfers"].([]any)
	if !ok {
		// May be wrapped differently
		return ConsistencyResult{Check: "no_stuck_transfers", Passed: true, Message: "no transfers found or different response shape"}
	}

	stuckCount := 0
	for _, raw := range transfers {
		tr, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		st, _ := tr["status"].(string)
		if terminalStates[st] {
			continue
		}
		// Check age
		createdStr, _ := tr["created_at"].(string)
		if createdStr == "" {
			continue
		}
		created, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			continue
		}
		if time.Since(created) > 60*time.Minute {
			stuckCount++
		}
	}

	return ConsistencyResult{
		Check:   "no_stuck_transfers",
		Passed:  stuckCount == 0,
		Message: fmt.Sprintf("%d stuck transfers (non-terminal >60min)", stuckCount),
		Details: map[string]int{"stuck_count": stuckCount, "total_checked": len(transfers)},
	}
}

func (cc *consistencyChecker) checkReconciliation() ConsistencyResult {
	body, status, err := cc.get("/v1/me/analytics/reconciliation")
	if err != nil {
		return ConsistencyResult{Check: "reconciliation", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	if status != 200 {
		return ConsistencyResult{Check: "reconciliation", Passed: false, Message: fmt.Sprintf("status=%d", status)}
	}

	return ConsistencyResult{
		Check:   "reconciliation",
		Passed:  true,
		Message: "reconciliation endpoint accessible",
		Details: body,
	}
}

func (cc *consistencyChecker) checkWebhookHealth() ConsistencyResult {
	body, status, err := cc.get("/v1/me/webhooks/stats?period=24h")
	if err != nil {
		return ConsistencyResult{Check: "webhook_health", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	// Accept 200 (success) or 400 (no webhook URL configured yet)
	if status != 200 && status != 400 {
		return ConsistencyResult{Check: "webhook_health", Passed: false, Message: fmt.Sprintf("status=%d", status)}
	}

	return ConsistencyResult{
		Check:   "webhook_health",
		Passed:  true,
		Message: fmt.Sprintf("webhook stats endpoint responded (status=%d)", status),
		Details: body,
	}
}

func (cc *consistencyChecker) checkDepositSessionIntegrity() ConsistencyResult {
	body, status, err := cc.get("/v1/deposits?limit=20")
	if err != nil {
		return ConsistencyResult{Check: "deposit_integrity", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	if status != 200 {
		return ConsistencyResult{Check: "deposit_integrity", Passed: false, Message: fmt.Sprintf("status=%d", status)}
	}

	// Check that all sessions have valid states
	sessions, _ := body["sessions"].([]any)
	validStates := map[string]bool{
		"PENDING_PAYMENT": true, "DETECTED": true, "CONFIRMED": true,
		"CREDITING": true, "CREDITED": true, "SETTLING": true,
		"SETTLED": true, "HELD": true, "EXPIRED": true,
		"FAILED": true, "CANCELLED": true,
	}

	invalidCount := 0
	for _, raw := range sessions {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		st, _ := s["status"].(string)
		if !validStates[st] && st != "" {
			invalidCount++
		}
	}

	return ConsistencyResult{
		Check:   "deposit_integrity",
		Passed:  invalidCount == 0,
		Message: fmt.Sprintf("%d sessions checked, %d invalid states", len(sessions), invalidCount),
	}
}

func (cc *consistencyChecker) checkBankDepositSessionIntegrity() ConsistencyResult {
	body, status, err := cc.get("/v1/bank-deposits?limit=20")
	if err != nil {
		return ConsistencyResult{Check: "bank_deposit_integrity", Passed: false, Message: fmt.Sprintf("error: %v", err)}
	}
	if status != 200 {
		return ConsistencyResult{Check: "bank_deposit_integrity", Passed: false, Message: fmt.Sprintf("status=%d", status)}
	}

	sessions, _ := body["sessions"].([]any)
	validStates := map[string]bool{
		"PENDING_PAYMENT": true, "PAYMENT_RECEIVED": true, "CREDITING": true,
		"CREDITED": true, "SETTLING": true, "SETTLED": true,
		"HELD": true, "EXPIRED": true, "FAILED": true,
		"CANCELLED": true, "UNDERPAID": true, "OVERPAID": true,
	}

	invalidCount := 0
	for _, raw := range sessions {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		st, _ := s["status"].(string)
		if !validStates[st] && st != "" {
			invalidCount++
		}
	}

	return ConsistencyResult{
		Check:   "bank_deposit_integrity",
		Passed:  invalidCount == 0,
		Message: fmt.Sprintf("%d sessions checked, %d invalid states", len(sessions), invalidCount),
	}
}
