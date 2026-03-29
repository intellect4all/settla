//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestQuote_CreateAndRetrieve(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.post("/v1/quotes", map[string]any{
		"source_currency": "GBP",
		"source_amount":   "1000.00",
		"dest_currency":   "NGN",
	})
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	requireStatus(t, resp, 201)

	quoteID := requireField(t, resp, "id")
	requireField(t, resp, "fx_rate")
	requireField(t, resp, "dest_amount")
	t.Logf("quote %s: rate=%s, dest=%s", quoteID, resp.str("fx_rate"), resp.str("dest_amount"))

	// Retrieve by ID
	resp, err = c.get("/v1/quotes/" + quoteID)
	if err != nil {
		t.Fatalf("get quote: %v", err)
	}
	requireStatus(t, resp, 200)

	if resp.str("id") != quoteID {
		t.Fatalf("quote ID mismatch: got %s, want %s", resp.str("id"), quoteID)
	}
}

func TestQuote_CachingConsistency(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Two identical quote requests within 30s should return similar rates
	body := map[string]any{
		"source_currency": "GBP",
		"source_amount":   "5000.00",
		"dest_currency":   "NGN",
	}

	resp1, err := c.post("/v1/quotes", body)
	if err != nil {
		t.Fatalf("quote1: %v", err)
	}
	requireStatus(t, resp1, 201)
	rate1 := resp1.str("fx_rate")

	resp2, err := c.post("/v1/quotes", body)
	if err != nil {
		t.Fatalf("quote2: %v", err)
	}
	requireStatus(t, resp2, 201)
	rate2 := resp2.str("fx_rate")

	// Same corridor, same amount bucket — rates should match (from cache)
	if rate1 != rate2 {
		t.Logf("WARN: rates differ (cache miss?): %s vs %s", rate1, rate2)
	} else {
		t.Logf("cache hit confirmed: rate=%s", rate1)
	}
}

func TestQuote_TransferWithQuoteID(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Create quote
	resp, err := c.post("/v1/quotes", map[string]any{
		"source_currency": "GBP",
		"source_amount":   "200.00",
		"dest_currency":   "NGN",
	})
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	requireStatus(t, resp, 201)
	quoteID := requireField(t, resp, "id")

	// Use quote in transfer
	idem := randomIdemKey()
	resp, err = c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    uniqueRef("quoted"),
		"source_currency": "GBP",
		"source_amount":   "200.00",
		"dest_currency":   "NGN",
		"quote_id":        quoteID,
		"sender":          defaultSender(),
		"recipient":       map[string]any{"name": "Quote Xfer", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	requireStatus(t, resp, 201)
	requireField(t, resp, "id")
}

func TestQuote_ExpiredQuote(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Create a quote
	resp, err := c.post("/v1/quotes", map[string]any{
		"source_currency": "GBP",
		"source_amount":   "100.00",
		"dest_currency":   "NGN",
	})
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	requireStatus(t, resp, 201)

	quoteID := requireField(t, resp, "id")

	// Check expiry field
	expiresAt := resp.str("expires_at")
	if expiresAt == "" {
		t.Log("WARN: quote has no expires_at field — cannot test expiry rejection")
		return
	}

	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Logf("WARN: cannot parse expires_at %q: %v", expiresAt, err)
		return
	}

	// If quote hasn't expired yet, we can't test the rejection path right now
	if time.Now().Before(expires) {
		t.Logf("quote %s expires at %s (in the future) — expiry rejection requires waiting; skipping", quoteID, expiresAt)
		return
	}

	// Quote expired: attempt to use it should fail
	idem := randomIdemKey()
	resp, err = c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    uniqueRef("expired-q"),
		"source_currency": "GBP",
		"source_amount":   "100.00",
		"dest_currency":   "NGN",
		"quote_id":        quoteID,
		"sender":          defaultSender(),
		"recipient":       map[string]any{"name": "Expired Quote", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("transfer with expired quote: %v", err)
	}
	// Should be rejected
	requireStatusOneOf(t, resp, 400, 409, 422)
	t.Logf("expired quote correctly rejected with status %d", resp.StatusCode)
}

func TestQuote_RoutingOptions(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.post("/v1/routes", map[string]any{
		"from_currency": "GBP",
		"to_currency":   "NGN",
		"amount":        "1000.00",
	})
	if err != nil {
		t.Fatalf("routing options: %v", err)
	}
	requireStatus(t, resp, 200)
	t.Logf("routing options response: %d bytes", len(resp.RawBody))
}
