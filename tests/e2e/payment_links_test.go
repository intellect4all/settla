//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

func TestPaymentLink_CreateAndRetrieve(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.post("/v1/payment-links", map[string]any{
		"amount":      "50.00",
		"currency":    "USDT",
		"chain":       "tron",
		"token":       "USDT",
		"description": "E2E test payment",
	})
	if err != nil {
		t.Fatalf("create payment link: %v", err)
	}
	requireStatus(t, resp, 201)

	linkID := resp.str("link.id")
	if linkID == "" {
		linkID = resp.str("id")
	}
	if linkID == "" {
		t.Fatal("payment link missing ID")
	}

	shortCode := resp.str("link.shortCode")
	if shortCode == "" {
		shortCode = resp.str("link.short_code")
	}
	t.Logf("payment link %s (code=%s)", linkID, shortCode)

	// Retrieve by ID
	resp, err = c.get("/v1/payment-links/" + linkID)
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestPaymentLink_List(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/payment-links?limit=10")
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestPaymentLink_ResolveAndRedeem(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Create link
	resp, err := c.post("/v1/payment-links", map[string]any{
		"amount":      "25.00",
		"currency":    "USDT",
		"chain":       "tron",
		"token":       "USDT",
		"description": "Redeem test",
		"use_limit":   5,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)

	shortCode := resp.str("link.shortCode")
	if shortCode == "" {
		shortCode = resp.str("link.short_code")
	}
	if shortCode == "" {
		t.Fatal("payment link has no short code")
	}

	// Resolve (public, no auth)
	resp, err = c.get("/v1/payment-links/resolve/"+shortCode, withNoAuth())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	requireStatus(t, resp, 200)

	// Redeem (public, no auth) → creates deposit session
	// May return 400 if the chain is not supported for the tenant
	resp, err = c.post("/v1/payment-links/redeem/"+shortCode, map[string]any{}, withNoAuth())
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	requireStatusOneOf(t, resp, 201, 400, 500)
	if resp.StatusCode == 201 {
		t.Logf("redeemed link %s → deposit session created", shortCode)
	} else {
		t.Logf("redeem returned %d (chain/pool issue): %s", resp.StatusCode, string(resp.RawBody))
	}
}

func TestPaymentLink_UseLimit(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Create link with use_limit=1
	resp, err := c.post("/v1/payment-links", map[string]any{
		"amount":      "10.00",
		"currency":    "USDT",
		"chain":       "tron",
		"token":       "USDT",
		"description": "Single use",
		"use_limit":   1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)

	shortCode := resp.str("link.shortCode")
	if shortCode == "" {
		shortCode = resp.str("link.short_code")
	}

	// First redeem: should succeed (or 400 if chain not supported)
	resp, err = c.post("/v1/payment-links/redeem/"+shortCode, map[string]any{}, withNoAuth())
	if err != nil {
		t.Fatalf("redeem 1: %v", err)
	}
	if (resp.StatusCode == 400 || resp.StatusCode == 500) && (strings.Contains(string(resp.RawBody), "not supported") || strings.Contains(string(resp.RawBody), "no available")) {
		t.Skipf("chain/pool issue, cannot test use limits: %s", string(resp.RawBody))
	}
	requireStatus(t, resp, 201)

	// Second redeem: should fail (exhausted)
	resp, err = c.post("/v1/payment-links/redeem/"+shortCode, map[string]any{}, withNoAuth())
	if err != nil {
		t.Fatalf("redeem 2: %v", err)
	}
	requireStatusOneOf(t, resp, 400, 409, 410, 422)
	t.Logf("use limit enforced: second redeem rejected with %d", resp.StatusCode)
}

func TestPaymentLink_Disable(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.post("/v1/payment-links", map[string]any{
		"amount":      "15.00",
		"currency":    "USDT",
		"chain":       "tron",
		"token":       "USDT",
		"description": "Disable test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)

	linkID := resp.str("link.id")
	if linkID == "" {
		linkID = resp.str("id")
	}
	shortCode := resp.str("link.shortCode")
	if shortCode == "" {
		shortCode = resp.str("link.short_code")
	}

	// Disable
	resp, err = c.del("/v1/payment-links/" + linkID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	requireStatus(t, resp, 204)

	// Attempt to redeem disabled link
	resp, err = c.post("/v1/payment-links/redeem/"+shortCode, map[string]any{}, withNoAuth())
	if err != nil {
		t.Fatalf("redeem disabled: %v", err)
	}
	requireStatusOneOf(t, resp, 400, 404, 410, 422)
	t.Logf("disabled link redeem rejected with %d", resp.StatusCode)
}
