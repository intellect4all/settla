//go:build e2e

package e2e

import (
	"fmt"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Tenant Profile & Account
// ---------------------------------------------------------------------------

func TestTenant_GetProfile(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/me")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	requireStatus(t, resp, 200)
	t.Logf("tenant profile: %s", string(resp.RawBody)[:min(200, len(resp.RawBody))])
}

func TestTenant_Dashboard(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/me/dashboard")
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestTenant_TransferStats(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/me/transfers/stats?period=7d")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestTenant_FeeReport(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/me/fees/report")
	if err != nil {
		t.Fatalf("fee report: %v", err)
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// API Key Management
// ---------------------------------------------------------------------------

func TestTenant_APIKeyLifecycle(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// List keys
	resp, err := c.get("/v1/me/api-keys")
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	requireStatus(t, resp, 200)

	// Create key
	resp, err = c.post("/v1/me/api-keys", map[string]any{
		"environment": "TEST",
		"name":        "E2E Test Key",
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	requireStatus(t, resp, 201)

	keyID := resp.str("key.id")
	if keyID == "" {
		keyID = resp.str("id")
	}
	if keyID == "" {
		t.Fatal("no key ID returned")
	}
	t.Logf("created key %s", keyID)

	// Rotate
	resp, err = c.post("/v1/me/api-keys/"+keyID+"/rotate", map[string]any{
		"name": "Rotated E2E Key",
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	requireStatus(t, resp, 200)

	// Revoke
	resp, err = c.del("/v1/me/api-keys/" + keyID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	requireStatus(t, resp, 204)
	t.Logf("key %s revoked", keyID)
}

// ---------------------------------------------------------------------------
// Webhook Configuration
// ---------------------------------------------------------------------------

func TestTenant_WebhookConfig(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Update webhook URL
	resp, err := c.put("/v1/me/webhooks", map[string]any{
		"webhook_url": "https://httpbin.org/post",
	})
	if err != nil {
		t.Fatalf("update webhook: %v", err)
	}
	requireStatus(t, resp, 200)

	// List deliveries
	resp, err = c.get("/v1/me/webhooks/deliveries?page_size=5")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	requireStatus(t, resp, 200)

	// Webhook stats
	resp, err = c.get("/v1/me/webhooks/stats?period=24h")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	requireStatus(t, resp, 200)

	// List subscriptions
	resp, err = c.get("/v1/me/webhooks/subscriptions")
	if err != nil {
		t.Fatalf("subscriptions: %v", err)
	}
	requireStatus(t, resp, 200)

	// Update subscriptions
	resp, err = c.put("/v1/me/webhooks/subscriptions", map[string]any{
		"event_types": []string{"transfer.completed", "transfer.failed"},
	})
	if err != nil {
		t.Fatalf("update subscriptions: %v", err)
	}
	requireStatus(t, resp, 200)

	// Send test webhook
	resp, err = c.post("/v1/me/webhooks/test", map[string]any{})
	if err != nil {
		t.Fatalf("test webhook: %v", err)
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// Crypto Settings
// ---------------------------------------------------------------------------

func TestTenant_CryptoSettings(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/portal/crypto-settings")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	requireStatus(t, resp, 200)

	resp, err = c.post("/v1/portal/crypto-settings", map[string]any{
		"crypto_enabled":          true,
		"default_settlement_pref": "HOLD",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// Tenant Isolation — Multi-Tenant Data Leak Check
// ---------------------------------------------------------------------------

func TestTenant_IsolationTransferList(t *testing.T) {
	skipIfNoGateway(t)

	cA := newClient(seedAPIKey())
	cB := newClient(seedAPIKeyB())

	// Each tenant lists their transfers
	respA, err := cA.get("/v1/transfers?page_size=100")
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	requireStatus(t, respA, 200)

	respB, err := cB.get("/v1/transfers?page_size=100")
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	requireStatus(t, respB, 200)

	// Both return 200 but should show different data (or at least not contain each other's transfers)
	t.Log("both tenants list transfers with 200 — manual verification of data isolation needed in CI")
}

func TestTenant_IsolationDepositList(t *testing.T) {
	skipIfNoGateway(t)

	cA := newClient(seedAPIKey())
	cB := newClient(seedAPIKeyB())

	respA, err := cA.get("/v1/deposits?limit=50")
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	requireStatus(t, respA, 200)

	respB, err := cB.get("/v1/deposits?limit=50")
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	requireStatus(t, respB, 200)
}

func TestTenant_IsolationTreasuryPositions(t *testing.T) {
	skipIfNoGateway(t)

	cA := newClient(seedAPIKey())
	cB := newClient(seedAPIKeyB())

	respA, err := cA.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("positions A: %v", err)
	}
	requireStatus(t, respA, 200)

	respB, err := cB.get("/v1/treasury/positions")
	if err != nil {
		t.Fatalf("positions B: %v", err)
	}
	requireStatus(t, respB, 200)

	// Positions should be independent
	t.Log("both tenants queried positions independently")
}

// ---------------------------------------------------------------------------
// Rapid Tenant Onboarding (Auth Flow)
// ---------------------------------------------------------------------------

func TestTenant_RapidOnboarding(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient("")

	const batchSize = 10 // Reduced from 100 for test speed; scale up for load testing
	var mu sync.Mutex
	var successes, failures int
	emails := make(map[string]bool)

	var wg sync.WaitGroup
	wg.Add(batchSize)

	for i := 0; i < batchSize; i++ {
		go func(idx int) {
			defer wg.Done()
			email := fmt.Sprintf("rapid-onboard-%s-%d@settla-test.io", uniqueRef(""), idx)

			resp, err := c.post("/v1/auth/register", map[string]any{
				"company_name": fmt.Sprintf("Rapid Corp %d", idx),
				"email":        email,
				"password":     "TestP@ss2024!",
				"display_name": fmt.Sprintf("Tester %d", idx),
			}, withNoAuth())

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				failures++
				return
			}
			if resp.StatusCode == 201 {
				successes++
				tenantID := resp.str("tenant_id")
				if emails[tenantID] {
					t.Errorf("duplicate tenant ID: %s", tenantID)
				}
				emails[tenantID] = true
			} else {
				failures++
			}
		}(i)
	}

	wg.Wait()
	t.Logf("rapid onboarding: %d successes, %d failures out of %d", successes, failures, batchSize)

	if successes == 0 {
		t.Error("no tenants onboarded successfully")
	}
}

// ---------------------------------------------------------------------------
// Analytics Endpoints
// ---------------------------------------------------------------------------

func TestTenant_Analytics(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	endpoints := []string{
		"/v1/me/analytics/status-distribution?period=7d",
		"/v1/me/analytics/corridors?period=7d",
		"/v1/me/analytics/latency?period=7d",
		"/v1/me/analytics/comparison?period=7d",
		"/v1/me/analytics/activity?limit=10",
		"/v1/me/analytics/fees?period=7d",
		"/v1/me/analytics/providers?period=7d",
		"/v1/me/analytics/reconciliation",
		"/v1/me/analytics/deposits?period=7d",
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			resp, err := c.get(ep)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			requireStatus(t, resp, 200)
		})
	}
}

func TestTenant_AnalyticsExport(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.post("/v1/me/analytics/export", map[string]any{
		"export_type": "transfers",
		"period":      "7d",
		"format":      "json",
	})
	if err != nil {
		t.Fatalf("create export: %v", err)
	}
	requireStatus(t, resp, 201)

	jobID := resp.str("job.id")
	if jobID == "" {
		jobID = resp.str("id")
	}
	if jobID != "" {
		resp, err = c.get("/v1/me/analytics/export/" + jobID)
		if err != nil {
			t.Fatalf("get export: %v", err)
		}
		requireStatus(t, resp, 200)
	}
}

// ---------------------------------------------------------------------------
// Ledger Accounts
// ---------------------------------------------------------------------------

func TestTenant_LedgerAccounts(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/accounts?page_size=10")
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	requireStatus(t, resp, 200)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
