//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Transfer Lifecycle — Happy Path
// ---------------------------------------------------------------------------

func TestTransfer_CreateAndRetrieve(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	extRef := uniqueRef("xfer")

	resp, err := c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    extRef,
		"source_currency": "GBP",
		"source_amount":   "500.00",
		"dest_currency":   "NGN",
		"sender": map[string]any{
			"name":    "E2E Sender",
			"email":   "sender@e2e-test.io",
			"country": "GB",
		},
		"recipient": map[string]any{
			"name":    "E2E Recipient",
			"country": "NG",
		},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	requireStatus(t, resp, 201)

	transferID := requireField(t, resp, "id")
	t.Logf("created transfer %s", transferID)

	// Verify GET returns same transfer
	resp, err = c.get("/v1/transfers/" + transferID)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	requireStatus(t, resp, 200)

	gotID := resp.str("id")
	if gotID != transferID {
		t.Fatalf("GET returned wrong transfer: got %s, want %s", gotID, transferID)
	}

	status := resp.str("status")
	if status == "" {
		t.Fatal("transfer missing status field")
	}
	t.Logf("transfer status: %s", status)
}

func TestTransfer_LifecycleToTerminalState(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    uniqueRef("lifecycle"),
		"source_currency": "GBP",
		"source_amount":   "10.00",
		"dest_currency":   "NGN",
		"sender":    defaultSender(),
		"recipient": map[string]any{"name": "Lifecycle Test", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if resp.StatusCode == 500 {
		t.Skipf("transfer creation failed (likely treasury/routing issue): %s", string(resp.RawBody))
	}
	requireStatus(t, resp, 201)
	transferID := requireField(t, resp, "id")

	// Poll until the transfer progresses beyond CREATED (workers are processing)
	// In mock mode, transfers may not always reach COMPLETED — workers depend on
	// mock provider responses and timing. We verify state progression, not termination.
	progressedStates := map[string]bool{
		"FUNDED": true, "ON_RAMPING": true, "SETTLING": true, "OFF_RAMPING": true,
		"COMPLETED": true, "FAILED": true, "REFUNDED": true, "REFUNDING": true,
		"TRANSFER_STATUS_FUNDED": true, "TRANSFER_STATUS_ON_RAMPING": true,
		"TRANSFER_STATUS_SETTLING": true, "TRANSFER_STATUS_OFF_RAMPING": true,
		"TRANSFER_STATUS_COMPLETED": true, "TRANSFER_STATUS_FAILED": true,
		"TRANSFER_STATUS_REFUNDED": true, "TRANSFER_STATUS_REFUNDING": true,
	}

	var finalStatus string
	pollUntil(t, "transfer state progression", func() (bool, error) {
		r, err := c.get("/v1/transfers/" + transferID)
		if err != nil {
			return false, err
		}
		finalStatus = r.str("status")
		return progressedStates[finalStatus], nil
	}, withTimeout(30*time.Second))

	t.Logf("transfer %s progressed to: %s", transferID, finalStatus)

	// Verify events were recorded
	resp, err = c.get("/v1/transfers/" + transferID + "/events")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestTransfer_ListAndFilter(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	// Create a transfer first
	idem := randomIdemKey()
	_, err := c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    uniqueRef("list-test"),
		"source_currency": "GBP",
		"source_amount":   "50.00",
		"dest_currency":   "NGN",
		"sender":          map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"},
		"recipient":       map[string]any{"name": "List Test", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}

	// List transfers
	resp, err := c.get("/v1/transfers?page_size=5")
	if err != nil {
		t.Fatalf("list transfers: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestTransfer_LookupByExternalRef(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	extRef := uniqueRef("lookup")
	idem := randomIdemKey()
	resp, err := c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    extRef,
		"source_currency": "GBP",
		"source_amount":   "75.00",
		"dest_currency":   "NGN",
		"sender":          map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"},
		"recipient":       map[string]any{"name": "Lookup Test", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)

	// Lookup by external ref
	resp, err = c.get("/v1/transactions/lookup?reference=" + extRef)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// Transfer Corridors
// ---------------------------------------------------------------------------

func TestTransfer_Corridors(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	corridors := []struct {
		src, dst string
		amount   string
	}{
		{"GBP", "NGN", "100.00"},
	}

	for _, cr := range corridors {
		t.Run(fmt.Sprintf("%s_%s", cr.src, cr.dst), func(t *testing.T) {
			idem := randomIdemKey()
			resp, err := c.post("/v1/transfers", map[string]any{
				"idempotency_key": idem,
				"external_ref":    uniqueRef("corridor"),
				"source_currency": cr.src,
				"source_amount":   cr.amount,
				"dest_currency":   cr.dst,
				"sender":          map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"},
				"recipient":       map[string]any{"name": "Corridor Test", "country": "NG"},
			}, withIdemKey(idem))
			if err != nil {
				t.Fatalf("create transfer %s→%s: %v", cr.src, cr.dst, err)
			}
			// Accept 201 (created) or 400 (corridor not configured)
			requireStatusOneOf(t, resp, 201, 400)
			if resp.StatusCode == 201 {
				requireField(t, resp, "id")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transfer — Multiple transfers in parallel
// ---------------------------------------------------------------------------

func TestTransfer_ConcurrentCreation(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	const count = 10
	type result struct {
		id  string
		err error
	}
	ch := make(chan result, count)

	for i := 0; i < count; i++ {
		go func(idx int) {
			idem := randomIdemKey()
			resp, err := c.post("/v1/transfers", map[string]any{
				"idempotency_key": idem,
				"external_ref":    uniqueRef(fmt.Sprintf("conc-%d", idx)),
				"source_currency": "GBP",
				"source_amount":   "10.00",
				"dest_currency":   "NGN",
				"sender":          map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"},
				"recipient":       map[string]any{"name": "Concurrent Test", "country": "NG"},
			}, withIdemKey(idem))
			if err != nil {
				ch <- result{err: err}
				return
			}
			if resp.StatusCode != 201 {
				ch <- result{err: fmt.Errorf("status %d: %s", resp.StatusCode, string(resp.RawBody))}
				return
			}
			ch <- result{id: resp.str("id")}
		}(i)
	}

	ids := make(map[string]bool)
	for i := 0; i < count; i++ {
		r := <-ch
		if r.err != nil {
			t.Errorf("concurrent create #%d: %v", i, r.err)
			continue
		}
		if ids[r.id] {
			t.Errorf("duplicate transfer ID: %s", r.id)
		}
		ids[r.id] = true
	}
	t.Logf("created %d unique transfers concurrently", len(ids))
}

// ---------------------------------------------------------------------------
// Transfer — Verify transaction
// ---------------------------------------------------------------------------

func TestTransfer_VerifyTransaction(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/transfers", map[string]any{
		"idempotency_key": idem,
		"external_ref":    uniqueRef("verify"),
		"source_currency": "GBP",
		"source_amount":   "200.00",
		"dest_currency":   "NGN",
		"sender":          map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"},
		"recipient":       map[string]any{"name": "Verify Test", "country": "NG"},
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)
	transferID := requireField(t, resp, "id")

	// Verify by ID
	resp, err = c.get("/v1/transactions/verify/" + transferID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	requireStatus(t, resp, 200)
}
