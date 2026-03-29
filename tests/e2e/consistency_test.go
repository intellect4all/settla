//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"testing"
)

// TestConsistency_FullCheck runs the consistency checker as a test.
// This can be run independently after the main test suite:
//
//	go test -tags e2e -run TestConsistency ./tests/e2e/ -v
func TestConsistency_FullCheck(t *testing.T) {
	skipIfNoGateway(t)

	cc := newConsistencyChecker()
	report := cc.run()

	// Print results
	for _, r := range report.Results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] %s: %s", status, r.Check, r.Message)
	}

	t.Logf("consistency check pass rate: %.1f%% (%d/%d)",
		report.PassRate, countPassed(report.Results), len(report.Results))

	// Write JSON report if output dir is set
	if dir := os.Getenv("E2E_REPORT_DIR"); dir != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		path := dir + "/consistency_report.json"
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Logf("WARN: could not write report to %s: %v", path, err)
		} else {
			t.Logf("report written to %s", path)
		}
	}

	// Fail test if any checks failed
	for _, r := range report.Results {
		if !r.Passed {
			t.Errorf("consistency check failed: %s — %s", r.Check, r.Message)
		}
	}
}

// TestConsistency_StuckTransfers is a focused check for stuck transfers.
func TestConsistency_StuckTransfers(t *testing.T) {
	skipIfNoGateway(t)

	cc := newConsistencyChecker()
	result := cc.checkNoStuckTransfers()

	if !result.Passed {
		t.Errorf("stuck transfers detected: %s", result.Message)
	} else {
		t.Logf("no stuck transfers: %s", result.Message)
	}
}

// TestConsistency_DepositIntegrity checks all deposit sessions have valid states.
func TestConsistency_DepositIntegrity(t *testing.T) {
	skipIfNoGateway(t)

	cc := newConsistencyChecker()
	result := cc.checkDepositSessionIntegrity()

	if !result.Passed {
		t.Errorf("deposit integrity issue: %s", result.Message)
	} else {
		t.Logf("deposit integrity OK: %s", result.Message)
	}
}

// TestConsistency_BankDepositIntegrity checks all bank deposit sessions have valid states.
func TestConsistency_BankDepositIntegrity(t *testing.T) {
	skipIfNoGateway(t)

	cc := newConsistencyChecker()
	result := cc.checkBankDepositSessionIntegrity()

	if !result.Passed {
		t.Errorf("bank deposit integrity issue: %s", result.Message)
	} else {
		t.Logf("bank deposit integrity OK: %s", result.Message)
	}
}

func countPassed(results []ConsistencyResult) int {
	n := 0
	for _, r := range results {
		if r.Passed {
			n++
		}
	}
	return n
}
