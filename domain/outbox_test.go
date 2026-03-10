package domain

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestNewOutboxEvent(t *testing.T) {
	aggregateID := uuid.New()
	tenantID := uuid.New()
	payload := []byte(`{"transfer_id":"abc"}`)

	entry := NewOutboxEvent("transfer", aggregateID, tenantID, EventTransferCreated, payload)

	if entry.IsIntent {
		t.Error("expected IsIntent=false for event")
	}
	if entry.AggregateType != "transfer" {
		t.Errorf("expected AggregateType=transfer, got %s", entry.AggregateType)
	}
	if entry.AggregateID != aggregateID {
		t.Errorf("expected AggregateID=%s, got %s", aggregateID, entry.AggregateID)
	}
	if entry.TenantID != tenantID {
		t.Errorf("expected TenantID=%s, got %s", tenantID, entry.TenantID)
	}
	if entry.EventType != EventTransferCreated {
		t.Errorf("expected EventType=%s, got %s", EventTransferCreated, entry.EventType)
	}
	if entry.Published {
		t.Error("expected Published=false for new entry")
	}
	if entry.PublishedAt != nil {
		t.Error("expected PublishedAt=nil for new entry")
	}
	if entry.RetryCount != 0 {
		t.Errorf("expected RetryCount=0, got %d", entry.RetryCount)
	}
	if entry.MaxRetries != 5 {
		t.Errorf("expected MaxRetries=5, got %d", entry.MaxRetries)
	}
	if entry.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
	if entry.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestNewOutboxIntent(t *testing.T) {
	aggregateID := uuid.New()
	tenantID := uuid.New()
	payload := []byte(`{"amount":"100.00"}`)

	entry := NewOutboxIntent("transfer", aggregateID, tenantID, IntentTreasuryReserve, payload)

	if !entry.IsIntent {
		t.Error("expected IsIntent=true for intent")
	}
	if entry.AggregateType != "transfer" {
		t.Errorf("expected AggregateType=transfer, got %s", entry.AggregateType)
	}
	if entry.EventType != IntentTreasuryReserve {
		t.Errorf("expected EventType=%s, got %s", IntentTreasuryReserve, entry.EventType)
	}
	if entry.Published {
		t.Error("expected Published=false for new intent")
	}
	if entry.MaxRetries != 5 {
		t.Errorf("expected MaxRetries=5, got %d", entry.MaxRetries)
	}
}

func TestTreasuryReservePayload_RoundTrip(t *testing.T) {
	original := TreasuryReservePayload{
		TransferID: uuid.New(),
		TenantID:   uuid.New(),
		Currency:   CurrencyNGN,
		Amount:     decimal.NewFromFloat(50000.50),
		Location:   "bank:gtbank:ngn",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded TreasuryReservePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.TransferID != original.TransferID {
		t.Errorf("TransferID mismatch: %s != %s", decoded.TransferID, original.TransferID)
	}
	if decoded.Currency != original.Currency {
		t.Errorf("Currency mismatch: %s != %s", decoded.Currency, original.Currency)
	}
	if !decoded.Amount.Equal(original.Amount) {
		t.Errorf("Amount mismatch: %s != %s", decoded.Amount, original.Amount)
	}
	if decoded.Location != original.Location {
		t.Errorf("Location mismatch: %s != %s", decoded.Location, original.Location)
	}
}

func TestTreasuryReleasePayload_RoundTrip(t *testing.T) {
	original := TreasuryReleasePayload{
		TransferID: uuid.New(),
		TenantID:   uuid.New(),
		Currency:   CurrencyGBP,
		Amount:     decimal.NewFromFloat(1234.56),
		Location:   "bank:barclays:gbp",
		Reason:     "settlement_failure",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded TreasuryReleasePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.TransferID != original.TransferID {
		t.Errorf("TransferID mismatch")
	}
	if !decoded.Amount.Equal(original.Amount) {
		t.Errorf("Amount mismatch: %s != %s", decoded.Amount, original.Amount)
	}
	if decoded.Reason != original.Reason {
		t.Errorf("Reason mismatch: %s != %s", decoded.Reason, original.Reason)
	}
}

func TestProviderOnRampPayload_RoundTrip(t *testing.T) {
	original := ProviderOnRampPayload{
		TransferID:   uuid.New(),
		TenantID:     uuid.New(),
		ProviderID:   "moonpay",
		Amount:       decimal.NewFromFloat(10000),
		FromCurrency: CurrencyNGN,
		ToCurrency:   CurrencyUSDT,
		Reference:    "ref-123",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ProviderOnRampPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ProviderID != original.ProviderID {
		t.Errorf("ProviderID mismatch: %s != %s", decoded.ProviderID, original.ProviderID)
	}
	if decoded.FromCurrency != original.FromCurrency {
		t.Errorf("FromCurrency mismatch")
	}
	if !decoded.Amount.Equal(original.Amount) {
		t.Errorf("Amount mismatch")
	}
}

func TestProviderOffRampPayload_RoundTrip(t *testing.T) {
	original := ProviderOffRampPayload{
		TransferID:   uuid.New(),
		TenantID:     uuid.New(),
		ProviderID:   "yellowcard",
		Amount:       decimal.NewFromFloat(500),
		FromCurrency: CurrencyUSDT,
		ToCurrency:   CurrencyGBP,
		Recipient: Recipient{
			Name:          "John Doe",
			AccountNumber: "12345678",
			SortCode:      "12-34-56",
			BankName:      "Barclays",
			Country:       "GB",
		},
		Reference: "ref-456",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ProviderOffRampPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Recipient.Name != original.Recipient.Name {
		t.Errorf("Recipient.Name mismatch")
	}
	if decoded.Recipient.AccountNumber != original.Recipient.AccountNumber {
		t.Errorf("Recipient.AccountNumber mismatch")
	}
}

func TestLedgerPostPayload_RoundTrip(t *testing.T) {
	original := LedgerPostPayload{
		TransferID:     uuid.New(),
		TenantID:       uuid.New(),
		IdempotencyKey: "ledger-post-abc",
		Description:    "On-ramp settlement",
		ReferenceType:  "transfer",
		Lines: []LedgerLineEntry{
			{AccountCode: "tenant:lemfi:assets:bank:ngn:clearing", EntryType: "DEBIT", Amount: decimal.NewFromFloat(50000), Currency: "NGN", Description: "Debit source"},
			{AccountCode: "tenant:lemfi:liabilities:ngn", EntryType: "CREDIT", Amount: decimal.NewFromFloat(50000), Currency: "NGN", Description: "Credit liability"},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded LedgerPostPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(decoded.Lines))
	}
	if decoded.Lines[0].AccountCode != original.Lines[0].AccountCode {
		t.Errorf("Lines[0].AccountCode mismatch")
	}
	if !decoded.Lines[0].Amount.Equal(original.Lines[0].Amount) {
		t.Errorf("Lines[0].Amount mismatch")
	}
}

func TestBlockchainSendPayload_RoundTrip(t *testing.T) {
	original := BlockchainSendPayload{
		TransferID: uuid.New(),
		TenantID:   uuid.New(),
		Chain:      "tron",
		From:       "TAddr1",
		To:         "TAddr2",
		Token:      "USDT",
		Amount:     decimal.NewFromFloat(100),
		Memo:       "settlement",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BlockchainSendPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Chain != original.Chain {
		t.Errorf("Chain mismatch: %s != %s", decoded.Chain, original.Chain)
	}
	if decoded.Token != original.Token {
		t.Errorf("Token mismatch")
	}
	if !decoded.Amount.Equal(original.Amount) {
		t.Errorf("Amount mismatch")
	}
}

func TestWebhookDeliverPayload_RoundTrip(t *testing.T) {
	original := WebhookDeliverPayload{
		TransferID: uuid.New(),
		TenantID:   uuid.New(),
		EventType:  EventTransferCompleted,
		Data:       []byte(`{"status":"COMPLETED"}`),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded WebhookDeliverPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.EventType != original.EventType {
		t.Errorf("EventType mismatch: %s != %s", decoded.EventType, original.EventType)
	}
	if string(decoded.Data) != string(original.Data) {
		t.Errorf("Data mismatch")
	}
}
