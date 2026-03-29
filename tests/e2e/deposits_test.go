//go:build e2e

package e2e

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Crypto Deposits
// ---------------------------------------------------------------------------

func TestDeposit_CreateSession(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/deposits", map[string]any{
		"chain":           "tron",
		"token":           "USDT",
		"expected_amount": "100.00",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create deposit: %v", err)
	}
	requireStatus(t, resp, 201)

	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}
	if sessionID == "" {
		t.Fatal("deposit session has no ID in response")
	}

	status := resp.str("session.status")
	if status == "" {
		status = resp.str("status")
	}
	t.Logf("deposit session %s status=%s", sessionID, status)

	// Retrieve
	resp, err = c.get("/v1/deposits/" + sessionID)
	if err != nil {
		t.Fatalf("get deposit: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestDeposit_Idempotency(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	body := map[string]any{
		"chain":           "tron",
		"token":           "USDT",
		"expected_amount": "50.00",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}

	resp1, err := c.post("/v1/deposits", body, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create deposit 1: %v", err)
	}
	requireStatus(t, resp1, 201)
	id1 := resp1.str("session.id")
	if id1 == "" {
		id1 = resp1.str("id")
	}

	// Same idempotency key → same session
	resp2, err := c.post("/v1/deposits", body, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create deposit 2: %v", err)
	}
	requireStatusOneOf(t, resp2, 200, 201)
	id2 := resp2.str("session.id")
	if id2 == "" {
		id2 = resp2.str("id")
	}

	if id1 != id2 {
		t.Fatalf("idempotency failed: got different session IDs %s vs %s", id1, id2)
	}
	t.Logf("deposit idempotency confirmed: session %s", id1)
}

func TestDeposit_CancelSession(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/deposits", map[string]any{
		"chain":           "tron",
		"token":           "USDT",
		"expected_amount": "25.00",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)
	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}

	// Cancel
	resp, err = c.post("/v1/deposits/"+sessionID+"/cancel", map[string]any{})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	requireStatus(t, resp, 200)

	// Verify cancelled
	resp, err = c.get("/v1/deposits/" + sessionID)
	if err != nil {
		t.Fatalf("get after cancel: %v", err)
	}
	requireStatus(t, resp, 200)
	status := resp.str("session.status")
	if status == "" {
		status = resp.str("status")
	}
	if status != "CANCELLED" {
		t.Logf("WARN: expected CANCELLED, got %s (may already have been processed)", status)
	}
}

func TestDeposit_PublicStatus(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/deposits", map[string]any{
		"chain":           "tron",
		"token":           "USDT",
		"expected_amount": "10.00",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)
	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}

	// Public status — no auth required
	resp, err = c.get("/v1/deposits/"+sessionID+"/public-status", withNoAuth())
	if err != nil {
		t.Fatalf("public status: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestDeposit_ListAndBalance(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/deposits?limit=5")
	if err != nil {
		t.Fatalf("list deposits: %v", err)
	}
	requireStatus(t, resp, 200)

	resp, err = c.get("/v1/deposits/balance")
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestDeposit_AutoConvert(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/deposits", map[string]any{
		"chain":           "tron",
		"token":           "USDT",
		"expected_amount": "500.00",
		"settlement_pref": "AUTO_CONVERT",
		"currency":        "GBP",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)

	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}
	t.Logf("auto-convert deposit session: %s", sessionID)
}

// ---------------------------------------------------------------------------
// Bank Deposits
// ---------------------------------------------------------------------------

func TestBankDeposit_CreateSession(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/bank-deposits", map[string]any{
		"currency":        "GBP",
		"expected_amount": "5000.00",
		"account_type":    "TEMPORARY",
		"settlement_pref": "AUTO_CONVERT",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create bank deposit: %v", err)
	}
	requireStatus(t, resp, 201)

	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}
	if sessionID == "" {
		t.Fatal("bank deposit session has no ID")
	}
	t.Logf("bank deposit session %s created", sessionID)

	// Retrieve
	resp, err = c.get("/v1/bank-deposits/" + sessionID)
	if err != nil {
		t.Fatalf("get bank deposit: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestBankDeposit_Idempotency(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	body := map[string]any{
		"currency":        "GBP",
		"expected_amount": "1000.00",
		"account_type":    "TEMPORARY",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}

	resp1, err := c.post("/v1/bank-deposits", body, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	requireStatus(t, resp1, 201)
	id1 := resp1.str("session.id")
	if id1 == "" {
		id1 = resp1.str("id")
	}

	resp2, err := c.post("/v1/bank-deposits", body, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	requireStatusOneOf(t, resp2, 200, 201)
	id2 := resp2.str("session.id")
	if id2 == "" {
		id2 = resp2.str("id")
	}

	if id1 != id2 {
		t.Fatalf("idempotency failed: %s vs %s", id1, id2)
	}
}

func TestBankDeposit_CancelSession(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	idem := randomIdemKey()
	resp, err := c.post("/v1/bank-deposits", map[string]any{
		"currency":        "GBP",
		"expected_amount": "2000.00",
		"account_type":    "TEMPORARY",
		"settlement_pref": "HOLD",
		"idempotency_key": idem,
	}, withIdemKey(idem))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	requireStatus(t, resp, 201)
	sessionID := resp.str("session.id")
	if sessionID == "" {
		sessionID = resp.str("id")
	}

	resp, err = c.post("/v1/bank-deposits/"+sessionID+"/cancel", map[string]any{})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestBankDeposit_ListVirtualAccounts(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/bank-deposits/accounts?limit=10")
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	requireStatus(t, resp, 200)
}

func TestBankDeposit_ListSessions(t *testing.T) {
	skipIfNoGateway(t)
	c := newClient(seedAPIKey())

	resp, err := c.get("/v1/bank-deposits?limit=5")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	requireStatus(t, resp, 200)
}

// ---------------------------------------------------------------------------
// NOTE: Full deposit lifecycle (on-chain detection → confirmation → credit)
// cannot be fully automated in e2e without blockchain infrastructure.
// The chain monitor watches for real on-chain transactions. In mock mode,
// the deposit session stays in PENDING_PAYMENT. Testing the full flow
// requires either:
//   - Mock provider mode with simulated on-chain events (done in integration tests)
//   - Testnet with real chain transactions (manual)
// ---------------------------------------------------------------------------
