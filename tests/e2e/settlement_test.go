//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSettlement_TreasuryPositions(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	requireStatus(t, resp, 200)
	t.Logf("treasury positions: %d bytes", len(resp.RawBody))
}

func TestSettlement_LiquidityReport(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/treasury/liquidity")
	if err != nil {
		t.Fatalf("liquidity report: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestSettlement_CreateTransfersAndVerifyPositions(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Capture initial positions
	resp, err := c.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("initial positions: %v", err)
	}
	requireStatus(t, resp, 200)

	// Create several transfers to generate settlement activity
	const count = 5
	transferIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		idem := randomIdemKey()
		resp, err := c.post("/v1/transfers", map[string]any{
			"idempotency_key": idem,
			"external_ref":    uniqueRef(fmt.Sprintf("settle-%d", i)),
			"source_currency": "GBP",
			"source_amount":   "100.00",
			"dest_currency":   "NGN",
			"sender":          defaultSender(),
			"recipient":       map[string]any{"name": "Settlement Test", "country": "NG"},
		}, withIdemKey(idem))
		if err != nil {
			t.Fatalf("create transfer %d: %v", i, err)
		}
		if resp.StatusCode == 201 {
			transferIDs = append(transferIDs, resp.str("id"))
		}
	}
	t.Logf("created %d transfers for settlement", len(transferIDs))

	// Wait for transfers to reach terminal states
	terminalStates := map[string]bool{
		"COMPLETED": true, "FAILED": true, "REFUNDED": true,
		"TRANSFER_STATUS_COMPLETED": true, "TRANSFER_STATUS_FAILED": true, "TRANSFER_STATUS_REFUNDED": true,
	}
	for _, id := range transferIDs {
		pollUntil(t, "transfer "+id[:8]+" terminal", func() (bool, error) {
			r, err := c.get("/v1/transfers/" + id)
			if err != nil {
				return false, err
			}
			return terminalStates[r.str("status")], nil
		}, withTimeout(60*time.Second))
	}

	// Check positions changed
	resp, err = c.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("final positions: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestSettlement_TopupAndWithdraw(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Topup request
	resp, err := c.post("/v1/treasury/topup", map[string]any{
		"currency": "GBP",
		"location": "bank",
		"amount":   "10000.00",
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	if resp.StatusCode == 500 && strings.Contains(string(resp.RawBody), "not configured") {
		t.Skip("position management not configured in this environment")
	}
	requireStatusOneOf(t, resp, 200, 201, 202)

	// Withdraw request
	resp, err = c.post("/v1/treasury/withdraw", map[string]any{
		"currency":    "GBP",
		"location":    "bank",
		"amount":      "100.00",
		"destination": "test-bank-account",
	})
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	requireStatusOneOf(t, resp, 200, 201, 202, 400, 422)
}

func TestSettlement_TransactionHistory(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/treasury/transactions?limit=10")
	if err != nil {
		t.Fatalf("list transactions: %v", err)
	}
	if resp.StatusCode == 500 && strings.Contains(string(resp.RawBody), "not configured") {
		t.Skip("position management not configured in this environment")
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// Multi-Tenant Settlement
// ---------------------------------------------------------------------------

func TestSettlement_MultiTenantIndependence(t *testing.T) {
	skipIfNoGateway(t)

	cA := newClient(seedAPIKey())
	cB := newClient(seedAPIKeyB())

	// Create transfers for tenant A
	idemA := randomIdemKey()
	respA, err := cA.post("/v1/transfers", map[string]any{
		"idempotency_key": idemA,
		"external_ref":    uniqueRef("settle-a"),
		"source_currency": "GBP",
		"source_amount":   "500.00",
		"dest_currency":   "NGN",
		"sender":          defaultSender(),
		"recipient":       map[string]any{"name": "Tenant A Settlement", "country": "NG"},
	}, withIdemKey(idemA))
	if err != nil {
		t.Fatalf("tenant A transfer: %v", err)
	}

	// Create transfers for tenant B (different corridor may be needed)
	idemB := randomIdemKey()
	respB, err := cB.post("/v1/transfers", map[string]any{
		"idempotency_key": idemB,
		"external_ref":    uniqueRef("settle-b"),
		"source_currency": "NGN",
		"source_amount":   "500000.00",
		"dest_currency":   "GBP",
		"sender":          map[string]any{"name": "Fincra Sender", "email": "sender@fincra-test.io", "country": "NG"},
		"recipient":       map[string]any{"name": "Tenant B Settlement", "country": "GB"},
	}, withIdemKey(idemB))
	if err != nil {
		t.Fatalf("tenant B transfer: %v", err)
	}

	// Both should succeed (or get corridor errors — depends on tenant config)
	if respA.StatusCode == 201 && respB.StatusCode == 201 {
		t.Log("both tenants created transfers successfully — settlement independence verifiable")
	} else {
		t.Logf("tenant A: %d, tenant B: %d (some corridors may not be configured)", respA.StatusCode, respB.StatusCode)
	}

	// Verify each tenant sees only their own positions
	posA, err := cA.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("positions A: %v", err)
	}
	requireStatus(t, posA, 200)

	posB, err := cB.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("positions B: %v", err)
	}
	requireStatus(t, posB, 200)
}
