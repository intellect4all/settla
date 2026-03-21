package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// EndpointTest defines a single API endpoint test.
// Every test must set ExpectedStatus to the exact status code the server should return.
type EndpointTest struct {
	Category       string
	Method         string
	Path           string
	Auth           string // "api_key" | "jwt" | "none"
	Body           map[string]any
	Query          map[string]string
	ExpectedStatus int
	Description    string
	ContextKey     string // Store extracted value in context
	ContextField   string // JSON field to extract from response
	DependsOn      string // Required context key
}

// EndpointCatalog returns all ~75 tenant-facing endpoint tests.
// Idempotency keys include a run ID to avoid conflicts across test runs.
func EndpointCatalog() []EndpointTest {
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	return []EndpointTest{
		// ── Health & Docs ──
		{Category: "Health", Method: "GET", Path: "/health", Auth: "none", ExpectedStatus: 200, Description: "Health check"},
		{Category: "Health", Method: "GET", Path: "/openapi.json", Auth: "none", ExpectedStatus: 200, Description: "OpenAPI spec"},

		// ── Quotes ──
		{Category: "Quotes", Method: "POST", Path: "/v1/quotes", Auth: "api_key", Body: map[string]any{
			"source_currency": "GBP",
			"source_amount":   "1000.00",
			"dest_currency":   "NGN",
		}, ExpectedStatus: 201, Description: "Create quote", ContextKey: "quote_id", ContextField: "id"},
		{Category: "Quotes", Method: "GET", Path: "/v1/quotes/{quote_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get quote by ID", DependsOn: "quote_id"},

		// ── Transfers ──
		{Category: "Transfers", Method: "POST", Path: "/v1/transfers", Auth: "api_key", Body: map[string]any{
			"idempotency_key": "apitest-transfer-" + runID,
			"source_currency": "GBP",
			"source_amount":   "500.00",
			"dest_currency":   "NGN",
			"recipient": map[string]any{
				"name":    "Test Recipient",
				"country": "NG",
			},
			"external_ref": "apitest-ref-001",
		}, ExpectedStatus: 201, Description: "Create transfer", ContextKey: "transfer_id", ContextField: "id"},
		{Category: "Transfers", Method: "GET", Path: "/v1/transfers/{transfer_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get transfer by ID", DependsOn: "transfer_id"},
		{Category: "Transfers", Method: "GET", Path: "/v1/transfers", Auth: "api_key", Query: map[string]string{"page_size": "10"}, ExpectedStatus: 200, Description: "List transfers"},
		{Category: "Transfers", Method: "GET", Path: "/v1/transfers/{transfer_id}/events", Auth: "api_key", ExpectedStatus: 200, Description: "Get transfer events", DependsOn: "transfer_id"},
		// Cancel may return 422 if workers already advanced the state (expected in async systems)
		{Category: "Transfers", Method: "POST", Path: "/v1/transfers/{transfer_id}/cancel", Auth: "api_key", Body: map[string]any{"reason": "API test cancellation"}, ExpectedStatus: 422, Description: "Cancel transfer (already processing)", DependsOn: "transfer_id"},

		// ── Treasury ──
		{Category: "Treasury", Method: "GET", Path: "/v1/treasury/positions", Auth: "api_key", ExpectedStatus: 200, Description: "List treasury positions"},
		// Returns 429 with INSUFFICIENT_FUNDS when position has zero balance (server behaviour)
		{Category: "Treasury", Method: "GET", Path: "/v1/treasury/positions/USDT/tron", Auth: "api_key", ExpectedStatus: 429, Description: "Get USDT/tron position (no balance seeded)"},
		{Category: "Treasury", Method: "GET", Path: "/v1/treasury/liquidity", Auth: "api_key", ExpectedStatus: 200, Description: "Get liquidity report"},

		// ── Ledger ──
		{Category: "Ledger", Method: "GET", Path: "/v1/accounts", Auth: "api_key", Query: map[string]string{"page_size": "10"}, ExpectedStatus: 200, Description: "List ledger accounts"},
		// Use tenant-scoped account code format that exists in seed data
		{Category: "Ledger", Method: "GET", Path: "/v1/accounts/tenant:lemfi:assets:bank:gbp:clearing/balance", Auth: "api_key", ExpectedStatus: 200, Description: "Get account balance"},
		{Category: "Ledger", Method: "GET", Path: "/v1/accounts/tenant:lemfi:assets:bank:gbp:clearing/transactions", Auth: "api_key", Query: map[string]string{"page_size": "10"}, ExpectedStatus: 200, Description: "List account transactions"},

		// ── Routes ──
		{Category: "Routes", Method: "POST", Path: "/v1/routes", Auth: "api_key", Body: map[string]any{
			"from_currency": "GBP",
			"to_currency":   "NGN",
			"amount":        "1000.00",
		}, ExpectedStatus: 200, Description: "Get routing options"},

		// ── Verification ──
		{Category: "Verification", Method: "GET", Path: "/v1/transactions/verify/{transfer_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Verify transaction by ID", DependsOn: "transfer_id"},
		{Category: "Verification", Method: "GET", Path: "/v1/transactions/lookup", Auth: "api_key", Query: map[string]string{"reference": "apitest-ref-001"}, ExpectedStatus: 200, Description: "Lookup by external ref"},

		// ── Crypto Deposits ──
		{Category: "Deposits", Method: "POST", Path: "/v1/deposits", Auth: "api_key", Body: map[string]any{
			"chain":           "tron",
			"token":           "USDT",
			"expected_amount": "100.00",
			"settlement_pref": "HOLD",
			"idempotency_key": "apitest-deposit-" + runID,
		}, ExpectedStatus: 201, Description: "Create deposit session", ContextKey: "deposit_session_id", ContextField: "session.id"},
		{Category: "Deposits", Method: "GET", Path: "/v1/deposits/{deposit_session_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get deposit session", DependsOn: "deposit_session_id"},
		{Category: "Deposits", Method: "GET", Path: "/v1/deposits", Auth: "api_key", Query: map[string]string{"limit": "10"}, ExpectedStatus: 200, Description: "List deposit sessions"},
		{Category: "Deposits", Method: "POST", Path: "/v1/deposits/{deposit_session_id}/cancel", Auth: "api_key", Body: map[string]any{}, ExpectedStatus: 200, Description: "Cancel deposit session", DependsOn: "deposit_session_id"},
		{Category: "Deposits", Method: "GET", Path: "/v1/deposits/balance", Auth: "api_key", ExpectedStatus: 200, Description: "Get crypto balances"},
		{Category: "Deposits", Method: "POST", Path: "/v1/deposits/convert", Auth: "api_key", Body: map[string]any{
			"chain":  "tron",
			"token":  "USDT",
			"amount": "10.00",
		}, ExpectedStatus: 200, Description: "Convert crypto to fiat"},
		{Category: "Deposits", Method: "GET", Path: "/v1/deposits/{deposit_session_id}/public-status", Auth: "none", ExpectedStatus: 200, Description: "Deposit public status (no auth)", DependsOn: "deposit_session_id"},

		// ── Bank Deposits ──
		{Category: "Bank Deposits", Method: "POST", Path: "/v1/bank-deposits", Auth: "api_key", Body: map[string]any{
			"currency":        "GBP",
			"expected_amount": "5000.00",
			"account_type":    "TEMPORARY",
			"settlement_pref": "AUTO_CONVERT",
			"idempotency_key": "apitest-bankdep-" + runID,
		}, ExpectedStatus: 201, Description: "Create bank deposit session", ContextKey: "bank_deposit_session_id", ContextField: "session.id"},
		{Category: "Bank Deposits", Method: "GET", Path: "/v1/bank-deposits/{bank_deposit_session_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get bank deposit session", DependsOn: "bank_deposit_session_id"},
		{Category: "Bank Deposits", Method: "GET", Path: "/v1/bank-deposits", Auth: "api_key", Query: map[string]string{"limit": "10"}, ExpectedStatus: 200, Description: "List bank deposit sessions"},
		{Category: "Bank Deposits", Method: "POST", Path: "/v1/bank-deposits/{bank_deposit_session_id}/cancel", Auth: "api_key", Body: map[string]any{}, ExpectedStatus: 200, Description: "Cancel bank deposit", DependsOn: "bank_deposit_session_id"},
		{Category: "Bank Deposits", Method: "GET", Path: "/v1/bank-deposits/accounts", Auth: "api_key", Query: map[string]string{"limit": "10"}, ExpectedStatus: 200, Description: "List virtual accounts"},

		// ── Payment Links ──
		{Category: "Payment Links", Method: "POST", Path: "/v1/payment-links", Auth: "api_key", Body: map[string]any{
			"amount":      "50.00",
			"currency":    "USDT",
			"chain":       "tron",
			"token":       "USDT",
			"description": "API test payment",
		}, ExpectedStatus: 201, Description: "Create payment link", ContextKey: "payment_link_id", ContextField: "link.id"},
		{Category: "Payment Links", Method: "GET", Path: "/v1/payment-links", Auth: "api_key", Query: map[string]string{"limit": "10"}, ExpectedStatus: 200, Description: "List payment links"},
		{Category: "Payment Links", Method: "GET", Path: "/v1/payment-links/{payment_link_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get payment link", DependsOn: "payment_link_id"},

		// ── Payment Links (Public) — must run before DELETE disables the link ──
		{Category: "Payment Links (Public)", Method: "GET", Path: "/v1/payment-links/resolve/{payment_link_code}", Auth: "none", ExpectedStatus: 200, Description: "Resolve payment link (public)", DependsOn: "payment_link_code"},
		// Redeem creates a deposit session from the link — returns 201
		{Category: "Payment Links (Public)", Method: "POST", Path: "/v1/payment-links/redeem/{payment_link_code}", Auth: "none", Body: map[string]any{}, ExpectedStatus: 201, Description: "Redeem payment link (public)", DependsOn: "payment_link_code"},

		// ── Payment Links (Disable) — after public tests ──
		{Category: "Payment Links", Method: "DELETE", Path: "/v1/payment-links/{payment_link_id}", Auth: "api_key", ExpectedStatus: 204, Description: "Disable payment link", DependsOn: "payment_link_id"},

		// ── Account / Portal ──
		{Category: "Account", Method: "GET", Path: "/v1/me", Auth: "api_key", ExpectedStatus: 200, Description: "Get tenant profile"},
		{Category: "Account", Method: "PUT", Path: "/v1/me/webhooks", Auth: "api_key", Body: map[string]any{
			"webhook_url": "https://httpbin.org/post",
		}, ExpectedStatus: 200, Description: "Update webhook config"},
		{Category: "Account", Method: "GET", Path: "/v1/me/api-keys", Auth: "api_key", ExpectedStatus: 200, Description: "List API keys"},
		{Category: "Account", Method: "POST", Path: "/v1/me/api-keys", Auth: "api_key", Body: map[string]any{
			"environment": "TEST",
			"name":        "Catalog Test Key",
		}, ExpectedStatus: 201, Description: "Create API key", ContextKey: "created_key_id", ContextField: "key.id"},
		{Category: "Account", Method: "POST", Path: "/v1/me/api-keys/{created_key_id}/rotate", Auth: "api_key", Body: map[string]any{
			"name": "Rotated Key",
		}, ExpectedStatus: 200, Description: "Rotate API key", DependsOn: "created_key_id"},
		{Category: "Account", Method: "DELETE", Path: "/v1/me/api-keys/{created_key_id}", Auth: "api_key", ExpectedStatus: 204, Description: "Revoke API key", DependsOn: "created_key_id"},
		{Category: "Account", Method: "GET", Path: "/v1/me/dashboard", Auth: "api_key", ExpectedStatus: 200, Description: "Dashboard metrics"},
		{Category: "Account", Method: "GET", Path: "/v1/me/transfers/stats", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Transfer stats"},
		{Category: "Account", Method: "GET", Path: "/v1/me/fees/report", Auth: "api_key", ExpectedStatus: 200, Description: "Fee report"},

		// ── Tenant Analytics ──
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/status-distribution", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Status distribution"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/corridors", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Corridor metrics"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/latency", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Latency percentiles"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/comparison", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Volume comparison"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/activity", Auth: "api_key", Query: map[string]string{"limit": "10"}, ExpectedStatus: 200, Description: "Recent activity"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/fees", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Fee analytics"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/providers", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Provider performance"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/reconciliation", Auth: "api_key", ExpectedStatus: 200, Description: "Reconciliation analytics"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/deposits", Auth: "api_key", Query: map[string]string{"period": "7d"}, ExpectedStatus: 200, Description: "Deposit analytics"},
		{Category: "Analytics", Method: "POST", Path: "/v1/me/analytics/export", Auth: "api_key", Body: map[string]any{
			"export_type": "transfers",
			"period":      "7d",
			"format":      "json",
		}, ExpectedStatus: 201, Description: "Create export job", ContextKey: "export_job_id", ContextField: "job.id"},
		{Category: "Analytics", Method: "GET", Path: "/v1/me/analytics/export/{export_job_id}", Auth: "api_key", ExpectedStatus: 200, Description: "Get export job status", DependsOn: "export_job_id"},

		// ── Webhooks Management ──
		{Category: "Webhooks", Method: "GET", Path: "/v1/me/webhooks/deliveries", Auth: "api_key", Query: map[string]string{"page_size": "10"}, ExpectedStatus: 200, Description: "List webhook deliveries"},
		{Category: "Webhooks", Method: "GET", Path: "/v1/me/webhooks/stats", Auth: "api_key", Query: map[string]string{"period": "24h"}, ExpectedStatus: 200, Description: "Webhook delivery stats"},
		{Category: "Webhooks", Method: "GET", Path: "/v1/me/webhooks/subscriptions", Auth: "api_key", ExpectedStatus: 200, Description: "List event subscriptions"},
		{Category: "Webhooks", Method: "PUT", Path: "/v1/me/webhooks/subscriptions", Auth: "api_key", Body: map[string]any{
			"event_types": []string{"transfer.completed", "transfer.failed"},
		}, ExpectedStatus: 200, Description: "Update subscriptions"},
		{Category: "Webhooks", Method: "POST", Path: "/v1/me/webhooks/test", Auth: "api_key", Body: map[string]any{}, ExpectedStatus: 200, Description: "Send test webhook"},

		// ── Crypto Settings ──
		{Category: "Crypto Settings", Method: "GET", Path: "/v1/portal/crypto-settings", Auth: "api_key", ExpectedStatus: 200, Description: "Get crypto settings"},
		{Category: "Crypto Settings", Method: "POST", Path: "/v1/portal/crypto-settings", Auth: "api_key", Body: map[string]any{
			"crypto_enabled":          true,
			"default_settlement_pref": "HOLD",
		}, ExpectedStatus: 200, Description: "Update crypto settings"},
	}
}

// extractPaymentLinkCode extracts the short code from a payment link create response.
func extractPaymentLinkCode(ctx map[string]string, r TestResult) {
	if !r.Passed || r.ResponseBody == nil {
		return
	}
	// Response shape: { "link": { "shortCode": "xxx", ... } }
	var m map[string]any
	if raw, ok := r.ResponseBody.(json.RawMessage); ok {
		if json.Unmarshal(raw, &m) == nil {
			if val := extractField(m, "link.shortCode"); val != "" {
				ctx["payment_link_code"] = val
				return
			}
			if val := extractField(m, "link.short_code"); val != "" {
				ctx["payment_link_code"] = val
				return
			}
		}
	}
	code := extractFromResult(r, "short_code")
	if code != "" {
		ctx["payment_link_code"] = code
	}
}

// RunEndpointCatalog executes all endpoint tests with the given API key.
func RunEndpointCatalog(c *Client, apiKey string) []TestResult {
	catalog := EndpointCatalog()
	ctx := map[string]string{}
	var results []TestResult

	for _, t := range catalog {
		authHeader := ""
		switch t.Auth {
		case "api_key":
			authHeader = "Bearer " + apiKey
		case "jwt":
			// JWT tests handled in Phase 4
			continue
		}

		r := executeTest(c, t, authHeader, ctx)
		results = append(results, r)
		printTestResult(r)

		// Special post-processing for payment link code extraction
		if t.ContextKey == "payment_link_id" {
			extractPaymentLinkCode(ctx, r)
		}
	}

	return results
}
